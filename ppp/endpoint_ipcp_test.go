package ppp

import (
	"net/netip"
	"testing"
)

// Go-only tests of IPCP at the endpoint: addresses no host can have,
// renegotiation, termination and the Nak tolerance.

func TestAClientRefusesAnAddressNoHostCanHave(t *testing.T) {
	loopback := NewConfig(ServerRole(ServerConfig{
		LocalAddress: serverAddress,
		PeerAddress:  netip.MustParseAddr("127.0.0.1"),
	}))
	link := newTestLink(t, clientConfig(), loopback)

	link.settle(t)

	assertDownReason(t, link.client, DownReason{
		Kind:     DownReasonNegotiationFailed,
		Protocol: "IPCP",
		Detail:   "the peer will not assign an IPv4 address",
	})
}

func TestAClientRefusesToRunWithoutAHostAddress(t *testing.T) {
	unspecified := NewConfig(ServerRole(ServerConfig{
		LocalAddress: serverAddress,
		PeerAddress:  netip.IPv4Unspecified(),
	}))
	link := newTestLink(t, clientConfig(), unspecified)

	link.settle(t)

	assertDownReason(t, link.client, DownReason{
		Kind:     DownReasonNegotiationFailed,
		Protocol: "IPCP",
		Detail:   "the peer assigned 0.0.0.0, which is not a host address",
	})
}

func TestARenegotiationOfIPCPTakesTheLinkBackToTheNetworkPhase(t *testing.T) {
	link := establishedLink(t, serverConfig())
	events(link.client)
	request := packet{code: codeConfigureRequest, identifier: 99, data: encodeOptionList([]configOption{ipv4Option(ipcpOptionIPAddress, serverAddress)})}

	link.client.HandleInput(framed(protocolIPCP, request.encode()), link.now)
	if link.client.Phase() != PhaseNetwork {
		t.Fatalf("phase = %v, want Network", link.client.Phase())
	}
	link.settle(t)

	if link.client.Phase() != PhaseOpen {
		t.Errorf("phase = %v, want Open", link.client.Phase())
	}
	if got := events(link.client); len(got) != 1 || got[0].Kind != EventUp {
		t.Errorf("events = %+v, want one Up", got)
	}
}

func TestAPeerTerminatingIPCPEndsTheLink(t *testing.T) {
	link := establishedLink(t, serverConfig())
	events(link.client)

	link.client.HandleInput(framed(protocolIPCP, packet{code: codeTerminateRequest, identifier: 5}.encode()), link.now)
	link.settle(t)

	assertDownReason(t, link.client, DownReason{Kind: DownReasonTerminatedByPeer})
}

func TestIPCPToleratesTwentyNaksInARow(t *testing.T) {
	link := newTestLink(t, clientConfig(), serverConfig())
	link.exchangeUntil(t, func() bool { return link.client.Phase() == PhaseNetwork })

	request := nakIPCPRequests(t, link.client, 20)
	link.client.HandleInput(framed(protocolIPCP, ackFor(request).encode()), link.now)
	link.client.HandleInput(framed(protocolIPCP, peerRequest(1, ipv4Option(ipcpOptionIPAddress, serverAddress)).encode()), link.now)

	assertEvents(t, events(link.client), []Event{{Kind: EventUp, IPv4: IPv4Settings{LocalAddress: clientAddress, PeerAddress: serverAddress}}})
}

func TestTheTwentyFirstIPCPNakInARowEndsTheLink(t *testing.T) {
	link := newTestLink(t, clientConfig(), serverConfig())
	link.exchangeUntil(t, func() bool { return link.client.Phase() == PhaseNetwork })
	request := nakIPCPRequests(t, link.client, 20)

	link.client.HandleInput(framed(protocolIPCP, addressNakFor(request).encode()), link.now)
	link.settle(t)

	assertDownReason(t, link.client, DownReason{
		Kind:     DownReasonNegotiationFailed,
		Protocol: "IPCP",
		Detail:   "the peer will not assign an IPv4 address",
	})
}

// nakIPCPRequests answers client's IPCP Configure-Requests with count Naks
// in a row, as a server that stalls while it finds an address does, and
// returns the request that answers the last. It fails the test unless
// every Nak is answered with a new request.
func nakIPCPRequests(t *testing.T, client *Endpoint, count int) packet {
	t.Helper()
	request := onlyIPCPRequest(t, client)
	for range count {
		client.HandleInput(framed(protocolIPCP, addressNakFor(request).encode()), testStart)
		request = onlyIPCPRequest(t, client)
	}
	return request
}

// addressNakFor returns a Nak of request suggesting the address the client
// is to have.
func addressNakFor(request packet) packet {
	suggestion := encodeOptionList([]configOption{ipv4Option(ipcpOptionIPAddress, clientAddress)})
	return packet{code: codeConfigureNak, identifier: request.identifier, data: suggestion}
}

// onlyIPCPRequest returns what client sent, failing the test unless it is
// one IPCP Configure-Request.
func onlyIPCPRequest(t *testing.T, client *Endpoint) packet {
	t.Helper()
	sent := controlPackets(client.TakeOutput(), protocolIPCP)
	if len(sent) != 1 || sent[0].code != codeConfigureRequest {
		t.Fatalf("sent %+v, want one IPCP ConfigureRequest", sent)
	}
	return sent[0]
}
