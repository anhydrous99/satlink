package serial

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	serialport "go.bug.st/serial"

	"github.com/anhydrous99/satlink/iridium"
)

// errPort is what a failing fakePort reports.
var errPort = errors.New("the port failed")

// fakePort is a serial port in memory, recording what is asked of it. A
// read takes what is queued; with nothing queued it returns nothing at
// once, as the library does when its timeout passes, or, with blocksReads,
// waits until Close. A write takes what it is given and a DTR change is
// made, or, with stallsCalls, each first waits until releaseCalls. It is
// safe for concurrent use.
type fakePort struct {
	// Port is nil: the transport never calls the methods fakePort lacks,
	// which would panic.
	serialport.Port

	mu           sync.Mutex
	incoming     []byte
	written      []byte
	readTimeouts []time.Duration
	dtrChanges   []bool
	rtsChanges   []bool
	drains       int
	closes       int
	closed       chan struct{}
	// readsWaiting counts the reads waiting for data or Close.
	readsWaiting int

	// blocksReads makes a read with nothing queued wait until Close.
	blocksReads bool
	// takesAtMost limits what one Write takes; 0 means everything.
	takesAtMost int
	// takesNothing makes every Write take nothing, without an error.
	takesNothing bool
	// answersOK queues an OK for every write that ends a command line.
	answersOK bool
	// fails makes every call but Close fail with errPort.
	fails bool
	// refusesControl makes SetDTR and SetRTS fail with errPort.
	refusesControl bool
	// interruptions is how many reads still fail with EINTR, as a read a
	// signal cuts short does.
	interruptions int
	// silentReads is how many reads still return nothing before what is
	// queued, as the library's do when their timeout passes.
	silentReads int
	// readPanic, when set, is called by every Read, to panic as a library
	// bug would.
	readPanic func()

	// stallsCalls makes writes and DTR changes wait until releaseCalls, as a
	// device that stopped taking data, or is slow to answer a control
	// request, makes the library's wait.
	stallsCalls bool
	// callsReleased is closed by releaseCalls.
	callsReleased chan struct{}
	// stalledCalls counts the calls that waited, or wait, for releaseCalls.
	stalledCalls int
	// actedOnceClosed is set by a write or DTR change that reached the port
	// once it was closed: on a real port it would have acted on a closed
	// descriptor, whose number the process may have given to another file.
	actedOnceClosed bool
}

// newFakePort returns an open fakePort.
func newFakePort() *fakePort {
	return &fakePort{closed: make(chan struct{}), callsReleased: make(chan struct{})}
}

// Read takes what is queued, or waits for Close with blocksReads.
func (f *fakePort) Read(p []byte) (int, error) {
	if f.readPanic != nil {
		f.readPanic()
	}
	if f.startWaitingForData() {
		<-f.closed
		return 0, errors.New("the port was closed during the read")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.fails:
		return 0, errPort
	case f.interruptions > 0:
		f.interruptions--
		return 0, syscall.EINTR
	case f.silentReads > 0:
		f.silentReads--
		return 0, nil
	}
	taken := copy(p, f.incoming)
	f.incoming = f.incoming[taken:]
	return taken, nil
}

// startWaitingForData reports whether a read must wait for Close, counting
// it among the reads waiting if so.
func (f *fakePort) startWaitingForData() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	waits := f.blocksReads && len(f.incoming) == 0 && !f.fails
	if waits {
		f.readsWaiting++
	}
	return waits
}

// waitingReads returns how many reads wait for data or Close.
func (f *fakePort) waitingReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readsWaiting
}

// Write takes p, or as much of it as takesAtMost allows, once a stalled
// write is released.
func (f *fakePort) Write(p []byte) (int, error) {
	f.stallIfAsked()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noteAction()
	if f.fails {
		return 0, errPort
	}
	taken := len(p)
	switch {
	case f.takesNothing:
		taken = 0
	case f.takesAtMost > 0:
		taken = min(taken, f.takesAtMost)
	}
	f.written = append(f.written, p[:taken]...)
	if f.answersOK && bytes.HasSuffix(f.written, []byte("\r")) {
		f.incoming = append(f.incoming, "\r\nOK\r\n"...)
	}
	return taken, nil
}

// stallIfAsked waits for releaseCalls with stallsCalls, counting the call
// among the stalled ones.
func (f *fakePort) stallIfAsked() {
	if f.startStalling() {
		<-f.callsReleased
	}
}

