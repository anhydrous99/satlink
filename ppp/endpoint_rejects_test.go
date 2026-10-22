package ppp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

// Tests of the packets an endpoint discards, and of the
// Protocol-Rejects it sends and receives.

func TestNetworkTrafficBeforeTheNetworkPhaseIsDiscarded(t *testing.T) {
	tests := []struct {
		name     string
		protocol uint16
	}{
		{"IPv4", protocolIPv4},
		{"IPCP", protocolIPCP},
		{"an unknown protocol", protocolIPv6},
		{"PAP without an authentication phase", protocolPAP},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint := mustEndpoint(t, clientConfig())
			endpoint.Open(testStart)
			endpoint.TakeOutput()

			endpoint.HandleInput(framed(test.protocol, packet{code: 1, identifier: 1}.encode()), testStart)

			if output := endpoint.TakeOutput(); output != nil {
				t.Errorf("answered with % X", output)
			}
			if got := endpoint.Stats().DiscardedPackets; got != 1 {
				t.Errorf("discarded %d packets, want 1", got)
			}
			if datagram, ok := endpoint.ReceiveIPv4(); ok {
				t.Errorf("the IP layer received % X", datagram)
			}
		})
	}
}

func TestADatagramThatArrivesWhileIPCPNegotiatesIsDiscarded(t *testing.T) {
	link := newTestLink(t, clientConfig(), serverConfig())
	link.exchangeUntil(t, func() bool { return link.client.Phase() == PhaseNetwork })

	// IPv4 may flow only once IPCP is open (RFC 1661 §3.6), even when the
	// link opens moments later.
	link.client.HandleInput(framed(protocolIPv4, []byte{0x45, 1, 2, 3}), link.now)
	link.settle(t)

	if link.client.Phase() != PhaseOpen {
		t.Fatalf("phase = %v, want Open", link.client.Phase())
	}
	if datagram, ok := link.client.ReceiveIPv4(); ok {
		t.Errorf("the IP layer received % X from before IPCP was open", datagram)
	}
}

func TestMalformedControlPacketsAreDiscarded(t *testing.T) {
	tests := []struct {
		name     string
		protocol uint16
		server   Config
		// phase is where the client takes the packet: where the protocol
		// runs.
		phase Phase
	}{
		{"LCP", protocolLCP, serverConfig(), PhaseOpen},
		{"PAP", protocolPAP, serverConfigDemanding(AuthProtocolPAP), PhaseAuthenticate},
		{"CHAP", protocolCHAP, serverConfigDemanding(AuthProtocolCHAPMD5), PhaseAuthenticate},
		{"IPCP", protocolIPCP, serverConfig(), PhaseOpen},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := newTestLink(t, clientConfig(), test.server)
			link.exchangeUntil(t, func() bool { return link.client.Phase() == test.phase })
			link.client.TakeOutput()
			before := link.client.Stats().DiscardedPackets

			link.client.HandleInput(framed(test.protocol, []byte{1, 1, 0, 9}), link.now)

			if output := link.client.TakeOutput(); output != nil {
				t.Errorf("answered with % X", output)
			}
			if got := link.client.Stats().DiscardedPackets - before; got != 1 {
				t.Errorf("discarded %d packets, want 1", got)
			}
			if link.client.Phase() != test.phase {
				t.Errorf("phase = %v, want %v", link.client.Phase(), test.phase)
			}
		})
	}
}

func TestAPeerThatRejectsIPv4EndsTheLink(t *testing.T) {
	for _, rejected := range []uint16{protocolIPCP, protocolIPv4} {
		t.Run(fmt.Sprintf("%#04x", rejected), func(t *testing.T) {
			link := establishedLink(t, serverConfig())
			events(link.client)

			link.client.HandleInput(lcpPacket(codeProtocolReject, 1, binary.BigEndian.AppendUint16(nil, rejected)), link.now)
			link.settle(t)

			assertDownReason(t, link.client, DownReason{
				Kind:     DownReasonNegotiationFailed,
				Protocol: "IPCP",
				Detail:   "the peer does not support IPv4",
			})
		})
	}
}

func TestProtocolRejectsThatChangeNothingAreIgnored(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"of another protocol", binary.BigEndian.AppendUint16(nil, protocolIPv6)},
		{"of PAP once authenticated", binary.BigEndian.AppendUint16(nil, protocolPAP)},
		{"without a protocol", []byte{0x80}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := establishedLink(t, serverConfigDemanding(AuthProtocolPAP))

			link.client.HandleInput(lcpPacket(codeProtocolReject, 1, test.data), link.now)

			if link.client.Phase() != PhaseOpen {
				t.Errorf("phase = %v, want Open", link.client.Phase())
			}
		})
	}
}

func TestAProtocolRejectBeforeLCPIsOpenIsIgnored(t *testing.T) {
	endpoint := mustEndpoint(t, clientConfig())
	endpoint.Open(testStart)

	endpoint.HandleInput(lcpPacket(codeProtocolReject, 1, binary.BigEndian.AppendUint16(nil, protocolIPCP)), testStart)

	if endpoint.Phase() != PhaseEstablish {
		t.Errorf("phase = %v, want Establish", endpoint.Phase())
	}
}

func TestAProtocolRejectCopiesAtMostSixtyFourBytes(t *testing.T) {
	link := establishedLink(t, serverConfig())
	unsupported := bytes.Repeat([]byte{0x60}, 100)

	link.client.HandleInput(framed(protocolIPv6, unsupported), link.now)

	rejects := controlPackets(link.client.TakeOutput(), protocolLCP)
	want := append(binary.BigEndian.AppendUint16(nil, protocolIPv6), unsupported[:64]...)
	if len(rejects) != 1 || !bytes.Equal(rejects[0].data, want) {
		t.Errorf("sent %+v, want one Protocol-Reject of the first 64 bytes", rejects)
	}
}
