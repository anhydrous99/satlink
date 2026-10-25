package ppp

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// testProtocol is the protocol number the automaton tests negotiate.
const testProtocol = protocolIPCP

// simpleNegotiator proposes option 1 = [1] and accepts anything but option
// 9.
type simpleNegotiator struct {
	peerOptions []configOption
}

func (s *simpleNegotiator) ownOptions() []configOption {
	return []configOption{{kind: 1, value: []byte{1}}}
}

func (s *simpleNegotiator) applyNak([]configOption) error {
	return nil
}

func (s *simpleNegotiator) applyReject([]configOption) error {
	return errors.New("cannot work without option 1")
}

func (s *simpleNegotiator) judge(option configOption) verdict {
	if option.kind == 9 {
		return nakVerdict([]byte{0})
	}
	return ackVerdict()
}

func (s *simpleNegotiator) acceptPeerOptions(options []configOption) {
	s.peerOptions = slices.Clone(options)
}

// yieldingNegotiator is a simpleNegotiator that can do without option 1.
type yieldingNegotiator struct {
	simpleNegotiator
}

func (*yieldingNegotiator) applyReject([]configOption) error {
	return nil
}

// testAutomaton is the automaton the tests negotiate with.
type testAutomaton = automaton[*simpleNegotiator]

// newTestAutomaton returns a closed automaton with a simpleNegotiator.
func newTestAutomaton(timers Timers) *testAutomaton {
	return newAutomaton(testProtocol, &simpleNegotiator{}, timers)
}

// openedRequest opens the automaton and returns the request it sends.
func openedRequest(t *testing.T, a *testAutomaton, now time.Time) packet {
	t.Helper()
	var out outbox
	a.open(now, &out)
	return takeFirst(t, &out)
}

// openedAutomaton returns an automaton that has completed negotiating, and
// the peer's request it acknowledged.
func openedAutomaton(t *testing.T, now time.Time) (*testAutomaton, packet) {
	t.Helper()
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	request := openedRequest(t, a, now)
	a.receive(ackFor(request), now, &out)
	peer := peerRequest(5, configOption{kind: 2, value: []byte{7}})
	assertTransition(t, a.receive(peer, now, &out), layerUp)
	return a, peer
}

func TestOpensAfterBothRequestsAreAcknowledged(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	request := openedRequest(t, a, testStart)

	assertTransition(t, a.receive(ackFor(request), testStart, &out), noTransition)
	got := a.receive(peerRequest(5, configOption{kind: 2, value: []byte{7}}), testStart, &out)

	assertTransition(t, got, layerUp)
	if answer := takeFirst(t, &out); answer.code != codeConfigureAck {
		t.Errorf("answer = %v, want ConfigureAck", answer.code)
	}
	assertOptions(t, a.negotiator.peerOptions, []configOption{{kind: 2, value: []byte{7}}})
}

func TestIgnoresDuplicateAndMismatchedAcks(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	request := openedRequest(t, a, testStart)

	wrongData := ackFor(request)
	wrongData.data = append(slices.Clone(wrongData.data), 0)
	// An Ack as long as the request, but with another value.
	wrongValue := ackFor(request)
	wrongValue.data = encodeOptionList([]configOption{option(1, 2)})
	for _, mismatched := range []packet{wrongData, wrongValue} {
		a.receive(mismatched, testStart, &out)
		if a.state != stateRequestSent {
			t.Fatalf("state after the mismatched Ack %v = %v, want RequestSent", mismatched, a.state)
		}
	}
	a.receive(ackFor(request), testStart, &out)
	a.receive(ackFor(request), testStart, &out)

	if a.state != stateAckReceived {
		t.Errorf("state = %v, want AckReceived", a.state)
	}
	if sent := out.take(); len(sent) != 0 {
		t.Errorf("Acks were answered with %+v", sent)
	}
}

func TestRetransmitsThenGivesUp(t *testing.T) {
	timers := DefaultTimers()
	now := testStart
	a := newTestAutomaton(timers)
	var out outbox
	first := openedRequest(t, a, now)

	sent := 1
	var finished transition
	for finished.kind == transitionNone {
		now = now.Add(timers.Restart)
		finished = a.handleTimeout(now, &out)
		if finished.kind == transitionNone {
			assertPacket(t, takeFirst(t, &out), first)
			sent++
		}
	}

	assertTransition(t, finished, layerFinished(finish{kind: finishTimedOut}))
	if sent != timers.MaxConfigure {
		t.Errorf("sent %d requests, want %d", sent, timers.MaxConfigure)
	}
}

