package mobile

import (
	"errors"
	"io"
	"time"

	"github.com/anhydrous99/satlink/satnet"
)

// Connections over the satellite link. Receive returns at most 64 KiB, so
// that a large maxLen cannot make the library allocate without bound. A
// stream's maxLen must be at least 1; a datagram's may be 0, as in the
// Rust bindings. A timeout of zero takes only what has already arrived.
//
// gomobile hands an empty byte array to Java as null and to Swift as nil —
// where a nil result without an error even throws — so the end of a stream
// is a flag of the result rather than an empty one.

// maxReceiveLen is the most bytes one Receive returns.
const maxReceiveLen = 64 * 1024

// Received is what a stream connection's Receive returns: data, or the end
// of the stream.
type Received struct {
	// Data holds at least one byte, unless Finished is set: then it is
	// empty, which reaches Java as null and Swift as nil.
	Data []byte
	// Finished reports the end of the stream: the peer has finished
	// sending (TCP), or the server has ended the TLS session (TLS).
	Finished bool
}

// Datagram is a datagram that UDPConnection.Receive returns.
type Datagram struct {
	// Data is the datagram, truncated to the maxLen asked for. A datagram
	// may be empty: Data then reaches Java as null and Swift as nil.
	Data []byte
}

// TCPConnection is a TCP connection over the satellite link. It is safe for
// use from several threads: one thread may receive while another sends.
// Calls in one direction share one deadline, though, which each sets to its
// own timeoutMillis as it starts: while two receives, or two sends, wait at
// once, the timeout of the one that started last applies to both, cutting
// the other's wait short or drawing it out. So receive from one thread at a
// time, and send from one thread at a time.
type TCPConnection struct {
	// conn is the connection itself; nil in a zero value.
	conn *satnet.TCPConn
}

// Send queues all of data within timeoutMillis; WaitAcknowledged tells when
// it has arrived. A SatlinkErrorTimeout does not say how much of data was
// queued — perhaps none, perhaps some — and what was is still sent, so the
// peer may get only the start of data: unless your protocol can tell, Abort
// the connection, or send with SendSome instead.
func (c *TCPConnection) Send(data []byte, timeoutMillis int64) (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TCPConnection")
	if err != nil {
		return err
	}
	return send(conn, data, timeoutMillis)
}

// SendSome queues what fits of data, waiting up to timeoutMillis for room,
// and returns how many bytes it queued, which are sent: perhaps not all of
// data. SatlinkErrorTimeout means that none were: send the rest of data
// again with SendSome.
func (c *TCPConnection) SendSome(data []byte, timeoutMillis int64) (queued int64, err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TCPConnection")
	if err != nil {
		return 0, err
	}
	return sendSome(conn, data, timeoutMillis)
}

// Receive returns up to maxLen bytes (at most 64 KiB), waiting up to
// timeoutMillis for the first, or a Received with Finished set once the
// peer has finished sending.
func (c *TCPConnection) Receive(maxLen int, timeoutMillis int64) (received *Received, err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TCPConnection")
	if err != nil {
		return nil, err
	}
	return receiveStream(conn, maxLen, timeoutMillis)
}

// WaitAcknowledged waits up to timeoutMillis until the peer has
// acknowledged everything sent. It fails with SatlinkErrorConnectionReset
// or SatlinkErrorLinkDown for data that Abort or the hang-up dropped
// before it left, and succeeds once everything sent has been
// acknowledged, even after the connection ended.
func (c *TCPConnection) WaitAcknowledged(timeoutMillis int64) (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TCPConnection")
	if err != nil {
		return err
	}
	return fromError(conn.WaitAcknowledged(deadlineAfterMillis(timeoutMillis)))
}

// ShutdownWrite finishes sending; receiving still works.
func (c *TCPConnection) ShutdownWrite() (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TCPConnection")
	if err != nil {
		return err
	}
	return fromError(conn.CloseWrite())
}

