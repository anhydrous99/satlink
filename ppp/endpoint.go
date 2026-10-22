package ppp

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"time"
)

// The PPP endpoint: link phases, protocol dispatch, keepalive and the API
// the IP layer uses.

// Phase is the progress of the link (RFC 1661 §3.2).
type Phase int

// The link phases.
const (
	// PhaseDead means not started yet, or finished.
	PhaseDead Phase = iota
	// PhaseEstablish means LCP is negotiating the link.
	PhaseEstablish
	// PhaseAuthenticate means PAP or CHAP is running.
	PhaseAuthenticate
	// PhaseNetwork means IPCP is negotiating addresses.
	PhaseNetwork
	// PhaseOpen means IPv4 datagrams flow.
	PhaseOpen
	// PhaseTerminate means the link is closing.
	PhaseTerminate
)

// String returns the phase's name, such as "Open".
func (p Phase) String() string {
	switch p {
	case PhaseDead:
		return "Dead"
	case PhaseEstablish:
		return "Establish"
	case PhaseAuthenticate:
		return "Authenticate"
	case PhaseNetwork:
		return "Network"
	case PhaseOpen:
		return "Open"
	case PhaseTerminate:
		return "Terminate"
	default:
		return fmt.Sprintf("Phase(%d)", int(p))
	}
}

// IPv4Settings is the IPv4 addressing agreed through IPCP. It holds a
// slice, so it is not comparable with ==: use [IPv4Settings.Equal].
type IPv4Settings struct {
	// LocalAddress is our address on the link.
	LocalAddress netip.Addr
	// PeerAddress is the peer's address; the zero Addr when the peer
	// announced none.
	PeerAddress netip.Addr
	// DNSServers are the DNS servers the peer offered (RFC 1877), primary
	// first.
	DNSServers []netip.Addr
}

// Equal reports whether s and other hold the same addresses, the DNS
// servers in the same order.
func (s IPv4Settings) Equal(other IPv4Settings) bool {
	return s.LocalAddress == other.LocalAddress &&
		s.PeerAddress == other.PeerAddress &&
		slices.Equal(s.DNSServers, other.DNSServers)
}

// Event is something the IP layer must react to. Its IPv4 settings hold a
// slice, so it is not comparable with ==: use [Event.Equal].
type Event struct {
	// Kind says what happened.
	Kind EventKind
	// IPv4 is the addressing now in force (EventUp).
	IPv4 IPv4Settings
	// Reason is why the link went down (EventDown).
	Reason DownReason
}

// Equal reports whether e and other are the same event: the same kind,
// settings and reason.
func (e Event) Equal(other Event) bool {
	return e.Kind == other.Kind && e.Reason == other.Reason && e.IPv4.Equal(other.IPv4)
}

// EventKind says what an [Event] reports.
type EventKind int

// The link events.
const (
	// EventUp means IPv4 is up — again, after a renegotiation that may have
	// changed the addresses.
	EventUp EventKind = iota
	// EventDown means the link is down for good.
	EventDown
)

// String returns "Up" or "Down".
func (k EventKind) String() string {
	switch k {
	case EventUp:
		return "Up"
	case EventDown:
		return "Down"
	default:
		return fmt.Sprintf("EventKind(%d)", int(k))
	}
}

// DownReason is why a link went down. It is comparable with ==.
type DownReason struct {
	// Kind classifies the reason.
	Kind DownReasonKind
	// Protocol is "LCP" or "IPCP", the protocol that was negotiating
	// (DownReasonNegotiationTimedOut, DownReasonNegotiationFailed).
	Protocol string
	// Detail is what went wrong (DownReasonNegotiationFailed,
	// DownReasonAuthenticationFailed).
	Detail string
}

// DownReasonKind classifies a [DownReason].
type DownReasonKind int

