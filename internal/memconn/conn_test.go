package memconn

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

// patience bounds every wait for something that should happen at once; a
// test that hits it has failed.
const patience = 5 * time.Second

// shortWait is how long a test lets something that should not happen have
// the chance to.
const shortWait = 20 * time.Millisecond

var (
	serverEnd = netip.MustParseAddrPort("192.0.2.43:443")
	clientEnd = netip.MustParseAddrPort("10.64.0.2:49152")
)

func newConn() *Conn {
	return New(serverEnd, clientEnd)
}

// readResult is what a Read in another goroutine returned.
type readResult struct {
	data []byte
	err  error
}

// readInBackground starts a Read of up to size bytes and returns where its
// result arrives.
func readInBackground(conn *Conn, size int) <-chan readResult {
	results := make(chan readResult, 1)
	go func() {
		buffer := make([]byte, size)
		count, err := conn.Read(buffer)
		results <- readResult{data: buffer[:count], err: err}
	}()
	return results
}

// awaitResult returns what a goroutine of the test sends on results, such
// as a background Read's result, failing the test if nothing arrives
// within patience.
func awaitResult[T any](t *testing.T, results <-chan T) T {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(patience):
		t.Fatalf("no result within %v", patience)
		var none T
		return none
	}
}

// awaitWaitingReader waits until a Read is blocked on conn.
func awaitWaitingReader(t *testing.T, conn *Conn) {
	t.Helper()
	if !conn.AwaitReader(patience) {
		t.Fatal("no Read started waiting")
	}
}

// awaitWaitingReaders waits until count Reads are blocked on conn side by
// side, which AwaitReader cannot tell: it is satisfied by one.
func awaitWaitingReaders(t *testing.T, conn *Conn, count int) {
	t.Helper()
	deadline := time.Now().Add(patience)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for conn.waitingReaders < count {
		if hasPassed(deadline) {
			t.Fatalf("%d Reads wait; want %d", conn.waitingReaders, count)
		}
		conn.waitLocked(conn.readerChanged.channel(), deadline)
	}
}

// bufferWithoutWaking puts text where Read takes its bytes from, as Inject
// does, but leaves the Reads that wait asleep: it holds still the moment
// between Inject and a Read it woke taking the bytes.
func bufferWithoutWaking(conn *Conn, text string) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	_, _ = conn.inbound.WriteString(text) // bytes.Buffer.WriteString takes everything or panics, never fails.
}

// closeInboundWithoutWaking records the peer's FIN, as CloseInbound does,
// but leaves the Reads that wait asleep: it holds still the moment between
// CloseInbound and a Read it woke taking the FIN.
func closeInboundWithoutWaking(conn *Conn) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.inboundClosed = true
}

// wakeWaitingReads wakes the Reads that wait, as Inject and CloseInbound
// do once the bytes or the FIN are in.
func wakeWaitingReads(conn *Conn) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.inputChanged.fire()
}

// awaitReaderInBackground starts an AwaitReader with patience and returns
// where its result arrives.
//
// The tests of what wakes AwaitReader run it in a synctest bubble: there
// synctest.Wait returns once every other goroutine waits, so the test knows
// that AwaitReader waits before it acts, and time passes only while every
// goroutine waits, so an
// AwaitReader that was woken returns with no time passed, while one that
// gave up after patience returns patience later — with the same result, as
// it looks once more before it gives up.
func awaitReaderInBackground(conn *Conn) <-chan bool {
	idle := make(chan bool, 1)
	go func() { idle <- conn.AwaitReader(patience) }()
	return idle
}

func TestReadReturnsInjectedBytesInOrder(t *testing.T) {
	t.Parallel()
	conn := newConn()

	conn.Inject([]byte("hello, "))
	conn.Inject([]byte("world"))
	buffer := make([]byte, 64)
	count, err := conn.Read(buffer)

	if err != nil || string(buffer[:count]) != "hello, world" {
		t.Errorf("Read = %q, %v; want \"hello, world\", nil", buffer[:count], err)
	}
}

