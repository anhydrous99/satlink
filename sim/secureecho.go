package sim

import (
	"crypto/sha256"
	"crypto/tls"
	_ "embed" // the test CA, certificate and key below are fixture files
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/anhydrous99/satlink/internal/memconn"
)

// The TLS side of the secure echo service: plaintext that a client sends
// inside TLS comes back inside the same session, so that TLS clients can be
// tested end to end. The server presents a certificate for secure.test from
// a test CA; the CA, the certificate and its key are fixtures in testdata/
// that protect nothing.
//
// crypto/tls has no sans-I/O interface, as rustls had, so each session runs
// in a goroutine of its own over an in-memory connection, which the service
// pumps from and to the session's TCP socket.

//go:embed testdata/test-ca.pem
var secureTestCAPEM string

//go:embed testdata/secure.test.pem
var serverCertificatePEM []byte

//go:embed testdata/secure.test.key
var serverKeyPEM []byte

const (
	// echoChunkLen is the plaintext echoed per write: as much as one TLS
	// record carries, so that the echo spends no more on record overhead
	// than the client did.
	echoChunkLen = 16 * 1024
	// sessionStepTimeout bounds how long a step of the service waits for a
	// session to deal with the records it was given. crypto/tls takes
	// milliseconds even for a handshake; the bound only keeps a session
	// that misbehaves from stalling the simulation, which then sends its
	// answer a step later.
	sessionStepTimeout = 10 * time.Second
)

// SecureTestCAPEM returns the CA that issued secure.test's certificate, in
// PEM: the trust anchor for testing TLS clients against the simulator.
//
// Trust it in tests only, never in production: the private key of the
// certificate it vouches for is published with this package, so anyone can
// pose as secure.test to a client that trusts this CA.
func SecureTestCAPEM() string {
	return secureTestCAPEM
}

// SecureTestKeySHA256 returns the SHA-256 of secure.test's public key (its
// DER SubjectPublicKeyInfo), for testing public-key pinning against the
// simulator.
//
// Pin it in tests only, never in production: the matching private key is
// published with this package.
func SecureTestKeySHA256() [sha256.Size]byte {
	return [sha256.Size]byte{
		0x34, 0x28, 0xD1, 0x08, 0x13, 0x76, 0x33, 0x65, 0xD3, 0x7D, 0xA5, 0xA3, 0xEB, 0x09, 0xD8, 0x61,
		0x8D, 0x5E, 0xDA, 0x6D, 0x69, 0xEA, 0x0E, 0xEA, 0x70, 0xE2, 0x04, 0xF2, 0xE6, 0x2D, 0x4A, 0x08,
	}
}

// serverCertificate returns secure.test's certificate and key, parsed once.
var serverCertificate = sync.OnceValue(func() tls.Certificate {
	certificate, err := tls.X509KeyPair(serverCertificatePEM, serverKeyPEM)
	if err != nil {
		panic(fmt.Sprintf("the bundled key matches its certificate: %v", err))
	}
	return certificate
})

// tls12CipherSuites are the TLS 1.2 cipher suites of rustls's ring
// provider, which the Rust echo served: ECDHE key exchange with an AEAD
// cipher. crypto/tls's defaults would also serve a client that offers only
// the ECDHE suites with CBC and HMAC-SHA1, which the Rust echo refused.
// crypto/tls ignores the list's order: of the suites a client offers, it
// picks by its own order, where rustls's server followed the client's. Its
// TLS 1.3 suites, which a server cannot choose, are rustls's three.
var tls12CipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// newSecureEchoConfig returns the TLS settings of one simulated internet.
// Its connections share them, and with them the session ticket keys
// crypto/tls draws for each configuration, so that a client can resume a
// session on a later connection as it would with a real server.
func newSecureEchoConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{serverCertificate()},
		// TLS 1.2 and 1.3, as rustls's safe defaults.
		MinVersion:   tls.VersionTLS12,
		CipherSuites: slices.Clone(tls12CipherSuites),
		// The key exchange groups of rustls's ring provider, without a
		// post-quantum one, which the Rust server could not offer.
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
		// Records as large as rustls makes them, up to 16 KiB of plaintext.
		DynamicRecordSizingDisabled: true,
	}
}

