package sim

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/memconn"
)

// The TLS client of the tests of the simulator's TLS echo. It lives in a
// test file of package sim, so that the internal tests and the external
// ones (package sim_test) share it; what the external tests use is
// exported, as they cannot reach unexported test code.

// TLSServerName is the name on the TLS echo's certificate.
const TLSServerName = "secure.test"

// tlsClientRoom is how much plaintext the client reads at a time.
const tlsClientRoom = 4096

// TLSClient is a TLS client for tests: crypto/tls over an in-memory
// connection whose records the test moves, trusting the test CA and
// nothing else. Its own goroutine completes the handshake and then reads
// the plaintext that comes back, until the session ends; it then closes
// its connection, so that waiting for it to read again ends at once.
type TLSClient struct {
	// Conn is the client's side of the in-memory connection: the test
	// extracts the client's records from it and injects the server's.
	Conn *memconn.Conn
	// TLS is the client's session.
	TLS *tls.Conn

	// handshakeDone closes once handshakeErr holds how the handshake
	// ended; finished closes once the client has stopped reading.
	handshakeDone chan struct{}
	handshakeErr  error
	finished      chan struct{}

	mu        sync.Mutex
	plaintext []byte
	finalErr  error
}

// NewTLSClient starts a client between local and remote that resumes
// sessions through cache (nil: none), and closes it when the test ends.
func NewTLSClient(t testing.TB, local, remote netip.AddrPort, cache tls.ClientSessionCache) *TLSClient {
	t.Helper()
	return newTLSClient(t, local, remote, secureTestClientConfig(t, cache))
}

// secureTestClientConfig returns the settings of a client of the TLS echo,
// which trusts the test CA and nothing else, and resumes sessions through
// cache (nil: none).
func secureTestClientConfig(t testing.TB, cache tls.ClientSessionCache) *tls.Config {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(SecureTestCAPEM())) {
		t.Fatal("the test CA is not valid PEM")
	}
	return &tls.Config{RootCAs: roots, ServerName: TLSServerName, ClientSessionCache: cache}
}

// newTLSClient starts a client with config between local and remote, and
// closes it when the test ends.
func newTLSClient(t testing.TB, local, remote netip.AddrPort, config *tls.Config) *TLSClient {
	conn := memconn.New(local, remote)
	client := &TLSClient{
		Conn:          conn,
		TLS:           tls.Client(conn, config),
		handshakeDone: make(chan struct{}),
		finished:      make(chan struct{}),
	}
	go client.run()
	t.Cleanup(client.Close)
	return client
}

func (c *TLSClient) run() {
	defer close(c.finished)
	defer c.closeConn()
	c.handshakeErr = c.TLS.Handshake()
	close(c.handshakeDone)
	if c.handshakeErr != nil {
		c.finish(c.handshakeErr)
		return
	}
	buffer := make([]byte, tlsClientRoom)
	for {
		count, err := c.TLS.Read(buffer)
		c.mu.Lock()
		c.plaintext = append(c.plaintext, buffer[:count]...)
		c.mu.Unlock()
		if err != nil {
			c.finish(err)
			return
		}
	}
}

func (c *TLSClient) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finalErr = err
}

// AwaitHandshake waits up to timeout for the handshake to finish, and
// returns how it failed: nil once it succeeded.
func (c *TLSClient) AwaitHandshake(timeout time.Duration) error {
	if !awaitClosed(c.handshakeDone, timeout) {
		return errors.New("the handshake never finished")
	}
	return c.handshakeErr
}

// HandshakeFinished reports, without waiting, whether the handshake has
// finished, successfully or not.
func (c *TLSClient) HandshakeFinished() bool {
	return awaitClosed(c.handshakeDone, 0)
}

// Plaintext returns what the client has read so far.
func (c *TLSClient) Plaintext() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.plaintext)
}

// IsReading reports whether the client still reads: its session has not
// ended.
func (c *TLSClient) IsReading() bool {
	return !awaitClosed(c.finished, 0)
}

// awaitEnd waits up to timeout for the client's session to end, and
// reports whether it did.
func (c *TLSClient) awaitEnd(timeout time.Duration) bool {
	return awaitClosed(c.finished, timeout)
}

// finalError returns why the client stopped reading: io.EOF for a
// close_notify or a FIN at a record boundary; nil while it reads.
func (c *TLSClient) finalError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.finalErr
}

// ServerSaidGoodbye reports whether the session has ended with the
// server's close_notify, everything before it read: io.EOF from crypto/tls
// without a FIN under it.
func (c *TLSClient) ServerSaidGoodbye() bool {
	if c.IsReading() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return errors.Is(c.finalErr, io.EOF) && !c.Conn.EOFDelivered()
}

// Close ends the client, and with it its goroutine.
func (c *TLSClient) Close() {
	c.closeConn()
	<-c.finished
}

func (c *TLSClient) closeConn() {
	_ = c.Conn.Close() // Closing an in-memory connection cannot fail.
}

// awaitClosed waits up to timeout for done to close, and reports whether
// it has; a done channel already closed counts at once, whatever timeout.
func awaitClosed(done <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-done:
		return true
	default:
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// extractAll takes everything conn's session has written.
func extractAll(conn *memconn.Conn) []byte {
	records := make([]byte, conn.Pending())
	return records[:conn.Extract(records)]
}
