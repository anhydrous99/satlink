package ppp

import (
	"bytes"
	"fmt"
	"slices"
	"time"
)

// The option-negotiation automaton LCP and IPCP share (RFC 1661 §4),
// trimmed to the states of an actively opened point-to-point link.

// codeRejectCopyLimit is the longest copy of an unrecognised packet echoed
// back in a Code-Reject; small enough to fit any MRU the peer can have
// negotiated.
const codeRejectCopyLimit = 64

// maxRepeatedRequests is how many retransmitted Configure-Requests are
// answered again while Opened before falling back to the RFC's
// renegotiation.
const maxRepeatedRequests = 3

// ownRequestLoopbackLimit is how many of our own Configure-Requests may come
// back before the line is called looped.
const ownRequestLoopbackLimit = 3

// stalledReason is why a negotiation fails once the peer has answered our
// request more often than the Nak tolerance allows without naming anything
// we could change. An empty Nak or Reject, or one naming only options we
// never proposed, would otherwise draw the same request again forever.
const stalledReason = "the peer keeps refusing our request without naming a change we can make"

// verdictKind classifies a verdict.
type verdictKind int

// The answers to one proposed option.
const (
	verdictAck verdictKind = iota
	// verdictNak means acceptable only with the verdict's suggestion
	// instead.
	verdictNak
	verdictReject
)

// verdict is an answer to one option the peer proposed.
type verdict struct {
	kind verdictKind
	// suggestion is the value that would be acceptable instead
	// (verdictNak).
	suggestion []byte
}

// ackVerdict accepts an option as proposed.
func ackVerdict() verdict {
	return verdict{kind: verdictAck}
}

// nakVerdict accepts an option only with suggestion as its value.
func nakVerdict(suggestion []byte) verdict {
	return verdict{kind: verdictNak, suggestion: suggestion}
}

// rejectVerdict refuses an option outright.
func rejectVerdict() verdict {
	return verdict{kind: verdictReject}
}

// negotiator is the protocol-specific half of a negotiation: which options
// to propose and which of the peer's proposals to accept.
type negotiator interface {
	// ownOptions returns the options for our next Configure-Request.
	ownOptions() []configOption
	// applyNak adopts the other values the peer proposed for some of our
	// options, or says why we cannot.
	applyNak(suggestions []configOption) error
	// applyReject drops the options the peer refused, or says why we
	// cannot do without them.
	applyReject(rejected []configOption) error
	// judge answers one option of the peer's Configure-Request.
	judge(option configOption) verdict
	// acceptPeerOptions records the options of a Configure-Request we
	// acknowledged.
	acceptPeerOptions(options []configOption)
}

// automatonState is a state of RFC 1661's automaton (§4.2), trimmed to
// those of an actively opened link.
type automatonState int

// The automaton's states.
const (
	stateClosed automatonState = iota
	stateRequestSent
	stateAckReceived
	stateAckSent
	stateOpened
	stateClosing
)

// transitionKind classifies a transition.
type transitionKind int

// The events an automaton reports to its owner.
const (
	// transitionNone asks the owner for nothing.
	transitionNone transitionKind = iota
	// transitionUp means that negotiation completed (This-Layer-Up).
	transitionUp
	// transitionDown means that the layer left the Opened state
	// (This-Layer-Down).
	transitionDown
	// transitionFinished means that the automaton stopped and will not
	// restart by itself (This-Layer-Finished).
	transitionFinished
)

// transition is what the owner of an automaton must react to.
type transition struct {
	kind transitionKind
	// finish is why the automaton stopped (transitionFinished).
	finish finish
}

// The transitions without a payload. They are never assigned to.
var (
	noTransition = transition{kind: transitionNone}
	layerUp      = transition{kind: transitionUp}
	layerDown    = transition{kind: transitionDown}
)

// layerFinished reports that the automaton stopped, and why.
func layerFinished(why finish) transition {
	return transition{kind: transitionFinished, finish: why}
}

// leaving returns This-Layer-Down when previous, the state the automaton
// has just left, was Opened, and no transition otherwise.
func leaving(previous automatonState) transition {
	if previous == stateOpened {
		return layerDown
	}
	return noTransition
}

