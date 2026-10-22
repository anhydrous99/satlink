package ipstack

import "github.com/anhydrous99/satlink/internal/ipstack/wire"

// ipPacket is a datagram the interface sends, still as representations: an
// IPv4 header and its payload (smoltcp's Packet and PacketV4). The header's
// NextHeader and PayloadLen match the payload.
type ipPacket struct {
	header  wire.IPv4Repr
	payload ipPayload
}

// ipPayload is what an ipPacket carries (smoltcp's IpPayload): an ICMPv4
// message, a UDP datagram or a TCP segment.
type ipPayload interface {
	// emit writes the payload into buffer, the header.PayloadLen octets
	// that follow the IPv4 header in the datagram.
	emit(buffer []byte, header wire.IPv4Repr, checksums wire.ChecksumCapabilities)
}

// newTCPPacket returns a packet of segment, with its IP header.
func newTCPPacket(header wire.IPv4Repr, segment wire.TCPRepr) ipPacket {
	return ipPacket{header: header, payload: tcpPayload{segment: segment}}
}

// newUDPPacket returns a packet of a UDP datagram with udpHeader and data,
// with its IP header.
func newUDPPacket(header wire.IPv4Repr, udpHeader wire.UDPRepr, data []byte) ipPacket {
	return ipPacket{header: header, payload: udpPayload{header: udpHeader, data: data}}
}

// icmpv4Payload is an ICMPv4 message: the interface's error replies.
type icmpv4Payload struct {
	message wire.ICMPv4Repr
}

func (p icmpv4Payload) emit(buffer []byte, _ wire.IPv4Repr, checksums wire.ChecksumCapabilities) {
	p.message.Emit(wire.ICMPv4Packet(buffer), checksums)
}

// udpPayload is a UDP datagram, from a UDP or DNS socket. data shares the
// socket's storage, so the packet must be emitted before the socket's
// dispatch returns.
type udpPayload struct {
	header wire.UDPRepr
	data   []byte
}

func (p udpPayload) emit(buffer []byte, header wire.IPv4Repr, checksums wire.ChecksumCapabilities) {
	p.header.Emit(wire.UDPPacket(buffer), header.SrcAddr, header.DstAddr, p.data, checksums)
}

// tcpPayload is a TCP segment: a socket's, its reply to a segment, or a RST
// for a segment no socket accepted. Its payload shares the socket's storage,
// as udpPayload's data does.
type tcpPayload struct {
	segment wire.TCPRepr
}

func (p tcpPayload) emit(buffer []byte, header wire.IPv4Repr, checksums wire.ChecksumCapabilities) {
	p.segment.Emit(wire.TCPPacket(buffer), header.SrcAddr, header.DstAddr, checksums)
}

// icmpv4ErrorHeaderLen is the length of the header of an ICMPv4
// destination-unreachable message: type, code, checksum and four unused
// octets (RFC 792).
const icmpv4ErrorHeaderLen = 8

// icmpReplyPayloadLen returns how many of the payloadLen octets of a datagram
// with a header of headerLen octets an ICMP error about it quotes.
func icmpReplyPayloadLen(payloadLen, headerLen int) int {
	// Send back as much of the original payload as will fit within the
	// minimum MTU required by IPv4. See RFC 1812 § 4.3.2.3 for more details.
	//
	// Since the entire network layer packet must fit within the minimum MTU
	// supported, the payload must not exceed the following:
	//
	// <min mtu> - IP Header Size * 2 - ICMPv4 DstUnreachable hdr size
	return min(payloadLen, wire.IPv4MinMTU-headerLen*2-icmpv4ErrorHeaderLen)
}

// noPacket returns the absence of a reply, for the functions that return an
// ipPacket only sometimes.
func noPacket() (ipPacket, bool) {
	return ipPacket{}, false
}
