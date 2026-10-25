package ipstack

// The tests up to TestTCPListenDropsUnspecifiedSrc follow the scenarios of
// smoltcp's interface tests that apply to bare IPv4 datagrams: UDP broadcasts,
// unspecified source addresses and TCP connections that are not accepted. The
// tests after them cover the rest of the interface.

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestHandleUDPBroadcast(t *testing.T) {
	t.Parallel()
	payload := []byte{0x48, 0x65, 0x6c, 0x6c, 0x6f}
	iface, sockets, _ := setup(t)
	handle := sockets.AddUDP(udp.NewSocket(udp.NewPacketBuffer(1, 15), udp.NewPacketBuffer(1, 15)))
	src := netip.AddrFrom4([4]byte{0x7f, 0x00, 0x00, 0x02})
	udpHeader := wire.UDPRepr{SrcPort: 67, DstPort: 68}
	header := wire.IPv4Repr{SrcAddr: src, DstAddr: limitedBroadcast, NextHeader: wire.IPProtocolUDP, PayloadLen: udpHeader.HeaderLen() + len(payload), HopLimit: 0x40}

	// Bind the socket to port 68.
	s := sockets.UDP(handle)
	if err := s.Bind(wire.ListenEndpoint{Port: 68}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if s.CanReceive() || !s.CanSend() {
		t.Fatalf("a new socket: CanReceive %t, CanSend %t", s.CanReceive(), s.CanSend())
	}
	datagram := udpBytes(header.SrcAddr, header.DstAddr, udpHeader, payload)

	// The packet should be handled by the bound UDP socket.
	reply, ok := iface.processUDP(sockets, header, datagram)
	assertNoPacket(t, reply, ok)

	// Make sure the payload of the UDP packet processUDP processed is
	// appended to the bound socket's receive buffer.
	s = sockets.UDP(handle)
	received, meta, err := s.Receive()
	want := udp.Metadata{Endpoint: netip.AddrPortFrom(src, 67), LocalAddress: limitedBroadcast}
	if err != nil || string(received) != string(payload) || meta != want {
		t.Errorf("Receive = %q from %+v, %v; want %q from %+v", received, meta, err, payload, want)
	}
}

func TestTCPNotAccepted(t *testing.T) {
	t.Parallel()
	iface, sockets, _ := setup(t)
	segment := wire.TCPRepr{
		SrcPort:   4242,
		DstPort:   4243,
		Control:   wire.TCPControlSYN,
		SeqNumber: wire.TCPSeqNumber(-10001),
		WindowLen: 256,
	}
	want := ipPacket{
		header: ipv4Header(localAddr, remoteAddr, wire.IPProtocolTCP, segment.BufferLen()),
		payload: tcpPayload{segment: wire.TCPRepr{
			SrcPort:   4243,
			DstPort:   4242,
			Control:   wire.TCPControlRST,
			SeqNumber: 0,
			AckNumber: wire.Some(wire.TCPSeqNumber(-10000)),
			WindowLen: 0,
		}},
	}

	header := ipv4Header(remoteAddr, localAddr, wire.IPProtocolTCP, segment.BufferLen())
	reply, ok := iface.processTCP(sockets, header, tcpBytes(remoteAddr, localAddr, segment))
	assertPacket(t, reply, ok, want)

	// Unspecified destination address.
	unspecified := netip.IPv4Unspecified()
	header = ipv4Header(remoteAddr, unspecified, wire.IPProtocolTCP, segment.BufferLen())
	reply, ok = iface.processTCP(sockets, header, tcpBytes(remoteAddr, unspecified, segment))
	assertNoPacket(t, reply, ok)
}

func TestTCPListenDropsUnspecifiedSrc(t *testing.T) {
	t.Parallel()
	iface, sockets, _ := setup(t)
	handle := sockets.AddTCP(newListener(t, 1234))
	segment := wire.TCPRepr{
		SrcPort:        65000,
		DstPort:        1234,
		Control:        wire.TCPControlSYN,
		SeqNumber:      0,
		WindowLen:      1024,
		MaxSegmentSize: wire.Some[uint16](1460),
	}
	unspecified := netip.IPv4Unspecified()
	header := ipv4Header(unspecified, loopbackAddr, wire.IPProtocolTCP, segment.BufferLen())

	reply, ok := iface.processTCP(sockets, header, tcpBytes(unspecified, loopbackAddr, segment))

	assertNoPacket(t, reply, ok)
	if !sockets.TCP(handle).IsListening() {
		t.Errorf("the listener is in state %v, want it still listening", sockets.TCP(handle).State())
	}
}

func TestUnmatchedSYNGetsRSTACK(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	device.deliver(tcpDatagram(remoteAddr, localAddr, syn(49500, 81)))

	iface.Poll(at(0), device, sockets)

	reply := onlySent(t, device)
	header, rst := parseTCP(t, reply.datagram)
	if header.SrcAddr != localAddr || header.DstAddr != remoteAddr || !reply.isReply {
		t.Errorf("RST %v (through the reply slot: %t), want one from %v to %v through it", header, reply.isReply, localAddr, remoteAddr)
	}
	wantACK := wire.Some(wire.TCPSeqNumber(-10001).Add(1))
	if rst.Control != wire.TCPControlRST || rst.AckNumber != wantACK || rst.SrcPort != 81 || rst.DstPort != 49500 {
		t.Errorf("reply %v, want RST|ACK %v from port 81 to 49500", rst, wantACK)
	}
}

func TestRSTIsNotAnsweredWithRST(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	rst := wire.TCPRepr{SrcPort: 49500, DstPort: 81, Control: wire.TCPControlRST, SeqNumber: 1, WindowLen: 0}
	device.deliver(tcpDatagram(remoteAddr, localAddr, rst))

	iface.Poll(at(0), device, sockets)

	assertNothingSent(t, device)
}

func TestFirstAcceptingTCPSocketGetsTheSegment(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	first := sockets.AddTCP(newListener(t, 80))
	second := sockets.AddTCP(newListener(t, 80))
	device.deliver(tcpDatagram(remoteAddr, localAddr, syn(49500, 80)))

	iface.Poll(at(0), device, sockets)

	if state := sockets.TCP(first).State(); state != tcp.StateSynReceived {
		t.Errorf("the first listener is %v, want %v", state, tcp.StateSynReceived)
	}
	if !sockets.TCP(second).IsListening() {
		t.Errorf("the second listener is %v, want it still listening", sockets.TCP(second).State())
	}

	// A listener added later takes the freed lowest slot, and so comes
	// first.
	sockets.Remove(first)
	third := sockets.AddTCP(newListener(t, 80))
	device.deliver(tcpDatagram(remoteAddr, localAddr, syn(49501, 80)))
	iface.Poll(at(time.Millisecond), device, sockets)

	if state := sockets.TCP(third).State(); state != tcp.StateSynReceived {
		t.Errorf("the listener in the freed slot is %v, want %v", state, tcp.StateSynReceived)
	}
	if !sockets.TCP(second).IsListening() {
		t.Errorf("the second listener is %v, want it still listening", sockets.TCP(second).State())
	}
}

// exampleResponse is the DNS server's answer, 203.0.113.80, to the first
// query a seed-0 interface makes for example.com's A records, whose
// transaction id is 0x54e.
var exampleResponse = []byte("\x05\x4e\x81\x80\x00\x01\x00\x01\x00\x00\x00\x00" +
	"\x07example\x03com\x00\x00\x01\x00\x01" +
	"\xc0\x0c\x00\x01\x00\x01\x00\x00\x01\x2c\x00\x04\xcb\x00\x71\x50")

// firstQueryPort is the source port of the first DNS query of a seed-0
// interface. Its transaction id and port are the second and third Uint16
// the seed gives, the interface having drawn the first.
const firstQueryPort = 57331

func TestUDPSocketsComeBeforeDNSSockets(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	server := netip.AddrFrom4([4]byte{192, 168, 1, 53})
	resolver := dns.NewSocket([]netip.Addr{server})
	query, err := resolver.StartQuery(iface.Context(), "example.com", wire.DNSTypeA)
	if err != nil {
		t.Fatalf("StartQuery: %v", err)
	}
	sockets.AddDNS(resolver)
	datagrams := sockets.AddUDP(newUDPSocket(t, wire.ListenEndpoint{Port: firstQueryPort}))
	response := udpDatagram(netip.AddrPortFrom(server, 53), netip.AddrPortFrom(localAddr, firstQueryPort), exampleResponse)
	iface.Poll(at(0), device, sockets)
	device.takeSent()

	device.deliver(response)
	iface.Poll(at(time.Millisecond), device, sockets)

	if !sockets.UDP(datagrams).CanReceive() {
		t.Fatal("the UDP socket in the later slot did not get the response")
	}
	if _, err := resolver.TakeQueryResult(query); !errors.Is(err, dns.ErrPending) {
		t.Errorf("the DNS query is %v, want it still pending", err)
	}

	// Without the UDP socket, the DNS socket takes the response.
	sockets.Remove(datagrams)
	device.deliver(response)
	iface.Poll(at(2*time.Millisecond), device, sockets)

	addresses, err := resolver.TakeQueryResult(query)
	if err != nil || len(addresses) != 1 || addresses[0] != netip.MustParseAddr("203.0.113.80") {
		t.Errorf("TakeQueryResult = %v, %v; want 203.0.113.80", addresses, err)
	}
}

func TestEmptyFrameCountsAsProcessed(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	device.deliver([]byte{})

	if result := iface.PollIngressSingle(at(0), device, sockets); result != IngressPacketProcessed {
		t.Errorf("PollIngressSingle of an empty frame = %v, want %v", result, IngressPacketProcessed)
	}
	device.deliver([]byte{}, []byte{})
	if result := iface.Poll(at(0), device, sockets); result != PollNone {
		t.Errorf("Poll of empty frames = %v, want %v", result, PollNone)
	}
}

func TestDroppedDatagramCountsAsChangingSockets(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	garbage := []byte{0x45, 0x00}
	device.deliver(garbage)

	if result := iface.PollIngressSingle(at(0), device, sockets); result != IngressSocketStateChanged {
		t.Errorf("PollIngressSingle of a malformed datagram = %v, want %v", result, IngressSocketStateChanged)
	}
	device.deliver(garbage)
	if result := iface.Poll(at(0), device, sockets); result != PollSocketStateChanged {
		t.Errorf("Poll of a malformed datagram = %v, want %v", result, PollSocketStateChanged)
	}
}

func TestPollWithNothingToDoChangesNothing(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	sockets.AddTCP(newListener(t, 80))

	result := iface.Poll(at(0), device, sockets)

	if result != PollNone || device.transmitRequests != 0 {
		t.Errorf("Poll = %v after %d transmit requests, want %v and none", result, device.transmitRequests, PollNone)
	}
}

// connection is a TCP connection a test established through the
// interface, from the peer's port 49500 to port 80 of the interface.
type connection struct {
	handle SocketHandle
	// remoteSeq and localSeq are the next sequence numbers of the peer and
	// of the socket.
	remoteSeq, localSeq wire.TCPSeqNumber
}

// establishedConnection completes a handshake from the peer to a listener on
// port 80 of the interface, through the interface, at 0 and 1 ms.
func establishedConnection(t *testing.T, iface *Interface, sockets *SocketSet, device *testingDevice) connection {
	t.Helper()
	handle := sockets.AddTCP(newListener(t, 80))
	request := syn(49500, 80)
	device.deliver(tcpDatagram(remoteAddr, localAddr, request))
	iface.Poll(at(0), device, sockets)
	_, synACK := parseTCP(t, onlySent(t, device).datagram)
	established := connection{handle: handle, remoteSeq: request.SeqNumber.Add(1), localSeq: synACK.SeqNumber.Add(1)}
	ack := wire.TCPRepr{
		SrcPort:   49500,
		DstPort:   80,
		SeqNumber: established.remoteSeq,
		AckNumber: wire.Some(established.localSeq),
		WindowLen: 1024,
	}
	device.deliver(tcpDatagram(remoteAddr, localAddr, ack))
	iface.Poll(at(time.Millisecond), device, sockets)
	if state := sockets.TCP(handle).State(); state != tcp.StateEstablished {
		t.Fatalf("after the handshake the socket is %v, want %v", state, tcp.StateEstablished)
	}
	assertNothingSent(t, device)
	return established
}

func TestSourceAddressChangeResetsTCPWithoutRST(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	handle := establishedConnection(t, iface, sockets, device).handle

	if err := iface.SetIPAddrs([]netip.Prefix{netip.MustParsePrefix("10.0.0.1/24")}); err != nil {
		t.Fatalf("SetIPAddrs: %v", err)
	}
	iface.Poll(at(2*time.Millisecond), device, sockets)

	if state := sockets.TCP(handle).State(); state != tcp.StateClosed {
		t.Errorf("the connection is %v, want %v", state, tcp.StateClosed)
	}
	assertNothingSent(t, device)
}

func TestPollAt(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	polled := pollTimes{iface: iface, sockets: sockets}

	if _, ok := iface.PollAt(at(0), sockets); ok {
		t.Error("PollAt of an empty set has a deadline")
	}

	client := newTCPSocket()
	if err := client.Connect(iface.Context(), netip.AddrPortFrom(remoteAddr, 80), wire.ListenEndpoint{Port: 49500}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	sockets.AddTCP(client)
	polled.assertPollAt(t, at(0), at(0))

	// The SYN leaves; the next deadline is its retransmission, after the
	// initial RTO of 1 s.
	iface.Poll(at(0), device, sockets)
	polled.assertPollAt(t, at(300*time.Millisecond), at(time.Second))

	// A datagram waiting to be sent asks for a poll now.
	datagrams := newUDPSocket(t, wire.ListenEndpoint{Port: 7})
	if err := datagrams.SendSlice([]byte("x"), udp.Metadata{Endpoint: netip.AddrPortFrom(remoteAddr, 7)}); err != nil {
		t.Fatalf("SendSlice: %v", err)
	}
	sockets.AddUDP(datagrams)
	polled.assertPollAt(t, at(300*time.Millisecond), at(300*time.Millisecond))
}

func TestPollAtOfANewDNSQueryIsThePast(t *testing.T) {
	t.Parallel()
	iface, sockets, _ := setup(t)
	resolver := dns.NewSocket([]netip.Addr{remoteAddr})
	if _, err := resolver.StartQuery(iface.Context(), "example.com", wire.DNSTypeA); err != nil {
		t.Fatalf("StartQuery: %v", err)
	}
	sockets.AddDNS(resolver)
	polled := pollTimes{iface: iface, sockets: sockets}

	polled.assertPollAt(t, at(time.Minute), time.Time{})
}

// pollTimes asks an interface when its sockets need the next poll.
type pollTimes struct {
	iface   *Interface
	sockets *SocketSet
}

// assertPollAt checks that PollAt at now is want.
func (p pollTimes) assertPollAt(t *testing.T, now, want time.Time) {
	t.Helper()
	got, ok := p.iface.PollAt(now, p.sockets)
	if !ok || !got.Equal(want) {
		t.Errorf("PollAt(%v) = %v, %t; want %v", now.Sub(time.Time{}), got.Sub(time.Time{}), ok, want.Sub(time.Time{}))
	}
}

func TestNewInterfacePanicsBelowMinMTU(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Errorf("NewInterface with an MTU of %d did not panic", MinMTU-1)
		}
	}()

	NewInterface(Config{}, DeviceCapabilities{MTU: MinMTU - 1}, time.Time{})
}