// finishKind classifies a finish.
type finishKind int

// The reasons an automaton stops.
const (
	// finishClosed means that our own close completed.
	finishClosed finishKind = iota
	// finishTerminatedByPeer means that the peer terminated the layer.
	finishTerminatedByPeer
	// finishTimedOut means that the peer never answered our requests
	// (Max-Configure exceeded).
	finishTimedOut
	// finishFailed means that the two sides could not agree.
	finishFailed
	// finishLoopedBack means that our own requests came back: something
	// echoes the line.
	finishLoopedBack
)

// finish is why an automaton stopped.
type finish struct {
	kind finishKind
	// reason is what went wrong (finishFailed).
	reason string
}

// automaton negotiates the options of one protocol, LCP or IPCP, for which
// its negotiator decides what to propose and what to accept.
type automaton[N negotiator] struct {
	protocol   uint16
	negotiator N
	timers     Timers
	state      automatonState
	// request is our latest Configure-Request, kept for retransmission and
	// to validate the answer; nil before the first.
	request *packet
	// requestAnswered is set once the request has been acknowledged, so
	// that duplicate answers are ignored. A Nak or Reject leaves no request
	// to mark: a fresh one replaces it at once, or the negotiation ends.
	requestAnswered bool
	lastIdentifier  uint8
	restartCounter  int
	naksSent        int
	naksReceived    int
	// fruitlessAnswers counts the Naks and Rejects that left our options as
	// they were: each drew the same request again.
	fruitlessAnswers int
	// nakTolerance is how many Naks in a row are accepted before further
	// ones count as Rejects, and how many fruitless answers before the
	// negotiation fails, so that a peer cannot stall negotiation forever.
	nakTolerance int
	// ackedRequest is the peer's request we last acknowledged, nil before
	// the first; repeatsAnswered counts how often it was repeated since.
	ackedRequest        *packet
	repeatsAnswered     int
	ownRequestsReceived int
	// deadline is when the restart timer expires; the zero Time while it
	// is not running.
	deadline time.Time
}

// newAutomaton returns a closed automaton for protocol that accepts up to
// Max-Failure Naks in a row, and as many fruitless answers.
func newAutomaton[N negotiator](protocol uint16, negotiator N, timers Timers) *automaton[N] {
	return &automaton[N]{
		protocol:     protocol,
		negotiator:   negotiator,
		timers:       timers,
		state:        stateClosed,
		nakTolerance: timers.MaxFailure,
	}
}

// toleratingNaks makes the automaton accept up to tolerance Naks in a row,
// and as many fruitless answers, and returns it.
func (a *automaton[N]) toleratingNaks(tolerance int) *automaton[N] {
	a.nakTolerance = tolerance
	return a
}

// isOpened reports whether negotiation has completed.
func (a *automaton[N]) isOpened() bool {
	return a.state == stateOpened
}

// nextTimeout returns when handleTimeout next needs to run; the zero Time
// when no timer is running.
func (a *automaton[N]) nextTimeout() time.Time {
	return a.deadline
}

// open starts negotiating (the Open event).
func (a *automaton[N]) open(now time.Time, out *outbox) {
	if a.state != stateClosed {
		return
	}
	a.naksSent = 0
	a.naksReceived = 0
	a.fruitlessAnswers = 0
	a.ownRequestsReceived = 0
	a.restart(now, out)
	a.state = stateRequestSent
}

// close asks the peer to close the layer (the Close event).
func (a *automaton[N]) close(now time.Time, out *outbox) transition {
	if a.state == stateClosed || a.state == stateClosing {
		return noTransition
	}
	previous := a.state
	a.restartCounter = a.timers.MaxTerminate
	a.sendTerminateRequest(now, out)
	a.state = stateClosing
	return leaving(previous)
}

// abandon stops without telling the peer, because a lower layer went away.
func (a *automaton[N]) abandon() {
	a.stop()
}

