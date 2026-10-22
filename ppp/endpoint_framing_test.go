package ppp

import (
	"bytes"
	"errors"
	"testing"
)

// Go-only tests of how an endpoint frames what it sends: within the peer's
// MRU, with the control characters each end asked for escaped, headers
// compressed once LCP agreed, LCP itself always conservatively.

func TestTheNegotiatedMRUBoundsWhatIsSent(t *testing.T) {
	tests := []struct {
		name           string
		client, server Config
		// sender is the end whose peer has the smaller MRU.
		sender func(*testLink) *Endpoint
	}{
		{"by the server", clientConfig(), withMRU(serverConfig(), 576), func(l *testLink) *Endpoint { return l.client }},
		{"by the client", withMRU(clientConfig(), 576), serverConfig(), func(l *testLink) *Endpoint { return l.server }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := newTestLink(t, test.client, test.server)
			link.settle(t)
			sender := test.sender(link)

			if got := sender.MTU(); got != 576 {
				t.Errorf("MTU = %d, want 576", got)
			}
			err := sender.SendIPv4(make([]byte, 577))

			var sendError *SendError
			if !errors.As(err, &sendError) || *sendError != (SendError{Kind: ErrTooLarge, Len: 577, MRU: 576}) {
				t.Errorf("sending 577 bytes: %v, want ErrTooLarge", err)
			}
			if err := sender.SendIPv4(make([]byte, 576)); err != nil {
				t.Errorf("sending 576 bytes: %v", err)
			}
		})
	}
}

func TestTheMTUIsTheDefaultUntilThePeerSaysOtherwise(t *testing.T) {
	if got := mustEndpoint(t, clientConfig()).MTU(); got != int(DefaultMRU) {
		t.Errorf("MTU = %d, want %d", got, DefaultMRU)
	}
}

func TestAnEndpointWithASmallerMRUStillTakesFullSizedFrames(t *testing.T) {
	link := newTestLink(t, withMRU(clientConfig(), 576), serverConfig())
	link.settle(t)
	datagram := bytes.Repeat([]byte{0x45}, int(DefaultMRU))

	// A peer may ignore it: every peer must accept 1500 bytes, whatever MRU
	// was negotiated (RFC 1661 §6.1).
	link.client.HandleInput(framed(protocolIPv4, datagram), link.now)

	if got, ok := link.client.ReceiveIPv4(); !ok || !bytes.Equal(got, datagram) {
		t.Errorf("received %d bytes, %t; want the %d-byte datagram", len(got), ok, len(datagram))
	}
}

func TestEachEndEscapesTheControlCharactersTheOtherAskedFor(t *testing.T) {
	const xonAndXoff uint32 = 1<<0x11 | 1<<0x13
	tests := []struct {
		name                   string
		clientACCM, serverACCM uint32
	}{
		{"the client asks for XON and XOFF", xonAndXoff, 0},
		{"the server asks for XON and XOFF", 0, xonAndXoff},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, server := clientConfig(), serverConfig()
			client.ReceiveACCM, server.ReceiveACCM = test.clientACCM, test.serverACCM
			link := newTestLink(t, client, server)
			link.settle(t)
			datagram := []byte{0x45, 0x11, 0x13, 0x01}
			ends := []struct {
				sender *Endpoint
				// peersACCM is what the sender's peer asked it to escape.
				peersACCM uint32
			}{{link.client, test.serverACCM}, {link.server, test.clientACCM}}

			for _, end := range ends {
				if err := end.sender.SendIPv4(datagram); err != nil {
					t.Fatal(err)
				}

				want := frameBytes(protocolIPv4, datagram, frameEncoding{accm: end.peersACCM, compressAddressAndControl: true, compressProtocol: true})
				if got := end.sender.TakeOutput(); !bytes.Equal(got, want) {
					t.Errorf("the %v sent % X, want % X", end.sender.role, got, want)
				}
			}
		})
	}
}

func TestIPv4IsFramedAsLCPAgreed(t *testing.T) {
	link := establishedLink(t, serverConfig())
	datagram := []byte{0x45, 0x00, 0x01, 0x11, 0x13, 0x1F, 0x7E, 0x7D, 0xFF}

	if err := link.client.SendIPv4(datagram); err != nil {
		t.Fatal(err)
	}

	// Both ends asked for no control characters escaped and offered to read
	// compressed headers.
	if got, want := link.client.TakeOutput(), frameBytes(protocolIPv4, datagram, negotiatedEncoding()); !bytes.Equal(got, want) {
		t.Errorf("frame = % X, want % X", got, want)
	}
}

func TestControlPacketsAfterLCPAreFramedAsAgreed(t *testing.T) {
	link := newTestLink(t, clientConfig(), serverConfig())

	link.exchangeUntil(t, func() bool { return link.client.Phase() == PhaseNetwork })

	// The client's first IPCP request, asking for an address and both DNS
	// servers: the reference vector, which spells out the option types of
	// RFC 1332 and RFC 1877.
	want := frameBytes(protocolIPCP, hexBytes(t, ipcpRequestVector), negotiatedEncoding())
	if got := link.client.TakeOutput(); !bytes.Equal(got, want) {
		t.Errorf("sent % X, want % X", got, want)
	}
}

func TestLCPAlwaysTravelsConservativelyFramed(t *testing.T) {
	link := establishedLink(t, serverConfig())

	link.client.HandleInput(lcpPacket(codeEchoRequest, 7, []byte{0xA5, 0xA5, 0xA5, 0xA5}), link.now)

	// Whatever LCP agreed (RFC 1661 §6.6, RFC 1662 §7.1).
	reply := packet{code: codeEchoReply, identifier: 7, data: magicNumberOf(link.client)}
	if got, want := link.client.TakeOutput(), framed(protocolLCP, reply.encode()); !bytes.Equal(got, want) {
		t.Errorf("sent % X, want % X", got, want)
	}
}