// The reasons a link goes down.
const (
	// DownReasonClosed means [Endpoint.Close] was called.
	DownReasonClosed DownReasonKind = iota
	// DownReasonTerminatedByPeer means the peer terminated the link.
	DownReasonTerminatedByPeer
	// DownReasonNegotiationTimedOut means the peer stopped answering while
	// Protocol was negotiating.
	DownReasonNegotiationTimedOut
	// DownReasonNegotiationFailed means the two ends could not agree on
	// Protocol's options.
	DownReasonNegotiationFailed
	// DownReasonAuthenticationFailed means authentication failed, in either
	// direction.
	DownReasonAuthenticationFailed
	// DownReasonPeerUnresponsive means the peer stopped answering keepalive
	// probes.
	DownReasonPeerUnresponsive
	// DownReasonLoopedBack means our own packets came back: something
	// echoes the line.
	DownReasonLoopedBack
)

// String returns the kind's name, such as "TerminatedByPeer".
func (k DownReasonKind) String() string {
	switch k {
	case DownReasonClosed:
		return "Closed"
	case DownReasonTerminatedByPeer:
		return "TerminatedByPeer"
	case DownReasonNegotiationTimedOut:
		return "NegotiationTimedOut"
	case DownReasonNegotiationFailed:
		return "NegotiationFailed"
	case DownReasonAuthenticationFailed:
		return "AuthenticationFailed"
	case DownReasonPeerUnresponsive:
		return "PeerUnresponsive"
	case DownReasonLoopedBack:
		return "LoopedBack"
	default:
		return fmt.Sprintf("DownReasonKind(%d)", int(k))
	}
}

// String describes the reason, such as "LCP negotiation timed out".
func (r DownReason) String() string {
	switch r.Kind {
	case DownReasonClosed:
		return "closed locally"
	case DownReasonTerminatedByPeer:
		return "terminated by the peer"
	case DownReasonNegotiationTimedOut:
		return r.Protocol + " negotiation timed out"
	case DownReasonNegotiationFailed:
		return r.Protocol + " negotiation failed: " + r.Detail
	case DownReasonAuthenticationFailed:
		return "authentication failed: " + r.Detail
	case DownReasonPeerUnresponsive:
		return "the peer stopped responding"
	case DownReasonLoopedBack:
		return "the line is looped back"
	default:
		return r.Kind.String()
	}
}

// SendErrorKind classifies a [SendError]. Each kind is also a sentinel for
// errors.Is.
type SendErrorKind int

// The reasons a datagram cannot be sent.
const (
	// ErrNotOpen reports that IPv4 is not up.
	ErrNotOpen SendErrorKind = iota
	// ErrTooLarge reports a datagram larger than the peer's MRU; the
	// error's Len and MRU fields hold the sizes.
	ErrTooLarge
)

// Error returns the kind's label, such as "not open for IPv4".
func (k SendErrorKind) Error() string {
	switch k {
	case ErrNotOpen:
		return "not open for IPv4"
	case ErrTooLarge:
		return "datagram too large"
	default:
		return fmt.Sprintf("ppp.SendErrorKind(%d)", int(k))
	}
}

// SendError is why a datagram could not be sent.
type SendError struct {
	// Kind classifies the error.
	Kind SendErrorKind
	// Len is the size of the datagram (ErrTooLarge).
	Len int
	// MRU is the peer's MRU (ErrTooLarge).
	MRU int
}

// Error returns the message, such as "a 1501-byte datagram exceeds the
// peer's MRU of 1500".
func (e *SendError) Error() string {
	switch e.Kind {
	case ErrNotOpen:
		return "the link is not open for IPv4"
	case ErrTooLarge:
		return fmt.Sprintf("a %d-byte datagram exceeds the peer's MRU of %d", e.Len, e.MRU)
	default:
		return e.Kind.Error()
	}
}

// Is reports whether target is e's kind, so that errors.Is(err, ErrNotOpen)
// matches every *SendError of that kind.
func (e *SendError) Is(target error) bool {
	kind, ok := target.(SendErrorKind)
	return ok && kind == e.Kind
}