// receive handles one packet of the automaton's protocol.
func (a *automaton[N]) receive(received packet, now time.Time, out *outbox) transition {
	if !received.code.isNegotiation() {
		a.rejectCode(received, out)
		return noTransition
	}
	if a.state == stateClosed {
		a.answerWhileClosed(received, out)
		return noTransition
	}
	switch received.code {
	case codeConfigureRequest:
		return a.onConfigureRequest(received, now, out)
	case codeConfigureAck:
		return a.onConfigureAck(received)
	case codeConfigureNak:
		return a.onConfigureNak(received, now, out)
	case codeConfigureReject:
		return a.onConfigureReject(received, now, out)
	case codeTerminateRequest:
		return a.onTerminateRequest(received, out)
	case codeTerminateAck:
		return a.onTerminateAck(now, out)
	default: // codeCodeReject, the one negotiation code left
		return a.onCodeReject(received)
	}
}

// handleTimeout retransmits or gives up once the restart timer has
// expired.
func (a *automaton[N]) handleTimeout(now time.Time, out *outbox) transition {
	if !isDue(a.deadline, now) {
		return noTransition
	}
	if a.restartCounter == 0 {
		return a.giveUp()
	}
	switch a.state {
	case stateClosing:
		a.sendTerminateRequest(now, out)
	case stateRequestSent, stateAckSent:
		a.repeatConfigureRequest(now, out)
	case stateAckReceived:
		a.sendConfigureRequest(now, out)
		a.state = stateRequestSent
	default:
		// Entering Opened or Closed stops the timer. Should one still run,
		// stop it, rather than let it come due on every call.
		a.deadline = time.Time{}
	}
	return noTransition
}

// repeatConfigureRequest sends our request again once the restart timer
// has expired: unchanged while it awaits its answer, and with a new
// identifier once it has been answered (RFC 1661 §5.1). A Terminate-Ack in
// Ack-Received leaves it answered in Request-Sent, and in Ack-Sent once the
// peer's request is acknowledged before the restart. Answers to an
// answered request are ignored as duplicates, so repeating it unchanged
// could never bring the layer up.
func (a *automaton[N]) repeatConfigureRequest(now time.Time, out *outbox) {
	if a.requestAnswered {
		a.sendConfigureRequest(now, out)
		return
	}
	a.retransmitConfigureRequest(now, out)
}

// giveUp stops once the retries have run out: a close completes, a
// negotiation times out.
func (a *automaton[N]) giveUp() transition {
	why := finish{kind: finishTimedOut}
	if a.state == stateClosing {
		why = finish{kind: finishClosed}
	}
	a.stop()
	return layerFinished(why)
}

// answerWhileClosed answers configuration and termination traffic with
// Terminate-Ack, so that a confused peer learns we are not there (RFC 1661
// §4.1).
func (a *automaton[N]) answerWhileClosed(received packet, out *outbox) {
	switch received.code {
	case codeConfigureRequest, codeConfigureAck, codeConfigureNak, codeConfigureReject, codeTerminateRequest:
		a.sendTerminateAck(received.identifier, out)
	}
}

// onConfigureRequest answers the peer's Configure-Request.
func (a *automaton[N]) onConfigureRequest(request packet, now time.Time, out *outbox) transition {
	if a.state == stateClosing {
		return noTransition
	}
	if a.request != nil && a.request.equal(request) {
		return a.onOwnRequestReturned()
	}
	if a.state == stateOpened && a.isRepeatOfAckedRequest(request) {
		// The peer retransmitted before our Ack reached it, as happens on
		// long round trips: answer again instead of renegotiating.
		a.repeatsAnswered++
		out.push(a.protocol, ackFor(request))
		return noTransition
	}
	options, ok := parseOptionList(request.data)
	if !ok {
		return noTransition
	}
	return a.answerRequest(request, options, now, out)
}

// isRepeatOfAckedRequest reports whether request repeats the one we last
// acknowledged, and may still be answered again.
func (a *automaton[N]) isRepeatOfAckedRequest(request packet) bool {
	return a.repeatsAnswered < maxRepeatedRequests && a.ackedRequest != nil && a.ackedRequest.equal(request)
}

