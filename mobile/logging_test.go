package mobile

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/logtext"
	"github.com/anhydrous99/satlink/internal/simtest"
)

// The bridge's tests build one bridge each, through newLogBridge, and log
// through a logger of their own: slog.Default stays untouched, which
// SetLogSink would change for the rest of the process.

// deliveryPatience is how long a test waits for records to reach the sink
// from the bridge's goroutine.
const deliveryPatience = 5 * time.Second

// recordingSink is a LogSink that keeps every record it receives. The
// bridge's goroutine writes while the test reads, so a mutex guards it.
type recordingSink struct {
	mu      sync.Mutex
	records []logEntry
}

func (s *recordingSink) Log(level int, target, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, logEntry{level: level, target: target, text: message})
	return nil
}

// received returns a copy of the records received so far.
func (s *recordingSink) received() []logEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records)
}

// awaitRecords waits until sink has received count records and returns
// them.
func awaitRecords(t *testing.T, sink *recordingSink, count int) []logEntry {
	t.Helper()
	simtest.Eventually(t, deliveryPatience, fmt.Sprintf("the sink has %d records", count), func() bool {
		return len(sink.received()) >= count
	})
	return sink.received()
}

// assertRecords fails the test unless got are want.
func assertRecords(t *testing.T, got, want []logEntry) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("the sink received %+v, want %+v", got, want)
	}
}

func TestRecordsAtOrAboveTheLevelReachTheSink(t *testing.T) {
	sink := &recordingSink{}
	logger := slog.New(newLogBridge(sink, slog.LevelInfo))

	logger.Debug("too detailed", "pkg", "satlink_test")
	logger.Info("dialling", "pkg", "satlink_test")

	assertRecords(t, awaitRecords(t, sink, 1), []logEntry{
		{level: LogLevelInfo, target: "satlink_test", text: "dialling"},
	})
}

func TestTheTargetIsTheRecordsPackageAndTheTextHasItsAttributes(t *testing.T) {
	sink := &recordingSink{}
	logger := slog.New(newLogBridge(sink, slog.LevelInfo))

	logger.With("pkg", "satnet").Info("IPv4 up", "address", "10.64.0.2")
	logger.Warn("simulated gateway: no route", "pkg", "sim", "to", "192.0.2.99")

	assertRecords(t, awaitRecords(t, sink, 2), []logEntry{
		{level: LogLevelInfo, target: "satnet", text: "IPv4 up address=10.64.0.2"},
		{level: LogLevelWarn, target: "sim", text: "simulated gateway: no route to=192.0.2.99"},
	})
}

func TestARecordWithoutAPackageHasNoTarget(t *testing.T) {
	sink := &recordingSink{}
	logger := slog.New(newLogBridge(sink, slog.LevelInfo))

	logger.Error("a log line from elsewhere")

	assertRecords(t, awaitRecords(t, sink, 1), []logEntry{
		{level: LogLevelError, target: "", text: "a log line from elsewhere"},
	})
}

func TestATraceSinkHearsTheFinestDetails(t *testing.T) {
	sink := &recordingSink{}
	minimum, err := slogLevelOf(LogLevelTrace)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(newLogBridge(sink, minimum))

	logger.Log(t.Context(), logtext.LevelTrace, "a byte arrived", "pkg", "ipstack")

	assertRecords(t, awaitRecords(t, sink, 1), []logEntry{
		{level: LogLevelTrace, target: "ipstack", text: "a byte arrived"},
	})
}

func TestRecordLevelsMapToTheLogLevels(t *testing.T) {
	cases := []struct {
		level slog.Level
		want  int
	}{
		{level: slog.LevelError + 4, want: LogLevelError},
		{level: slog.LevelError, want: LogLevelError},
		{level: slog.LevelWarn, want: LogLevelWarn},
		{level: slog.LevelInfo + 1, want: LogLevelInfo},
		{level: slog.LevelInfo, want: LogLevelInfo},
		{level: slog.LevelDebug, want: LogLevelDebug},
		{level: slog.LevelDebug - 1, want: LogLevelTrace},
		{level: logtext.LevelTrace, want: LogLevelTrace},
	}
	for _, c := range cases {
		if got := logLevelOf(c.level); got != c.want {
			t.Errorf("logLevelOf(%v) = %d, want %d", c.level, got, c.want)
		}
	}
}

