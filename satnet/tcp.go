package satnet

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
)

// TCP connections over the satellite link.

// TCPConn is a TCP connection through a [Link]. It is a net.Conn, and like
// one it is safe for concurrent use by multiple goroutines: each Write and
// WriteSome goes out whole, after any other under way, as a net.TCPConn's
// does.
//
// Reads and writes block, as a net.TCPConn's do, until their deadline (none
// by default). Read returns io.EOF only at the end of the stream; an
// expired deadline fails with an [*Error] of kind ErrTimedOut, which is a
// net.Error whose Timeout method reports true. Changing a deadline affects
// calls already waiting.
//
// Close is graceful: what was written is still delivered while the link
// stays up, and [Link.Close] waits for it. A TCPConn that is garbage
// collected without Close is closed the same way. [TCPConn.Abort] resets
// the connection instead, so that the peer cannot take a part of what was
// written for the whole. While the link drains after [Link.RequestClose],
// Read and WaitAcknowledged keep working and Write fails with ErrLinkDown.
// Once the hang-up starts, the connection ends, and its calls fail with
// ErrLinkDown but for what is already settled: Read still returns what had
// arrived, up to the end of the stream if the peer had finished, and
// WaitAcknowledged returns nil when everything written had been
// acknowledged; CloseWrite has nothing left to end, and returns nil.
//
// The zero TCPConn is not usable; [Link.Dial] and [Link.DialContext] make
// one.
type TCPConn struct {
	conn *connection
	// writeMu makes each Write and WriteSome go out whole: a write waiting
	// for room would otherwise take turns at it with another, and the peer
	// would get their bytes interleaved. Nothing that wakes a waiting write
	// takes it.
	writeMu sync.Mutex
}

var _ net.Conn = (*TCPConn)(nil)

// newTCPConn returns the connection over the established socket handle
// names, from local to peer. A TCPConn collected without Close is closed as
// Close closes it: the cleanup holds the connection's state, never the
// TCPConn.
func newTCPConn(sharedEngine *shared, handle ipstack.SocketHandle, local, peer netip.AddrPort) *TCPConn {
	conn := &TCPConn{conn: newConnection(sharedEngine, handle, local, peer)}
	runtime.AddCleanup(conn, closeTCPGracefully, conn.conn)
	return conn
}

// closeTCPGracefully closes the connection gracefully, once.
func closeTCPGracefully(conn *connection) {
	conn.close((*stack).closeTCP)
}

// Read reads into p, waiting until the read deadline for at least one byte.
// Data already received is returned even when the deadline has passed, so a
// deadline of now takes only what has arrived. It returns io.EOF once the
// peer has finished sending and everything before has been read; an empty
// p returns at once.
//
// It fails with ErrTimedOut for operation "TCP read", ErrConnectionReset
// when the peer reset the connection, and ErrLinkDown when the link went
// down. After [TCPConn.CloseWrite], what the peer sent stays readable for
// ten seconds once it has finished too (TCP's TIME-WAIT); the connection
// then ends, discarding what was not read, and Read fails with
// ErrConnectionReset.
func (c *TCPConn) Read(p []byte) (int, error) {
	read := 0
	err := c.conn.wait(operationTCPRead, c.conn.readUntil, func(e *engine) (bool, error) {
		if len(p) == 0 {
			return true, nil
		}
		received, finished, err := receiveNow(e, c.conn.handle, p)
		read = received
		switch {
		case err != nil:
			return true, err
		case finished:
			return true, io.EOF
		default:
			return received > 0, nil
		}
	})
	return read, err
}

// Write queues all of p for sending, waiting until the write deadline for
// room in the send buffer. It returns once the data is queued:
// [TCPConn.WaitAcknowledged] tells when it has arrived. A closing link only
// delivers what was written before: Write then fails with ErrLinkDown.
// After ErrTimedOut, n says how much of p was queued: that part is still
// sent, so exactly the rest can be written again. Unless the protocol tells
// a part from the whole, [TCPConn.Abort] keeps the peer from taking what
// arrived for all of it; closing would end the stream cleanly after the
// part. So would a [TCPConn.CloseWrite] in another goroutine while Write
// waits for room: Write then fails with ErrConnectionReset, and the n bytes
// it queued still go, before the end of the stream. After the
// ErrConnectionReset of a reset — by the peer, by Abort, or by PPP
// renegotiating a new address — nothing more is sent, and after ErrLinkDown
// only a link closing at the app's request still delivers what was queued,
// within [Tuning.CloseGrace]. [TCPConn.WriteSome] returns as soon as part
// of p is queued.
//
// It fails with ErrTimedOut for operation "TCP write", ErrConnectionReset,
// and ErrLinkDown.
func (c *TCPConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	written := 0
	err := c.conn.wait(operationTCPWrite, c.conn.writeUntil, func(e *engine) (bool, error) {
		if written == len(p) {
			return true, nil
		}
		queued, err := sendNow(e, c.conn.handle, p[written:])
		written += queued
		return written == len(p), err
	})
	return written, err
}