func TestNewInterfaceTakesMinMTU(t *testing.T) {
	t.Parallel()

	iface := NewInterface(Config{}, DeviceCapabilities{MTU: MinMTU}, time.Time{})

	if mtu := iface.Context().IPMTU(); mtu != MinMTU {
		t.Errorf("IPMTU = %d, want %d", mtu, MinMTU)
	}
}

func TestNewInterfaceMakesItsContext(t *testing.T) {
	t.Parallel()
	now := at(time.Hour)

	cx := NewInterface(Config{}, DeviceCapabilities{MTU: 576}, now).Context()

	if !cx.Now().Equal(now) || cx.IPMTU() != 576 {
		t.Errorf("the context is at %v with an IP MTU of %d, want %v and 576", cx.Now(), cx.IPMTU(), now)
	}
	if address, ok := cx.IPv4Addr(); ok || cx.HasIPAddr(foreignAddr) {
		t.Errorf("a new interface has the address %v, or takes %v as its own; want no address and any-IP off", address, foreignAddr)
	}
}

func TestSeedDeterminesTheSocketsRandomValues(t *testing.T) {
	t.Parallel()
	// The first Uint32 a socket draws, as a TCP socket does for its initial
	// sequence number, is the first ISN of smoltcp 0.14.0 for the same seed.
	// The interface has drawn the fragment identifier first: the seed's first
	// Uint16, unless that is 0, as it is for 0x2e80, which draws again.
	tests := []struct {
		seed        uint64
		firstSocket uint32
	}{
		{seed: 0, firstSocket: 0xfdecf8a2},
		{seed: 0x5EED, firstSocket: 0x9ee15bb3},
		{seed: 0x2e80, firstSocket: 0x4b4ee653},
	}
	for _, test := range tests {
		cx := NewInterface(Config{RandomSeed: test.seed}, DeviceCapabilities{MTU: 576}, time.Time{}).Context()

		if got := cx.Rand().Uint32(); got != test.firstSocket {
			t.Errorf("seed %#x: the sockets' first Uint32 is %#x, want %#x", test.seed, got, test.firstSocket)
		}
	}
}

