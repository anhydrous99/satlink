package mobile

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/anhydrous99/satlink/internal/logtest"
	"github.com/anhydrous99/satlink/internal/simtest"
)

func TestAPortCloserThatPanicsLogsItAndTheAppCarriesOn(t *testing.T) {
	// No t.Parallel: logtest.Record replaces the process-wide logger.
	records := logtest.Record(t, logPackage, slog.LevelError)
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
	want := logtest.Line{Level: slog.LevelError, Text: "the port closer panicked: " + bugWhileClosing}
	simtest.Eventually(t, portPatience, "the port closer logs its panic", func() bool {
		return slices.Contains(records(), want)
	})
}