func TestReadReturnsAtMostWhatFits(t *testing.T) {
	t.Parallel()
	conn := newConn()
	conn.Inject([]byte("abcdef"))

	first := make([]byte, 4)
	count, err := conn.Read(first)
	rest := make([]byte, 4)
	restCount, restErr := conn.Read(rest)

	if err != nil || string(first[:count]) != "abcd" {
		t.Errorf("first Read = %q, %v; want \"abcd\", nil", first[:count], err)
	}
	if restErr != nil || string(rest[:restCount]) != "ef" {
		t.Errorf("second Read = %q, %v; want \"ef\", nil", rest[:restCount], restErr)
	}
}

func TestReadWaitsForInjectedBytes(t *testing.T) {
	t.Parallel()
	conn := newConn()
	results := readInBackground(conn, 16)
	awaitWaitingReader(t, conn)

	conn.Inject([]byte("late"))

	if result := awaitResult(t, results); result.err != nil || string(result.data) != "late" {
		t.Errorf("Read = %q, %v; want \"late\", nil", result.data, result.err)
	}
}

func TestAnEmptyReadReturnsAtOnce(t *testing.T) {
	t.Parallel()
	conn := newConn()

	count, err := conn.Read(nil)

	if count != 0 || err != nil {
		t.Errorf("Read(nil) = %d, %v; want 0, nil", count, err)
	}
}

func TestReadReturnsEOFOnceWhatCameBeforeTheFINIsRead(t *testing.T) {
	t.Parallel()
	conn := newConn()
	conn.Inject([]byte("last words"))

	conn.CloseInbound()
	data, err := io.ReadAll(conn)

	if err != nil || string(data) != "last words" {
		t.Errorf("ReadAll = %q, %v; want \"last words\", nil", data, err)
	}
	if count, err := conn.Read(make([]byte, 8)); count != 0 || err != io.EOF {
		t.Errorf("Read after EOF = %d, %v; want 0, io.EOF again", count, err)
	}
}

func TestEOFDeliveredReportsOnlyAnEOFThatReadReturned(t *testing.T) {
	t.Parallel()
	conn := newConn()
	conn.Inject([]byte("x"))
	conn.CloseInbound()

	beforeReading := conn.EOFDelivered()
	if _, err := conn.Read(make([]byte, 8)); err != nil {
		t.Fatalf("reading the last byte: %v", err)
	}
	beforeTheEOF := conn.EOFDelivered()
	_, err := conn.Read(make([]byte, 8))

	if beforeReading || beforeTheEOF {
		t.Errorf("EOFDelivered was true before Read returned io.EOF")
	}
	if err != io.EOF || !conn.EOFDelivered() {
		t.Errorf("Read = %v and EOFDelivered = %v; want io.EOF and true", err, conn.EOFDelivered())
	}
}

func TestCloseInboundWakesAWaitingRead(t *testing.T) {
	t.Parallel()
	conn := newConn()
	results := readInBackground(conn, 16)
	awaitWaitingReader(t, conn)

	conn.CloseInbound()

	if result := awaitResult(t, results); result.err != io.EOF {
		t.Errorf("Read = %v; want io.EOF", result.err)
	}
}

func TestAReadDeadlineThatPassesFailsTheReadWithATemporaryTimeout(t *testing.T) {
	t.Parallel()
	conn := newConn()
	if err := conn.SetReadDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}

	_, err := conn.Read(make([]byte, 8))

	var netErr net.Error
	if !errors.Is(err, os.ErrDeadlineExceeded) || !errors.As(err, &netErr) {
		t.Fatalf("Read = %v; want os.ErrDeadlineExceeded", err)
	}
	//lint:ignore SA1019 crypto/tls keeps a session only after an error whose Temporary reports true.
	temporary := netErr.Temporary()
	if !netErr.Timeout() || !temporary {
		t.Errorf("Timeout() = %v, Temporary() = %v; want both true", netErr.Timeout(), temporary)
	}
}

func TestBytesThatArrivedBeforeAPassedDeadlineAreStillRead(t *testing.T) {
	t.Parallel()
	conn := newConn()
	conn.Inject([]byte("early"))
	if err := conn.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}

	buffer := make([]byte, 8)
	count, err := conn.Read(buffer)
	_, laterErr := conn.Read(buffer)

	if err != nil || string(buffer[:count]) != "early" {
		t.Errorf("Read = %q, %v; want \"early\", nil", buffer[:count], err)
	}
	if !errors.Is(laterErr, os.ErrDeadlineExceeded) {
		t.Errorf("Read with nothing left = %v; want os.ErrDeadlineExceeded", laterErr)
	}
}

