package sim

import (
	"net/netip"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// UDPDatagram returns an IPv4 datagram carrying payload from source to
// destination over UDP, checksums and all, built with the stack's own
// Reprs. It is exported, in a test file, for the external tests as well.
func UDPDatagram(source, destination netip.AddrPort, payload []byte) []byte {
	udp := wire.UDPRepr{SrcPort: source.Port(), DstPort: destination.Port()}
	ip := wire.IPv4Repr{
		SrcAddr:    source.Addr(),
		DstAddr:    destination.Addr(),
		NextHeader: wire.IPProtocolUDP,
		PayloadLen: udp.HeaderLen() + len(payload),
		HopLimit:   wire.DefaultHopLimit,
	}
	checksums := wire.ChecksumCapabilities{}
	datagram := make([]byte, ip.BufferLen())
	packet := wire.IPv4Packet(datagram)
	ip.Emit(packet, checksums)
	udp.Emit(wire.UDPPacket(packet.Payload()), ip.SrcAddr, ip.DstAddr, payload, checksums)
	return datagram
}
