package ppp

import (
	"bytes"
	"math"
	"slices"
	"testing"
	"time"
)

// Tests of LCP as the line sees it: the codes on the wire, answers
// to unusual requests, loops and stray answers.

func TestLCPPacketsGoOutWithRFC1661sCodes(t *testing.T) {
	tests := []struct {
		name string
		// send makes an endpoint send the packet, and returns what it sent.
		send func(t *testing.T) []byte
		// code is the packet's, as RFC 1661 §5 numbers it: written out, so
		// that the test checks packet.go's constants instead of trusting
		// them, as both ends of every other test do.
		code byte
	}{
		{"Configure-Request, on Open", opening, 1},
		{"Configure-Ack, to an acceptable request", answering(peerRequest(1)), 2},
		{"Configure-Nak, to an MRU too small", answering(peerRequest(1, option(lcpOptionMRU, 0, 67))), 3},
		{"Configure-Reject, to an unknown option", answering(peerRequest(1, option(13, 6))), 4},
		{"Terminate-Request, on Close", byAnOpenClient(func(l *testLink) { l.client.Close(l.now) }), 5},
		{"Terminate-Ack, to a Terminate-Request", answering(packet{code: codeTerminateRequest, identifier: 1}), 6},
		{"Code-Reject, to an unknown code", answering(packet{code: 42, identifier: 1}), 7},
		{"Protocol-Reject, to an unknown protocol", byAnOpenClient(sendAnUnknownProtocol), 8},
		{"Echo-Request, after a silence", byAnOpenClient(probeAfterASilence), 9},
		{"Echo-Reply, to an Echo-Request (9)", byAnOpenClient(func(l *testLink) {
			l.client.HandleInput(framed(protocolLCP, []byte{9, 7, 0, 8, 0x12, 0x34, 0x56, 0x78}), l.now)
		}), 10},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sent := controlPackets(test.send(t), protocolLCP)

			if len(sent) != 1 || byte(sent[0].code) != test.code {
				t.Errorf("sent %+v, want one packet of code %d", sent, test.code)
			}
		})
	}
}

// opening returns what a client sends as it opens.
func opening(t *testing.T) []byte {
	client := mustEndpoint(t, clientConfig())
	client.Open(testStart)
	return client.TakeOutput()
}

// answering returns a function that hands received to a client that has
// just sent its first Configure-Request, and returns the client's answer.
func answering(received packet) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		client, _ := negotiatingEndpoint(t, clientConfig())
		client.HandleInput(framed(protocolLCP, received.encode()), testStart)
		return client.TakeOutput()
	}
}

// byAnOpenClient returns a function that makes the client of an open link
// act, and returns what the client sent.
func byAnOpenClient(act func(link *testLink)) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		link := establishedLink(t, serverConfig())
		act(link)
		return link.client.TakeOutput()
	}
}

// sendAnUnknownProtocol hands the client of an open link a frame of a
// protocol it does not speak, which it rejects.
func sendAnUnknownProtocol(link *testLink) {
	link.client.HandleInput(framed(protocolIPv6, []byte{0x60}), link.now)
}

// sendAnUnknownCode hands the client of an open link an LCP packet of a
// code it does not know, which it rejects. The packet's identifier is
// always the same, so that a Code-Reject that copied it would show.
func sendAnUnknownCode(link *testLink) {
	link.client.HandleInput(lcpPacket(42, 1, nil), link.now)
}

// probeAfterASilence lets a keepalive interval pass in silence, after which
// the client of an open link probes its peer.
func probeAfterASilence(link *testLink) {
	link.now = link.now.Add(DefaultKeepalive().Interval)
	link.client.HandleTimeout(link.now)
}

func TestEveryEchoRequestCodeRejectAndProtocolRejectTakesANewIdentifier(t *testing.T) {
	tests := []struct {
		name string
		code code
		// send makes the client of an open link send one.
		send func(link *testLink)
	}{
		{"Echo-Requests", codeEchoRequest, probeAfterASilence},
		// RFC 1661 §5.6–5.7: the identifier MUST be changed for each
		// Code-Reject and each Protocol-Reject sent.
		{"Code-Rejects", codeCodeReject, sendAnUnknownCode},
		{"Protocol-Rejects", codeProtocolReject, sendAnUnknownProtocol},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := establishedLink(t, serverConfig())

			var identifiers []uint8
			for range 2 {
				test.send(link)
				for _, sent := range withCode(controlPackets(link.client.TakeOutput(), protocolLCP), test.code) {
					identifiers = append(identifiers, sent.identifier)
				}
			}

			if len(identifiers) != 2 || identifiers[0] == identifiers[1] {
				t.Errorf("identifiers = %v, want two different ones", identifiers)
			}
		})
	}
}

