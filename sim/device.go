package sim

import (
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
)

// The stack's device between the gateway's PPP server and the simulated
// internet: two queues of bare IPv4 datagrams, no link-layer header.

// packetQueue holds the datagrams waiting for the stack, and the datagrams
// the stack sent.
type packetQueue struct {
	received    [][]byte
	transmitted [][]byte
	mtu         int
}

// A packetQueue is the device of the simulated internet's interface.
var _ ipstack.Device = (*packetQueue)(nil)

// newPacketQueue returns empty queues for datagrams of at most mtu bytes.
func newPacketQueue(mtu int) *packetQueue {
	return &packetQueue{mtu: mtu}
}

// capabilities describes the device to the interface: bare IP datagrams of
// at most mtu bytes, checksums computed and verified in software.
func (q *packetQueue) capabilities() ipstack.DeviceCapabilities {
	return ipstack.DeviceCapabilities{MTU: q.mtu}
}

// pushReceived queues a datagram from the PPP link for the stack.
func (q *packetQueue) pushReceived(datagram []byte) {
	q.received = append(q.received, datagram)
}

// nextReceived returns the datagram the stack will process next, if any.
func (q *packetQueue) nextReceived() ([]byte, bool) {
	if len(q.received) == 0 {
		return nil, false
	}
	return q.received[0], true
}

// discardNextReceived drops the datagram the stack would process next.
func (q *packetQueue) discardNextReceived() {
	popFront(&q.received)
}

// popTransmitted returns the oldest datagram the stack sent, for the PPP
// link.
func (q *packetQueue) popTransmitted() ([]byte, bool) {
	return popFront(&q.transmitted)
}

// Receive hands the stack the next received datagram, with a slot for its
// reply.
func (q *packetQueue) Receive(time.Time) ([]byte, ipstack.TransmitSlot, bool) {
	datagram, ok := popFront(&q.received)
	if !ok {
		return nil, nil, false
	}
	return datagram, q.transmit, true
}

// Transmit gives the stack a slot: the queue has room for everything.
func (q *packetQueue) Transmit(time.Time) ipstack.TransmitSlot {
	return q.transmit
}

func (q *packetQueue) transmit(datagram []byte) {
	q.transmitted = append(q.transmitted, datagram)
}

// popFront removes and returns the first datagram of queue, if any.
func popFront(queue *[][]byte) ([]byte, bool) {
	if len(*queue) == 0 {
		return nil, false
	}
	datagram := (*queue)[0]
	(*queue)[0] = nil // Let the datagram go with its last user.
	*queue = (*queue)[1:]
	return datagram, true
}
