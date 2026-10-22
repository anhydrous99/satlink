package sim

import (
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/ppp"
)

const (
	// exchangeRounds bounds how many rounds a negotiation between two
	// endpoints that answer at once may take.
	exchangeRounds = 50
	// renegotiatedMRU is what a client renegotiating with the gateway asks
	// for: less than PPP's default, so that the gateway's MTU changes.
	renegotiatedMRU = 1200
	// clientUDPPort is the port the client sends its datagrams from.
	clientUDPPort = 40_000
)

// pppPair is a PPP client and a gateway talking straight to each other,
// without a link in between, at one moment.
type pppPair struct {
	t       *testing.T
	client  *ppp.Endpoint
	gateway *gateway
	now     time.Time
}

// newPPPPair returns a client that has started negotiating with g at now.
func newPPPPair(t *testing.T, g *gateway, now time.Time) *pppPair {
	t.Helper()
	return newPPPPairAskingFor(t, g, ppp.DefaultMRU, now)
}

// newPPPPairAskingFor returns a client that has started negotiating with g
// at now, asking for mru.
func newPPPPairAskingFor(t *testing.T, g *gateway, mru uint16, now time.Time) *pppPair {
	t.Helper()
	config := ppp.NewConfig(ppp.ClientRole(ppp.Credentials{}))
	config.MRU = mru
	client, err := ppp.NewEndpoint(config)
	if err != nil {
		t.Fatal(err)
	}
	client.Open(now)
	return &pppPair{t: t, client: client, gateway: g, now: now}
}

// startSecureEchoSession starts the session of the internet's secure echo
// listener, as a client connecting to it does, and closes it when the test
// ends.
func (i *internet) startSecureEchoSession(t *testing.T) *secureEcho {
	t.Helper()
	for _, c := range i.services.connections {
		if c.session.kind == serviceSecureEcho {
			c.session.secureEcho.start(sessionServerEnd, sessionClientEnd)
			t.Cleanup(c.session.secureEcho.close)
			return c.session.secureEcho
		}
	}
	t.Fatal("the internet has no secure echo listener")
	return nil
}

// awaitClientEvent moves bytes between the client and the gateway until
// the client reports an event of kind.
func (p *pppPair) awaitClientEvent(kind ppp.EventKind) {
	p.t.Helper()
	for range exchangeRounds {
		p.gateway.receive(p.client.TakeOutput(), p.now)
		p.gateway.advance(p.now)
		p.client.HandleInput(p.gateway.takeOutput(), p.now)
		for event, ok := p.client.PollEvent(); ok; event, ok = p.client.PollEvent() {
			if event.Kind == kind {
				return
			}
		}
	}
	p.t.Fatalf("the client never reported %v", kind)
}