// startStalling reports whether a call must wait for releaseCalls,
// counting it among the stalled calls if so.
func (f *fakePort) startStalling() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stallsCalls {
		f.stalledCalls++
	}
	return f.stallsCalls
}

// stalledCallCount returns how many calls waited, or wait, for
// releaseCalls.
func (f *fakePort) stalledCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stalledCalls
}

// releaseCalls lets the stalled calls go on.
func (f *fakePort) releaseCalls() {
	close(f.callsReleased)
}

// noteAction notes a write or DTR change reaching the port, with f.mu
// held: once the port is closed, it is one too many.
func (f *fakePort) noteAction() {
	if f.closes > 0 {
		f.actedOnceClosed = true
	}
}

// hasActedOnceClosed reports whether a write or DTR change reached the port
// once it was closed.
func (f *fakePort) hasActedOnceClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.actedOnceClosed
}

// Drain counts a drain.
func (f *fakePort) Drain() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails {
		return errPort
	}
	f.drains++
	return nil
}

// SetDTR records a DTR change, once a stalled one is released.
func (f *fakePort) SetDTR(dtr bool) error {
	f.stallIfAsked()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noteAction()
	if f.fails || f.refusesControl {
		return errPort
	}
	f.dtrChanges = append(f.dtrChanges, dtr)
	return nil
}

// SetRTS records an RTS change.
func (f *fakePort) SetRTS(rts bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails || f.refusesControl {
		return errPort
	}
	f.rtsChanges = append(f.rtsChanges, rts)
	return nil
}

// SetReadTimeout records the timeout.
func (f *fakePort) SetReadTimeout(timeout time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails {
		return errPort
	}
	f.readTimeouts = append(f.readTimeouts, timeout)
	return nil
}

// Close counts a close and ends a read waiting for data.
func (f *fakePort) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	if f.closes == 1 {
		close(f.closed)
	}
	return nil
}

// queue makes data arrive.
func (f *fakePort) queue(data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.incoming = append(f.incoming, data...)
}

// calls returns everything the port was asked to do but close, as a count.
func (f *fakePort) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.written) + len(f.readTimeouts) + len(f.dtrChanges) + len(f.rtsChanges) + f.drains
}

// closeCount returns how many times the port was closed.
func (f *fakePort) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

// drainCount returns how many times the port was drained.
func (f *fakePort) drainCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.drains
}

// libraryShapedPort is a fakePort that keeps its descriptor where
// go.bug.st/serial v1.8.0's Unix port keeps its own, so that descriptorsOf
// finds it on Unix.
type libraryShapedPort struct {
	*fakePort
	handle int
}

func TestReadTimeoutWaitsAtLeastAMillisecond(t *testing.T) {
	port := newFakePort()
	transport := &Transport{port: port}
	timeouts := []time.Duration{0, -5 * time.Second, 500 * time.Microsecond, time.Second}

	for _, timeout := range timeouts {
		if _, err := transport.ReadTimeout(make([]byte, 8), timeout); err != nil {
			t.Fatalf("ReadTimeout(%v): %v", timeout, err)
		}
	}

	want := []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Second}
	if !slices.Equal(port.readTimeouts, want) {
		t.Errorf("read timeouts %v, want %v", port.readTimeouts, want)
	}
}

func TestReadTimeoutReturnsWhatArrived(t *testing.T) {
	port := newFakePort()
	port.queue("OK")
	transport := &Transport{port: port}
	buf := make([]byte, 8)

	received, err := transport.ReadTimeout(buf, time.Second)

	if err != nil || string(buf[:received]) != "OK" {
		t.Errorf("ReadTimeout = %q, %v; want OK", buf[:received], err)
	}
}

func TestAReadThatASignalInterruptsWaitsOutTheRestOfItsTimeout(t *testing.T) {
	port := newFakePort()
	port.interruptions = 1
	port.queue("OK")
	transport := &Transport{port: port}
	buf := make([]byte, 8)

	received, err := transport.ReadTimeout(buf, time.Second)

	if err != nil || string(buf[:received]) != "OK" {
		t.Fatalf("ReadTimeout = %q, %v; want OK, read once the interruption is over", buf[:received], err)
	}
	if len(port.readTimeouts) != 2 {
		t.Fatalf("read timeouts %v, want one before the interruption and one after", port.readTimeouts)
	}
	if rest := port.readTimeouts[1]; rest > time.Second || rest < shortestReadTimeout {
		t.Errorf("the read after the interruption waits %v, want what is left of a second", rest)
	}
}

