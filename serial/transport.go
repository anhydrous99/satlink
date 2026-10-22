package serial

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	serialport "go.bug.st/serial"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/errnotext"
)

// DefaultBaud is the nominal baud rate: over USB CDC-ACM the rate means
// nothing, and 19200 matches the handset's RS-232 default.
const DefaultBaud = 19_200

// dataBits is the character size of the handset's 8N1 serial interface.
const dataBits = 8

// shortestReadTimeout is the least a read waits, as in the Rust version: a
// zero or negative timeout still takes what has already arrived, and the
// library would wait forever for its NoTimeout (-1) and refuse other
// negative timeouts.
const shortestReadTimeout = time.Millisecond

// longestLibraryWait is the longest one library read is asked to wait; a
// longer timeout is waited out in turns. macOS's select(2) refuses a wait
// above 10^8 seconds, and the library's Windows port one above 0xFFFFFFFE
// ms, where the Rust version's serial library cut such a wait down to what
// it could wait. That port also cuts a wait above 0x7FFFFFFE ms, some 24.8
// days, down to that, and would report silence before the wait was over. A
// day is well below them all.
const longestLibraryWait = 24 * time.Hour

// selectSetSize is FD_SETSIZE: the descriptor sets of select(2), with which
// the library's Unix Read waits for input, hold the numbers below it.
const selectSetSize = 1024

// Closing while another goroutine uses the port.
const (
	// closeGrace is how long Close waits for a library call under way
	// before it closes the descriptor all the same: a write or a DTR change
	// takes milliseconds, but a device that stopped taking data blocks a
	// write for good, and closing the port is the last resort for ending a
	// call.
	closeGrace = time.Second
	// closeGraceCheck is how often Close looks whether the calls under way
	// have finished.
	closeGraceCheck = time.Millisecond
)

// portOpener opens a serial port, as go.bug.st/serial's Open does.
type portOpener func(path string, mode *serialport.Mode) (serialport.Port, error)

// Transport is an open serial port to a handset. It implements
// satlink.Transport, satlink.DTRController and io.Closer, and is safe for
// one goroutine reading while another writes; Close may be called from any
// goroutine and ends a read in progress.
//
// A Transport collected without Close closes its port all the same, as the
// Rust version's dropped transport did, which gives the port back and drops
// DTR — but only once the collector notices: call Close to have it closed
// at once.
//
// The zero Transport is not usable; create one with [Open].
type Transport struct {
	port serialport.Port
	// inUse is held for reading by each library call that acts on the
	// port's descriptor — but for the wait of a read, which the library ends
	// itself when the port closes — and Close waits up to closeGrace to take
	// it before it closes the descriptor: a call under way would otherwise
	// act on its number later, which the process may have given to another
	// file by then. The library's Unix writes and line changes use the
	// number without checking it.
	inUse sync.RWMutex
	// closed is set by Close: every library call that starts later fails
	// without touching the descriptor.
	closed atomic.Bool
	// abandoned closes the port of a Transport collected without Close.
	abandoned runtime.Cleanup
}

// A Transport is a handset's port: data, the DTR line, and closing.
var (
	_ satlink.Transport     = (*Transport)(nil)
	_ satlink.DTRController = (*Transport)(nil)
	_ io.Closer             = (*Transport)(nil)
)

// Open opens the handset's serial device at path exclusively, 8N1 at baud,
// with DTR raised, and with RTS asserted, as some CDC firmwares only
// transmit once it is. Raising the lines is best effort, as in the Rust
// version: a device that refuses control requests still opens.
//
// The operating system drops DTR again when the port closes, even if the
// program crashes; with AT&D2 (which data calls set) the handset should
// then end any call — not yet verified on hardware over USB, so
// satlink.Handset.Initialize also ends a data call it finds still up. Child
// processes the program starts do not inherit the port, which would keep
// it open, and DTR up, as long as they live.
//
// On Unix the serial library waits for input with select(2), which cannot
// watch a descriptor numbered 1024 or more: the reads of a port opened while
// the process holds about a thousand files and sockets fail with
// ErrTransport.
func Open(path string, baud int) (*Transport, error) {
	return open(path, baud, openCloseOnExec)
}