func TestAnAnswerToOurRequestRestartsTheRetryCount(t *testing.T) {
	timers := DefaultTimers()
	for _, answer := range []code{codeConfigureAck, codeConfigureNak, codeConfigureReject} {
		t.Run(answer.String(), func(t *testing.T) {
			now := testStart
			// It can do without its option: a Reject, too, draws a fresh
			// request.
			a := newAutomaton(testProtocol, &yieldingNegotiator{}, timers)
			var out outbox
			a.open(now, &out)
			request := takeFirst(t, &out)
			// The answer comes with one retry left: the request went out
			// MaxConfigure-1 times.
			for range timers.MaxConfigure - 2 {
				now = now.Add(timers.Restart)
				a.handleTimeout(now, &out)
			}
			out.take()
			assertTransition(t, a.receive(answerOf(answer, request), now, &out), noTransition)

			// RFC 1661's irc: after an answer, we wait a whole negotiation's
			// worth of retries before giving up — after an Ack, for the
			// peer's own request, which it may be slow to send; after a Nak
			// or Reject, for the answer to our fresh request, which a peer
			// that stalls with Naks may send late.
			requests := len(out.take())
			var finished transition
			for range 2 * timers.MaxConfigure {
				now = now.Add(timers.Restart)
				finished = a.handleTimeout(now, &out)
				requests += len(out.take())
				if finished.kind != transitionNone {
					break
				}
			}

			assertTransition(t, finished, layerFinished(finish{kind: finishTimedOut}))
			if requests != timers.MaxConfigure {
				t.Errorf("sent %d requests after the answer, want %d", requests, timers.MaxConfigure)
			}
		})
	}
}

func TestAnAnswerToAnAcknowledgedRequestIsIgnored(t *testing.T) {
	// Once its Ack has come, a request has had its answer: another one
	// carrying its identifier, a duplicate or a crossed one, is silently
	// discarded (RFC 1661 §4.1, RCA).
	states := []struct {
		want automatonState
		// reach brings a, whose request the peer has just acknowledged, to
		// want.
		reach func(t *testing.T, a *testAutomaton)
	}{
		{stateAckReceived, func(*testing.T, *testAutomaton) {}},
		{stateOpened, func(t *testing.T, a *testAutomaton) {
			var out outbox
			assertTransition(t, a.receive(peerRequest(5), testStart, &out), layerUp)
		}},
	}
	for _, state := range states {
		for _, answer := range []code{codeConfigureAck, codeConfigureNak, codeConfigureReject} {
			t.Run(answer.String()+" in "+state.want.String(), func(t *testing.T) {
				a := newTestAutomaton(DefaultTimers())
				var out outbox
				request := openedRequest(t, a, testStart)
				a.receive(ackFor(request), testStart, &out)
				state.reach(t, a)

				got := a.receive(answerOf(answer, request), testStart, &out)

				assertTransition(t, got, noTransition)
				if sent := out.take(); len(sent) != 0 {
					t.Errorf("the answer drew %+v", sent)
				}
				if a.state != state.want {
					t.Errorf("state = %v, want %v", a.state, state.want)
				}
			})
		}
	}
}

func TestStopsSuggestingAfterMaxFailure(t *testing.T) {
	timers := DefaultTimers()
	a := newTestAutomaton(timers)
	var out outbox
	openedRequest(t, a, testStart)
	stubborn := peerRequest(1, configOption{kind: 9, value: []byte{1}})

	var answers []code
	for range timers.MaxFailure + 1 {
		a.receive(stubborn, testStart, &out)
		answers = append(answers, takeFirst(t, &out).code)
	}

	for index, answer := range answers[:timers.MaxFailure] {
		if answer != codeConfigureNak {
			t.Errorf("answer %d = %v, want ConfigureNak", index, answer)
		}
	}
	if last := answers[len(answers)-1]; last != codeConfigureReject {
		t.Errorf("last answer = %v, want ConfigureReject", last)
	}
}

