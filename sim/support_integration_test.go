package sim_test

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/ppp"
	"github.com/anhydrous99/satlink/sim"
)

// What the integration tests share: a handset ready for commands, a PPP
// client driven over a data call, hand-built datagrams, and a TCP/IP stack
// for the client side, with TLS on its connections.
//
// internal/simtest offers a short guard and ready handsets too, but it is
// built on this package for the tests of the packages above it. The
// simulator's own tests keep their set-up here, so that nothing simtest
// needs can change what they test.

const (
	// simulatorGuard is the simulator's escape guard in these tests:
	// short, so they run fast.
	simulatorGuard = 20 * time.Millisecond
	// clientGuard is a little more silence than the handset requires.
	clientGuard = 40 * time.Millisecond
	// pumpWait is how long one pump of the PPP client waits for input.
	pumpWait = 5 * time.Millisecond
	// patience is generous: nothing on an instant link takes nearly this
	// long.
	patience              = 10 * time.Second
	iridiumInternetNumber = "008816000025"

	dnsPort        = 53
	echoPort       = 7
	httpPort       = 80
	secureEchoPort = 443
	clientDNSPort  = 40_053
	dnsTypeA       = 1
	dnsClassIN     = 1

	// clientSeed seeds the client stack: the tests need no unpredictable
	// sequence numbers.
	clientSeed = 0x5EED
	// clientTCPBufferLen is per direction and connection of the client.
	clientTCPBufferLen = 4096
	// pppReadLen is how much one pump of the PPP client reads.
	pppReadLen = 2048
	// hostPrefixLen is the client's prefix: a single host on the link.
	hostPrefixLen = 32
)

var (
	gatewayAddress = netip.MustParseAddr("10.64.0.1")
	clientAddress  = netip.MustParseAddr("10.64.0.2")
	echoHost       = netip.MustParseAddr("192.0.2.7")
	webHost        = netip.MustParseAddr("192.0.2.80")
	secureHost     = netip.MustParseAddr("192.0.2.43")
	// renumberedAddress is what the gateway assigns instead of
	// clientAddress when the tests make it restart PPP.
	renumberedAddress = netip.MustParseAddr("10.64.0.3")
)

// fastConfig returns the default simulator, with a short escape guard.
func fastConfig() sim.Config {
	config := sim.DefaultConfig()
	config.EscapeGuard = simulatorGuard
	return config
}

// readyHandset returns a handset initialised the way an app starts — echo
// off, text-mode SMS — over a simulator that closes when the test ends.
func readyHandset(t *testing.T, config sim.Config) (*iridium.Handset, *sim.Control) {
	t.Helper()
	simulator := sim.New(config)
	handset := iridium.NewHandset(simulator)
	// Closing a simulator, which is what closing its handset does, cannot
	// fail.
	t.Cleanup(func() { _ = handset.Close() })
	if err := handset.Initialize(); err != nil {
		t.Fatalf("the simulated handset does not initialise: %v", err)
	}
	return handset, simulator.Control()
}

// callSettings returns Iridium's documented bearer and a guard time the
// simulator accepts.
func callSettings() iridium.DataCallSettings {
	settings := iridium.DefaultDataCallSettings()
	settings.Bearer = &iridium.BearerService{Speed: 71, Name: 0, ConnectionElement: 1}
	settings.ConnectTimeout = patience
	settings.EscapeGuard = clientGuard
	return settings
}

// dial places a data call to the Iridium internet with callSettings.
func dial(t *testing.T, handset *iridium.Handset) *iridium.DataCall {
	t.Helper()
	return dialWith(t, handset, callSettings())
}

// dialWith places a data call to the Iridium internet with settings.
func dialWith(t *testing.T, handset *iridium.Handset, settings iridium.DataCallSettings) *iridium.DataCall {
	t.Helper()
	call, err := handset.DialData(iridiumInternetNumber, settings)
	if err != nil {
		t.Fatalf("the data call does not connect: %v", err)
	}
	return call
}

// pppClient is a PPP client endpoint driven over a data call.
type pppClient struct {
	t        *testing.T
	call     *iridium.DataCall
	endpoint *ppp.Endpoint
}

