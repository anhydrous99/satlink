package udp

// The tests of the UDP socket cover binding, sending, receiving and the
// metadata of datagrams. The hop limit is always the default, so there are no
// hop-limit tests.

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

const (
	localPort  uint16 = 53
	remotePort uint16 = 49500
	// payloadOctetsPerPacket is how much payload storage each datagram slot of
	// the test buffers has.
	payloadOctetsPerPacket = 16
	// testIPMTU is the IP MTU of the test context.
	testIPMTU = 1500
)

var (
	localAddr  = netip.AddrFrom4([4]byte{192, 168, 1, 1})
	remoteAddr = netip.AddrFrom4([4]byte{192, 168, 1, 2})
	// otherAddr is an address of the test network that is neither the
	// socket's nor its peer's.
	otherAddr = netip.AddrFrom4([4]byte{192, 168, 1, 3})

	// localEnd is the local port on the local address, as Bind takes it.
	localEnd = wire.ListenEndpoint{Addr: localAddr, Port: localPort}
	// localPortOnly is a bare port, the local port on every address.
	localPortOnly = wire.ListenEndpoint{Port: localPort}
	remoteEnd     = netip.AddrPortFrom(remoteAddr, remotePort)
	// toRemote is the metadata of a datagram for remoteEnd, from an address
	// the socket chooses.
	toRemote = Metadata{Endpoint: remoteEnd}

	testPayload = []byte("abcdef")

	localIPRepr = wire.IPv4Repr{
		SrcAddr:    localAddr,
		DstAddr:    remoteAddr,
		NextHeader: wire.IPProtocolUDP,
		PayloadLen: wire.UDPHeaderLen + len(testPayload),
		HopLimit:   wire.DefaultHopLimit,
	}
	remoteIPRepr = wire.IPv4Repr{
		SrcAddr:    remoteAddr,
		DstAddr:    localAddr,
		NextHeader: wire.IPProtocolUDP,
		PayloadLen: wire.UDPHeaderLen + len(testPayload),
		HopLimit:   wire.DefaultHopLimit,
	}
	// badIPRepr is a datagram from the peer to an address that is not the
	// socket's.
	badIPRepr = wire.IPv4Repr{
		SrcAddr:    remoteAddr,
		DstAddr:    otherAddr,
		NextHeader: wire.IPProtocolUDP,
		PayloadLen: wire.UDPHeaderLen + len(testPayload),
		HopLimit:   wire.DefaultHopLimit,
	}
	localUDPRepr  = wire.UDPRepr{SrcPort: localPort, DstPort: remotePort}
	remoteUDPRepr = wire.UDPRepr{SrcPort: remotePort, DstPort: localPort}
)

// buffer returns a test buffer of packets datagram slots with 16 payload
// octets each.
func buffer(packets int) *PacketBuffer {
	return NewPacketBuffer(packets, payloadOctetsPerPacket*packets)
}

// remoteMetadataWithLocal is the metadata of a datagram the peer sent to the
// local address, or of one for the peer from the local address.
func remoteMetadataWithLocal() Metadata {
	return Metadata{Endpoint: remoteEnd, LocalAddress: localAddr}
}

// testContext returns a context at the epoch, with seed 0, an IP MTU of 1500
// and the addresses 192.168.1.1/24 and 127.0.0.1/8.
func testContext(t *testing.T) *socket.Context {
	t.Helper()
	cx := socket.NewContext(time.Time{}, 0, testIPMTU)
	addresses := []netip.Prefix{netip.MustParsePrefix("192.168.1.1/24"), netip.MustParsePrefix("127.0.0.1/8")}
	if err := cx.SetIPAddrs(addresses); err != nil {
		t.Fatalf("SetIPAddrs: %v", err)
	}
	return cx
}

func mustBind(t *testing.T, s *Socket, local wire.ListenEndpoint) {
	t.Helper()
	if err := s.Bind(local); err != nil {
		t.Fatalf("Bind(%v): %v", local, err)
	}
}

// sentDatagram is what a socket handed to its EmitFunc.
type sentDatagram struct {
	ip      wire.IPv4Repr
	header  wire.UDPRepr
	payload []byte
}

