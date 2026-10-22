package satnet

import (
	"context"
	"log"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// logRecord is what the recording handler keeps of a record.
type logRecord struct {
	level   slog.Level
	pkg     string
	message string
}

// recordingHandler keeps the records of this package that reach it. Other
// goroutines may log while a test records, so a mutex guards the records.
type recordingHandler struct {
	minimum slog.Level
	mu      *sync.Mutex
	records *[]logRecord
	// attrs are those passed to WithAttrs, where pkg may come from.
	attrs []slog.Attr
}

// recordLogs installs a recording handler for records from minimum up, as
// the process-wide logger, until the test ends, and returns what it
// records. Tests that call it must not run in parallel.
func recordLogs(t *testing.T, minimum slog.Level) func() []logRecord {
	t.Helper()
	handler := recordingHandler{minimum: minimum, mu: &sync.Mutex{}, records: &[]logRecord{}}
	previous, previousOutput, previousFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() {
		// SetDefault also points the log package at the handler; the
		// previous default does not undo that.
		slog.SetDefault(previous)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
	return func() []logRecord {
		handler.mu.Lock()
		defer handler.mu.Unlock()
		return slices.Clone(*handler.records)
	}
}

func (h recordingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minimum
}

func (h recordingHandler) Handle(_ context.Context, record slog.Record) error {
	pkg := packageOf(h.attrs)
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "pkg" {
			pkg = attr.Value.String()
		}
		return true
	})
	if pkg != logPackage {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.records = append(*h.records, logRecord{level: record.Level, pkg: pkg, message: record.Message})
	return nil
}

func (h recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(slices.Clip(h.attrs), attrs...)
	return h
}

func (h recordingHandler) WithGroup(string) slog.Handler { return h }

// packageOf returns the pkg attribute among attrs, if any.
func packageOf(attrs []slog.Attr) string {
	for _, attr := range attrs {
		if attr.Key == "pkg" {
			return attr.Value.String()
		}
	}
	return ""
}

func TestRecordingLogsLeavesTheLogPackageAsItWas(t *testing.T) {
	output, flags := log.Writer(), log.Flags()

	t.Run("recording", func(t *testing.T) { recordLogs(t, slog.LevelInfo) })

	if log.Writer() != output || log.Flags() != flags {
		t.Error("the log package still writes to the recording handler of a finished test")
	}
}

func TestLogRecordsNameThePackageAndTheirLevel(t *testing.T) {
	records := recordLogs(t, slog.LevelDebug)

	logDebug("debug")
	logInfo("info")
	logWarn("warn")

	want := []logRecord{
		{slog.LevelDebug, logPackage, "debug"},
		{slog.LevelInfo, logPackage, "info"},
		{slog.LevelWarn, logPackage, "warn"},
	}
	if got := records(); !slices.Equal(got, want) {
		t.Errorf("records %v, want %v", got, want)
	}
}

func TestDisabledLevelsAreNotLogged(t *testing.T) {
	records := recordLogs(t, slog.LevelWarn)

	logDebug("debug")
	logInfo("info")

	if got := records(); len(got) != 0 {
		t.Errorf("records %v", got)
	}
}

func TestADroppedRepeatIsLoggedAtDebugLevel(t *testing.T) {
	records := recordLogs(t, slog.LevelDebug)
	filter := newRepeatFilter(repeatWindow)
	syn := segment(1, wire.TCPControlSYN, "")
	filter.admit(syn, pacingEpoch, pacingEpoch)

	filter.admit(syn, pacingEpoch, pacingEpoch)

	if got := records(); len(got) != 1 || got[0].level != slog.LevelDebug ||
		got[0].message != "dropped an early repeat of Tcp { source_port: 49152, destination: 192.0.2.7, "+
			"destination_port: 7, sequence: 1, length: 0, syn: true, fin: false, acknowledgement: None, window: 1024 }" {
		t.Errorf("records %v", got)
	}
}