func TestFragmentIdentifierIsTheFirstUint16ThatIsNotZero(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		seed       uint64
		identifier uint16
	}{
		{"the first Uint16", 0x5EED, 0xc7d6},
		{"the second Uint16 after a first of 0", 0x2e80, 0x8b47},
	}
	for _, test := range tests {
		random := socket.NewContext(time.Time{}, test.seed, MinMTU).Rand()

		if got := fragmentIdentifier(random); got != test.identifier {
			t.Errorf("%s: fragmentIdentifier with seed %#x = %#x, want %#x", test.name, test.seed, got, test.identifier)
		}
	}
}

func TestSetIPAddrsRefusesAddressesThatCannotBeTheInterfaces(t *testing.T) {
	t.Parallel()
	iface, _, _ := setup(t)
	for _, prefix := range []string{"224.0.0.1/4", "255.255.255.255/32", "2001:db8::1/64"} {
		err := iface.SetIPAddrs([]netip.Prefix{netip.PrefixFrom(localAddr, 24), netip.MustParsePrefix(prefix)})

		if !errors.Is(err, socket.ErrInvalidIPAddr) {
			t.Errorf("SetIPAddrs with %s = %v, want %v", prefix, err, socket.ErrInvalidIPAddr)
		}
	}
	// Every refused list holds localAddr but not loopbackAddr, so a change
	// would drop one of the two.
	if cx := iface.Context(); !cx.HasIPAddr(localAddr) || !cx.HasIPAddr(loopbackAddr) {
		t.Error("a refused list of addresses replaced the interface's")
	}
}