// dispatch runs Dispatch with an EmitFunc that records every datagram,
// copying its payload, and returns what was emitted.
func dispatch(cx *socket.Context, s *Socket) []sentDatagram {
	var sent []sentDatagram
	s.Dispatch(cx, func(ip wire.IPv4Repr, header wire.UDPRepr, payload []byte) {
		sent = append(sent, sentDatagram{ip: ip, header: header, payload: bytes.Clone(payload)})
	})
	return sent
}

// mustSendSlice queues a copy of data for meta.Endpoint.
func mustSendSlice(t *testing.T, s *Socket, data []byte, meta Metadata) {
	t.Helper()
	if err := s.SendSlice(data, meta); err != nil {
		t.Fatalf("SendSlice of %d octets: %v", len(data), err)
	}
}

// assertSentLocalDatagram checks that sent holds exactly one datagram, the
// test payload from localEnd to remoteEnd.
func assertSentLocalDatagram(t *testing.T, sent []sentDatagram) {
	t.Helper()
	if len(sent) != 1 {
		t.Fatalf("emitted %d datagrams, want 1", len(sent))
	}
	got := sent[0]
	if got.ip != localIPRepr || got.header != localUDPRepr || !bytes.Equal(got.payload, testPayload) {
		t.Errorf("emitted %v %v %q, want %v %v %q", got.ip, got.header, got.payload, localIPRepr, localUDPRepr, testPayload)
	}
}

// assertReceived checks a received datagram against the test payload from
// the peer to the local address.
func assertReceived(t *testing.T, payload []byte, meta Metadata, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("got error %v, want the test datagram", err)
	}
	if !bytes.Equal(payload, testPayload) || meta != remoteMetadataWithLocal() {
		t.Errorf("got %q from %+v, want %q from %+v", payload, meta, testPayload, remoteMetadataWithLocal())
	}
}

func TestBindUnaddressable(t *testing.T) {
	t.Parallel()
	s := NewSocket(buffer(0), buffer(0))

	err := s.Bind(wire.ListenEndpoint{Port: 0})

	if !errors.Is(err, ErrUnaddressable) {
		t.Errorf("Bind(port 0) = %v, want %v", err, ErrUnaddressable)
	}
}

func TestBindTwice(t *testing.T) {
	t.Parallel()
	s := NewSocket(buffer(0), buffer(0))

	first := s.Bind(wire.ListenEndpoint{Port: 1})
	second := s.Bind(wire.ListenEndpoint{Port: 2})

	if first != nil {
		t.Errorf("first Bind = %v, want nil", first)
	}
	if !errors.Is(second, ErrInvalidState) {
		t.Errorf("second Bind = %v, want %v", second, ErrInvalidState)
	}
}

func TestSendUnaddressable(t *testing.T) {
	t.Parallel()
	s := NewSocket(buffer(0), buffer(1))

	if err := s.SendSlice(testPayload, toRemote); !errors.Is(err, ErrUnaddressable) {
		t.Errorf("SendSlice on an unbound socket = %v, want %v", err, ErrUnaddressable)
	}
	mustBind(t, s, localPortOnly)
	unspecified := Metadata{Endpoint: netip.AddrPortFrom(netip.IPv4Unspecified(), remotePort)}
	if err := s.SendSlice(testPayload, unspecified); !errors.Is(err, ErrUnaddressable) {
		t.Errorf("SendSlice to 0.0.0.0 = %v, want %v", err, ErrUnaddressable)
	}
	portZero := Metadata{Endpoint: netip.AddrPortFrom(remoteAddr, 0)}
	if err := s.SendSlice(testPayload, portZero); !errors.Is(err, ErrUnaddressable) {
		t.Errorf("SendSlice to port 0 = %v, want %v", err, ErrUnaddressable)
	}
	if err := s.SendSlice(testPayload, toRemote); err != nil {
		t.Errorf("SendSlice to the peer = %v, want nil", err)
	}
}

func TestSendWithSource(t *testing.T) {
	t.Parallel()
	s := NewSocket(buffer(0), buffer(1))
	mustBind(t, s, localPortOnly)

	err := s.SendSlice(testPayload, remoteMetadataWithLocal())

	if err != nil {
		t.Errorf("SendSlice with a local address = %v, want nil", err)
	}
}

