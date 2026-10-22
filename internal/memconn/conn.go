package memconn

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

// Conn is an in-memory connection with a net.Conn side, for crypto/tls, and
// a pump side, for the code that moves its bytes (see the package
// documentation). Bytes cross it in order and without loss, in both
// directions. Every method is safe for concurrent use. The zero Conn is not
// usable; create one with [New].
type Conn struct {
	local, remote netip.AddrPort

	// mu guards everything below.
	mu sync.Mutex

	// inputChanged wakes the waiting Reads: bytes or the FIN injected, the
	// connection closed, the read deadline moved. readerChanged wakes
	// AwaitReader: a Read started waiting, or the connection closed. Two
	// signals, so that Reads waiting side by side do not wake each other.
	inputChanged, readerChanged signal

	// inbound holds what the owner injected and Read has yet to return;
	// outbound what Write queued and the owner has yet to extract.
	inbound, outbound bytes.Buffer

	inboundClosed bool
	eofDelivered  bool
	closed        bool

	// readDeadline and writeDeadline are the zero time when there is none.
	readDeadline, writeDeadline time.Time

	// waitingReaders counts the Reads blocked for want of bytes, which is
	// how AwaitReader knows that crypto/tls has dealt with what it was
	// given.
	waitingReaders int
}

// A Conn is what crypto/tls reads from and writes to.
var _ net.Conn = (*Conn)(nil)

// New returns an open connection between local, this side's address, and
// remote, the peer's: the TCP endpoints it stands in for, which LocalAddr
// and RemoteAddr report.
func New(local, remote netip.AddrPort) *Conn {
	return &Conn{
		local:         local,
		remote:        remote,
		inputChanged:  newSignal(),
		readerChanged: newSignal(),
	}
}

// Read reads bytes the owner injected, waiting for at least one. Once
// [Conn.CloseInbound] was called and everything injected before it has been
// read, Read returns io.EOF, and [Conn.EOFDelivered] reports true from then
// on. Read fails with os.ErrDeadlineExceeded — a net.Error whose Timeout
// and Temporary methods report true, so crypto/tls keeps its session — when
// the read deadline passes first, and with net.ErrClosed after Close, which
// also wakes a Read that is waiting. An empty p returns at once.
func (c *Conn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		switch {
		case c.closed:
			return 0, net.ErrClosed
		case len(p) == 0:
			return 0, nil
		case c.inbound.Len() > 0:
			// Bytes already here are returned even past the deadline: they
			// arrived before it.
			return c.inbound.Read(p)
		case c.inboundClosed:
			c.eofDelivered = true
			return 0, io.EOF
		case hasPassed(c.readDeadline):
			return 0, os.ErrDeadlineExceeded
		}
		c.waitForInputLocked()
	}
}

// Write queues all of p for the owner to extract, without waiting: the
// owner bounds what crypto/tls writes by what it injects. Write fails with
// net.ErrClosed after Close, and with os.ErrDeadlineExceeded once the write
// deadline has passed, writing nothing either way.
func (c *Conn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	if hasPassed(c.writeDeadline) {
		return 0, os.ErrDeadlineExceeded
	}
	return c.outbound.Write(p)
}

// Close closes the connection: a Read that is waiting returns
// net.ErrClosed, and so do later Reads and Writes. What crypto/tls wrote
// before stays for the owner to extract, so that its last records — a
// close_notify, an alert — still reach the peer; [Conn.Inject] takes
// nothing more. Close is idempotent and returns nil.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.inputChanged.fire()
		c.readerChanged.fire()
	}
	return nil
}

// LocalAddr returns this side's address, as given to [New], as a
// *net.TCPAddr.
func (c *Conn) LocalAddr() net.Addr {
	return net.TCPAddrFromAddrPort(c.local)
}

// RemoteAddr returns the peer's address, as given to [New], as a
// *net.TCPAddr.
func (c *Conn) RemoteAddr() net.Addr {
	return net.TCPAddrFromAddrPort(c.remote)
}

// SetDeadline sets the read and write deadlines, as SetReadDeadline and
// SetWriteDeadline do.
func (c *Conn) SetDeadline(deadline time.Time) error {
	if err := c.SetReadDeadline(deadline); err != nil {
		return err
	}
	return c.SetWriteDeadline(deadline)
}