// Stats are an endpoint's traffic counters.
type Stats struct {
	// BytesReceived counts the raw bytes handed to HandleInput.
	BytesReceived uint64
	// BytesSent counts the raw bytes returned by TakeOutput.
	BytesSent uint64
	// FramesReceived counts well-formed frames received.
	FramesReceived uint64
	// FramesSent counts frames queued for sending.
	FramesSent uint64
	// InvalidFrames counts corrupted or malformed frames, and noise between
	// frames.
	InvalidFrames uint64
	// DiscardedPackets counts well-formed packets dropped because they made
	// no sense at the time.
	DiscardedPackets uint64
}

// maxQueuedDatagrams is how many received datagrams are held for the IP
// layer; beyond this the oldest is dropped.
const maxQueuedDatagrams = 64

// protocolRejectCopyLimit is how many bytes of an unsupported packet are
// copied into our Protocol-Reject.
const protocolRejectCopyLimit = 64

// ipcpNakTolerance is how many IPCP Naks are accepted in a row. Some
// servers stall with repeated Naks while they find an address (pppd
// tolerates 100), so allow more than for LCP.
const ipcpNakTolerance = 20

// The names DownReason gives the negotiated protocols.
const (
	lcpName  = "LCP"
	ipcpName = "IPCP"
)

// Endpoint is one end of a PPP link over a byte stream.
//
// The endpoint performs no I/O and never reads the clock. Its owner:
//  1. passes received bytes to [Endpoint.HandleInput],
//  2. calls [Endpoint.HandleTimeout] once [Endpoint.NextTimeout] has
//     passed,
//  3. writes out [Endpoint.TakeOutput], and
//  4. drains [Endpoint.PollEvent] and [Endpoint.ReceiveIPv4].
//
// Every now argument must come from one clock and never go backwards. An
// Endpoint is not safe for concurrent use, and must not be copied. The zero
// Endpoint is not usable; create one with [NewEndpoint].
type Endpoint struct {
	role    Role
	timers  Timers
	phase   Phase
	decoder *frameDecoder
	// encoding is how we frame what we send, as LCP agreed; LCP itself
	// always travels with the conservative encoding.
	encoding frameEncoding
	lcp      *automaton[*lcpNegotiator]
	// authentication is the exchange of the authentication phase; nil
	// when none runs.
	authentication authExchange
	ipcp           *automaton[ipcpNegotiator]
	// prober sends keepalive probes; nil without keepalive, or once the
	// peer showed that it cannot answer them.
	prober *prober
	// lcpIdentifier numbers the LCP packets the endpoint sends itself:
	// Protocol-Rejects, Echo-Requests and a last Terminate-Request.
	lcpIdentifier     uint8
	ownProbesReturned int
	outbox            outbox
	output            []byte
	datagrams         [][]byte
	events            []Event
	// ipv4 is the addressing last agreed through IPCP; nil before IPv4
	// first comes up, and once the link is dead.
	ipv4 *IPv4Settings
	// pendingDownReason is why we are closing the link, to be reported once
	// LCP has finished; nil when we are not closing it.
	pendingDownReason *DownReason
	stats             Stats
}

