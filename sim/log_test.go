package sim

import (
	"context"
	"log"
	"log/slog"
	"slices"
	"sync"
	"testing"
)

// logRecord is what a recorded log line said.
type logRecord struct {
	level   slog.Level
	message string
}

// logRecorder keeps the records this package logs. Background goroutines
// log into it too, so a mutex guards it; it keeps the attributes given to
// WithAttrs, where "pkg" may come from.
type logRecorder struct {
	mu      *sync.Mutex
	records *[]logRecord
	attrs   []slog.Attr
}

func (r logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r logRecorder) Handle(_ context.Context, record slog.Record) error {
	if !r.isFromThisPackage(record) {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.records = append(*r.records, logRecord{level: record.Level, message: record.Message})
	return nil
}

func (r logRecorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	r.attrs = append(slices.Clone(r.attrs), attrs...)
	return r
}

func (r logRecorder) WithGroup(string) slog.Handler { return r }

// isFromThisPackage reports whether record carries pkg=sim, as an attribute
// of its own or through WithAttrs.
func (r logRecorder) isFromThisPackage(record slog.Record) bool {
	fromHere := slices.ContainsFunc(r.attrs, isThisPackage)
	record.Attrs(func(attr slog.Attr) bool {
		fromHere = fromHere || isThisPackage(attr)
		return !fromHere
	})
	return fromHere
}

func isThisPackage(attr slog.Attr) bool {
	return attr.Key == "pkg" && attr.Value.String() == logPackage
}

// recorded returns a copy of the records so far.
func (r logRecorder) recorded() []logRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(*r.records)
}

// recordLogs makes the default logger record this package's lines for the
// rest of the test, which therefore must not run in parallel.
func recordLogs(t *testing.T) logRecorder {
	t.Helper()
	recorder := newLogRecorder()
	installDefaultLogger(t, recorder)
	return recorder
}

// newLogRecorder returns a recorder with no records yet.
func newLogRecorder() logRecorder {
	return logRecorder{mu: &sync.Mutex{}, records: &[]logRecord{}}
}

// installDefaultLogger makes handler the default logger's for the rest of
// the test. slog.SetDefault also points the log package at handler, and
// setting the previous default again does not undo that: the cleanup
// restores the log package's output and flags too, or every later line of
// the test binary would go to a handler nobody reads.
func installDefaultLogger(t *testing.T, handler slog.Handler) {
	t.Helper()
	previous, previousOutput, previousFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
	slog.SetDefault(slog.New(handler))
}

func TestRecordingLogsLeavesTheLogPackageAsItWas(t *testing.T) {
	output, flags := log.Writer(), log.Flags()

	t.Run("recording", func(t *testing.T) { recordLogs(t) })

	if log.Writer() != output || log.Flags() != flags {
		t.Error("the log package still writes to the recording handler of a finished test")
	}
}

func TestTheLogHelpersLogAtTheirLevelWithThePackageAttribute(t *testing.T) {
	recorder := recordLogs(t)

	logDebug("simulated gateway: debug")
	logInfo("simulated gateway: info")
	logWarn("simulated gateway: warning")

	want := []logRecord{
		{level: slog.LevelDebug, message: "simulated gateway: debug"},
		{level: slog.LevelInfo, message: "simulated gateway: info"},
		{level: slog.LevelWarn, message: "simulated gateway: warning"},
	}
	if got := recorder.recorded(); !slices.Equal(got, want) {
		t.Errorf("recorded %+v; want %+v", got, want)
	}
}

func TestDisabledLevelsAreNotLogged(t *testing.T) {
	recorder := newLogRecorder()
	installDefaultLogger(t, levelFilter{minimum: slog.LevelInfo, Handler: recorder})

	logDebug("simulated gateway: hidden")

	if got := recorder.recorded(); len(got) != 0 {
		t.Errorf("recorded %+v below the minimum level", got)
	}
}

// levelFilter passes on only the records at or above minimum.
type levelFilter struct {
	slog.Handler
	minimum slog.Level
}

func (f levelFilter) Enabled(_ context.Context, level slog.Level) bool {
	return level >= f.minimum
}
