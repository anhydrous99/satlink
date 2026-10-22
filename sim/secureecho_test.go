package sim

import (
	"bytes"
	"crypto/tls"
	"log/slog"
	"net/netip"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// Go only: the TLS echo's sessions, which run crypto/tls in goroutines of
// their own. A TLSClient talks to a session directly here, its records
// moved by the test as the service moves them between a session and its
// TCP socket.

const (
	// sessionPatience bounds every wait for a session or its client; a
	// test that reaches it has failed.
	sessionPatience = 10 * time.Second
	// recordRoom is how much the test moves per step, as a TCP buffer of
	// the service would.
	recordRoom = 4096
	// maxRecordPlaintext is the most plaintext one TLS record carries
	// (RFC 8446, section 5.1).
	maxRecordPlaintext = 1 << 14
)

var (
	sessionServerEnd = netip.MustParseAddrPort("192.0.2.43:443")
	sessionClientEnd = netip.MustParseAddrPort("10.64.0.2:49152")
)

// sessionPair is a secure echo session and a client connected straight to
// it.
type sessionPair struct {
	t      *testing.T
	echo   *secureEcho
	client *TLSClient
	// toEcho holds the records the client wrote that the echo has not
	// taken yet, as the service's TCP socket would.
	toEcho []byte
}

// connectSession starts a session of a fresh echo of config, and a client
// that resumes sessions through cache.
func connectSession(t *testing.T, config *tls.Config, cache tls.ClientSessionCache) *sessionPair {
	t.Helper()
	return connectClient(t, config, secureTestClientConfig(t, cache))
}

// connectClient starts a session of a fresh echo of config, and a client
// with clientConfig.
func connectClient(t *testing.T, config, clientConfig *tls.Config) *sessionPair {
	t.Helper()
	echo := newSecureEcho(config)
	echo.start(sessionServerEnd, sessionClientEnd)
	t.Cleanup(echo.close)
	return &sessionPair{t: t, echo: echo, client: newTLSClient(t, sessionClientEnd, sessionServerEnd, clientConfig)}
}

// connectTLS12ClientOffering starts a session of a fresh echo, and a client
// that speaks TLS 1.2 at most and offers suite alone.
func connectTLS12ClientOffering(t *testing.T, suite uint16) *sessionPair {
	t.Helper()
	clientConfig := secureTestClientConfig(t, nil)
	clientConfig.MaxVersion = tls.VersionTLS12
	clientConfig.CipherSuites = []uint16{suite}
	return connectClient(t, newSecureEchoConfig(), clientConfig)
}

// exchange moves records both ways until neither direction moves more.
func (p *sessionPair) exchange() {
	for {
		p.toEcho = append(p.toEcho, extractAll(p.client.Conn)...)
		taken := p.echo.receive(p.toEcho)
		p.toEcho = p.toEcho[taken:]
		room := make([]byte, recordRoom)
		sent := p.echo.send(room)
		if sent > 0 {
			p.client.Conn.Inject(room[:sent])
			p.client.Conn.AwaitReader(sessionPatience)
		}
		if taken == 0 && sent == 0 {
			return
		}
	}
}

// handshake runs the exchange until the client's handshake has finished,
// and returns how it failed: nil once it succeeded.
func (p *sessionPair) handshake() error {
	p.t.Helper()
	if !p.client.Conn.AwaitReader(sessionPatience) {
		p.t.Fatal("the client never sent its hello")
	}
	p.exchange()
	return p.client.AwaitHandshake(sessionPatience)
}

// completeHandshake runs the handshake, failing the test if it fails.
func (p *sessionPair) completeHandshake() {
	p.t.Helper()
	if err := p.handshake(); err != nil {
		p.t.Fatalf("the handshake failed: %v", err)
	}
}

// echoed sends message inside TLS and returns what has come back once the
// exchange has settled.
func (p *sessionPair) echoed(message []byte) []byte {
	p.t.Helper()
	if _, err := p.client.TLS.Write(message); err != nil {
		p.t.Fatalf("writing: %v", err)
	}
	p.exchange()
	return p.client.Plaintext()
}

// record returns the records that carry message, which the client wrote
// without the test moving them.
func (p *sessionPair) record(message string) []byte {
	p.t.Helper()
	if _, err := p.client.TLS.Write([]byte(message)); err != nil {
		p.t.Fatalf("writing: %v", err)
	}
	return extractAll(p.client.Conn)
}

// assertServerSaidGoodbye fails the test unless the client has read the
// session's close_notify and the session is finished.
func (p *sessionPair) assertServerSaidGoodbye() {
	p.t.Helper()
	if !p.client.awaitEnd(sessionPatience) || !p.client.ServerSaidGoodbye() {
		p.t.Errorf("the client's session ended with %v (FIN %v); want the server's close_notify",
			p.client.finalError(), p.client.Conn.EOFDelivered())
	}
	if !p.echo.isFinished() {
		p.t.Error("the session is not finished after saying goodbye")
	}
}

func TestASessionProvesToBeSecureTestAndEchoesInsideTLS(t *testing.T) {
	t.Parallel()
	pair := connectSession(t, newSecureEchoConfig(), nil)
	pair.completeHandshake()
	// More than a record of the client's buffer holds, so that the echo
	// takes several steps.
	message := bytes.Repeat([]byte("Sealed all the way to orbit and back. "), 160)

	echoed := pair.echoed(message)

	if !bytes.Equal(echoed, message) {
		t.Errorf("got %d bytes back; want the %d sent", len(echoed), len(message))
	}
}

func TestASessionTakesNoRecordsWhileItsEchoWaits(t *testing.T) {
	t.Parallel()
	pair := connectSession(t, newSecureEchoConfig(), nil)
	pair.completeHandshake()
	first, second := pair.record("first"), pair.record("second")

	firstTaken := pair.echo.receive(first)
	secondTaken := pair.echo.receive(second)

	if firstTaken != len(first) || secondTaken != 0 {
		t.Errorf("took %d, then %d bytes while the echo waited; want %d, then 0", firstTaken, secondTaken, len(first))
	}
	pair.echo.send(make([]byte, recordRoom))
	if taken := pair.echo.receive(second); taken != len(second) {
		t.Errorf("took %d bytes once the echo had gone; want %d", taken, len(second))
	}
}

func TestAClientsCloseNotifyIsAnsweredWithCloseNotify(t *testing.T) {
	t.Parallel()
	pair := connectSession(t, newSecureEchoConfig(), nil)
	pair.completeHandshake()

	if err := pair.client.TLS.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	pair.exchange()

	pair.assertServerSaidGoodbye()
}

func TestASessionWhoseClientHungUpStillSaysGoodbye(t *testing.T) {
	t.Parallel()
	pair := connectSession(t, newSecureEchoConfig(), nil)
	pair.completeHandshake()

	pair.echo.hangUp()
	pair.exchange()

	pair.assertServerSaidGoodbye()
	if taken := pair.echo.receive([]byte("late records")); taken != len("late records") {
		t.Errorf("a finished session took %d bytes; want all, ignored", taken)
	}
}

// Rust's hang_up says goodbye whatever the record state. A FIN in the
// middle of a record fails crypto/tls's Read with io.ErrUnexpectedEOF
// rather than io.EOF, and the session says goodbye all the same.
func TestASessionWhoseClientHungUpInTheMiddleOfARecordStillSaysGoodbye(t *testing.T) {
	t.Parallel()
	pair := connectSession(t, newSecureEchoConfig(), nil)
	pair.completeHandshake()
	records := pair.record("cut short")
	firstHalf := records[:len(records)/2]
	if taken := pair.echo.receive(firstHalf); taken != len(firstHalf) {
		t.Fatalf("took %d of the %d bytes of half a record", taken, len(firstHalf))
	}

	pair.echo.hangUp()
	pair.exchange()

	pair.assertServerSaidGoodbye()
}

// As Rust's is_finished, which waits until rustls has nothing left to
// write: the connection closes only once the rest of the echo and the
// close_notify have gone, or they would never leave.
func TestASessionIsFinishedOnlyOnceItsLastRecordsHaveGone(t *testing.T) {
	t.Parallel()
	pair := connectSession(t, newSecureEchoConfig(), nil)
	pair.completeHandshake()
	pair.echo.receive(pair.record("echoed just before the client hung up"))

	pair.echo.hangUp()
	finishedBeforeSending := pair.echo.isFinished()
	pair.exchange()

	if finishedBeforeSending {
		t.Error("the session was finished with its echo and close_notify still to be sent")
	}
	pair.assertServerSaidGoodbye()
}

func TestAClientThatHangsUpBeforeItsHelloEndsTheSession(t *testing.T) {
	t.Parallel()
	echo := newSecureEcho(newSecureEchoConfig())
	echo.start(sessionServerEnd, sessionClientEnd)

	echo.hangUp()

	if !echo.isFinished() || echo.send(make([]byte, recordRoom)) != 0 {
		t.Error("the session went on, or had something to say, without a client")
	}
}

func TestASessionThatFailsEndsAndIgnoresTheRest(t *testing.T) {
	t.Parallel()
	echo := newSecureEcho(newSecureEchoConfig())
	echo.start(sessionServerEnd, sessionClientEnd)

	taken := echo.receive([]byte("GET / HTTP/1.0\r\n\r\n"))
	echo.send(make([]byte, recordRoom))

	if taken == 0 || !echo.isFinished() {
		t.Errorf("took %d bytes and finished %v; want the session failed and over", taken, echo.isFinished())
	}
	if late := echo.receive([]byte("more")); late != len("more") {
		t.Errorf("a failed session took %d bytes; want all, ignored", late)
	}
}

// What `satlink --simulate -vv` shows of a session that fails, such as one
// whose client rejects the certificate: Rust's text, with crypto/tls's
// reason (docs/PORTING.md, section 3). The test records the process-wide
// logger, so it does not run in parallel.
func TestAFailedSessionLogsWhyAsTheRustVersionLoggedIt(t *testing.T) {
	recorder := recordLogs(t)
	echo := newSecureEcho(newSecureEchoConfig())
	echo.start(sessionServerEnd, sessionClientEnd)
	t.Cleanup(echo.close)

	echo.receive([]byte("GET / HTTP/1.0\r\n\r\n"))

	want := logRecord{
		level:   slog.LevelDebug,
		message: "simulated internet: TLS echo failed: tls: first record does not look like a TLS handshake",
	}
	if got := recorder.recorded(); !slices.Contains(got, want) {
		t.Errorf("records %v; want one of them %v", got, want)
	}
}

func TestASessionNotStartedTakesAndSendsNothing(t *testing.T) {
	t.Parallel()
	echo := newSecureEcho(newSecureEchoConfig())

	echo.hangUp()
	echo.close()

	if echo.receive([]byte("x")) != 0 || echo.send(make([]byte, 8)) != 0 || echo.isFinished() {
		t.Error("a session without a client did something")
	}
}

func TestAClientResumesItsSessionOnALaterConnection(t *testing.T) {
	t.Parallel()
	config := newSecureEchoConfig()
	cache := tls.NewLRUClientSessionCache(1)
	first := connectSession(t, config, cache)
	first.completeHandshake()
	// The session ticket arrives after the handshake, and the client takes
	// it in while reading.
	first.echoed([]byte("ping"))

	second := connectSession(t, config, cache)
	second.completeHandshake()

	if !second.client.TLS.ConnectionState().DidResume {
		t.Error("the second connection did not resume the first's session")
	}
}

// TLS 1.2 and 1.3, as rustls's safe default protocol versions: a client
// that speaks TLS 1.2 at most is served too.
func TestTheEchoServesATLS12Client(t *testing.T) {
	t.Parallel()
	clientConfig := secureTestClientConfig(t, nil)
	clientConfig.MaxVersion = tls.VersionTLS12
	pair := connectClient(t, newSecureEchoConfig(), clientConfig)

	pair.completeHandshake()

	if version := pair.client.TLS.ConnectionState().Version; version != tls.VersionTLS12 {
		t.Errorf("the session runs %s; want TLS 1.2", tls.VersionName(version))
	}
}

// TLS 1.2's cipher suites of rustls's ring provider, ECDHE with an AEAD
// cipher: a client that offers one of them alone is served with it, as the
// Rust echo served it. Only the ECDSA ones can be agreed: secure.test's key
// is on P-256.
func TestTheEchoServesATLS12ClientThatOffersOnlyAnAEADSuite(t *testing.T) {
	t.Parallel()
	for _, suite := range []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	} {
		t.Run(tls.CipherSuiteName(suite), func(t *testing.T) {
			t.Parallel()
			pair := connectTLS12ClientOffering(t, suite)

			pair.completeHandshake()

			if agreed := pair.client.TLS.ConnectionState().CipherSuite; agreed != suite {
				t.Errorf("the session agreed %s; want %s", tls.CipherSuiteName(agreed), tls.CipherSuiteName(suite))
			}
		})
	}
}

