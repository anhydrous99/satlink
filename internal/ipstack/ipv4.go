package ipstack

import (
	"net/netip"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// allSystemsGroup is 224.0.0.1, the multicast group of every host
// (RFC 1112 § 4), which the interface belongs to without multicast support.
var allSystemsGroup = netip.AddrFrom4([4]byte{224, 0, 0, 1})

// processIPv4 processes a checked IPv4 datagram and returns the reply to send
// at once, if any: smoltcp's process_ipv4.
func (i *Interface) processIPv4(sockets *SocketSet, packet wire.IPv4Packet) (ipPacket, bool) {
	header, err := wire.ParseIPv4Repr(packet, i.checksums)
	if err != nil {
		traceMalformed("wire.ParseIPv4Repr")
		return noPacket()
	}
	if !i.cx.IsUnicast(header.SrcAddr) && !header.SrcAddr.IsUnspecified() {
		// Discard packets with non-unicast source addresses but allow
		// unspecified.
		netlog.Debug("non-unicast or unspecified source address")
		return noPacket()
	}
	if !i.isForThisHost(header.DstAddr) {
		return noPacket()
	}
	return i.processIPv4Payload(sockets, header, packet.Payload())
}

// isForThisHost reports whether a datagram for dst is the interface's to
// process: dst is one of its addresses (every address, with any-IP), one of
// its broadcast addresses or the all-systems group.
func (i *Interface) isForThisHost(dst netip.Addr) bool {
	if i.cx.HasIPAddr(dst) || dst == allSystemsGroup || i.cx.IsBroadcast(dst) {
		return true
	}
	// Ignore IP packets not directed at us, or broadcast, or any of the
	// multicast groups.
	if !wire.IsUnicast(dst) {
		netlog.Trace("Rejecting IPv4 packet; not a unicast address", "dst", dst)
		return false
	}
	// smoltcp looks for a route to dst through one of the interface's
	// addresses, and rejects the datagram either way; without routes, which
	// Medium::Ip does not need, there is none.
	netlog.Trace("Rejecting IPv4 packet; no matching routes")
	return false
}

// processIPv4Payload hands the payload of a datagram for this host to its
// protocol, answering a protocol the stack does not handle with an ICMP
// protocol-unreachable error.
func (i *Interface) processIPv4Payload(sockets *SocketSet, header wire.IPv4Repr, payload []byte) (ipPacket, bool) {
	switch header.NextHeader {
	case wire.IPProtocolICMP:
		return i.processICMPv4(payload)
	case wire.IPProtocolUDP:
		return i.processUDP(sockets, header, payload)
	case wire.IPProtocolTCP:
		return i.processTCP(sockets, header, payload)
	default:
		// Send back as much of the original payload as we can.
		return i.icmpv4Reply(header, destinationUnreachable(wire.ICMPv4ProtoUnreachable, header, payload))
	}
}

// processICMPv4 checks an ICMPv4 message and drops it: smoltcp's
// process_icmpv4 without the auto-icmp-echo-reply feature and ICMP sockets,
// neither of which the workspace enables. Echo requests therefore go
// unanswered, like echo replies and every other message.
func (i *Interface) processICMPv4(payload []byte) (ipPacket, bool) {
	packet, err := wire.NewICMPv4Packet(payload)
	if err != nil {
		traceMalformed("wire.NewICMPv4Packet")
		return noPacket()
	}
	if _, err := wire.ParseICMPv4Repr(packet, i.checksums); err != nil {
		traceMalformed("wire.ParseICMPv4Repr")
	}
	return noPacket()
}

// destinationUnreachable returns the ICMP error that reports a datagram with
// header and payload undeliverable, for the reason code: it quotes the header
// and as much of the payload as fits into the minimum IPv4 MTU.
func destinationUnreachable(code uint8, header wire.IPv4Repr, payload []byte) wire.ICMPv4Repr {
	return wire.ICMPv4Repr{
		Message: wire.ICMPv4DstUnreachable,
		Code:    code,
		Header:  header,
		Data:    payload[:icmpReplyPayloadLen(len(payload), header.HeaderLen())],
	}
}

// icmpv4Reply returns the packet that answers a datagram with header with
// message, an ICMP error: smoltcp's icmpv4_reply. It sends none to a source
// that is not unicast, and none about a datagram to a broadcast or multicast
// address: smoltcp answers those only with echo replies, which this stack
// never sends.
func (i *Interface) icmpv4Reply(header wire.IPv4Repr, message wire.ICMPv4Repr) (ipPacket, bool) {
	if !i.cx.IsUnicast(header.SrcAddr) || !i.cx.IsUnicast(header.DstAddr) {
		return noPacket()
	}
	reply := wire.IPv4Repr{
		SrcAddr:    header.DstAddr,
		DstAddr:    header.SrcAddr,
		NextHeader: wire.IPProtocolICMP,
		PayloadLen: message.BufferLen(),
		HopLimit:   wire.DefaultHopLimit,
	}
	return ipPacket{header: reply, payload: icmpv4Payload{message: message}}, true
}
