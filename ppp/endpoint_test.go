package ppp

import (
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"
)

func TestOpensAndAssignsAddressesWithoutAuthentication(t *testing.T) {
	link := newTestLink(t, clientConfig(), serverConfig())

	link.settle(t)

	assertEvents(t, events(link.client), []Event{{Kind: EventUp, IPv4: IPv4Settings{
		LocalAddress: clientAddress,
		PeerAddress:  serverAddress,
		DNSServers:   []netip.Addr{dnsServer},
	}}})
	assertEvents(t, events(link.server), []Event{{Kind: EventUp, IPv4: IPv4Settings{
		LocalAddress: serverAddress,
		PeerAddress:  clientAddress,
		DNSServers:   []netip.Addr{dnsServer},
	}}})
}

func TestAuthenticatesWithPAPAndCHAP(t *testing.T) {
	for _, protocol := range []AuthProtocol{AuthProtocolPAP, AuthProtocolCHAPMD5} {
		t.Run(protocol.String(), func(t *testing.T) {
			link := newTestLink(t, clientConfig(), serverConfigDemanding(protocol))

			link.settle(t)

			if link.client.Phase() != PhaseOpen || link.server.Phase() != PhaseOpen {
				t.Errorf("phases = %v and %v, want Open", link.client.Phase(), link.server.Phase())
			}
		})
	}
}

func TestAWrongPasswordBringsBothEndsDown(t *testing.T) {
	for _, protocol := range []AuthProtocol{AuthProtocolPAP, AuthProtocolCHAPMD5} {
		t.Run(protocol.String(), func(t *testing.T) {
			wrong := NewConfig(ClientRole(Credentials{Username: "pilot", Password: "guess"}))
			link := newTestLink(t, wrong, serverConfigDemanding(protocol))

			link.settle(t)

			for _, endpoint := range []*Endpoint{link.client, link.server} {
				if endpoint.Phase() != PhaseDead {
					t.Errorf("phase = %v, want Dead", endpoint.Phase())
				}
				got := events(endpoint)
				if len(got) != 1 || got[0].Kind != EventDown || got[0].Reason.Kind != DownReasonAuthenticationFailed {
					t.Errorf("events = %+v, want one authentication failure", got)
				}
			}
		})
	}
}

func TestCarriesDatagramsBothWaysIncludingBytesThatNeedEscaping(t *testing.T) {
	link := establishedLink(t, serverConfigDemanding(AuthProtocolCHAPMD5))
	everyByte := make([]byte, 256)
	for value := range everyByte {
		everyByte[value] = byte(value)
	}

	if err := link.client.SendIPv4(everyByte); err != nil {
		t.Fatal(err)
	}
	if err := link.server.SendIPv4([]byte("reply")); err != nil {
		t.Fatal(err)
	}
	link.settle(t)

	if got, ok := link.server.ReceiveIPv4(); !ok || string(got) != string(everyByte) {
		t.Errorf("the server received % X, %t", got, ok)
	}
	if got, ok := link.client.ReceiveIPv4(); !ok || string(got) != "reply" {
		t.Errorf("the client received %q, %t", got, ok)
	}
	if got, ok := link.client.ReceiveIPv4(); ok {
		t.Errorf("the client received a second datagram: %q", got)
	}
}

func TestRefusesDatagramsBeforeOpenAndBeyondThePeerMRU(t *testing.T) {
	unopened := mustEndpoint(t, clientConfig())
	if err := unopened.SendIPv4([]byte("x")); !errors.Is(err, ErrNotOpen) {
		t.Errorf("sending before open: %v, want ErrNotOpen", err)
	}

	link := establishedLink(t, serverConfig())
	err := link.client.SendIPv4(make([]byte, 1501))

	var sendError *SendError
	if !errors.As(err, &sendError) || *sendError != (SendError{Kind: ErrTooLarge, Len: 1501, MRU: 1500}) {
		t.Errorf("sending 1501 bytes: %v, want ErrTooLarge", err)
	}
}