func TestTheInternetComesWithIPv4AndGoesWithPPP(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g, err := startGateway(DefaultNetworkConfig(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	pair := newPPPPair(t, g, now)

	pair.awaitClientEvent(ppp.EventUp)
	up := g.internet != nil
	pair.client.Close(now)
	pair.awaitClientEvent(ppp.EventDown)

	if !up || g.internet != nil {
		t.Errorf("an internet while IPv4 was up: %v, after PPP went down: %v; want true, false", up, g.internet != nil)
	}
}

// This test records the process-wide logger, so it does not run in
// parallel.
func TestTheGatewayLogsIPv4Up(t *testing.T) {
	recorder := recordLogs(t)
	now := time.Now()
	g, err := startGateway(DefaultNetworkConfig(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)

	newPPPPair(t, g, now).awaitClientEvent(ppp.EventUp)

	want := logRecord{
		level: slog.LevelInfo,
		message: "simulated gateway: IPv4 up, Ipv4Settings { local_address: 10.64.0.1, " +
			"peer_address: Some(10.64.0.2), dns_servers: [10.64.0.1] }",
	}
	if got := recorder.recorded(); !slices.Contains(got, want) {
		t.Errorf("records %v, want one of them %v", got, want)
	}
}

// This test records the process-wide logger, so it does not run in
// parallel.
func TestTheGatewayLogsPPPDown(t *testing.T) {
	recorder := recordLogs(t)
	now := time.Now()
	g, err := startGateway(DefaultNetworkConfig(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	pair := newPPPPair(t, g, now)
	pair.awaitClientEvent(ppp.EventUp)

	pair.client.Close(now)
	pair.awaitClientEvent(ppp.EventDown)

	want := logRecord{level: slog.LevelInfo, message: "simulated gateway: PPP down, terminated by the peer"}
	if got := recorder.recorded(); !slices.Contains(got, want) {
		t.Errorf("records %v, want one of them %v", got, want)
	}
}

// What `satlink --simulate -vv` shows of a datagram the internet answers
// while the client starts PPP over: PPP is not open for IPv4 until the new
// negotiation ends, but the internet stays, so that a renegotiation that
// keeps the MTU keeps its connections. This test records the process-wide
// logger, so it does not run in parallel.
func TestTheGatewayLogsADatagramPPPCannotSend(t *testing.T) {
	recorder := recordLogs(t)
	now := time.Now()
	g, err := startGateway(DefaultNetworkConfig(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)
	pair := newPPPPair(t, g, now)
	pair.awaitClientEvent(ppp.EventUp)
	from := netip.AddrPortFrom(defaultClientAddress, clientUDPPort)
	echo := netip.AddrPortFrom(defaultGatewayAddress, echoPort)
	if err := pair.client.SendIPv4(UDPDatagram(from, echo, []byte("ping"))); err != nil {
		t.Fatal(err)
	}
	// The request for an echo, and right behind it the Configure-Request
	// of a client starting PPP over.
	request := pair.client.TakeOutput()
	renegotiation := newPPPPair(t, g, now).client.TakeOutput()

	g.receive(append(request, renegotiation...), now)
	g.advance(now)

	// The echo is 32 bytes: 20 of IPv4, 8 of UDP, and the 4 of the ping.
	want := logRecord{level: slog.LevelDebug, message: "simulated gateway: dropped a 32-byte datagram: the link is not open for IPv4"}
	if got := recorder.recorded(); !slices.Contains(got, want) {
		t.Errorf("records %v, want one of them %v", got, want)
	}
}

func TestTheInternetGoesWithTheCall(t *testing.T) {
	t.Parallel()
	now := time.Now()
	c, err := connectCircuit(DefaultConfig(), now)
	if err != nil {
		t.Fatal(err)
	}
	newPPPPair(t, c.gateway, now).awaitClientEvent(ppp.EventUp)

	c.close()

	if c.gateway.internet != nil {
		t.Error("the internet outlived its call")
	}
}

func TestTheGatewayRepeatsItsRequestToASilentClient(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g, err := startGateway(DefaultNetworkConfig(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	first := g.takeOutput()

	g.advance(now.Add(ppp.DefaultTimers().Restart))

	if len(first) == 0 || len(g.takeOutput()) == 0 {
		t.Error("the gateway did not repeat its Configure-Request once its restart timer ran out")
	}
}

// The internet is kept across a renegotiation that keeps the MTU, and with
// it open connections; the internet is built for the MTU again only when it
// changes.
func TestTheInternetOutlivesARenegotiationThatKeepsTheMTU(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g, err := startGateway(DefaultNetworkConfig(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	newPPPPair(t, g, now).awaitClientEvent(ppp.EventUp)
	before := g.internet

	g.connectInternet(now)

	if g.internet != before {
		t.Error("the same MTU brought a new internet")
	}
}

// A renegotiation that changes the MTU brings EventUp again, with no
// EventDown before it. The internet is then replaced, which drops the old
// one: its TLS sessions have to be closed, or their goroutines would outlive
// it.
func TestARenegotiationThatChangesTheMTUBuildsANewInternet(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g, err := startGateway(DefaultNetworkConfig(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)
	newPPPPair(t, g, now).awaitClientEvent(ppp.EventUp)
	before := g.internet
	session := before.startSecureEchoSession(t)

	newPPPPairAskingFor(t, g, renegotiatedMRU, now).awaitClientEvent(ppp.EventUp)

	if g.internet == nil || g.internet == before {
		t.Fatal("the renegotiation kept the old internet, or left none")
	}
	if mtu := g.internet.mtu(); mtu != renegotiatedMRU {
		t.Errorf("the new internet's MTU is %d; want the %d just negotiated", mtu, renegotiatedMRU)
	}
	if !session.conn.Closed() {
		t.Error("the old internet's TLS session is still open")
	}
}

func TestARestartOfPPPBuildsANewInternetForTheNewAddress(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g, err := startGateway(DefaultNetworkConfig(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)
	pair := newPPPPair(t, g, now)
	pair.awaitClientEvent(ppp.EventUp)
	before := g.internet
	newAddress := netip.MustParseAddr("10.64.0.3")

	if err := g.restartPPP(newAddress, now); err != nil {
		t.Fatal(err)
	}
	lostAtOnce := g.internet == nil
	pair.awaitClientEvent(ppp.EventUp)

	if !lostAtOnce || g.internet == nil || g.internet == before {
		t.Errorf("the old internet lost at once: %v; a new one once IPv4 was up again: %v; want both",
			lostAtOnce, g.internet != nil && g.internet != before)
	}
	if settings, _ := pair.client.IPv4(); settings.LocalAddress != newAddress {
		t.Errorf("the client got %v; want %v", settings.LocalAddress, newAddress)
	}
}

func TestARestartOfPPPThatCannotStartChangesNothing(t *testing.T) {
	t.Parallel()
	now := time.Now()
	g, err := startGateway(DefaultNetworkConfig(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)
	newPPPPair(t, g, now).awaitClientEvent(ppp.EventUp)
	server, internet := g.ppp, g.internet

	err = g.restartPPP(netip.Addr{}, now)

	if !errors.Is(err, ppp.ErrInvalidAddress) {
		t.Errorf("restarting with the zero address failed with %v; want ErrInvalidAddress", err)
	}
	if g.ppp != server || g.internet != internet || g.network.ClientAddress != defaultClientAddress {
		t.Error("the failed restart changed the gateway")
	}
}
