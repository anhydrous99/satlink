package netlog

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/anhydrous99/satlink/internal/logtest"
)

func TestTraceAndDebugLogAtTheirLevelsWithThePackageAttribute(t *testing.T) {
	lines := logtest.Record(t, "ipstack", slog.LevelDebug-4)

	Trace("tcp: state changed", "to", "ESTABLISHED")
	Debug("iface: malformed packet")

	want := []logtest.Line{
		{Level: slog.LevelDebug - 4, Text: "tcp: state changed to=ESTABLISHED"},
		{Level: slog.LevelDebug, Text: "iface: malformed packet"},
	}
	if got := lines(); !slices.Equal(got, want) {
		t.Errorf("logged %+v, want %+v", got, want)
	}
}