func TestAReadInterruptedOnceItsTimeIsUpReportsSilence(t *testing.T) {
	port := newFakePort()
	port.interruptions = 2
	port.queue("OK")
	transport := &Transport{port: port}

	received, err := transport.ReadTimeout(make([]byte, 8), 0)

	if received != 0 || err != nil {
		t.Errorf("ReadTimeout = %d, %v; want 0, nil: nothing arrived in time", received, err)
	}
	if len(port.readTimeouts) != 1 {
		t.Errorf("read timeouts %v, want no read once the time was up", port.readTimeouts)
	}
}

func TestReadTimeoutReportsTheLibrarysTimeoutAsNothing(t *testing.T) {
	transport := &Transport{port: newFakePort()}

	received, err := transport.ReadTimeout(make([]byte, 8), time.Millisecond)

	if received != 0 || err != nil {
		t.Errorf("ReadTimeout = %d, %v; want 0, nil", received, err)
	}
}

func TestAWaitLongerThanTheLibraryTakesIsWaitedOutInTurns(t *testing.T) {
	timeouts := map[string]time.Duration{
		"four years":  4 * 365 * 24 * time.Hour,
		"no deadline": math.MaxInt64,
	}
	for name, timeout := range timeouts {
		t.Run(name, func(t *testing.T) {
			port := newFakePort()
			port.silentReads = 2
			port.queue("OK")
			transport := &Transport{port: port}
			buf := make([]byte, 8)

			received, err := transport.ReadTimeout(buf, timeout)

			if err != nil || string(buf[:received]) != "OK" {
				t.Errorf("ReadTimeout = %q, %v; want OK, read on the third turn", buf[:received], err)
			}
			want := []time.Duration{longestLibraryWait, longestLibraryWait, longestLibraryWait}
			if !slices.Equal(port.readTimeouts, want) {
				t.Errorf("read timeouts %v, want %v: turns of the longest wait the library is asked for", port.readTimeouts, want)
			}
		})
	}
}

// The longest waits go.bug.st/serial v1.8.0 takes in one read.
const (
	// windowsLongestWait is the longest read timeout its Windows port waits
	// out in full: it cuts a longer one down to this (serial_windows.go:311
	// and :326), and refuses one above 0xFFFFFFFE ms (:322).
	windowsLongestWait = 0x7FFFFFFE * time.Millisecond
	// macOSLongestWait is about the longest wait macOS's select(2), with
	// which its Unix port waits, takes: it refuses one of more than 10^8
	// seconds with EINVAL.
	macOSLongestWait = 100_000_000 * time.Second
)

func TestNoReadAsksTheLibraryToWaitLongerThanItCan(t *testing.T) {
	// A longer wait would fail on macOS, and on Windows end early or fail.
	port := newFakePort()
	port.queue("OK")
	transport := &Transport{port: port}

	if _, err := transport.ReadTimeout(make([]byte, 8), math.MaxInt64); err != nil {
		t.Fatalf("ReadTimeout: %v", err)
	}

	longest := slices.Max(port.readTimeouts)
	limits := map[string]time.Duration{"Windows": windowsLongestWait, "macOS": macOSLongestWait}
	for system, limit := range limits {
		if longest > limit {
			t.Errorf("the library was asked to wait %v, longer than it waits on %s in one read, %v", longest, system, limit)
		}
	}
}

func TestAPanicOfTheLibraryThatIsNoRuntimeErrorIsPassedOn(t *testing.T) {
	port := newFakePort()
	port.readPanic = func() { panic("a port bug") }
	transport := &Transport{port: port}

	panicked := panicOf(func() {
		received, err := transport.ReadTimeout(make([]byte, 8), time.Second)
		t.Errorf("ReadTimeout = %d, %v instead of passing the library's panic on", received, err)
	})

	if panicked != "a port bug" {
		t.Errorf("ReadTimeout panicked with %v, want the library's panic", panicked)
	}
}

