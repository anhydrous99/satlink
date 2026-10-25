package ipstack_test

import (
	"bytes"
	"fmt"
	"net/netip"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// queueDevice is a device that holds datagrams in two queues: those received,
// and those sent.
type queueDevice struct {
	received, sent [][]byte
}

func (d *queueDevice) Receive(time.Time) ([]byte, ipstack.TransmitSlot, bool) {
	if len(d.received) == 0 {
		return nil, nil, false
	}
	datagram := d.received[0]
	d.received = d.received[1:]
	return datagram, d.send, true
}

func (d *queueDevice) Transmit(time.Time) ipstack.TransmitSlot {
	return d.send
}

func (d *queueDevice) send(datagram []byte) {
	d.sent = append(d.sent, datagram)
}

// An echo server: an interface with one UDP socket answers a datagram the
// device received.
func Example() {
	now := time.Now()
	device := &queueDevice{}
	iface := ipstack.NewInterface(ipstack.Config{RandomSeed: 1}, ipstack.DeviceCapabilities{MTU: 1500}, now)
	if err := iface.SetIPAddrs([]netip.Prefix{netip.MustParsePrefix("192.168.1.1/24")}); err != nil {
		panic(err)
	}
	var sockets ipstack.SocketSet
	echo := udp.NewSocket(udp.NewPacketBuffer(4, 1500), udp.NewPacketBuffer(4, 1500))
	if err := echo.Bind(wire.ListenEndpoint{Port: 7}); err != nil {
		panic(err)
	}
	sockets.AddUDP(echo)

	// A peer's datagram arrives, and the poll hands it to the socket.
	device.received = append(device.received, datagram("192.168.1.2:49500", "192.168.1.1:7", "ping"))
	iface.Poll(now, device, &sockets)
	payload, meta, err := echo.Receive()
	if err != nil {
		panic(err)
	}
	fmt.Printf("received %q from %v\n", payload, meta.Endpoint)

	// The answer goes back to the sender, from the address it wrote to,
	// at the next poll. The received payload is valid only until the
	// socket is next used, so SendSlice gets a copy of it.
	if err := echo.SendSlice(bytes.Clone(payload), meta); err != nil {
		panic(err)
	}
	iface.Poll(now, device, &sockets)
	header, err := wire.ParseIPv4Repr(wire.IPv4Packet(device.sent[0]), wire.ChecksumCapabilities{})
	if err != nil {
		panic(err)
	}
	fmt.Println("sent", header)
	// Output:
	// received "ping" from 192.168.1.2:49500
	// sent IPv4 src=192.168.1.1 dst=192.168.1.2 proto=UDP
}

// datagram returns a UDP datagram carrying payload from src to dst.
func datagram(src, dst, payload string) []byte {
	from, to := netip.MustParseAddrPort(src), netip.MustParseAddrPort(dst)
	udpHeader := wire.UDPRepr{SrcPort: from.Port(), DstPort: to.Port()}
	header := wire.IPv4Repr{
		SrcAddr:    from.Addr(),
		DstAddr:    to.Addr(),
		NextHeader: wire.IPProtocolUDP,
		PayloadLen: udpHeader.HeaderLen() + len(payload),
		HopLimit:   wire.DefaultHopLimit,
	}
	buffer := make([]byte, header.BufferLen())
	header.Emit(wire.IPv4Packet(buffer), wire.ChecksumCapabilities{})
	udpHeader.Emit(wire.UDPPacket(buffer[header.HeaderLen():]), header.SrcAddr, header.DstAddr, []byte(payload), wire.ChecksumCapabilities{})
	return buffer
}