func TestARepeatedOptionDrawsOneSuggestion(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	openedRequest(t, a, testStart)
	repeating := peerRequest(1, option(9, 1), option(2, 7), option(9, 2), option(9, 3))

	a.receive(repeating, testStart, &out)

	nak := packet{code: codeConfigureNak, identifier: 1, data: encodeOptionList([]configOption{option(9, 0)})}
	assertOutgoing(t, out.take(), []outgoingPacket{{testProtocol, nak}})
}

func TestAnAckRestartsTheCountOfNaksInARow(t *testing.T) {
	timers := DefaultTimers()
	a := newTestAutomaton(timers)
	var out outbox
	openedRequest(t, a, testStart)
	stubborn := peerRequest(1, option(9, 1))
	for range timers.MaxFailure - 1 {
		a.receive(stubborn, testStart, &out)
	}
	a.receive(peerRequest(2, option(2, 7)), testStart, &out)
	out.take()

	var answers []code
	for range timers.MaxFailure {
		a.receive(stubborn, testStart, &out)
		answers = append(answers, takeFirst(t, &out).code)
	}

	for index, answer := range answers {
		if answer != codeConfigureNak {
			t.Errorf("answer %d after the Ack = %v, want ConfigureNak", index, answer)
		}
	}
}

func TestReopeningStartsEveryCountAfresh(t *testing.T) {
	timers := DefaultTimers()
	t.Run("of the Naks we sent", func(t *testing.T) {
		a := newTestAutomaton(timers)
		var out outbox
		openedRequest(t, a, testStart)
		stubborn := peerRequest(1, option(9, 1))
		for range timers.MaxFailure { // as many Naks as we send before refusing
			a.receive(stubborn, testStart, &out)
		}
		out.take()

		reopen(t, a)
		a.receive(stubborn, testStart, &out)

		if answer := out.take(); len(answer) != 1 || answer[0].packet.code != codeConfigureNak {
			t.Errorf("answer = %+v, want a ConfigureNak", answer)
		}
	})
	t.Run("of the Naks we received", func(t *testing.T) {
		a := newTestAutomaton(timers)
		assertNaksTolerated(t, a, openedRequest(t, a, testStart), timers.MaxFailure)

		request := reopen(t, a)

		assertNaksTolerated(t, a, request, timers.MaxFailure)
	})
	t.Run("of our own requests come back", func(t *testing.T) {
		a := newTestAutomaton(timers)
		var out outbox
		request := openedRequest(t, a, testStart)
		for range ownRequestLoopbackLimit - 1 { // one short of a looped line
			a.receive(request, testStart, &out)
		}

		request = reopen(t, a)

		assertTransition(t, a.receive(request, testStart, &out), noTransition)
	})
}

func TestEachOpeningStartsItsCountsAfresh(t *testing.T) {
	t.Run("of the retransmitted requests answered", func(t *testing.T) {
		a, peer := openedAutomaton(t, testStart)
		var out outbox
		for range maxRepeatedRequests { // as many as are answered again
			a.receive(peer, testStart, &out)
		}
		out.take()
		assertTransition(t, a.receive(peer, testStart, &out), layerDown)
		reacknowledge(t, a, &out)

		for range maxRepeatedRequests {
			assertTransition(t, a.receive(peer, testStart, &out), noTransition)
		}
	})
	t.Run("of the Naks we received", func(t *testing.T) {
		timers := DefaultTimers()
		a := newTestAutomaton(timers)
		var out outbox
		request := assertNaksTolerated(t, a, openedRequest(t, a, testStart), timers.MaxFailure)
		a.receive(ackFor(request), testStart, &out)
		assertTransition(t, a.receive(peerRequest(5), testStart, &out), layerUp)
		out.take()

		// The peer renegotiates, and Naks our new request.
		assertTransition(t, a.receive(peerRequest(6, option(2, 8)), testStart, &out), layerDown)
		request = takeFirst(t, &out)

		assertNaksTolerated(t, a, request, timers.MaxFailure)
	})
}

