package ipstack

import (
	"net/netip"
	"testing"
	"time"
)

// testingDeviceMTU is the MTU of smoltcp's TestingDevice on Medium::Ip.
const testingDeviceMTU = 1500

// unlimitedSlots is the transmit slot budget of a device that never refuses
// one.
const unlimitedSlots = -1

// testingDevice is smoltcp's TestingDevice (src/tests.rs): received
// datagrams come from one queue, and every datagram the interface sends,
// through a reply slot or a transmit slot, goes to another, in order. For
// the pull-model tests it also refuses transmit slots on demand, counts the
// calls to Transmit, and records which datagrams were replies.
type testingDevice struct {
	// received holds the datagrams the interface has yet to receive,
	// oldest first (smoltcp's rx_queue).
	received [][]byte
	// sent holds what the interface sent, oldest first (smoltcp's
	// tx_queue).
	sent []sentDatagram
	// transmitSlots is how many more transmit slots the device grants, or
	// unlimitedSlots.
	transmitSlots int
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
	return &testingDevice{transmitSlots: unlimitedSlots}
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

// Transmit grants a slot while the device has slots left.
func (d *testingDevice) Transmit(time.Time) (TransmitSlot, bool) {
	d.transmitRequests++
	if d.transmitSlots == 0 {
		return nil, false
	}
	if d.transmitSlots > 0 {
		d.transmitSlots--
	}
	return func(datagram []byte) {
		d.sent = append(d.sent, sentDatagram{datagram: datagram})
	}, true
}

// deliver queues datagrams for the interface to receive.
func (d *testingDevice) deliver(datagrams ...[]byte) {
	d.received = append(d.received, datagrams...)
}

// refuseTransmit makes the device refuse every transmit slot from now on.
func (d *testingDevice) refuseTransmit() {
	d.transmitSlots = 0
}

// grantTransmit makes the device grant every transmit slot from now on.
func (d *testingDevice) grantTransmit() {
	d.transmitSlots = unlimitedSlots
}

// takeSent returns what the interface sent since the last call.
func (d *testingDevice) takeSent() []sentDatagram {
	sent := d.sent
	d.sent = nil
	return sent
}

// setup returns smoltcp's test interface (tests::setup(Medium::Ip)): made at
// the epoch with seed 0 for a testing device, with the addresses
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