// WriteSome queues some of p for sending, waiting until the write deadline
// for room in the send buffer, and returns how many bytes were queued: as
// many as fit once there was room, perhaps not all of p. That part is still
// sent, so exactly the rest can be written again. Unlike Write, it returns
// as soon as anything is queued; an
// error means that nothing was, and an empty p queues nothing without
// waiting for room.
//
// It fails as Write does.
func (c *TCPConn) WriteSome(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	queued := 0
	err := c.conn.wait(operationTCPWrite, c.conn.writeUntil, func(e *engine) (bool, error) {
		if len(p) == 0 {
			return true, nil
		}
		var err error
		queued, err = sendNow(e, c.conn.handle, p)
		return queued > 0, err
	})
	return queued, err
}

// Close closes the connection gracefully: queued data is still sent, then
// the connection ends with a FIN. Nobody reads it any more, so no keepalive
// airtime is spent on it; a peer that never says goodbye is timed out
// instead. Close never waits, and it wakes the Read, Write and
// WaitAcknowledged calls waiting in other goroutines, which return
// net.ErrClosed, as net.Conn requires. After Close, Read, Write,
// CloseWrite, WaitAcknowledged and the deadline setters fail with
// net.ErrClosed; Close itself returns nil again.
func (c *TCPConn) Close() error {
	closeTCPGracefully(c.conn)
	return nil
}

// CloseWrite finishes sending: the peer reads the end of the stream once
// everything queued has arrived, and writes fail with ErrConnectionReset.
// This side can still read — until ten seconds after the peer has finished
// too, when the connection ends and discards what was not read (see
// [TCPConn.Read]).
func (c *TCPConn) CloseWrite() error {
	return c.change(func(s *stack) { s.shutdownTCP(c.conn.handle) })
}

// Abort ends the connection at once with a reset, dropping what is still
// queued: the peer must not take what it got for the whole stream. The
// calls waiting on the connection wake and fail with ErrConnectionReset,
// and so do later calls, but for what is already settled: Read still
// returns what had arrived, up to the end of the stream if the peer had
// finished, and WaitAcknowledged returns nil when everything written had
// been acknowledged; CloseWrite has nothing left to end, and returns nil.
// Close still lets the connection go. After Close, Abort fails with
// net.ErrClosed.
func (c *TCPConn) Abort() error {
	// Not the stack's abortTCP: the socket, and so the handle, must stay
	// valid until the connection is closed.
	return c.change(func(s *stack) { s.resetTCP(c.conn.handle) })
}

// change applies a change of the connection's socket to the stack, unless
// the connection is closed, and wakes the calls waiting on it.
func (c *TCPConn) change(apply func(s *stack)) error {
	err := c.conn.unlessClosed(func(e *engine) error {
		apply(e.stack)
		return nil
	})
	c.conn.shared.notify()
	return err
}

// WaitAcknowledged waits until deadline for the peer to acknowledge
// everything sent so far: the way to know it arrived before closing the
// link. The zero deadline waits for as long as the connection lives. It
// fails with ErrTimedOut for operation "TCP flush", ErrConnectionReset and
// ErrLinkDown — also for data that never left, dropped by [TCPConn.Abort]
// or the hang-up, or cut off on its way when PPP renegotiated a new
// address. Once everything written has been acknowledged, it returns nil,
// even after the connection was reset or the link went down.
func (c *TCPConn) WaitAcknowledged(deadline time.Time) error {
	return c.conn.wait(operationTCPFlush, fixedDeadline(deadline), func(e *engine) (bool, error) {
		handle := c.conn.handle
		switch {
		case e.stack.tcpUnacknowledged(handle) == 0 && !e.stack.wasCutOff(handle):
			return true, nil
		case e.stack.tcp(handle).IsOpen():
			return false, nil
		default:
			return true, e.connectionError()
		}
	})
}

// PeerAddr returns the remote end of the connection.
func (c *TCPConn) PeerAddr() netip.AddrPort {
	return c.conn.peer
}

// LocalAddr returns our end of the connection, a *net.TCPAddr: our address
// on the link and the local port.
func (c *TCPConn) LocalAddr() net.Addr {
	return net.TCPAddrFromAddrPort(c.conn.local)
}

// RemoteAddr returns the remote end of the connection, a *net.TCPAddr.
func (c *TCPConn) RemoteAddr() net.Addr {
	return net.TCPAddrFromAddrPort(c.conn.peer)
}

// SetDeadline sets the read and write deadlines; the zero time means none.
func (c *TCPConn) SetDeadline(deadline time.Time) error {
	return c.conn.setDeadlines(deadline, &c.conn.readDeadline, &c.conn.writeDeadline)
}

