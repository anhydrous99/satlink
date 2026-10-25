package satnet

import (
	"errors"
	"net"
	"net/netip"
	"runtime"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
)

// UDP datagrams over the satellite link, to and from one allowed peer.

// UDPConn is a UDP socket bound to one peer, through a [Link]: it sends
// only to that peer and drops datagrams from anyone else. It is a net.Conn
// with datagram semantics: each Write sends one datagram, each Read returns
// one.
//
// Datagrams already written are still delivered after Close, as long as the
// link stays up. Reads and writes wait until their deadline (none by
// default), as for a [TCPConn]; while the link drains after
// [Link.RequestClose], Read keeps working and Write fails with ErrLinkDown.
// Like a net.Conn, a UDPConn is safe for concurrent use by multiple
// goroutines.
//
// The zero UDPConn is not usable; [Link.DialUDP] and [Link.DialContext]
// make one.
type UDPConn struct {
	conn *connection
}

var _ net.Conn = (*UDPConn)(nil)

// newUDPConn returns the socket handle names, bound to local, talking to
// peer. A UDPConn collected without Close is closed as Close closes it: the
// cleanup holds the connection's state, never the UDPConn.
func newUDPConn(sharedEngine *shared, handle ipstack.SocketHandle, local, peer netip.AddrPort) *UDPConn {
	conn := &UDPConn{conn: newConnection(sharedEngine, handle, local, peer)}
	runtime.AddCleanup(conn, releaseUDP, conn.conn)
	return conn
}

// releaseUDP lets the socket go, once its queue is sent.
func releaseUDP(conn *connection) {
	conn.close((*stack).closeUDP)
}

// Read waits until the read deadline for a datagram from the peer and
// copies it into p; a datagram longer than p is truncated and the rest
// discarded. A datagram already received is returned even when the deadline
// has passed. It fails with ErrTimedOut for operation "UDP receive" and
// with ErrLinkDown.
func (c *UDPConn) Read(p []byte) (int, error) {
	read := 0
	err := c.conn.wait(operationUDPReceive, c.conn.readUntil, func(e *engine) (bool, error) {
		received, ok := receiveFrom(e.stack.udp(c.conn.handle), c.conn.peer, p)
		if !ok {
			return false, nil
		}
		e.recordActivity(time.Now())
		read = received
		return true, nil
	})
	return read, err
}

// Write sends p as one datagram. It waits its turn on the line, as a
// [TCPConn]'s data does: only a few datagrams queue, and then Write waits,
// until the write deadline, for those written before to leave. So a writer
// goes at the line's pace. A closing link would never send it: Write then
// fails with ErrLinkDown. It fails with ErrDatagramTooLarge beyond
// [UDPConn.MaxDatagramLen], and ErrTimedOut for operation "UDP send".
func (c *UDPConn) Write(p []byte) (int, error) {
	err := c.conn.wait(operationUDPSend, c.conn.writeUntil, func(e *engine) (bool, error) {
		return queueDatagram(e, c.conn, p)
	})
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// queueDatagram is one attempt at queueing p as a datagram for conn's peer,
// which is not over while the socket's turn on the line has yet to come or
// its send buffer is full.
func queueDatagram(e *engine, conn *connection, p []byte) (bool, error) {
	// A closing link would never send it.
	if err := e.checkUsable(); err != nil {
		return true, err
	}
	if limit := e.stack.maxUDPPayload(); len(p) > limit {
		return true, &Error{Kind: ErrDatagramTooLarge, Len: len(p), Max: limit}
	}
	// The stack hands the device all the socket holds at every poll, line
	// or no line: only this wait keeps the backlog in the app.
	if !e.stack.udpTurnHasCome(conn.handle, conn.peer) {
		return false, nil
	}
	err := e.stack.udp(conn.handle).SendSlice(p, udp.Metadata{Endpoint: conn.peer})
	switch {
	case err == nil:
		e.recordActivity(time.Now())
		return true, nil
	case errors.Is(err, udp.ErrBufferFull):
		return false, nil
	default:
		return true, e.connectionError()
	}
}

// Close lets the socket go once its queued datagrams have been sent. It
// never waits, and it wakes the Read and Write calls waiting in other
// goroutines, which return net.ErrClosed, as net.Conn requires. After
// Close, Read, Write and the deadline setters fail with net.ErrClosed;
// Close itself returns nil again.
func (c *UDPConn) Close() error {
	releaseUDP(c.conn)
	return nil
}

// MaxDatagramLen returns the largest datagram that fits in one packet on
// the link, which depends on what the gateway accepts: the link's MTU less
// 28 bytes of IPv4 and UDP headers.
func (c *UDPConn) MaxDatagramLen() int {
	e := c.conn.shared.lock()
	defer c.conn.shared.unlock()
	return e.stack.maxUDPPayload()
}

// PeerAddr returns the only address this socket talks to.
func (c *UDPConn) PeerAddr() netip.AddrPort {
	return c.conn.peer
}

// LocalAddr returns our end of the socket, a *net.UDPAddr: our address on
// the link now, and the socket's port. The socket is bound to its port
// only, so after PPP renegotiates a new address its datagrams leave from
// that one, and LocalAddr says so.
func (c *UDPConn) LocalAddr() net.Addr {
	e := c.conn.shared.lock()
	defer c.conn.shared.unlock()
	return net.UDPAddrFromAddrPort(e.stack.localEndpoint(c.conn.local.Port()))
}

// RemoteAddr returns the peer, a *net.UDPAddr.
func (c *UDPConn) RemoteAddr() net.Addr {
	return net.UDPAddrFromAddrPort(c.conn.peer)
}

// SetDeadline sets the read and write deadlines; the zero time means none.
func (c *UDPConn) SetDeadline(deadline time.Time) error {
	return c.conn.setDeadlines(deadline, &c.conn.readDeadline, &c.conn.writeDeadline)
}

// SetReadDeadline sets the deadline for future reads and any read already
// waiting; the zero time means none.
func (c *UDPConn) SetReadDeadline(deadline time.Time) error {
	return c.conn.setDeadlines(deadline, &c.conn.readDeadline)
}

// SetWriteDeadline sets the deadline for future writes and any write
// already waiting; the zero time means none.
func (c *UDPConn) SetWriteDeadline(deadline time.Time) error {
	return c.conn.setDeadlines(deadline, &c.conn.writeDeadline)
}

// receiveFrom copies the next datagram from peer into p, dropping any from
// elsewhere, and returns its length; false when nothing from the peer is
// waiting.
func receiveFrom(socket *udp.Socket, peer netip.AddrPort, p []byte) (int, bool) {
	for {
		payload, metadata, err := socket.Receive()
		if err != nil {
			return 0, false
		}
		if metadata.Endpoint == peer {
			return copy(p, payload), true
		}
	}
}