// rustls's ring provider has no suite with CBC: a client that offers only
// such a suite is refused, as the Rust echo refused it. crypto/tls
// implements all three, and its defaults would serve the first two.
func TestTheEchoRefusesATLS12ClientThatOffersOnlyCBC(t *testing.T) {
	t.Parallel()
	for _, suite := range []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
	} {
		t.Run(tls.CipherSuiteName(suite), func(t *testing.T) {
			t.Parallel()
			pair := connectTLS12ClientOffering(t, suite)

			err := pair.handshake()

			if err == nil || !strings.Contains(err.Error(), "handshake failure") {
				t.Errorf("the handshake ended with %v; want the echo's handshake_failure alert", err)
			}
		})
	}
}

// The key exchange groups of rustls's ring provider, which has no
// post-quantum one: a client that prefers X25519MLKEM768, as crypto/tls's
// clients do by default, agrees X25519, whose key share takes 32 bytes of
// the ServerHello rather than 1120.
func TestTheEchoAgreesNoPostQuantumKeyExchange(t *testing.T) {
	t.Parallel()
	clientConfig := secureTestClientConfig(t, nil)
	clientConfig.CurvePreferences = []tls.CurveID{tls.X25519MLKEM768, tls.X25519}
	pair := connectClient(t, newSecureEchoConfig(), clientConfig)

	pair.completeHandshake()

	if agreed := pair.client.TLS.ConnectionState().CurveID; agreed != tls.X25519 {
		t.Errorf("the session agreed %v; want X25519", agreed)
	}
}