func TestSendDispatch(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(buffer(0), buffer(1))
	mustBind(t, s, localEnd)

	if !s.CanSend() {
		t.Fatal("a new socket cannot send")
	}
	if sent := dispatch(cx, s); len(sent) != 0 {
		t.Fatalf("Dispatch with nothing queued emitted %d datagrams", len(sent))
	}

	mustSendSlice(t, s, testPayload, toRemote)
	if err := s.SendSlice([]byte("123456"), toRemote); !errors.Is(err, ErrBufferFull) {
		t.Errorf("SendSlice into a full buffer = %v, want %v", err, ErrBufferFull)
	}
	if s.CanSend() {
		t.Error("CanSend with a full buffer")
	}

	assertSentLocalDatagram(t, dispatch(cx, s))
	if !s.CanSend() {
		t.Error("a sent datagram was not dequeued")
	}
}

func TestRecvProcess(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(buffer(1), buffer(0))
	mustBind(t, s, localPortOnly)

	if s.CanReceive() {
		t.Error("a new socket can receive")
	}
	if _, _, err := s.Receive(); !errors.Is(err, ErrExhausted) {
		t.Errorf("Receive on a new socket = %v, want %v", err, ErrExhausted)
	}

	if !s.Accepts(cx, remoteIPRepr, remoteUDPRepr) {
		t.Fatal("the socket does not accept the peer's datagram")
	}
	s.Process(remoteIPRepr, remoteUDPRepr, testPayload)
	if !s.CanReceive() {
		t.Fatal("the socket cannot receive a processed datagram")
	}

	if !s.Accepts(cx, remoteIPRepr, remoteUDPRepr) {
		t.Fatal("the socket does not accept the peer's second datagram")
	}
	// The receive buffer holds one datagram: the second one is dropped.
	s.Process(remoteIPRepr, remoteUDPRepr, testPayload)

	payload, meta, err := s.Receive()
	assertReceived(t, payload, meta, err)
	if s.CanReceive() {
		t.Error("the datagram that did not fit was queued")
	}
}

func TestDoesntAcceptWrongPort(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(buffer(1), buffer(0))
	mustBind(t, s, localPortOnly)

	header := remoteUDPRepr
	if !s.Accepts(cx, remoteIPRepr, header) {
		t.Error("the socket does not accept a datagram for its port")
	}
	header.DstPort++
	if s.Accepts(cx, remoteIPRepr, header) {
		t.Error("the socket accepts a datagram for another port")
	}
}

func TestDoesntAcceptWrongIP(t *testing.T) {
	t.Parallel()
	cx := testContext(t)

	portBound := NewSocket(buffer(1), buffer(0))
	mustBind(t, portBound, localPortOnly)
	ipBound := NewSocket(buffer(1), buffer(0))
	mustBind(t, ipBound, localEnd)

	if !portBound.Accepts(cx, badIPRepr, remoteUDPRepr) {
		t.Error("a socket bound to a port only refuses a datagram for another address")
	}
	if ipBound.Accepts(cx, badIPRepr, remoteUDPRepr) {
		t.Error("a socket bound to an address accepts a datagram for another address")
	}
}

func TestSendLargePacket(t *testing.T) {
	t.Parallel()
	// buffer(4) creates a payload buffer of size 16*4
	s := NewSocket(buffer(0), buffer(4))
	mustBind(t, s, localEnd)
	tooLarge := []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdefx")

	if err := s.SendSlice(tooLarge, toRemote); !errors.Is(err, ErrBufferFull) {
		t.Errorf("SendSlice of %d octets = %v, want %v", len(tooLarge), err, ErrBufferFull)
	}
	if err := s.SendSlice(tooLarge[:payloadOctetsPerPacket*4], toRemote); err != nil {
		t.Errorf("SendSlice of the whole buffer = %v, want nil", err)
	}
}

func TestProcessEmptyPayload(t *testing.T) {
	t.Parallel()
	s := NewSocket(NewPacketBuffer(1, 0), buffer(0))
	mustBind(t, s, localPortOnly)
	header := wire.UDPRepr{SrcPort: remotePort, DstPort: localPort}

	s.Process(remoteIPRepr, header, nil)

	payload, meta, err := s.Receive()
	if err != nil || len(payload) != 0 || meta != remoteMetadataWithLocal() {
		t.Errorf("Receive = %q, %+v, %v; want an empty datagram from %+v", payload, meta, err, remoteMetadataWithLocal())
	}
}