// open is Open, opening the port with openPort.
func open(path string, baud int, openPort portOpener) (*Transport, error) {
	// No InitialStatusBits: with them, a firmware that refuses control
	// requests would fail the whole open. Windows enables DTR and RTS in
	// its device control block, and on Unix open(2) raises both.
	mode := &serialport.Mode{
		BaudRate: baud,
		DataBits: dataBits,
		Parity:   serialport.NoParity,
		StopBits: serialport.OneStopBit,
	}
	port, err := openPort(path, mode)
	if err != nil {
		return nil, transportError(err)
	}
	// Best effort, as the Rust version's dtr_on_open, which ignores the
	// result: the line is up already wherever the port opened normally.
	_ = port.SetDTR(true)
	// Some CDC firmwares only transmit once RTS is asserted too; the Rust
	// version drops this result as well.
	_ = port.SetRTS(true)
	transport := &Transport{port: port}
	// The cleanup holds the library's port, not the Transport, which it
	// would keep from being collected.
	transport.abandoned = runtime.AddCleanup(transport, closeAbandonedPort, port)
	return transport, nil
}

// closeAbandonedPort closes port, whose Transport was collected without
// Close: as the Rust version's dropped transport did, which gives the port
// back and drops DTR, so that with AT&D2 a call left up ends.
func closeAbandonedPort(port serialport.Port) {
	// Nobody is left to tell of a failure.
	_ = port.Close()
}

// WriteAll writes every byte of p and waits until they have left, so every
// write starts with an empty output queue. The serial library has no write
// timeout: a device that stops taking data blocks the write. After Close it
// fails with a *satlink.Error of kind ErrTransport, "the serial port is
// closed", without touching the device.
func (t *Transport) WriteAll(p []byte) error {
	for len(p) > 0 {
		written, err := t.write(p)
		if err != nil {
			return err
		}
		if written == 0 {
			return transportError(io.ErrShortWrite)
		}
		p = p[written:]
	}
	return t.usePort(t.port.Drain)
}

// write writes what the library takes of p, reporting how much that was.
func (t *Transport) write(p []byte) (written int, err error) {
	err = t.usePort(func() error {
		var failure error
		written, failure = t.port.Write(p)
		return failure
	})
	return written, err
}

// ReadTimeout reads what has arrived, waiting at most timeout (at least a
// millisecond) for the first byte, as satlink.Transport specifies. A read
// that a signal interrupts (EINTR) waits out the rest of timeout, and so
// does one that waits longer than the serial library can at once. An empty
// p reads nothing, at once. After Close it fails as WriteAll does.
func (t *Transport) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	if len(p) == 0 {
		// The library's Unix Read would take the nothing that read(2) gives
		// an empty buffer for a port that hung up, and fail with "Port has
		// been closed" while input waits.
		return 0, t.checkNotClosed()
	}
	deadline := time.Now().Add(timeout)
	for wait := timeout; ; wait = time.Until(deadline) {
		received, endedEarly, err := t.readOnce(p, wait)
		if err != nil || !endedEarly || !time.Now().Before(deadline) {
			return received, err
		}
	}
}

// readOnce reads with the library, which waits at most wait for the first
// byte, but at least shortestReadTimeout and at most longestLibraryWait.
// endedEarly reports a read that ended with nothing before wait was over: a
// signal cut it short, or the library was given less than wait.
func (t *Transport) readOnce(p []byte, wait time.Duration) (received int, endedEarly bool, err error) {
	libraryWait := min(max(wait, shortestReadTimeout), longestLibraryWait)
	err = t.usePort(func() error { return t.port.SetReadTimeout(libraryWait) })
	if err != nil {
		return 0, false, err
	}
	// The library returns 0 and no error when its timeout passes. Close
	// ends its wait through the library, which inUse would hold up. The
	// library closes the descriptor before it wakes the read, so a read
	// that select(2) has just let through may still read once from that
	// number after the close: a window only the library could shut.
	received, err = t.libraryRead(p)
	switch {
	case err == nil:
		return received, received == 0 && wait > libraryWait, nil
	case errors.Is(err, syscall.EINTR):
		// A signal the program handles (Ctrl-C, say) cuts the wait short
		// with nothing wrong with the port: wait out the rest, or silence
		// would seem to have come early. go.bug.st/serial's Unix Read
		// retries such a wait itself; this keeps the promise for any port.
		return 0, true, nil
	default:
		return 0, false, transportError(err)
	}
}