func TestSetAnyIPSetsTheContexts(t *testing.T) {
	t.Parallel()
	iface, _, _ := setup(t)

	iface.SetAnyIP(true)

	if !iface.Context().HasIPAddr(foreignAddr) {
		t.Error("with any-IP on, the context does not take every address as the interface's")
	}
}

func TestChecksumCapabilitiesAreTheDevices(t *testing.T) {
	t.Parallel()
	capabilities := DeviceCapabilities{MTU: testingDeviceMTU, Checksum: wire.ChecksumCapabilities{UDP: wire.ChecksumNone}}
	iface := NewInterface(Config{}, capabilities, time.Time{})
	if err := iface.SetIPAddrs([]netip.Prefix{netip.PrefixFrom(localAddr, 24)}); err != nil {
		t.Fatalf("SetIPAddrs: %v", err)
	}
	sockets, device := &SocketSet{}, newTestingDevice()
	echo := newUDPSocket(t, wire.ListenEndpoint{Port: 7})
	sockets.AddUDP(echo)
	datagram := udpDatagram(netip.AddrPortFrom(remoteAddr, 49500), netip.AddrPortFrom(localAddr, 7), []byte("ping"))
	packet := wire.UDPPacket(wire.IPv4Packet(datagram).Payload())
	packet.SetChecksum(packet.Checksum() + 1)
	device.deliver(datagram)

	iface.Poll(at(0), device, sockets)
	payload, meta, err := echo.Receive()
	if err != nil {
		t.Fatalf("a datagram with a wrong checksum did not arrive: %v", err)
	}
	if err := echo.SendSlice(bytes.Clone(payload), meta); err != nil {
		t.Fatalf("SendSlice: %v", err)
	}
	iface.Poll(at(time.Millisecond), device, sockets)

	_, sent := parseIPv4(t, onlySent(t, device).datagram)
	if checksum := wire.UDPPacket(sent).Checksum(); checksum != 0 {
		t.Errorf("the reply's UDP checksum is %#x, want none (0)", checksum)
	}
}