// SetReadDeadline sets the deadline for future reads and any read already
// waiting; the zero time means none.
func (c *TCPConn) SetReadDeadline(deadline time.Time) error {
	return c.conn.setDeadlines(deadline, &c.conn.readDeadline)
}

// SetWriteDeadline sets the deadline for future writes and any write
// already waiting; the zero time means none.
func (c *TCPConn) SetWriteDeadline(deadline time.Time) error {
	return c.conn.setDeadlines(deadline, &c.conn.writeDeadline)
}

// The primitives below serve the TLS record buffer (tlsTransport): moving
// bytes without waiting, and waiting without moving any.

// trySend queues what of p fits now, without waiting, and returns how much
// that was: 0 when the send buffer is full.
func (c *TCPConn) trySend(p []byte) (int, error) {
	queued := 0
	err := c.conn.unlessClosed(func(e *engine) error {
		var err error
		queued, err = sendNow(e, c.conn.handle, p)
		return err
	})
	return queued, err
}

// tryReceive reads what has arrived into p, without waiting. It returns
// how much it read, and finished once the peer has finished sending and
// everything before has been read; 0 without finished means nothing has
// arrived yet.
func (c *TCPConn) tryReceive(p []byte) (int, bool, error) {
	var (
		received int
		finished bool
	)
	err := c.conn.unlessClosed(func(e *engine) error {
		var err error
		received, finished, err = receiveNow(e, c.conn.handle, p)
		return err
	})
	return received, finished, err
}

// checkSendable returns nil while what is written now can still be sent:
// the link takes new data and our half of the connection is open.
func (c *TCPConn) checkSendable() error {
	return c.conn.unlessClosed(func(e *engine) error {
		if err := e.checkUsable(); err != nil {
			return err
		}
		if !e.stack.tcpMaySend(c.conn.handle) {
			return e.connectionError()
		}
		return nil
	})
}

// waitWritable waits until until for room to send, or for sending to end.
func (c *TCPConn) waitWritable(until deadline) error {
	return c.conn.wait(operationTCPWrite, until, func(e *engine) (bool, error) {
		return isWritable(e.stack, c.conn.handle), nil
	})
}

// waitReadable waits until until for something to read, for the stream to
// end, or for changed to hold.
func (c *TCPConn) waitReadable(until deadline, changed func() bool) error {
	return c.conn.wait(operationTCPRead, until, func(e *engine) (bool, error) {
		return isReadable(e.stack.tcp(c.conn.handle)) || changed(), nil
	})
}

// waitReadableOrWritable waits until until for something to read, room to
// send, either direction to end, or for changed to hold.
func (c *TCPConn) waitReadableOrWritable(until deadline, changed func() bool) error {
	return c.conn.wait(operationTCPRead, until, func(e *engine) (bool, error) {
		handle := c.conn.handle
		return isReadable(e.stack.tcp(handle)) || isWritable(e.stack, handle) || changed(), nil
	})
}

// announce makes a change that a wait's changed looks at, and wakes the
// waits: under the link's lock, so that none misses it.
func (c *TCPConn) announce(change func()) {
	c.conn.shared.update(func(*engine) { change() })
}

// readUntil and writeUntil are the connection's deadlines.
func (c *TCPConn) readUntil() time.Time  { return c.conn.readUntil() }
func (c *TCPConn) writeUntil() time.Time { return c.conn.writeUntil() }

// isReadable reports whether a read would not wait: data arrived, or the
// receive half ended.
func isReadable(socket *tcp.Socket) bool {
	return socket.CanReceive() || !socket.MayReceive()
}

// isWritable reports whether a write on the connection handle names would
// not wait: its send buffer has room, or sending ended.
func isWritable(s *stack, handle ipstack.SocketHandle) bool {
	return s.tcpCanSend(handle) || !s.tcpMaySend(handle)
}

// receiveNow is one attempt at reading into p: how much it read, and
// finished at the end of the stream; 0 without finished while nothing has
// arrived.
func receiveNow(e *engine, handle ipstack.SocketHandle, p []byte) (int, bool, error) {
	received, err := e.stack.tcp(handle).ReceiveSlice(p)
	switch {
	case errors.Is(err, tcp.ErrFinished):
		return 0, true, nil
	case err != nil:
		return 0, false, e.connectionError()
	}
	if received > 0 {
		e.recordActivity(time.Now())
	}
	return received, false, nil
}

// sendNow is one attempt at queueing data: how much of it was queued, 0
// while the send buffer is full.
func sendNow(e *engine, handle ipstack.SocketHandle, data []byte) (int, error) {
	// A closing link only delivers what was written before.
	if err := e.checkUsable(); err != nil {
		return 0, err
	}
	queued, err := e.stack.sendTCP(handle, data)
	if err != nil {
		return 0, e.connectionError()
	}
	if queued > 0 {
		e.recordActivity(time.Now())
	}
	return queued, nil
}
