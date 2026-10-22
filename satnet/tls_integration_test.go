package satnet_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/satnet"
	"github.com/anhydrous99/satlink/sim"
)

// TLS end to end: crypto/tls in the app, over the satellite link, against
// the simulator's secure.test, whose certificate comes from a test CA.

// The TLS echo service.
const (
	secureHost = "secure.test"
	securePort = 443
)

// tlsTimeout bounds the TLS operations the tests expect to succeed.
const tlsTimeout = 20 * time.Second

// unthrottledRate is the pacing of links over the simulator's unthrottled
// line: pacing to Iridium's rate would only slow the tests down.
const unthrottledRate = 50_000

// tlsLink returns a link to secure.test over a simulated handset.
func tlsLink(t *testing.T) *satnet.Link {
	t.Helper()
	link, _ := tlsLinkOver(t, sim.InstantLinkProfile(), unthrottledRate)
	return link
}

// tlsLinkOver returns a link to secure.test, and echo.test's echo, over a
// simulated handset whose line is profile, paced to linkRate bytes per
// second, and the simulator's control.
func tlsLinkOver(t *testing.T, profile sim.LinkProfile, linkRate int) (*satnet.Link, *sim.Control) {
	t.Helper()
	config := linkConfig(t, "secure.test:443", "echo.test:7")
	config.Tuning.LinkRate = linkRate
	// A test that fills the session leaves records the echo never takes.
	config.Tuning.CloseGrace = briefCloseGrace
	handset, control := simtest.NewHandset(t, simulatorWith(profile))
	return establish(t, handset, config), control
}

// lineOf returns a link to secure.test over a line of bytesPerSecond each
// way, and pacing to match.
func lineOf(t *testing.T, bytesPerSecond int) *satnet.Link {
	t.Helper()
	link, _ := tlsLinkOver(t, sim.LinkProfile{BytesPerSecond: bytesPerSecond}, bytesPerSecond)
	return link
}

// readExactlyBy reads n bytes from conn by deadline; it may run outside the
// test's goroutine.
func readExactlyBy(conn *satnet.TLSConn, n int, deadline time.Time) ([]byte, error) {
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	received := make([]byte, n)
	read, err := io.ReadFull(conn, received)
	if err != nil {
		return nil, fmt.Errorf("%w after %d bytes", err, read)
	}
	return received, nil
}

// readInBackground reads n bytes from conn by deadline in another
// goroutine, and returns the channel what it read arrives on.
func readInBackground(t *testing.T, conn *satnet.TLSConn, n int, deadline time.Time) <-chan []byte {
	t.Helper()
	received := make(chan []byte, 1)
	go func() {
		data, err := readExactlyBy(conn, n, deadline)
		if err != nil {
			t.Error(err)
		}
		received <- data
	}()
	return received
}

// testCA returns a connector that trusts the simulator's test CA, and
// nothing else, with pins.
func testCA(t *testing.T, pins ...[sha256.Size]byte) *satnet.TLSConnector {
	t.Helper()
	connector, err := satnet.NewTLSConnector(satnet.TLSOptions{
		ExtraRootsPEM:      [][]byte{[]byte(sim.SecureTestCAPEM())},
		WithoutPublicRoots: true,
		PinnedPublicKeys:   pins,
	})
	if err != nil {
		t.Fatal(err)
	}
	return connector
}

// dialSecure opens a TLS connection to secure.test.
func dialSecure(link *satnet.Link, connector *satnet.TLSConnector) (*satnet.TLSConn, error) {
	return link.DialTLS(secureHost, securePort, connector, tlsTimeout)
}

// mustDialSecure opens a TLS connection to secure.test, failing the test
// otherwise; the test's end closes it.
func mustDialSecure(t *testing.T, link *satnet.Link, connector *satnet.TLSConnector) *satnet.TLSConn {
	t.Helper()
	conn, err := dialSecure(link, connector)
	if err != nil {
		t.Fatalf("DialTLS: %v", err)
	}
	t.Cleanup(func() {
		// The test is over; a goodbye that fails changes nothing.
		_ = conn.Close()
	})
	return conn
}