func TestInformationalLCPPacketsChangeNothing(t *testing.T) {
	// Echo-Reply, Discard-Request, Identification and Time-Remaining, as
	// RFC 1661 §5 and RFC 1570 number them.
	for _, informational := range []code{10, 11, 12, 13} {
		t.Run(informational.String(), func(t *testing.T) {
			link := establishedLink(t, serverConfig())

			link.client.HandleInput(lcpPacket(informational, 3, []byte{0, 0, 0, 1}), link.now)

			if output := link.client.TakeOutput(); output != nil {
				t.Errorf("answered with % X", output)
			}
			if link.client.Phase() != PhaseOpen {
				t.Errorf("phase = %v, want Open", link.client.Phase())
			}
		})
	}
}

func TestALineLoopedBackWhileNegotiatingIsDetected(t *testing.T) {
	endpoint := mustEndpoint(t, clientConfig())
	endpoint.Open(testStart)
	request := endpoint.TakeOutput()

	for range 2 {
		endpoint.HandleInput(request, testStart)
	}
	if endpoint.Phase() != PhaseEstablish {
		t.Fatalf("phase = %v after two of our requests came back, want Establish", endpoint.Phase())
	}
	endpoint.HandleInput(request, testStart) // the third

	assertDownReason(t, endpoint, DownReason{Kind: DownReasonLoopedBack})
}

func TestALinkRecoversFromAStrayTerminateAckAtTheNextRestart(t *testing.T) {
	restart := DefaultTimers().Restart
	tests := []struct {
		name string
		// serverAhead is how much sooner than the client the server opened,
		// and so retransmits the request it lost.
		serverAhead time.Duration
	}{
		{"the two restarts crossing", 0},
		// The client acknowledges the server's request before its own
		// restart, which then finds it in Ack-Sent.
		{"the server's retransmission first", restart / 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := &testLink{
				client: mustEndpoint(t, withoutKeepalive(clientConfig())),
				server: mustEndpoint(t, withoutKeepalive(serverConfig())),
				now:    testStart,
			}
			link.server.Open(link.now)
			link.now = link.now.Add(test.serverAhead)
			link.client.Open(link.now)
			link.server.TakeOutput() // the server's request is lost
			link.server.HandleInput(link.client.TakeOutput(), link.now)
			link.client.HandleInput(link.server.TakeOutput(), link.now) // the client's is acknowledged
			started := link.now

			link.client.HandleInput(lcpPacket(codeTerminateAck, 9, nil), link.now)
			for link.client.Phase() != PhaseOpen && link.now.Sub(started) < 2*restart {
				link.advanceAsTheOwnersDo(t, 100*time.Millisecond)
			}

			if elapsed := link.now.Sub(started); link.client.Phase() != PhaseOpen || elapsed > restart {
				t.Errorf("phase = %v %v after the Terminate-Ack, want Open within %v", link.client.Phase(), elapsed, restart)
			}
		})
	}
}

func TestAHostileRequestCannotInflateTheNak(t *testing.T) {
	tests := []struct {
		name string
		// receiver returns the endpoint that answers the request, whose MRU
		// is the largest.
		receiver   func(t *testing.T) *Endpoint
		protocol   uint16
		repeated   configOption
		suggestion configOption
	}{
		{"LCP's Authentication-Protocol, to a client", negotiatingClientWithTheLargestMRU, protocolLCP,
			option(lcpOptionAuthentication), configOption{kind: lcpOptionAuthentication, value: AuthProtocolCHAPMD5.optionValue()}},
		{"IPCP's IP-Address, to a server", openServerWithTheLargestMRU, protocolIPCP,
			option(ipcpOptionIPAddress), ipv4Option(ipcpOptionIPAddress, clientAddress)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receiver := test.receiver(t)
			// As many of an option as fit, each answered by a longer
			// suggestion: one for every one would outgrow any packet.
			repeats := (math.MaxUint16 - headerLen) / optionHeaderLen
			request := peerRequest(42, slices.Repeat([]configOption{test.repeated}, repeats)...)

			receiver.HandleInput(framed(test.protocol, request.encode()), testStart)

			nak := packet{code: codeConfigureNak, identifier: 42, data: encodeOptionList([]configOption{test.suggestion})}
			assertPackets(t, withCode(controlPackets(receiver.TakeOutput(), test.protocol), codeConfigureNak), []packet{nak})
		})
	}
}

// negotiatingClientWithTheLargestMRU returns a client with the largest MRU
// that has just sent its first LCP request.
func negotiatingClientWithTheLargestMRU(t *testing.T) *Endpoint {
	t.Helper()
	client := mustEndpoint(t, withMRU(clientConfig(), math.MaxUint16))
	client.Open(testStart)
	client.TakeOutput()
	return client
}

