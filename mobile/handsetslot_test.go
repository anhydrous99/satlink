package mobile

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/sim"
)

// In the Rust bindings a link owned the handset it borrowed, and with it
// the port, until the call had ended: dropping the IridiumHandset meanwhile
// could not close the port the link hangs up through. These tests hold
// IridiumHandset.Close to the same.

// callPatience is how long a test waits for a call that must not block.
const callPatience = 10 * time.Second

// closableHandset returns an IridiumHandset over a port that records its
// closing, as one OpenSerialHandset made.
func closableHandset(t *testing.T) (*IridiumHandset, *closingPort) {
	t.Helper()
	return closableHandsetWith(t, sim.DefaultConfig())
}

// closableHandsetWith returns closableHandset's handset and port over a
// simulated handset made with config.
func closableHandsetWith(t *testing.T, config sim.Config) (*IridiumHandset, *closingPort) {
	t.Helper()
	port := newClosingPortWith(t, config)
	handset, err := newIridiumHandset(port)
	if err != nil {
		t.Fatalf("newIridiumHandset: %v", err)
	}
	return handset, port
}

// assertHungUpCleanly hangs link up and fails the test unless the hang-up
// was confirmed, which needs the port to be open.
func assertHungUpCleanly(t *testing.T, link *SatelliteLink) {
	t.Helper()
	state, err := link.HangUp(linkTimeoutMillis)
	if err != nil || state.Kind != LinkStateClosed || link.Status().CallMayStillBeUp {
		t.Fatalf("HangUp = %+v, %v (call perhaps up: %t), want a confirmed hang-up",
			state, err, link.Status().CallMayStillBeUp)
	}
}

// setupOutcome is how setting a link up in another goroutine went.
type setupOutcome struct {
	link *SatelliteLink
	err  error
}

// awaitLink returns the link that another goroutine set up, failing the
// test unless it came up within callPatience.
func awaitLink(t *testing.T, outcome <-chan setupOutcome) *SatelliteLink {
	t.Helper()
	select {
	case result := <-outcome:
		if result.err != nil {
			t.Fatalf("setting the link up: %v", result.err)
		}
		return result.link
	case <-time.After(callPatience):
		t.Fatalf("the link was not set up within %v", callPatience)
		return nil
	}
}

// returnsWithin runs call in another goroutine and returns its error,
// failing the test unless it returns within callPatience; what names the
// call in that failure.
func returnsWithin(t *testing.T, what string, call func() error) error {
	t.Helper()
	returned := make(chan error, 1)
	go func() { returned <- call() }()
	select {
	case err := <-returned:
		return err
	case <-time.After(callPatience):
		t.Fatalf("%s did not return within %v", what, callPatience)
		return nil
	}
}

