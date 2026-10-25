// Package logtest is test support for the tests that look at what the
// module logs:
//   - [Install] makes a handler the process-wide logger until the test ends;
//   - [Record] keeps the lines one package logs, as [Line] values, rendered
//     as the command-line tool renders them.
//
// Only tests import it. It imports nothing of the module but
// internal/logtext, so that the internal tests of every package, ipstack's
// included, can use it. A test that uses it changes the logger of the whole
// test binary, so it must not run in parallel.
package logtest

import (
	"log"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// Install makes handler the process-wide logger's until the test ends.
// slog.SetDefault also points the log package at handler, and setting the
// previous default again does not undo that: the cleanup restores the log
// package's output and flags too, or every later line of the test binary
// would go to a handler nobody reads.
func Install(t testing.TB, handler slog.Handler) {
	t.Helper()
	previous, previousOutput, previousFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
	slog.SetDefault(slog.New(handler))
}

// Line is what a package logged: the record's level, and its text as
// logtext renders it, the message followed by " key=value" for every
// attribute but the package.
type Line struct {
	Level slog.Level
	Text  string
}

// Record installs, until the test ends, a process-wide logger that enables
// minimum and above and keeps the lines that pkg logs, and returns a
// function that reports the lines kept so far, oldest first. Background
// goroutines may log while the test reads, so a mutex guards the lines.
func Record(t testing.TB, pkg string, minimum slog.Level) func() []Line {
	t.Helper()
	var mu sync.Mutex
	var lines []Line
	Install(t, logtext.NewHandler(minimum, func(level slog.Level, from, text string) {
		if from != pkg {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, Line{Level: level, Text: text})
	}))
	return func() []Line {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(lines)
	}
}
