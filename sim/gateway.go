package sim

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/anhydrous99/satlink/internal/rustdebug"
	"github.com/anhydrous99/satlink/ppp"
)

// The far end of a data call: the provider's PPP server and, once IPv4 is
// up on the link, the simulated internet behind it.
//
// When PPP terminates, the call stays up, as the gateway does not hang up;
// the server then answers further negotiation with Terminate-Ack, so the
// app must hang up and dial again for a new PPP session.

// gateway is the PPP server and the internet behind it.
type gateway struct {
	ppp            *ppp.Endpoint
	network        NetworkConfig
	authentication *Authentication
	// internet exists while IPv4 is up on the link; nil otherwise.
	internet *internet
}

// startGateway starts the PPP server, which sends its first
// Configure-Request at once. It fails for a network the gateway cannot
// serve: addresses that are not IPv4, an MRU below ppp.MinMRU, credentials
// PAP and CHAP cannot carry, a host without an IPv4 address.
func startGateway(network NetworkConfig, authentication *Authentication, now time.Time) (*gateway, error) {
	server, err := startServer(network, authentication, now)
	if err != nil {
		return nil, err
	}
	if err := checkHosts(network.Hosts); err != nil {
		return nil, err
	}
	return &gateway{ppp: server, network: network, authentication: authentication}, nil
}

// restartPPP starts PPP over, assigning clientAddress this time, as RFC
// 1332 allows. The internet is rebuilt for the new address, so whatever was
// open on the old one is lost. It fails, changing nothing, when PPP cannot
// start with clientAddress: one that is not IPv4.
func (g *gateway) restartPPP(clientAddress netip.Addr, now time.Time) error {
	network := g.network
	network.ClientAddress = clientAddress
	server, err := startServer(network, g.authentication, now)
	if err != nil {
		return err
	}
	g.ppp = server
	g.network = network
	g.closeInternet()
	return nil
}

// receive takes bytes that came up the link.
func (g *gateway) receive(data []byte, now time.Time) {
	g.ppp.HandleInput(data, now)
}

// advance runs the PPP timers, follows the link up and down, and passes
// datagrams between PPP and the internet.
func (g *gateway) advance(now time.Time) {
	if due, ok := g.ppp.NextTimeout(); ok && !due.After(now) {
		g.ppp.HandleTimeout(now)
	}
	g.followLinkEvents(now)
	g.exchangeDatagrams(now)
}

// takeOutput returns the bytes for the link down to the handset.
func (g *gateway) takeOutput() []byte {
	return g.ppp.TakeOutput()
}

// takeDiscardedBytes returns the bytes the discard service has read since
// the last call.
func (g *gateway) takeDiscardedBytes() uint64 {
	if g.internet == nil {
		return 0
	}
	return g.internet.takeDiscardedBytes()
}

// close ends the internet, if there is one: the call is over.
func (g *gateway) close() {
	g.closeInternet()
}

func (g *gateway) followLinkEvents(now time.Time) {
	for event, ok := g.ppp.PollEvent(); ok; event, ok = g.ppp.PollEvent() {
		switch event.Kind {
		case ppp.EventUp:
			settings := event.IPv4
			logInfo("simulated gateway: IPv4 up, " +
				rustdebug.IPv4Settings(settings.LocalAddress, settings.PeerAddress, settings.DNSServers))
			g.connectInternet(now)
		case ppp.EventDown:
			logInfo(fmt.Sprintf("simulated gateway: PPP down, %v", event.Reason))
			g.closeInternet()
		}
	}
}

// connectInternet builds the internet for the MTU just negotiated. A
// renegotiation that keeps the MTU keeps the internet, and with it open
// connections.
func (g *gateway) connectInternet(now time.Time) {
	mtu := g.ppp.MTU()
	if g.internet != nil && g.internet.mtu() == mtu {
		return
	}
	g.closeInternet()
	g.internet = newInternet(g.network, mtu, now)
}

func (g *gateway) closeInternet() {
	if g.internet != nil {
		g.internet.close()
		g.internet = nil
	}
}

func (g *gateway) exchangeDatagrams(now time.Time) {
	if g.internet == nil {
		return
	}
	for datagram, ok := g.ppp.ReceiveIPv4(); ok; datagram, ok = g.ppp.ReceiveIPv4() {
		g.internet.deliver(datagram)
	}
	g.internet.poll(now)
	for datagram, ok := g.internet.nextOutgoing(); ok; datagram, ok = g.internet.nextOutgoing() {
		if err := g.ppp.SendIPv4(datagram); err != nil {
			logDebug(fmt.Sprintf("simulated gateway: dropped a %d-byte datagram: %v", len(datagram), err))
		}
	}
}

// startServer returns the PPP server for network, started: it sends its
// first Configure-Request at once.
func startServer(network NetworkConfig, authentication *Authentication, now time.Time) (*ppp.Endpoint, error) {
	config := ppp.NewConfig(ppp.ServerRole(ppp.ServerConfig{
		LocalAddress:   network.GatewayAddress,
		PeerAddress:    network.ClientAddress,
		DNSServers:     network.DNSServers,
		Authentication: authentication,
	}))
	config.MRU = network.GatewayMRU
	server, err := ppp.NewEndpoint(config)
	if err != nil {
		return nil, err
	}
	server.Open(now)
	return server, nil
}
