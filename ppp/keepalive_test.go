package ppp

import (
	"bytes"
	"slices"
	"testing"
	"time"
)

// Tests of LCP echoes: keepalive probes, the answers to the peer's
// probes, and loopback detection from our own probes coming back.

func TestAnOpenLinkWithoutKeepaliveRunsNoTimer(t *testing.T) {
	link := newTestLink(t, withoutKeepalive(clientConfig()), serverConfig())
	link.settle(t)

	if next, ok := link.client.NextTimeout(); ok {
		t.Errorf("next timeout = %v, want none", next)
	}
	link.advance(time.Hour)
	if output := link.client.TakeOutput(); output != nil {
		t.Errorf("an idle link without keepalive sent % X", output)
	}
}

func TestTheEndpointKeepsItsOwnCopyOfTheKeepalive(t *testing.T) {
	config := clientConfig()
	link := newTestLink(t, config, serverConfig())
	config.Keepalive.Interval = time.Hour
	link.settle(t)

	link.advance(DefaultKeepalive().Interval)

	probes := controlPackets(link.client.TakeOutput(), protocolLCP)
	if len(probes) != 1 || probes[0].code != codeEchoRequest {
		t.Errorf("sent %+v after the default interval, want one Echo-Request", probes)
	}
}

func TestAPeerThatCannotAnswerProbesIsNotProbed(t *testing.T) {
	link := establishedLink(t, serverConfig())

	refuseProbes(link)
	link.advance(time.Hour)

	if next, ok := link.client.NextTimeout(); ok {
		t.Errorf("next timeout = %v, want no more probes", next)
	}
	if output := link.client.TakeOutput(); output != nil {
		t.Errorf("sent % X after the peer refused probes", output)
	}
	if link.client.Phase() != PhaseOpen {
		t.Errorf("phase = %v, want Open", link.client.Phase())
	}
}

func TestALinkThatRunsNoProbesCanBeClosed(t *testing.T) {
	tests := []struct {
		name   string
		client Config
		// stopProbes makes the client of an open link stop probing, if it
		// probes at all.
		stopProbes func(link *testLink)
	}{
		{"without keepalive", withoutKeepalive(clientConfig()), func(*testLink) {}},
		{"once the peer refused probes", clientConfig(), refuseProbes},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := newTestLink(t, test.client, serverConfig())
			link.settle(t)
			events(link.client)
			test.stopProbes(link)

			link.client.Close(link.now)
			link.settle(t)

			assertDownReason(t, link.client, DownReason{Kind: DownReasonClosed})
		})
	}
}

func TestAPeerThatRefusedProbesCanRestartLCP(t *testing.T) {
	link := establishedLink(t, serverConfig())
	refuseProbes(link)

	link.client.HandleInput(framed(protocolLCP, peerRequest(77).encode()), link.now)
	if link.client.Phase() != PhaseEstablish {
		t.Fatalf("phase = %v, want Establish", link.client.Phase())
	}
	link.settle(t)

	if link.client.Phase() != PhaseOpen {
		t.Errorf("phase = %v, want Open", link.client.Phase())
	}
}

// refuseProbes has the peer of an open link Code-Reject the client's
// Echo-Requests.
func refuseProbes(link *testLink) {
	rejectedProbe := packet{code: codeEchoRequest, identifier: 1, data: []byte{0, 0, 0, 1}}.encode()
	link.client.HandleInput(lcpPacket(codeCodeReject, 1, rejectedProbe), link.now)
}

func TestEchoRequestsAreAnsweredOnceLCPIsOpen(t *testing.T) {
	link := establishedLink(t, serverConfig())
	probe := []byte{0x12, 0x34, 0x56, 0x78, 'p', 'i', 'n', 'g'}

	link.client.HandleInput(lcpPacket(codeEchoRequest, 7, probe), link.now)

	replies := controlPackets(link.client.TakeOutput(), protocolLCP)
	want := append(magicNumberOf(link.client), "ping"...)
	if len(replies) != 1 {
		t.Fatalf("sent %+v, want one Echo-Reply", replies)
	}
	assertPacket(t, replies[0], packet{code: codeEchoReply, identifier: 7, data: want})
}

func TestEchoRequestsAreIgnoredWhileNegotiating(t *testing.T) {
	endpoint := mustEndpoint(t, clientConfig())
	endpoint.Open(testStart)
	endpoint.TakeOutput()

	endpoint.HandleInput(lcpPacket(codeEchoRequest, 7, []byte{1, 2, 3, 4}), testStart)

	if output := endpoint.TakeOutput(); output != nil {
		t.Errorf("answered an Echo-Request while negotiating: % X", output)
	}
}

