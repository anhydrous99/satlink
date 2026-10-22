package satnet

import (
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
)

// What TCP connections and UDP sockets share: their socket in the link's
// stack, whether this side has closed them, and their deadlines.

// connection is the state behind a TCPConn or a UDPConn. It refers to the
// link's engine, never to the Link, so that holding a connection does not
// keep a dropped Link — and its call — alive; and nothing in it refers to
// the TCPConn or UDPConn, so that a runtime cleanup can close a collected
// one.
type connection struct {
	shared *shared
	// handle is the socket in the engine's stack. It stays valid until the
	// connection is closed: the stack keeps a socket until its owner lets
	// go.
	handle ipstack.SocketHandle
	// local and peer are the ends of the connection when it opened. A new
	// address resets a TCP connection, while a UDP socket carries on from
	// it: UDPConn.LocalAddr looks the address up.
	local netip.AddrPort
	peer  netip.AddrPort
	// closed reports that this side closed the connection; the engine's
	// lock guards it.
	closed bool
	// readDeadline and writeDeadline are the net.Conn deadlines; the zero
	// time means none.
	readDeadline  atomicTime
	writeDeadline atomicTime
}

// newConnection returns the state of a connection over the socket handle
// names, between local and peer.
func newConnection(sharedEngine *shared, handle ipstack.SocketHandle, local, peer netip.AddrPort) *connection {
	return &connection{shared: sharedEngine, handle: handle, local: local, peer: peer}
}

// wait waits, as waitUntil does, for operation until the deadline, giving up
// with net.ErrClosed once the connection is closed.
func (c *connection) wait(operation string, until deadline, try attempt) error {
	return c.shared.waitUntil(waitTerms{operation: operation, until: until, closed: &c.closed}, try)
}

// unlessClosed runs try once with the engine locked, unless the connection
// is closed.
func (c *connection) unlessClosed(try func(e *engine) error) error {
	e := c.shared.lock()
	defer c.shared.unlock()
	if c.closed {
		return net.ErrClosed
	}
	return try(e)
}

// close closes the connection once, letting go of its socket with release;
// later calls do nothing. It wakes the calls waiting on the connection,
// which then return net.ErrClosed.
func (c *connection) close(release func(s *stack, handle ipstack.SocketHandle)) {
	c.shared.update(func(e *engine) {
		if c.closed {
			return
		}
		c.closed = true
		release(e.stack, c.handle)
	})
}

// setDeadlines sets each of deadlines, the connection's own, to deadline,
// and wakes the calls waiting on the connection so that they apply it. It
// fails with net.ErrClosed once the connection is closed.
func (c *connection) setDeadlines(deadline time.Time, deadlines ...*atomicTime) error {
	err := c.unlessClosed(func(*engine) error {
		for _, stored := range deadlines {
			stored.Store(deadline)
		}
		return nil
	})
	c.shared.notify()
	return err
}

// readUntil and writeUntil are the connection's own deadlines, looked up
// whenever a wait wakes.
func (c *connection) readUntil() time.Time  { return c.readDeadline.Load() }
func (c *connection) writeUntil() time.Time { return c.writeDeadline.Load() }

// atomicTime is a time.Time that goroutines can read while another sets
// it. Its zero value holds the zero time.
type atomicTime struct {
	value atomic.Pointer[time.Time]
}

// Load returns the time last stored, or the zero time.
func (a *atomicTime) Load() time.Time {
	if stored := a.value.Load(); stored != nil {
		return *stored
	}
	return time.Time{}
}

// Store sets the time.
func (a *atomicTime) Store(at time.Time) {
	a.value.Store(&at)
}
