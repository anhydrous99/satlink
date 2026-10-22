package ppp

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"testing"
)

// Tests of the endpoint's exported API: the names and texts of its
// values, how they compare, and what it hands out — IPv4 settings,
// datagrams, output and statistics.

func TestPhasesAreNamed(t *testing.T) {
	tests := map[Phase]string{
		PhaseDead:         "Dead",
		PhaseEstablish:    "Establish",
		PhaseAuthenticate: "Authenticate",
		PhaseNetwork:      "Network",
		PhaseOpen:         "Open",
		PhaseTerminate:    "Terminate",
		9:                 "Phase(9)",
	}
	for phase, want := range tests {
		if got := phase.String(); got != want {
			t.Errorf("Phase(%d).String() = %q, want %q", int(phase), got, want)
		}
	}
}

func TestEventKindsAreNamed(t *testing.T) {
	tests := map[EventKind]string{EventUp: "Up", EventDown: "Down", 9: "EventKind(9)"}
	for kind, want := range tests {
		if got := kind.String(); got != want {
			t.Errorf("EventKind(%d).String() = %q, want %q", int(kind), got, want)
		}
	}
}

func TestDownReasonKindsAreNamed(t *testing.T) {
	tests := map[DownReasonKind]string{
		DownReasonClosed:               "Closed",
		DownReasonTerminatedByPeer:     "TerminatedByPeer",
		DownReasonNegotiationTimedOut:  "NegotiationTimedOut",
		DownReasonNegotiationFailed:    "NegotiationFailed",
		DownReasonAuthenticationFailed: "AuthenticationFailed",
		DownReasonPeerUnresponsive:     "PeerUnresponsive",
		DownReasonLoopedBack:           "LoopedBack",
		9:                              "DownReasonKind(9)",
	}
	for kind, want := range tests {
		if got := kind.String(); got != want {
			t.Errorf("DownReasonKind(%d).String() = %q, want %q", int(kind), got, want)
		}
	}
}

func TestDownReasonsStringMessages(t *testing.T) {
	tests := []struct {
		reason DownReason
		want   string
	}{
		{DownReason{Kind: DownReasonClosed}, "closed locally"},
		{DownReason{Kind: DownReasonTerminatedByPeer}, "terminated by the peer"},
		{DownReason{Kind: DownReasonNegotiationTimedOut, Protocol: "LCP"}, "LCP negotiation timed out"},
		{DownReason{Kind: DownReasonNegotiationFailed, Protocol: "IPCP", Detail: "the peer does not support IPv4"}, "IPCP negotiation failed: the peer does not support IPv4"},
		{DownReason{Kind: DownReasonAuthenticationFailed, Detail: "Access denied"}, "authentication failed: Access denied"},
		{DownReason{Kind: DownReasonPeerUnresponsive}, "the peer stopped responding"},
		{DownReason{Kind: DownReasonLoopedBack}, "the line is looped back"},
		{DownReason{Kind: 9}, "DownReasonKind(9)"},
	}
	for _, test := range tests {
		if got := test.reason.String(); got != test.want {
			t.Errorf("%+v.String() = %q, want %q", test.reason, got, test.want)
		}
	}
}

func TestSendErrorsSayWhatIsWrongAndMatchTheirKinds(t *testing.T) {
	tests := []struct {
		err   SendError
		text  string
		label string
	}{
		{SendError{Kind: ErrNotOpen}, "the link is not open for IPv4", "not open for IPv4"},
		{SendError{Kind: ErrTooLarge, Len: 1501, MRU: 1500}, "a 1501-byte datagram exceeds the peer's MRU of 1500", "datagram too large"},
		{SendError{Kind: 9}, "ppp.SendErrorKind(9)", "ppp.SendErrorKind(9)"},
	}
	for _, test := range tests {
		t.Run(test.label, func(t *testing.T) {
			err := fmt.Errorf("sending: %w", &test.err)

			if got := test.err.Error(); got != test.text {
				t.Errorf("Error() = %q, want %q", got, test.text)
			}
			if got := test.err.Kind.Error(); got != test.label {
				t.Errorf("Kind.Error() = %q, want %q", got, test.label)
			}
			if !errors.Is(err, test.err.Kind) || errors.Is(err, ErrMRUTooSmall) {
				t.Errorf("errors.Is does not match %v by its kind alone", err)
			}
		})
	}
	if errors.Is(&SendError{Kind: ErrNotOpen}, ErrTooLarge) {
		t.Error("ErrNotOpen matched ErrTooLarge")
	}
}

func TestEventsAndSettingsCompareByValue(t *testing.T) {
	settings := IPv4Settings{LocalAddress: clientAddress, PeerAddress: serverAddress, DNSServers: []netip.Addr{dnsServer, serverAddress}}
	reordered := IPv4Settings{LocalAddress: clientAddress, PeerAddress: serverAddress, DNSServers: []netip.Addr{serverAddress, dnsServer}}
	up := Event{Kind: EventUp, IPv4: settings}
	tests := []struct {
		name  string
		a, b  Event
		equal bool
	}{
		{"the same settings in other slices", up, Event{Kind: EventUp, IPv4: settings.clone()}, true},
		{"DNS servers in another order", up, Event{Kind: EventUp, IPv4: reordered}, false},
		{"no DNS servers, nil or empty", Event{Kind: EventUp}, Event{Kind: EventUp, IPv4: IPv4Settings{DNSServers: []netip.Addr{}}}, true},
		{"another kind", Event{Kind: EventUp}, Event{Kind: EventDown}, false},
		{"another reason", Event{Kind: EventDown}, Event{Kind: EventDown, Reason: DownReason{Kind: DownReasonLoopedBack}}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.a.Equal(test.b); got != test.equal {
				t.Errorf("Equal = %t, want %t", got, test.equal)
			}
		})
	}
}