// assertNaksTolerated Naks count of a's requests in a row, starting with
// request, and fails the test unless a takes each Nak and asks again; it
// returns a's last request. The Naks change nothing in the request, so
// they are fruitless answers too.
func assertNaksTolerated(t *testing.T, a *testAutomaton, request packet, count int) packet {
	t.Helper()
	var out outbox
	for range count {
		assertTransition(t, a.receive(nakOf(request), testStart, &out), noTransition)
		request = takeFirst(t, &out)
	}
	return request
}

// reopen closes a, lets the close complete and opens a again, returning
// its fresh request.
func reopen(t *testing.T, a *testAutomaton) packet {
	t.Helper()
	var out outbox
	a.close(testStart, &out)
	assertTransition(t, a.receive(packet{code: codeTerminateAck, identifier: 1}, testStart, &out), layerFinished(finish{kind: finishClosed}))
	return openedRequest(t, a, testStart)
}

// reacknowledge completes a renegotiation the peer started, acknowledging
// the request a sent: the first packet out holds.
func reacknowledge(t *testing.T, a *testAutomaton, out *outbox) {
	t.Helper()
	assertTransition(t, a.receive(ackFor(takeFirst(t, out)), testStart, out), layerUp)
}

func TestReportsARefusedEssentialOption(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	request := openedRequest(t, a, testStart)
	reject := packet{code: codeConfigureReject, identifier: request.identifier, data: request.data}

	got := a.receive(reject, testStart, &out)

	assertTransition(t, got, layerFinished(finish{kind: finishFailed, reason: "cannot work without option 1"}))
}

func TestRejectsUnknownCodesAndAcknowledgesTheTermination(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	openedRequest(t, a, testStart)

	a.receive(packet{code: 42, identifier: 3}, testStart, &out)
	if reject := takeFirst(t, &out); reject.code != codeCodeReject {
		t.Errorf("answer to an unknown code = %v, want CodeReject", reject.code)
	}
	got := a.receive(packet{code: codeTerminateRequest, identifier: 4}, testStart, &out)

	assertTransition(t, got, layerFinished(finish{kind: finishTerminatedByPeer}))
	assertPacket(t, takeFirst(t, &out), packet{code: codeTerminateAck, identifier: 4})
}

func TestAnUnknownCodeDrawsOnlyACodeReject(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	openedRequest(t, a, testStart)
	// A Vendor-Specific packet (RFC 2153, code 0), which we do not speak. Its
	// data begin with a magic number whose first octet, 5, is a
	// Terminate-Request's code, as the data of a Code-Reject refusing one
	// would.
	vendorSpecific := packet{code: 0, identifier: 7, data: hexBytes(t, "05 12 34 56 00 00 5E 01")}

	got := a.receive(vendorSpecific, testStart, &out)

	assertTransition(t, got, noTransition)
	if a.state != stateRequestSent {
		t.Errorf("state = %v, want RequestSent", a.state)
	}
	reject := takeOnly(t, &out)
	want := packet{code: codeCodeReject, identifier: reject.packet.identifier, data: vendorSpecific.encode()}
	if reject.protocol != testProtocol || !reject.packet.equal(want) {
		t.Errorf("answer = %v, want a Code-Reject of the packet on protocol %#04x", reject, testProtocol)
	}
}

func TestARetransmittedRequestAfterOpeningIsAnsweredWithoutRenegotiating(t *testing.T) {
	a, peer := openedAutomaton(t, testStart)
	var out outbox

	// Three, written out, so that the test checks maxRepeatedRequests
	// instead of trusting it.
	for range 3 {
		assertTransition(t, a.receive(peer, testStart, &out), noTransition)
		assertOutgoing(t, out.take(), []outgoingPacket{{testProtocol, ackFor(peer)}})
	}
	if !a.isOpened() {
		t.Fatal("the automaton left the Opened state")
	}

	// Beyond that, it is a genuine renegotiation.
	assertTransition(t, a.receive(peer, testStart, &out), layerDown)
}

func TestANewRequestAfterOpeningRenegotiates(t *testing.T) {
	a, _ := openedAutomaton(t, testStart)
	var out outbox
	changed := peerRequest(6, configOption{kind: 2, value: []byte{8}})

	assertTransition(t, a.receive(changed, testStart, &out), layerDown)

	if a.state != stateAckSent {
		t.Errorf("state = %v, want AckSent", a.state)
	}
}

