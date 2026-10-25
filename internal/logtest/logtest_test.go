package logtest_test

import (
	"log"
	"log/slog"
	"slices"
	"testing"

	"github.com/anhydrous99/satlink/internal/logtest"
)

// These tests set the process-wide logger, so they do not run in parallel.

func TestInstallLeavesTheLoggersAsTheyWere(t *testing.T) {
	previous, output, flags := slog.Default(), log.Writer(), log.Flags()

	t.Run("installing", func(t *testing.T) { logtest.Install(t, slog.DiscardHandler) })

	if slog.Default() != previous || log.Writer() != output || log.Flags() != flags {
		t.Error("the loggers still write to the handler of a finished test")
	}
}

func TestRecordKeepsThePackagesLinesFromTheMinimumUp(t *testing.T) {
	lines := logtest.Record(t, "sim", slog.LevelInfo)

	slog.Info("simulated gateway: IPv4 up", "pkg", "sim", "mtu", 1500)
	slog.Debug("below the minimum", "pkg", "sim")
	slog.Warn("another package's", "pkg", "satnet")
	slog.Warn("no package's")

	want := []logtest.Line{{Level: slog.LevelInfo, Text: "simulated gateway: IPv4 up mtu=1500"}}
	if got := lines(); !slices.Equal(got, want) {
		t.Errorf("recorded %+v, want %+v", got, want)
	}
}
