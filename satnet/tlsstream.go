package satnet

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// A TLS session over a TCP connection, driven so that every operation
// honours a deadline and a timeout never corrupts the session: crypto/tls
// writes into a record buffer (tlsrecords.go), and the calls below send
// its records on, each within its deadline.

// closedDuringHandshake is the reason a handshake fails when the server
// closes the connection.
const closedDuringHandshake = "the server closed the connection during the handshake"

// readAheadLimit is the most application data taken in ahead of the reader:
// as much as rustls holds of received plaintext.
const readAheadLimit = 16 * 1024

// TLSConn is a TLS connection through a [Link]. It is a net.Conn, and like
// one it is safe for concurrent use by multiple goroutines.
//
// Like a [TCPConn], reads and writes block until their deadline. Read
// returns io.EOF only once the server has ended the session properly; a
// connection closed without that fails with an [*Error] of kind
// ErrTLSTruncated, as what arrived may be cut short. A read or a write that
// timed out leaves the session usable; once the session has failed (ErrTLS),
// every later call reports why.
//
// A write returns once its data is encrypted and queued, like a TCP write:
// [TLSConn.WaitAcknowledged] tells when it has arrived. What a write took is
// sent even when that outlasts its deadline: encrypted records that TCP had
// no room for wait in the connection, and leave while a Read waits for the
// server, or with the next call on it — a Read, Write, WaitAcknowledged,
// CloseWrite or Close. The n a timed-out Write returns counts what the
// session took, so exactly the rest can be written again; unless the
// protocol tells a part from the whole, [TLSConn.Abort] keeps the server
// from taking what arrived for all of it. The link's own Close drops
// records still waiting, so call WaitAcknowledged first. WaitAcknowledged
// and CloseWrite also take in what the server has already sent, without
// waiting — its session tickets among it — so that a connection that never
// reads still lets the next one resume; the data in it is kept for the
// next Read.
//
// The deadlines are those set on the TLSConn, and they bound the TLS
// goodbye too: crypto/tls's own five-second limit for sending close_notify
// does not apply, so a goodbye queued behind a full send buffer at 300
// bytes per second still leaves within a longer write deadline, as in the
// Rust version.
//
// The zero TLSConn is not usable; [Link.DialTLS] and [TLSConnector.Secure]
// make one.
type TLSConn struct {
	transport tlsTransport
	records   *recordBuffer
	session   *tls.Conn
	// readMu is held by Read for the whole of crypto/tls's read, and tried
	// by the take-in of arrived records, which another reader makes
	// needless.
	readMu sync.Mutex
	// readAhead is application data taken in before the app asked for it;
	// readMu guards it.
	readAhead []byte
	// writeMu makes each Write go out whole, as a net.TCPConn's does.
	writeMu sync.Mutex
	// failure is the session's first ErrTLS.
	failure atomic.Pointer[Error]
	// goodbyeQueued reports that CloseWrite is queueing the close_notify:
	// the server ignores whatever follows it, so no write may be taken
	// after.
	goodbyeQueued atomic.Bool
	// closed reports that Close was called.
	closed atomic.Bool
}

var _ net.Conn = (*TLSConn)(nil)

// newTLSConn returns a client session over transport, with config, before
// its handshake.
func newTLSConn(transport tlsTransport, config *tls.Config) *TLSConn {
	records := newRecordBuffer(transport)
	return &TLSConn{transport: transport, records: records, session: tls.Client(records, config)}
}

// handshake runs the TLS handshake by deadline. The buffer's reads send
// each flight while they wait for the server's, and the client's last
// flight, its Finished among it, has left before the handshake counts as
// done.
func (c *TLSConn) handshake(deadline time.Time) error {
	if err := c.transport.SetDeadline(deadline); err != nil {
		return err
	}
	err := c.session.Handshake()
	if err == nil {
		err = c.records.pushUntil(c.transport.writeUntil)
	}
	// The deadlines were the handshake's: the connection starts with none.
	if clearErr := c.transport.SetDeadline(time.Time{}); err == nil {
		err = clearErr
	}
	if err != nil {
		return c.handshakeFailure(err)
	}
	return nil
}

