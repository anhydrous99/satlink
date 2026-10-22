package satnet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"iter"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anhydrous99/satlink/internal/asciicase"
	"github.com/anhydrous99/satlink/internal/ringprovider"
	"golang.org/x/crypto/x509roots/fallback/bundle"
)

// TLS for connections over the satellite link, with crypto/tls; see "TLS"
// in the package documentation for what it offers and what it cannot.

// pemCertificateType is the PEM block type of a certificate; blocks of
// other types, such as keys, are skipped.
const pemCertificateType = "CERTIFICATE"

// pemSectionStart begins every PEM section, whatever its type.
const pemSectionStart = "-----BEGIN "

// The texts of the trust settings' errors, as the Rust version words them.
const (
	invalidPEMMessage       = "invalid PEM"
	noPEMCertificateMessage = "the PEM data holds no certificate"
	noRootsMessage          = "no root trust anchors were provided"
)

// errMalformedPEMSection is why PEM data that holds a section which does not
// decode is invalid.
var errMalformedPEMSection = errors.New("a section is malformed")

// TLSOptions are the trust settings for a [TLSConnector]. The zero value
// trusts the public web PKI, pins nothing and offers no ALPN.
type TLSOptions struct {
	// ExtraRootsPEM are PEM bundles of further certificates to trust, such
	// as a private CA. Each must hold at least one certificate.
	ExtraRootsPEM [][]byte

	// ExtraRootsDER are further certificates to trust, DER-encoded.
	ExtraRootsDER [][]byte

	// WithoutPublicRoots trusts only the extra roots, not the public web
	// PKI.
	WithoutPublicRoots bool

	// PinnedPublicKeys, if any, also require the server's public key to be
	// one of these: the SHA-256 of its DER SubjectPublicKeyInfo, as in
	// `openssl x509 -pubkey -noout | openssl pkey -pubin -outform der |
	// sha256sum`. Pin the server's own key, plus a backup. Only the
	// server's own certificate is checked against the pins, and only after
	// the whole chain has been verified: the intermediates a server sends
	// are chosen by the server, so a pin that any sent certificate could
	// satisfy would let an attacker append the pinned certificate to an
	// unrelated valid chain.
	//
	// A server that holds several certificates may send satnet another one
	// than it sends the Rust version, so pin the key of each: crypto/tls,
	// which cannot be told otherwise, asks for ML-DSA, then RSA-PSS,
	// signatures before ECDSA ones, where rustls asks for ECDSA first and
	// never for ML-DSA or P-521.
	PinnedPublicKeys [][sha256.Size]byte

	// ALPNProtocols are offered through ALPN, most preferred first.
	ALPNProtocols []string
}

// TLSConnector makes TLS connections. Make one and share it for the whole
// app: resumption tickets are kept per connector, and a resumed handshake
// costs about 1 KB where a full one costs 3–5 KB. It is safe for use by
// several goroutines.
//
// Connections use TLS 1.2 or 1.3 with X25519, P-256 and P-384 key exchange,
// the groups rustls's ring backend offers, and send a key share for X25519
// only: no post-quantum key share. TLS 1.2 is offered with rustls's suites
// only, ECDHE with AES-GCM or ChaCha20-Poly1305. A server's certificate
// chain must not hold an RSA key of under 2041 or over 8192 bits, or an
// ECDSA key on P-224, which rustls's ring backend refuses too: a server or
// a CA with a short RSA key is refused, as in the Rust version. It may hold
// ML-DSA and P-521 keys, which ring cannot verify, as crypto/tls asks
// servers for them, and a server that holds several certificates may send
// another one than it sends the Rust version: see
// [TLSOptions.PinnedPublicKeys]. crypto/tls resumes a TLS 1.2 session with
// a ticket only, where rustls also resumed one by its session ID: a TLS 1.2
// server that issues no tickets costs a full handshake every time. The
// public web PKI is Mozilla's root program, as the NSS bundle of
// golang.org/x/crypto/x509roots/fallback carries it, whatever the operating
// system trusts. For a root it phases out, NSS distrusts what the root
// issued after a date, which the bundle carries and the Rust version's
// roots could not: such a chain is refused.
//
// The zero TLSConnector is not usable; create one with [NewTLSConnector].
type TLSConnector struct {
	// config is shared by every connection, with its session cache; each
	// connection gets a copy naming its server.
	config *tls.Config
}

