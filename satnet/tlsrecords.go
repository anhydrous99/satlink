package satnet

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// The record buffer between crypto/tls and the TCP connection under a TLS
// session. crypto/tls alone cannot keep the semantics a timed-out write
// needs: it makes every write error permanent, so a write that timed out
// would break the session, and it takes in post-handshake messages — TLS 1.3
// session tickets — only while reading. So crypto/tls writes into this
// buffer, which never waits; the TLSConn's calls push the records on as
// TCP takes them, each within its own deadline; and a timed-out write
// leaves its records here, for the next call to send.

// sessionBufferLimit is the most records the session holds waiting for TCP.
// A full session makes a write wait rather than fail.
const sessionBufferLimit = 64 * 1024

// maxPlaintextLen is the most plaintext one TLS record carries (RFC 8446
// §5.1), and so the most a write hands crypto/tls at a time.
const maxPlaintextLen = 16 * 1024

// tlsTransport is what a TLS session needs from the TCP connection under it:
// a net.Conn's addresses, deadlines and Close, but not its Read and Write,
// which wait — the session moves bytes only with primitives that never do,
// and waits with primitives that move none; the end of sending, the reset
// and the wait for acknowledgement. *TCPConn has them; tests use a fake.
type tlsTransport interface {
	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	SetDeadline(deadline time.Time) error
	SetReadDeadline(deadline time.Time) error
	SetWriteDeadline(deadline time.Time) error
	CloseWrite() error
	Abort() error
	WaitAcknowledged(deadline time.Time) error
	PeerAddr() netip.AddrPort
	// trySend queues what of p fits now, and returns how much that was.
	trySend(p []byte) (int, error)
	// tryReceive reads what has arrived into p, and returns how much it
	// read, and finished once the peer has finished sending.
	tryReceive(p []byte) (int, bool, error)
	// checkSendable returns nil while what is written now can still be
	// sent.
	checkSendable() error
	// waitWritable waits for room to send, or for sending to end.
	waitWritable(until deadline) error
	// waitReadable waits for something to read, for the stream to end, or
	// for changed to hold.
	waitReadable(until deadline, changed func() bool) error
	// waitReadableOrWritable waits for either, or for changed to hold.
	waitReadableOrWritable(until deadline, changed func() bool) error
	// announce makes a change that a wait's changed looks at, and wakes
	// the waits.
	announce(change func())
	// readUntil and writeUntil are the connection's deadlines.
	readUntil() time.Time
	writeUntil() time.Time
}

// A TCP connection carries TLS sessions.
var _ tlsTransport = (*TCPConn)(nil)

// errNothingArrived is what the buffer's Read returns in non-blocking mode
// when TCP holds nothing more: a temporary net.Error, after which crypto/tls
// keeps the session, as it does after a read that timed out.
var errNothingArrived net.Error = nothingArrived{}

// nothingArrived is the type of errNothingArrived.
type nothingArrived struct{}

func (nothingArrived) Error() string   { return "no more TLS records have arrived" }
func (nothingArrived) Timeout() bool   { return true }
func (nothingArrived) Temporary() bool { return true }

// recordBuffer is the net.Conn crypto/tls runs over. Its Write keeps the
// records crypto/tls writes and sends what TCP takes at once, so it never
// waits and never fails but for a dead TCP connection; its Read sends the
// records still waiting before it reads. Its deadline setters do nothing:
// the TLSConn's deadlines are those of the TCP connection, which the
// buffer's waits use. It is safe for concurrent use.
type recordBuffer struct {
	transport tlsTransport
	mu        sync.Mutex
	// pending are the records TCP has not taken yet, oldest first; mu
	// guards them.
	pending bytes.Buffer
	// ended reports that the transport said its stream ended, which tells
	// a truncation from a close_notify.
	ended atomic.Bool
	// nonBlocking makes Read return errNothingArrived rather than wait.
	nonBlocking atomic.Bool
	// changes counts the changes that a Read waiting for TCP cannot see
	// there: records a write left unsent. Records another call takes in are
	// not counted, as the take-in is skipped while a Read waits.
	changes atomic.Uint64
}

// A record buffer is the connection crypto/tls writes to and reads from.
var _ net.Conn = (*recordBuffer)(nil)

// newRecordBuffer returns an empty buffer in front of transport.
func newRecordBuffer(transport tlsTransport) *recordBuffer {
	return &recordBuffer{transport: transport}
}

