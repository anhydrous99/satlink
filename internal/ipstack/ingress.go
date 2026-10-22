package ipstack

import (
	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// ipv4Version is the version field of an IPv4 datagram (RFC 791 § 3.1), the
// only version the stack handles.
const ipv4Version = 4

// socketIngress takes the next datagram the device has received, if any,
// hands it to the sockets it is for and sends the interface's immediate
// reply, if any, through the datagram's reply slot.
func (i *Interface) socketIngress(device Device, sockets *SocketSet) PollIngressSingleResult {
	datagram, reply, ok := device.Receive(i.cx.Now())
	if !ok {
		return IngressNone
	}
	if len(datagram) == 0 {
		return IngressPacketProcessed
	}
	if response, ok := i.processIP(sockets, datagram); ok {
		i.dispatchIP(reply, response)
	}
	// smoltcp counts every datagram that is not empty as having maybe
	// changed a socket, even those that cannot have (IP fragments, ICMP
	// messages, datagrams for other hosts): it does not find out which did.
	return IngressSocketStateChanged
}

// processIP processes a datagram that is not empty and returns the reply to
// send at once, if any: smoltcp's process_ip on Medium::Ip.
func (i *Interface) processIP(sockets *SocketSet, datagram []byte) (ipPacket, bool) {
	if wire.IPv4Packet(datagram).Version() != ipv4Version {
		// Drop all other traffic.
		return noPacket()
	}
	packet, err := wire.NewIPv4Packet(datagram)
	if err != nil {
		traceMalformed("wire.NewIPv4Packet")
		return noPacket()
	}
	return i.processIPv4(sockets, packet)
}

// traceMalformed logs that the interface dropped a datagram because the
// parser what names rejected it, as smoltcp's check! macro does.
func traceMalformed(what string) {
	netlog.Trace("iface: malformed", "what", what)
}
