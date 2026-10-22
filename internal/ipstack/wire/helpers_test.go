package wire

import (
	"context"
	"log"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
)

// The addresses smoltcp's TCP and UDP tests send segments between.
var (
	testSrcAddr = netip.MustParseAddr("192.168.1.1")
	testDstAddr = netip.MustParseAddr("192.168.1.2")
)

// addressesThatAreNotIPv4 are addresses the functions that write or checksum
// an address must refuse.
var addressesThatAreNotIPv4 = map[string]netip.Addr{
	"IPv6":        netip.MustParseAddr("2001:db8::1"),
	"IPv4-mapped": netip.MustParseAddr("::ffff:192.168.1.1"),
	"zero Addr":   {},
}

// field is the value an accessor returned and the value it should have.
type field struct {
	name      string
	got, want any
}

// checkFields reports every field whose value differs from the wanted one.
func checkFields(t *testing.T, fields []field) {
	t.Helper()
	for _, f := range fields {
		if f.got != f.want {
			t.Errorf("%s = %v, want %v", f.name, f.got, f.want)
		}
	}
}

// recoverPanic runs action and returns the value it panicked with, or nil.
func recoverPanic(action func()) (recovered any) {
	defer func() { recovered = recover() }()
	action()
	return nil
}

// expectPanic reports an error unless action panics.
func expectPanic(t *testing.T, description string, action func()) {
	t.Helper()
	if recoverPanic(action) == nil {
		t.Errorf("%s did not panic", description)
	}
}

// expectPanicWith reports an error unless action panics with message.
func expectPanicWith(t *testing.T, message string, action func()) {
	t.Helper()
	if recovered := recoverPanic(action); recovered != message {
		t.Errorf("panicked with %v, want %q", recovered, message)
	}
}

// filled returns a buffer of length octets, each set to octet: smoltcp's
// tests emit into buffers filled with 0xa5 to show that every octet is
// written.
func filled(length int, octet byte) []byte {
	buffer := make([]byte, length)
	for i := range buffer {
		buffer[i] = octet
	}
	return buffer
}

// recordingHandler keeps the records the default logger receives at debug
// level and above.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug
}

func (h *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordingHandler) WithGroup(string) slog.Handler { return h }

// messages returns the message and attributes of every record, as
// "message key=value…" strings.
func (h *recordingHandler) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	messages := make([]string, 0, len(h.records))
	for _, record := range h.records {
		message := record.Message
		record.Attrs(func(attr slog.Attr) bool {
			message += " " + attr.String()
			return true
		})
		messages = append(messages, message)
	}
	return messages
}

// recordLogs makes the default logger record debug messages for the rest of
// the test, which therefore must not run in parallel. slog.SetDefault also
// points the log package at the handler, and setting the previous default
// again does not undo that: the cleanup restores the log package's output
// and flags too, or every later line of the test binary would go to a
// handler nobody reads.
func recordLogs(t *testing.T) *recordingHandler {
	t.Helper()
	previous, previousOutput, previousFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
	handler := &recordingHandler{}
	slog.SetDefault(slog.New(handler))
	return handler
}

func TestRecordingLogsLeavesTheLogPackageAsItWas(t *testing.T) {
	output, flags := log.Writer(), log.Flags()

	t.Run("recording", func(t *testing.T) { recordLogs(t) })

	if log.Writer() != output || log.Flags() != flags {
		t.Error("the log package still writes to the recording handler of a finished test")
	}
}