func TestEndlessNaksEndUpTreatedAsRejects(t *testing.T) {
	a := newTestAutomaton(DefaultTimers()).toleratingNaks(2)
	var out outbox
	request := openedRequest(t, a, testStart)

	for range 2 {
		nak := packet{code: codeConfigureNak, identifier: request.identifier, data: request.data}
		assertTransition(t, a.receive(nak, testStart, &out), noTransition)
		request = takeFirst(t, &out)
	}
	nak := packet{code: codeConfigureNak, identifier: request.identifier, data: request.data}
	got := a.receive(nak, testStart, &out)

	if got.kind != transitionFinished || got.finish.kind != finishFailed {
		t.Errorf("transition = %+v, want a failure", got)
	}
}

func TestDetectsALoopedBackLine(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	request := openedRequest(t, a, testStart)

	var outcomes []transition
	for range ownRequestLoopbackLimit {
		outcomes = append(outcomes, a.receive(request, testStart, &out))
	}

	for _, early := range outcomes[:len(outcomes)-1] {
		assertTransition(t, early, noTransition)
	}
	assertTransition(t, outcomes[len(outcomes)-1], layerFinished(finish{kind: finishLoopedBack}))
}

func TestCloseFinishesOnTerminateAckOrAfterRetries(t *testing.T) {
	timers := DefaultTimers()
	timers.Restart = time.Second
	t.Run("after the retries", func(t *testing.T) {
		now := testStart
		a := newTestAutomaton(timers)
		var out outbox
		openedRequest(t, a, now)

		a.close(now, &out)
		if a.state != stateClosing {
			t.Fatalf("state = %v, want Closing", a.state)
		}
		var finished transition
		for range 10 {
			now = now.Add(timers.Restart)
			if finished = a.handleTimeout(now, &out); finished.kind != transitionNone {
				break
			}
		}

		assertTransition(t, finished, layerFinished(finish{kind: finishClosed}))
	})
	t.Run("on the Terminate-Ack", func(t *testing.T) {
		a := newTestAutomaton(timers)
		var out outbox
		openedRequest(t, a, testStart)
		a.close(testStart, &out)

		got := a.receive(packet{code: codeTerminateAck, identifier: 1}, testStart, &out)

		assertTransition(t, got, layerFinished(finish{kind: finishClosed}))
	})
}

func TestAClosedAutomatonAnswersNegotiationWithTerminateAck(t *testing.T) {
	answered := []code{codeConfigureRequest, codeConfigureAck, codeConfigureNak, codeConfigureReject, codeTerminateRequest}
	for _, received := range answered {
		t.Run(received.String(), func(t *testing.T) {
			a := newTestAutomaton(DefaultTimers())
			var out outbox

			assertTransition(t, a.receive(packet{code: received, identifier: 9}, testStart, &out), noTransition)

			assertOutgoing(t, out.take(), []outgoingPacket{{testProtocol, packet{code: codeTerminateAck, identifier: 9}}})
		})
	}
	for _, ignored := range []code{codeTerminateAck, codeCodeReject} {
		t.Run(ignored.String(), func(t *testing.T) {
			a := newTestAutomaton(DefaultTimers())
			var out outbox

			a.receive(packet{code: ignored, identifier: 9, data: []byte{1}}, testStart, &out)

			if sent := out.take(); len(sent) != 0 {
				t.Errorf("a closed automaton answered with %+v", sent)
			}
		})
	}
}

