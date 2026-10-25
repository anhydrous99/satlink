package ipstack

// socketEgress runs one round of sending: it offers every socket, in slot
// order, the chance to send one packet through a transmit slot it asks the
// device for. It reports whether the round sent anything, a datagram larger
// than the MTU included, which is dropped once it has its slot.
func (i *Interface) socketEgress(device Device, sockets *SocketSet) PollResult {
	result := PollNone
	emit := func(packet ipPacket) {
		i.dispatchIP(device.Transmit(i.cx.Now()), packet)
		result = PollSocketStateChanged
	}
	for _, item := range sockets.items() {
		item.dispatch(i.cx, emit)
	}
	return result
}
