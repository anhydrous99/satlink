package netlog

import (
	"context"
	"log"
	"log/slog"
	"testing"
)

// recordingHandler keeps every record at or above its level.
type recordingHandler struct {
	level   slog.Level
	records []slog.Record
}

func (h *recordingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	h.records = append(h.records, record)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordingHandler) WithGroup(string) slog.Handler { return h }

// recordLogs makes the default logger record messages at level and above
// for the rest of the test. slog.SetDefault also points the log package at
// the handler, and setting the previous default again does not undo that:
// the cleanup restores the log package's output and flags too, or every
// later line of the test binary would go to a handler nobody reads.
func recordLogs(t *testing.T, level slog.Level) *recordingHandler {
	t.Helper()
	previous, previousOutput, previousFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
	handler := &recordingHandler{level: level}
	slog.SetDefault(slog.New(handler))
	return handler
}

func TestRecordingLogsLeavesTheLogPackageAsItWas(t *testing.T) {
	output, flags := log.Writer(), log.Flags()

	t.Run("recording", func(t *testing.T) { recordLogs(t, slog.LevelInfo) })

	if log.Writer() != output || log.Flags() != flags {
		t.Error("the log package still writes to the recording handler of a finished test")
	}
}

func attributes(record slog.Record) map[string]any {
	found := map[string]any{}
	record.Attrs(func(attr slog.Attr) bool {
		found[attr.Key] = attr.Value.Any()
		return true
	})
	return found
}

func TestTraceLogsBelowDebugWithThePackageAttribute(t *testing.T) {
	handler := recordLogs(t, LevelTrace)

	Trace("tcp: state changed", "to", "ESTABLISHED")

	if len(handler.records) != 1 {
		t.Fatalf("got %d records, want 1", len(handler.records))
	}
	record := handler.records[0]
	if record.Level != slog.LevelDebug-4 || record.Message != "tcp: state changed" {
		t.Errorf("got %v %q, want level %v", record.Level, record.Message, slog.LevelDebug-4)
	}
	want := map[string]any{"pkg": "ipstack", "to": "ESTABLISHED"}
	got := attributes(record)
	for key, value := range want {
		if got[key] != value {
			t.Errorf("attribute %q = %v, want %v", key, got[key], value)
		}
	}
}

func TestDebugLogsAtDebugLevel(t *testing.T) {
	handler := recordLogs(t, slog.LevelDebug)

	Debug("iface: malformed packet")

	if len(handler.records) != 1 || handler.records[0].Level != slog.LevelDebug {
		t.Fatalf("got %v, want one debug record", handler.records)
	}
}

func TestDisabledLevelsAreNotHandled(t *testing.T) {
	handler := recordLogs(t, slog.LevelInfo)

	Trace("dropped")
	Debug("dropped too")

	if len(handler.records) != 0 {
		t.Errorf("got %d records, want none below info", len(handler.records))
	}
}

func TestLoggerIsLookedUpAtLogTime(t *testing.T) {
	recordLogs(t, slog.LevelInfo)
	later := recordLogs(t, LevelTrace)

	Trace("after the handler changed")

	if len(later.records) != 1 {
		t.Errorf("the handler installed last got %d records, want 1", len(later.records))
	}
}