func TestARuntimePanicOfAPortSelectCanWatchIsPassedOn(t *testing.T) {
	// The port's descriptor, 1023, is the last that select(2)'s sets hold,
	// so the panic is not the select(2) limit but a bug, which no error
	// should hide.
	port := &libraryShapedPort{fakePort: newFakePort(), handle: 1023}
	port.readPanic = func() { _ = (*fakePort)(nil).closes }
	transport := &Transport{port: port}

	panicked := panicOf(func() {
		received, err := transport.ReadTimeout(make([]byte, 8), time.Second)
		t.Errorf("ReadTimeout = %d, %v instead of passing the library's panic on", received, err)
	})

	if _, isRuntimeError := panicked.(runtime.Error); !isRuntimeError {
		t.Errorf("ReadTimeout panicked with %v, want the library's runtime error", panicked)
	}
}

func TestWriteAllWritesEverythingThenDrains(t *testing.T) {
	port := newFakePort()
	port.takesAtMost = 3
	transport := &Transport{port: port}

	err := transport.WriteAll([]byte("hello world"))

	if err != nil || string(port.written) != "hello world" || port.drains != 1 {
		t.Errorf("WriteAll = %v; port has %q after %d drains, want everything and one drain", err, port.written, port.drains)
	}
}

func TestWriteAllStopsAtAWriteThatTakesNothing(t *testing.T) {
	port := newFakePort()
	port.takesNothing = true
	transport := &Transport{port: port}

	err := transport.WriteAll([]byte("AT\r"))

	if !errors.Is(err, iridium.ErrTransport) || !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("WriteAll = %v, want ErrTransport wrapping io.ErrShortWrite", err)
	}
}

func TestDTRChangesReachThePort(t *testing.T) {
	port := newFakePort()
	transport := &Transport{port: port}

	raiseErr := transport.RaiseDTR()
	dropErr := transport.DropDTR()

	if raiseErr != nil || dropErr != nil || !slices.Equal(port.dtrChanges, []bool{true, false}) {
		t.Errorf("RaiseDTR, DropDTR = %v, %v; DTR changes %v, want [true false]", raiseErr, dropErr, port.dtrChanges)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	port := newFakePort()
	transport := &Transport{port: port}

	first := transport.Close()
	second := transport.Close()

	if first != nil || second != nil || port.closeCount() != 1 {
		t.Errorf("Close, Close = %v, %v after %d closes; want nil, nil after one", first, second, port.closeCount())
	}
}

func TestCloseEndsABlockedRead(t *testing.T) {
	port := newFakePort()
	port.blocksReads = true
	transport := &Transport{port: port}
	readEnded := make(chan error, 1)
	go func() {
		_, err := transport.ReadTimeout(make([]byte, 8), time.Hour)
		readEnded <- err
	}()
	awaitCondition(t, "a read waits on the port", func() bool { return port.waitingReads() > 0 })

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-readEnded:
		if !errors.Is(err, iridium.ErrTransport) {
			t.Errorf("the read ended with %v, want ErrTransport", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read did not end when the port closed")
	}
}

// promisedCloseGrace is how long Close promises to give a write or DTR
// change under way to finish: a second.
const promisedCloseGrace = time.Second

// callFinishesAfter is how long a call under way takes to finish once Close
// has begun: well within the second Close gives it, but longer than a grace
// of a few milliseconds would allow.
const callFinishesAfter = 50 * time.Millisecond

func TestCloseGivesAWriteUnderWayAWhileButNoMore(t *testing.T) {
	// Slow — Close waits a second for the stalled write — but
	// timing-insensitive.
	t.Parallel()
	port := newFakePort()
	port.stallsCalls = true
	transport := &Transport{port: port}
	writeEnded := make(chan error, 1)
	go func() { writeEnded <- transport.WriteAll([]byte("a PPP frame")) }()
	awaitCondition(t, "a write is stalled on the port", func() bool { return port.stalledCallCount() > 0 })
	started := time.Now()

	closeErr := closeWithin(t, transport, 10*time.Second)

	if waited := time.Since(started); waited < promisedCloseGrace {
		t.Errorf("Close closed the port %v into a write under way, want it to wait %v first", waited, promisedCloseGrace)
	}
	if closeErr != nil || port.closeCount() != 1 {
		t.Errorf("Close = %v after %d port closes; want the port closed all the same", closeErr, port.closeCount())
	}
	port.releaseCalls()
	assertTransportMessage(t, awaitError(t, writeEnded), "the serial port is closed")
	if drains := port.drainCount(); drains != 0 {
		t.Errorf("the port was drained %d times once closed", drains)
	}
}

func TestCloseLetsACallUnderWayFinishFirst(t *testing.T) {
	// Closed under the call, the descriptor would be acted on once closed,
	// when the process may have given its number to another file.
	calls := map[string]func(*Transport) error{
		"write":      func(transport *Transport) error { return transport.WriteAll([]byte("a PPP frame")) },
		"DTR change": (*Transport).DropDTR,
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			port := newFakePort()
			port.stallsCalls = true
			transport := &Transport{port: port}
			callEnded := make(chan error, 1)
			go func() { callEnded <- call(transport) }()
			awaitCondition(t, "the call is stalled on the port", func() bool { return port.stalledCallCount() > 0 })

			closeEnded := make(chan error, 1)
			go func() { closeEnded <- transport.Close() }()
			awaitCondition(t, "Close has begun", transport.closed.Load)
			// The scenario's own duration: nothing shows Close waiting.
			time.Sleep(callFinishesAfter)
			port.releaseCalls()

			// What the call returns does not matter here, only when it
			// acted on the port.
			_ = awaitError(t, callEnded)
			closeErr := awaitError(t, closeEnded)
			if port.hasActedOnceClosed() {
				t.Error("Close closed the port under the call, which then acted on its descriptor")
			}
			if closeErr != nil || port.closeCount() != 1 {
				t.Errorf("Close = %v after %d port closes; want the port closed once the call was done", closeErr, port.closeCount())
			}
		})
	}
}

