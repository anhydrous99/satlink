package ipstack

import (
	"errors"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
)

// errDeviceExhausted is what a socket's emit returns when the device refuses
// a transmit slot. The socket hands it back from its Dispatch, which ends the
// egress round; it never leaves the package.
var errDeviceExhausted = errors.New("device exhausted")

// socketEgress runs one round of sending: it offers every socket, in slot
// order, the chance to send one packet through a transmit slot it asks the
// device for, and ends the round early when the device refuses one. It
// reports whether the round sent anything, a datagram larger than the MTU
// included, which is dropped once it has its slot.
func (i *Interface) socketEgress(device Device, sockets *SocketSet) PollResult {
	result := PollNone
	emit := func(packet ipPacket) error {
		slot, ok := device.Transmit(i.cx.Now())
		if !ok {
			netlog.Debug("failed to transmit IP: device exhausted")
			return errDeviceExhausted
		}
		i.dispatchIP(slot, packet)
		result = PollSocketStateChanged
		return nil
	}
	for _, item := range sockets.items() {
		// emit is the only source of a socket's error, and it fails only
		// when the device buffer is full.
		if err := item.dispatch(i.cx, emit); err != nil {
			break
		}
	}
	return result
}
