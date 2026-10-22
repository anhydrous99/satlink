package logtext_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/anhydrous99/satlink/internal/logtext"
)

func TestLevelNamesMatchTheirBands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		level slog.Level
		want  string
	}{
		{slog.LevelError + 4, "ERROR"},
		{slog.LevelError, "ERROR"},
		{slog.LevelWarn + 1, "WARN"},
		{slog.LevelWarn, "WARN"},
		{slog.LevelInfo + 2, "INFO"},
		{slog.LevelInfo, "INFO"},
		{slog.LevelDebug, "DEBUG"},
		{slog.LevelDebug - 1, "TRACE"},
		{logtext.LevelTrace, "TRACE"},
		{logtext.LevelTrace - 10, "TRACE"},
	}
	for _, test := range tests {
		if got := logtext.LevelName(test.level); got != test.want {
			t.Errorf("LevelName(%v) = %q, want %q", test.level, got, test.want)
		}
	}
}

// The packages that log trace log it four below debug, as slog spaces its
// levels: a handler from LevelTrace shows those records, as TRACE, and none
// finer.
func TestAHandlerFromLevelTraceShowsTheRecordsLoggedFourBelowDebug(t *testing.T) {
	t.Parallel()
	logger, recorder := newLogger()

	logger.Log(context.Background(), slog.LevelDebug-4, "segment sent", logtext.PackageKey, "ipstack")
	logger.Log(context.Background(), slog.LevelDebug-5, "finer still", logtext.PackageKey, "ipstack")

	if got := recorder.only(t); logtext.LevelName(got.level) != "TRACE" || got.text != "segment sent" {
		t.Errorf("emitted %+v, want the trace record named TRACE", got)
	}
}
