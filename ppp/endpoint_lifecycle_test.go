package ppp

import (
	"encoding/binary"
	"testing"
	"time"
)

// Go-only tests of an endpoint's life: opening, closing, opening again,
// going down for good, and the timers its owner runs.

func TestOpenStartsOnlyADeadLink(t *testing.T) {
	tests := []struct {
		name   string
		server Config
		phase  Phase
	}{
		{"while LCP negotiates", serverConfig(), PhaseEstablish},
		{"while it authenticates", serverConfigDemanding(AuthProtocolPAP), PhaseAuthenticate},
		{"while IPCP negotiates", serverConfig(), PhaseNetwork},
		{"once open", serverConfig(), PhaseOpen},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := newTestLink(t, clientConfig(), test.server)
			link.exchangeUntil(t, func() bool { return link.client.Phase() == test.phase })
			inFlight := link.client.TakeOutput()

			link.client.Open(link.now)

			if output := link.client.TakeOutput(); output != nil {
				t.Errorf("Open sent % X", output)
			}
			if link.client.Phase() != test.phase {
				t.Fatalf("phase = %v after Open, want %v", link.client.Phase(), test.phase)
			}
			// Nor did it change anything else: the link comes up as it would
			// have.
			link.server.HandleInput(inFlight, link.now)
			link.settle(t)
			if err := link.client.SendIPv4([]byte{0x45}); err != nil {
				t.Errorf("sending once the link settled: %v", err)
			}
		})
	}
}

func TestOpenDoesNotUndoATerminationUnderWay(t *testing.T) {
	link := establishedLink(t, serverConfig())
	events(link.client)
	link.client.HandleInput(lcpPacket(codeProtocolReject, 1, binary.BigEndian.AppendUint16(nil, protocolIPCP)), link.now)
	goodbye := link.client.TakeOutput()

	link.client.Open(link.now)

	if output := link.client.TakeOutput(); output != nil {
		t.Errorf("Open sent % X", output)
	}
	if link.client.Phase() != PhaseTerminate {
		t.Fatalf("phase = %v after Open, want Terminate", link.client.Phase())
	}
	link.server.HandleInput(goodbye, link.now)
	link.settle(t)
	assertDownReason(t, link.client, DownReason{
		Kind:     DownReasonNegotiationFailed,
		Protocol: "IPCP",
		Detail:   "the peer does not support IPv4",
	})
}

func TestCloseDoesNothingOnceTheLinkIsDeadOrTerminating(t *testing.T) {
	unopened := mustEndpoint(t, clientConfig())
	unopened.Close(testStart)
	if output := unopened.TakeOutput(); output != nil {
		t.Errorf("closing an unopened link sent % X", output)
	}

	link := establishedLink(t, serverConfig())
	link.client.Close(link.now)
	link.client.TakeOutput()
	link.client.Close(link.now)

	if output := link.client.TakeOutput(); output != nil {
		t.Errorf("closing a terminating link sent % X", output)
	}
	if link.client.Phase() != PhaseTerminate {
		t.Errorf("phase = %v, want Terminate", link.client.Phase())
	}
}

func TestACloseWhileTheLinkEndsKeepsTheReasonItEndsFor(t *testing.T) {
	link := establishedLink(t, serverConfig())
	events(link.client)
	link.client.HandleInput(lcpPacket(codeProtocolReject, 1, binary.BigEndian.AppendUint16(nil, protocolIPCP)), link.now)

	link.client.Close(link.now)
	link.settle(t)

	assertDownReason(t, link.client, DownReason{
		Kind:     DownReasonNegotiationFailed,
		Protocol: "IPCP",
		Detail:   "the peer does not support IPv4",
	})
}

func TestClosingUnansweredGivesUpAfterTheRetries(t *testing.T) {
	timers := DefaultTimers()
	link := establishedLink(t, serverConfig())
	events(link.client)

	link.client.Close(link.now)
	terminateRequests := 0
	for link.client.Phase() != PhaseDead {
		terminateRequests += len(controlPackets(link.client.TakeOutput(), protocolLCP))
		link.now = link.now.Add(timers.Restart)
		link.client.HandleTimeout(link.now)
	}

	if terminateRequests != timers.MaxTerminate {
		t.Errorf("sent %d Terminate-Requests, want %d", terminateRequests, timers.MaxTerminate)
	}
	assertDownReason(t, link.client, DownReason{Kind: DownReasonClosed})
}