// setWriteDeadline sets conn's write deadline after, failing the test
// otherwise.
func setWriteDeadline(t *testing.T, conn *satnet.TLSConn, after time.Duration) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(after)); err != nil {
		t.Fatal(err)
	}
}

func TestEchoesThroughATLSSession(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	message := pattern(3_000)

	expectEcho(t, conn, message)

	if err := conn.WaitAcknowledged(time.Now().Add(tlsTimeout)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
}

func TestAServerTheConnectorDoesNotTrustIsRefused(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	publicRootsOnly, err := satnet.NewTLSConnector(satnet.TLSOptions{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = dialSecure(link, publicRootsOnly)

	expectKind(t, err, satnet.ErrTLS)
}

func TestAPinnedKeyMustMatch(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)

	if _, err := dialSecure(link, testCA(t, sim.SecureTestKeySHA256())); err != nil {
		t.Errorf("the pinned key is refused: %v", err)
	}
	_, err := dialSecure(link, testCA(t, [sha256.Size]byte{}))
	expectKind(t, err, satnet.ErrTLS)
}

func TestASecondConnectionResumesTheSession(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	connector := testCA(t)
	first := mustDialSecure(t, link, connector)
	if first.Resumed() {
		t.Fatal("the first connection resumed")
	}
	// TLS 1.3 tickets follow the handshake; reading takes them in.
	expectEcho(t, first, []byte("ticket?"))
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := mustDialSecure(t, link, connector)

	if !second.Resumed() {
		t.Error("the second connection did not resume")
	}
}

func TestOneGoroutineReadsWhileAnotherWritesTLS(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	message := bytes.Repeat([]byte{0x5A}, 4_000)
	received := make(chan []byte, 1)
	go func() {
		echoed, err := readExactly(conn, len(message))
		if err != nil {
			t.Error(err)
		}
		received <- echoed
	}()
	// A fixed 200 ms sleep would have the reader waiting by the time the
	// writer starts; AwaitWaitingCall waits for exactly that.
	satnet.AwaitWaitingCall(t, link, timeout)

	send(t, conn, message)

	if echoed := <-received; !bytes.Equal(echoed, message) {
		t.Error("the echo differs from the message")
	}
}

func TestATLSWriteWaitingForRoomGoesOutWholeBeforeAnother(t *testing.T) {
	t.Parallel()
	link, control := tlsLinkOver(t, sim.InstantLinkProfile(), unthrottledRate)
	conn := mustDialSecure(t, link, testCA(t))
	setWriteDeadline(t, conn, tlsTimeout)

	// More than the session's 64 KiB of records hold.
	expectAHeartbeatAfterTheBulk(t, control, conn, bytes.Repeat([]byte{'A'}, 100_000))
}

func TestDataAlreadyReceivedIsReturnedAfterTheDeadline(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	send(t, conn, []byte("ready"))
	if err := conn.WaitAcknowledged(time.Now().Add(tlsTimeout)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 5)
	received := 0

	simtest.Eventually(t, timeout, "the echo arrives", func() bool {
		// A deadline that has already passed: only what is here comes back.
		if err := conn.SetReadDeadline(time.Now()); err != nil {
			t.Fatal(err)
		}
		count, err := conn.Read(buffer)
		if err != nil && !errors.Is(err, satnet.ErrTimedOut) {
			t.Fatal(err)
		}
		received = count
		return err == nil
	})

	if received == 0 || !bytes.HasPrefix([]byte("ready"), buffer[:received]) {
		t.Errorf("received %q", buffer[:received])
	}
}

func TestAReaderKeepsSendingWhatATimedOutWriteLeft(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	// Nobody reads the echo yet, so the server stops reading too.
	message := bytes.Repeat([]byte{0x5A}, 30_000)
	setWriteDeadline(t, conn, time.Second)

	_, err := conn.Write(message)

	expectKind(t, err, satnet.ErrTimedOut)
	// The session took it all; the server answers only what reaches it.
	if echoed := receiveExactly(t, conn, len(message)); !bytes.Equal(echoed, message) {
		t.Error("the echo differs from the message")
	}
}

// TestAWriteThatTimesOutCountsWhatTheSessionTook checks that a write which
// times out returns the count of what the session took, with the error.
func TestAWriteThatTimesOutCountsWhatTheSessionTook(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	setWriteDeadline(t, conn, time.Second)

	taken, err := conn.Write(bytes.Repeat([]byte{0x5A}, 30_000))

	if err != nil && !errors.Is(err, satnet.ErrTimedOut) || taken == 0 {
		t.Fatalf("Write = %d, %v; want part of it taken", taken, err)
	}
	receiveExactly(t, conn, taken)
	if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, satnet.ErrTimedOut) {
		t.Errorf("Read = %v, want a timeout: nothing was sent twice", err)
	}
}

func TestAConnectionThatOnlySendsStillLetsTheNextResume(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	connector := testCA(t)
	first := mustDialSecure(t, link, connector)
	send(t, first, []byte("report"))
	if err := first.WaitAcknowledged(time.Now().Add(tlsTimeout)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := mustDialSecure(t, link, connector)

	if !second.Resumed() {
		t.Error("the second connection did not resume")
	}
}

func TestAFullSessionMakesAWriteWaitRatherThanFail(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	for _, size := range []int{30_000, 100_000} {
		setWriteDeadline(t, conn, time.Second)
		_, err := conn.Write(make([]byte, size))
		expectKind(t, err, satnet.ErrTimedOut)
	}
	setWriteDeadline(t, conn, 3*time.Second)

	_, err := conn.Write([]byte("x"))

	if err != nil && !errors.Is(err, satnet.ErrTimedOut) {
		t.Errorf("Write = %v, want it taken or a timeout", err)
	}
}

func TestAGoodbyeOutlastsCryptoTLSsFiveSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("sends 6000 bytes at Iridium's 300 B/s")
	}
	// The default tuning paces what the app sends at Iridium's 300 B/s,
	// which is what the goodbye waits for.
	link, _ := newLink(t, "secure.test:443")
	conn := mustDialSecure(t, link, testCA(t))
	message := bytes.Repeat([]byte{0x42}, 6_000)
	setWriteDeadline(t, conn, time.Second)
	// What TCP has no room for waits in the session.
	if _, err := conn.Write(message); !errors.Is(err, satnet.ErrTimedOut) {
		t.Fatalf("Write = %v, want a timeout with records still waiting", err)
	}
	setWriteDeadline(t, conn, 30*time.Second)
	started := time.Now()

	if err := conn.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() = %v", err)
	}

	// At 300 B/s, the records still waiting take more than five seconds.
	if elapsed := time.Since(started); elapsed < 5*time.Second {
		t.Errorf("the goodbye left after %v; the test did not wait past five seconds", elapsed)
	}
	if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// The echo answers close_notify with its own: the end, not a truncation.
	echoed, err := io.ReadAll(conn)
	if err != nil || len(echoed) != len(message) {
		t.Errorf("read %d bytes, then %v; want %d, then the end", len(echoed), err, len(message))
	}
}

