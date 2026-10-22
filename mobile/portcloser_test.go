package mobile

import (
	"slices"
	"testing"

	"github.com/anhydrous99/satlink/internal/simtest"
)

func TestAPortCloserThatPanicsLogsItAndTheAppCarriesOn(t *testing.T) {
	// No t.Parallel: recordErrors replaces the process-wide logger.
	records := recordErrors(t)
	port := &closePanickingPort{closingPort: newClosingPortWith(t, simtest.SimulatorConfig())}
	handset, err := initializedHandset(port)
	if err != nil {
		t.Fatalf("initializedHandset: %v", err)
	}
	config := linkConfig(t, NewLinkSettings())
	// The simulator's short escape guard keeps the hang-up short.
	config.Call.EscapeGuard = simtest.CallEscapeGuard
	link, err := establishOwnLink(handset, config, nil)
	if err != nil {
		t.Fatalf("establishOwnLink: %v", err)
	}
	HangUpAtCleanup(t, link)
	closeAfterCall(link.watcher, handset)

	assertHungUpCleanly(t, link)

	// Unrecovered, the panic would end the test binary, as it would the app.
	want := "the port closer panicked: " + bugWhileClosing
	simtest.Eventually(t, portPatience, "the port closer logs its panic", func() bool {
		return slices.Contains(records(), want)
	})
}