// startPPP starts negotiating at once, as a dial-up client does after
// CONNECT.
func startPPP(t *testing.T, call *iridium.DataCall, credentials ppp.Credentials) *pppClient {
	t.Helper()
	config := ppp.NewConfig(ppp.ClientRole(credentials))
	// Keep CR and LF out of the server's frames, so the handset's NO
	// CARRIER cannot be mistaken for data.
	config.ReceiveACCM = 1<<'\r' | 1<<'\n'
	endpoint, err := ppp.NewEndpoint(config)
	if err != nil {
		t.Fatalf("an invalid client configuration: %v", err)
	}
	endpoint.Open(time.Now())
	client := &pppClient{t: t, call: call, endpoint: endpoint}
	client.flush()
	return client
}

// expectUp waits for IPv4 to come up and returns what was agreed.
func (c *pppClient) expectUp() ppp.IPv4Settings {
	c.t.Helper()
	event := c.nextEvent(patience)
	if event.Kind != ppp.EventUp {
		c.t.Fatalf("expected the link to come up, got %v: %v", event.Kind, event.Reason)
	}
	return event.IPv4
}

func (c *pppClient) nextEvent(timeout time.Duration) ppp.Event {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if event, ok := c.endpoint.PollEvent(); ok {
			return event
		}
		if !time.Now().Before(deadline) {
			c.t.Fatalf("no PPP event within %v", timeout)
		}
		c.pump(pumpWait)
	}
}

// pump reads what arrived (waiting at most wait), runs the PPP timers and
// writes what PPP wants sent.
func (c *pppClient) pump(wait time.Duration) {
	c.t.Helper()
	buffer := make([]byte, pppReadLen)
	received, err := c.call.ReadTimeout(buffer, wait)
	if err != nil {
		c.t.Fatalf("the simulator failed: %v", err)
	}
	now := time.Now()
	c.endpoint.HandleInput(buffer[:received], now)
	if due, ok := c.endpoint.NextTimeout(); ok && !due.After(now) {
		c.endpoint.HandleTimeout(now)
	}
	c.flush()
}

func (c *pppClient) send(datagram []byte) {
	c.t.Helper()
	if err := c.endpoint.SendIPv4(datagram); err != nil {
		c.t.Fatalf("IPv4 is not up: %v", err)
	}
	c.flush()
}

// close ends the PPP session, as an app does before hanging up.
func (c *pppClient) close() {
	c.endpoint.Close(time.Now())
	c.flush()
}

// receive returns the next datagram, waiting at most timeout; false when
// none came.
func (c *pppClient) receive(timeout time.Duration) ([]byte, bool) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if datagram, ok := c.endpoint.ReceiveIPv4(); ok {
			return datagram, true
		}
		if !time.Now().Before(deadline) {
			return nil, false
		}
		c.pump(pumpWait)
	}
}

// mustReceive returns the next datagram, failing the test if none comes in
// time.
func (c *pppClient) mustReceive(what string) []byte {
	c.t.Helper()
	datagram, ok := c.receive(patience)
	if !ok {
		c.t.Fatalf("no %s arrived", what)
	}
	return datagram
}

func (c *pppClient) mtu() int {
	return c.endpoint.MTU()
}

func (c *pppClient) flush() {
	c.t.Helper()
	if output := c.endpoint.TakeOutput(); len(output) > 0 {
		if err := c.call.WriteAll(output); err != nil {
			c.t.Fatalf("the simulator failed: %v", err)
		}
	}
}

// readUntil reads from transport until needle has arrived, and returns all
// it read.
func readUntil(t *testing.T, transport iridium.Transport, needle string) []byte {
	t.Helper()
	deadline := time.Now().Add(patience)
	var received []byte
	buffer := make([]byte, 512)
	for !bytes.Contains(received, []byte(needle)) {
		if !time.Now().Before(deadline) {
			t.Fatalf("never received %q, only %q", needle, received)
		}
		count, err := transport.ReadTimeout(buffer, pumpWait)
		if err != nil {
			t.Fatalf("the simulator failed: %v", err)
		}
		received = append(received, buffer[:count]...)
	}
	return received
}

