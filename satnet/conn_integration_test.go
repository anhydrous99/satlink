package satnet_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/satnet"
	"github.com/anhydrous99/satlink/sim"
)

// The connections as net.Conns — deadlines, Close waking blocked
// calls, use after Close — DialContext and net/http over the link, and the
// runtime cleanups of dropped links.

// awaitError returns the error a call running in another goroutine
// delivers, failing the test if it does not come within timeout.
func awaitError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(timeout):
		t.Fatal("the call never returned")
		return nil
	}
}

// inBackground runs call in another goroutine and returns the channel its
// error arrives on.
func inBackground(call func() error) <-chan error {
	result := make(chan error, 1)
	go func() { result <- call() }()
	return result
}

// readAByte reads a byte from conn.
func readAByte(conn net.Conn) error {
	_, err := conn.Read(make([]byte, 1))
	return err
}

// writeMoreThanItTakes writes far more on conn than its send buffer holds,
// which the link drains at 300 bytes a second.
func writeMoreThanItTakes(conn net.Conn) error {
	_, err := conn.Write(make([]byte, moreThanTheSendBufferHolds))
	return err
}

func TestADeadlineMovedWhileACallWaitsApplies(t *testing.T) {
	t.Parallel()
	dialEcho := func(t *testing.T, link *satnet.Link) net.Conn { return dial(t, link, "echo.test", echoPort) }
	dialEchoUDP := func(t *testing.T, link *satnet.Link) net.Conn { return dialUDP(t, link, "echo.test", echoPort) }
	for _, c := range []struct {
		name      string
		operation string
		open      func(t *testing.T, link *satnet.Link) net.Conn
		wait      func(conn net.Conn) error
		move      func(conn net.Conn, deadline time.Time) error
	}{
		{"a TCP read", "TCP read", dialEcho, readAByte, net.Conn.SetReadDeadline},
		{"a TCP write", "TCP write", dialEcho, writeMoreThanItTakes, net.Conn.SetWriteDeadline},
		{"a UDP read", "UDP receive", dialEchoUDP, readAByte, net.Conn.SetReadDeadline},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			// A write that times out leaves its data queued.
			link := linkClosingBriefly(t, "echo.test:7")
			conn := c.open(t, link)
			waiting := inBackground(func() error { return c.wait(conn) })
			satnet.AwaitWaitingCall(t, link, timeout)

			if err := c.move(conn, time.Now()); err != nil {
				t.Fatal(err)
			}

			err := awaitError(t, waiting)
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Errorf("%s = %v, want a net.Error timeout", c.name, err)
			}
			expectTimedOut(t, err, c.operation)
		})
	}
}

func TestCloseWakesAWaitingRead(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)
	read := inBackground(func() error {
		_, err := conn.Read(make([]byte, 1))
		return err
	})
	satnet.AwaitWaitingCall(t, link, timeout)

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if err := awaitError(t, read); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Read = %v, want net.ErrClosed", err)
	}
}

func TestCloseWakesAWaitingWrite(t *testing.T) {
	t.Parallel()
	link := linkClosingBriefly(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)
	// Far more than the send buffer holds, at 300 B/s.
	write := inBackground(func() error {
		_, err := conn.Write(make([]byte, 64*1024))
		return err
	})
	satnet.AwaitWaitingCall(t, link, timeout)

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if err := awaitError(t, write); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Write = %v, want net.ErrClosed", err)
	}
}

func TestCloseWakesAWaitForAcknowledgement(t *testing.T) {
	t.Parallel()
	link := linkClosingBriefly(t, "echo.test:9")
	conn := dial(t, link, "echo.test", discardPort)
	// Over ten seconds of data at 300 B/s.
	send(t, conn, make([]byte, 4_000))
	acknowledged := inBackground(func() error { return conn.WaitAcknowledged(time.Time{}) })
	satnet.AwaitWaitingCall(t, link, timeout)

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if err := awaitError(t, acknowledged); !errors.Is(err, net.ErrClosed) {
		t.Errorf("WaitAcknowledged = %v, want net.ErrClosed", err)
	}
}

func TestCloseEndsAHandshakeOfCryptoTLSOverATCPConn(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "secure.test:443")
	conn := dial(t, link, secureHost, securePort)
	config := testCA(t).Config()
	config.ServerName = secureHost
	// At 300 B/s, the ClientHello alone takes about a second to leave.
	handshake := inBackground(tls.Client(conn, config).Handshake)
	satnet.AwaitWaitingCall(t, link, timeout)

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if err := awaitError(t, handshake); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Handshake = %v, want net.ErrClosed", err)
	}
}

