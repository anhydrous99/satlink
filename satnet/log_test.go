package satnet

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
	"github.com/anhydrous99/satlink/internal/logtest"
)

func TestLogRecordsNameThePackageAndTheirLevel(t *testing.T) {
	records := logtest.Record(t, logPackage, slog.LevelDebug)

	logDebug("debug")
	logInfo("info")
	logWarn("warn")

	want := []logtest.Line{
		{Level: slog.LevelDebug, Text: "debug"},
		{Level: slog.LevelInfo, Text: "info"},
		{Level: slog.LevelWarn, Text: "warn"},
	}
	if got := records(); !slices.Equal(got, want) {
		t.Errorf("records %v, want %v", got, want)
	}
}

func TestADroppedRepeatIsLoggedAtDebugLevel(t *testing.T) {
	records := logtest.Record(t, logPackage, slog.LevelDebug)
	filter := newRepeatFilter(repeatWindow)
	syn := segment(1, wire.TCPControlSYN, "")
	filter.admit(syn, pacingEpoch, pacingEpoch)

	filter.admit(syn, pacingEpoch, pacingEpoch)

	if got := records(); len(got) != 1 || got[0].Level != slog.LevelDebug ||
		got[0].Text != "dropped an early repeat of Tcp { source_port: 49152, destination: 192.0.2.7, "+
			"destination_port: 7, sequence: 1, length: 0, syn: true, fin: false, acknowledgement: None, window: 1024 }" {
		t.Errorf("records %v", got)
	}
}
