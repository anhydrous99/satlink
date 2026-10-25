package ipstack

import (
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// Config configures a new Interface.
type Config struct {
	// RandomSeed seeds TCP initial sequence numbers and DNS query ids and
	// ports. It should differ on every run, to avoid confusion with the
	// connections of an earlier one, but need not be cryptographically
	// secure: the values it seeds are predictable anyway (socket.Rand).
	RandomSeed uint64
}

// PollResult says whether a poll may have changed any socket.
type PollResult int

const (
	// PollNone guarantees that no socket changed: the poll received nothing
	// but empty datagrams, if anything, and sent nothing.
	PollNone PollResult = iota
	// PollSocketStateChanged means sockets may have received data or
	// finished operations, and are worth checking: the poll received a
	// datagram that was not empty, or sent something.
	PollSocketStateChanged
)

// String returns the variant name.
func (r PollResult) String() string {
	switch r {
	case PollNone:
		return "None"
	case PollSocketStateChanged:
		return "SocketStateChanged"
	default:
		return "PollResult(" + strconv.Itoa(int(r)) + ")"
	}
}

// PollIngressSingleResult says what PollIngressSingle did.
type PollIngressSingleResult int

const (
	// IngressNone means no datagram was waiting, and no socket changed:
	// there is no need to call PollIngressSingle again until more arrive.
	IngressNone PollIngressSingleResult = iota
	// IngressPacketProcessed means an empty datagram was taken, which
	// changes no socket; more may be waiting. It is returned for empty
	// datagrams only.
	IngressPacketProcessed
	// IngressSocketStateChanged means a datagram that was not empty was
	// processed, and sockets may have changed; more may be waiting. It is
	// returned for every such datagram, whether a socket took it or the
	// interface answered or dropped it.
	IngressSocketStateChanged
)

// String returns the variant name.
func (r PollIngressSingleResult) String() string {
	switch r {
	case IngressNone:
		return "None"
	case IngressPacketProcessed:
		return "PacketProcessed"
	case IngressSocketStateChanged:
		return "SocketStateChanged"
	default:
		return "PollIngressSingleResult(" + strconv.Itoa(int(r)) + ")"
	}
}

// Interface is a network interface: it filters the datagrams a Device
// receives, hands them to the sockets they are for, answers what no socket
// wants, and sends what the sockets have to send.
//
// Datagrams for addresses that are not the interface's own are dropped,
// unless any-IP is on. A TCP segment no socket accepts is answered with a
// RST, and a UDP datagram no socket accepts, or a datagram of a protocol the
// stack does not handle, with an ICMP destination-unreachable message. Echo
// requests are not answered.
//
// The stack does not fragment. A datagram to send that is larger than the
// MTU is dropped once the device has handed out its transmit slot, which goes
// unused, and it counts as sent: a UDP or DNS socket dequeues it, and Poll
// runs another round of sending. Received datagrams are not checked against
// the MTU.
//
// The zero Interface is not usable; NewInterface makes one. An Interface is
// not safe for concurrent use.
type Interface struct {
	// cx is what the sockets know of the interface: the time of the poll,
	// the random source, the IP MTU and the addresses.
	cx *socket.Context
	// checksums says which checksums the stack verifies and computes, as
	// the device asked.
	checksums wire.ChecksumCapabilities
}

// NewInterface returns an interface for a device with capabilities, at time
// now, with no addresses and any-IP off. It reads capabilities only here: to
// change the MTU, create a new interface. It panics if capabilities.MTU is
// below MinMTU.
//
// It draws from the random source before any socket does, so that a seed gives
// the sockets their initial sequence numbers and DNS query ids and ports.
func NewInterface(config Config, capabilities DeviceCapabilities, now time.Time) *Interface {
	if capabilities.MTU < MinMTU {
		panic(fmt.Sprintf("ipstack: an MTU of %d is below MinMTU, %d", capabilities.MTU, MinMTU))
	}
	// Devices carry bare IP datagrams, so the IP MTU is the device's.
	cx := socket.NewContext(now, config.RandomSeed, capabilities.MTU)
	// The identifier is used only to fragment datagrams, which the stack does
	// not do. Drawing it all the same keeps the sockets' draws the same for a
	// given seed.
	_ = fragmentIdentifier(cx.Rand())
	return &Interface{cx: cx, checksums: capabilities.Checksum}
}

// fragmentIdentifier draws the identification the interface gives the IPv4
// datagrams it fragments (RFC 791 § 3.1): a random Uint16 other than 0, drawn
// again until it is one. It is drawn before any socket draws.
func fragmentIdentifier(random *socket.Rand) uint16 {
	for {
		if identifier := random.Uint16(); identifier != 0 {
			return identifier
		}
	}
}

// Context returns the context the interface hands its sockets, which some
// socket methods need (tcp.Socket.Connect, dns.Socket.StartQuery).
func (i *Interface) Context() *socket.Context {
	return i.cx
}

// SetIPAddrs replaces the interface's addresses.
// Each must be an IPv4 unicast address, or 0.0.0.0, with a valid prefix
// length; otherwise SetIPAddrs returns an error wrapping
// socket.ErrInvalidIPAddr and changes nothing (socket.Context.SetIPAddrs). A
// TCP connection whose local address goes away is reset, without a RST, at
// the next poll.
func (i *Interface) SetIPAddrs(addrs []netip.Prefix) error {
	return i.cx.SetIPAddrs(addrs)
}

// SetAnyIP sets whether the interface accepts datagrams for every address,
// not only its own; it is off by default. The simulated internet turns it on
// to answer for every destination.
func (i *Interface) SetAnyIP(anyIP bool) {
	i.cx.SetAnyIP(anyIP)
}

// Poll processes every datagram the device has received, then sends what the
// sockets have to send, at time now. Sending goes in rounds, each offering
// every socket a turn in slot order, and Poll starts another round as long as
// the last one sent something. It reports whether any socket may have
// changed.
//
// Poll processes an unbounded number of datagrams if they arrive faster than
// it handles them; PollIngressSingle bounds the work per call.
func (i *Interface) Poll(now time.Time, device Device, sockets *SocketSet) PollResult {
	i.cx.SetNow(now)
	result := PollNone
	// Everything received reaches the sockets before they send, so that what
	// they send answers it: acknowledgements, window updates and resets
	// included.
	for {
		ingress := i.socketIngress(device, sockets)
		if ingress == IngressNone {
			break
		}
		if ingress == IngressSocketStateChanged {
			result = PollSocketStateChanged
		}
	}
	for i.socketEgress(device, sockets) == PollSocketStateChanged {
		result = PollSocketStateChanged
	}
	return result
}

// PollIngressSingle processes at most one received datagram, at time now,
// sending its immediate reply, if any, through the datagram's reply slot. It
// sends nothing else and never calls Transmit; Poll does. It returns
// IngressNone when no datagram is waiting.
func (i *Interface) PollIngressSingle(now time.Time, device Device, sockets *SocketSet) PollIngressSingleResult {
	i.cx.SetNow(now)
	return i.socketIngress(device, sockets)
}

// PollAt returns when Poll should next be called, given the time now: now
// itself if a socket has something to send, the earliest socket timer
// otherwise, which may have passed already (a DNS query not yet sent is due
// at the epoch, the zero time), and false if only incoming datagrams can
// change anything. Polling earlier is harmless, polling later delays the
// sockets.
func (i *Interface) PollAt(now time.Time, sockets *SocketSet) (time.Time, bool) {
	i.cx.SetNow(now)
	earliest := socket.PollOnIngress()
	for _, item := range sockets.items() {
		earliest = socket.Earliest(earliest, item.pollAt(i.cx))
	}
	return earliest.Deadline(now)
}