func TestAClosedTCPConnRefusesEverything(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	_, readErr := conn.Read(make([]byte, 1))
	_, writeErr := conn.Write([]byte("x"))
	_, writeSomeErr := conn.WriteSome([]byte("x"))
	for name, err := range map[string]error{
		"Read":             readErr,
		"Write":            writeErr,
		"WriteSome":        writeSomeErr,
		"CloseWrite":       conn.CloseWrite(),
		"Abort":            conn.Abort(),
		"WaitAcknowledged": conn.WaitAcknowledged(time.Time{}),
		"SetDeadline":      conn.SetDeadline(time.Time{}),
		"SetReadDeadline":  conn.SetReadDeadline(time.Time{}),
		"SetWriteDeadline": conn.SetWriteDeadline(time.Time{}),
	} {
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("%s = %v, want net.ErrClosed", name, err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Errorf("a second Close = %v", err)
	}
}

func TestAbortResetsATCPConnectionAndWakesAWaitingRead(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)
	read := inBackground(func() error {
		_, err := conn.Read(make([]byte, 1))
		return err
	})
	satnet.AwaitWaitingCall(t, link, timeout)

	if err := conn.Abort(); err != nil {
		t.Fatal(err)
	}

	expectKind(t, awaitError(t, read), satnet.ErrConnectionReset)
	_, err := conn.Write([]byte("more"))
	expectKind(t, err, satnet.ErrConnectionReset)
}

// moreThanTheSendBufferHolds is far more than the default TCP send buffer,
// 4 KiB, holds, which the link drains at 300 bytes a second.
const moreThanTheSendBufferHolds = 64 * 1024

func TestWriteSomeQueuesWhatFitsWithoutWaitingForRoomForTheRest(t *testing.T) {
	t.Parallel()
	link := linkClosingBriefly(t, "echo.test:9")
	conn := dial(t, link, "echo.test", discardPort)
	data := make([]byte, moreThanTheSendBufferHolds)
	// Write would wait out this deadline for room for the rest, and fail.
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}

	queued, err := conn.WriteSome(data)

	if err != nil || queued == 0 || queued >= len(data) {
		t.Errorf("WriteSome = %d, %v; want part of the %d bytes queued, and no error", queued, err, len(data))
	}
}

func TestWriteSomeTimesOutOnlyWhenNothingFits(t *testing.T) {
	t.Parallel()
	link := linkClosingBriefly(t, "echo.test:9")
	conn := dial(t, link, "echo.test", discardPort)
	data := make([]byte, moreThanTheSendBufferHolds)
	if err := conn.SetWriteDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}

	queued, err := conn.WriteSome(data)
	more, fullErr := conn.WriteSome(data[queued:])

	if err != nil || queued == 0 {
		t.Errorf("WriteSome = %d, %v; want what fits queued at once", queued, err)
	}
	if more != 0 {
		t.Errorf("WriteSome with the send buffer full queued %d bytes", more)
	}
	expectKind(t, fullErr, satnet.ErrTimedOut)
}

func TestAWriteThatTimesOutCountsWhatItQueued(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, slowLink())
	config := linkConfig(t, "echo.test:9")
	config.Tuning.LinkRate = 3_000
	link := establish(t, handset, config)
	conn := dial(t, link, "echo.test", discardPort)
	message := pattern(6_000)
	// A deadline of now: Write queues what fits at once, then times out.
	if err := conn.SetWriteDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}

	queued, err := conn.Write(message)

	expectTimedOut(t, err, "TCP write")
	if queued == 0 || queued >= len(message) {
		t.Fatalf("Write = %d, want part of the %d bytes queued", queued, len(message))
	}
	// That part is still sent: written again, exactly the rest makes every
	// byte arrive once.
	send(t, conn, message[queued:])
	if err := conn.WaitAcknowledged(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	if discarded := control.Stats().DiscardedBytes; discarded != uint64(len(message)) {
		t.Errorf("the discard service received %d bytes, want %d", discarded, len(message))
	}
}

func TestAWriteWaitingForRoomGoesOutWholeBeforeAnother(t *testing.T) {
	t.Parallel()
	config := linkConfig(t, "echo.test:7")
	config.Tuning.LinkRate = unthrottledRate
	link, control := linkWith(t, config)
	conn := dial(t, link, "echo.test", echoPort)

	// Five times what the 4 KiB send buffer holds.
	expectAHeartbeatAfterTheBulk(t, control, conn, bytes.Repeat([]byte{'A'}, 20_000))
}