func TestMovingTheReadDeadlineWakesAWaitingRead(t *testing.T) {
	t.Parallel()
	conn := newConn()
	results := readInBackground(conn, 8)
	awaitWaitingReader(t, conn)

	if err := conn.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}

	if result := awaitResult(t, results); !errors.Is(result.err, os.ErrDeadlineExceeded) {
		t.Errorf("Read = %v; want os.ErrDeadlineExceeded", result.err)
	}
}

func TestClearingTheReadDeadlineLetsAReadWaitOn(t *testing.T) {
	t.Parallel()
	conn := newConn()
	if err := conn.SetReadDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}
	results := readInBackground(conn, 8)
	awaitWaitingReader(t, conn)

	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-results:
		t.Fatalf("Read returned %q, %v without a deadline or bytes", result.data, result.err)
	case <-time.After(2 * shortWait):
	}
	conn.Inject([]byte("ok"))

	if result := awaitResult(t, results); result.err != nil || string(result.data) != "ok" {
		t.Errorf("Read = %q, %v; want \"ok\", nil", result.data, result.err)
	}
}

func TestCloseWakesAWaitingRead(t *testing.T) {
	t.Parallel()
	conn := newConn()
	results := readInBackground(conn, 8)
	awaitWaitingReader(t, conn)

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if result := awaitResult(t, results); !errors.Is(result.err, net.ErrClosed) {
		t.Errorf("Read = %v; want net.ErrClosed", result.err)
	}
}

func TestReadsWaitingSideBySideAllEndWithClose(t *testing.T) {
	t.Parallel()
	conn := newConn()
	first := readInBackground(conn, 8)
	second := readInBackground(conn, 8)
	awaitWaitingReaders(t, conn, 2)

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	for _, results := range []<-chan readResult{first, second} {
		if result := awaitResult(t, results); !errors.Is(result.err, net.ErrClosed) {
			t.Errorf("Read = %v; want net.ErrClosed", result.err)
		}
	}
}

func TestEverythingButExtractFailsAfterClose(t *testing.T) {
	t.Parallel()
	conn := newConn()
	conn.Inject([]byte("unread"))

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := conn.Read(make([]byte, 8)); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Read = %v; want net.ErrClosed", err)
	}
	if _, err := conn.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Write = %v; want net.ErrClosed", err)
	}
	setters := map[string]func(time.Time) error{
		"SetDeadline":      conn.SetDeadline,
		"SetReadDeadline":  conn.SetReadDeadline,
		"SetWriteDeadline": conn.SetWriteDeadline,
	}
	for name, set := range setters {
		if err := set(time.Now()); !errors.Is(err, net.ErrClosed) {
			t.Errorf("%s = %v; want net.ErrClosed", name, err)
		}
	}
	if !conn.Closed() {
		t.Error("Closed() = false after Close")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	conn := newConn()

	first, second := conn.Close(), conn.Close()

	if first != nil || second != nil {
		t.Errorf("Close, Close = %v, %v; want nil, nil", first, second)
	}
}