// NewEndpoint validates config and returns an endpoint in [PhaseDead];
// [Endpoint.Open] starts it.
//
// It fails with a [*ConfigError], checking in this order: ErrMRUTooSmall
// for an MRU below [MinMRU]; ErrInvalidTimers for a restart timer,
// MaxConfigure or MaxTerminate that is not above zero, or a negative
// MaxFailure; ErrInvalidKeepalive for a keepalive interval or MaxMissed
// that is not above zero; ErrCredentialTooLong for a name or password over
// 255 bytes, the client's own or those a server demands. For a server,
// ErrInvalidAddress follows for a LocalAddress, PeerAddress or DNS server
// that is not IPv4, and ErrInvalidAuthProtocol for a protocol without a
// constant of its own.
func NewEndpoint(config Config) (*Endpoint, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	lcp, ipcp := newNegotiators(config)
	return &Endpoint{
		role:   config.Role,
		timers: config.Timers,
		phase:  PhaseDead,
		// Every peer must accept 1500 bytes whatever MRU was negotiated
		// (RFC 1661 §6.1).
		decoder:  newFrameDecoder(int(max(config.MRU, DefaultMRU))),
		encoding: conservativeEncoding(),
		lcp:      newAutomaton(protocolLCP, lcp, config.Timers),
		ipcp:     newAutomaton(protocolIPCP, ipcp, config.Timers).toleratingNaks(ipcpNakTolerance),
		prober:   newProberFor(config.Keepalive),
	}, nil
}

// newNegotiators returns the LCP and IPCP policies of config's role.
func newNegotiators(config Config) (*lcpNegotiator, ipcpNegotiator) {
	server := config.Role.server
	if server == nil {
		return newClientLCP(config.MRU, config.ReceiveACCM), newClientIPCP()
	}
	return newServerLCP(config.MRU, config.ReceiveACCM, server.Authentication), newServerIPCP(*server)
}

// Phase returns where the link is in its life cycle.
func (e *Endpoint) Phase() Phase {
	return e.phase
}

// IPv4 returns the addressing last agreed with the peer through IPCP, and
// false before IPv4 first comes up and once the link is dead. While the
// link renegotiates, the last agreed settings remain. The settings are a
// copy for the caller to keep.
func (e *Endpoint) IPv4() (IPv4Settings, bool) {
	if e.ipv4 == nil {
		return IPv4Settings{}, false
	}
	return e.ipv4.clone(), true
}

// clone returns a copy of s that shares nothing with it.
func (s IPv4Settings) clone() IPv4Settings {
	s.DNSServers = slices.Clone(s.DNSServers)
	return s
}

// MTU returns the largest IPv4 datagram the peer accepts: its negotiated
// MRU, or [DefaultMRU] before it said otherwise.
func (e *Endpoint) MTU() int {
	return int(e.lcp.negotiator.peerMRU())
}

// Stats returns the traffic counters since the endpoint was created.
func (e *Endpoint) Stats() Stats {
	stats := e.stats
	stats.InvalidFrames = e.decoder.invalidFrames
	return stats
}

// String, GoString and Format are the endpoint's only methods that take it
// by value. fmt cannot call a pointer method on a copy, and would print a
// copy of an Endpoint field by field, the credentials it authenticates with
// included. An Endpoint must not be copied; one copied to be printed is
// redacted too, but for fmt's exceptions that Credentials lists.

// String describes the endpoint by its role and phase, such as "client
// endpoint in phase Open".
func (e Endpoint) String() string {
	return fmt.Sprintf("%v endpoint in phase %v", e.role, e.phase)
}

// GoString describes the endpoint by its role and phase in Go syntax, as
// the *Endpoint that [NewEndpoint] returns, with passwords redacted.
func (e Endpoint) GoString() string {
	return fmt.Sprintf("&ppp.Endpoint{role:%#v, phase:ppp.Phase%v}", e.role, e.phase)
}

// Format prints GoString for %#v and String for every other verb, %+v
// included, as for [Role]: fmt calls no methods of the endpoint's
// unexported fields, and would print them all, the credentials it
// authenticates with included.
func (e Endpoint) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, e)
}

// Open starts negotiating: an LCP Configure-Request is queued for
// [Endpoint.TakeOutput]. It does nothing unless the link is in [PhaseDead].
func (e *Endpoint) Open(now time.Time) {
	if e.phase != PhaseDead {
		return
	}
	e.phase = PhaseEstablish
	e.pendingDownReason = nil
	e.lcp.open(now, &e.outbox)
	e.flushOutbox()
}

