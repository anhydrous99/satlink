package simtest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/sim"
)

// handsetTimeout is how long the handsets of these tests wait for an
// answer.
const handsetTimeout = 500 * time.Millisecond

// readAll reads from transport until it has been quiet for a moment.
func readAll(t *testing.T, transport satlink.Transport) string {
	t.Helper()
	var received strings.Builder
	buffer := make([]byte, 256)
	for {
		count, err := transport.ReadTimeout(buffer, 50*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			return received.String()
		}
		received.Write(buffer[:count])
	}
}

func TestSimulatorConfigIsTheDefaultWithAShortEscapeGuard(t *testing.T) {
	t.Parallel()
	defaults := sim.DefaultConfig()

	config := SimulatorConfig()

	if config.EscapeGuard != 20*time.Millisecond || CallEscapeGuard != 60*time.Millisecond {
		t.Errorf("guards %v and %v; want 20ms and 60ms", config.EscapeGuard, CallEscapeGuard)
	}
	if config.Model != defaults.Model || config.IMEI != defaults.IMEI || config.Link != defaults.Link {
		t.Errorf("SimulatorConfig() = %+v; want the defaults but for the guard", config)
	}
}

func TestNewSimulatorGivesAHandsetThatClosesWithTheTest(t *testing.T) {
	var simulator *sim.Handset
	t.Run("using it", func(t *testing.T) {
		simulator = NewSimulator(t, SimulatorConfig())

		if err := simulator.WriteAll([]byte("AT\r")); err != nil {
			t.Fatal(err)
		}
		if got := readAll(t, simulator); got != "AT\r\r\nOK\r\n" {
			t.Errorf("the simulator answered %q", got)
		}
	})

	err := simulator.WriteAll([]byte("AT\r"))

	var handsetErr *satlink.Error
	if !errors.As(err, &handsetErr) || handsetErr.Message != "the simulated handset is closed" {
		t.Errorf("WriteAll after the test = %v; want the simulator closed", err)
	}
}

func TestNewHandsetIsInitialisedOverASimulator(t *testing.T) {
	t.Parallel()
	config := SimulatorConfig()
	config.SignalBars = 2

	handset, control := NewHandset(t, config)

	bars, err := handset.SignalQuality()
	if err != nil || bars != 2 {
		t.Errorf("SignalQuality() = %d, %v; want 2", bars, err)
	}
	if stats := control.Stats(); stats.CallsPlaced != 0 {
		t.Errorf("the control reports %+v; want a fresh simulator", stats)
	}
}

func TestInitializedHandsetFailsTheTestWhenTheHandsetCannotBeInitialised(t *testing.T) {
	t.Parallel()
	unplugged := NewFaultyTransport(NewSimulator(t, SimulatorConfig()))
	unplugged.Unplugged.Store(true)
	recorder := &failureRecorder{TB: t}

	InitializedHandset(recorder, unplugged)

	if len(recorder.failures) != 1 || !strings.HasPrefix(recorder.failures[0], "initialising the handset: ") {
		t.Errorf("failures %q; want one about initialising", recorder.failures)
	}
}

func TestAFaultyTransportOverASimulatorIsDeafToTheEscape(t *testing.T) {
	t.Parallel()
	simulator := NewSimulator(t, SimulatorConfig())
	faulty := NewFaultyTransport(simulator)
	faulty.DeafToEscape.Store(true)
	handset := InitializedHandset(t, faulty)
	// Short, as in the Rust tests' faulty handset: an escape the handset
	// ignores is followed by waits for its answer to ATH.
	handset.SetTimeout(handsetTimeout)
	settings := satlink.DefaultDataCallSettings()
	settings.EscapeGuard = CallEscapeGuard
	call, err := handset.DialData("008816000025", settings)
	if err != nil {
		t.Fatal(err)
	}
	faulty.DeafToDTR.Store(true)

	hangUpErr := call.HangUp()

	if !errors.Is(hangUpErr, satlink.ErrCallMayStillBeUp) || !simulator.Control().Stats().InDataCall {
		t.Errorf("HangUp = %v with the call up: %v; want a call that could not be ended",
			hangUpErr, simulator.Control().Stats().InDataCall)
	}
	if faulty.DTRDrops() == 0 {
		t.Error("the hang-up never tried DTR")
	}
}

func TestAFaultyTransportWithoutCPASOverASimulatorGetsERRORForIt(t *testing.T) {
	t.Parallel()
	faulty := NewFaultyTransport(NewSimulator(t, SimulatorConfig()))
	handset := InitializedHandset(t, faulty)
	faulty.WithoutCPAS.Store(true)

	_, err := handset.Command("AT+CPAS")

	if !errors.Is(err, satlink.ErrCommandFailed) {
		t.Errorf("AT+CPAS = %v; want ERROR, as older firmware answers", err)
	}
}
