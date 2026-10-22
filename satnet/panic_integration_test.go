package satnet_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/satnet"
	"github.com/anhydrous99/satlink/sim"
)

// Whatever panics in the link's driver, the link reports itself
// closed, the handset is released, and the process is kept alive; a call
// that a panic kept from being hung up is still hung up. Unwinding the
// driver would drop the transport, whose DTR drop ends the call; here the
// transport stays with the app, so the driver must hang up itself.
// A hang-up that itself panics cannot be run again: the core pulses DTR as
// the panic leaves it, which ends the call after AT&D2, but nothing confirms
// that, so the link reports that the call may still be up — for the app to
// end it where DTR reaches nothing.

// expectThePanickedDriversError checks that err is Close's error once a
// panic got past the driver.
func expectThePanickedDriversError(t *testing.T, err error) {
	t.Helper()
	var linkErr *satnet.Error
	if !errors.As(err, &linkErr) || linkErr.Kind != satnet.ErrLinkDown || !linkErr.CallMayStillBeUp ||
		linkErr.Reason != (satnet.CloseReason{Kind: satnet.CloseReasonInternal, Detail: "the link's driver thread panicked"}) {
		t.Errorf("Close() = %#v, want the panicked driver's error", err)
	}
}

// expectHungUpAndReleased checks that the call has ended and that the
// handset is released: what a link reported closed promises, panic or not.
func expectHungUpAndReleased(t *testing.T, handset *iridium.Handset, control *sim.Control) {
	t.Helper()
	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
	expectNotBusy(t, handset)
}

// brokenError is an error whose Error method fails on a nil receiver, as a
// careless transport's might: a panic carrying a nil *brokenError cannot
// describe itself.
type brokenError struct {
	text string
}

func (e *brokenError) Error() string { return e.text }

// panicWithABrokenError panics with a nil *brokenError.
func panicWithABrokenError() {
	var broken *brokenError
	panic(broken)
}

// brokenPanicTransport passes everything to the simulated handset it wraps,
// but panics with a nil *brokenError on cue: on its next read, or on every
// escape a hang-up writes. Its DTR line reaches nothing, as an adapter's
// may not, so that only the app can end a call that a panic left up.
type brokenPanicTransport struct {
	*sim.Handset
	panicsOnNextRead atomic.Bool
	panicsOnEscape   atomic.Bool
}

// RaiseDTR reaches nothing: see brokenPanicTransport.
func (b *brokenPanicTransport) RaiseDTR() error { return nil }

// DropDTR reaches nothing: see brokenPanicTransport.
func (b *brokenPanicTransport) DropDTR() error { return nil }

func (b *brokenPanicTransport) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	if b.panicsOnNextRead.CompareAndSwap(true, false) {
		panicWithABrokenError()
	}
	return b.Handset.ReadTimeout(p, timeout)
}

func (b *brokenPanicTransport) WriteAll(p []byte) error {
	if b.panicsOnEscape.Load() && bytes.Equal(p, []byte("+++")) {
		panicWithABrokenError()
	}
	return b.Handset.WriteAll(p)
}

// brokenPanicHandset returns a handset whose transport panics with a broken
// error on cue, the transport, and the simulator's control.
func brokenPanicHandset(t *testing.T) (*iridium.Handset, *brokenPanicTransport, *sim.Control) {
	t.Helper()
	simulator := simtest.NewSimulator(t, simtest.SimulatorConfig())
	transport := &brokenPanicTransport{Handset: simulator}
	return simtest.InitializedHandset(t, transport), transport, simulator.Control()
}

func TestAPanicThatCannotDescribeItselfStillHangsUp(t *testing.T) {
	t.Parallel()
	handset, transport, control := brokenPanicHandset(t)
	link := establish(t, handset, linkConfig(t))

	transport.panicsOnNextRead.Store(true)

	reason, ok := link.Watcher().WaitClosed(timeout)
	if !ok || reason != (satnet.CloseReason{Kind: satnet.CloseReasonInternal, Detail: "the link driver panicked"}) {
		t.Errorf("WaitClosed() = %v, %t; want the panic that says nothing", reason, ok)
	}
	if link.Status().CallMayStillBeUp || control.Stats().InDataCall {
		t.Error("the call was not hung up")
	}
	if err := link.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	expectNotBusy(t, handset)
}