func TestWhatWasWrittenIsExtractedInOrderEvenAfterClose(t *testing.T) {
	t.Parallel()
	conn := newConn()
	for _, record := range []string{"alert", "close_notify"} {
		if _, err := conn.Write([]byte(record)); err != nil {
			t.Fatal(err)
		}
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	pending := conn.Pending()
	first := make([]byte, 5)
	firstCount := conn.Extract(first)
	rest := make([]byte, 64)
	restCount := conn.Extract(rest)

	if pending != len("alertclose_notify") {
		t.Errorf("Pending() = %d; want %d", pending, len("alertclose_notify"))
	}
	if string(first[:firstCount]) != "alert" || string(rest[:restCount]) != "close_notify" {
		t.Errorf("extracted %q then %q; want \"alert\" then \"close_notify\"", first[:firstCount], rest[:restCount])
	}
	if conn.Pending() != 0 || conn.Extract(rest) != 0 {
		t.Error("bytes are left after extracting everything")
	}
}

func TestAWriteDeadlineThatHasPassedFailsWritesAndWritesNothing(t *testing.T) {
	t.Parallel()
	conn := newConn()
	if err := conn.SetWriteDeadline(time.Now().Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	count, err := conn.Write([]byte("too late"))

	if count != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("Write = %d, %v; want 0, os.ErrDeadlineExceeded", count, err)
	}
	if conn.Pending() != 0 {
		t.Errorf("Pending() = %d after a failed write; want 0", conn.Pending())
	}
}

func TestAWriteDeadlineInTheFutureLetsWritesThrough(t *testing.T) {
	t.Parallel()
	conn := newConn()
	if err := conn.SetDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	count, err := conn.Write([]byte("in time"))

	if count != len("in time") || err != nil {
		t.Errorf("Write = %d, %v; want %d, nil", count, err, len("in time"))
	}
}

func TestInjectTakesNothingOnceClosedOrInboundClosed(t *testing.T) {
	t.Parallel()
	closers := map[string]func(*Conn){
		"Close":        func(conn *Conn) { _ = conn.Close() }, // Close never fails.
		"CloseInbound": (*Conn).CloseInbound,
	}
	for name, closeSide := range closers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			conn := newConn()

			closeSide(conn)

			if taken := conn.Inject([]byte("ignored")); taken != 0 {
				t.Errorf("Inject took %d bytes after %s; want 0", taken, name)
			}
		})
	}
}

func TestInjectKeepsNoReferenceToItsArgument(t *testing.T) {
	t.Parallel()
	conn := newConn()
	data := []byte("original")

	taken := conn.Inject(data)
	copy(data, "XXXXXXXX")
	buffer := make([]byte, 16)
	count, err := conn.Read(buffer)

	if taken != len("original") || err != nil || string(buffer[:count]) != "original" {
		t.Errorf("Inject = %d, then Read = %q, %v; want 8, \"original\", nil", taken, buffer[:count], err)
	}
}

func TestAwaitReaderReportsAReadWaitingWithNothingLeft(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		conn := newConn()
		idle := awaitReaderInBackground(conn)
		synctest.Wait() // AwaitReader waits, as no Read does yet.
		readStartedAt := time.Now()

		results := readInBackground(conn, 8)

		if !<-idle {
			t.Error("AwaitReader = false while a Read waits")
		}
		if waited := time.Since(readStartedAt); waited != 0 {
			t.Errorf("AwaitReader returned %v after the Read started waiting; want at once", waited)
		}
		conn.Inject([]byte("x"))
		awaitResult(t, results)
	})
}

func TestAwaitReaderTimesOutWhileNoReadWaits(t *testing.T) {
	t.Parallel()
	conn := newConn()
	start := time.Now()

	waiting := conn.AwaitReader(shortWait)

	if waiting {
		t.Error("AwaitReader = true without a Read")
	}
	if elapsed := time.Since(start); elapsed < shortWait {
		t.Errorf("AwaitReader returned after %v; want it to wait %v", elapsed, shortWait)
	}
}

// Right after Inject, the Read it woke still counts as waiting, with the
// bytes yet to take, until it has the lock again: the moment in which an
// owner calls AwaitReader. A test cannot catch it from outside, as the Read
// may well be quicker, so this one holds it still: it buffers the bytes
// without waking the Read.
func TestAwaitReaderWaitsUntilTheReaderHasTakenWhatWasInjected(t *testing.T) {
	t.Parallel()
	conn := newConn()
	results := readInBackground(conn, 16)
	awaitWaitingReader(t, conn)
	bufferWithoutWaking(conn, "unread")

	idleWithBytesLeft := conn.AwaitReader(shortWait)
	wakeWaitingReads(conn)
	first := awaitResult(t, results)
	next := readInBackground(conn, 16)
	idleOnceTaken := conn.AwaitReader(patience)

	if idleWithBytesLeft {
		t.Error("AwaitReader = true while the waiting Read had yet to take what was injected")
	}
	if string(first.data) != "unread" || !idleOnceTaken {
		t.Errorf("read %q, then AwaitReader = %v; want \"unread\", true", first.data, idleOnceTaken)
	}
	conn.CloseInbound()
	awaitResult(t, next)
}

