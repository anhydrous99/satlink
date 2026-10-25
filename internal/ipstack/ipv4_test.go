package ipstack

// The tests up to TestICMPReplySize follow smoltcp's IPv4 interface tests for
// bare IPv4 datagrams. The tests after them cover the rest of IPv4 processing.

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
	"github.com/anhydrous99/satlink/internal/logtest"
)

// unknownProtocol is a protocol the stack does not handle (0x0c, PUP).
const unknownProtocol wire.IPProtocol = 0x0c

func TestNoICMPNoUnicast(t *testing.T) {
	t.Parallel()
	iface, sockets, _ := setup(t)
	// Unknown IPv4 protocol.
	//
	// Because the destination is the broadcast address, this should not
	// trigger a Destination Unreachable response. See RFC 1122 § 3.2.2.
	header := wire.IPv4Repr{
		SrcAddr:    loopbackAddr,
		DstAddr:    limitedBroadcast,
		NextHeader: unknownProtocol,
		PayloadLen: 0,
		HopLimit:   0x40,
	}
	datagram := make([]byte, 54)
	header.Emit(wire.IPv4Packet(datagram), anyChecksums)

	// Ensure that the unknown protocol frame does not trigger an ICMP error
	// response when the destination address is a broadcast address.
	reply, ok := iface.processIPv4(sockets, wire.IPv4Packet(datagram))

	assertNoPacket(t, reply, ok)
}

func TestICMPErrorNoPayload(t *testing.T) {
	t.Parallel()
	iface, sockets, _ := setup(t)
	peer := netip.AddrFrom4([4]byte{127, 0, 0, 2})
	// Unknown IPv4 protocol with no payload.
	header := wire.IPv4Repr{SrcAddr: peer, DstAddr: loopbackAddr, NextHeader: unknownProtocol, PayloadLen: 0, HopLimit: 0x40}
	datagram := make([]byte, 34)
	header.Emit(wire.IPv4Packet(datagram), anyChecksums)
	// The expected Destination Unreachable response due to the unknown
	// protocol.
	message := wire.ICMPv4Repr{
		Message: wire.ICMPv4DstUnreachable,
		Code:    wire.ICMPv4ProtoUnreachable,
		Header:  wire.IPv4Repr{SrcAddr: peer, DstAddr: loopbackAddr, NextHeader: unknownProtocol, PayloadLen: 0, HopLimit: wire.DefaultHopLimit},
		Data:    []byte{},
	}
	want := ipPacket{
		header:  ipv4Header(loopbackAddr, peer, wire.IPProtocolICMP, message.BufferLen()),
		payload: icmpv4Payload{message: message},
	}

	// Ensure that the unknown protocol triggers an error response, and that
	// we correctly handle no payload.
	reply, ok := iface.processIPv4(sockets, wire.IPv4Packet(datagram))

	assertPacket(t, reply, ok, want)
}

func TestICMPErrorPortUnreachable(t *testing.T) {
	t.Parallel()
	iface, sockets, _ := setup(t)
	udpPayload := []byte("Hello, Wold!")
	peer := netip.AddrFrom4([4]byte{127, 0, 0, 2})
	udpHeader := wire.UDPRepr{SrcPort: 67, DstPort: 68}
	header := ipv4Header(peer, loopbackAddr, wire.IPProtocolUDP, udpHeader.HeaderLen()+len(udpPayload))
	data := udpBytes(header.SrcAddr, header.DstAddr, udpHeader, udpPayload)
	// The expected Destination Unreachable ICMPv4 error response due to no
	// sockets listening on the destination port.
	message := wire.ICMPv4Repr{Message: wire.ICMPv4DstUnreachable, Code: wire.ICMPv4PortUnreachable, Header: header, Data: data}
	want := ipPacket{
		header:  ipv4Header(loopbackAddr, peer, wire.IPProtocolICMP, message.BufferLen()),
		payload: icmpv4Payload{message: message},
	}

	reply, ok := iface.processUDP(sockets, header, data)
	assertPacket(t, reply, ok, want)

	broadcastHeader := ipv4Header(peer, limitedBroadcast, wire.IPProtocolUDP, udpHeader.HeaderLen()+len(udpPayload))
	broadcast := udpBytes(broadcastHeader.SrcAddr, broadcastHeader.DstAddr, udpHeader, udpPayload)
	// Ensure that the port unreachable error does not trigger an ICMP error
	// response when the destination address is a broadcast address and no
	// socket is bound to the port.
	reply, ok = iface.processUDP(sockets, broadcastHeader, broadcast)
	assertNoPacket(t, reply, ok)
}