// openServerWithTheLargestMRU returns a server with the largest MRU, open
// for IPv4.
func openServerWithTheLargestMRU(t *testing.T) *Endpoint {
	t.Helper()
	link := newTestLink(t, clientConfig(), withMRU(serverConfig(), math.MaxUint16))
	link.settle(t)
	return link.server
}

func TestARequestWithAnOptionWeRejectDrawsOnlyTheReject(t *testing.T) {
	client := mustEndpoint(t, clientConfig())
	client.Open(testStart)
	client.TakeOutput()
	callback := option(13, 6) // Callback (RFC 1570), which we do not speak
	request := peerRequest(1, callback, option(lcpOptionMRU, 0, 20))

	client.HandleInput(framed(protocolLCP, request.encode()), testStart)

	// A Nak only once every option is one we can take (RFC 1661 §5.3–5.4):
	// the MRU's turn comes when the peer asks again, without the Callback.
	reject := packet{code: codeConfigureReject, identifier: 1, data: encodeOptionList([]configOption{callback})}
	assertPackets(t, controlPackets(client.TakeOutput(), protocolLCP), []packet{reject})
}

func TestANakOrRejectKeepsTheOptionsInTheRequestsOrder(t *testing.T) {
	// RFC 1661 §5.3–5.4: the options of a Configure-Nak or -Reject MUST NOT
	// be reordered, nor, in a Reject, modified, so each instance of a
	// repeated option keeps its place. pppd discards an answer whose options
	// are not in the order it sent them, and asks again until Max-Configure
	// runs out. No request below is in ascending order of type, so that a
	// sorted answer would show too.
	tests := []struct {
		name string
		// receiver returns the endpoint that answers the request.
		receiver func(t *testing.T) *Endpoint
		protocol uint16
		// request holds the options of the peer's Configure-Request, and
		// answer those of the answer, a Nak or a Reject as answerCode says.
		request    string
		answerCode code
		answer     string
	}{
		// Endpoint-Discriminator (19), an MRU of 1500, Callback (13) and MRRU
		// (17): all but the MRU are options a client does not speak.
		{"an LCP Reject, from a client", negotiatingClient, protocolLCP,
			"13 03 00 01 04 05 DC 0D 03 06 11 04 05 DC", codeConfigureReject, "13 03 00 0D 03 06 11 04 05 DC"},
		{"an LCP Reject repeating an option, from a client", negotiatingClient, protocolLCP,
			"0D 03 06 11 04 05 DC 0D 03 07", codeConfigureReject, "0D 03 06 11 04 05 DC 0D 03 07"},
		// An Authentication-Protocol a client does not speak (EAP, C2 27) and
		// an MRU below 68 draw CHAP-MD5 and 68.
		{"an LCP Nak, from a client", negotiatingClient, protocolLCP,
			"03 04 C2 27 01 04 00 14", codeConfigureNak, "03 05 C2 23 05 01 04 00 44"},
		// A Primary DNS Server Address and an IP-Address, both 0.0.0.0, draw
		// the server's DNS server and the address it hands out.
		{"an IPCP Nak, from a server", openServer, protocolIPCP,
			"81 06 00 00 00 00 03 06 00 00 00 00", codeConfigureNak, "81 06 0A 40 00 35 03 06 0A 40 00 02"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receiver := test.receiver(t)
			request := packet{code: codeConfigureRequest, identifier: 1, data: hexBytes(t, test.request)}

			receiver.HandleInput(framed(test.protocol, request.encode()), testStart)

			// An open server sends its own request first, as it renegotiates.
			answers := withCode(controlPackets(receiver.TakeOutput(), test.protocol), test.answerCode)
			assertPackets(t, answers, []packet{{code: test.answerCode, identifier: 1, data: hexBytes(t, test.answer)}})
		})
	}
}

// negotiatingClient returns a client that has just sent its first LCP
// request.
func negotiatingClient(t *testing.T) *Endpoint {
	t.Helper()
	client, _ := negotiatingEndpoint(t, clientConfig())
	return client
}

// openServer returns the server of a link open for IPv4.
func openServer(t *testing.T) *Endpoint {
	t.Helper()
	return establishedLink(t, serverConfig()).server
}

func TestTheLongestOptionAPeerCanSendIsRejectedWhole(t *testing.T) {
	client, _ := negotiatingEndpoint(t, clientConfig())
	// A Callback (13), which we do not speak, with 253 octets of value: all
	// that an option's one-octet length, 255, leaves room for.
	longest := append([]byte{13, 255}, bytes.Repeat([]byte{6}, 253)...)

	client.HandleInput(framed(protocolLCP, packet{code: codeConfigureRequest, identifier: 1, data: longest}.encode()), testStart)

	reject := packet{code: codeConfigureReject, identifier: 1, data: longest}
	assertPackets(t, controlPackets(client.TakeOutput(), protocolLCP), []packet{reject})
}