func TestEchoRepliesFitThePeersMRU(t *testing.T) {
	link := establishedLink(t, withMRU(serverConfig(), MinMRU))
	probe := append([]byte{0x12, 0x34, 0x56, 0x78}, bytes.Repeat([]byte{'x'}, 200)...)

	link.client.HandleInput(lcpPacket(codeEchoRequest, 7, probe), link.now)

	replies := controlPackets(link.client.TakeOutput(), protocolLCP)
	if len(replies) != 1 || len(replies[0].encode()) != int(MinMRU) {
		t.Errorf("sent %+v, want one Echo-Reply of %d bytes", replies, MinMRU)
	}
}

func TestOurOwnDiscardRequestsComingBackEndTheLinkAsLooped(t *testing.T) {
	link := establishedLink(t, serverConfig())
	events(link.client)
	ownProbe := lcpPacket(codeDiscardRequest, 1, magicNumberOf(link.client))

	for range 2 {
		link.client.HandleInput(ownProbe, link.now)
	}
	if link.client.Phase() != PhaseOpen {
		t.Fatalf("phase = %v after two of our probes came back, want Open", link.client.Phase())
	}
	link.client.HandleInput(ownProbe, link.now) // the third

	assertDownReason(t, link.client, DownReason{Kind: DownReasonLoopedBack})
	if link.client.Phase() != PhaseDead {
		t.Errorf("phase = %v, want Dead", link.client.Phase())
	}
	link.client.HandleInput(ownProbe, link.now)
	if got := events(link.client); len(got) != 0 {
		t.Errorf("a probe returning to a dead link reported %+v", got)
	}
}

func TestEchoRepliesCarryingOurMagicNumberAreNotOurProbes(t *testing.T) {
	link := establishedLink(t, serverConfig())
	events(link.client)
	// A looped line reflects our requests; a reply is the peer's, whatever
	// magic number it carries.
	reply := lcpPacket(codeEchoReply, 1, magicNumberOf(link.client))

	for range ownProbeLoopbackLimit {
		link.client.HandleInput(reply, link.now)
	}

	if link.client.Phase() != PhaseOpen {
		t.Errorf("phase = %v, want Open", link.client.Phase())
	}
	assertEvents(t, events(link.client), nil)
}

func TestProbesWithoutAMagicNumberAreThePeersOnceOursIsRefused(t *testing.T) {
	tests := []struct {
		name    string
		code    code
		answers bool
	}{
		{"Echo-Requests", codeEchoRequest, true},
		{"Discard-Requests", codeDiscardRequest, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := linkRefusingTheClientsMagicNumber(t)
			events(link.client)
			// A peer without a magic number sends zero (RFC 1661 §5.8), the
			// number our own probes now carry.
			probe := []byte{0, 0, 0, 0, 'h', 'i'}

			for identifier := range uint8(5) {
				link.client.HandleInput(lcpPacket(test.code, identifier, probe), link.now)

				var want []packet
				if test.answers {
					want = []packet{{code: codeEchoReply, identifier: identifier, data: probe}}
				}
				assertPackets(t, controlPackets(link.client.TakeOutput(), protocolLCP), want)
			}

			if link.client.Phase() != PhaseOpen {
				t.Errorf("phase = %v, want Open", link.client.Phase())
			}
			if got := events(link.client); len(got) != 0 {
				t.Errorf("events = %+v, want none", got)
			}
		})
	}
}

// linkRefusingTheClientsMagicNumber returns an open link whose server
// Configure-Rejected the Magic-Number option of the client's first
// request, so that the client has no magic number.
func linkRefusingTheClientsMagicNumber(t *testing.T) *testLink {
	t.Helper()
	link := newTestLink(t, clientConfig(), serverConfig())
	request := controlPackets(link.client.TakeOutput(), protocolLCP)[0]
	options, _ := parseOptionList(request.data)
	isMagicNumber := func(proposed configOption) bool { return proposed.kind == lcpOptionMagicNumber }
	magicNumber := options[slices.IndexFunc(options, isMagicNumber)]
	reject := packet{code: codeConfigureReject, identifier: request.identifier, data: encodeOptionList([]configOption{magicNumber})}

	link.client.HandleInput(framed(protocolLCP, reject.encode()), link.now)
	link.settle(t)

	if link.client.Phase() != PhaseOpen || link.client.lcp.negotiator.ownMagicNumber() != 0 {
		t.Fatalf("phase = %v, magic number = %#x; want an open link without one", link.client.Phase(), link.client.lcp.negotiator.ownMagicNumber())
	}
	return link
}