// handshakeFailure is the error of a handshake that failed with err: the
// transport's own, ErrTLS saying that the server went away, or ErrTLS with
// crypto/tls's error.
func (c *TLSConn) handshakeFailure(err error) error {
	if transportErr, ok := transportError(err); ok {
		return transportErr
	}
	if c.records.ended.Load() && isEndOfStream(err) {
		return &Error{Kind: ErrTLS, Message: closedDuringHandshake}
	}
	return &Error{Kind: ErrTLS, Err: err}
}

// Read reads decrypted data into p, waiting until the read deadline for at
// least one byte. Data already decrypted is returned even when the deadline
// has passed. It returns io.EOF after the server's close_notify, and fails
// with ErrTLSTruncated when the server closed the connection without one,
// ErrTLS when the session fails, and the TCP connection's errors.
func (c *TLSConn) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(c.readAhead) > 0 {
		return c.readTakenIn(p), nil
	}
	if failure := c.failure.Load(); failure != nil {
		return 0, failure
	}
	read, err := c.session.Read(p)
	if read > 0 {
		return read, nil
	}
	return 0, c.readFailure(err)
}

// readTakenIn moves application data taken in ahead of the reader into p.
func (c *TLSConn) readTakenIn(p []byte) int {
	read := copy(p, c.readAhead)
	c.readAhead = c.readAhead[read:]
	if len(c.readAhead) == 0 {
		c.readAhead = nil
	}
	return read
}

// readFailure is what Read reports for crypto/tls's err: io.EOF after the
// server's close_notify, which crypto/tls reports before TCP's end is ever
// read; ErrTLSTruncated once the connection ended without one, mid-record
// or not; the transport's own errors; and otherwise ErrTLS, the session's
// failure.
func (c *TLSConn) readFailure(err error) error {
	if errors.Is(err, io.EOF) && !c.records.ended.Load() {
		return io.EOF
	}
	if isEndOfStream(err) {
		return &Error{Kind: ErrTLSTruncated}
	}
	if transportErr, ok := transportError(err); ok {
		return c.closedOr(transportErr)
	}
	return c.fail(err)
}