func TestCloseBringsBothEndsDown(t *testing.T) {
	link := establishedLink(t, serverConfig())
	events(link.client)
	events(link.server)

	link.client.Close(link.now)
	link.settle(t)

	assertEvents(t, events(link.client), []Event{{Kind: EventDown, Reason: DownReason{Kind: DownReasonClosed}}})
	assertEvents(t, events(link.server), []Event{{Kind: EventDown, Reason: DownReason{Kind: DownReasonTerminatedByPeer}}})
	if link.client.Phase() != PhaseDead {
		t.Errorf("phase = %v, want Dead", link.client.Phase())
	}
}

func TestRecoversWhenTheFirstConfigureRequestIsLost(t *testing.T) {
	link := newTestLink(t, clientConfig(), serverConfig())
	link.client.TakeOutput()
	link.settle(t)
	if link.client.Phase() == PhaseOpen {
		t.Fatal("the link opened without the client's request")
	}

	link.advance(DefaultTimers().Restart)
	link.settle(t)

	if link.client.Phase() != PhaseOpen || link.server.Phase() != PhaseOpen {
		t.Errorf("phases = %v and %v, want Open", link.client.Phase(), link.server.Phase())
	}
}

func TestGivesUpWhenThePeerNeverAnswers(t *testing.T) {
	timers := DefaultTimers()
	now := testStart
	endpoint := mustEndpoint(t, clientConfig())
	endpoint.Open(now)

	requestsSent := 0
	for endpoint.Phase() != PhaseDead {
		requestsSent += len(controlPackets(endpoint.TakeOutput(), protocolLCP))
		now = now.Add(timers.Restart)
		endpoint.HandleTimeout(now)
	}

	if requestsSent != timers.MaxConfigure {
		t.Errorf("sent %d requests, want %d", requestsSent, timers.MaxConfigure)
	}
	assertEvents(t, events(endpoint), []Event{{Kind: EventDown, Reason: DownReason{
		Kind:     DownReasonNegotiationTimedOut,
		Protocol: "LCP",
	}}})
}

func TestDeclaresASilentPeerUnresponsive(t *testing.T) {
	link := establishedLink(t, serverConfig())
	events(link.client)

	// A probe goes out after each silent interval; once MaxMissed went
	// unanswered the link is down at once — no goodbye to wait for.
	keepalive := DefaultKeepalive()
	started := link.now
	giveUp := started.Add(10 * time.Minute)
	for link.client.Phase() != PhaseDead && link.now.Before(giveUp) {
		link.client.TakeOutput() // nothing reaches the peer, nothing comes back
		link.now = link.now.Add(time.Second)
		link.client.HandleTimeout(link.now)
	}

	assertEvents(t, events(link.client), []Event{{Kind: EventDown, Reason: DownReason{Kind: DownReasonPeerUnresponsive}}})
	if elapsed, want := link.now.Sub(started), keepalive.Interval*time.Duration(keepalive.MaxMissed+1); elapsed != want {
		t.Errorf("the link went down after %v, want %v", elapsed, want)
	}
}

func TestALoopedBackLineIsDetectedFromReturningProbes(t *testing.T) {
	link := establishedLink(t, serverConfig())
	events(link.client)

	// From now on everything the client sends comes straight back to it.
	for range 100 {
		if link.client.Phase() == PhaseDead {
			break
		}
		link.now = link.now.Add(time.Second)
		link.client.HandleTimeout(link.now)
		link.client.HandleInput(link.client.TakeOutput(), link.now)
	}

	assertEvents(t, events(link.client), []Event{{Kind: EventDown, Reason: DownReason{Kind: DownReasonLoopedBack}}})
}

func TestAnsweredProbesKeepAnIdleLinkUp(t *testing.T) {
	link := establishedLink(t, serverConfig())

	for range 10 {
		link.advance(DefaultKeepalive().Interval)
		link.settle(t)
	}

	if link.client.Phase() != PhaseOpen {
		t.Errorf("phase = %v, want Open", link.client.Phase())
	}
	for _, event := range events(link.client) {
		if event.Kind != EventUp {
			t.Errorf("event %+v, want only Up", event)
		}
	}
}