func TestICMPReplySize(t *testing.T) {
	t.Parallel()
	const maxPayloadLen = 528
	iface, sockets, _ := setup(t)
	src, dst := localAddr, remoteAddr
	// UDP packet that if not truncated will cause an ICMP port unreachable
	// reply to exceed the minimum MTU in length.
	udpHeader := wire.UDPRepr{SrcPort: 67, DstPort: 68}
	payload := udpBytes(src, dst, udpHeader, bytes.Repeat([]byte{0x2a}, maxPayloadLen))
	header := ipv4Header(src, dst, wire.IPProtocolUDP, udpHeader.HeaderLen()+maxPayloadLen)
	message := wire.ICMPv4Repr{Message: wire.ICMPv4DstUnreachable, Code: wire.ICMPv4PortUnreachable, Header: header, Data: payload[:maxPayloadLen]}
	want := ipPacket{header: ipv4Header(dst, src, wire.IPProtocolICMP, message.BufferLen()), payload: icmpv4Payload{message: message}}
	if size := want.header.BufferLen(); size != wire.IPv4MinMTU {
		t.Fatalf("the expected reply has %d octets, want %d", size, wire.IPv4MinMTU)
	}

	reply, ok := iface.processUDP(sockets, header, payload)

	assertPacket(t, reply, ok, want)
}

func TestNoICMPErrorAnswersADatagramForAGroupOrABroadcast(t *testing.T) {
	t.Parallel()
	// An ICMP error must not answer a datagram sent to a broadcast address
	// or a multicast group (RFC 1122 § 3.2.2). The interface accepts
	// datagrams for these, so they reach the check: the subnet broadcasts of
	// its two networks, which only the interface knows to be broadcasts,
	// the limited broadcast, its own group and, with any-IP on, any group.
	payload := []byte("a payload nobody takes")
	datagrams := map[string]func(dst netip.Addr) []byte{
		"UDP to a port without a socket": func(dst netip.Addr) []byte {
			return udpDatagram(netip.AddrPortFrom(remoteAddr, 49500), netip.AddrPortFrom(dst, 9), payload)
		},
		"an unknown protocol": func(dst netip.Addr) []byte {
			return ipv4Datagram(ipv4Header(remoteAddr, dst, unknownProtocol, len(payload)), func(buffer []byte) { copy(buffer, payload) })
		},
	}
	destinations := []struct {
		addr  netip.Addr
		anyIP bool
	}{
		{addr: netip.MustParseAddr("192.168.1.255")},
		{addr: netip.MustParseAddr("127.255.255.255")},
		{addr: limitedBroadcast},
		{addr: allSystemsGroup},
		{addr: netip.MustParseAddr("224.0.0.251"), anyIP: true},
	}
	for name, datagram := range datagrams {
		for _, destination := range destinations {
			t.Run(name+" for "+destination.addr.String(), func(t *testing.T) {
				t.Parallel()
				iface, sockets, device := setup(t)
				iface.SetAnyIP(destination.anyIP)
				device.deliver(datagram(destination.addr))

				iface.Poll(at(0), device, sockets)

				assertNothingSent(t, device)
			})
		}
	}
}

func TestMalformedDatagramsAreDropped(t *testing.T) {
	t.Parallel()
	for name, datagram := range malformedDatagrams() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			iface, sockets, device := setup(t)
			receiver := newUDPSocket(t, wire.ListenEndpoint{Port: 68})
			sockets.AddUDP(receiver)
			device.deliver(datagram)

			result := iface.PollIngressSingle(at(0), device, sockets)

			if result != IngressSocketStateChanged {
				t.Errorf("PollIngressSingle = %v, want %v", result, IngressSocketStateChanged)
			}
			assertNothingSent(t, device)
			if receiver.CanReceive() {
				t.Error("the socket received the malformed datagram")
			}
		})
	}
}

// malformedDatagrams returns datagrams the interface cannot parse, by what
// is wrong with them. Most are a UDP datagram for port 68 changed after it
// was emitted, its IPv4 checksum computed again where a change would spoil
// it.
func malformedDatagrams() map[string][]byte {
	valid := udpDatagram(netip.AddrPortFrom(remoteAddr, 67), netip.AddrPortFrom(localAddr, 68), []byte("payload"))
	changed := func(change func(packet wire.IPv4Packet)) []byte {
		datagram := bytes.Clone(valid)
		change(wire.IPv4Packet(datagram))
		return datagram
	}
	asProtocol := func(protocol wire.IPProtocol, totalLen uint16) []byte {
		return changed(func(packet wire.IPv4Packet) {
			packet.SetNextHeader(protocol)
			packet.SetTotalLen(totalLen)
			packet.FillChecksum()
		})
	}
	badTCPChecksum := tcpDatagram(remoteAddr, localAddr, syn(49500, 80))
	segment := wire.TCPPacket(wire.IPv4Packet(badTCPChecksum).Payload())
	segment.SetChecksum(segment.Checksum() + 1)
	return map[string][]byte{
		"IPv6":                   {0x60, 0, 0, 0},
		"a truncated header":     valid[:wire.IPv4HeaderLen-1],
		"a bad header checksum":  changed(func(packet wire.IPv4Packet) { packet.SetChecksum(packet.Checksum() + 1) }),
		"a fragment":             changed(func(packet wire.IPv4Packet) { packet.SetMoreFragments(true); packet.FillChecksum() }),
		"a bad UDP checksum":     changed(func(packet wire.IPv4Packet) { packet.Payload()[wire.UDPHeaderLen] ^= 0xff }),
		"a truncated UDP header": asProtocol(wire.IPProtocolUDP, wire.IPv4HeaderLen+4),
		"a truncated TCP header": asProtocol(wire.IPProtocolTCP, uint16(len(valid))),
		"a bad TCP checksum":     badTCPChecksum,
	}
}