// secureEcho is one connection's TLS session, which sends back the
// plaintext it receives.
type secureEcho struct {
	config *tls.Config
	// conn is what the session reads from and writes to; nil until the
	// client connects.
	conn *memconn.Conn
}

// newSecureEcho returns a session that waits for its client.
func newSecureEcho(config *tls.Config) *secureEcho {
	return &secureEcho{config: config}
}

// start starts the session, between the connection's local and remote
// ends, once the client has connected; later calls change nothing.
func (e *secureEcho) start(local, remote netip.AddrPort) {
	if e.conn != nil {
		return
	}
	e.conn = memconn.New(local, remote)
	// The goroutine holds the session's connection and nothing of the
	// simulation, which may be collected while it runs: closing the
	// connection ends it.
	go runSecureEcho(tls.Server(e.conn, e.config), e.conn)
}

// receive takes records from the client and has the session echo the
// plaintext they carry; it returns how many bytes it took. It takes none
// while the last echo is still to be sent, so that a client that sends
// without reading stalls, as with the plain echo service, rather than pile
// data up here. Once the session is over, it takes everything and ignores
// it.
func (e *secureEcho) receive(records []byte) int {
	switch {
	case e.conn == nil:
		return 0
	case e.conn.Closed():
		return len(records)
	case len(records) == 0 || e.conn.Pending() > 0:
		return 0
	}
	taken := e.conn.Inject(records)
	e.awaitSession()
	return taken
}

// send writes the records due to the client into room, as many bytes as
// fit, and returns how many.
func (e *secureEcho) send(room []byte) int {
	if e.conn == nil {
		return 0
	}
	return e.conn.Extract(room)
}

// hangUp tells the session that the client has hung up without saying
// goodbye, but can still read: the session says goodbye to it all the
// same.
func (e *secureEcho) hangUp() {
	if e.conn == nil || e.conn.Closed() {
		return
	}
	e.conn.CloseInbound()
	e.awaitSession()
}

// isFinished reports whether the session is over and its last records have
// been sent, so that the connection can close.
func (e *secureEcho) isFinished() bool {
	return e.conn != nil && e.conn.Closed() && e.conn.Pending() == 0
}

// close ends the session at once, and with it its goroutine.
func (e *secureEcho) close() {
	if e.conn != nil {
		closeSession(e.conn)
	}
}

// awaitSession waits until the session has dealt with what it was given,
// so that its answer leaves in the same step, as rustls's would. A session
// that takes longer answers in a later step.
func (e *secureEcho) awaitSession() {
	e.conn.AwaitReader(sessionStepTimeout)
}

// runSecureEcho runs a session: it echoes the plaintext the client sends
// until the client says goodbye — with close_notify or with FIN — or the
// session fails, and then ends the session. What crypto/tls wrote last, a
// close_notify or an alert telling the client why, still leaves after
// that.
func runSecureEcho(server *tls.Conn, conn *memconn.Conn) {
	defer closeSession(conn)
	err := echoPlaintext(server)
	switch {
	case conn.EOFDelivered() || errors.Is(err, io.EOF):
		// The client said goodbye: with FIN, which the connection reports,
		// or with close_notify. crypto/tls reports a FIN in the middle of a
		// record as io.ErrUnexpectedEOF, not io.EOF; Rust's hang_up says
		// goodbye whatever the record state, and so does this.
		sayGoodbye(server)
	case errors.Is(err, net.ErrClosed):
		// The session was closed from outside: its internet went away.
	default:
		logDebug(fmt.Sprintf("simulated internet: TLS echo failed: %v", err))
	}
}

// echoPlaintext sends back what the client sends, until reading or
// writing fails, and returns why.
func echoPlaintext(server *tls.Conn) error {
	chunk := make([]byte, echoChunkLen)
	for {
		count, err := server.Read(chunk)
		if count > 0 {
			if _, writeErr := server.Write(chunk[:count]); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			return err
		}
	}
}

// sayGoodbye answers with close_notify, after the last of the echo.
func sayGoodbye(server *tls.Conn) {
	// It fails only when the handshake never completed: crypto/tls has no
	// close_notify to send then, and the session ends without one.
	_ = server.CloseWrite()
}

// closeSession closes a session's connection, which ends the session's
// goroutine and leaves only its last records to extract.
func closeSession(conn *memconn.Conn) {
	_ = conn.Close() // Closing an in-memory connection cannot fail.
}