func TestAPanicThatCannotDescribeItselfWhileHangingUpSparesTheProcess(t *testing.T) {
	t.Parallel()
	handset, transport, control := brokenPanicHandset(t)
	link := establish(t, handset, linkConfig(t))
	transport.panicsOnEscape.Store(true)

	err := link.Close()

	expectThePanickedDriversError(t, err)
	expectClosed(t, link.Watcher(), satnet.CloseReasonRequested)
	// Neither the escape nor the core's DTR pulse reached the handset, so
	// the call is still up: the app ends it, as the docs advise, through the
	// handset, which the link released.
	if !control.Stats().InDataCall {
		t.Fatal("the call is not up, so this shows nothing")
	}
	transport.panicsOnEscape.Store(false)
	if err := handset.EndDataCall(simtest.CallEscapeGuard); err != nil {
		t.Fatalf("EndDataCall() = %v", err)
	}
	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
}

// panicOnAClosingConnection is a log handler that, once armed, panics on
// the records ipstack logs as a TCP connection closes — as a buggy handler
// of the app's might, when the hang-up aborts the connections — and drops
// every other record. It disarms itself as it panics, unless it is broken
// for good.
type panicOnAClosingConnection struct {
	armed *atomic.Bool
	// brokenForGood keeps it armed once it has panicked: it then panics
	// whenever a connection closes.
	brokenForGood bool
}

func (h panicOnAClosingConnection) Enabled(context.Context, slog.Level) bool {
	return h.armed.Load()
}

func (h panicOnAClosingConnection) Handle(_ context.Context, record slog.Record) error {
	record.Attrs(func(attr slog.Attr) bool {
		if record.Message == "state" && attr.Key == "to" && attr.Value.Any() == tcp.StateClosed && h.fires() {
			panic("a log handler bug")
		}
		return true
	})
	return nil
}

// fires reports whether the handler panics on a closing connection: while
// it is armed, and only the once unless it is broken for good.
func (h panicOnAClosingConnection) fires() bool {
	if h.brokenForGood {
		return h.armed.Load()
	}
	return h.armed.CompareAndSwap(true, false)
}

func (h panicOnAClosingConnection) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h panicOnAClosingConnection) WithGroup(string) slog.Handler { return h }