// Abort ends the connection at once with a reset, dropping what is still
// queued: the peer cannot take what it got for the whole stream. Calls
// waiting on the connection wake and fail with SatlinkErrorConnectionReset,
// and so do later ones, but for what is already settled: Receive still
// returns what had arrived, and WaitAcknowledged succeeds when everything
// sent had been acknowledged. ShutdownWrite has nothing left to end, and
// succeeds. Close still releases the connection.
func (c *TCPConnection) Abort() (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TCPConnection")
	if err != nil {
		return err
	}
	return fromError(conn.Abort())
}

// PeerAddress returns the remote end, such as "192.0.2.7:7".
func (c *TCPConnection) PeerAddress() (address string) {
	defer survivePanic("TCPConnection.PeerAddress")
	if c.conn == nil {
		// A zero TCPConnection, which has no peer.
		return ""
	}
	return c.conn.PeerAddr().String()
}

// Close releases the connection without waiting: it closes gracefully, and
// what was sent is still delivered while the link stays up. Close is
// idempotent.
func (c *TCPConnection) Close() (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TCPConnection")
	if err != nil {
		return err
	}
	return fromError(conn.Close())
}

// TLSConnection is a TLS connection over the satellite link. It is safe for
// use from several threads: one thread may receive while another sends. As
// for TCPConnection, calls in one direction — the receives; the sends and
// ShutdownWrite — share one deadline: receive from one thread at a time,
// and send from one thread at a time.
type TLSConnection struct {
	// conn is the connection itself; nil in a zero value.
	conn *satnet.TLSConn
}

// Send encrypts and queues all of data within timeoutMillis;
// WaitAcknowledged tells when it has arrived. A SatlinkErrorTimeout does
// not say how much of data was taken — none, some or all — and what was is
// still sent (WaitAcknowledged before SatelliteLink.HangUp makes sure it
// is), so the server may get only the start of data: unless your protocol
// can tell, Abort the connection, or send with SendSome instead.
func (c *TLSConnection) Send(data []byte, timeoutMillis int64) (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TLSConnection")
	if err != nil {
		return err
	}
	return send(conn, data, timeoutMillis)
}

// SendSome encrypts and queues what fits of data, waiting up to
// timeoutMillis for room, and returns how many bytes it took: perhaps not
// all of data. SatlinkErrorTimeout means that none were: send the rest of
// data again with SendSome. What was taken may still wait in the session
// for a later call on this connection to send it: WaitAcknowledged before
// SatelliteLink.HangUp does, as closing the link or this connection drops
// it.
func (c *TLSConnection) SendSome(data []byte, timeoutMillis int64) (taken int64, err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TLSConnection")
	if err != nil {
		return 0, err
	}
	return sendSome(conn, data, timeoutMillis)
}

// Receive returns up to maxLen decrypted bytes (at most 64 KiB), waiting up
// to timeoutMillis, or a Received with Finished set once the server has
// ended the session. A connection closed without that fails with
// SatlinkErrorTLSTruncated.
func (c *TLSConnection) Receive(maxLen int, timeoutMillis int64) (received *Received, err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TLSConnection")
	if err != nil {
		return nil, err
	}
	return receiveStream(conn, maxLen, timeoutMillis)
}

// WaitAcknowledged waits up to timeoutMillis until the server has
// acknowledged everything sent. It fails with SatlinkErrorConnectionReset
// or SatlinkErrorLinkDown for data that Abort or the hang-up dropped
// before it left, and succeeds once everything sent has been
// acknowledged, even after the connection ended.
func (c *TLSConnection) WaitAcknowledged(timeoutMillis int64) (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TLSConnection")
	if err != nil {
		return err
	}
	return fromError(conn.WaitAcknowledged(deadlineAfterMillis(timeoutMillis)))
}

// ShutdownWrite sends the TLS goodbye (close_notify) and finishes sending,
// waiting up to timeoutMillis for room: the end of our half of the
// connection. What the server still sends can be received. Sending fails
// from then on, even after a SatlinkErrorTimeout: call ShutdownWrite again
// to finish. Close only releases this object.
func (c *TLSConnection) ShutdownWrite(timeoutMillis int64) (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TLSConnection")
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(deadlineAfterMillis(timeoutMillis)); err != nil {
		return fromError(err)
	}
	return fromError(conn.CloseWrite())
}

