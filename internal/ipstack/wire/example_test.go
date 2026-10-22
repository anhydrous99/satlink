package wire_test

import (
	"fmt"
	"net/netip"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// To emit an IP packet header into an octet buffer, and then parse it back
// (the example of smoltcp's wire module).
func Example() {
	repr := wire.IPv4Repr{
		SrcAddr:    netip.MustParseAddr("10.0.0.1"),
		DstAddr:    netip.MustParseAddr("10.0.0.2"),
		NextHeader: wire.IPProtocolTCP,
		PayloadLen: 10,
		HopLimit:   64,
	}
	buffer := make([]byte, repr.BufferLen())
	repr.Emit(wire.IPv4Packet(buffer), wire.ChecksumCapabilities{})

	packet, err := wire.NewIPv4Packet(buffer)
	if err != nil {
		fmt.Println("truncated packet:", err)
		return
	}
	parsed, err := wire.ParseIPv4Repr(packet, wire.ChecksumCapabilities{})
	if err != nil {
		fmt.Println("malformed packet:", err)
		return
	}
	fmt.Println(parsed == repr)
	fmt.Println(parsed)
	// Output:
	// true
	// IPv4 src=10.0.0.1 dst=10.0.0.2 proto=TCP
}