// installLogHandler makes handler the process-wide logger until the test
// ends. Tests that call it must not run in parallel.
func installLogHandler(t *testing.T, handler slog.Handler) {
	t.Helper()
	previous, previousOutput, previousFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() {
		// SetDefault also points the log package at the handler; the
		// previous default does not undo that.
		slog.SetDefault(previous)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
}

// armPanicOnAClosingConnection installs, until the test ends, a logger that
// panics once, as the hang-up aborts the open connections, and arms it.
// Tests that call it must not run in parallel.
func armPanicOnAClosingConnection(t *testing.T) {
	t.Helper()
	installArmed(t, panicOnAClosingConnection{armed: &atomic.Bool{}})
}

// armPanicOnEveryClosingConnection installs, until the test ends, a logger
// that panics whenever the open connections are aborted — as the hang-up
// starts, and again as the driver abandons the link — and arms it. Tests
// that call it must not run in parallel.
func armPanicOnEveryClosingConnection(t *testing.T) {
	t.Helper()
	installArmed(t, panicOnAClosingConnection{armed: &atomic.Bool{}, brokenForGood: true})
}

// installArmed makes handler the process-wide logger until the test ends,
// and arms it.
func installArmed(t *testing.T, handler panicOnAClosingConnection) {
	t.Helper()
	installLogHandler(t, handler)
	handler.armed.Store(true)
}

// TestAPanicWhileTheHangUpAbortsTheConnectionsStillHangsUp is not parallel:
// it installs the process-wide logger. The link is reported closed only once
// the call that the panic kept from being hung up has been, and the handset
// released, so it checks them as WaitClosed returns, before Close.
func TestAPanicWhileTheHangUpAbortsTheConnectionsStillHangsUp(t *testing.T) {
	handset, control := simtest.NewHandset(t, simtest.SimulatorConfig())
	link := establish(t, handset, linkConfig(t, "echo.test:7"))
	expectEcho(t, dial(t, link, "echo.test", echoPort), []byte("still open"))
	armPanicOnAClosingConnection(t)

	link.RequestClose()

	expectClosed(t, link.Watcher(), satnet.CloseReasonRequested)
	expectHungUpAndReleased(t, handset, control)
	expectThePanickedDriversError(t, link.Close())
}

// TestTheFirstOfTwoPanicsIsWhyTheLinkClosed is not parallel: it installs the
// process-wide logger. Like the test above, it checks the call and the
// handset as WaitClosed returns.
func TestTheFirstOfTwoPanicsIsWhyTheLinkClosed(t *testing.T) {
	handset, control, faulty := faultyHandset(t, simtest.SimulatorConfig())
	link := establish(t, handset, linkConfig(t, "echo.test:7"))
	expectEcho(t, dial(t, link, "echo.test", echoPort), []byte("still open"))
	armPanicOnAClosingConnection(t)

	// The driver's read panics; then aborting the connections does.
	faulty.PanicsOnNextRead.Store(true)

	reason, ok := link.Watcher().WaitClosed(timeout)
	if !ok || reason != (satnet.CloseReason{Kind: satnet.CloseReasonInternal, Detail: "a transport bug"}) {
		t.Errorf("WaitClosed() = %v, %t; want the first panic", reason, ok)
	}
	expectHungUpAndReleased(t, handset, control)
	expectThePanickedDriversError(t, link.Close())
}

// TestAPanicEveryTimeTheConnectionsAreAbortedStillClosesTheLink is not
// parallel: it installs the process-wide logger. Should the driver's
// abandon let the second panic through, the test binary crashes.
func TestAPanicEveryTimeTheConnectionsAreAbortedStillClosesTheLink(t *testing.T) {
	handset, control := simtest.NewHandset(t, simtest.SimulatorConfig())
	link := establish(t, handset, linkConfig(t, "echo.test:7"))
	expectEcho(t, dial(t, link, "echo.test", echoPort), []byte("still open"))
	// Aborting the connections panics as the hang-up starts, and again as
	// the driver abandons the link.
	armPanicOnEveryClosingConnection(t)

	err := link.Close()

	expectThePanickedDriversError(t, err)
	expectClosed(t, link.Watcher(), satnet.CloseReasonRequested)
	if !link.Status().CallMayStillBeUp {
		t.Error("the status does not say the call may still be up")
	}
	expectHungUpAndReleased(t, handset, control)
}

// TestTheCallsDurationCountsTheHangUpThatFollowsAPanic is not parallel: it
// installs the process-wide logger, and it times the call. The hang-up that
// the driver runs once a panic has got past its run is airtime as any other,
// even when aborting the connections panics again as the driver abandons
// the link.
func TestTheCallsDurationCountsTheHangUpThatFollowsAPanic(t *testing.T) {
	for _, c := range []struct {
		name string
		arm  func(*testing.T)
	}{
		{"a panic as the hang-up starts", armPanicOnAClosingConnection},
		{"a panic whenever the connections are aborted", armPanicOnEveryClosingConnection},
	} {
		t.Run(c.name, func(t *testing.T) {
			config := linkConfig(t, "echo.test:7")
			config.Call.EscapeGuard = billingGuard
			link, _ := linkWith(t, config)
			established := time.Now()
			expectEcho(t, dial(t, link, "echo.test", echoPort), []byte("still open"))
			c.arm(t)

			expectThePanickedDriversError(t, link.Close())

			expectTheHangUpBilled(t, link, established)
		})
	}
}

// TestAHangUpThatPanicsAfterAnEarlierPanicSparesTheProcess is not parallel:
// it installs the process-wide logger. Should the driver let the hang-up's
// panic through, the test binary crashes.
func TestAHangUpThatPanicsAfterAnEarlierPanicSparesTheProcess(t *testing.T) {
	handset, control, faulty := faultyHandset(t, simtest.SimulatorConfig())
	link := establish(t, handset, linkConfig(t, "echo.test:7"))
	expectEcho(t, dial(t, link, "echo.test", echoPort), []byte("still open"))
	// Aborting the connections panics as the hang-up starts; the hang-up
	// the driver then runs itself panics on the escape.
	armPanicOnAClosingConnection(t)
	faulty.PanicsOnEscape.Store(true)

	err := link.Close()

	expectThePanickedDriversError(t, err)
	expectClosed(t, link.Watcher(), satnet.CloseReasonRequested)
	expectNotBusy(t, handset)
	// As in TestAPanicWhileHangingUpStillReportsTheLinkClosed, the core pulsed
	// DTR as the panic left the hang-up, which ended the call.
	if faulty.DTRDrops() < 1 || control.Stats().InDataCall {
		t.Errorf("DTR dropped %d times, and the call still up: %t; want the pulse to have ended the call",
			faulty.DTRDrops(), control.Stats().InDataCall)
	}
}