// parseUDP returns the source, destination and payload of a UDP datagram
// whose checksums verify.
func parseUDP(t *testing.T, datagram []byte) (source, destination netip.AddrPort, payload []byte) {
	t.Helper()
	checksums := wire.ChecksumCapabilities{}
	packet, err := wire.NewIPv4Packet(datagram)
	if err != nil {
		t.Fatalf("not an IPv4 datagram: %v", err)
	}
	ip, err := wire.ParseIPv4Repr(packet, checksums)
	if err != nil || ip.NextHeader != wire.IPProtocolUDP {
		t.Fatalf("not a valid IPv4 header for UDP: %v, %v", ip, err)
	}
	segment, err := wire.NewUDPPacket(packet.Payload())
	if err != nil {
		t.Fatalf("not a UDP datagram: %v", err)
	}
	udp, err := wire.ParseUDPRepr(segment, ip.SrcAddr, ip.DstAddr, checksums)
	if err != nil {
		t.Fatalf("not a valid UDP checksum: %v", err)
	}
	return netip.AddrPortFrom(ip.SrcAddr, udp.SrcPort), netip.AddrPortFrom(ip.DstAddr, udp.DstPort),
		bytes.Clone(segment.Payload())
}

// dnsQueryDatagram returns a UDP datagram from the client asking the
// gateway's DNS for name's A record.
func dnsQueryDatagram(id uint16, name string) []byte {
	query := binary.BigEndian.AppendUint16(nil, id)
	// A standard query asking for recursion, with one question.
	query = append(query, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0)
	for label := range strings.SplitSeq(name, ".") {
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0)
	query = binary.BigEndian.AppendUint16(query, dnsTypeA)
	query = binary.BigEndian.AppendUint16(query, dnsClassIN)
	return sim.UDPDatagram(netip.AddrPortFrom(clientAddress, clientDNSPort), netip.AddrPortFrom(gatewayAddress, dnsPort), query)
}

// dnsAnswer is what a DNS response datagram said.
type dnsAnswer struct {
	id        uint16
	rcode     wire.DNSRcode
	addresses []netip.Addr
}

func parseDNSAnswer(t *testing.T, datagram []byte) dnsAnswer {
	t.Helper()
	source, destination, payload := parseUDP(t, datagram)
	if source != netip.AddrPortFrom(gatewayAddress, dnsPort) || destination != netip.AddrPortFrom(clientAddress, clientDNSPort) {
		t.Fatalf("a DNS answer from %v to %v", source, destination)
	}
	packet, err := wire.NewDNSPacket(payload)
	if err != nil {
		t.Fatalf("not a DNS message: %v", err)
	}
	_, records, err := wire.ParseDNSQuestion(packet.Payload())
	if err != nil {
		t.Fatalf("no question in the answer: %v", err)
	}
	answer := dnsAnswer{id: packet.TransactionID(), rcode: packet.Rcode()}
	for range packet.AnswerRecordCount() {
		var record wire.DNSRecord
		if record, records, err = wire.ParseDNSRecord(records); err != nil {
			t.Fatalf("not an answer record: %v", err)
		}
		if record.Data.Type == wire.DNSTypeA {
			answer.addresses = append(answer.addresses, record.Data.Address)
		}
	}
	return answer
}

// clientNetwork is a TCP/IP stack on the app's side of the PPP link.
type clientNetwork struct {
	t       *testing.T
	ppp     *pppClient
	iface   *ipstack.Interface
	device  *clientDevice
	sockets ipstack.SocketSet
	control *sim.Control
	// sessions are the TLS clients on connections of the network, whose
	// records every exchange moves.
	sessions []*tlsSession
}

// connectNetwork brings PPP up over a fresh data call and configures the
// stack with the address it was given.
func connectNetwork(t *testing.T, config sim.Config) *clientNetwork {
	t.Helper()
	handset, control := readyHandset(t, config)
	return connectNetworkThrough(t, dial(t, handset), control)
}

// connectNetworkThrough brings PPP up over call, a data call to the
// simulator that control steers.
func connectNetworkThrough(t *testing.T, call *iridium.DataCall, control *sim.Control) *clientNetwork {
	t.Helper()
	client := startPPP(t, call, ppp.Credentials{})
	settings := client.expectUp()
	device := &clientDevice{}
	iface := ipstack.NewInterface(ipstack.Config{RandomSeed: clientSeed}, ipstack.DeviceCapabilities{MTU: client.mtu()}, time.Now())
	if err := iface.SetIPAddrs([]netip.Prefix{netip.PrefixFrom(settings.LocalAddress, hostPrefixLen)}); err != nil {
		t.Fatalf("the address IPCP gave: %v", err)
	}
	return &clientNetwork{t: t, ppp: client, iface: iface, device: device, control: control}
}