func TestATransportCollectedWithoutCloseClosesItsPort(t *testing.T) {
	port := newFakePort()
	if _, err := open("/dev/ttyACM0", DefaultBaud, openerOf(port)); err != nil {
		t.Fatalf("open: %v", err)
	}

	awaitCollection(t, "the port of a collected Transport is closed", func() bool { return port.closeCount() > 0 })

	if closes := port.closeCount(); closes != 1 {
		t.Errorf("the port was closed %d times, want once", closes)
	}
}

func TestAClosedTransportIsNotClosedAgainOnceCollected(t *testing.T) {
	port := newFakePort()
	collected := openCloseAndDrop(t, port)

	awaitCollection(t, "the Transport is collected", func() bool { return isClosed(collected) })

	// A wait for something not to happen: had Close left the Transport's
	// cleanup in place, the collection that ran the test's would have run it
	// too, at about the same time.
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	if closes := port.closeCount(); closes != 1 {
		t.Errorf("the port was closed %d times, want once: by Close", closes)
	}
}

func TestAClosedTransportLeavesThePortAlone(t *testing.T) {
	port := newFakePort()
	transport := &Transport{port: port}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, readErr := transport.ReadTimeout(make([]byte, 8), time.Millisecond)
	_, emptyReadErr := transport.ReadTimeout(nil, time.Millisecond)
	errs := []error{transport.WriteAll([]byte("AT\r")), readErr, emptyReadErr, transport.RaiseDTR(), transport.DropDTR()}

	for _, err := range errs {
		assertTransportMessage(t, err, "the serial port is closed")
	}
	if calls := port.calls(); calls != 0 {
		t.Errorf("the closed port was asked %d things", calls)
	}
}

func TestPortErrorsAreTransportErrors(t *testing.T) {
	port := newFakePort()
	port.fails = true
	transport := &Transport{port: port}

	_, readErr := transport.ReadTimeout(make([]byte, 8), time.Millisecond)
	errs := []error{transport.WriteAll([]byte("AT\r")), readErr, transport.RaiseDTR(), transport.DropDTR()}

	for _, err := range errs {
		if !errors.Is(err, iridium.ErrTransport) || !errors.Is(err, errPort) {
			t.Errorf("error = %v, want ErrTransport wrapping the port's", err)
		}
	}
}

func TestADrainThatFailsIsATransportError(t *testing.T) {
	port := newFakePort()
	port.fails = true
	transport := &Transport{port: port}

	err := transport.WriteAll(nil)

	if !errors.Is(err, errPort) {
		t.Errorf("WriteAll = %v, want the drain's error", err)
	}
}