func TestAFinishedAutomatonIsClosedAndRunsNoTimer(t *testing.T) {
	tests := []struct {
		name string
		// finish stops a, which has just sent request.
		finish func(a *testAutomaton, request packet, out *outbox) transition
		want   finish
	}{
		{"its requests unanswered", func(a *testAutomaton, _ packet, out *outbox) transition {
			return runOutOfRetries(a, out)
		}, finish{kind: finishTimedOut}},
		{"its close unanswered", func(a *testAutomaton, _ packet, out *outbox) transition {
			a.close(testStart, out)
			return runOutOfRetries(a, out)
		}, finish{kind: finishClosed}},
		{"its close acknowledged", func(a *testAutomaton, _ packet, out *outbox) transition {
			a.close(testStart, out)
			return a.receive(packet{code: codeTerminateAck, identifier: 2}, testStart, out)
		}, finish{kind: finishClosed}},
		{"terminated by the peer", func(a *testAutomaton, _ packet, out *outbox) transition {
			return a.receive(packet{code: codeTerminateRequest, identifier: 4}, testStart, out)
		}, finish{kind: finishTerminatedByPeer}},
		{"its requests coming back", func(a *testAutomaton, request packet, out *outbox) transition {
			for range ownRequestLoopbackLimit - 1 {
				a.receive(request, testStart, out)
			}
			return a.receive(request, testStart, out)
		}, finish{kind: finishLoopedBack}},
		{"an essential option refused", func(a *testAutomaton, request packet, out *outbox) transition {
			return a.receive(answerOf(codeConfigureReject, request), testStart, out)
		}, finish{kind: finishFailed, reason: "cannot work without option 1"}},
		{"a Nak beyond the tolerance", func(a *testAutomaton, request packet, out *outbox) transition {
			return a.toleratingNaks(0).receive(nakOf(request), testStart, out)
		}, finish{kind: finishFailed, reason: "cannot work without option 1"}},
		{"a negotiation code rejected", func(a *testAutomaton, request packet, out *outbox) transition {
			return a.receive(packet{code: codeCodeReject, identifier: 2, data: request.encode()}, testStart, out)
		}, finish{kind: finishFailed, reason: "the peer does not understand ConfigureRequest"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := newTestAutomaton(DefaultTimers())
			var out outbox
			request := openedRequest(t, a, testStart)

			got := test.finish(a, request, &out)

			assertTransition(t, got, layerFinished(test.want))
			if a.state != stateClosed || !a.nextTimeout().IsZero() {
				t.Errorf("state = %v, next timeout = %v; want Closed with no timer", a.state, a.nextTimeout())
			}
		})
	}
}

// runOutOfRetries lets a's transmissions go unanswered until it gives up,
// and returns the transition it gives up with: none if it never does.
func runOutOfRetries(a *testAutomaton, out *outbox) transition {
	now := testStart
	for range 2 * a.timers.MaxConfigure {
		now = now.Add(a.timers.Restart)
		if finished := a.handleTimeout(now, out); finished.kind != transitionNone {
			return finished
		}
	}
	return noTransition
}

func TestACodeRejectOfANegotiationCodeEndsTheNegotiation(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	openedRequest(t, a, testStart)
	request := peerRequest(1).encode()

	got := a.receive(packet{code: codeCodeReject, identifier: 2, data: request}, testStart, &out)

	assertTransition(t, got, layerFinished(finish{
		kind:   finishFailed,
		reason: "the peer does not understand ConfigureRequest",
	}))
}

func TestCodeRejectsTheNegotiationCanDoWithoutAreIgnored(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"an Echo-Request", packet{code: codeEchoRequest, identifier: 1}.encode()},
		{"an unknown code", []byte{42, 1, 0, 4}},
		{"nothing", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := newTestAutomaton(DefaultTimers())
			var out outbox
			openedRequest(t, a, testStart)

			got := a.receive(packet{code: codeCodeReject, identifier: 2, data: test.data}, testStart, &out)

			assertTransition(t, got, noTransition)
			if a.state != stateRequestSent {
				t.Errorf("state = %v, want RequestSent", a.state)
			}
		})
	}
}

func TestACodeRejectCopiesAtMostSixtyFourBytesOfTheUnknownPacket(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	openedRequest(t, a, testStart)
	unknown := packet{code: 42, identifier: 3, data: make([]byte, 100)}

	a.receive(unknown, testStart, &out)

	reject := takeFirst(t, &out)
	if want := unknown.encode()[:64]; !reject.equal(packet{code: codeCodeReject, identifier: reject.identifier, data: want}) {
		t.Errorf("Code-Reject = %+v, want the first 64 bytes of the packet", reject)
	}
}

func TestATerminateAckWhileOpenedRenegotiates(t *testing.T) {
	a, _ := openedAutomaton(t, testStart)
	var out outbox

	got := a.receive(packet{code: codeTerminateAck, identifier: 1}, testStart, &out)

	assertTransition(t, got, layerDown)
	if a.state != stateRequestSent {
		t.Errorf("state = %v, want RequestSent", a.state)
	}
	if request := takeFirst(t, &out); request.code != codeConfigureRequest {
		t.Errorf("sent %v, want a fresh ConfigureRequest", request.code)
	}
}