// Write encrypts all of p and queues it, waiting until the write deadline
// for room: in the session, which holds at most 64 KiB of records waiting
// for TCP, and then in TCP for those records. A full session makes Write
// wait rather than fail. It fails with ErrTimedOut for operation "TCP
// write" when the deadline passes first; n then counts the bytes of p the
// session took, perhaps all of them, which are still sent. What could never
// reach the server is refused rather than taken: once CloseWrite has been
// called, Write fails with ErrConnectionReset, as TCP refuses a write after
// its own shutdown, and once the TCP connection or the link no longer
// sends, with its error; nothing of p is taken then, and no records are
// left behind to fail every later WaitAcknowledged. [TLSConn.WriteSome]
// returns once it has taken what the session has room for.
func (c *TLSConn) Write(p []byte) (int, error) {
	if err := c.usable(); err != nil {
		return 0, err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	taken, err := c.encryptAll(p)
	if err != nil {
		return taken, err
	}
	if err := c.records.pushUntil(c.transport.writeUntil); err != nil {
		return taken, c.closedOr(err)
	}
	return taken, nil
}

// WriteSome encrypts and queues what the session has room for of p,
// waiting until the write deadline for room, and returns how much of p it
// took: perhaps not all of it, which is still sent, so exactly the rest can
// be written again. Unlike Write, it takes no more once that much is taken,
// as the Rust version's send_until does: ErrTimedOut means that nothing was
// taken, and with another error the count says what was. What it took but
// could not hand to TCP by the deadline stays in the session for the
// connection's next calls to send, as for Write; the link's own Close drops
// it, so call WaitAcknowledged first. It is refused as Write is, taking
// nothing; an empty p only sends the records still waiting.
func (c *TLSConn) WriteSome(p []byte) (int, error) {
	if err := c.usable(); err != nil {
		return 0, err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	taken, err := c.encryptWhatFits(p)
	if err != nil {
		return taken, err
	}
	// What TCP had no room for by the deadline leaves with a later call.
	if err := c.records.pushUntil(c.transport.writeUntil); err != nil && !errors.Is(err, ErrTimedOut) {
		return taken, c.closedOr(err)
	}
	return taken, nil
}

// encryptAll hands all of p to the session, which turns it into records,
// and returns how much it took. While the session is full, it sends
// records until the write deadline, as room comes as they leave.
func (c *TLSConn) encryptAll(p []byte) (int, error) {
	taken := 0
	for taken < len(p) {
		room, err := c.awaitRoom()
		if err != nil {
			return taken, err
		}
		written, err := c.encryptChunk(p[taken:], room)
		taken += written
		if err != nil {
			return taken, err
		}
	}
	return taken, nil
}

// encryptWhatFits hands the session as much of p as it has room for and
// returns how much it took: at least a byte unless p is empty, as while the
// session is full it first sends records until the write deadline.
func (c *TLSConn) encryptWhatFits(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	room, err := c.awaitRoom()
	if err != nil {
		return 0, err
	}
	taken := 0
	for taken < len(p) && room > 0 {
		written, err := c.encryptChunk(p[taken:], room)
		taken += written
		if err != nil {
			return taken, err
		}
		room = c.records.room()
	}
	return taken, nil
}

// awaitRoom returns how many more bytes of records the session may hold,
// first sending records until the write deadline while it may hold none:
// room comes as they leave.
func (c *TLSConn) awaitRoom() (int, error) {
	for {
		if room := c.records.room(); room > 0 {
			return room, nil
		}
		if err := c.records.pushUntil(c.transport.writeUntil); err != nil {
			return 0, c.closedOr(err)
		}
	}
}

// encryptChunk hands the session the start of p — at most a record's
// worth, and no more than room, the bytes of records the session may still
// hold — unless nothing more may be encrypted, and returns how much it
// took.
func (c *TLSConn) encryptChunk(p []byte, room int) (int, error) {
	if err := c.checkWritable(); err != nil {
		return 0, err
	}
	written, err := c.session.Write(p[:min(len(p), maxPlaintextLen, room)])
	if err != nil {
		return written, c.writeFailure(err)
	}
	return written, nil
}

// checkWritable returns why nothing more may be encrypted, if anything: the
// close_notify queued, after which the server ignores whatever comes, or a
// TCP connection or link that no longer sends, so that the records could
// never reach the server. (A failed session needs no check: Write checks
// first, and crypto/tls refuses to write once the session has failed.)
func (c *TLSConn) checkWritable() error {
	if c.goodbyeQueued.Load() {
		return writeAfterGoodbye()
	}
	return c.closedOr(c.transport.checkSendable())
}

// writeAfterGoodbye is the error of a write after CloseWrite, as TCP
// reports a write after its own shutdown.
func writeAfterGoodbye() error {
	return &Error{Kind: ErrConnectionReset}
}

// writeFailure is what Write and CloseWrite report for crypto/tls's err:
// the transport's own errors; for a write crypto/tls refused, why it did —
// the session's failure, or ErrConnectionReset once the close_notify was
// queued — as a Read or CloseWrite in another goroutine may have got there
// first; or ErrTLS with crypto/tls's error. The session has not failed for
// it: crypto/tls's writes fail only for what this side did.
func (c *TLSConn) writeFailure(err error) error {
	if transportErr, ok := transportError(err); ok {
		return c.closedOr(transportErr)
	}
	if failure := c.failure.Load(); failure != nil {
		return failure
	}
	if c.goodbyeQueued.Load() {
		return writeAfterGoodbye()
	}
	return &Error{Kind: ErrTLS, Err: err}
}

// Close ends the session with a close_notify, after any records still
// waiting, sent within the write deadline (with none, it waits for room as
// long as the link is up), and closes the TCP connection gracefully. While
// another goroutine is inside crypto/tls's Write, crypto/tls skips the
// close_notify; the records already waiting still go within the write
// deadline before the TCP connection closes. The Read, Write and
// WaitAcknowledged calls waiting in other goroutines wake when the TCP
// connection closes — once the close_notify has gone, or the write deadline
// has passed — and return net.ErrClosed; with a write deadline of now,
// Close sends what TCP has room for at once and closes without waiting.
// After Close, Read, Write, CloseWrite, WaitAcknowledged and the deadline
// setters fail with net.ErrClosed.
//
// Close returns the session's ErrTLS once it has failed, found among what
// arrived if need be, closing the TCP connection without a close_notify;
// ErrTimedOut when the close_notify could not leave by the write deadline,
// which it is then dropped for; the TCP connection's error when it takes
// no close_notify at all — ErrConnectionReset once it was reset, by
// [TLSConn.Abort] or the server, and ErrLinkDown once the link is closing;
// and nil otherwise, as for a second Close. The TCP connection is closed
// whatever Close returns.
func (c *TLSConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	// As CloseWrite does, take in what arrived — session tickets for the
	// next connection — and, as in Rust, send no close_notify for a session
	// that failed.
	if err := c.takeInArrivedRecords(); err != nil {
		closeAbandoned(c.transport)
		return err
	}
	return c.closeFailure(c.session.Close())
}

// closeFailure is what Close reports for crypto/tls's err: nothing, or what
// kept the close_notify from leaving in time, which was then dropped.
func (c *TLSConn) closeFailure(err error) error {
	if err == nil {
		return nil
	}
	if transportErr, ok := transportError(err); ok {
		return transportErr
	}
	return &Error{Kind: ErrTLS, Err: err}
}

// CloseWrite ends our side of the session: a close_notify, after any
// records still waiting, sent within the write deadline, then the end of
// our half of the TCP connection. What the server still sends can be read.
// Writes fail from the moment it is called, as the server would ignore
// them; after a timeout, calling it again finishes the shutdown.
func (c *TLSConn) CloseWrite() error {
	if err := c.usable(); err != nil {
		return err
	}
	if err := c.takeInArrivedRecords(); err != nil {
		return c.closedOr(err)
	}
	// Before the close_notify: a write goes before it, or not at all.
	c.goodbyeQueued.Store(true)
	if err := c.session.CloseWrite(); err != nil {
		return c.writeFailure(err)
	}
	if err := c.records.pushUntil(c.transport.writeUntil); err != nil {
		return c.closedOr(err)
	}
	return c.closedOr(c.transport.CloseWrite())
}

// Abort ends the connection at once with a TCP reset, dropping what is
// still queued, records waiting in the session included: the server must
// not take what it got for the whole stream. The calls waiting on the
// connection wake and fail with ErrConnectionReset, and so do later calls,
// but for what is already settled: Read still returns what had arrived,
// and WaitAcknowledged returns nil when everything written had been
// acknowledged. CloseWrite fails too, as no close_notify can go; Close
// still lets the connection go. After Close, Abort fails with
// net.ErrClosed.
func (c *TLSConn) Abort() error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	return c.closedOr(c.transport.Abort())
}

// WaitAcknowledged sends the records still waiting, then waits until
// deadline for the server to acknowledge everything sent so far: the way to
// know it arrived before closing the link. The zero deadline waits for as
// long as the connection lives.
func (c *TLSConn) WaitAcknowledged(deadline time.Time) error {
	if err := c.usable(); err != nil {
		return err
	}
	if err := c.records.pushUntil(fixedDeadline(deadline)); err != nil {
		return c.closedOr(err)
	}
	if err := c.transport.WaitAcknowledged(deadline); err != nil {
		return c.closedOr(err)
	}
	// The server's session tickets are here by now: taking them in lets the
	// next connection resume, even after one that is never read.
	return c.closedOr(c.takeInArrivedRecords())
}

// takeInArrivedRecords takes in the records TCP already holds, without
// waiting — their application data is kept for the next Read — and reports
// the session's failure, if it has failed. It is skipped while another
// goroutine reads: crypto/tls holds its input lock for the whole of a
// blocking read, and that reader takes the records in anyway.
func (c *TLSConn) takeInArrivedRecords() error {
	if failure := c.failure.Load(); failure != nil {
		return failure
	}
	if !c.readMu.TryLock() {
		return nil
	}
	defer c.readMu.Unlock()
	c.records.nonBlocking.Store(true)
	defer c.records.nonBlocking.Store(false)
	scratch := make([]byte, maxPlaintextLen)
	for len(c.readAhead) < readAheadLimit {
		read, err := c.session.Read(scratch)
		c.readAhead = append(c.readAhead, scratch[:read]...)
		if err != nil {
			return c.takeInFailure(err)
		}
	}
	return nil
}

// takeInFailure is what the take-in reports for crypto/tls's err: the
// session's failure, and nothing for the rest — nothing more arrived, the
// TCP connection failed, or the stream ended, with the server's goodbye or
// without — which a Read or a Write reports, not this.
func (c *TLSConn) takeInFailure(err error) error {
	if _, fromTransport := transportError(err); fromTransport || errors.Is(err, errNothingArrived) || isEndOfStream(err) {
		return nil
	}
	return c.fail(err)
}

// Resumed reports whether the handshake resumed an earlier session, which
// costs a fraction of a full handshake's airtime.
func (c *TLSConn) Resumed() bool {
	return c.session.ConnectionState().DidResume
}

// ALPNProtocol returns the protocol the server picked through ALPN, and ""
// when it picked none.
func (c *TLSConn) ALPNProtocol() string {
	return c.session.ConnectionState().NegotiatedProtocol
}

// PeerAddr returns the remote end of the connection.
func (c *TLSConn) PeerAddr() netip.AddrPort {
	return c.transport.PeerAddr()
}

// LocalAddr returns our end of the connection, a *net.TCPAddr.
func (c *TLSConn) LocalAddr() net.Addr {
	return c.transport.LocalAddr()
}

// RemoteAddr returns the remote end of the connection, a *net.TCPAddr.
func (c *TLSConn) RemoteAddr() net.Addr {
	return c.transport.RemoteAddr()
}

// SetDeadline sets the read and write deadlines, those of the TCP
// connection underneath; the zero time means none.
func (c *TLSConn) SetDeadline(deadline time.Time) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	return c.transport.SetDeadline(deadline)
}

