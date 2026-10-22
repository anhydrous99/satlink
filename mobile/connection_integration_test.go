package mobile_test

import (
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/mobile"
	"github.com/anhydrous99/satlink/sim"
)

// The connections over a link, as an app uses them: TCP to the simulator's
// web and echo services, UDP to its echo, and TLS to its TLS echo.

func TestAWebPageArrivesWhole(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, settingsAllowing("web.test:80"), nil)
	connection, err := link.OpenTCP("web.test", 80, timeoutMillis)
	if err != nil {
		t.Fatalf("OpenTCP: %v", err)
	}
	defer closeConnection(t, connection.Close)
	if err := connection.Send([]byte("GET / HTTP/1.0\r\nHost: web.test\r\n\r\n"), timeoutMillis); err != nil {
		t.Fatalf("Send: %v", err)
	}

	response := receiveUntilFinished(t, connection.Receive)

	if !strings.HasPrefix(response, "HTTP/1.0 200") || !strings.HasSuffix(response, "Hello from the simulated Iridium network!\n") {
		t.Errorf("the response is %q", response)
	}
	if peer := connection.PeerAddress(); peer != "192.0.2.80:80" {
		t.Errorf("the peer is %q, want 192.0.2.80:80", peer)
	}
}

// receiveUntilFinished returns everything a stream connection receives
// until the end of the stream.
func receiveUntilFinished(t *testing.T, receive func(maxLen int, timeoutMillis int64) (*mobile.Received, error)) string {
	t.Helper()
	var text strings.Builder
	for {
		received, err := receive(4096, timeoutMillis)
		if err != nil {
			t.Fatalf("Receive after %q: %v", text.String(), err)
		}
		if received.Finished {
			return text.String()
		}
		text.Write(received.Data)
	}
}

// closeConnection closes a connection at the end of a test.
func closeConnection(t *testing.T, closeIt func() error) {
	t.Helper()
	if err := closeIt(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// closeAtCleanup closes a connection with closeConnection when the test
// ends, in case the test has not: Close is idempotent.
func closeAtCleanup(t *testing.T, closeIt func() error) {
	t.Helper()
	t.Cleanup(func() { closeConnection(t, closeIt) })
}

func TestAClosedConnectionSaysSo(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, settingsAllowing("echo.test:7"), nil)
	connection, err := link.OpenTCP("echo.test", 7, timeoutMillis)
	if err != nil {
		t.Fatalf("OpenTCP: %v", err)
	}

	closeConnection(t, connection.Close)

	sendErr := connection.Send([]byte("too late"), timeoutMillis)
	_, sendSomeErr := connection.SendSome([]byte("too late"), timeoutMillis)
	_, receiveErr := connection.Receive(64, timeoutMillis)
	abortErr := connection.Abort()
	for name, err := range map[string]error{"Send": sendErr, "SendSome": sendSomeErr, "Receive": receiveErr, "Abort": abortErr} {
		if details := mobile.AsSatlinkError(err); details == nil || details.Reason != "the connection is closed" {
			t.Errorf("%s after Close fails with %v, want \"the connection is closed\"", name, err)
		}
	}
	closeConnection(t, connection.Close)
}

func TestAnAbortedConnectionKeepsWhatWasSettled(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, settingsAllowing("echo.test:7"), nil)
	connection, err := link.OpenTCP("echo.test", 7, timeoutMillis)
	if err != nil {
		t.Fatalf("OpenTCP: %v", err)
	}
	defer closeConnection(t, connection.Close)
	sendAndReadTheEchosFirstByte(t, connection, "hello")

	if err := connection.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	if err := connection.WaitAcknowledged(timeoutMillis); err != nil {
		t.Errorf("WaitAcknowledged after Abort = %v, want success: everything sent was acknowledged", err)
	}
	if err := connection.ShutdownWrite(); err != nil {
		t.Errorf("ShutdownWrite after Abort = %v, want success: there is nothing left to end", err)
	}
	if rest, err := connection.Receive(64, 0); err != nil || string(rest.Data) != "ello" {
		t.Errorf("Receive after Abort = %v, %v; want the rest of the echo, which had arrived", rest, err)
	}
	_, err = connection.Receive(64, 0)
	assertKind(t, err, mobile.SatlinkErrorConnectionReset)
}