func TestATerminateAckAfterOurRequestWasAcknowledgedStartsOver(t *testing.T) {
	deadline := testStart.Add(DefaultTimers().Restart)
	peer := peerRequest(5)
	t.Run("the peer asking after our restart", func(t *testing.T) {
		a, first := afterAStrayTerminateAck(t)
		var out outbox

		fresh := assertAFreshRequestAtTheRestart(t, a, first, deadline)

		assertTransition(t, a.receive(ackFor(fresh), deadline, &out), noTransition)
		assertTransition(t, a.receive(peer, deadline, &out), layerUp)
	})
	t.Run("the peer asking before our restart", func(t *testing.T) {
		// As when an active peer's retransmission arrives first: the restart
		// then finds Ack-Sent, our request still answered.
		a, first := afterAStrayTerminateAck(t)
		var out outbox
		assertTransition(t, a.receive(peer, testStart, &out), noTransition)
		assertOutgoing(t, out.take(), []outgoingPacket{{testProtocol, ackFor(peer)}})
		if a.state != stateAckSent {
			t.Fatalf("state = %v, want AckSent", a.state)
		}

		fresh := assertAFreshRequestAtTheRestart(t, a, first, deadline)

		assertTransition(t, a.receive(ackFor(fresh), deadline, &out), layerUp)
	})
}

// afterAStrayTerminateAck returns an automaton that received a Terminate-Ack
// once the peer had acknowledged its first request, and that request. It
// fails the test unless the Terminate-Ack left the automaton in Request-Sent
// without sending anything, as RFC 1661's RTA in Ack-Rcvd does: the fresh
// request waits for the restart timeout.
func afterAStrayTerminateAck(t *testing.T) (*testAutomaton, packet) {
	t.Helper()
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	first := openedRequest(t, a, testStart)
	a.receive(ackFor(first), testStart, &out)

	assertTransition(t, a.receive(packet{code: codeTerminateAck, identifier: 1}, testStart, &out), noTransition)

	if sent := out.take(); len(sent) != 0 {
		t.Fatalf("the Terminate-Ack drew %+v", sent)
	}
	if a.state != stateRequestSent {
		t.Fatalf("state = %v, want RequestSent", a.state)
	}
	return a, first
}

// assertAFreshRequestAtTheRestart runs a's restart timeout at now and
// returns the request a sends, failing the test unless it is a fresh one:
// first was answered, so the next takes a new identifier (RFC 1661 §5.1),
// and only an answer to it counts.
func assertAFreshRequestAtTheRestart(t *testing.T, a *testAutomaton, first packet, now time.Time) packet {
	t.Helper()
	waiting := a.state
	var out outbox

	assertTransition(t, a.handleTimeout(now, &out), noTransition)

	fresh := takeOnly(t, &out).packet
	if fresh.code != codeConfigureRequest || fresh.identifier == first.identifier {
		t.Fatalf("sent %+v, want a ConfigureRequest with a new identifier", fresh)
	}
	assertTransition(t, a.receive(ackFor(first), now, &out), noTransition)
	if a.state != waiting {
		t.Fatalf("state after a late Ack of the first request = %v, want %v", a.state, waiting)
	}
	return fresh
}

func TestATimeoutAfterOurRequestWasAcknowledgedSendsAFreshRequest(t *testing.T) {
	timers := DefaultTimers()
	a := newTestAutomaton(timers)
	var out outbox
	request := openedRequest(t, a, testStart)
	a.receive(ackFor(request), testStart, &out)

	assertTransition(t, a.handleTimeout(testStart.Add(timers.Restart), &out), noTransition)

	fresh := takeFirst(t, &out)
	if fresh.code != codeConfigureRequest || fresh.identifier == request.identifier {
		t.Errorf("sent %+v, want a ConfigureRequest with a new identifier", fresh)
	}
	if a.state != stateRequestSent {
		t.Errorf("state = %v, want RequestSent", a.state)
	}
}

