package satnet

import (
	"errors"
	"log/slog"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/ppp"
	"github.com/anhydrous99/satlink/sim"
)

// The driver over a real data call to the simulated handset: with PPP's
// timers short enough to see them fire, and a line that loses, refuses or
// stops hearing PPP frames on cue — what the external tests cannot reach,
// as Config does not set PPP's timers.

// frameFlag begins every PPP frame (RFC 1662 §3.1), and so everything the
// driver writes once the call is up; AT commands and the escape do not.
const frameFlag = 0x7E

// lostFrame is the transport error of a PPP frame the line refuses.
const lostFrame = "USB write failed"

// shortRestart is PPP's restart timer in these tests: the simulator answers
// within milliseconds, so a request it never received is sent again soon,
// and ten tries still allow for a loaded machine.
const shortRestart = 200 * time.Millisecond

// shortKeepalive probes a silent gateway in these tests: a probe after a
// quarter of a second without a word, and the gateway declared gone after
// two probes go unanswered.
var shortKeepalive = ppp.Keepalive{Interval: 250 * time.Millisecond, MaxMissed: 2}

// testNegotiationTimeout bounds how long PPP may take to come up in these
// tests: long against PPP over the simulator, short against the default.
const testNegotiationTimeout = 5 * time.Second

// faultyLine passes everything between a data call and the simulated
// handset it wraps, but for PPP frames on cue: it loses the next one, fails
// every one, or hears nothing from the gateway.
type faultyLine struct {
	*sim.Handset
	// losesNextFrame makes the next PPP frame vanish as if sent, turning
	// itself off as it does.
	losesNextFrame atomic.Bool
	// refusesFrames makes writing a PPP frame fail with a transport error.
	refusesFrames atomic.Bool
	// deaf makes what the gateway sends vanish, as if it went silent.
	deaf atomic.Bool
	// lostFrames counts the frames lost.
	lostFrames atomic.Int64
}

// newFaultyLine returns a line, with every fault off, to a new simulated
// handset, which the test's end closes.
func newFaultyLine(t *testing.T) *faultyLine {
	t.Helper()
	return &faultyLine{Handset: simtest.NewSimulator(t, simtest.SimulatorConfig())}
}

func (l *faultyLine) WriteAll(p []byte) error {
	if len(p) > 0 && p[0] == frameFlag {
		if l.refusesFrames.Load() {
			return &iridium.Error{Kind: iridium.ErrTransport, Message: lostFrame}
		}
		if l.losesNextFrame.CompareAndSwap(true, false) {
			l.lostFrames.Add(1)
			return nil
		}
	}
	return l.Handset.WriteAll(p)
}

func (l *faultyLine) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	received, err := l.Handset.ReadTimeout(p, timeout)
	if l.deaf.Load() {
		return 0, err
	}
	return received, err
}

// testLinkConfig returns a configuration for a link to the simulator: an
// escape guard above its own, and PPP given testNegotiationTimeout.
func testLinkConfig() Config {
	config := NewConfig(DenyAll())
	config.Call.EscapeGuard = simtest.CallEscapeGuard
	config.Tuning.NegotiationTimeout = testNegotiationTimeout
	return config
}

// driverOver dials a data call over line and starts a driver for it, with
// testLinkConfig and a PPP endpoint as Establish makes one, changed by
// adjust; it returns the engine the driver shares, and its outcome. The
// test's end closes the link and waits for the hang-up.
func driverOver(t *testing.T, line iridium.Transport, adjust func(*ppp.Config)) (*shared, *driverOutcome) {
	t.Helper()
	config := testLinkConfig()
	pppConfig := pppClientConfig(&config)
	adjust(&pppConfig)
	endpoint, err := ppp.NewEndpoint(pppConfig)
	if err != nil {
		t.Fatal(err)
	}
	call, err := simtest.InitializedHandset(t, line).DialData(config.Number, config.Call)
	if err != nil {
		t.Fatal(err)
	}
	sharedEngine, outcome := startDriver(call, endpoint, &config)
	t.Cleanup(func() {
		sharedEngine.requestClose()
		// What the test needed is checked; a call this hang-up leaves up
		// ends with the simulator's Close.
		_ = outcome.wait()
	})
	return sharedEngine, outcome
}