// onOwnRequestReturned counts one of our own Configure-Requests coming
// back: something echoes the line.
func (a *automaton[N]) onOwnRequestReturned() transition {
	a.ownRequestsReceived++
	if a.ownRequestsReceived < ownRequestLoopbackLimit {
		return noTransition
	}
	a.stop()
	return layerFinished(finish{kind: finishLoopedBack})
}

// answerRequest sends the answer to the peer's well-formed request and
// moves to the state it leads to.
func (a *automaton[N]) answerRequest(request packet, options []configOption, now time.Time, out *outbox) transition {
	answer := a.answer(request, options)
	a.record(request, options, answer.code)
	previous := a.state
	if previous == stateOpened {
		// The peer is renegotiating; our own request goes out first (RFC
		// 1661 §4.3).
		a.restart(now, out)
	}
	out.push(a.protocol, answer)
	a.state = stateAfterAnswering(previous, answer.code)
	if a.state == stateOpened {
		return a.enterOpened()
	}
	return leaving(previous)
}

// record keeps what answering request with answerCode commits us to.
func (a *automaton[N]) record(request packet, options []configOption, answerCode code) {
	switch answerCode {
	case codeConfigureAck:
		a.negotiator.acceptPeerOptions(options)
		a.ackedRequest = &request
		a.naksSent = 0
	case codeConfigureNak:
		a.naksSent++
	}
}

// stateAfterAnswering returns the state that answering the peer's request
// with answerCode leads to from current: RFC 1661's Receive-Configure-
// Request events, good (an Ack) or bad.
func stateAfterAnswering(current automatonState, answerCode code) automatonState {
	acknowledged := answerCode == codeConfigureAck
	switch {
	case current == stateAckReceived && acknowledged:
		return stateOpened
	case current == stateAckReceived:
		return stateAckReceived
	case acknowledged:
		return stateAckSent
	default:
		return stateRequestSent
	}
}

// enterOpened completes the negotiation.
func (a *automaton[N]) enterOpened() transition {
	a.state = stateOpened
	a.deadline = time.Time{}
	a.repeatsAnswered = 0
	a.naksReceived = 0
	a.fruitlessAnswers = 0
	return layerUp
}

// answer returns the Configure-Ack, -Nak or -Reject answering request (RFC
// 1661 §5.2–5.4).
func (a *automaton[N]) answer(request packet, options []configOption) packet {
	// After Max-Failure Naks, refuse instead of suggesting again (RFC 1661
	// §4.6).
	stopSuggesting := a.naksSent >= a.timers.MaxFailure
	var rejected, suggestions []configOption
	for _, option := range options {
		judged := a.negotiator.judge(option)
		switch {
		case judged.kind == verdictAck:
			// Accepted as proposed: the Ack repeats it.
		case judged.kind == verdictNak && !stopSuggesting:
			suggestions = suggestOnce(suggestions, configOption{kind: option.kind, value: judged.suggestion})
		default: // a Reject, or a Nak once we stopped suggesting
			rejected = append(rejected, option)
		}
	}
	switch {
	case len(rejected) > 0:
		return packet{code: codeConfigureReject, identifier: request.identifier, data: encodeOptionList(rejected)}
	case len(suggestions) > 0:
		return packet{code: codeConfigureNak, identifier: request.identifier, data: encodeOptionList(suggestions)}
	default:
		return ackFor(request)
	}
}

// suggestOnce adds suggestion to the options of a Configure-Nak, unless the
// Nak already suggests a value for that kind of option. Every option LCP
// and IPCP Nak takes one value (RFC 1661 §5.3), and a suggestion may be
// longer than the option it answers: a request repeating such an option
// would otherwise draw a Nak too large for any packet.
func suggestOnce(suggestions []configOption, suggestion configOption) []configOption {
	sameKind := func(suggested configOption) bool { return suggested.kind == suggestion.kind }
	if slices.ContainsFunc(suggestions, sameKind) {
		return suggestions
	}
	return append(suggestions, suggestion)
}