func TestIPv4HandsOutCopies(t *testing.T) {
	link := establishedLink(t, serverConfig())
	up := events(link.client)[0]

	first, _ := link.client.IPv4()
	first.DNSServers[0] = netip.MustParseAddr("192.0.2.1")
	up.IPv4.DNSServers[0] = netip.MustParseAddr("192.0.2.2")

	second, ok := link.client.IPv4()
	if !ok || !second.Equal(IPv4Settings{LocalAddress: clientAddress, PeerAddress: serverAddress, DNSServers: []netip.Addr{dnsServer}}) {
		t.Errorf("IPv4 = %+v, %t after the copies were changed", second, ok)
	}
}

func TestIPv4IsUnsetBeforeTheLinkComesUpAndOnceItIsDead(t *testing.T) {
	link := newTestLink(t, clientConfig(), serverConfig())
	if settings, ok := link.client.IPv4(); ok {
		t.Errorf("IPv4 before negotiating = %+v", settings)
	}
	link.settle(t)
	if _, ok := link.client.IPv4(); !ok {
		t.Fatal("IPv4 is unset on an open link")
	}

	link.client.Close(link.now)
	link.settle(t)

	if settings, ok := link.client.IPv4(); ok {
		t.Errorf("IPv4 of a dead link = %+v", settings)
	}
}

func TestIPv4KeepsTheLastSettingsWhileTheLinkRenegotiates(t *testing.T) {
	tests := []struct {
		name     string
		protocol uint16
		phase    Phase
	}{
		{"LCP", protocolLCP, PhaseEstablish},
		{"IPCP", protocolIPCP, PhaseNetwork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := establishedLink(t, serverConfig())
			agreed, _ := link.client.IPv4()

			link.client.HandleInput(framed(test.protocol, peerRequest(99).encode()), link.now)

			if link.client.Phase() != test.phase {
				t.Fatalf("phase = %v, want %v", link.client.Phase(), test.phase)
			}
			if settings, ok := link.client.IPv4(); !ok || !settings.Equal(agreed) {
				t.Errorf("IPv4 = %+v, %t; want the last agreed %+v", settings, ok, agreed)
			}
		})
	}
}

func TestADeadLinkHandsOutNoDatagrams(t *testing.T) {
	link := establishedLink(t, serverConfig())
	if err := link.server.SendIPv4([]byte{0x45}); err != nil {
		t.Fatal(err)
	}
	link.settle(t) // the datagram waits for the IP layer

	link.client.Close(link.now)
	link.settle(t)

	if datagram, ok := link.client.ReceiveIPv4(); ok {
		t.Errorf("a dead link handed out % X", datagram)
	}
}

func TestTakeOutputReturnsNilWhenThereIsNothing(t *testing.T) {
	endpoint := mustEndpoint(t, clientConfig())
	if output := endpoint.TakeOutput(); output != nil {
		t.Errorf("an unopened endpoint's output = % X", output)
	}

	endpoint.Open(testStart)
	if output := endpoint.TakeOutput(); len(output) == 0 {
		t.Fatal("no Configure-Request after Open")
	}

	if output := endpoint.TakeOutput(); output != nil {
		t.Errorf("output taken twice = % X", output)
	}
}

func TestStatsCountTheTraffic(t *testing.T) {
	endpoint := mustEndpoint(t, clientConfig())
	endpoint.Open(testStart)
	sent := endpoint.TakeOutput()
	noise := []byte("\r\nNO CARRIER\r\n~")
	unexpected := framed(protocolIPCP, packet{code: codeConfigureRequest, identifier: 1}.encode())

	endpoint.HandleInput(noise, testStart)
	endpoint.HandleInput(unexpected, testStart)

	want := Stats{
		BytesReceived:    uint64(len(noise) + len(unexpected)),
		BytesSent:        uint64(len(sent)),
		FramesReceived:   1,
		FramesSent:       1,
		InvalidFrames:    1,
		DiscardedPackets: 1,
	}
	if got := endpoint.Stats(); got != want {
		t.Errorf("stats = %+v, want %+v", got, want)
	}
}

func TestEveryDatagramSentCountsAsAFrame(t *testing.T) {
	link := establishedLink(t, serverConfig())
	before := link.client.Stats().FramesSent

	if err := link.client.SendIPv4([]byte{0x45}); err != nil {
		t.Fatal(err)
	}

	if sent := link.client.Stats().FramesSent - before; sent != 1 {
		t.Errorf("counted %d frames sent, want 1", sent)
	}
}

func TestAtMost64DatagramsAreHeld(t *testing.T) {
	const held = 64 // as ReceiveIPv4 documents
	link := establishedLink(t, serverConfig())
	before := link.client.Stats().DiscardedPackets

	for index := range held + 1 {
		if err := link.server.SendIPv4([]byte{byte(index)}); err != nil {
			t.Fatal(err)
		}
	}
	link.settle(t)

	for index := 1; index <= held; index++ {
		if datagram, ok := link.client.ReceiveIPv4(); !ok || !bytes.Equal(datagram, []byte{byte(index)}) {
			t.Fatalf("datagram %d = % X, %t; want the oldest dropped", index, datagram, ok)
		}
	}
	if _, ok := link.client.ReceiveIPv4(); ok {
		t.Error("more than 64 datagrams were held")
	}
	if dropped := link.client.Stats().DiscardedPackets - before; dropped != 1 {
		t.Errorf("discarded %d packets, want 1", dropped)
	}
}
