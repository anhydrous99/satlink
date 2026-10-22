package ipstack

import "github.com/anhydrous99/satlink/internal/ipstack/wire"

// processUDP hands a UDP datagram to the first UDP socket in slot order that
// accepts it or, failing that, to the first DNS socket that does; a datagram
// none accepts is answered with an ICMP port-unreachable error. This is
// smoltcp's process_udp.
func (i *Interface) processUDP(sockets *SocketSet, header wire.IPv4Repr, payload []byte) (ipPacket, bool) {
	packet, err := wire.NewUDPPacket(payload)
	if err != nil {
		traceMalformed("wire.NewUDPPacket")
		return noPacket()
	}
	udpHeader, err := wire.ParseUDPRepr(packet, header.SrcAddr, header.DstAddr, i.checksums)
	if err != nil {
		traceMalformed("wire.ParseUDPRepr")
		return noPacket()
	}
	for _, s := range sockets.UDPSockets() {
		if s.Accepts(i.cx, header, udpHeader) {
			s.Process(header, udpHeader, packet.Payload())
			return noPacket()
		}
	}
	for _, s := range sockets.DNSSockets() {
		if s.Accepts(header, udpHeader) {
			s.Process(header, udpHeader, packet.Payload())
			return noPacket()
		}
	}
	return i.icmpv4Reply(header, destinationUnreachable(wire.ICMPv4PortUnreachable, header, payload))
}