// onConfigureAck handles the peer's acknowledgement of our request. Only a
// request still awaiting its answer is acknowledged, so Ack-Received and
// Opened, whose request has been answered, never get this far: RFC 1661's
// Receive-Configure-Ack there is a duplicate, ignored like any other.
func (a *automaton[N]) onConfigureAck(ack packet) transition {
	if !a.isAnswerToRequest(ack) || !bytes.Equal(a.request.data, ack.data) {
		return noTransition
	}
	a.requestAnswered = true
	switch a.state {
	case stateRequestSent:
		a.restartCounter = a.timers.MaxConfigure
		a.state = stateAckReceived
	case stateAckSent:
		return a.enterOpened()
	}
	return noTransition // Closing: the close goes on
}

// onConfigureNak adopts the values the peer suggested for our options and
// asks again.
func (a *automaton[N]) onConfigureNak(nak packet, now time.Time, out *outbox) transition {
	options, ok := a.optionsAnsweringRequest(nak)
	if !ok {
		return noTransition
	}
	if err := a.adoptSuggestions(options); err != nil {
		return a.fail(err.Error())
	}
	return a.requestAgain(now, out)
}

// adoptSuggestions adopts the values a Nak suggests for our options. A
// peer that keeps suggesting values is treated as refusing them: beyond
// the Nak tolerance, the options count as rejected.
func (a *automaton[N]) adoptSuggestions(options []configOption) error {
	a.naksReceived++
	if a.naksReceived > a.nakTolerance {
		return a.negotiator.applyReject(options)
	}
	return a.negotiator.applyNak(options)
}

// onConfigureReject drops the options the peer refused and asks again.
func (a *automaton[N]) onConfigureReject(reject packet, now time.Time, out *outbox) transition {
	options, ok := a.optionsAnsweringRequest(reject)
	if !ok {
		return noTransition
	}
	if err := a.negotiator.applyReject(options); err != nil {
		return a.fail(err.Error())
	}
	return a.requestAgain(now, out)
}

// optionsAnsweringRequest returns the options of a Nak or Reject that
// answers our current request, and false for any other packet. Only a
// request still awaiting its answer can be answered, so the automaton is
// then in Request-Sent or Ack-Sent: Ack-Received and Opened, whose request
// has been answered, never get this far, as RFC 1661's
// Receive-Configure-Nak/Rej there is a duplicate, ignored like any other.
func (a *automaton[N]) optionsAnsweringRequest(answer packet) ([]configOption, bool) {
	if !a.isAnswerToRequest(answer) || a.state == stateClosing {
		return nil, false
	}
	return parseOptionList(answer.data)
}

// requestAgain replaces the request the peer answered with a fresh one, our
// options adjusted to its answer (RFC 1661's irc, scr). Request-Sent and
// Ack-Sent, the only states an answer reaches, stay as they are. An answer
// that left our options as they were is fruitless: beyond the Nak
// tolerance, the negotiation fails instead of asking the same again.
func (a *automaton[N]) requestAgain(now time.Time, out *outbox) transition {
	if bytes.Equal(encodeOptionList(a.negotiator.ownOptions()), a.request.data) {
		a.fruitlessAnswers++
		if a.fruitlessAnswers > a.nakTolerance {
			return a.fail(stalledReason)
		}
	}
	a.restart(now, out)
	return noTransition
}

// fail stops the automaton because the two sides cannot agree, for reason.
func (a *automaton[N]) fail(reason string) transition {
	a.stop()
	return layerFinished(finish{kind: finishFailed, reason: reason})
}

// onTerminateRequest acknowledges the peer's request to close the layer.
func (a *automaton[N]) onTerminateRequest(request packet, out *outbox) transition {
	a.sendTerminateAck(request.identifier, out)
	if a.state == stateClosing {
		return noTransition // both ends are closing; wait for the answer to ours
	}
	a.stop()
	return layerFinished(finish{kind: finishTerminatedByPeer})
}

