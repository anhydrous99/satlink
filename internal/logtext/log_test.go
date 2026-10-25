package logtext_test

import (
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/logtest"
	"github.com/anhydrous99/satlink/internal/logtext"
)

// These tests set the process-wide logger, so they do not run in parallel.

func TestLogLogsWithThePackageThroughTheDefaultLoggerOfTheMoment(t *testing.T) {
	earlier := logtest.Record(t, "satnet", logtext.LevelTrace)
	logtext.Log("satnet", logtext.LevelTrace, "before")
	later := logtest.Record(t, "satnet", logtext.LevelTrace)

	logtext.Log("satnet", slog.LevelWarn, "link closed", "reason", "idle for too long", "after", time.Second)

	if got, want := earlier(), []logtest.Line{{Level: logtext.LevelTrace, Text: "before"}}; !slices.Equal(got, want) {
		t.Errorf("the logger installed first recorded %+v, want %+v", got, want)
	}
	want := []logtest.Line{{Level: slog.LevelWarn, Text: `link closed reason="idle for too long" after=1s`}}
	if got := later(); !slices.Equal(got, want) {
		t.Errorf("the logger installed last recorded %+v, want %+v", got, want)
	}
}

// The logging packages call Log in loops over octets: at a level that is
// off, it must cost nothing beyond what the caller built.
func TestLogAtALevelThatIsOffBuildsNothing(t *testing.T) {
	lines := logtest.Record(t, "ipstack", slog.LevelInfo)

	allocations := testing.AllocsPerRun(100, func() { logtext.Log("ipstack", slog.LevelDebug, "segment sent") })

	if allocations != 0 || len(lines()) != 0 {
		t.Errorf("logging below the minimum allocated %v times and recorded %+v; want nothing", allocations, lines())
	}
}
