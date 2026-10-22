package sim

import (
	"net/netip"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// Spots a client's request to open a TCP connection, a SYN without ACK, in
// a datagram that the stack has yet to process.

// connectionRequest is the two ends of the connection a SYN without ACK
// asks for.
type connectionRequest struct {
	// local is the address and port the client wants to reach: the
	// server's end.
	local netip.AddrPort
	// remote is the client's address and port.
	remote netip.AddrPort
}

// parseConnectionRequest returns the request datagram carries, if it is
// IPv4 with a SYN without ACK. Checksums are left to the stack, which drops
// the datagram if they fail.
func parseConnectionRequest(datagram []byte) (connectionRequest, bool) {
	ip, err := wire.NewIPv4Packet(datagram)
	if err != nil || ip.NextHeader() != wire.IPProtocolTCP {
		return connectionRequest{}, false
	}
	segment, err := wire.NewTCPPacket(ip.Payload())
	if err != nil || !segment.SYN() || segment.ACK() {
		return connectionRequest{}, false
	}
	return connectionRequest{
		local:  netip.AddrPortFrom(ip.DstAddr(), segment.DstPort()),
		remote: netip.AddrPortFrom(ip.SrcAddr(), segment.SrcPort()),
	}, true
}