// Abort ends the connection at once with a TCP reset, dropping what is
// still queued, records waiting in the session included: the server cannot
// take what it got for the whole stream. Calls waiting on the connection
// wake and fail with SatlinkErrorConnectionReset, and so do later ones, but
// for what is already settled: Receive still returns what had arrived, and
// WaitAcknowledged succeeds when everything sent had been acknowledged.
// ShutdownWrite fails too, as no close_notify can go. Close still releases
// the connection.
func (c *TLSConnection) Abort() (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TLSConnection")
	if err != nil {
		return err
	}
	return fromError(conn.Abort())
}

// Close releases the connection without waiting: a close_notify if the
// send buffer has room for it at once, then the graceful close of the TCP
// connection, as for TCPConnection. Close is idempotent.
func (c *TLSConnection) Close() (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "TLSConnection")
	if err != nil {
		return err
	}
	// Releasing must not wait, as the Rust bindings' destroy did not: with
	// a write deadline of now, what does not fit at once is dropped, and a
	// connection already closed refuses the deadline, which changes nothing.
	_ = conn.SetWriteDeadline(time.Now())
	// A close_notify that did not fit is expected — the Rust bindings sent
	// none — and the TCP connection closes gracefully either way, as it
	// does after a link that has gone down.
	_ = conn.Close()
	return nil
}

// Resumed reports whether the handshake resumed an earlier session, saving
// airtime.
func (c *TLSConnection) Resumed() (resumed bool) {
	defer survivePanic("TLSConnection.Resumed")
	return c.conn != nil && c.conn.Resumed()
}

// ALPNProtocol returns the protocol the server chose through ALPN; "" when
// none.
func (c *TLSConnection) ALPNProtocol() (protocol string) {
	defer survivePanic("TLSConnection.ALPNProtocol")
	if c.conn == nil {
		// A zero TLSConnection, which has no session.
		return ""
	}
	return c.conn.ALPNProtocol()
}

// PeerAddress returns the remote end, such as "192.0.2.43:443".
func (c *TLSConnection) PeerAddress() (address string) {
	defer survivePanic("TLSConnection.PeerAddress")
	if c.conn == nil {
		// A zero TLSConnection, which has no peer.
		return ""
	}
	return c.conn.PeerAddr().String()
}

// UDPConnection exchanges datagrams with one allowed peer. It is safe for
// use from several threads: one thread may receive while another sends. As
// for TCPConnection, calls in one direction share one deadline: receive
// from one thread at a time, and send from one thread at a time.
type UDPConnection struct {
	// conn is the socket itself; nil in a zero value.
	conn *satnet.UDPConn
}

// Send sends one datagram, waiting up to timeoutMillis for buffer space.
func (c *UDPConnection) Send(datagram []byte, timeoutMillis int64) (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "UDPConnection")
	if err != nil {
		return err
	}
	return send(conn, datagram, timeoutMillis)
}

// Receive returns the next datagram from the peer, truncated to maxLen (at
// most 64 KiB), waiting up to timeoutMillis. With maxLen 0 it waits for a
// datagram and keeps none of it.
func (c *UDPConnection) Receive(maxLen int, timeoutMillis int64) (datagram *Datagram, err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "UDPConnection")
	if err != nil {
		return nil, err
	}
	buffer, err := datagramBuffer(maxLen)
	if err != nil {
		return nil, err
	}
	if err := conn.SetReadDeadline(deadlineAfterMillis(timeoutMillis)); err != nil {
		return nil, fromError(err)
	}
	n, err := conn.Read(buffer)
	if err != nil {
		return nil, fromError(err)
	}
	return &Datagram{Data: buffer[:n]}, nil
}

// MaxDatagramLen returns the largest datagram that fits in one packet on
// the link.
func (c *UDPConnection) MaxDatagramLen() (length int) {
	defer survivePanic("UDPConnection.MaxDatagramLen")
	if c.conn == nil {
		// A zero UDPConnection, which has no link to fit datagrams to.
		return 0
	}
	return c.conn.MaxDatagramLen()
}