// SetReadDeadline sets the deadline for future reads and any read already
// waiting; the zero time means none.
func (c *TLSConn) SetReadDeadline(deadline time.Time) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	return c.transport.SetReadDeadline(deadline)
}

// SetWriteDeadline sets the deadline for future writes and any write
// already waiting; the zero time means none.
func (c *TLSConn) SetWriteDeadline(deadline time.Time) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	return c.transport.SetWriteDeadline(deadline)
}

// usable returns net.ErrClosed once the connection is closed, and the
// session's failure once it has failed.
func (c *TLSConn) usable() error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	if failure := c.failure.Load(); failure != nil {
		return failure
	}
	return nil
}

// fail records crypto/tls's err as the session's failure, unless one was
// recorded first, and returns the failure: once the session has failed,
// every call reports why.
func (c *TLSConn) fail(err error) error {
	c.failure.CompareAndSwap(nil, &Error{Kind: ErrTLS, Err: err})
	return c.failure.Load()
}

// closedOr returns net.ErrClosed once the connection is closed, as a call
// that Close woke reports, and err otherwise.
func (c *TLSConn) closedOr(err error) error {
	if err != nil && c.closed.Load() {
		return net.ErrClosed
	}
	return err
}

// transportError returns the transport's own error inside err, which
// crypto/tls hands back as it got it or inside its permanent error, and
// false when err is crypto/tls's.
func transportError(err error) (error, bool) {
	var linkErr *Error
	if errors.As(err, &linkErr) {
		return linkErr, true
	}
	if errors.Is(err, net.ErrClosed) {
		return net.ErrClosed, true
	}
	return nil, false
}

// isEndOfStream reports whether err says the stream ended: at a record
// boundary (io.EOF) or inside a record (io.ErrUnexpectedEOF).
func isEndOfStream(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