// asEstablished leaves PPP's configuration as Establish makes it.
func asEstablished(*ppp.Config) {}

// awaitLinkClosing waits until the link driven over sharedEngine starts
// closing, and returns why, failing the test if it does not within
// waitBudget.
func awaitLinkClosing(t *testing.T, sharedEngine *shared) CloseReason {
	t.Helper()
	var reason CloseReason
	closing := sharedEngine.waitFor(time.Now().Add(waitBudget), func(e *engine) bool {
		reason = e.state.Reason
		return e.state.Kind == LinkStateClosing || e.state.Kind == LinkStateClosed
	})
	if !closing {
		t.Fatal("the link did not close")
	}
	return reason
}

// refusedLine is why a link ends whose line refused a PPP frame.
var refusedLine = CloseReason{Kind: CloseReasonTransport, Detail: "transport error: " + lostFrame}

func TestALostConfigureRequestIsSentAgain(t *testing.T) {
	line := newFaultyLine(t)
	// The first frame the driver writes is its LCP Configure-Request.
	line.losesNextFrame.Store(true)
	sharedEngine, _ := driverOver(t, line, func(c *ppp.Config) {
		c.Timers.Restart = shortRestart
		c.Keepalive = nil
	})

	if err := awaitUp(sharedEngine); err != nil {
		t.Fatalf("the link does not come up: %v", err)
	}

	if lost := line.lostFrames.Load(); lost != 1 {
		t.Errorf("%d frames lost, want the first request", lost)
	}
}

func TestTheKeepaliveNoticesAGatewayThatWentSilent(t *testing.T) {
	line := newFaultyLine(t)
	sharedEngine, _ := driverOver(t, line, func(c *ppp.Config) {
		keepalive := shortKeepalive
		c.Keepalive = &keepalive
	})
	if err := awaitUp(sharedEngine); err != nil {
		t.Fatalf("the link does not come up: %v", err)
	}

	line.deaf.Store(true)
	// Before the test's hang-up, which must hear the handset's answers.
	t.Cleanup(func() { line.deaf.Store(false) })

	reason := awaitLinkClosing(t, sharedEngine)
	// So must the driver's own.
	line.deaf.Store(false)
	want := CloseReason{Kind: CloseReasonPPP, PPP: DownReason{Kind: ppp.DownReasonPeerUnresponsive}}
	if reason != want {
		t.Errorf("the link closed for %v, want %v", reason, want)
	}
}

func TestALineThatRefusesTheFirstFrameEndsTheLink(t *testing.T) {
	line := newFaultyLine(t)
	line.refusesFrames.Store(true)
	sharedEngine, outcome := driverOver(t, line, asEstablished)

	err := awaitUp(sharedEngine)

	var linkErr *Error
	if !errors.As(err, &linkErr) || linkErr.Kind != ErrLinkDown || linkErr.Reason != refusedLine {
		t.Errorf("awaitUp = %v, want the link down for the refused frame", err)
	}
	if err := outcome.wait(); err != nil {
		t.Errorf("the hang-up failed: %v", err)
	}
}