// sendAndReadTheEchosFirstByte sends message to the echo service over
// connection, waits until it was acknowledged, and reads the first byte of
// its echo, which shows that the segment holding all of it has arrived.
func sendAndReadTheEchosFirstByte(t *testing.T, connection *mobile.TCPConnection, message string) {
	t.Helper()
	if err := connection.Send([]byte(message), timeoutMillis); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := connection.WaitAcknowledged(timeoutMillis); err != nil {
		t.Fatalf("WaitAcknowledged: %v", err)
	}
	if first, err := connection.Receive(1, timeoutMillis); err != nil || string(first.Data) != message[:1] {
		t.Fatalf("Receive = %v, %v; want the echo's first byte", first, err)
	}
}

func TestAClosedUDPConnectionSaysSo(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, settingsAllowing("echo.test:7"), nil)
	connection, err := link.OpenUDP("echo.test", 7, timeoutMillis)
	if err != nil {
		t.Fatalf("OpenUDP: %v", err)
	}

	closeConnection(t, connection.Close)

	sendErr := connection.Send([]byte("too late"), timeoutMillis)
	_, receiveErr := connection.Receive(64, timeoutMillis)
	for name, err := range map[string]error{"Send": sendErr, "Receive": receiveErr} {
		if details := mobile.AsSatlinkError(err); details == nil || details.Reason != "the connection is closed" {
			t.Errorf("%s after Close fails with %v, want \"the connection is closed\"", name, err)
		}
	}
	closeConnection(t, connection.Close)
}

// moreThanTheSendBufferHolds is twice what satnet's default TCP send
// buffer holds, 4 KiB, which the link drains at 300 bytes a second.
const moreThanTheSendBufferHolds = 8 * 1024

// roomForTheRestMillis is long enough for the send buffer to make room for
// the rest of moreThanTheSendBufferHolds, some 15 seconds at 300 bytes a
// second: a SendSome that waited for that room would queue all of it.
const roomForTheRestMillis = 60_000

func TestSendSomeQueuesWhatFitsAndTimesOutOnlyWhenNothingDoes(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	settings := settingsAllowing("echo.test:7")
	// What is still queued when the test ends need not arrive.
	settings.CloseGraceSeconds = 1
	connection := openEcho(t, establish(t, transport, settings, nil))
	data := make([]byte, moreThanTheSendBufferHolds)

	queued, err := connection.SendSome(data, roomForTheRestMillis)
	_, fullErr := connection.SendSome(data[queued:], 0)

	if err != nil || queued <= 0 || queued >= int64(len(data)) {
		t.Errorf("SendSome = %d, %v, want part of the %d bytes queued", queued, err, len(data))
	}
	if details := mobile.AsSatlinkError(fullErr); details == nil || details.Kind != mobile.SatlinkErrorTimeout || details.Command != "TCP write" {
		t.Errorf("SendSome with the send buffer full fails with %v, want SatlinkErrorTimeout for TCP write", fullErr)
	}
}

func TestReceiveRefusesALengthItCannotHonour(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, settingsAllowing("echo.test:7"), nil)
	tcp := openEcho(t, link)
	udp := openUDPEcho(t, link)

	// A stream's Receive of nothing could not tell data from the end of the
	// stream; a datagram's takes a datagram (TestUDPDatagramsAreEchoed).
	_, tcpErr := tcp.Receive(0, timeoutMillis)
	_, udpErr := udp.Receive(-1, timeoutMillis)

	for reason, err := range map[string]error{"maxLen must be at least 1": tcpErr, "maxLen must not be negative": udpErr} {
		if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorInvalidArgument || details.Reason != reason {
			t.Errorf("Receive fails with %v, want SatlinkErrorInvalidArgument: %s", err, reason)
		}
	}
}

