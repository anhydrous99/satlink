package ipstack

import (
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// processTCP hands a TCP segment to the first socket in slot order that
// accepts it and returns that socket's immediate reply, if any; a segment no
// socket accepts is answered with a RST.
func (i *Interface) processTCP(sockets *SocketSet, header wire.IPv4Repr, payload []byte) (ipPacket, bool) {
	// Per RFC 1122 § 3.2.1.3, the unspecified address must never appear as
	// a source or destination in any IP datagram. Drop such TCP segments
	// early to avoid creating sockets with unspecified peers (which would
	// later panic on egress). This is not done at the interface level
	// because it might be useful with UDP sockets, but it's definitely not
	// useful for TCP.
	if header.SrcAddr.IsUnspecified() || header.DstAddr.IsUnspecified() {
		return noPacket()
	}
	packet, err := wire.NewTCPPacket(payload)
	if err != nil {
		traceMalformed("wire.NewTCPPacket")
		return noPacket()
	}
	segment, err := wire.ParseTCPRepr(packet, header.SrcAddr, header.DstAddr, i.checksums)
	if err != nil {
		traceMalformed("wire.ParseTCPRepr")
		return noPacket()
	}
	for _, s := range sockets.TCPSockets() {
		if s.Accepts(header, segment) {
			replyHeader, reply, ok := s.Process(i.cx, header, segment)
			if !ok {
				return noPacket()
			}
			return newTCPPacket(replyHeader, reply), true
		}
	}
	if segment.Control == wire.TCPControlRST {
		// Never reply to a TCP RST packet with another TCP RST packet.
		return noPacket()
	}
	return newTCPPacket(tcp.RSTReply(header, segment)), true
}
