package socket

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// ErrInvalidIPAddr reports an interface address that is not an IPv4 unicast
// address (or 0.0.0.0); smoltcp panics on those.
var ErrInvalidIPAddr = errors.New("invalid interface address")

// Context is what a socket knows about the interface it belongs to: the
// current time, the random source, the largest datagram the device carries,
// and the interface's addresses. smoltcp calls it iface::Context
// (InterfaceInner).
//
// The interface owns the context and is the only one that changes it, at the
// start of every poll and when its addresses change; sockets only read it.
// Tests create their own with NewContext. The zero Context is like one from
// NewContext(time.Time{}, 0, 0): at the epoch, seeded with 0, without
// addresses, and with an IP MTU of 0, which no datagram fits. A Context is
// not safe for concurrent use.
type Context struct {
	now   time.Time
	rand  Rand
	ipMTU int
	anyIP bool
	// ipAddrs are the interface's addresses, every one IPv4 unicast or
	// 0.0.0.0, in the order they were set.
	ipAddrs []netip.Prefix
}

// NewContext returns a context at time now, with a random source seeded with
// randomSeed, for a device whose datagrams are at most ipMTU octets, and with
// no addresses.
func NewContext(now time.Time, randomSeed uint64, ipMTU int) *Context {
	return &Context{now: now, rand: Rand{state: randomSeed}, ipMTU: ipMTU}
}

// Now returns the time of the poll in progress (or of the last one).
func (cx *Context) Now() time.Time {
	return cx.now
}

// SetNow sets the current time; the interface calls it at the start of every
// poll.
func (cx *Context) SetNow(now time.Time) {
	cx.now = now
}

// Rand returns the random source: TCP initial sequence numbers and DNS query
// ids and ports come from it.
func (cx *Context) Rand() *Rand {
	return &cx.rand
}

// IPMTU returns the largest IP datagram the device carries, in octets. With
// bare-IP devices, the only kind the stack supports, it is the device's MTU.
func (cx *Context) IPMTU() int {
	return cx.ipMTU
}

// IPAddrs returns a copy of the interface's addresses, each with the prefix
// length of its network.
func (cx *Context) IPAddrs() []netip.Prefix {
	return slices.Clone(cx.ipAddrs)
}

// SetIPAddrs replaces the interface's addresses. Each must be an IPv4
// unicast address, or 0.0.0.0, with a valid prefix length; otherwise
// SetIPAddrs returns an error wrapping ErrInvalidIPAddr that names the
// address ("IP address 224.0.0.1 is not unicast"), and changes nothing.
func (cx *Context) SetIPAddrs(addrs []netip.Prefix) error {
	for _, prefix := range addrs {
		if err := checkInterfaceAddress(prefix); err != nil {
			return err
		}
	}
	cx.ipAddrs = slices.Clone(addrs)
	return nil
}

// AnyIP reports whether the interface accepts datagrams for every address,
// not only its own.
func (cx *Context) AnyIP() bool {
	return cx.anyIP
}

// SetAnyIP sets whether the interface accepts datagrams for every address.
func (cx *Context) SetAnyIP(anyIP bool) {
	cx.anyIP = anyIP
}

// HasIPAddr reports whether addr is one of the interface's addresses; with
// any-IP on, every address is.
func (cx *Context) HasIPAddr(addr netip.Addr) bool {
	if cx.anyIP {
		return true
	}
	return slices.ContainsFunc(cx.ipAddrs, func(prefix netip.Prefix) bool {
		return prefix.Addr() == addr
	})
}

// IPv4Addr returns the interface's first IPv4 address, if it has one.
func (cx *Context) IPv4Addr() (netip.Addr, bool) {
	for _, prefix := range cx.ipAddrs {
		if prefix.Addr().Is4() {
			return prefix.Addr(), true
		}
	}
	return netip.Addr{}, false
}

// SourceAddress returns the address to send from when sending to dst: the
// first of the interface's addresses whose network contains dst, or else its
// first IPv4 address; false when it has none, or when dst is not IPv4.
func (cx *Context) SourceAddress(dst netip.Addr) (netip.Addr, bool) {
	if !dst.Is4() {
		return netip.Addr{}, false
	}
	for _, prefix := range cx.ipAddrs {
		if prefix.Contains(dst) {
			return prefix.Addr(), true
		}
	}
	return cx.IPv4Addr()
}

// IsBroadcast reports whether addr is a broadcast address: the limited
// broadcast 255.255.255.255, or the broadcast address of one of the
// interface's networks.
func (cx *Context) IsBroadcast(addr netip.Addr) bool {
	if wire.IsBroadcast(addr) {
		return true
	}
	return slices.ContainsFunc(cx.ipAddrs, func(prefix netip.Prefix) bool {
		broadcast, ok := wire.SubnetBroadcast(prefix)
		return ok && broadcast == addr
	})
}

// IsUnicast reports whether addr is a unicast address from the interface's
// point of view: unicast (wire.IsUnicast) and not the broadcast address of
// one of its networks.
func (cx *Context) IsUnicast(addr netip.Addr) bool {
	return wire.IsUnicast(addr) && !cx.IsBroadcast(addr)
}

// checkInterfaceAddress returns an error wrapping ErrInvalidIPAddr unless
// prefix can be an interface address: smoltcp's check_ip_addrs, which allows
// unicast addresses and the unspecified one, plus the checks that smoltcp's
// types make unnecessary (IPv4 only, a prefix length of at most 32).
func checkInterfaceAddress(prefix netip.Prefix) error {
	addr := prefix.Addr()
	switch {
	case !addr.Is4():
		return fmt.Errorf("%w: IP address %v is not IPv4", ErrInvalidIPAddr, addr)
	case !prefix.IsValid():
		return fmt.Errorf("%w: IP address %v has an invalid prefix length", ErrInvalidIPAddr, addr)
	case !wire.IsUnicast(addr) && !addr.IsUnspecified():
		return fmt.Errorf("%w: IP address %v is not unicast", ErrInvalidIPAddr, addr)
	default:
		return nil
	}
}