// NewTLSConnector returns a connector with options' trust settings and a
// fresh session cache. It fails with an [*Error] of kind ErrTLS for extra
// roots that do not parse — "invalid PEM: …", or "the PEM data holds no
// certificate", most likely the wrong file — and for a connector that would
// trust nothing.
func NewTLSConnector(options TLSOptions) (*TLSConnector, error) {
	roots, err := trustAnchors(options)
	if err != nil {
		return nil, err
	}
	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The TLS 1.2 suites rustls offers. crypto/tls ignores the list's
		// order; its TLS 1.3 suites, which a client cannot choose, are
		// rustls's three.
		CipherSuites: ringprovider.TLS12CipherSuites(),
		// The groups of rustls's ring backend, without the post-quantum ones:
		// X25519MLKEM768 would add about 2.3 KB to every handshake, 1.2 KB of
		// it in the ClientHello. crypto/tls ignores the list's order and
		// sends a key share for the first of its own order, X25519.
		CurvePreferences:   ringprovider.KeyExchangeGroups(),
		ClientSessionCache: caseBlindSessionCache{sessions: tls.NewLRUClientSessionCache(0)},
		RootCAs:            roots,
		// Refuses chains holding an RSA key of under 2041 or over 8192 bits,
		// or an ECDSA key on P-224, as rustls's ring backend does: crypto/tls
		// alone would take RSA keys from 1024 bits.
		VerifyPeerCertificate: verifyChainKeys,
		NextProtos:            slices.Clone(options.ALPNProtocols),
		// Records as large as rustls's, up to 16 KiB of plaintext:
		// crypto/tls would otherwise send its first 128 KB in records of
		// about 1.2 KB, each with 22 bytes of TLS 1.3 overhead.
		DynamicRecordSizingDisabled: true,
	}
	if len(options.PinnedPublicKeys) > 0 {
		config.VerifyConnection = verifyPinnedKey(slices.Clone(options.PinnedPublicKeys))
	}
	return &TLSConnector{config: config}, nil
}

// caseBlindSessionCache is a session cache that ignores the ASCII case of
// the server names it keeps sessions for, as rustls's does: a session made
// for "API.example.com" resumes for "api.example.com", where crypto/tls
// would key it by the name as written and pay for a full handshake. The
// handshake still names the server as written, as rustls's does.
type caseBlindSessionCache struct {
	sessions tls.ClientSessionCache
}

// Get returns the session kept for key, crypto/tls's cache key: the server
// name.
func (c caseBlindSessionCache) Get(key string) (*tls.ClientSessionState, bool) {
	return c.sessions.Get(asciicase.Lower(key))
}

// Put keeps session for key, crypto/tls's cache key, or forgets the session
// kept for it when session is nil.
func (c caseBlindSessionCache) Put(key string, session *tls.ClientSessionState) {
	c.sessions.Put(asciicase.Lower(key), session)
}

// trustAnchors returns the roots options trusts: the public web PKI unless
// it is left out, and the extra roots.
func trustAnchors(options TLSOptions) (*x509.CertPool, error) {
	roots := x509.NewCertPool()
	count := 0
	if !options.WithoutPublicRoots {
		public := publicRoots()
		roots, count = public.pool.Clone(), public.count
	}
	extras, err := extraRoots(options)
	if err != nil {
		return nil, err
	}
	for _, root := range extras {
		roots.AddCert(root)
	}
	if count+len(extras) == 0 {
		return nil, &Error{Kind: ErrTLS, Message: noRootsMessage}
	}
	return roots, nil
}

// extraRoots parses the extra roots: the PEM bundles, then the DER
// certificates.
func extraRoots(options TLSOptions) ([]*x509.Certificate, error) {
	var roots []*x509.Certificate
	for _, data := range options.ExtraRootsPEM {
		certificates, err := parsePEMCertificates(data)
		if err != nil {
			return nil, err
		}
		roots = append(roots, certificates...)
	}
	for _, der := range options.ExtraRootsDER {
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, &Error{Kind: ErrTLS, Err: err}
		}
		roots = append(roots, certificate)
	}
	return roots, nil
}

// parsePEMCertificates returns the certificates of a PEM bundle, which must
// hold at least one: a bundle without any is most likely the wrong file,
// and trusting nothing new is no help.
func parsePEMCertificates(data []byte) ([]*x509.Certificate, error) {
	var certificates []*x509.Certificate
	sections := 0
	for block, rest := pem.Decode(data); block != nil; block, rest = pem.Decode(rest) {
		sections++
		if block.Type != pemCertificateType {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, &Error{Kind: ErrTLS, Err: err}
		}
		certificates = append(certificates, certificate)
	}
	// pem.Decode skips a section that does not decode; every section start
	// must have given a block.
	if sections < sectionStarts(data) {
		return nil, &Error{Kind: ErrTLS, Message: invalidPEMMessage, Err: errMalformedPEMSection}
	}
	if len(certificates) == 0 {
		return nil, &Error{Kind: ErrTLS, Message: noPEMCertificateMessage}
	}
	return certificates, nil
}