func TestDispatchIPPanicsOnTheUnspecifiedDestination(t *testing.T) {
	t.Parallel()
	iface, _, _ := setup(t)
	packet := newUDPPacket(ipv4Header(localAddr, netip.IPv4Unspecified(), wire.IPProtocolUDP, wire.UDPHeaderLen),
		wire.UDPRepr{SrcPort: 7, DstPort: 7}, nil)
	defer func() {
		if recover() == nil {
			t.Error("dispatchIP of a datagram for 0.0.0.0 did not panic")
		}
	}()

	iface.dispatchIP(func([]byte) { t.Error("the datagram was sent") }, packet)
}

func TestResultStringsAreTheVariantNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value interface{ String() string }
		want  string
	}{
		{PollNone, "None"},
		{PollSocketStateChanged, "SocketStateChanged"},
		{PollResult(7), "PollResult(7)"},
		{IngressNone, "None"},
		{IngressPacketProcessed, "PacketProcessed"},
		{IngressSocketStateChanged, "SocketStateChanged"},
		{PollIngressSingleResult(9), "PollIngressSingleResult(9)"},
	}
	for _, test := range tests {
		if got := test.value.String(); got != test.want {
			t.Errorf("String of %#v = %q, want %q", test.value, got, test.want)
		}
	}
}