func TestACloseWhileLCPNegotiatesIsNotUndoneByACrossingNak(t *testing.T) {
	endpoint := mustEndpoint(t, clientConfig())
	endpoint.Open(testStart)
	request := controlPackets(endpoint.TakeOutput(), protocolLCP)[0]
	endpoint.Close(testStart)
	terminate := controlPackets(endpoint.TakeOutput(), protocolLCP)[0]

	// The peer's Nak of our request crosses our Terminate-Request.
	endpoint.HandleInput(framed(protocolLCP, nakOf(request).encode()), testStart)
	if answers := controlPackets(endpoint.TakeOutput(), protocolLCP); len(answers) != 0 {
		t.Errorf("answered the Nak with %+v", answers)
	}
	endpoint.HandleInput(lcpPacket(codeTerminateAck, terminate.identifier, nil), testStart)

	assertDownReason(t, endpoint, DownReason{Kind: DownReasonClosed})
}

func TestTheLinkCanBeOpenedAgainOnceDown(t *testing.T) {
	link := establishedLink(t, serverConfig())
	link.client.Close(link.now)
	link.settle(t)
	events(link.client)
	events(link.server)

	link.client.Open(link.now)
	link.server.Open(link.now)
	link.settle(t)

	if link.client.Phase() != PhaseOpen || link.server.Phase() != PhaseOpen {
		t.Errorf("phases = %v and %v, want Open", link.client.Phase(), link.server.Phase())
	}
	if got := events(link.client); len(got) != 1 || got[0].Kind != EventUp {
		t.Errorf("events = %+v, want one Up", got)
	}
}

func TestALinkDownForGoodStaysDeadUntilOpenedAgain(t *testing.T) {
	for _, test := range endings {
		t.Run(test.name, func(t *testing.T) {
			endpoint, now := test.end(t)

			assertDownReason(t, endpoint, test.reason)
			assertDeadUntilOpenedAgain(t, endpoint, now)
		})
	}
}

// ending is a way for a link to go down for good, other than abandoning it
// (keepalive_test.go).
type ending struct {
	name string
	// end brings an endpoint's link down, takes what the endpoint sent, and
	// returns it with the time the link went down.
	end func(t *testing.T) (*Endpoint, time.Time)
	// reason is why the link goes down.
	reason DownReason
}

// endings are the ways LCP stops, taking the link with it: a negotiation
// that times out, loops or fails, and a termination by either end.
var endings = []ending{
	{"the peer never answers", endUnanswered, DownReason{Kind: DownReasonNegotiationTimedOut, Protocol: "LCP"}},
	{"the line is looped back", endLoopedBack, DownReason{Kind: DownReasonLoopedBack}},
	{"the peer refuses to authenticate", answerTheAuthenticationDemand(codeConfigureReject, 0xC0, 0x23),
		lcpFailure("the peer refuses to authenticate")},
	{"the peer offers a protocol we do not speak", answerTheAuthenticationDemand(codeConfigureNak, 0xC2, 0x27),
		lcpFailure("the peer can only authenticate with a protocol we do not support")},
	{"the peer does not understand our request", endCodeRejected, lcpFailure("the peer does not understand ConfigureRequest")},
	{"the peer terminates the link", endTerminatedByThePeer, DownReason{Kind: DownReasonTerminatedByPeer}},
	{"our close is acknowledged", endClosed, DownReason{Kind: DownReasonClosed}},
	{"our close goes unanswered", endClosedUnanswered, DownReason{Kind: DownReasonClosed}},
}

// lcpFailure returns why a link goes down when LCP negotiation fails for
// detail.
func lcpFailure(detail string) DownReason {
	return DownReason{Kind: DownReasonNegotiationFailed, Protocol: "LCP", Detail: detail}
}

// endUnanswered lets a client's Configure-Requests go unanswered until it
// gives up.
func endUnanswered(t *testing.T) (*Endpoint, time.Time) {
	client, _ := negotiatingEndpoint(t, clientConfig())
	return client, runTimersUntilDead(client, testStart)
}

// endLoopedBack hands a client its own Configure-Request until it calls the
// line looped.
func endLoopedBack(t *testing.T) (*Endpoint, time.Time) {
	client := mustEndpoint(t, clientConfig())
	client.Open(testStart)
	request := client.TakeOutput()
	for range ownRequestLoopbackLimit {
		client.HandleInput(request, testStart)
	}
	client.TakeOutput()
	return client, testStart
}

// answerTheAuthenticationDemand returns an ending in which the peer answers
// the Authentication-Protocol option of a server's first Configure-Request
// with answer, a Configure-Nak or -Reject carrying value.
func answerTheAuthenticationDemand(answer code, value ...byte) func(t *testing.T) (*Endpoint, time.Time) {
	return func(t *testing.T) (*Endpoint, time.Time) {
		server, request := negotiatingEndpoint(t, serverConfigDemanding(AuthProtocolPAP))
		data := encodeOptionList([]configOption{option(lcpOptionAuthentication, value...)})
		server.HandleInput(framed(protocolLCP, packet{code: answer, identifier: request.identifier, data: data}.encode()), testStart)
		server.TakeOutput()
		return server, testStart
	}
}

