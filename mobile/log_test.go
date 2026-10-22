package mobile

import (
	"log"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// This package logs only the panics it recovers where no caller can be
// told. The tests that check those records replace the process-wide logger
// while they run, so they never run in parallel; the other tests leave it
// alone.

// recordErrors installs, until the test ends, a process-wide logger that
// keeps the text of this package's records at error level, and returns a
// function that reports what it has kept so far. Background goroutines may
// log meanwhile, so a mutex guards the records. Tests that call it must
// not run in parallel.
func recordErrors(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var texts []string
	handler := logtext.NewHandler(slog.LevelError, func(_ slog.Level, pkg, text string) {
		if pkg != logPackage {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		texts = append(texts, text)
	})
	previous, previousOutput, previousFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() {
		// SetDefault also points the log package at the handler; setting
		// the previous default again does not undo that.
		slog.SetDefault(previous)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(texts)
	}
}

func TestRecordingErrorsLeavesTheLogPackageAsItWas(t *testing.T) {
	output, flags := log.Writer(), log.Flags()

	t.Run("recording", func(t *testing.T) { recordErrors(t) })

	if log.Writer() != output || log.Flags() != flags {
		t.Error("the log package still writes to the recording handler of a finished test")
	}
}