// libraryRead is the library's Read, whose panic for a port select(2)
// cannot watch becomes an error. go.bug.st/serial's Unix Read waits with
// select(2), whose descriptor sets hold the numbers below selectSetSize,
// and panics for a port above them, which a Go program reaches easily, as
// the runtime raises its limit of open files at start-up; the Rust
// version's serial library waited with poll(2), which has no such limit.
func (t *Transport) libraryRead(p []byte) (received int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			received, err = 0, t.libraryPanicError(recovered)
		}
	}()
	return t.port.Read(p)
}

// libraryPanicError returns the error that recovered, a panic of the
// library's Read, stands for: the runtime error of a port select(2) cannot
// watch. It panics again with anything else, a runtime error on a port
// select(2) can watch included: that is a bug, not a failure of the port's.
func (t *Transport) libraryPanicError(recovered any) error {
	runtimeErr, ok := recovered.(runtime.Error)
	if !ok || !isBeyondSelect(t.port) {
		panic(recovered)
	}
	return fmt.Errorf("the serial library cannot wait for this port (select(2) watches no descriptor from %d up): %w",
		selectSetSize, runtimeErr)
}

// isBeyondSelect reports whether port has a descriptor that select(2)'s
// descriptor sets cannot hold.
func isBeyondSelect(port serialport.Port) bool {
	return slices.ContainsFunc(descriptorsOf(port), func(descriptor int) bool {
		return descriptor >= selectSetSize
	})
}

// RaiseDTR raises the DTR line. After Close it fails as WriteAll does.
func (t *Transport) RaiseDTR() error {
	return t.usePort(func() error { return t.port.SetDTR(true) })
}

// DropDTR drops the DTR line; with AT&D2 the handset ends any call. After
// Close it fails as WriteAll does.
func (t *Transport) DropDTR() error {
	return t.usePort(func() error { return t.port.SetDTR(false) })
}

// Close closes the port, which drops DTR, and ends a read in progress. A
// write or DTR change under way is given up to a second to finish first,
// so that it cannot act on the descriptor once closed; what it had left to
// do fails. Close is idempotent.
func (t *Transport) Close() error {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}
	t.abandoned.Stop()
	if t.awaitCallsUnderWay() {
		defer t.inUse.Unlock()
	}
	return transportError(t.port.Close())
}

// usePort runs call, a library call that acts on the port's descriptor,
// unless the transport is closed, and reports its failure as a transport
// error. Close waits for a call under way before it closes the descriptor.
func (t *Transport) usePort(call func() error) error {
	t.inUse.RLock()
	// Deferred, this also keeps t from being collected, and the port from
	// being closed by its cleanup, while call runs.
	defer t.inUse.RUnlock()
	if err := t.checkNotClosed(); err != nil {
		return err
	}
	return transportError(call())
}

// checkNotClosed fails once Close has been called, as every call on the
// port then does.
func (t *Transport) checkNotClosed() error {
	if t.closed.Load() {
		return closedPortError()
	}
	return nil
}

// awaitCallsUnderWay waits up to closeGrace for the library calls under way
// to finish, and reports whether they did: inUse is then held, for the
// caller to unlock once the port is closed. The calls that start meanwhile
// fail at once, as the transport is closed.
func (t *Transport) awaitCallsUnderWay() bool {
	deadline := time.Now().Add(closeGrace)
	for !t.inUse.TryLock() {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(closeGraceCheck)
	}
	return true
}

// closedPortError reports I/O on a port already closed.
func closedPortError() error {
	return &satlink.Error{Kind: satlink.ErrTransport, Message: "the serial port is closed"}
}

// transportError reports err, the serial library's, as a *satlink.Error of
// kind ErrTransport wrapping it. A nil err stays nil. An error number the
// library passes on from the operating system reads as strerror(3) writes
// it (see [systemError]).
func transportError(err error) error {
	if err == nil {
		return nil
	}
	if errno, ok := err.(syscall.Errno); ok {
		err = systemError{errno: errno}
	}
	return &satlink.Error{Kind: satlink.ErrTransport, Err: err}
}

// systemError is an error number from the operating system, written as
// strerror(3) writes it, with a capital letter: "No such file or
// directory", where Go writes "no such file or directory". The Rust
// version's serial library reported the C library's texts, which the CLI
// prints after "transport error: ".
type systemError struct {
	errno syscall.Errno
}

// Error returns the C library's text for the error number.
func (e systemError) Error() string {
	return errnotext.Text(e.errno)
}

// Unwrap returns the error number, so that errors.Is matches it and the
// io/fs errors it stands for, such as fs.ErrNotExist.
func (e systemError) Unwrap() error {
	return e.errno
}
