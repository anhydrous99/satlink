package simtest

import (
	"bytes"
	"io"
	"sync/atomic"
	"time"

	"github.com/anhydrous99/satlink"
)

// What the switches watch for, written in one piece as satlink.Handset
// writes it.
var (
	// escapeSequence is the Hayes escape, as a hang-up writes it.
	escapeSequence = []byte("+++")
	// hangUpCommand is ATH with the CR that ends every command line.
	hangUpCommand = []byte("ATH\r")
	// activityStatusQuery is AT+CPAS, which asks whether a data call is up.
	activityStatusQuery = []byte("AT+CPAS\r")
)

const (
	// unknownCommand is what WithoutCPAS writes instead of AT+CPAS: a
	// command no handset knows, so that it answers ERROR.
	unknownCommand = "AT+NOSUCH\r"
	// transportBug is what the panicking switches panic with.
	transportBug = "a transport bug"
	// lostHangUp is the transport error FailsNextHangUp fails ATH with.
	lostHangUp = "USB write failed"
)

// FaultyTransport passes everything to the transport it wraps, except what
// its fault switches hold back, so that a test can make a handset misbehave
// on cue. Every switch starts off and may be flipped while the transport is
// in use; with all of them off, the FaultyTransport changes nothing.
//
// It implements satlink.Transport, satlink.DTRController and io.Closer, and
// is safe for concurrent use as far as the transport it wraps is. The zero
// FaultyTransport is not usable; create one with [NewFaultyTransport].
type FaultyTransport struct {
	// DeafToEscape makes "+++", written in one piece, vanish, as if the
	// handset ignored the escape.
	DeafToEscape atomic.Bool
	// DeafToDTR makes DTR changes vanish, as if the handset ignored them.
	DeafToDTR atomic.Bool
	// PanicsOnEscape makes writing "+++" panic with "a transport bug", as a
	// buggy transport might.
	PanicsOnEscape atomic.Bool
	// PanicsOnNextEscape makes the next "+++" panic with "a transport bug",
	// turning itself off as it does: whoever sends the next escape dies.
	PanicsOnNextEscape atomic.Bool
	// PanicsOnNextRead makes the next ReadTimeout panic with "a transport
	// bug", turning itself off as it does.
	PanicsOnNextRead atomic.Bool
	// FailsNextHangUp makes the next "ATH\r", written in one piece, fail with
	// a *satlink.Error of kind satlink.ErrTransport, "USB write failed",
	// turning itself off as it does: that ATH never reaches the handset.
	FailsNextHangUp atomic.Bool
	// WithoutCPAS makes "AT+CPAS\r", written in one piece, reach the handset
	// as a command it does not know, which it answers with ERROR, as older
	// firmware does.
	WithoutCPAS atomic.Bool
	// Unplugged makes every call but Close fail with a *satlink.Error of
	// kind satlink.ErrTransport, "unplugged": nothing gets through any
	// more, DTR included, as if the cable had been pulled.
	Unplugged atomic.Bool

	transport satlink.Transport
	dtrDrops  atomic.Int64
}

// A FaultyTransport is a handset's port, faults and all.
var (
	_ satlink.Transport     = (*FaultyTransport)(nil)
	_ satlink.DTRController = (*FaultyTransport)(nil)
	_ io.Closer             = (*FaultyTransport)(nil)
)

// NewFaultyTransport returns a FaultyTransport over transport, usually a
// *sim.Handset, with every switch off. DTR changes get through only if
// transport implements satlink.DTRController; without it, RaiseDTR and
// DropDTR fail as satlink.Handset does ("this transport has no DTR line").
func NewFaultyTransport(transport satlink.Transport) *FaultyTransport {
	return &FaultyTransport{transport: transport}
}

// WriteAll writes p to the wrapped transport, unless a switch holds it
// back: Unplugged fails it; PanicsOnEscape and PanicsOnNextEscape panic on
// "+++", and DeafToEscape drops it while reporting success;
// FailsNextHangUp fails "ATH\r"; and WithoutCPAS turns "AT+CPAS\r" into a
// command the handset does not know.
func (f *FaultyTransport) WriteAll(p []byte) error {
	if err := f.checkPluggedIn(); err != nil {
		return err
	}
	switch {
	case bytes.Equal(p, escapeSequence):
		return f.writeEscape(p)
	case bytes.Equal(p, hangUpCommand) && f.FailsNextHangUp.CompareAndSwap(true, false):
		return &satlink.Error{Kind: satlink.ErrTransport, Message: lostHangUp}
	case bytes.Equal(p, activityStatusQuery) && f.WithoutCPAS.Load():
		return f.transport.WriteAll([]byte(unknownCommand))
	default:
		return f.transport.WriteAll(p)
	}
}

// writeEscape writes escape, the "+++" of a hang-up, unless a switch holds
// it back.
func (f *FaultyTransport) writeEscape(escape []byte) error {
	if f.PanicsOnEscape.Load() || f.PanicsOnNextEscape.CompareAndSwap(true, false) {
		panic(transportBug)
	}
	if f.DeafToEscape.Load() {
		return nil
	}
	return f.transport.WriteAll(escape)
}

// ReadTimeout reads from the wrapped transport, unless Unplugged fails it
// or PanicsOnNextRead panics.
func (f *FaultyTransport) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	if err := f.checkPluggedIn(); err != nil {
		return 0, err
	}
	if f.PanicsOnNextRead.CompareAndSwap(true, false) {
		panic(transportBug)
	}
	return f.transport.ReadTimeout(p, timeout)
}

// RaiseDTR raises the wrapped transport's DTR line, unless Unplugged fails
// it or DeafToDTR drops it while reporting success.
func (f *FaultyTransport) RaiseDTR() error {
	return f.changeDTR(satlink.DTRController.RaiseDTR)
}

// DropDTR drops the wrapped transport's DTR line, unless Unplugged fails it
// or DeafToDTR drops it while reporting success. Every call counts towards
// [FaultyTransport.DTRDrops].
func (f *FaultyTransport) DropDTR() error {
	f.dtrDrops.Add(1)
	return f.changeDTR(satlink.DTRController.DropDTR)
}

// DTRDrops returns how many times DropDTR has been called, whatever the
// switches did with the drops: proof that a hang-up took the DTR path.
func (f *FaultyTransport) DTRDrops() int {
	return int(f.dtrDrops.Load())
}

// Close closes the wrapped transport if it implements io.Closer, whatever
// the switches say, as pulling a cable cannot stop a port from closing.
// Closing a simulator drops its DTR line from inside, which ends its call
// even while DeafToDTR is on, as closing a real port would.
func (f *FaultyTransport) Close() error {
	closer, ok := f.transport.(io.Closer)
	if !ok {
		return nil
	}
	return closer.Close()
}

// checkPluggedIn fails while Unplugged is on.
func (f *FaultyTransport) checkPluggedIn() error {
	if f.Unplugged.Load() {
		return &satlink.Error{Kind: satlink.ErrTransport, Message: "unplugged"}
	}
	return nil
}

// changeDTR makes change to the wrapped transport's DTR line, unless
// Unplugged fails it or DeafToDTR drops it while reporting success.
func (f *FaultyTransport) changeDTR(change func(satlink.DTRController) error) error {
	if err := f.checkPluggedIn(); err != nil {
		return err
	}
	if f.DeafToDTR.Load() {
		return nil
	}
	line, err := dtrLineOf(f.transport)
	if err != nil {
		return err
	}
	return change(line)
}