func TestNonUnicastSourceIsDropped(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"192.168.1.255", "255.255.255.255", "224.0.0.5"} {
		iface, sockets, device := setup(t)
		receiver := newUDPSocket(t, wire.ListenEndpoint{Port: 68})
		sockets.AddUDP(receiver)
		// A datagram for a port without a socket, to show that no ICMP
		// error answers it either.
		device.deliver(
			udpDatagram(netip.AddrPortFrom(netip.MustParseAddr(source), 67), netip.AddrPortFrom(localAddr, 68), []byte("x")),
			udpDatagram(netip.AddrPortFrom(netip.MustParseAddr(source), 67), netip.AddrPortFrom(localAddr, 69), []byte("x")),
		)

		iface.Poll(at(0), device, sockets)

		if receiver.CanReceive() {
			t.Errorf("a datagram from %s reached the socket", source)
		}
		assertNothingSent(t, device)
	}
}

func TestUnspecifiedSourceIsAccepted(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	receiver := newUDPSocket(t, wire.ListenEndpoint{Port: 67})
	sockets.AddUDP(receiver)
	device.deliver(udpDatagram(netip.AddrPortFrom(netip.IPv4Unspecified(), 68), netip.AddrPortFrom(limitedBroadcast, 67), []byte("discover")))

	iface.Poll(at(0), device, sockets)

	payload, meta, err := receiver.Receive()
	if err != nil || string(payload) != "discover" || meta.Endpoint.Addr() != netip.IPv4Unspecified() {
		t.Errorf("Receive = %q from %v, %v; want the datagram from 0.0.0.0", payload, meta, err)
	}
}

func TestDatagramsForTheInterfacesGroupsAndBroadcastsAreAccepted(t *testing.T) {
	t.Parallel()
	for _, destination := range []netip.Addr{localAddr, loopbackAddr, limitedBroadcast, netip.MustParseAddr("192.168.1.255"), allSystemsGroup} {
		iface, sockets, device := setup(t)
		receiver := newUDPSocket(t, wire.ListenEndpoint{Port: 68})
		sockets.AddUDP(receiver)
		device.deliver(udpDatagram(netip.AddrPortFrom(remoteAddr, 67), netip.AddrPortFrom(destination, 68), []byte("x")))

		iface.Poll(at(0), device, sockets)

		if !receiver.CanReceive() {
			t.Errorf("a datagram for %v did not reach the socket", destination)
		}
	}
}

func TestAnyIPAcceptsEveryDestination(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	receiver := newUDPSocket(t, wire.ListenEndpoint{Port: 7})
	sockets.AddUDP(receiver)
	datagram := udpDatagram(netip.AddrPortFrom(remoteAddr, 49500), netip.AddrPortFrom(foreignAddr, 7), []byte("echo"))

	device.deliver(datagram)
	iface.Poll(at(0), device, sockets)
	if receiver.CanReceive() {
		t.Fatal("without any-IP, a datagram for a foreign address reached the socket")
	}
	assertNothingSent(t, device)

	iface.SetAnyIP(true)
	device.deliver(datagram)
	iface.Poll(at(0), device, sockets)

	payload, meta, err := receiver.Receive()
	if err != nil || string(payload) != "echo" || meta.LocalAddress != foreignAddr {
		t.Errorf("with any-IP: Receive = %q sent to %v, %v; want it, sent to %v", payload, meta.LocalAddress, err, foreignAddr)
	}
}

func TestDatagramForAForeignMulticastGroupIsDropped(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	receiver := newUDPSocket(t, wire.ListenEndpoint{Port: 68})
	sockets.AddUDP(receiver)
	device.deliver(udpDatagram(netip.AddrPortFrom(remoteAddr, 67), netip.MustParseAddrPort("224.0.0.251:68"), []byte("x")))

	iface.Poll(at(0), device, sockets)

	if receiver.CanReceive() {
		t.Error("a datagram for a group the interface is not in reached the socket")
	}
}