func TestAReceiveWithNothingArrivedTimesOut(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, settingsAllowing("echo.test:7"), nil)
	tcp := openEcho(t, link)
	udp := openUDPEcho(t, link)

	_, tcpErr := tcp.Receive(64, 0)
	_, udpErr := udp.Receive(64, 0)

	for operation, err := range map[string]error{"TCP read": tcpErr, "UDP receive": udpErr} {
		if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorTimeout || details.Command != operation {
			t.Errorf("Receive fails with %v, want SatlinkErrorTimeout for %s", err, operation)
		}
	}
	if err := tcp.WaitAcknowledged(timeoutMillis); err != nil {
		t.Errorf("WaitAcknowledged with nothing sent: %v", err)
	}
	if err := tcp.ShutdownWrite(); err != nil {
		t.Errorf("ShutdownWrite: %v", err)
	}
}

func TestUDPDatagramsAreEchoed(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, settingsAllowing("echo.test:7"), nil)
	connection, err := link.OpenUDP("echo.test", 7, timeoutMillis)
	if err != nil {
		t.Fatalf("OpenUDP: %v", err)
	}
	defer closeConnection(t, connection.Close)
	// In order: a datagram that a Receive took is not received again.
	cases := []struct {
		sent   string
		maxLen int
		want   string
	}{
		// As in the Rust bindings: taken, and none of it kept.
		{sent: "arrived", maxLen: 0, want: ""},
		{sent: "ping", maxLen: 64, want: "ping"},
		{sent: "truncated", maxLen: 5, want: "trunc"},
		{sent: "", maxLen: 64, want: ""},
	}
	for _, c := range cases {
		if err := connection.Send([]byte(c.sent), timeoutMillis); err != nil {
			t.Fatalf("Send(%q): %v", c.sent, err)
		}

		datagram, err := connection.Receive(c.maxLen, timeoutMillis)

		if err != nil || string(datagram.Data) != c.want {
			t.Errorf("sent %q, received %v, %v, want %q", c.sent, datagram, err, c.want)
		}
	}
	if connection.MaxDatagramLen() != 548 || connection.PeerAddress() != "192.0.2.7:7" {
		t.Errorf("datagrams of up to %d bytes to %s, want 548 to 192.0.2.7:7", connection.MaxDatagramLen(), connection.PeerAddress())
	}
}

// secureEcho is what the TLS test sends to the simulator's TLS echo.
const secureEcho = "ticket?"

// secureSettings returns settings that may reach the simulator's TLS echo,
// at secure.test, which they answer locally.
func secureSettings() *mobile.LinkSettings {
	settings := settingsAllowing("secure.test:443")
	settings.AddHost("secure.test", "192.0.2.43")
	return settings
}

// secureLink returns a link with secureSettings.
func secureLink(t *testing.T) *mobile.SatelliteLink {
	t.Helper()
	transport, _ := simulatedUSB(t)
	return establish(t, transport, secureSettings(), nil)
}

// testCAConnector returns a connector that trusts the simulator's test CA
// only, and requires the server's key to be pinned.
func testCAConnector(t *testing.T, pinned []byte) *mobile.TLSConnector {
	t.Helper()
	settings := mobile.NewTLSSettings()
	settings.TrustPublicRoots = false
	settings.AddRootCertificatesPEM(sim.SecureTestCAPEM())
	settings.AddPinnedPublicKeySHA256(pinned)
	connector, err := mobile.NewTLSConnector(settings)
	if err != nil {
		t.Fatalf("NewTLSConnector: %v", err)
	}
	return connector
}

// openUDPEcho opens a UDP connection to the simulator's echo service, which
// link must allow, and closes it at the test's end, before the link hangs
// up.
func openUDPEcho(t *testing.T, link *mobile.SatelliteLink) *mobile.UDPConnection {
	t.Helper()
	connection, err := link.OpenUDP("echo.test", 7, timeoutMillis)
	if err != nil {
		t.Fatalf("OpenUDP: %v", err)
	}
	closeAtCleanup(t, connection.Close)
	return connection
}

