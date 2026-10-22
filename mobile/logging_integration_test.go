package mobile_test

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/mobile"
)

// The log bridge as an app meets it: records reach the app's sink on their
// own thread. SetLogSink can set a sink once per process and changes the
// process's default logger, so the test runs in a process of its own, as
// the Rust test binary did: other tests, and -count, cannot interfere.

// logSinkChild is the environment variable that tells TestLogSinkChild it
// runs in the child process that
// TestRecordsAtOrAboveTheLevelReachTheSinkOnceSet starts.
const logSinkChild = "SATLINK_MOBILE_LOG_SINK_CHILD"

// logPatience is how long the child waits for its record to reach the sink.
const logPatience = 5 * time.Second

// sinkRecord is one record a sink received.
type sinkRecord struct {
	level   int
	target  string
	message string
}

// recorder is a LogSink that keeps what it receives; the bridge's thread
// writes while the test reads.
type recorder struct {
	mu      sync.Mutex
	records []sinkRecord
}

func (r *recorder) Log(level int, target, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, sinkRecord{level: level, target: target, message: message})
	return nil
}

func (r *recorder) received() []sinkRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.records)
}

func TestRecordsAtOrAboveTheLevelReachTheSinkOnceSet(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestLogSinkChild$", "-test.v")
	child.Env = append(os.Environ(), logSinkChild+"=1")

	output, err := child.CombinedOutput()

	if err != nil {
		t.Fatalf("the child process failed: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte("--- PASS: TestLogSinkChild")) {
		t.Fatalf("the child process did not pass TestLogSinkChild:\n%s", output)
	}
}

func TestLogSinkChild(t *testing.T) {
	if os.Getenv(logSinkChild) == "" {
		t.Skip("runs in the child process that TestRecordsAtOrAboveTheLevelReachTheSinkOnceSet starts")
	}
	sink := &recorder{}
	if err := mobile.SetLogSink(sink, mobile.LogLevelInfo); err != nil {
		t.Fatalf("SetLogSink: %v", err)
	}

	slog.Debug("too detailed", "pkg", "satlink_test")
	slog.Info("dialling", "pkg", "satlink_test")

	simtest.Eventually(t, logPatience, "the sink has a record", func() bool {
		return len(sink.received()) > 0
	})
	want := []sinkRecord{{level: mobile.LogLevelInfo, target: "satlink_test", message: "dialling"}}
	if got := sink.received(); !slices.Equal(got, want) {
		t.Errorf("the sink received %+v, want %+v", got, want)
	}
	again := mobile.AsSatlinkError(mobile.SetLogSink(&recorder{}, mobile.LogLevelTrace))
	if again == nil || again.Kind != mobile.SatlinkErrorInvalidArgument || again.Reason != "a log sink is already set" {
		t.Errorf("a second SetLogSink gave %v, want SatlinkErrorInvalidArgument, \"a log sink is already set\"", again)
	}
}