// Close closes the link gracefully: a Terminate-Request is queued, and an
// [EventDown] with [DownReasonClosed] follows once the peer acknowledges,
// or after the retries run out. It does nothing once the link is dead or
// already terminating.
func (e *Endpoint) Close(now time.Time) {
	e.terminate(DownReason{Kind: DownReasonClosed}, now)
	e.flushOutbox()
}

// HandleInput processes bytes received from the line. Anything that is not
// a well-formed frame — line noise, modem result codes such as NO CARRIER,
// aborted or oversized frames — is dropped and counted in Stats.
func (e *Endpoint) HandleInput(data []byte, now time.Time) {
	e.stats.BytesReceived += uint64(len(data))
	// Inserted control characters are recognised against the map we ask
	// the peer to honour; until it agrees it escapes them all anyway.
	e.decoder.setReceiveACCM(e.lcp.negotiator.receiveACCM())
	for _, received := range e.decoder.decode(data) {
		e.stats.FramesReceived++
		if e.isOwnProbe(received) {
			e.onOwnProbeReturned()
			continue
		}
		if e.prober != nil {
			e.prober.heardFromPeer(now)
		}
		e.dispatch(received, now)
	}
	e.flushOutbox()
}

// dispatch hands a received frame to its protocol.
func (e *Endpoint) dispatch(received frame, now time.Time) {
	switch received.protocol {
	case protocolLCP:
		e.receiveLCP(received.information, now)
	case protocolPAP, protocolCHAP:
		e.receiveAuthentication(received.protocol, received.information, now)
	case protocolIPCP:
		e.receiveIPCP(received.information, now)
	case protocolIPv4:
		e.receiveDatagram(received.information)
	default:
		e.rejectProtocol(received.protocol, received.information)
	}
}

// HandleTimeout runs the retransmissions and keepalive probes that are due
// at now. Calling it early is harmless.
func (e *Endpoint) HandleTimeout(now time.Time) {
	e.onLCPTransition(e.lcp.handleTimeout(now, &e.outbox), now)
	if e.authentication != nil {
		e.onAuthenticationOutcome(e.authentication.handleTimeout(now, &e.outbox), now)
	}
	e.onIPCPTransition(e.ipcp.handleTimeout(now, &e.outbox), now)
	e.probeKeepalive(now)
	e.flushOutbox()
}

// NextTimeout returns when [Endpoint.HandleTimeout] next needs to run, and
// false when no timer is running.
func (e *Endpoint) NextTimeout() (time.Time, bool) {
	deadlines := []time.Time{e.lcp.nextTimeout(), e.ipcp.nextTimeout()}
	if e.authentication != nil {
		deadlines = append(deadlines, e.authentication.nextTimeout())
	}
	if e.prober != nil {
		deadlines = append(deadlines, e.prober.nextTimeout())
	}
	next := earliest(deadlines...)
	return next, !next.IsZero()
}

// SendIPv4 frames an IPv4 datagram for the peer; it is queued for
// [Endpoint.TakeOutput]. It fails with a [*SendError]: ErrNotOpen unless
// the link is in [PhaseOpen], ErrTooLarge for a datagram over
// [Endpoint.MTU]. The endpoint does not keep datagram.
func (e *Endpoint) SendIPv4(datagram []byte) error {
	if e.phase != PhaseOpen {
		return &SendError{Kind: ErrNotOpen}
	}
	if len(datagram) > e.MTU() {
		return &SendError{Kind: ErrTooLarge, Len: len(datagram), MRU: e.MTU()}
	}
	e.output = appendFrame(e.output, protocolIPv4, datagram, e.encoding)
	e.stats.FramesSent++
	return nil
}

// ReceiveIPv4 returns the next IPv4 datagram received from the peer, oldest
// first, and false when none is waiting. At most 64 datagrams are held;
// beyond that the oldest is dropped.
func (e *Endpoint) ReceiveIPv4() ([]byte, bool) {
	if len(e.datagrams) == 0 {
		return nil, false
	}
	datagram := e.datagrams[0]
	e.datagrams = e.datagrams[1:]
	return datagram, true
}