// connectTCP opens a TCP connection; the SYN goes out on the next
// exchange.
func (n *clientNetwork) connectTCP(address netip.Addr, port, localPort uint16) ipstack.SocketHandle {
	n.t.Helper()
	socket := tcp.NewSocket(tcp.NewSocketBuffer(clientTCPBufferLen), tcp.NewSocketBuffer(clientTCPBufferLen))
	if err := socket.Connect(n.iface.Context(), netip.AddrPortFrom(address, port), wire.ListenEndpoint{Port: localPort}); err != nil {
		n.t.Fatalf("the connection does not start: %v", err)
	}
	return n.sockets.AddTCP(socket)
}

// startLookup starts looking up name's A record with the stack's own
// resolver.
func (n *clientNetwork) startLookup(server netip.Addr, name string) (ipstack.SocketHandle, dns.QueryHandle) {
	n.t.Helper()
	resolver := n.sockets.AddDNS(dns.NewSocket([]netip.Addr{server}))
	query, err := n.sockets.DNS(resolver).StartQuery(n.iface.Context(), name, wire.DNSTypeA)
	if err != nil {
		n.t.Fatalf("the lookup does not start: %v", err)
	}
	return resolver, query
}

// runUntil moves traffic until done holds, and reports false if it never
// did.
func (n *clientNetwork) runUntil(done func(*ipstack.SocketSet) bool) bool {
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		n.exchange()
		if done(&n.sockets) {
			return true
		}
	}
	return false
}

func (n *clientNetwork) exchange() {
	n.ppp.pump(pumpWait)
	for datagram, ok := n.ppp.receive(0); ok; datagram, ok = n.ppp.receive(0) {
		n.device.received = append(n.device.received, datagram)
	}
	n.iface.Poll(time.Now(), n.device, &n.sockets)
	for _, datagram := range n.device.takeTransmitted() {
		n.ppp.send(datagram)
	}
	for _, session := range n.sessions {
		session.exchangeRecords(n.sockets.TCP(session.handle))
	}
}

// startTLS starts a TLS client for secure.test, trusting the test CA only,
// on the connection handle; every exchange moves its records.
func (n *clientNetwork) startTLS(handle ipstack.SocketHandle) *tlsSession {
	n.t.Helper()
	socket := n.sockets.TCP(handle)
	session := &tlsSession{
		handle: handle,
		client: sim.NewTLSClient(n.t, socket.LocalEndpoint(), socket.RemoteEndpoint(), nil),
	}
	n.sessions = append(n.sessions, session)
	return session
}

// tlsSession is a TLS client on a connection of the client network.
type tlsSession struct {
	handle ipstack.SocketHandle
	client *sim.TLSClient
}

// exchangeRecords moves records between the client's session and its
// connection, both ways, until neither direction takes more. The client's
// answers are awaited only while it reads: once it has read the server's
// close_notify it reads no more.
func (s *tlsSession) exchangeRecords(socket *tcp.Socket) {
	for {
		sent := movedOrNothing(socket.Send(s.client.Conn.Extract))
		taken := movedOrNothing(socket.Receive(s.takeRecords))
		if sent == 0 && taken == 0 {
			return
		}
	}
}

// movedOrNothing returns how many bytes a socket moved: none when it
// reports an error, as a connection that can no longer send or receive
// takes nothing.
func movedOrNothing(count int, err error) int {
	if err != nil {
		return 0
	}
	return count
}

// takeRecords hands records to the client's session and waits until it has
// dealt with them; it takes none once the session has ended.
func (s *tlsSession) takeRecords(records []byte) int {
	if len(records) == 0 || !s.client.IsReading() {
		return 0
	}
	taken := s.client.Conn.Inject(records)
	s.client.Conn.AwaitReader(patience)
	return taken
}

// clientDevice is the client stack's device: datagrams from and for the
// PPP client.
type clientDevice struct {
	received    [][]byte
	transmitted [][]byte
}

var _ ipstack.Device = (*clientDevice)(nil)

func (d *clientDevice) Receive(time.Time) ([]byte, ipstack.TransmitSlot, bool) {
	if len(d.received) == 0 {
		return nil, nil, false
	}
	datagram := d.received[0]
	d.received = d.received[1:]
	return datagram, d.transmit, true
}

func (d *clientDevice) Transmit(time.Time) ipstack.TransmitSlot {
	return d.transmit
}

func (d *clientDevice) transmit(datagram []byte) {
	d.transmitted = append(d.transmitted, datagram)
}

// takeTransmitted returns what the stack sent, and forgets it.
func (d *clientDevice) takeTransmitted() [][]byte {
	transmitted := d.transmitted
	d.transmitted = nil
	return transmitted
}
