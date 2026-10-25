package ipstack

import (
	"net/netip"
	"testing"
	"time"
)

// testingDeviceMTU is the MTU of the testing device.
const testingDeviceMTU = 1500

// testingDevice is a device for the tests, modelled on smoltcp's
// TestingDevice: received datagrams come from one queue, and every datagram the interface sends,
// through a reply slot or a transmit slot, goes to another, in order. For
// the pull-model tests it also counts the calls to Transmit, and records
// which datagrams were replies.
type testingDevice struct {
	// received holds the datagrams the interface has yet to receive,
	// oldest first.
	received [][]byte
	// sent holds what the interface sent, oldest first.
	sent []sentDatagram
	// transmitRequests counts the calls to Transmit.
	transmitRequests int
}

// sentDatagram is a datagram the interface sent through the device.
type sentDatagram struct {
	datagram []byte
	// isReply says whether it went through a received datagram's reply
	// slot rather than through a slot from Transmit.
	isReply bool
}

func newTestingDevice() *testingDevice {
	return &testingDevice{}
}

// Receive hands over the oldest received datagram, with a reply slot that
// always takes a datagram.
func (d *testingDevice) Receive(time.Time) ([]byte, TransmitSlot, bool) {
	if len(d.received) == 0 {
		return nil, nil, false
	}
	datagram := d.received[0]
	d.received = d.received[1:]
	return datagram, func(reply []byte) {
		d.sent = append(d.sent, sentDatagram{datagram: reply, isReply: true})
	}, true
}

// Transmit hands out a slot, and counts the call.
func (d *testingDevice) Transmit(time.Time) TransmitSlot {
	d.transmitRequests++
	return func(datagram []byte) {
		d.sent = append(d.sent, sentDatagram{datagram: datagram})
	}
}

// deliver queues datagrams for the interface to receive.
func (d *testingDevice) deliver(datagrams ...[]byte) {
	d.received = append(d.received, datagrams...)
}

// takeSent returns what the interface sent since the last call.
func (d *testingDevice) takeSent() []sentDatagram {
	sent := d.sent
	d.sent = nil
	return sent
}

// setup returns the test interface, made at the epoch with seed 0 for a
// testing device, with the addresses
// 192.168.1.1/24 and 127.0.0.1/8, with an empty socket set and the device.
func setup(t *testing.T) (*Interface, *SocketSet, *testingDevice) {
	t.Helper()
	iface := NewInterface(Config{}, DeviceCapabilities{MTU: testingDeviceMTU}, time.Time{})
	addresses := []netip.Prefix{netip.PrefixFrom(localAddr, 24), netip.PrefixFrom(loopbackAddr, 8)}
	if err := iface.SetIPAddrs(addresses); err != nil {
		t.Fatalf("SetIPAddrs: %v", err)
	}
	return iface, &SocketSet{}, newTestingDevice()
}