// Write keeps p, one or more records, and sends what TCP takes now.
func (b *recordBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.pending.Write(p)
	b.mu.Unlock()
	if _, err := b.push(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Read reads records for crypto/tls. It first sends the records still
// waiting — answers the session owes, such as a key update, and what a
// timed-out write left behind — and while some remain it waits for room to
// send them as well as for records to arrive, so that they keep leaving; a
// write that leaves records while it waits wakes it to send them too. Its
// errors are the transport's, unchanged: crypto/tls keeps the session after
// a read error only when err.(net.Error) reports Temporary, as a timeout
// does.
func (b *recordBuffer) Read(p []byte) (int, error) {
	for {
		seen := b.changes.Load()
		allSent := b.pushBeforeReading()
		received, finished, err := b.transport.tryReceive(p)
		switch {
		case err != nil:
			return 0, err
		case finished:
			b.ended.Store(true)
			return 0, io.EOF
		case received > 0:
			return received, nil
		case b.nonBlocking.Load():
			return 0, errNothingArrived
		}
		// While records are left, keep sending them.
		await := b.transport.waitReadableOrWritable
		if allSent {
			await = b.transport.waitReadable
		}
		if err := await(b.transport.readUntil, b.changedSince(seen)); err != nil {
			return 0, err
		}
	}
}

// pushBeforeReading sends what of the pending records TCP takes now, and
// reports whether none are left to wait for room. Records TCP no longer
// takes — our side has shut down, or the link is closing — count as gone:
// they must not keep a read from what is still to read, and the read
// reports a connection that has ended.
func (b *recordBuffer) pushBeforeReading() bool {
	allSent, err := b.push()
	return allSent || err != nil
}

// changedSince returns a check of whether the session changed since seen,
// the count of changes when a read last looked.
func (b *recordBuffer) changedSince(seen uint64) func() bool {
	return func() bool { return b.changes.Load() != seen }
}

// sessionChanged wakes a Read waiting for TCP: the session changed under
// it.
func (b *recordBuffer) sessionChanged() {
	b.transport.announce(func() { b.changes.Add(1) })
}

// Close sends the records still waiting — the close_notify among them —
// within the write deadline, and closes the transport gracefully: the
// records that did not fit are dropped.
func (b *recordBuffer) Close() error {
	err := b.pushUntil(b.transport.writeUntil)
	if closeErr := b.transport.Close(); err == nil {
		err = closeErr
	}
	return err
}

// push sends what of the pending records TCP takes now, and reports whether
// that was all of them.
func (b *recordBuffer) push() (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.pending.Len() > 0 {
		sent, err := b.transport.trySend(b.pending.Bytes())
		if err != nil {
			return false, err
		}
		if sent == 0 {
			return false, nil
		}
		b.pending.Next(sent)
	}
	return true, nil
}

// pushUntil sends every pending record, waiting until until for room.
func (b *recordBuffer) pushUntil(until deadline) error {
	for {
		allSent, err := b.push()
		if err != nil || allSent {
			return err
		}
		if err := b.transport.waitWritable(until); err != nil {
			// Records are left: a Read waiting meanwhile sends them on.
			b.sessionChanged()
			return err
		}
	}
}

// room returns how many more bytes of records the session may hold.
func (b *recordBuffer) room() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return sessionBufferLimit - b.pending.Len()
}

// LocalAddr returns the transport's local address.
func (b *recordBuffer) LocalAddr() net.Addr {
	return b.transport.LocalAddr()
}

// RemoteAddr returns the transport's remote address.
func (b *recordBuffer) RemoteAddr() net.Addr {
	return b.transport.RemoteAddr()
}

// SetDeadline does nothing, and nor do SetReadDeadline and
// SetWriteDeadline: crypto/tls calls them only to send its close_notify
// within five seconds, while a close_notify behind a full send buffer at
// 300 bytes per second may need far longer. It goes within the TLSConn's
// write deadline instead, none meaning forever.
func (b *recordBuffer) SetDeadline(time.Time) error { return nil }

// SetReadDeadline does nothing; see SetDeadline.
func (b *recordBuffer) SetReadDeadline(time.Time) error { return nil }

// SetWriteDeadline does nothing; see SetDeadline.
func (b *recordBuffer) SetWriteDeadline(time.Time) error { return nil }