func TestSecureTakesOverATCPConnection(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn, err := link.Dial(secureHost, securePort, tlsTimeout)
	if err != nil {
		t.Fatal(err)
	}

	secure, err := testCA(t).Secure(conn, secureHost, tlsTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The test is over; a goodbye that fails changes nothing.
		_ = secure.Close()
	})

	expectEcho(t, secure, []byte("over a TCP connection of my own"))
	if secure.PeerAddr() != conn.PeerAddr() {
		t.Errorf("PeerAddr() = %v, want %v", secure.PeerAddr(), conn.PeerAddr())
	}
}

func TestAWaitingReadGetsTheAnswerAnotherCallTookIn(t *testing.T) {
	t.Parallel()
	// The answer comes with the acknowledgement of the request, so the
	// writer's WaitAcknowledged may take it into the session first.
	link, _ := tlsLinkOver(t, sim.LinkProfile{OneWayDelay: 150 * time.Millisecond}, unthrottledRate)
	conn := mustDialSecure(t, link, testCA(t))
	for round := range 20 {
		request := []byte(fmt.Sprintf("request %d\n", round))
		answer := readInBackground(t, conn, len(request), time.Now().Add(3*time.Second))
		// The reader is waiting for the server by now.
		satnet.AwaitWaitingCall(t, link, timeout)

		send(t, conn, request)
		if err := conn.WaitAcknowledged(time.Now().Add(timeout)); err != nil {
			t.Fatal(err)
		}

		if got := <-answer; !bytes.Equal(got, request) {
			t.Fatalf("round %d: the answer is %q, want %q", round, got, request)
		}
	}
}