func TestAnEmptyWriteSomeQueuesNothing(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)

	queued, err := conn.WriteSome(nil)

	if queued != 0 || err != nil {
		t.Errorf("WriteSome(nil) = %d, %v; want 0, nil", queued, err)
	}
}

func TestAnEmptyReadOrWriteReturnsAtOnce(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)

	read, readErr := conn.Read(nil)
	written, writeErr := conn.Write(nil)

	if read != 0 || readErr != nil || written != 0 || writeErr != nil {
		t.Errorf("Read(nil) = %d, %v; Write(nil) = %d, %v; want 0, nil for both", read, readErr, written, writeErr)
	}
}

func TestAClosedUDPConnRefusesEverything(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	socket := dialUDP(t, link, "echo.test", echoPort)

	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}

	_, readErr := socket.Read(make([]byte, 1))
	_, writeErr := socket.Write([]byte("x"))
	for name, err := range map[string]error{
		"Read":             readErr,
		"Write":            writeErr,
		"SetDeadline":      socket.SetDeadline(time.Time{}),
		"SetReadDeadline":  socket.SetReadDeadline(time.Time{}),
		"SetWriteDeadline": socket.SetWriteDeadline(time.Time{}),
	} {
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("%s = %v, want net.ErrClosed", name, err)
		}
	}
}

func TestAfterCloseWriteTheConnectionStillReads(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)
	send(t, conn, []byte("last words"))

	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	if echoed := receiveExactly(t, conn, 10); string(echoed) != "last words" {
		t.Errorf("echoed %q", echoed)
	}
}

func TestAfterCloseWriteAWriteIsRefused(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	_, err := conn.Write([]byte("too late"))

	expectKind(t, err, satnet.ErrConnectionReset)
}

func TestTheConnectionsAddressesAreTheLinks(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)
	socket := dialUDP(t, link, "echo.test", echoPort)
	local := link.Status().IPv4.LocalAddress

	tcpLocal, tcpOK := conn.LocalAddr().(*net.TCPAddr)
	udpLocal, udpOK := socket.LocalAddr().(*net.UDPAddr)

	if !tcpOK || tcpLocal.AddrPort().Addr() != local || conn.RemoteAddr().String() != "192.0.2.7:7" {
		t.Errorf("TCP: %v to %v", conn.LocalAddr(), conn.RemoteAddr())
	}
	if !udpOK || udpLocal.AddrPort().Addr() != local || socket.RemoteAddr().String() != "192.0.2.7:7" ||
		socket.PeerAddr() != conn.PeerAddr() {
		t.Errorf("UDP: %v to %v", socket.LocalAddr(), socket.RemoteAddr())
	}
}

func TestDialContextGivesTheConnectionsOfEachNetwork(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	for network, want := range map[string]string{
		"tcp": "*satnet.TCPConn", "tcp4": "*satnet.TCPConn",
		"udp": "*satnet.UDPConn", "udp4": "*satnet.UDPConn",
	} {
		conn, err := link.DialContext(context.Background(), network, "echo.test:7")
		if err != nil {
			t.Errorf("DialContext(%s): %v", network, err)
			continue
		}
		if got := typeName(conn); got != want {
			t.Errorf("DialContext(%s) gives a %s", network, got)
		}
		// Closing a connection of the link cannot fail.
		_ = conn.Close()
	}
}

// typeName names the dynamic type of conn.
func typeName(conn net.Conn) string {
	switch conn.(type) {
	case *satnet.TCPConn:
		return "*satnet.TCPConn"
	case *satnet.UDPConn:
		return "*satnet.UDPConn"
	default:
		return "another type"
	}
}

func TestDialContextRefusesWhatNetDialerRefuses(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")

	_, err := link.DialContext(context.Background(), "tcp6", "echo.test:7")
	var unknown net.UnknownNetworkError
	if !errors.As(err, &unknown) {
		t.Errorf("tcp6: %v, want net.UnknownNetworkError", err)
	}
	for _, address := range []string{"echo.test", "echo.test:echo", "echo.test:70000"} {
		_, err := link.DialContext(context.Background(), "tcp", address)
		var addrErr *net.AddrError
		if !errors.As(err, &addrErr) {
			t.Errorf("%q: %v, want a *net.AddrError", address, err)
		}
	}
	_, err = link.DialContext(context.Background(), "tcp", "web.test:80")
	expectKind(t, err, satnet.ErrPolicyDenied)
}

// expectDialContextError checks that err is net.Dialer's error for a
// context that ended with cause.
func expectDialContextError(t *testing.T, err, cause error) {
	t.Helper()
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" || opErr.Net != "tcp" || !errors.Is(err, cause) {
		t.Errorf("DialContext = %v, want a dial *net.OpError for %v", err, cause)
	}
}