func TestRejectsProtocolsItDoesNotSpeak(t *testing.T) {
	link := establishedLink(t, serverConfig())

	link.client.HandleInput(framed(protocolIPv6, []byte{0x60, 0x00, 0x00, 0x00}), link.now)

	rejects := controlPackets(link.client.TakeOutput(), protocolLCP)
	if len(rejects) != 1 || rejects[0].code != codeProtocolReject {
		t.Fatalf("sent %+v, want one ProtocolReject", rejects)
	}
	if rejected := rejects[0].data[:uint16Len]; rejected[0] != 0x00 || rejected[1] != 0x57 {
		t.Errorf("rejected protocol % X, want 00 57", rejected)
	}
}

func TestAcknowledgesATypicalPPPDConfigureRequest(t *testing.T) {
	client := mustEndpoint(t, clientConfig())
	client.Open(testStart)
	client.TakeOutput()
	// The option types as pppd puts them on the line (RFC 1661 §6), written
	// out so that the test checks lcp.go's constants instead of trusting
	// them.
	options := []configOption{
		option(1, 0x05, 0xDC),
		option(2, 0, 0, 0, 0),
		option(3, 0xC0, 0x23),
		option(5, 0x5A, 0x5A, 0x5A, 0x5A),
		option(7),
		option(8),
	}
	request := packet{code: codeConfigureRequest, identifier: 1, data: encodeOptionList(options)}

	client.HandleInput(framed(protocolLCP, request.encode()), testStart)

	answers := controlPackets(client.TakeOutput(), protocolLCP)
	if len(answers) != 1 {
		t.Fatalf("answers = %+v, want one", answers)
	}
	assertPacket(t, answers[0], packet{code: codeConfigureAck, identifier: 1, data: request.data})
}

func TestRenegotiatesWhenThePeerRestartsLCP(t *testing.T) {
	link := establishedLink(t, serverConfigDemanding(AuthProtocolPAP))
	events(link.client)
	restart := packet{code: codeConfigureRequest, identifier: 77}

	link.client.HandleInput(framed(protocolLCP, restart.encode()), link.now)
	if link.client.Phase() != PhaseEstablish {
		t.Fatalf("phase = %v, want Establish", link.client.Phase())
	}
	link.settle(t)

	if link.client.Phase() != PhaseOpen {
		t.Errorf("phase = %v, want Open", link.client.Phase())
	}
	if got := events(link.client); len(got) != 1 || got[0].Kind != EventUp {
		t.Errorf("events = %+v, want one Up", got)
	}
}

// hostileProtocols are the protocols the endpoints' hostile input comes in:
// every one they speak, and one they do not.
var hostileProtocols = []uint16{protocolLCP, protocolPAP, protocolCHAP, protocolIPCP, protocolIPv4, protocolIPv6}

func TestSurvivesArbitraryInputInEveryPhase(t *testing.T) {
	random := xorshift(0x2545_F491_4F6C_DD1D)

	for stepsBeforeAttack := range 6 {
		link := newTestLink(t, clientConfig(), serverConfigDemanding(AuthProtocolCHAPMD5))
		for range stepsBeforeAttack {
			link.server.HandleInput(link.client.TakeOutput(), link.now)
			link.client.HandleInput(link.server.TakeOutput(), link.now)
		}
		for range 2_000 {
			protocol := hostileProtocols[random()%uint64(len(hostileProtocols))]
			information := randomControlPacket(random)
			for _, endpoint := range []*Endpoint{link.client, link.server} {
				endpoint.HandleInput(framed(protocol, information), link.now)
			}
			link.client.HandleInput(randomNoise(random), link.now)
			link.advance(time.Duration(random()%5_000) * time.Millisecond)
		}
	}
}

// randomControlPacket returns a mostly well-formed control packet with a
// random code, identifier and option list, to reach deep into the parsers.
func randomControlPacket(random func() uint64) []byte {
	code := byte(random() % 14)
	identifier := byte(random())
	var data []byte
	for range random() % 6 {
		kinds := [...]byte{1, 2, 3, 5, 7, 8, 129, 131, byte(random())}
		kind := kinds[random()%uint64(len(kinds))]
		value := randomBytesOf(random, random()%7)
		data = append(data, kind, byte(len(value)+optionHeaderLen))
		data = append(data, value...)
	}
	if random()%4 == 0 {
		data = data[:random()%uint64(len(data)+1)] // break the option list
	}
	length := uint16(len(data) + headerLen)
	if random()%8 == 0 {
		length = uint16(random())
	}
	return append([]byte{code, identifier, byte(length >> 8), byte(length)}, data...)
}