func TestABoundSocketIsOpen(t *testing.T) {
	t.Parallel()
	s := NewSocket(NewPacketBuffer(1, 0), buffer(0))
	if s.IsOpen() {
		t.Fatal("an unbound socket is open")
	}

	mustBind(t, s, localPortOnly)

	if !s.IsOpen() {
		t.Error("a bound socket is not open")
	}
}

func TestBindRefusesAnAddressThatIsNotIPv4(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"2001:db8::1", "::ffff:192.168.1.1"} {
		s := NewSocket(buffer(0), buffer(0))

		err := s.Bind(wire.ListenEndpoint{Addr: netip.MustParseAddr(address), Port: localPort})

		if !errors.Is(err, ErrUnaddressable) || s.IsOpen() {
			t.Errorf("Bind to %s = %v, open %t; want %v and unbound", address, err, s.IsOpen(), ErrUnaddressable)
		}
	}
}

func TestSendRefusesAddressesThatAreNotIPv4(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		meta Metadata
	}{
		{"IPv6 destination", Metadata{Endpoint: netip.MustParseAddrPort("[2001:db8::2]:49500")}},
		{"IPv4-mapped destination", Metadata{Endpoint: netip.MustParseAddrPort("[::ffff:192.168.1.2]:49500")}},
		{"zero destination address", Metadata{Endpoint: netip.AddrPortFrom(netip.Addr{}, remotePort)}},
		{"IPv6 local address", Metadata{Endpoint: remoteEnd, LocalAddress: netip.MustParseAddr("2001:db8::1")}},
		{"IPv4-mapped local address", Metadata{Endpoint: remoteEnd, LocalAddress: netip.MustParseAddr("::ffff:192.168.1.1")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := NewSocket(buffer(0), buffer(1))
			mustBind(t, s, localPortOnly)

			_, sendErr := s.Send(len(testPayload), test.meta)
			sendSliceErr := s.SendSlice(testPayload, test.meta)

			for operation, err := range map[string]error{"Send": sendErr, "SendSlice": sendSliceErr} {
				if !errors.Is(err, ErrUnaddressable) {
					t.Errorf("%s = %v, want %v", operation, err, ErrUnaddressable)
				}
			}
			if s.SendQueue() != 0 || !s.CanSend() {
				t.Errorf("%d octets queued; want nothing queued", s.SendQueue())
			}
		})
	}
}

func TestSendReturnsThePayloadToFill(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(buffer(0), buffer(1))
	mustBind(t, s, localEnd)

	payload, err := s.Send(len(testPayload), toRemote)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	copy(payload, testPayload)

	assertSentLocalDatagram(t, dispatch(cx, s))
}

// The size is checked first, so an unbound socket, which refuses every
// datagram, panics all the same.
func TestSendPanicsOnANegativeSize(t *testing.T) {
	t.Parallel()
	for _, bound := range []bool{true, false} {
		t.Run(fmt.Sprintf("bound %t", bound), func(t *testing.T) {
			t.Parallel()
			s := NewSocket(buffer(0), buffer(1))
			if bound {
				mustBind(t, s, localEnd)
			}
			defer func() {
				if recovered, want := recover(), "udp: Send: size -1 is negative"; recovered != want {
					t.Errorf("Send(-1) panicked with %v, want %q", recovered, want)
				}
				if s.SendQueue() != 0 || !s.CanSend() {
					t.Error("Send(-1) queued something")
				}
			}()

			_, _ = s.Send(-1, toRemote)
		})
	}
}

func TestDispatchChoosesTheSourceAddress(t *testing.T) {
	t.Parallel()
	loopbackPeer := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.2"), remotePort)
	otherLocal := netip.MustParseAddr("192.168.1.9")
	tests := []struct {
		name       string
		bind       wire.ListenEndpoint
		meta       Metadata
		wantSource netip.Addr
	}{
		{"the metadata's local address", localEnd, Metadata{Endpoint: remoteEnd, LocalAddress: otherLocal}, otherLocal},
		{"the bound address", wire.ListenEndpoint{Addr: otherLocal, Port: localPort}, toRemote, otherLocal},
		{"the interface's address in the destination's network", localPortOnly, Metadata{Endpoint: loopbackPeer}, netip.MustParseAddr("127.0.0.1")},
		{"the interface's first address", localPortOnly, Metadata{Endpoint: netip.MustParseAddrPort("10.0.0.1:49500")}, localAddr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cx := testContext(t)
			s := NewSocket(buffer(0), buffer(1))
			mustBind(t, s, test.bind)
			mustSendSlice(t, s, testPayload, test.meta)

			sent := dispatch(cx, s)
			if len(sent) != 1 {
				t.Fatalf("Dispatch emitted %d datagrams, want one", len(sent))
			}

			if got := sent[0].ip.SrcAddr; got != test.wantSource {
				t.Errorf("sent from %v, want %v", got, test.wantSource)
			}
			if got := sent[0].ip.DstAddr; got != test.meta.Endpoint.Addr() {
				t.Errorf("sent to %v, want %v", got, test.meta.Endpoint.Addr())
			}
		})
	}
}