// SetReadDeadline sets the deadline for future Reads and for a Read already
// waiting, which looks at it again; the zero time means none. After Close
// it fails with net.ErrClosed.
func (c *Conn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.readDeadline = deadline
	c.inputChanged.fire()
	return nil
}

// SetWriteDeadline sets the deadline for future Writes; the zero time means
// none. After Close it fails with net.ErrClosed.
func (c *Conn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.writeDeadline = deadline
	return nil
}

// Inject hands p, bytes from the peer, to Read, and returns how many it
// took: all of p, or none once the connection or its inbound side is
// closed ([Conn.Close], [Conn.CloseInbound]). It never waits, and keeps no
// reference to p.
func (c *Conn) Inject(p []byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.inboundClosed {
		return 0
	}
	taken, _ := c.inbound.Write(p) // bytes.Buffer.Write takes everything or panics, never fails.
	c.inputChanged.fire()
	return taken
}

// CloseInbound records the peer's FIN: once the bytes injected before it
// have been read, Read returns io.EOF. Later calls change nothing.
func (c *Conn) CloseInbound() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inboundClosed {
		c.inboundClosed = true
		c.inputChanged.fire()
	}
}

// Extract moves the oldest bytes crypto/tls wrote into p and returns how
// many: at most len(p), and none when nothing is waiting. It never waits,
// and still works after Close.
func (c *Conn) Extract(p []byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	extracted := copy(p, c.outbound.Bytes())
	c.outbound.Next(extracted)
	return extracted
}

// Pending returns how many written bytes wait to be extracted. The
// simulator's TLS echo injects nothing more while some do, so that a client
// that sends without reading stalls, as with the plain echo service.
func (c *Conn) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outbound.Len()
}

// AwaitReader waits until crypto/tls has dealt with everything injected —
// a Read is waiting with nothing left to read, or the connection is closed
// — and reports whether that happened within timeout. It returns at once
// when it already has. The owner calls it between injecting and
// extracting, so that the answer to what it injected is there to extract
// in the same step, as with the Rust version's sans-I/O sessions. A session
// that stops reading without closing, such as a client that has read the
// peer's close_notify, makes it wait for all of timeout: call it only while
// the session reads.
func (c *Conn) AwaitReader(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	c.mu.Lock()
	defer c.mu.Unlock()
	for !c.readerIsIdleLocked() {
		if hasPassed(deadline) {
			return false
		}
		c.waitLocked(c.readerChanged.channel(), deadline)
	}
	return true
}

// EOFDelivered reports whether Read has returned io.EOF, the peer's FIN.
// crypto/tls returns io.EOF both for the peer's close_notify and for a FIN
// at a record boundary, and only this tells them apart: io.EOF from
// crypto/tls while EOFDelivered reports false was a close_notify.
func (c *Conn) EOFDelivered() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eofDelivered
}

// Closed reports whether Close has been called: the session is over, and
// once [Conn.Pending] reports 0 the owner can close its socket too.
func (c *Conn) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// readerIsIdleLocked reports whether crypto/tls has nothing left to read:
// a Read waits with nothing buffered, or the connection is closed. A FIN
// not yet delivered is still something to read, so after CloseInbound only
// the end of the session satisfies it: a Read then returns io.EOF instead
// of waiting.
func (c *Conn) readerIsIdleLocked() bool {
	if c.closed {
		return true
	}
	return c.waitingReaders > 0 && c.inbound.Len() == 0 && !c.inboundClosed
}

// waitForInputLocked waits, as a Read, until the input changes or the read
// deadline passes. It counts the Read as waiting meanwhile, and tells
// AwaitReader so.
func (c *Conn) waitForInputLocked() {
	c.waitingReaders++
	c.readerChanged.fire()
	c.waitLocked(c.inputChanged.channel(), c.readDeadline)
	c.waitingReaders--
}

// waitLocked releases the lock until changed fires or deadline passes (the
// zero time: never), then takes it again.
func (c *Conn) waitLocked(changed <-chan struct{}, deadline time.Time) {
	c.mu.Unlock()
	defer c.mu.Lock()
	if deadline.IsZero() {
		<-changed
		return
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-changed:
	case <-timer.C:
	}
}

// hasPassed reports whether deadline is set and no longer in the future.
func hasPassed(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}
