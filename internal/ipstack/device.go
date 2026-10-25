package ipstack

import (
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// Device is a network device that carries bare IPv4 datagrams, without a
// link-layer header, as on a PPP link.
//
// The interface asks for the next received datagram when it polls, and asks
// for a transmit slot each time a socket has a datagram ready to send. A
// Device is used by one interface at a time, from one goroutine.
type Device interface {
	// Receive takes the next received datagram, if any, and returns it with
	// a slot for a reply: the interface answers some datagrams at once (an
	// ACK, a RST, an ICMP error), with zero or one datagrams for each one it
	// receives. The interface reads datagram only until the poll call that
	// took it returns, so the device may reuse its storage afterwards.
	Receive(now time.Time) (datagram []byte, reply TransmitSlot, ok bool)
	// Transmit returns a slot for one outgoing datagram the interface has
	// ready. The sockets count the datagram as sent once it is in the slot,
	// so a device whose line cannot take it yet must hold it.
	Transmit(now time.Time) TransmitSlot
}

// TransmitSlot sends one datagram through the device that handed out the
// slot; the device owns the datagram from then on. The interface uses each
// slot at most once, and may leave one unused, as it does with a reply slot
// when it has nothing to answer or a datagram that is larger than the MTU
// (which it drops: the stack does not fragment).
type TransmitSlot func(datagram []byte)

// DeviceCapabilities describes what a Device can carry. NewInterface takes
// it, and reads it only then: an interface for a device whose MTU changes
// has to be created again.
type DeviceCapabilities struct {
	// MTU is the largest datagram the device sends or receives, in octets.
	// As devices carry bare IP datagrams, it is also the IP MTU.
	MTU int
	// Checksum says which checksums the stack verifies and computes; the
	// zero value has it do all of them, as devices without checksum offload
	// need.
	Checksum wire.ChecksumCapabilities
}

// MinMTU is the smallest MTU an interface accepts: an IPv4 and a TCP header,
// the overhead of every TCP segment, from which the sockets compute their
// maximum segment size. PPP never negotiates less (its minimum MRU is 68).
const MinMTU = wire.IPv4HeaderLen + wire.TCPHeaderLen