func TestTrafficFromThePeerPostponesTheProbes(t *testing.T) {
	link := establishedLink(t, serverConfig())
	events(link.client)
	keepalive := DefaultKeepalive()
	started := link.now

	// Long enough for a silent peer to be probed and declared unresponsive;
	// nothing the client sends reaches the peer.
	var probes int
	for link.now.Sub(started) < keepalive.Interval*time.Duration(keepalive.MaxMissed+2) {
		link.now = link.now.Add(keepalive.Interval / 3)
		if err := link.server.SendIPv4([]byte{0x45}); err != nil {
			t.Fatal(err)
		}
		link.client.HandleInput(link.server.TakeOutput(), link.now)
		link.client.HandleTimeout(link.now)
		probes += len(withCode(controlPackets(link.client.TakeOutput(), protocolLCP), codeEchoRequest))
	}

	if probes != 0 {
		t.Errorf("sent %d probes on a busy link, want none", probes)
	}
	if link.client.Phase() != PhaseOpen {
		t.Errorf("phase = %v, want Open", link.client.Phase())
	}
}

func TestNoProbeGoesOutWhileLCPRenegotiates(t *testing.T) {
	link := establishedLink(t, serverConfig())
	link.client.HandleInput(framed(protocolLCP, peerRequest(77).encode()), link.now)
	started := link.now

	// The peer falls silent for two keepalive intervals.
	var probes int
	for link.now.Sub(started) < 2*DefaultKeepalive().Interval {
		link.now = link.now.Add(time.Second)
		link.client.HandleTimeout(link.now)
		probes += len(withCode(controlPackets(link.client.TakeOutput(), protocolLCP), codeEchoRequest))
	}

	if probes != 0 {
		t.Errorf("sent %d probes while LCP renegotiated, want none", probes)
	}
	if link.client.Phase() != PhaseEstablish {
		t.Errorf("phase = %v, want Establish", link.client.Phase())
	}
}

func TestTrafficReachingADeadLinkStartsNoProbes(t *testing.T) {
	link := establishedLink(t, serverConfig())
	link.client.Close(link.now)
	link.settle(t)
	if link.server.Phase() != PhaseDead {
		t.Fatalf("the server's phase = %v, want Dead", link.server.Phase())
	}

	// The client, starting over, asks the server, whose PPP has ended.
	link.server.HandleInput(framed(protocolLCP, peerRequest(60).encode()), link.now)
	link.server.TakeOutput() // a Terminate-Ack

	if next, ok := link.server.NextTimeout(); ok {
		t.Errorf("a dead link runs a timer until %v", next)
	}
	link.server.HandleTimeout(link.now.Add(DefaultKeepalive().Interval))
	if output := link.server.TakeOutput(); output != nil {
		t.Errorf("a dead link sent % X", output)
	}
}

func TestAnAbandonedLinkSaysGoodbyeWithOneTerminateRequest(t *testing.T) {
	for _, test := range abandonments {
		t.Run(test.name, func(t *testing.T) {
			link := establishedLink(t, serverConfig())
			events(link.client)

			goodbye := controlPackets(test.giveUp(link), protocolLCP)

			if len(goodbye) != 1 || goodbye[0].code != codeTerminateRequest {
				t.Errorf("the last words were %+v, want one TerminateRequest", goodbye)
			}
			assertDownReason(t, link.client, test.reason)
		})
	}
}

func TestAnAbandonedLinkStaysDeadUntilOpenedAgain(t *testing.T) {
	for _, test := range abandonments {
		t.Run(test.name, func(t *testing.T) {
			link := establishedLink(t, serverConfig())
			events(link.client)

			test.giveUp(link)

			assertDownReason(t, link.client, test.reason)
			assertDeadUntilOpenedAgain(t, link.client, link.now)
		})
	}
}

// abandonment is a way for the client to give a link up without waiting
// for the peer, which cannot answer.
type abandonment struct {
	name string
	// giveUp makes the client abandon the link, and returns what it sent as
	// it did.
	giveUp func(link *testLink) []byte
	// reason is why the link goes down.
	reason DownReason
}

// abandonments are the ways a link is abandoned: for a peer fallen silent,
// and for a line that echoes the client's probes.
var abandonments = []abandonment{
	{"for a silent peer", abandonForASilentPeer, DownReason{Kind: DownReasonPeerUnresponsive}},
	{"for a looped-back line", abandonForALoopedBackLine, DownReason{Kind: DownReasonLoopedBack}},
}

// abandonForASilentPeer lets the client's probes go unanswered until it
// gives the link up, and returns what it sent last.
func abandonForASilentPeer(link *testLink) []byte {
	var lastWords []byte
	for range 100 {
		if link.client.Phase() == PhaseDead {
			break
		}
		link.now = link.now.Add(time.Second)
		link.client.HandleTimeout(link.now)
		lastWords = link.client.TakeOutput()
	}
	return lastWords
}

// abandonForALoopedBackLine hands the client its own probes until it gives
// the link up, and returns what it sent last.
func abandonForALoopedBackLine(link *testLink) []byte {
	ownProbe := lcpPacket(codeDiscardRequest, 1, magicNumberOf(link.client))
	var lastWords []byte
	for range ownProbeLoopbackLimit {
		link.client.HandleInput(ownProbe, link.now)
		lastWords = link.client.TakeOutput()
	}
	return lastWords
}
