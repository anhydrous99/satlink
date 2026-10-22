package ipstack

// This file holds what the interface's tests share beyond the testing device:
// the test network's addresses, builders and parsers of datagrams (made with
// the wire package's Reprs), and comparisons of the packets the interface
// builds.

import (
	"bytes"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

var (
	// localAddr is the interface's address on the test network,
	// 192.168.1.0/24, and loopbackAddr its address on 127.0.0.0/8.
	localAddr    = netip.AddrFrom4([4]byte{192, 168, 1, 1})
	loopbackAddr = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	// remoteAddr is a peer on the test network.
	remoteAddr = netip.AddrFrom4([4]byte{192, 168, 1, 2})
	// foreignAddr is an address that is not the interface's.
	foreignAddr = netip.MustParseAddr("203.0.113.9")
	// limitedBroadcast is 255.255.255.255.
	limitedBroadcast = netip.AddrFrom4([4]byte{255, 255, 255, 255})
	// anyChecksums verifies and computes every checksum.
	anyChecksums = wire.ChecksumCapabilities{}
)

// at returns the time elapsed after the tests' epoch, the zero time.Time
// (smoltcp's Instant::ZERO).
func at(elapsed time.Duration) time.Time {
	return time.Time{}.Add(elapsed)
}

// ipv4Datagram returns a datagram with header, its checksum computed, and
// the payload emitPayload writes into the octets after the header.
func ipv4Datagram(header wire.IPv4Repr, emitPayload func(payload []byte)) []byte {
	datagram := make([]byte, header.BufferLen())
	header.Emit(wire.IPv4Packet(datagram), anyChecksums)
	emitPayload(datagram[header.HeaderLen():])
	return datagram
}

// ipv4Header returns the header of a datagram from src to dst of protocol,
// with payloadLen octets after it.
func ipv4Header(src, dst netip.Addr, protocol wire.IPProtocol, payloadLen int) wire.IPv4Repr {
	return wire.IPv4Repr{SrcAddr: src, DstAddr: dst, NextHeader: protocol, PayloadLen: payloadLen, HopLimit: wire.DefaultHopLimit}
}

// udpDatagram returns a UDP datagram with payload from src to dst.
func udpDatagram(src, dst netip.AddrPort, payload []byte) []byte {
	udpHeader := wire.UDPRepr{SrcPort: src.Port(), DstPort: dst.Port()}
	header := ipv4Header(src.Addr(), dst.Addr(), wire.IPProtocolUDP, udpHeader.HeaderLen()+len(payload))
	return ipv4Datagram(header, func(buffer []byte) {
		udpHeader.Emit(wire.UDPPacket(buffer), header.SrcAddr, header.DstAddr, payload, anyChecksums)
	})
}

// tcpDatagram returns a datagram carrying segment from src to dst.
func tcpDatagram(src, dst netip.Addr, segment wire.TCPRepr) []byte {
	header := ipv4Header(src, dst, wire.IPProtocolTCP, segment.BufferLen())
	return ipv4Datagram(header, func(buffer []byte) {
		segment.Emit(wire.TCPPacket(buffer), src, dst, anyChecksums)
	})
}

// tcpBytes returns segment as the octets of a TCP packet from src to dst.
func tcpBytes(src, dst netip.Addr, segment wire.TCPRepr) []byte {
	buffer := make([]byte, segment.BufferLen())
	segment.Emit(wire.TCPPacket(buffer), src, dst, anyChecksums)
	return buffer
}

// udpBytes returns a UDP packet with header and payload from src to dst.
func udpBytes(src, dst netip.Addr, header wire.UDPRepr, payload []byte) []byte {
	buffer := make([]byte, header.HeaderLen()+len(payload))
	header.Emit(wire.UDPPacket(buffer), src, dst, payload, anyChecksums)
	return buffer
}

// parseIPv4 parses a datagram the interface sent and returns its header and
// payload.
func parseIPv4(t *testing.T, datagram []byte) (wire.IPv4Repr, []byte) {
	t.Helper()
	packet, err := wire.NewIPv4Packet(datagram)
	if err != nil {
		t.Fatalf("the interface sent a malformed datagram: %v", err)
	}
	header, err := wire.ParseIPv4Repr(packet, anyChecksums)
	if err != nil {
		t.Fatalf("the interface sent a datagram with a malformed header: %v", err)
	}
	if len(datagram) != header.BufferLen() {
		t.Errorf("the interface sent %d octets for a datagram of %d", len(datagram), header.BufferLen())
	}
	return header, packet.Payload()
}

// parseTCP parses a datagram the interface sent that carries a TCP segment.
func parseTCP(t *testing.T, datagram []byte) (wire.IPv4Repr, wire.TCPRepr) {
	t.Helper()
	header, payload := parseIPv4(t, datagram)
	if header.NextHeader != wire.IPProtocolTCP {
		t.Fatalf("the interface sent %v, want a TCP segment", header)
	}
	packet, err := wire.NewTCPPacket(payload)
	if err != nil {
		t.Fatalf("the interface sent a malformed TCP segment: %v", err)
	}
	segment, err := wire.ParseTCPRepr(packet, header.SrcAddr, header.DstAddr, anyChecksums)
	if err != nil {
		t.Fatalf("the interface sent a malformed TCP segment: %v", err)
	}
	return header, segment
}

// parseUDP parses a datagram the interface sent that carries a UDP datagram.
func parseUDP(t *testing.T, datagram []byte) (wire.IPv4Repr, wire.UDPRepr, []byte) {
	t.Helper()
	header, payload := parseIPv4(t, datagram)
	if header.NextHeader != wire.IPProtocolUDP {
		t.Fatalf("the interface sent %v, want a UDP datagram", header)
	}
	packet, err := wire.NewUDPPacket(payload)
	if err != nil {
		t.Fatalf("the interface sent a malformed UDP datagram: %v", err)
	}
	udpHeader, err := wire.ParseUDPRepr(packet, header.SrcAddr, header.DstAddr, anyChecksums)
	if err != nil {
		t.Fatalf("the interface sent a malformed UDP datagram: %v", err)
	}
	return header, udpHeader, packet.Payload()
}

// parseICMPv4 parses a datagram the interface sent that carries an ICMPv4
// message, with its checksum verified.
func parseICMPv4(t *testing.T, datagram []byte) (wire.IPv4Repr, wire.ICMPv4Packet) {
	t.Helper()
	header, payload := parseIPv4(t, datagram)
	if header.NextHeader != wire.IPProtocolICMP {
		t.Fatalf("the interface sent %v, want an ICMP message", header)
	}
	packet, err := wire.NewICMPv4Packet(payload)
	if err != nil || !packet.VerifyChecksum() {
		t.Fatalf("the interface sent a malformed ICMP message (%v)", err)
	}
	return header, packet
}

// onlySent returns the one datagram the device sent since the last call; it
// fails the test unless there is exactly one.
func onlySent(t *testing.T, device *testingDevice) sentDatagram {
	t.Helper()
	sent := device.takeSent()
	if len(sent) != 1 {
		t.Fatalf("the interface sent %d datagrams, want 1", len(sent))
	}
	return sent[0]
}

// assertNothingSent fails the test if the device sent anything since the
// last call.
func assertNothingSent(t *testing.T, device *testingDevice) {
	t.Helper()
	if sent := device.takeSent(); len(sent) != 0 {
		t.Errorf("the interface sent %d datagrams, want none", len(sent))
	}
}

// assertPacket checks a reply the interface built against want.
func assertPacket(t *testing.T, got ipPacket, ok bool, want ipPacket) {
	t.Helper()
	if !ok {
		t.Fatalf("no reply, want %v", want.header)
	}
	if !packetsEqual(got, want) {
		t.Errorf("reply = %+v, want %+v", got, want)
	}
}

// assertNoPacket checks that the interface built no reply.
func assertNoPacket(t *testing.T, got ipPacket, ok bool) {
	t.Helper()
	if ok {
		t.Errorf("reply = %+v, want none", got)
	}
}

// packetsEqual reports whether two packets have the same header and payload,
// payload octets included.
func packetsEqual(a, b ipPacket) bool {
	if a.header != b.header {
		return false
	}
	switch want := b.payload.(type) {
	case icmpv4Payload:
		got, ok := a.payload.(icmpv4Payload)
		return ok && got.message.Equal(want.message)
	case udpPayload:
		got, ok := a.payload.(udpPayload)
		return ok && got.header == want.header && bytes.Equal(got.data, want.data)
	case tcpPayload:
		got, ok := a.payload.(tcpPayload)
		return ok && got.segment.Equal(want.segment)
	default:
		return false
	}
}

// newUDPSocket returns a socket bound to local with room for four datagrams
// of up to an MTU each way.
func newUDPSocket(t *testing.T, local wire.ListenEndpoint) *udp.Socket {
	t.Helper()
	const datagrams = 4
	newBuffer := func() *udp.PacketBuffer { return udp.NewPacketBuffer(datagrams, datagrams*testingDeviceMTU) }
	s := udp.NewSocket(newBuffer(), newBuffer())
	if err := s.Bind(local); err != nil {
		t.Fatalf("Bind(%v): %v", local, err)
	}
	return s
}

// newTCPSocket returns a closed TCP socket with 64-octet buffers, as
// smoltcp's interface tests use.
func newTCPSocket() *tcp.Socket {
	const bufferLen = 64
	return tcp.NewSocket(tcp.NewSocketBuffer(bufferLen), tcp.NewSocketBuffer(bufferLen))
}

// newListener returns a TCP socket listening on port.
func newListener(t *testing.T, port uint16) *tcp.Socket {
	t.Helper()
	s := newTCPSocket()
	if err := s.Listen(wire.ListenEndpoint{Port: port}); err != nil {
		t.Fatalf("Listen(%d): %v", port, err)
	}
	return s
}

// syn returns a connection request from port to port.
func syn(srcPort, dstPort uint16) wire.TCPRepr {
	return wire.TCPRepr{
		SrcPort:        srcPort,
		DstPort:        dstPort,
		Control:        wire.TCPControlSYN,
		SeqNumber:      wire.TCPSeqNumber(-10001),
		WindowLen:      1024,
		MaxSegmentSize: wire.Some[uint16](1460),
	}
}