func TestClosingALentHandsetLeavesItsPortToTheLinkUntilTheCallEnds(t *testing.T) {
	t.Parallel()
	handset, port := closableHandset(t)
	link, err := EstablishLinkWithHandset(handset, NewLinkSettings(), nil, nil)
	if err != nil {
		t.Fatalf("EstablishLinkWithHandset: %v", err)
	}
	HangUpAtCleanup(t, link)

	for range 2 {
		if err := handset.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	if port.closed.Load() {
		t.Fatal("closing the handset closed the port under its link")
	}
	assertHungUpCleanly(t, link)
	simtest.Eventually(t, portPatience, "the port closes once the call has ended", port.closed.Load)
}

func TestClosingAHandsetWhileItsLinkIsSetUpClosesItOnceTheCallEnds(t *testing.T) {
	t.Parallel()
	config := sim.DefaultConfig()
	config.Link.CallSetup = SlowCallSetup
	handset, port := closableHandsetWith(t, config)
	established := make(chan setupOutcome, 1)
	go func() {
		link, err := EstablishLinkWithHandset(handset, NewLinkSettings(), nil, nil)
		established <- setupOutcome{link: link, err: err}
	}()
	simtest.Eventually(t, callPatience, "the link is being set up", func() bool {
		return port.control.Stats().CallsPlaced == 1
	})

	if err := handset.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	link := awaitLink(t, established)
	HangUpAtCleanup(t, link)
	if port.closed.Load() {
		t.Fatal("closing the handset closed the port under the link being set up")
	}
	assertHungUpCleanly(t, link)
	simtest.Eventually(t, portPatience, "the port closes once the call has ended", port.closed.Load)
}

func TestClosingAHandsetDoesNotWaitForACommandInProgress(t *testing.T) {
	t.Parallel()
	handset, port := closableHandset(t)
	polled := make(chan error, 1)
	go func() {
		// Only the port closing ends a wait for events this long.
		_, err := handset.PollEvents(math.MaxInt64)
		polled <- err
	}()
	simtest.Eventually(t, callPatience, "the poll has the handset", func() bool {
		if handset.mu.TryLock() {
			handset.mu.Unlock()
			return false
		}
		return true
	})

	err := returnsWithin(t, "Close during a poll", handset.Close)

	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !port.closed.Load() {
		t.Error("Close left the port open")
	}
	pollErr := returnsWithin(t, "the poll on the closed port", func() error { return <-polled })
	if details := AsSatlinkError(pollErr); details == nil || details.Kind != SatlinkErrorTransport {
		t.Errorf("the poll fails with %v, want the closed port's SatlinkErrorTransport", pollErr)
	}
}

func TestAFaultWhileTakingTheHandsetBackLeavesItsLockFree(t *testing.T) {
	// A lent handset with no link to follow: taking it back panics.
	handset := &IridiumHandset{handset: satlink.NewHandset(newForeignTransport(&recordingTransport{})), slot: slotLent}

	for attempt := range 2 {
		err := returnsWithin(t, "SignalQuality after a fault", func() error {
			_, err := handset.SignalQuality()
			return err
		})

		if details := AsSatlinkError(err); details == nil || details.Kind != SatlinkErrorInternal {
			t.Errorf("attempt %d fails with %v, want SatlinkErrorInternal", attempt+1, err)
		}
	}
}

func TestStrandingTakesOnlyAReadyHandset(t *testing.T) {
	// A handset lent or being lent stays with its link, which reports how
	// its own call ended.
	cases := map[handsetSlot]handsetSlot{
		slotHere:     slotStranded,
		slotStranded: slotStranded,
		slotDialling: slotDialling,
		slotLent:     slotLent,
	}
	for slot, want := range cases {
		handset := &IridiumHandset{slot: slot}

		handset.strand()

		if handset.slot != want {
			t.Errorf("stranding a handset %v leaves it %v, want %v", slot, handset.slot, want)
		}
	}
}

func TestAbandoningASetupTakesOnlyAHandsetBeingLent(t *testing.T) {
	// A handset that is not being lent any more is no setup's to abandon.
	cases := map[handsetSlot]handsetSlot{
		slotHere:     slotHere,
		slotStranded: slotStranded,
		slotDialling: slotStranded,
		slotLent:     slotLent,
	}
	for slot, want := range cases {
		handset := &IridiumHandset{slot: slot}

		handset.abandonDialling()

		if handset.slot != want {
			t.Errorf("abandoning the setup of a handset %v leaves it %v, want %v", slot, handset.slot, want)
		}
	}
}

func TestTheSlotsAreNamedAsTheRustBindingsNameThem(t *testing.T) {
	cases := map[handsetSlot]string{
		slotHere:        "Here",
		slotStranded:    "Stranded",
		slotDialling:    "Dialling",
		slotLent:        "Lent",
		handsetSlot(-1): "handsetSlot(-1)",
		handsetSlot(9):  "handsetSlot(9)",
	}
	for slot, want := range cases {
		if got := slot.String(); got != want {
			t.Errorf("handsetSlot(%d).String() = %q, want %q", int(slot), got, want)
		}
	}
}

// lendForSetup lends handset to a link being set up, as
// EstablishLinkWithHandset does, failing the test if it cannot, and returns
// the setup's loan.
func lendForSetup(t *testing.T, handset *IridiumHandset) *handsetLoan {
	t.Helper()
	loan, err := handset.lend()
	if err != nil {
		t.Fatalf("lend: %v", err)
	}
	return loan
}

// errNoCarrier is why the links of these tests do not come up.
var errNoCarrier = errors.New("NO CARRIER")

func TestClosingAHandsetWhoseLinkDoesNotComeUpClosesItThen(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*handsetLoan){
		"the link fails": func(loan *handsetLoan) { loan.returnedAfter(errNoCarrier) },
		// A panic leaves the loan unsettled for the cleanup to abandon.
		"setting the link up panics": (*handsetLoan).abandonUnlessSettled,
	}
	for name, endSetup := range cases {
		t.Run(name, func(t *testing.T) {
			handset, port := closableHandset(t)
			loan := lendForSetup(t, handset)
			if err := handset.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if port.closed.Load() {
				t.Fatal("the port closed while the link was being set up")
			}

			endSetup(loan)

			if !port.closed.Load() {
				t.Error("the port is still open after the link did not come up")
			}
		})
	}
}

// TestASettledSetupLeavesTheNextSetupAlone takes EstablishLinkWithHandset's
// steps in an order that two threads allow: a setup settles where the
// handset goes, the handset comes back, another thread's setup takes it,
// and the app closes the IridiumHandset, all before the cleanup that the
// first setup deferred runs. That cleanup must neither strand the handset
// nor close the port under the next setup, which dials through it.
func TestASettledSetupLeavesTheNextSetupAlone(t *testing.T) {
	t.Parallel()
	settles := map[string]func(*testing.T, *handsetLoan){
		"the link did not come up": func(_ *testing.T, loan *handsetLoan) {
			loan.returnedAfter(errNoCarrier)
		},
		"the link came up, then ended": func(t *testing.T, loan *handsetLoan) {
			// A link of its own stands in for the setup's: the slot follows
			// only the link's watcher.
			link := simulatedLink(t)
			loan.lentTo(link.watcher)
			assertHungUpCleanly(t, link)
		},
	}
	for name, settle := range settles {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			handset, port := closableHandset(t)
			first := lendForSetup(t, handset)
			settle(t, first)
			next := lendForSetup(t, handset)
			if err := handset.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			first.abandonUnlessSettled()

			if handset.slot != slotDialling || port.closed.Load() {
				t.Fatalf("the first setup's cleanup left the handset %v, its port closed: %t; want it still being lent, the port open",
					handset.slot, port.closed.Load())
			}
			next.returnedAfter(errNoCarrier)
			if handset.slot != slotHere || !port.closed.Load() {
				t.Errorf("once the next setup failed, the handset is %v, its port closed: %t; want it back and the port closed",
					handset.slot, port.closed.Load())
			}
		})
	}
}