// endCodeRejected has the peer Code-Reject a client's Configure-Request.
func endCodeRejected(t *testing.T) (*Endpoint, time.Time) {
	client, request := negotiatingEndpoint(t, clientConfig())
	client.HandleInput(lcpPacket(codeCodeReject, 1, request.encode()), testStart)
	client.TakeOutput()
	return client, testStart
}

// endTerminatedByThePeer has the peer of an open link terminate it.
func endTerminatedByThePeer(t *testing.T) (*Endpoint, time.Time) {
	link := establishedLink(t, serverConfig())
	events(link.client)
	link.client.HandleInput(lcpPacket(codeTerminateRequest, 5, nil), link.now)
	link.client.TakeOutput() // the Terminate-Ack
	return link.client, link.now
}

// endClosed closes an open link, which the peer acknowledges.
func endClosed(t *testing.T) (*Endpoint, time.Time) {
	link := establishedLink(t, serverConfig())
	events(link.client)
	link.client.Close(link.now)
	link.settle(t)
	return link.client, link.now
}

// endClosedUnanswered closes an open link whose peer has fallen silent.
func endClosedUnanswered(t *testing.T) (*Endpoint, time.Time) {
	link := establishedLink(t, serverConfig())
	events(link.client)
	link.client.Close(link.now)
	return link.client, runTimersUntilDead(link.client, link.now)
}

// runTimersUntilDead runs endpoint's timers every Restart from now, all it
// sends lost on the line, until its link is down, and returns the time it
// went down.
func runTimersUntilDead(endpoint *Endpoint, now time.Time) time.Time {
	const rounds = 100 // far more than any retry limit
	for range rounds {
		endpoint.TakeOutput()
		if endpoint.Phase() == PhaseDead {
			break
		}
		now = now.Add(DefaultTimers().Restart)
		endpoint.HandleTimeout(now)
	}
	return now
}

func TestNextTimeoutFollowsTheEarliestTimer(t *testing.T) {
	endpoint := mustEndpoint(t, clientConfig())
	if next, ok := endpoint.NextTimeout(); ok {
		t.Errorf("an unopened endpoint's next timeout = %v", next)
	}

	endpoint.Open(testStart)
	if next, ok := endpoint.NextTimeout(); !ok || next != testStart.Add(DefaultTimers().Restart) {
		t.Errorf("next timeout while negotiating = %v, %t", next, ok)
	}

	link := newTestLink(t, clientConfig(), serverConfig())
	link.exchangeUntil(t, func() bool { return link.client.Phase() == PhaseNetwork })
	// IPCP's restart timer and the keepalive probe's both run.
	if next, ok := link.client.NextTimeout(); !ok || next != link.now.Add(DefaultTimers().Restart) {
		t.Errorf("next timeout while IPCP negotiates = %v, %t; want IPCP's restart, before the probe", next, ok)
	}

	link.settle(t)
	if next, ok := link.client.NextTimeout(); !ok || next != link.now.Add(DefaultKeepalive().Interval) {
		t.Errorf("next timeout of an open link = %v, %t; want the next probe", next, ok)
	}
}

func TestAnOwnerThatFollowsNextTimeoutRecoversALostIPCPRequest(t *testing.T) {
	tests := []struct {
		name           string
		client, server Config
	}{
		{"without keepalive", withoutKeepalive(clientConfig()), withoutKeepalive(serverConfig())},
		// Every frame from the peer postpones the probes, so they cannot
		// stand in for IPCP's timer.
		{"with keepalive", clientConfig(), serverConfig()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			restart := DefaultTimers().Restart
			link := newTestLink(t, test.client, test.server)
			link.exchangeUntil(t, func() bool { return link.client.Phase() == PhaseNetwork })
			lost := link.now

			link.server.HandleInput(withoutFramesOf(protocolIPCP, link.client.TakeOutput()), link.now)
			link.settle(t)
			for link.client.Phase() != PhaseOpen && link.now.Sub(lost) < 2*restart {
				link.advanceAsTheOwnersDo(t, 100*time.Millisecond)
			}

			if elapsed := link.now.Sub(lost); link.client.Phase() != PhaseOpen || elapsed > restart {
				t.Errorf("phase = %v %v after the request was lost, want Open within %v", link.client.Phase(), elapsed, restart)
			}
		})
	}
}