// TakeOutput returns the bytes to write to the line and forgets them; nil
// when there are none.
func (e *Endpoint) TakeOutput() []byte {
	output := e.output
	e.output = nil
	e.stats.BytesSent += uint64(len(output))
	if len(output) == 0 {
		return nil
	}
	return output
}

// PollEvent returns the next link event, oldest first, and false when there
// is none.
func (e *Endpoint) PollEvent() (Event, bool) {
	if len(e.events) == 0 {
		return Event{}, false
	}
	event := e.events[0]
	e.events = e.events[1:]
	return event, true
}

// receiveLCP handles an LCP packet: echoes and rejects here, negotiation in
// the automaton.
func (e *Endpoint) receiveLCP(information []byte, now time.Time) {
	received, ok := parsePacket(information)
	if !ok {
		e.discard()
		return
	}
	switch {
	case received.code == codeEchoRequest:
		e.answerEchoRequest(received)
	case received.code == codeEchoReply, received.code == codeDiscardRequest,
		received.code == codeIdentification, received.code == codeTimeRemaining:
		// Any frame already counts as a sign of life.
	case received.code == codeProtocolReject:
		e.onProtocolReject(received, now)
	case received.code == codeCodeReject && rejectsEchoRequests(received):
		e.prober = nil // the peer cannot answer probes, so stop sending them
	default:
		e.onLCPTransition(e.lcp.receive(received, now, &e.outbox), now)
	}
}

// rejectsEchoRequests reports whether a Code-Reject refuses our
// Echo-Requests.
func rejectsEchoRequests(codeReject packet) bool {
	return len(codeReject.data) > 0 && code(codeReject.data[0]) == codeEchoRequest
}

// receiveAuthentication hands a PAP or CHAP packet to the authentication
// exchange, if one runs for that protocol.
func (e *Endpoint) receiveAuthentication(protocol uint16, information []byte, now time.Time) {
	received, ok := parsePacket(information)
	if !ok || e.authentication == nil || e.authentication.protocolNumber() != protocol {
		e.discard()
		return
	}
	e.onAuthenticationOutcome(e.authentication.receive(received, now, &e.outbox), now)
}

// receiveIPCP hands an IPCP packet to its automaton.
func (e *Endpoint) receiveIPCP(information []byte, now time.Time) {
	// Network-layer packets before the network phase are discarded (RFC
	// 1661 §3.4–3.5).
	if !e.inNetworkPhase() {
		e.discard()
		return
	}
	received, ok := parsePacket(information)
	if !ok {
		e.discard()
		return
	}
	e.onIPCPTransition(e.ipcp.receive(received, now, &e.outbox), now)
}

// receiveDatagram queues an IPv4 datagram for the IP layer.
func (e *Endpoint) receiveDatagram(datagram []byte) {
	if e.phase != PhaseOpen {
		e.discard()
		return
	}
	if len(e.datagrams) == maxQueuedDatagrams {
		e.datagrams = e.datagrams[1:]
		e.stats.DiscardedPackets++
	}
	e.datagrams = append(e.datagrams, datagram)
}

// rejectProtocol tells the peer that we do not speak protocol (RFC 1661
// §5.7).
func (e *Endpoint) rejectProtocol(protocol uint16, information []byte) {
	e.discard()
	// Before the network phase, unknown protocols are simply dropped (RFC
	// 1661 §3.4–3.5).
	if !e.inNetworkPhase() {
		return
	}
	rejected := information[:min(len(information), protocolRejectCopyLimit)]
	data := append(binary.BigEndian.AppendUint16(nil, protocol), rejected...)
	e.outbox.push(protocolLCP, packet{code: codeProtocolReject, identifier: e.nextLCPIdentifier(), data: data})
}