func TestLogLevelsAreSlogThresholds(t *testing.T) {
	want := map[int]slog.Level{
		LogLevelError: slog.LevelError,
		LogLevelWarn:  slog.LevelWarn,
		LogLevelInfo:  slog.LevelInfo,
		LogLevelDebug: slog.LevelDebug,
		LogLevelTrace: logtext.LevelTrace,
	}
	for level, threshold := range want {
		if got, err := slogLevelOf(level); err != nil || got != threshold {
			t.Errorf("slogLevelOf(%d) = %v, %v, want %v", level, got, err, threshold)
		}
	}
}

func TestUnknownLogLevelsAreRefused(t *testing.T) {
	for _, level := range []int{-1, LogLevelTrace + 1} {
		_, err := slogLevelOf(level)

		assertSatlinkError(t, err,
			&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: fmt.Sprintf("%d is not a log level", level)},
			fmt.Sprintf("invalid argument: %d is not a log level", level))
	}
}

// blockingSink is a recordingSink whose first Log waits until release is
// closed, after closing started: a sink that has fallen behind.
type blockingSink struct {
	recordingSink
	started chan struct{}
	release chan struct{}
	first   sync.Once
}

func newBlockingSink() *blockingSink {
	return &blockingSink{started: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingSink) Log(level int, target, message string) error {
	s.first.Do(func() {
		close(s.started)
		<-s.release
	})
	return s.recordingSink.Log(level, target, message)
}

func TestASinkThatFallsBehindLosesRecordsRatherThanHoldingUpTheLogger(t *testing.T) {
	sink := newBlockingSink()
	logger := slog.New(newLogBridge(sink, slog.LevelInfo))
	logger.Info("first")
	select {
	case <-sink.started:
	case <-time.After(deliveryPatience):
		t.Fatal("the first record never reached the sink")
	}

	// SetLogSink's documented queue, the Rust bindings' QUEUE_LIMIT: written
	// out, so that the test checks logQueueLimit instead of trusting it.
	const documentedQueueLimit = 256
	const overflow = 10
	for i := range documentedQueueLimit + overflow {
		logger.Info(fmt.Sprintf("queued %d", i))
	}
	close(sink.release)
	awaitRecords(t, &sink.recordingSink, 1+documentedQueueLimit)
	logger.Info("after")
	got := awaitRecords(t, &sink.recordingSink, 2+documentedQueueLimit)

	want := []logEntry{{level: LogLevelInfo, text: "first"}}
	for i := range documentedQueueLimit {
		want = append(want, logEntry{level: LogLevelInfo, text: fmt.Sprintf("queued %d", i)})
	}
	want = append(want, logEntry{level: LogLevelInfo, text: "after"})
	assertRecords(t, got, want)
}

// failingSink fails its first record with an error and its second with a
// panic, and keeps the others.
type failingSink struct {
	recordingSink
	calls int
}

func (s *failingSink) Log(level int, target, message string) error {
	s.calls++
	switch s.calls {
	case 1:
		return errors.New("the app's log is full")
	case 2:
		panic("a sink bug")
	default:
		return s.recordingSink.Log(level, target, message)
	}
}

func TestAFailingSinkStillReceivesTheNextRecords(t *testing.T) {
	sink := &failingSink{}
	logger := slog.New(newLogBridge(sink, slog.LevelInfo))

	logger.Info("refused")
	logger.Info("panicked over")
	logger.Info("kept")

	assertRecords(t, awaitRecords(t, &sink.recordingSink, 1), []logEntry{
		{level: LogLevelInfo, text: "kept"},
	})
}

func TestSetLogSinkRefusesANilSinkAndAnUnknownLevelBeforeInstallingAnything(t *testing.T) {
	cases := []struct {
		name   string
		sink   LogSink
		level  int
		reason string
	}{
		{name: "nil sink", sink: nil, level: LogLevelInfo, reason: "sink must not be nil"},
		{name: "unknown level", sink: &recordingSink{}, level: 7, reason: "7 is not a log level"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := SetLogSink(c.sink, c.level)

			assertSatlinkError(t, err,
				&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: c.reason},
				"invalid argument: "+c.reason)
			if logSinkSet.Load() {
				t.Error("a refused SetLogSink installed its sink")
			}
		})
	}
}