// PeerAddress returns the peer, such as "192.0.2.7:7".
func (c *UDPConnection) PeerAddress() (address string) {
	defer survivePanic("UDPConnection.PeerAddress")
	if c.conn == nil {
		// A zero UDPConnection, which has no peer.
		return ""
	}
	return c.conn.PeerAddr().String()
}

// Close releases the socket without waiting: its queued datagrams are
// still sent. Close is idempotent.
func (c *UDPConnection) Close() (err error) {
	defer recoverPanic(&err)
	conn, err := made(c.conn, "UDPConnection")
	if err != nil {
		return err
	}
	return fromError(conn.Close())
}

// deadlineWriter is what Send needs of a connection: satnet's TCP, TLS and
// UDP connections.
type deadlineWriter interface {
	io.Writer
	SetWriteDeadline(deadline time.Time) error
}

// send writes all of data to conn by the deadline timeoutMillis from now.
func send(conn deadlineWriter, data []byte, timeoutMillis int64) error {
	if err := conn.SetWriteDeadline(deadlineAfterMillis(timeoutMillis)); err != nil {
		return fromError(err)
	}
	// Send reports no count, as the Rust bindings' send reports none: an app
	// that needs it sends with SendSome.
	_, err := conn.Write(data)
	return fromError(err)
}

// partialWriter is what SendSome needs of a stream connection: satnet's TCP
// and TLS connections, whose WriteSome is the Rust version's send_until.
type partialWriter interface {
	WriteSome(p []byte) (int, error)
	SetWriteDeadline(deadline time.Time) error
}

// sendSome queues what fits of data on conn, waiting until the deadline
// timeoutMillis from now for room, and returns how much it queued, which is
// still sent. conn's ErrTimedOut means that it queued nothing.
func sendSome(conn partialWriter, data []byte, timeoutMillis int64) (int64, error) {
	if err := conn.SetWriteDeadline(deadlineAfterMillis(timeoutMillis)); err != nil {
		return 0, fromError(err)
	}
	queued, err := conn.WriteSome(data)
	if err != nil {
		return 0, fromError(err)
	}
	return int64(queued), nil
}

// deadlineReader is what Receive needs of a stream connection: satnet's TCP
// and TLS connections.
type deadlineReader interface {
	io.Reader
	SetReadDeadline(deadline time.Time) error
}

// receiveStream returns up to maxLen bytes that conn has received, waiting
// up to timeoutMillis for the first, or the end of the stream. Data that
// arrived with an error is returned first; a lasting error is reported by
// the next call.
func receiveStream(conn deadlineReader, maxLen int, timeoutMillis int64) (*Received, error) {
	buffer, err := streamBuffer(maxLen)
	if err != nil {
		return nil, err
	}
	if err := conn.SetReadDeadline(deadlineAfterMillis(timeoutMillis)); err != nil {
		return nil, fromError(err)
	}
	n, err := conn.Read(buffer)
	switch {
	case n > 0 || err == nil:
		return &Received{Data: buffer[:n]}, nil
	case errors.Is(err, io.EOF):
		return &Received{Finished: true}, nil
	default:
		return nil, fromError(err)
	}
}

// streamBuffer returns a buffer for receiving up to maxLen bytes of a
// stream. maxLen must be at least 1, or Receive could return neither data
// nor the end of the stream.
func streamBuffer(maxLen int) ([]byte, error) {
	if maxLen < 1 {
		return nil, invalidArgument("maxLen must be at least 1")
	}
	return receiveBuffer(maxLen), nil
}

// datagramBuffer returns a buffer for the first maxLen bytes of a
// datagram. maxLen may be 0, as in the Rust bindings, as a datagram has no
// end of stream that an empty result could be taken for: Receive then
// takes a datagram and keeps none of it.
func datagramBuffer(maxLen int) ([]byte, error) {
	if maxLen < 0 {
		return nil, invalidArgument("maxLen must not be negative")
	}
	return receiveBuffer(maxLen), nil
}

// receiveBuffer returns a buffer for receiving up to maxLen bytes, at most
// maxReceiveLen.
func receiveBuffer(maxLen int) []byte {
	return make([]byte, min(maxLen, maxReceiveLen))
}