func TestDialContextGivesUpWhenItsContextIsCancelled(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, delayedLink(500*time.Millisecond))
	link := establish(t, handset, linkConfig(t, "192.0.2.7:7"))
	ctx, cancel := context.WithCancel(context.Background())
	before := control.Stats().DataBytesFromApp
	dialled := inBackground(func() error {
		_, err := link.DialContext(ctx, "tcp", "192.0.2.7:7")
		return err
	})

	simtest.Eventually(t, timeout, "the SYN is on the line", func() bool {
		return control.Stats().DataBytesFromApp > before
	})
	cancel()

	expectDialContextError(t, awaitError(t, dialled), context.Canceled)
}

func TestDialContextFinishesByItsContextsDeadline(t *testing.T) {
	t.Parallel()
	handset, _ := simtest.NewHandset(t, delayedLink(500*time.Millisecond))
	link := establish(t, handset, linkConfig(t, "192.0.2.7:7"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := link.DialContext(ctx, "tcp", "192.0.2.7:7")

	expectDialContextError(t, err, context.DeadlineExceeded)
}

func TestDialContextKeepsTheTimeoutOfALookupNobodyAnswered(t *testing.T) {
	t.Parallel()
	link := linkWithASilentDNSServer(t, "echo.test:7")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err := link.DialContext(ctx, "tcp", "echo.test:7")

	// Ten seconds in, the context has most of its minute left: its deadline
	// is not what ended the dial.
	expectTimedOut(t, err, "DNS lookup")
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Errorf("DialContext = %v with the context's error %v, want the lookup's own timeout", err, ctx.Err())
	}
}

func TestDialContextRefusesAContextAlreadyDone(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := link.DialContext(ctx, "tcp", "echo.test:7")

	expectDialContextError(t, err, context.Canceled)
}

func TestNetHTTPGetsAPageOverTheLink(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "web.test:80")
	client := &http.Client{Transport: &http.Transport{DialContext: link.DialContext}, Timeout: timeout}

	response, err := client.Get("http://web.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, response)
	body, err := io.ReadAll(response.Body)

	if err != nil || response.StatusCode != http.StatusOK || string(body) != sim.DefaultNetworkConfig().WebPage {
		t.Errorf("GET = %d %q, %v", response.StatusCode, body, err)
	}
}

// closeBody closes a response's body.
func closeBody(t *testing.T, response *http.Response) {
	t.Helper()
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
}

// establishThenDrop brings a link up over handset and drops it, keeping only
// a watcher and a connection, neither of which may keep it reachable.
func establishThenDrop(t *testing.T, handset *satlink.Handset) (*satnet.Watcher, *satnet.TCPConn) {
	t.Helper()
	link, err := satnet.Establish(handset, linkConfig(t, "echo.test:7"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := link.Dial("echo.test", echoPort, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return link.Watcher(), conn
}

func TestADroppedLinkStillHangsUp(t *testing.T) {
	handset, control := simtest.NewHandset(t, simtest.SimulatorConfig())

	watcher, conn := establishThenDrop(t, handset)

	deadline := time.Now().Add(timeout)
	for {
		runtime.GC()
		if reason, closed := watcher.WaitClosed(100 * time.Millisecond); closed {
			if reason.Kind != satnet.CloseReasonRequested {
				t.Errorf("the dropped link closed for %v", reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the dropped link never hung up")
		}
	}
	_, err := readExactly(conn, 1)
	expectLinkDown(t, err, satnet.CloseReasonRequested)
	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
	expectCommandMode(t, handset)
}

func TestTheConnectorsConfigurationServesCryptoTLS(t *testing.T) {
	t.Parallel()
	link := tlsLink(t)
	config := testCA(t).Config()
	config.ServerName = secureHost
	conn, err := link.DialContext(context.Background(), "tcp", "secure.test:443")
	if err != nil {
		t.Fatal(err)
	}
	client := tls.Client(conn, config)
	t.Cleanup(func() {
		// The test is over; a goodbye that fails changes nothing.
		_ = client.Close()
	})
	if err := client.SetDeadline(time.Now().Add(tlsTimeout)); err != nil {
		t.Fatal(err)
	}

	if _, err := client.Write([]byte("crypto/tls itself")); err != nil {
		t.Fatal(err)
	}

	echoed := make([]byte, len("crypto/tls itself"))
	if _, err := io.ReadFull(client, echoed); err != nil || !bytes.Equal(echoed, []byte("crypto/tls itself")) {
		t.Errorf("echoed %q, %v", echoed, err)
	}
}