// onTerminateAck completes our close, or restarts a negotiation the peer
// has just ended.
func (a *automaton[N]) onTerminateAck(now time.Time, out *outbox) transition {
	switch a.state {
	case stateClosing:
		a.stop()
		return layerFinished(finish{kind: finishClosed})
	case stateAckReceived:
		// The peer's Ack no longer holds. Nothing is sent now (RFC 1661
		// §4.1): the restart timeout sends a fresh request.
		a.state = stateRequestSent
	case stateOpened:
		a.restart(now, out)
		a.state = stateRequestSent
		return layerDown
	}
	return noTransition
}

// onCodeReject stops when the peer does not understand a code negotiation
// cannot do without.
func (a *automaton[N]) onCodeReject(reject packet) transition {
	if len(reject.data) == 0 {
		return noTransition
	}
	rejected := code(reject.data[0])
	if !rejected.isNegotiation() {
		return noTransition // e.g. an Echo-Request: we can live without it
	}
	return a.fail(fmt.Sprintf("the peer does not understand %v", rejected))
}

// rejectCode tells the peer that we do not understand the code of the
// packet received (RFC 1661 §5.6).
func (a *automaton[N]) rejectCode(received packet, out *outbox) {
	rejected := received.encode()
	rejected = rejected[:min(len(rejected), codeRejectCopyLimit)]
	out.push(a.protocol, packet{code: codeCodeReject, identifier: a.allocateIdentifier(), data: rejected})
}

// restart resets the retry budget and sends a fresh Configure-Request.
func (a *automaton[N]) restart(now time.Time, out *outbox) {
	a.restartCounter = a.timers.MaxConfigure
	a.sendConfigureRequest(now, out)
}

// sendConfigureRequest sends a Configure-Request with a new identifier.
func (a *automaton[N]) sendConfigureRequest(now time.Time, out *outbox) {
	request := packet{
		code:       codeConfigureRequest,
		identifier: a.allocateIdentifier(),
		data:       encodeOptionList(a.negotiator.ownOptions()),
	}
	out.push(a.protocol, request)
	a.request = &request
	a.requestAnswered = false
	a.countTransmission(now)
}

// retransmitConfigureRequest sends our request again. Retransmissions keep
// the identifier, so that a late answer to the first copy still counts.
func (a *automaton[N]) retransmitConfigureRequest(now time.Time, out *outbox) {
	if a.request != nil {
		out.push(a.protocol, *a.request)
	}
	a.countTransmission(now)
}

// sendTerminateRequest asks the peer to close the layer.
func (a *automaton[N]) sendTerminateRequest(now time.Time, out *outbox) {
	out.push(a.protocol, packet{code: codeTerminateRequest, identifier: a.allocateIdentifier()})
	a.countTransmission(now)
}

// sendTerminateAck acknowledges the peer's Terminate-Request, or tells it
// that we are closed.
func (a *automaton[N]) sendTerminateAck(identifier uint8, out *outbox) {
	out.push(a.protocol, packet{code: codeTerminateAck, identifier: identifier})
}

// countTransmission uses up one retry and restarts the timer: every
// transmission does.
func (a *automaton[N]) countTransmission(now time.Time) {
	a.restartCounter = max(a.restartCounter-1, 0)
	a.deadline = now.Add(a.timers.Restart)
}

// allocateIdentifier returns the identifier for our next packet, wrapping
// around after 255. The endpoint takes LCP's for the LCP packets it sends
// itself, so that one count numbers every LCP packet we originate.
func (a *automaton[N]) allocateIdentifier() uint8 {
	a.lastIdentifier++
	return a.lastIdentifier
}

// isAnswerToRequest reports whether answer answers our current request,
// which has not been answered yet.
func (a *automaton[N]) isAnswerToRequest(answer packet) bool {
	return !a.requestAnswered && a.request != nil && a.request.identifier == answer.identifier
}

// stop closes the automaton and its timer.
func (a *automaton[N]) stop() {
	a.state = stateClosed
	a.deadline = time.Time{}
}

// ackFor returns the Configure-Ack of request, which repeats its options
// exactly (RFC 1661 §5.2).
func ackFor(request packet) packet {
	return packet{code: codeConfigureAck, identifier: request.identifier, data: request.data}
}
