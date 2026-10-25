package sim

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/anhydrous99/satlink/internal/logtest"
)

func TestTheLogHelpersLogAtTheirLevelWithThePackageAttribute(t *testing.T) {
	lines := logtest.Record(t, logPackage, slog.LevelDebug)

	logDebug("simulated gateway: debug")
	logInfo("simulated gateway: info")
	logWarn("simulated gateway: warning")

	want := []logtest.Line{
		{Level: slog.LevelDebug, Text: "simulated gateway: debug"},
		{Level: slog.LevelInfo, Text: "simulated gateway: info"},
		{Level: slog.LevelWarn, Text: "simulated gateway: warning"},
	}
	if got := lines(); !slices.Equal(got, want) {
		t.Errorf("recorded %+v; want %+v", got, want)
	}
}