func TestAWaitingReadSendsOnWhatATimedOutWriteLeft(t *testing.T) {
	if testing.Short() {
		t.Skip("echoes 12 000 bytes at 3000 B/s")
	}
	link := lineOf(t, 3_000)
	conn := mustDialSecure(t, link, testCA(t))
	message := bytes.Repeat([]byte{0x5A}, 12_000)
	started := time.Now()
	echoed := readInBackground(t, conn, len(message), time.Now().Add(30*time.Second))
	// The reader is waiting for the server by now.
	satnet.AwaitWaitingCall(t, link, timeout)
	setWriteDeadline(t, conn, 2*time.Second)

	_, err := conn.Write(message)

	expectKind(t, err, satnet.ErrTimedOut)
	// The server answers only once the whole record has reached it.
	if got := <-echoed; !bytes.Equal(got, message) {
		t.Error("the echo differs from the message")
	}
	if elapsed := time.Since(started); elapsed >= 20*time.Second {
		t.Errorf("the rest of the record waited %v", elapsed)
	}
}

func TestSendingAgainJustWhatWasNotTakenDeliversAMessageOnce(t *testing.T) {
	t.Parallel()
	link := lineOf(t, 30_000)
	conn := mustDialSecure(t, link, testCA(t))
	message := pattern(200_000)
	echoed := make(chan []byte, 1)
	go func() {
		received, err := readExactly(conn, len(message))
		if err != nil {
			t.Error(err)
		}
		echoed <- received
	}()

	timeouts := 0
	for rest := message; len(rest) > 0; {
		setWriteDeadline(t, conn, 50*time.Millisecond)
		taken, err := conn.WriteSome(rest)
		rest = rest[taken:]
		switch {
		case errors.Is(err, satnet.ErrTimedOut) && taken == 0:
			timeouts++
		case err != nil:
			t.Fatalf("WriteSome = %d, %v; a timeout must take nothing", taken, err)
		}
	}

	if timeouts == 0 {
		t.Error("the session never filled up")
	}
	if got := <-echoed; !bytes.Equal(got, message) {
		t.Error("the echo differs from the message")
	}
}

func TestAWriteAfterShutdownDoesNotCutOffWhatIsLeftToRead(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	send(t, conn, []byte("last words\n"))
	setWriteDeadline(t, conn, timeout)
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	// Racing the shutdown, say from a heartbeat goroutine: it cannot be
	// sent.
	if _, err := conn.Write([]byte("heartbeat\n")); err == nil {
		t.Error("a write after the shutdown was taken")
	}

	if got := receiveExactly(t, conn, 11); string(got) != "last words\n" {
		t.Errorf("received %q", got)
	}
	if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("Read = %v, want io.EOF: the server said goodbye", err)
	}
}

// TestAWaitingReadGetsWhatAPollingReadTookIn checks that a waiting read gets
// what a polling read took in. Reads are serialised, so no read need poll:
// the first read waits for the server holding the
// session's read, and the second waits behind it. The first takes the
// records in but returns a byte of them; the second must get the rest from
// the session, rather than wait for it in TCP.
func TestAWaitingReadGetsWhatAPollingReadTookIn(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	const echoed = 3_000
	for round := range 5 {
		// The deadline only bounds a read that would wait for good: the
		// test checks which read gets the bytes, not how fast they come.
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			t.Fatal(err)
		}
		first := readInBackgroundUpTo(conn, 1)
		// The first read waits for the server by now.
		satnet.AwaitWaitingCall(t, link, timeout)
		second := readInBackgroundUpTo(conn, 1_000)

		send(t, conn, bytes.Repeat([]byte{0x5A}, echoed))

		took, waited := <-first, <-second
		if took.err != nil || waited.err != nil {
			t.Fatalf("round %d: the first read: %v; the second: %v", round, took.err, waited.err)
		}
		if waited.read < 1 {
			t.Fatalf("round %d: the waiting read got nothing", round)
		}
		receiveExactly(t, conn, echoed-took.read-waited.read)
	}
}