// randomNoise returns up to 15 random bytes.
func randomNoise(random func() uint64) []byte {
	return randomBytesOf(random, random()%16)
}

// randomBytesOf returns length bytes drawn from random.
func randomBytesOf(random func() uint64, length uint64) []byte {
	drawn := make([]byte, 0, length)
	for range length {
		drawn = append(drawn, byte(random()))
	}
	return drawn
}

// xorshift returns Marsaglia's xorshift64 generator started at state: the
// same sequence on every run.
func xorshift(state uint64) func() uint64 {
	return func() uint64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return state
	}
}

// FuzzEndpointHandleInput feeds a hostile packet to both ends of a link at
// every stage of its negotiation: nothing may make an endpoint panic. The
// fuzzer also chooses whether the server demands CHAP-MD5 or PAP, as an
// endpoint hands a PAP or CHAP packet only to an exchange of that protocol.
// The packet is framed for the line, as in
// TestSurvivesArbitraryInputInEveryPhase: mutated line bytes would break the
// frame's FCS and reach nothing beyond the frame decoder, which
// FuzzFrameDecoder fuzzes. Line noise is left to that target and that test:
// Go's fuzzer lets two byte-slice arguments share their bytes as it mutates
// them, so noise would overwrite the packet.
func FuzzEndpointHandleInput(f *testing.F) {
	// seed names the protocol the server demands and the packet's protocol,
	// which the fuzzer varies by its index in hostileProtocols.
	seed := func(demanded AuthProtocol, stepsBeforeAttack uint8, protocol uint16, information []byte) {
		index := slices.Index(hostileProtocols, protocol)
		if index < 0 {
			f.Fatalf("protocol %#04x is not one of hostileProtocols", protocol)
		}
		f.Add(demanded == AuthProtocolPAP, stepsBeforeAttack, uint8(index), information)
	}
	seed(AuthProtocolCHAPMD5, 0, protocolLCP, packet{code: codeConfigureRequest, identifier: 1}.encode())
	seed(AuthProtocolCHAPMD5, 2, protocolCHAP, []byte{byte(chapChallenge), 1, 0, 6, 1, 0})
	seed(AuthProtocolCHAPMD5, 4, protocolIPCP, []byte{1, 1, 0, 10, 3, 6, 0, 0, 0, 0})
	// Over PAP, the client's exchange takes packets from step 1, the
	// server's from step 2.
	seed(AuthProtocolPAP, 1, protocolPAP, []byte{byte(papNak), 1, 0, 5, 0})
	seed(AuthProtocolPAP, 2, protocolPAP, []byte{byte(papRequest), 1, 0, 6, 0, 0})
	seed(AuthProtocolCHAPMD5, 9, protocolIPv4, []byte{0x45})
	f.Fuzz(func(t *testing.T, demandsPAP bool, stepsBeforeAttack, protocolIndex uint8, information []byte) {
		demanded := AuthProtocolCHAPMD5
		if demandsPAP {
			demanded = AuthProtocolPAP
		}
		link := newTestLink(t, clientConfig(), serverConfigDemanding(demanded))
		for range stepsBeforeAttack % 12 {
			link.server.HandleInput(link.client.TakeOutput(), link.now)
			link.client.HandleInput(link.server.TakeOutput(), link.now)
		}
		hostile := framed(hostileProtocols[int(protocolIndex)%len(hostileProtocols)], information)

		for _, endpoint := range []*Endpoint{link.client, link.server} {
			before := endpoint.Stats().BytesReceived
			endpoint.HandleInput(hostile, link.now)
			if received := endpoint.Stats().BytesReceived - before; received != uint64(len(hostile)) {
				t.Fatalf("counted %d bytes received, want %d", received, len(hostile))
			}
		}
		link.advance(DefaultTimers().Restart)
		link.settle(t)
	})
}