// sectionStarts counts the PEM sections that data begins, where pem.Decode
// and rustls's parser look for them: at the start of the data or of a line.
// A marker inside a line, in a comment say, begins nothing.
func sectionStarts(data []byte) int {
	starts := bytes.Count(data, []byte("\n"+pemSectionStart))
	if bytes.HasPrefix(data, []byte(pemSectionStart)) {
		starts++
	}
	return starts
}

// rootPool is a pool of trust anchors and how many it holds.
type rootPool struct {
	pool  *x509.CertPool
	count int
}

// publicRoots returns Mozilla's root program, built once.
var publicRoots = sync.OnceValue(func() rootPool { return rootPoolOf(bundle.Roots()) })

// rootPoolOf returns a pool of the bundled roots, with the constraints NSS
// places on some of them: the date after which it distrusts what a root it
// phases out issued.
func rootPoolOf(bundled iter.Seq[bundle.Root]) rootPool {
	roots := rootPool{pool: x509.NewCertPool()}
	for root := range bundled {
		certificate, err := x509.ParseCertificate(root.Certificate)
		if err != nil {
			// The bundle's own tests parse every root; one that does not
			// parse could not anchor a chain anyway.
			continue
		}
		if root.Constraint == nil {
			roots.pool.AddCert(certificate)
		} else {
			roots.pool.AddCertWithConstraint(certificate, root.Constraint)
		}
		roots.count++
	}
	return roots
}

// Secure secures conn with TLS for serverName, completing the handshake
// within timeout, and takes conn over: on failure it is closed.
//
// serverName must be an IP address (without a zone), or a host name — at
// most 253 bytes of labels of 1 to 63 letters, digits, '-' (not at either
// end) and '_', with an optional trailing dot, as in policy rules — whose
// last label is not all digits. Anything else fails with ErrInvalidHost
// before a byte is sent: crypto/tls would only refuse it after a full
// handshake, at the cost of its airtime.
//
// It fails with ErrTLS when the handshake fails (certificate, protocol, a
// pin that does not match, or "the server closed the connection during the
// handshake"), and with conn's errors, such as ErrTimedOut for operation
// "TCP read" or "TCP write", and ErrLinkDown.
func (c *TLSConnector) Secure(conn *TCPConn, serverName string, timeout time.Duration) (*TLSConn, error) {
	return c.secure(conn, serverName, deadlineAfter(timeout))
}

// secure secures transport for serverName by deadline, closing it on
// failure.
func (c *TLSConnector) secure(transport tlsTransport, serverName string, deadline time.Time) (*TLSConn, error) {
	if !isValidServerName(serverName) {
		closeAbandoned(transport)
		return nil, &Error{Kind: ErrInvalidHost, Host: serverName}
	}
	config := c.config.Clone()
	config.ServerName = serverName
	conn := newTLSConn(transport, config)
	if err := conn.handshake(deadline); err != nil {
		closeAbandoned(transport)
		return nil, err
	}
	return conn, nil
}

// closeAbandoned closes the connection of a session that never started.
func closeAbandoned(transport tlsTransport) {
	// What failed is reported; closing a TCP connection cannot fail.
	_ = transport.Close()
}

// Config returns a copy of the connector's TLS configuration, sharing its
// session cache, for crypto/tls or net/http — for example as an
// http.Transport's TLSClientConfig next to [Link.DialContext].
func (c *TLSConnector) Config() *tls.Config {
	return c.config.Clone()
}

// DialTLS opens a TCP connection to host on port as [Link.Dial] does and
// secures it with TLS for that name, all within timeout. connector's
// session cache lets later connections to the same server resume.
func (l *Link) DialTLS(host string, port uint16, connector *TLSConnector, timeout time.Duration) (*TLSConn, error) {
	deadline := deadlineAfter(timeout)
	conn, err := l.dial(context.Background(), host, port, deadline)
	if err != nil {
		return nil, err
	}
	return connector.secure(conn, host, deadline)
}

// isValidServerName reports whether name can be a TLS server name, as
// rustls's ServerName::try_from decides: an IP address without a zone, or a
// host name whose last label is not all digits, which would make it an
// address.
func isValidServerName(name string) bool {
	if address, err := netip.ParseAddr(name); err == nil {
		return address.Zone() == ""
	}
	host, ok := normalizeHost(name)
	if !ok {
		return false
	}
	lastLabel := host[strings.LastIndexByte(host, '.')+1:]
	return strings.Trim(lastLabel, "0123456789") != ""
}