// readResult is what a Read returned.
type readResult struct {
	read int
	err  error
}

// readInBackgroundUpTo reads up to n bytes from conn in another goroutine,
// and returns the channel what the Read returned arrives on.
func readInBackgroundUpTo(conn *satnet.TLSConn, n int) <-chan readResult {
	result := make(chan readResult, 1)
	go func() {
		read, err := conn.Read(make([]byte, n))
		result <- readResult{read, err}
	}()
	return result
}

func TestWhatWasAcknowledgedBeforeTheConnectionWasResetWasDelivered(t *testing.T) {
	t.Parallel()
	link, control := tlsLinkOver(t, sim.InstantLinkProfile(), unthrottledRate)
	conn := mustDialSecure(t, link, testCA(t))
	probe := dial(t, link, "echo.test", echoPort)
	expectEcho(t, conn, []byte("report\n"))
	if err := conn.WaitAcknowledged(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}

	// A new address resets the connection, with nothing left on its way.
	newAddress := netip.AddrFrom4([4]byte{10, 64, 0, 3})
	if err := control.RestartPPP(newAddress); err != nil {
		t.Fatal(err)
	}
	simtest.Eventually(t, timeout, "IPCP finished again", func() bool {
		ipv4 := link.Status().IPv4
		return ipv4 != nil && ipv4.LocalAddress == newAddress
	})
	// The connections are reset when they next look at their address, all
	// in the same pass: the probe tells when that was.
	simtest.Eventually(t, timeout, "the connections on the old address are reset", func() bool {
		return isReset(t, probe)
	})

	if err := conn.WaitAcknowledged(time.Now().Add(timeout)); err != nil {
		t.Errorf("WaitAcknowledged() = %v, want the report delivered", err)
	}
}

// isReset reports whether conn, which has nothing to read, has been reset.
func isReset(t *testing.T, conn *satnet.TCPConn) bool {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := conn.Read(make([]byte, 1))
	return errors.Is(err, satnet.ErrConnectionReset)
}

func TestAbortResetsTheConnectionAndWakesAWaitingRead(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	read := inBackground(func() error {
		_, err := conn.Read(make([]byte, 100))
		return err
	})
	satnet.AwaitWaitingCall(t, link, timeout)

	if err := conn.Abort(); err != nil {
		t.Fatal(err)
	}

	expectKind(t, awaitError(t, read), satnet.ErrConnectionReset)
	_, err := conn.Write([]byte("more\n"))
	expectKind(t, err, satnet.ErrConnectionReset)
}

func TestAbortWakesATLSWriteWaitingForRoom(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	setWriteDeadline(t, conn, tlsTimeout)
	// Nobody reads the echo, so the server soon stops reading too: the
	// write waits for room that never comes.
	written := inBackground(func() error {
		_, err := conn.Write(make([]byte, 200_000))
		return err
	})
	satnet.AwaitWaitingCall(t, link, timeout)

	if err := conn.Abort(); err != nil {
		t.Fatal(err)
	}

	expectKind(t, awaitError(t, written), satnet.ErrConnectionReset)
}

func TestAbortWakesATLSCloseWriteWaitingBehindRecords(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	// Nobody reads the echo, so the server stops reading: what this write
	// took waits in the session, and the goodbye behind it.
	setWriteDeadline(t, conn, 500*time.Millisecond)
	if _, err := conn.Write(make([]byte, 200_000)); !errors.Is(err, satnet.ErrTimedOut) {
		t.Fatalf("Write = %v, want a timeout with records still waiting", err)
	}
	setWriteDeadline(t, conn, tlsTimeout)
	closed := inBackground(conn.CloseWrite)
	satnet.AwaitWaitingCall(t, link, timeout)

	if err := conn.Abort(); err != nil {
		t.Fatal(err)
	}

	expectKind(t, awaitError(t, closed), satnet.ErrConnectionReset)
}