// inNetworkPhase reports whether network-layer protocols may run.
func (e *Endpoint) inNetworkPhase() bool {
	return e.phase == PhaseNetwork || e.phase == PhaseOpen
}

// onProtocolReject ends the link when the peer refuses a protocol it
// cannot do without.
func (e *Endpoint) onProtocolReject(reject packet, now time.Time) {
	if len(reject.data) < uint16Len || !e.lcp.isOpened() {
		return
	}
	switch binary.BigEndian.Uint16(reject.data) {
	case protocolIPCP, protocolIPv4:
		e.terminate(DownReason{
			Kind:     DownReasonNegotiationFailed,
			Protocol: ipcpName,
			Detail:   "the peer does not support IPv4",
		}, now)
	case protocolPAP, protocolCHAP:
		if e.phase == PhaseAuthenticate {
			e.terminate(DownReason{
				Kind:   DownReasonAuthenticationFailed,
				Detail: "the peer rejected the authentication protocol",
			}, now)
		}
	}
}

// onLCPTransition moves the link along with LCP.
func (e *Endpoint) onLCPTransition(lcpTransition transition, now time.Time) {
	switch lcpTransition.kind {
	case transitionUp:
		e.onLinkEstablished(now)
	case transitionDown:
		e.onLinkLost()
	case transitionFinished:
		e.onLinkFinished(downReason(lcpName, lcpTransition.finish))
	}
}

// onLinkEstablished starts authentication, or the network phase when LCP
// agreed on none.
func (e *Endpoint) onLinkEstablished(now time.Time) {
	e.encoding = e.lcp.negotiator.transmitEncoding()
	if e.prober != nil {
		e.prober.start(now)
	}
	e.authentication = e.authenticationForLink()
	if e.authentication == nil {
		e.startNetwork(now)
		return
	}
	e.phase = PhaseAuthenticate
	e.authentication.start(now, &e.outbox)
}

// authenticationForLink returns the exchange LCP agreed must run, and nil
// when it agreed on none.
func (e *Endpoint) authenticationForLink() authExchange {
	lcp := e.lcp.negotiator
	server := e.role.server
	if server == nil {
		protocol, ok := lcp.authenticationToPerform()
		if !ok {
			return nil
		}
		return newAuthenticatee(protocol, e.role.credentials, e.timers)
	}
	protocol, ok := lcp.authenticationToVerify()
	if server.Authentication == nil || !ok {
		return nil
	}
	return newAuthenticator(protocol, server.Authentication.Credentials, e.timers)
}

// onAuthenticationOutcome starts the network phase once authentication
// succeeds, and ends the link when it fails.
func (e *Endpoint) onAuthenticationOutcome(result outcome, now time.Time) {
	switch result.kind {
	case outcomeSucceeded:
		if e.phase == PhaseAuthenticate {
			e.startNetwork(now)
		}
	case outcomeFailed:
		e.terminate(DownReason{Kind: DownReasonAuthenticationFailed, Detail: result.reason}, now)
	}
}

// startNetwork starts negotiating IPv4.
func (e *Endpoint) startNetwork(now time.Time) {
	e.phase = PhaseNetwork
	e.ipcp.open(now, &e.outbox)
}

// onIPCPTransition moves the link along with IPCP.
func (e *Endpoint) onIPCPTransition(ipcpTransition transition, now time.Time) {
	switch ipcpTransition.kind {
	case transitionUp:
		e.onNetworkUp(now)
	case transitionDown:
		e.phase = PhaseNetwork
	case transitionFinished:
		e.terminate(downReason(ipcpName, ipcpTransition.finish), now)
	}
}

// onNetworkUp opens the link for IPv4 with the addressing IPCP agreed, or
// ends it when that addressing is unusable.
func (e *Endpoint) onNetworkUp(now time.Time) {
	settings, err := e.ipcp.negotiator.settings()
	if err != nil {
		e.terminate(DownReason{Kind: DownReasonNegotiationFailed, Protocol: ipcpName, Detail: err.Error()}, now)
		return
	}
	e.phase = PhaseOpen
	e.ipv4 = &settings
	e.events = append(e.events, Event{Kind: EventUp, IPv4: settings.clone()})
}