func TestOpenOpens8N1WithDTRAndRTSRaised(t *testing.T) {
	port := newFakePort()
	var openedPath string
	var openedMode serialport.Mode
	opener := func(path string, mode *serialport.Mode) (serialport.Port, error) {
		openedPath, openedMode = path, *mode
		return port, nil
	}

	transport, err := open("/dev/cu.usbmodem1101", DefaultBaud, opener)

	if err != nil || transport == nil {
		t.Fatalf("open = %v, %v", transport, err)
	}
	wantMode := serialport.Mode{BaudRate: DefaultBaud, DataBits: 8, Parity: serialport.NoParity, StopBits: serialport.OneStopBit}
	if openedPath != "/dev/cu.usbmodem1101" || openedMode != wantMode {
		t.Errorf("opened %q with %+v, want %+v without initial status bits", openedPath, openedMode, wantMode)
	}
	if !slices.Equal(port.dtrChanges, []bool{true}) || !slices.Equal(port.rtsChanges, []bool{true}) {
		t.Errorf("DTR %v, RTS %v; want both raised", port.dtrChanges, port.rtsChanges)
	}
}

func TestOpenToleratesAPortThatRefusesControlRequests(t *testing.T) {
	port := newFakePort()
	port.refusesControl = true

	transport, err := open("/dev/ttyACM0", DefaultBaud, openerOf(port))

	if err != nil || transport == nil {
		t.Errorf("open = %v, %v; want the port open anyway", transport, err)
	}
}

func TestAPortThatDoesNotOpenIsATransportError(t *testing.T) {
	opener := func(string, *serialport.Mode) (serialport.Port, error) { return nil, errPort }

	_, err := open("/dev/ttyACM0", DefaultBaud, opener)

	if !errors.Is(err, iridium.ErrTransport) || !errors.Is(err, errPort) {
		t.Errorf("open = %v, want ErrTransport wrapping the library's error", err)
	}
}

func TestErrorNumbersAreWrittenAsTheCLibraryWritesThem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows writes its own texts for error numbers")
	}
	tests := []struct {
		failure error
		want    string
	}{
		{syscall.ENOENT, "transport error: No such file or directory"},
		{syscall.EIO, "transport error: Input/output error"},
		{errPort, "transport error: the port failed"},
	}
	for _, test := range tests {
		opener := func(string, *serialport.Mode) (serialport.Port, error) { return nil, test.failure }

		_, err := open("/dev/ttyACM0", DefaultBaud, opener)

		if got := fmt.Sprint(err); got != test.want || !errors.Is(err, test.failure) {
			t.Errorf("open = %q, want %q wrapping %v", got, test.want, test.failure)
		}
	}
}

// awaitCondition returns once condition holds, failing the test if it does
// not within a few seconds: what says what was awaited.
func awaitCondition(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// closeWithin closes transport and returns Close's error, failing the test
// if Close does not return within timeout.
func closeWithin(t *testing.T, transport *Transport, timeout time.Duration) error {
	t.Helper()
	closed := make(chan error, 1)
	go func() { closed <- transport.Close() }()
	select {
	case err := <-closed:
		return err
	case <-time.After(timeout):
		t.Fatalf("Close did not return within %v", timeout)
		return nil
	}
}

// awaitError returns the error ended delivers, failing the test if none
// comes within a few seconds.
func awaitError(t *testing.T, ended <-chan error) error {
	t.Helper()
	select {
	case err := <-ended:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not end")
		return nil
	}
}

// awaitCollection runs the collector until condition holds, failing the
// test if it does not within a few seconds.
func awaitCollection(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
}

// openCloseAndDrop opens a Transport over port, closes it and drops it, and
// returns a channel closed once the collector has taken the Transport.
func openCloseAndDrop(t *testing.T, port *fakePort) <-chan struct{} {
	t.Helper()
	transport, err := open("/dev/ttyACM0", DefaultBaud, openerOf(port))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	collected := make(chan struct{})
	runtime.AddCleanup(transport, func(collected chan struct{}) { close(collected) }, collected)
	return collected
}

// isClosed reports whether channel is closed.
func isClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

// panicOf runs action and returns what it panicked with, nil if it did not
// panic.
func panicOf(action func()) (panicked any) {
	defer func() { panicked = recover() }()
	action()
	return nil
}

// openerOf returns a portOpener that opens port, whatever the path.
func openerOf(port serialport.Port) portOpener {
	return func(string, *serialport.Mode) (serialport.Port, error) { return port, nil }
}

// assertTransportMessage fails the test unless err is an ErrTransport
// whose message is want.
func assertTransportMessage(t *testing.T, err error, want string) {
	t.Helper()
	var transportErr *iridium.Error
	if !errors.As(err, &transportErr) || transportErr.Kind != iridium.ErrTransport || transportErr.Message != want {
		t.Errorf("error = %v, want ErrTransport %q", err, want)
	}
}