func TestAWriteOnceTheGoodbyeIsQueuedIsRefused(t *testing.T) {
	t.Parallel()
	link := lineOf(t, 3_000)
	conn := mustDialSecure(t, link, testCA(t))
	// A backlog the goodbye has to wait behind.
	setWriteDeadline(t, conn, 500*time.Millisecond)
	if taken, err := conn.Write(bytes.Repeat([]byte{0x5A}, 12_000)); taken != 12_000 {
		t.Fatalf("Write = %d, %v; want all of it taken", taken, err)
	}
	setWriteDeadline(t, conn, 500*time.Millisecond)
	expectKind(t, conn.CloseWrite(), satnet.ErrTimedOut)

	// The server ignores whatever follows the close_notify. The write is
	// expected to fail with a reset, as TCP refuses a write after its own
	// shutdown.
	setWriteDeadline(t, conn, 200*time.Millisecond)
	_, err := conn.Write([]byte("after the goodbye\n"))
	expectKind(t, err, satnet.ErrConnectionReset)
}

func TestAWriteRefusedOnceTheLinkClosedLeavesDeliveryConfirmed(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	conn := mustDialSecure(t, link, testCA(t))
	send(t, conn, []byte("report\n"))
	if err := conn.WaitAcknowledged(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	link.RequestClose()
	expectClosed(t, link.Watcher(), satnet.CloseReasonRequested)

	if _, err := conn.Write([]byte("one more\n")); err == nil {
		t.Error("a write was taken once the link closed")
	}
	if err := conn.WaitAcknowledged(time.Now().Add(timeout)); err != nil {
		t.Errorf("WaitAcknowledged() = %v, want the report delivered", err)
	}
}

// drainingLine is the line and pacing rate of the link that
// TestAWriteRefusedWhileTheLinkDrainsLeavesDeliveryConfirmed closes: fast
// enough for a quick handshake, slow enough for its upload to outlast the
// close grace, which the link then drains for in full.
const drainingLine = 3_000

// TestAWriteRefusedWhileTheLinkDrainsLeavesDeliveryConfirmed checks that a
// write refused while the link drains leaves delivery confirmed. The test
// above writes once the link has closed, when the aborted connection refuses
// the write anyway; here the link still drains what was written before the
// close, and the connection still sends.
func TestAWriteRefusedWhileTheLinkDrainsLeavesDeliveryConfirmed(t *testing.T) {
	t.Parallel()
	config := linkConfig(t, "secure.test:443", "192.0.2.7:9")
	config.Tuning.LinkRate = drainingLine
	config.Tuning.CloseGrace = 3 * time.Second
	// Room for an upload of over five seconds, all queued at once.
	config.Tuning.TCPSendBuffer = 16 * 1024
	handset, _ := simtest.NewHandset(t, simulatorWith(sim.LinkProfile{BytesPerSecond: drainingLine}))
	link := establish(t, handset, config)
	watcher := link.Watcher()
	expectStateChange(t, watcher, satnet.LinkState{Kind: satnet.LinkStateUp})
	conn := mustDialSecure(t, link, testCA(t))
	send(t, conn, []byte("report\n"))
	if err := conn.WaitAcknowledged(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	upload := dial(t, link, "192.0.2.7", discardPort)
	send(t, upload, make([]byte, config.Tuning.TCPSendBuffer))
	link.RequestClose()
	expectStateChange(t, watcher, state(satnet.LinkStateClosing, satnet.CloseReasonRequested))
	// Still draining: the connections are not aborted yet.
	expectTimedOut(t, upload.WaitAcknowledged(time.Now()), "TCP flush")

	taken, err := conn.Write([]byte("one more\n"))

	if taken != 0 {
		t.Errorf("Write took %d bytes once the link was closing", taken)
	}
	expectLinkDown(t, err, satnet.CloseReasonRequested)
	if err := conn.WaitAcknowledged(time.Now().Add(timeout)); err != nil {
		t.Errorf("WaitAcknowledged() = %v, want the report delivered", err)
	}
}