// As in Rust, whose echo writes as much plaintext at a time as a record
// carries (ECHO_CHUNK_LEN), and whose rustls makes records as large as
// that: the echo of a whole record is one record, so that the echo spends
// no more on record overhead than the client did. crypto/tls would
// otherwise start each connection with small records.
func TestTheEchoOfAWholeRecordIsOneRecord(t *testing.T) {
	t.Parallel()
	clientConfig := secureTestClientConfig(t, nil)
	clientConfig.DynamicRecordSizingDisabled = true
	pair := connectClient(t, newSecureEchoConfig(), clientConfig)
	pair.completeHandshake()
	record := pair.record(strings.Repeat("x", maxRecordPlaintext))

	pair.echo.receive(record)
	echo := extractAll(pair.echo.conn)

	if len(echo) != len(record) {
		t.Errorf("the echo of a %d-byte record took %d bytes; want as many", len(record), len(echo))
	}
}

func TestClosingASessionEndsItsGoroutine(t *testing.T) {
	baseline := runtime.NumGoroutine()
	pair := connectSession(t, newSecureEchoConfig(), nil)
	pair.completeHandshake()

	pair.echo.close()
	pair.client.Close()

	AwaitGoroutines(t, baseline)
}

func TestClosingTheServicesEndsTheirSessions(t *testing.T) {
	baseline := runtime.NumGoroutine()
	pair := connectSession(t, newSecureEchoConfig(), nil)
	pair.completeHandshake()
	services := &services{connections: []*connection{
		{session: session{kind: serviceEcho}},
		{session: session{kind: serviceSecureEcho, secureEcho: pair.echo}},
	}}

	// What closing the session's internet does.
	services.close()
	pair.client.Close()

	AwaitGoroutines(t, baseline)
}