func TestAnEarlyTimeoutChangesNothing(t *testing.T) {
	timers := DefaultTimers()
	a := newTestAutomaton(timers)
	var out outbox
	openedRequest(t, a, testStart)

	assertTransition(t, a.handleTimeout(testStart.Add(timers.Restart-time.Nanosecond), &out), noTransition)

	if sent := out.take(); len(sent) != 0 {
		t.Errorf("an early timeout sent %+v", sent)
	}
	if want := testStart.Add(timers.Restart); a.nextTimeout() != want {
		t.Errorf("next timeout = %v, want %v", a.nextTimeout(), want)
	}
}

func TestClosingOpenedAndClosedAutomata(t *testing.T) {
	opened, _ := openedAutomaton(t, testStart)
	closed := newTestAutomaton(DefaultTimers())
	var out outbox

	assertTransition(t, opened.close(testStart, &out), layerDown)
	if terminate := takeFirst(t, &out); terminate.code != codeTerminateRequest {
		t.Errorf("sent %v, want TerminateRequest", terminate.code)
	}
	assertTransition(t, opened.close(testStart, &out), noTransition)
	assertTransition(t, closed.close(testStart, &out), noTransition)
	if sent := out.take(); len(sent) != 0 {
		t.Errorf("closing again sent %+v", sent)
	}
}

func TestAClosingAutomatonIgnoresRequestsAndWaitsForItsOwnAnswer(t *testing.T) {
	a, _ := openedAutomaton(t, testStart)
	var out outbox
	a.close(testStart, &out)
	out.take()

	assertTransition(t, a.receive(peerRequest(7), testStart, &out), noTransition)
	assertTransition(t, a.receive(packet{code: codeTerminateRequest, identifier: 8}, testStart, &out), noTransition)

	assertOutgoing(t, out.take(), []outgoingPacket{{testProtocol, packet{code: codeTerminateAck, identifier: 8}}})
	assertTransition(t, a.receive(packet{code: codeTerminateAck, identifier: 1}, testStart, &out), layerFinished(finish{kind: finishClosed}))
}

func TestACloseIsNotUndoneByAnAnswerCrossingIt(t *testing.T) {
	// Each on an automaton of its own: taken up, a Nak and a Reject would go
	// different ways.
	for _, crossing := range []code{codeConfigureNak, codeConfigureReject} {
		t.Run(crossing.String(), func(t *testing.T) {
			a := newTestAutomaton(DefaultTimers())
			var out outbox
			request := openedRequest(t, a, testStart)
			a.close(testStart, &out)
			terminate := takeFirst(t, &out)

			// The peer answered our request before our Terminate-Request
			// reached it.
			assertTransition(t, a.receive(answerOf(crossing, request), testStart, &out), noTransition)

			if sent := out.take(); len(sent) != 0 {
				t.Errorf("the answer was taken up with %+v", sent)
			}
			if a.state != stateClosing {
				t.Errorf("state = %v, want Closing", a.state)
			}
			terminateAck := packet{code: codeTerminateAck, identifier: terminate.identifier}
			assertTransition(t, a.receive(terminateAck, testStart, &out), layerFinished(finish{kind: finishClosed}))
		})
	}
}

func TestMalformedOptionListsAreIgnored(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	request := openedRequest(t, a, testStart)
	malformed := []byte{1, 9}

	assertTransition(t, a.receive(packet{code: codeConfigureRequest, identifier: 5, data: malformed}, testStart, &out), noTransition)
	assertTransition(t, a.receive(packet{code: codeConfigureNak, identifier: request.identifier, data: malformed}, testStart, &out), noTransition)

	if sent := out.take(); len(sent) != 0 {
		t.Errorf("malformed packets were answered with %+v", sent)
	}
	if a.requestAnswered {
		t.Error("a malformed Nak counted as the answer to our request")
	}
}

func TestOpeningAnAutomatonThatIsNotClosedChangesNothing(t *testing.T) {
	a := newTestAutomaton(DefaultTimers())
	var out outbox
	first := openedRequest(t, a, testStart)

	a.open(testStart, &out)

	if sent := out.take(); len(sent) != 0 {
		t.Errorf("a second open sent %+v", sent)
	}
	if a.request == nil || !a.request.equal(first) || a.state != stateRequestSent {
		t.Errorf("state = %v, request = %+v; want RequestSent with the first request", a.state, a.request)
	}
}