func TestALineThatRefusesALaterFrameEndsTheLink(t *testing.T) {
	line := newFaultyLine(t)
	sharedEngine, _ := driverOver(t, line, asEstablished)
	if err := awaitUp(sharedEngine); err != nil {
		t.Fatalf("the link does not come up: %v", err)
	}
	line.refusesFrames.Store(true)

	// A connection's SYN is the next frame the driver writes.
	err := sharedEngine.withLock(func(e *engine) error {
		_, err := e.stack.openTCP(netip.AddrPortFrom(ipv4(192, 0, 2, 7), 7))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if reason := awaitLinkClosing(t, sharedEngine); reason != refusedLine {
		t.Errorf("the link closed for %v, want %v", reason, refusedLine)
	}
}

// awaitNoCarrier reads from call until the handset says NO CARRIER, failing
// the test if it does not within waitBudget.
func awaitNoCarrier(t *testing.T, call *iridium.DataCall) {
	t.Helper()
	buffer := make([]byte, readChunk)
	simtest.Eventually(t, waitBudget, "the handset says NO CARRIER", func() bool {
		// What counts is whether the call saw NO CARRIER, which a failed read
		// leaves as it was.
		_, _ = call.ReadTimeout(buffer, pollInterval)
		return call.CarrierLost()
	})
}

func TestPPPOutputAfterNoCarrierIsDroppedWithoutFailingTheLink(t *testing.T) {
	line := newFaultyLine(t)
	config := testLinkConfig()
	call, err := simtest.InitializedHandset(t, line).DialData(config.Number, config.Call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// What the test needed is checked; a call this hang-up leaves up ends
		// with the simulator's Close.
		_ = call.HangUp()
	})
	line.Control().DropCall()
	awaitNoCarrier(t, call)
	// PPP has its first Configure-Request to send.
	endpoint := clientEndpoint(t)
	endpoint.Open(time.Now())
	d := &driver{call: call, ppp: endpoint, pacer: newPacer(DefaultTuning().LinkRate, time.Now())}

	// The handset, in command mode again, would read the frame as commands.
	if err := d.writeOutput(); err != nil {
		t.Errorf("writeOutput once the call dropped = %v, want nil: the link would end for its transport, "+
			"not for the dropped call", err)
	}
}

// lastRecord returns the last of records, failing the test if there is
// none.
func lastRecord(t *testing.T, records []logRecord) logRecord {
	t.Helper()
	if len(records) == 0 {
		t.Fatal("nothing was logged")
	}
	return records[len(records)-1]
}

func TestAHangUpIsLogged(t *testing.T) {
	records := recordLogs(t, slog.LevelInfo)
	handset, _ := simtest.NewHandset(t, simtest.SimulatorConfig())
	link, err := Establish(handset, testLinkConfig())
	if err != nil {
		t.Fatal(err)
	}

	if err := link.Close(); err != nil {
		t.Fatal(err)
	}

	want := logRecord{slog.LevelInfo, logPackage, "link closed: closed by the app"}
	if got := lastRecord(t, records()); got != want {
		t.Errorf("logged %v, want %v", got, want)
	}
}

func TestAHangUpThatCannotBeConfirmedIsLoggedAsAWarning(t *testing.T) {
	records := recordLogs(t, slog.LevelWarn)
	faulty := simtest.NewFaultyTransport(simtest.NewSimulator(t, simtest.SimulatorConfig()))
	link, err := Establish(simtest.InitializedHandset(t, faulty), testLinkConfig())
	if err != nil {
		t.Fatal(err)
	}

	faulty.Unplugged.Store(true)

	// The link ends by itself, for the transport, before the app asks.
	if reason, ok := link.Watcher().WaitClosed(waitBudget); !ok || reason.Kind != CloseReasonTransport {
		t.Fatalf("WaitClosed() = %v, %t; want the link closed for the transport", reason, ok)
	}
	var closeErr *Error
	if err := link.Close(); !errors.As(err, &closeErr) || closeErr.Err == nil {
		t.Fatalf("Close() = %v, want the hang-up's failure", err)
	}
	want := logRecord{slog.LevelWarn, logPackage, "link closed (handset I/O failed: transport error: unplugged), " +
		"but the hang-up was not confirmed, so the call may still be up: " + closeErr.Err.Error()}
	if got := lastRecord(t, records()); got != want {
		t.Errorf("logged %v, want %v", got, want)
	}
}