// Go only: PORTING section 2 has the tests' connection helpers close their
// connections, so that no test leaves one to satnet's runtime cleanup,
// which runs only when garbage is collected.
func TestTheConnectionHelpersCloseTheirConnectionsAtTheTestsEnd(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	settings := secureSettings()
	settings.Allow("echo.test:7")
	link := establish(t, transport, settings, nil)
	pin := sim.SecureTestKeySHA256()
	var tcp *mobile.TCPConnection
	var udp *mobile.UDPConnection
	var secure *mobile.TLSConnection

	t.Run("a test that opens them", func(t *testing.T) {
		tcp, udp, secure = openEcho(t, link), openUDPEcho(t, link), openTLS(t, link, testCAConnector(t, pin[:]))
	})

	_, tcpErr := tcp.Receive(64, 0)
	_, udpErr := udp.Receive(64, 0)
	_, tlsErr := secure.Receive(64, 0)
	for name, err := range map[string]error{"TCP": tcpErr, "UDP": udpErr, "TLS": tlsErr} {
		if details := mobile.AsSatlinkError(err); details == nil || details.Reason != "the connection is closed" {
			t.Errorf("the %s connection's Receive fails with %v, want \"the connection is closed\"", name, err)
		}
	}
}

// openTLS opens a TLS connection to the simulator's TLS echo, and closes it
// at the test's end, before the link hangs up.
func openTLS(t *testing.T, link *mobile.SatelliteLink, connector *mobile.TLSConnector) *mobile.TLSConnection {
	t.Helper()
	connection, err := link.OpenTLS("secure.test", 443, connector, timeoutMillis)
	if err != nil {
		t.Fatalf("OpenTLS: %v", err)
	}
	closeAtCleanup(t, connection.Close)
	return connection
}

// echoAndSayGoodbye sends message through the TLS echo, ends our side of
// the session and returns everything the echo sends back before it ends
// its own.
func echoAndSayGoodbye(t *testing.T, connection *mobile.TLSConnection, message string) string {
	t.Helper()
	if err := connection.Send([]byte(message), timeoutMillis); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return sayGoodbye(t, connection)
}

// sayGoodbye waits until what was sent through the TLS echo has arrived,
// ends our side of the session and returns everything the echo sends back
// before it ends its own.
func sayGoodbye(t *testing.T, connection *mobile.TLSConnection) string {
	t.Helper()
	if err := connection.WaitAcknowledged(timeoutMillis); err != nil {
		t.Fatalf("WaitAcknowledged: %v", err)
	}
	if err := connection.ShutdownWrite(timeoutMillis); err != nil {
		t.Fatalf("ShutdownWrite: %v", err)
	}
	return receiveUntilFinished(t, connection.Receive)
}

func TestATLSConnectionEchoesAndTheNextResumesItsSession(t *testing.T) {
	t.Parallel()
	link := secureLink(t)
	pin := sim.SecureTestKeySHA256()
	connector := testCAConnector(t, pin[:])

	first := openTLS(t, link, connector)
	if first.Resumed() || first.ALPNProtocol() != "" || first.PeerAddress() != "192.0.2.43:443" {
		t.Errorf("first connection: resumed %t, ALPN %q, peer %s", first.Resumed(), first.ALPNProtocol(), first.PeerAddress())
	}
	// TLS 1.3 tickets follow the handshake; reading takes them in.
	if echoed := echoAndSayGoodbye(t, first, secureEcho); echoed != secureEcho {
		t.Errorf("the TLS echo returned %q, want %q", echoed, secureEcho)
	}
	closeConnection(t, first.Close)

	second := openTLS(t, link, connector)

	if !second.Resumed() {
		t.Error("the second connection did not resume the session")
	}
	closeConnection(t, second.Close)
	closeConnection(t, second.Close)
	if details := mobile.AsSatlinkError(second.ShutdownWrite(timeoutMillis)); details == nil || details.Reason != "the connection is closed" {
		t.Errorf("ShutdownWrite after Close fails with %v, want \"the connection is closed\"", details)
	}
}