func TestDispatchDropsADatagramWithoutASourceAddress(t *testing.T) {
	t.Parallel()
	cx := socket.NewContext(time.Time{}, 0, testIPMTU)
	s := NewSocket(buffer(0), buffer(1))
	mustBind(t, s, localPortOnly)
	mustSendSlice(t, s, testPayload, toRemote)

	sent := dispatch(cx, s)

	if len(sent) != 0 {
		t.Errorf("Dispatch emitted %d datagrams, want none", len(sent))
	}
	if s.SendQueue() != 0 {
		t.Errorf("%d octets still queued, want the datagram dropped", s.SendQueue())
	}
}

func TestPollAtIsNowWhileDatagramsWait(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(buffer(0), buffer(1))
	mustBind(t, s, localEnd)

	if !s.PollAt().Equal(socket.PollOnIngress()) {
		t.Errorf("PollAt with nothing queued = %v, want on ingress", s.PollAt())
	}
	mustSendSlice(t, s, testPayload, toRemote)
	if !s.PollAt().Equal(socket.PollNow()) {
		t.Errorf("PollAt with a datagram queued = %v, want now", s.PollAt())
	}
	dispatch(cx, s)
	if !s.PollAt().Equal(socket.PollOnIngress()) {
		t.Errorf("PollAt after the datagram went = %v, want on ingress", s.PollAt())
	}
}

func TestAddressBoundSocketAcceptsBroadcastsAndMulticasts(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(buffer(1), buffer(0))
	mustBind(t, s, localEnd)
	tests := map[string]bool{
		"255.255.255.255": true,
		"192.168.1.255":   true,
		"224.0.0.1":       true,
		"192.168.1.1":     true,
		"192.168.1.3":     false,
	}
	for destination, want := range tests {
		ip := remoteIPRepr
		ip.DstAddr = netip.MustParseAddr(destination)

		if got := s.Accepts(cx, ip, remoteUDPRepr); got != want {
			t.Errorf("Accepts a datagram for %s = %t, want %t", destination, got, want)
		}
	}
}

func TestUnboundSocketAcceptsNothing(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(buffer(1), buffer(0))

	if s.Accepts(cx, remoteIPRepr, remoteUDPRepr) {
		t.Error("an unbound socket accepts a datagram")
	}
}

func TestEndpointIsTheBoundEndpoint(t *testing.T) {
	t.Parallel()
	s := NewSocket(buffer(0), buffer(0))

	if s.Endpoint().Port != 0 {
		t.Errorf("an unbound socket's endpoint is %v, want port 0", s.Endpoint())
	}
	mustBind(t, s, localEnd)
	if s.Endpoint() != localEnd {
		t.Errorf("Endpoint = %v, want %v", s.Endpoint(), localEnd)
	}
}

func TestSendQueueCountsPayloadOctetsAndPadding(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(NewPacketBuffer(3, 10), NewPacketBuffer(3, 10))
	mustBind(t, s, localEnd)
	four := []byte("1234")
	for range 2 {
		mustSendSlice(t, s, four, toRemote)
	}
	dispatch(cx, s)

	// Two octets are left at the end of the storage, too few for the next
	// datagram, which goes to the front behind two octets of padding.
	mustSendSlice(t, s, four, toRemote)

	if s.SendQueue() != 10 {
		t.Errorf("SendQueue = %d, want 10: two datagrams and two octets of padding", s.SendQueue())
	}
}

func TestMetadataStringIsTheEndpoint(t *testing.T) {
	t.Parallel()

	got := remoteMetadataWithLocal().String()

	if got != "192.168.1.2:49500" {
		t.Errorf("String = %q, want %q", got, "192.168.1.2:49500")
	}
}