func TestEchoRequestGetsNoReply(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	// Type 8 (echo request), code 0, its checksum, identifier 0x1234,
	// sequence number 1, and "ping".
	echo := []byte{0x08, 0x00, 0x06, 0xfa, 0x12, 0x34, 0x00, 0x01, 'p', 'i', 'n', 'g'}
	header := ipv4Header(remoteAddr, localAddr, wire.IPProtocolICMP, len(echo))
	device.deliver(ipv4Datagram(header, func(buffer []byte) { copy(buffer, echo) }))

	result := iface.Poll(at(0), device, sockets)

	if result != PollSocketStateChanged {
		t.Errorf("Poll = %v, want %v: every datagram counts", result, PollSocketStateChanged)
	}
	assertNothingSent(t, device)
}

func TestIncomingICMPIsDroppedUnread(t *testing.T) {
	// No t.Parallel: logtest.Record replaces the process-wide logger.
	sent := ipv4Header(localAddr, remoteAddr, wire.IPProtocolUDP, 600)
	portUnreachable := func(quotedLen int) []byte {
		return emitICMPv4(wire.ICMPv4Repr{Message: wire.ICMPv4DstUnreachable, Code: wire.ICMPv4PortUnreachable,
			Header: sent, Data: make([]byte, quotedLen)})
	}
	messages := map[string][]byte{
		// Routers quote the header and 8 octets of the payload (RFC 792).
		"an error quoting 8 of 600 octets": portUnreachable(8),
		// The stack's own errors quote as much as fits into IPv4MinMTU.
		"an error quoting 528 of 600 octets": portUnreachable(528),
		"a message shorter than its header":  {0x03, 0x03, 0x00, 0x00},
	}
	for name, message := range messages {
		t.Run(name, func(t *testing.T) {
			iface, sockets, device := setup(t)
			logs := logtest.Record(t, "ipstack", netlog.LevelTrace)
			header := ipv4Header(remoteAddr, localAddr, wire.IPProtocolICMP, len(message))
			device.deliver(ipv4Datagram(header, func(buffer []byte) { copy(buffer, message) }))

			iface.Poll(at(0), device, sockets)

			assertNothingSent(t, device)
			if got := logs(); len(got) != 0 {
				t.Errorf("logged %v, want nothing", got)
			}
		})
	}
}

func TestUnknownProtocolGetsProtocolUnreachable(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	payload := []byte("an unknown protocol's payload")
	header := ipv4Header(remoteAddr, localAddr, unknownProtocol, len(payload))
	device.deliver(ipv4Datagram(header, func(buffer []byte) { copy(buffer, payload) }))

	iface.Poll(at(0), device, sockets)

	reply := onlySent(t, device)
	replyHeader, message := parseICMPv4(t, reply.datagram)
	if replyHeader.SrcAddr != localAddr || replyHeader.DstAddr != remoteAddr || !reply.isReply {
		t.Errorf("reply %v (through the reply slot: %t), want one from %v to %v through it", replyHeader, reply.isReply, localAddr, remoteAddr)
	}
	want := emitICMPv4(wire.ICMPv4Repr{Message: wire.ICMPv4DstUnreachable, Code: wire.ICMPv4ProtoUnreachable,
		Header: header, Data: payload})
	if !bytes.Equal(message, want) {
		t.Errorf("ICMP % x, want protocol unreachable quoting the datagram: % x", message, want)
	}
}

func TestUnmatchedUDPGetsPortUnreachable(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	datagram := udpDatagram(netip.AddrPortFrom(remoteAddr, 49500), netip.AddrPortFrom(localAddr, 9), []byte("discard"))
	device.deliver(datagram)

	iface.Poll(at(0), device, sockets)

	_, message := parseICMPv4(t, onlySent(t, device).datagram)
	header, payload := parseIPv4(t, datagram)
	want := emitICMPv4(wire.ICMPv4Repr{Message: wire.ICMPv4DstUnreachable, Code: wire.ICMPv4PortUnreachable,
		Header: header, Data: payload})
	if !bytes.Equal(message, want) {
		t.Errorf("ICMP % x, want port unreachable quoting the whole datagram: % x", message, want)
	}
}

func TestNoICMPErrorGoesToTheUnspecifiedSource(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	unspecified := netip.IPv4Unspecified()
	payload := []byte("an unknown protocol's payload")
	unknown := ipv4Header(unspecified, localAddr, unknownProtocol, len(payload))
	device.deliver(
		udpDatagram(netip.AddrPortFrom(unspecified, 68), netip.AddrPortFrom(localAddr, 9), []byte("discard")),
		ipv4Datagram(unknown, func(buffer []byte) { copy(buffer, payload) }),
	)

	iface.Poll(at(0), device, sockets)

	assertNothingSent(t, device)
}