func TestSendSomeOverTLSSaysHowMuchTheSessionTook(t *testing.T) {
	t.Parallel()
	pin := sim.SecureTestKeySHA256()
	connection := openTLS(t, secureLink(t), testCAConnector(t, pin[:]))
	defer closeConnection(t, connection.Close)

	taken, err := connection.SendSome([]byte(secureEcho), timeoutMillis)

	if err != nil || taken != int64(len(secureEcho)) {
		t.Fatalf("SendSome = %d, %v, want all %d bytes taken", taken, err, len(secureEcho))
	}
	if echoed := sayGoodbye(t, connection); echoed != secureEcho {
		t.Errorf("the TLS echo returned %q, want %q", echoed, secureEcho)
	}
}

func TestAbortResetsATLSConnection(t *testing.T) {
	t.Parallel()
	pin := sim.SecureTestKeySHA256()
	connection := openTLS(t, secureLink(t), testCAConnector(t, pin[:]))
	defer closeConnection(t, connection.Close)

	if err := connection.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	_, err := connection.Receive(64, timeoutMillis)
	assertKind(t, err, mobile.SatlinkErrorConnectionReset)
	// No close_notify can go any more.
	assertKind(t, connection.ShutdownWrite(timeoutMillis), mobile.SatlinkErrorConnectionReset)
}

// A Send that Close finds still waiting for room: far more data than the
// link carries meanwhile, with a timeout that a Close waiting for it would
// show.
const (
	sendInProgressBytes  = 30_000
	sendInProgressMillis = 20_000
)

// underWay is how far DataBytesFromApp grows before a test takes a
// transfer to be on the line: more than the acknowledgements an idle
// connection sends.
const underWay = 500

// closeAtOnce bounds a Close that must not wait: far longer than it takes,
// far shorter than any wait it must not do.
const closeAtOnce = time.Second

// TestClosingATLSConnectionDoesNotWaitForASendInProgress runs alone, as it
// times Close, and only without -short, as getting the send under way takes
// seconds.
func TestClosingATLSConnectionDoesNotWaitForASendInProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("times Close, after seconds of a send getting under way")
	}
	transport, control := simulatedUSB(t)
	settings := secureSettings()
	// What is still queued when the test ends need not arrive.
	settings.CloseGraceSeconds = 1
	pin := sim.SecureTestKeySHA256()
	connection := openTLS(t, establish(t, transport, settings, nil), testCAConnector(t, pin[:]))
	before := control.Stats().DataBytesFromApp
	sending := make(chan error, 1)
	go func() {
		sending <- connection.Send(make([]byte, sendInProgressBytes), sendInProgressMillis)
	}()
	simtest.Eventually(t, patience, "the send is under way", func() bool {
		return control.Stats().DataBytesFromApp > before+underWay
	})

	started := time.Now()
	closeConnection(t, connection.Close)
	took := time.Since(started)

	if took > closeAtOnce {
		t.Errorf("Close took %v, waiting for the send in progress", took)
	}
	select {
	case err := <-sending:
		// Woken by Close, the send fails with a timeout or with the closed
		// connection, as Close's deadline or its closing reaches it first.
		if err == nil {
			t.Error("the send in progress succeeded after Close")
		}
	case <-time.After(patience):
		t.Error("the send in progress went on after Close")
	}
}

func TestATLSServerWhoseKeyIsNotPinnedIsRefused(t *testing.T) {
	t.Parallel()
	link := secureLink(t)
	connector := testCAConnector(t, make([]byte, 32))

	_, err := link.OpenTLS("secure.test", 443, connector, timeoutMillis)

	if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorTLS || !strings.Contains(details.Reason, "not pinned") {
		t.Errorf("OpenTLS fails with %v, want SatlinkErrorTLS: the public key is not pinned", err)
	}
}