// Right after CloseInbound, likewise, the Read it woke still counts as
// waiting, with the FIN yet to take: the moment in which the simulator's
// TLS echo, saying goodbye to a client that hung up, calls AwaitReader.
// This test holds it still the same way: it records the FIN without waking
// the Read.
func TestAwaitReaderWaitsUntilTheReaderHasTakenTheFIN(t *testing.T) {
	t.Parallel()
	conn := newConn()
	results := readInBackground(conn, 16)
	awaitWaitingReader(t, conn)
	closeInboundWithoutWaking(conn)

	idleWithTheFINLeft := conn.AwaitReader(shortWait)
	wakeWaitingReads(conn)
	result := awaitResult(t, results)

	if idleWithTheFINLeft {
		t.Error("AwaitReader = true while the waiting Read had yet to take the FIN")
	}
	if result.err != io.EOF || !conn.EOFDelivered() {
		t.Errorf("Read = %v with EOFDelivered %v; want io.EOF and true", result.err, conn.EOFDelivered())
	}
}

func TestAwaitReaderReportsAClosedConnAtOnce(t *testing.T) {
	t.Parallel()
	conn := newConn()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if !conn.AwaitReader(0) {
		t.Error("AwaitReader = false on a closed Conn")
	}
}

// The session here ends once the test lets it, after AwaitReader has
// started waiting, so that Close is what wakes AwaitReader.
func TestAfterTheFINAwaitReaderWaitsForTheSessionToEnd(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		conn := newConn()
		mayEnd := make(chan struct{})
		// A session that ends once it has read the FIN, as the simulator's
		// TLS echo does.
		go func() {
			_, _ = io.Copy(io.Discard, conn) // Only the end matters here.
			<-mayEnd
			_ = conn.Close() // Close never fails.
		}()
		synctest.Wait() // The session's Read waits.
		conn.CloseInbound()
		idle := awaitReaderInBackground(conn)
		synctest.Wait() // The session has read the FIN, and AwaitReader waits.
		returnedBeforeTheEnd := len(idle) > 0
		endedAt := time.Now()

		close(mayEnd)
		idleAtTheEnd := <-idle

		if returnedBeforeTheEnd {
			t.Error("AwaitReader returned while the session, done reading, was still open")
		}
		if !idleAtTheEnd || !conn.Closed() {
			t.Errorf("AwaitReader = %v with Closed() = %v; want true once the session closed", idleAtTheEnd, conn.Closed())
		}
		if waited := time.Since(endedAt); waited != 0 {
			t.Errorf("AwaitReader returned %v after the session closed; want at once", waited)
		}
	})
}

func TestTheAddressesAreTheEndpointsGivenToNew(t *testing.T) {
	t.Parallel()
	conn := newConn()

	local, remote := conn.LocalAddr(), conn.RemoteAddr()

	if local.Network() != "tcp" || local.String() != serverEnd.String() {
		t.Errorf("LocalAddr() = %v %v; want tcp %v", local.Network(), local, serverEnd)
	}
	if remote.Network() != "tcp" || remote.String() != clientEnd.String() {
		t.Errorf("RemoteAddr() = %v %v; want tcp %v", remote.Network(), remote, clientEnd)
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	t.Parallel()
	conn := newConn()
	// A failed run's reader would otherwise wait in Read for good. Close
	// never fails.
	t.Cleanup(func() { _ = conn.Close() })
	message := bytes.Repeat([]byte("0123456789"), 1000)
	received := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(conn) // The comparison below tells what arrived.
		received <- data
	}()

	// One bound for the whole exchange: a regression that makes AwaitReader
	// wait out its timeout then fails the test within patience, where a
	// bound for each chunk would add up to hours.
	deadline := time.Now().Add(patience)
	for chunk := range slices.Chunk(message, 7) {
		conn.Inject(chunk)
		if !conn.AwaitReader(time.Until(deadline)) {
			t.Fatalf("AwaitReader did not see the reader take every chunk within %v", patience)
		}
		if _, err := conn.Write(chunk); err != nil {
			t.Fatal(err)
		}
		_ = conn.Pending() // Only the race detector looks at this.
	}
	conn.CloseInbound()

	if data := awaitResult(t, received); !bytes.Equal(data, message) {
		t.Errorf("received %d bytes; want the %d injected", len(data), len(message))
	}
	echoed := make([]byte, len(message)+1)
	if count := conn.Extract(echoed); count != len(message) {
		t.Errorf("extracted %d bytes; want %d", count, len(message))
	}
}