// onLinkLost reacts to LCP leaving the Opened state: everything above it
// goes down with it.
func (e *Endpoint) onLinkLost() {
	if e.phase != PhaseTerminate {
		e.phase = PhaseEstablish
	}
	e.authentication = nil
	e.ipcp.abandon()
	e.encoding = conservativeEncoding()
	if e.prober != nil {
		e.prober.stop()
	}
}

// onLinkFinished ends the link for good, reporting why: the reason we
// closed it for, if we did, or else reason.
func (e *Endpoint) onLinkFinished(reason DownReason) {
	e.onLinkLost()
	e.phase = PhaseDead
	e.ipv4 = nil
	e.datagrams = nil
	if e.pendingDownReason != nil {
		reason = *e.pendingDownReason
		e.pendingDownReason = nil
	}
	e.events = append(e.events, Event{Kind: EventDown, Reason: reason})
}

// terminate closes the link, reporting reason once LCP has finished.
func (e *Endpoint) terminate(reason DownReason, now time.Time) {
	if e.phase == PhaseDead || e.phase == PhaseTerminate {
		return
	}
	e.pendingDownReason = &reason
	e.phase = PhaseTerminate
	lcpTransition := e.lcp.close(now, &e.outbox)
	if e.lcp.state == stateClosed {
		// LCP stops only as the link dies, so this cannot happen; should it,
		// the link ends here rather than wait for a goodbye nobody can say.
		e.onLinkFinished(DownReason{Kind: DownReasonClosed})
		return
	}
	e.onLCPTransition(lcpTransition, now)
}

// abandonLink ends the link without waiting for the peer, which cannot
// answer: one Terminate-Request goes out as a courtesy, then the link is
// down.
func (e *Endpoint) abandonLink(reason DownReason) {
	if e.phase == PhaseDead {
		return
	}
	e.outbox.push(protocolLCP, packet{code: codeTerminateRequest, identifier: e.nextLCPIdentifier()})
	e.lcp.abandon()
	e.onLinkFinished(reason)
}

// flushOutbox frames the waiting control packets into the output.
func (e *Endpoint) flushOutbox() {
	for _, waiting := range e.outbox.take() {
		// LCP always travels with conservative framing (RFC 1661 §6.6, RFC
		// 1662 §7.1).
		encoding := e.encoding
		if waiting.protocol == protocolLCP {
			encoding = conservativeEncoding()
		}
		e.output = appendFrame(e.output, waiting.protocol, waiting.packet.encode(), encoding)
		e.stats.FramesSent++
	}
}

// nextLCPIdentifier returns the identifier for the next LCP packet the
// endpoint sends itself, wrapping around after 255.
func (e *Endpoint) nextLCPIdentifier() uint8 {
	e.lcpIdentifier++
	return e.lcpIdentifier
}

// discard counts a packet dropped from a well-formed frame: malformed, or
// making no sense at the time.
func (e *Endpoint) discard() {
	e.stats.DiscardedPackets++
}

// downReason returns why the link went down when protocol's automaton
// stopped for why.
func downReason(protocol string, why finish) DownReason {
	switch why.kind {
	case finishClosed:
		return DownReason{Kind: DownReasonClosed}
	case finishTerminatedByPeer:
		return DownReason{Kind: DownReasonTerminatedByPeer}
	case finishTimedOut:
		return DownReason{Kind: DownReasonNegotiationTimedOut, Protocol: protocol}
	case finishFailed:
		return DownReason{Kind: DownReasonNegotiationFailed, Protocol: protocol, Detail: why.reason}
	default: // finishLoopedBack
		return DownReason{Kind: DownReasonLoopedBack}
	}
}
