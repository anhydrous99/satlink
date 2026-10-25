package ipstack

import (
	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// dispatchIP serialises packet and sends it through slot. The stack does not
// fragment, so a datagram larger than the IP MTU is dropped, and the slot left
// unused.
func (i *Interface) dispatchIP(slot TransmitSlot, packet ipPacket) {
	// The unspecified address must never be a datagram's destination
	// (RFC 1122 § 3.2.1.3), and no socket or reply ever makes it one.
	if packet.header.DstAddr.IsUnspecified() {
		panic("ipstack: a datagram for the unspecified address")
	}
	datagramLen := packet.header.BufferLen()
	if datagramLen > i.cx.IPMTU() {
		netlog.Debug("fragmentation is not supported, dropping a datagram larger than the IP MTU",
			"len", datagramLen, "mtu", i.cx.IPMTU())
		return
	}
	datagram := make([]byte, datagramLen)
	packet.header.Emit(wire.IPv4Packet(datagram), i.checksums)
	packet.payload.emit(datagram[packet.header.HeaderLen():], packet.header, i.checksums)
	slot(datagram)
}
