package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// mostVerbose is a verbosity that shows every record.
const mostVerbose = 3

// newTestLog returns a log that writes to stderr, closed when the test ends.
func newTestLog(t *testing.T, stderr io.Writer) *stderrLog {
	t.Helper()
	log := newStderrLog(stderr)
	t.Cleanup(log.close)
	return log
}

func TestLogLinesNameTheirLevelAndPackage(t *testing.T) {
	var stderr lockedBuffer
	log := newTestLog(t, &stderr)
	logger := slog.New(log.handler(mostVerbose))

	logger.Log(context.Background(), logtext.LevelTrace, "segment sent", "pkg", "ipstack")
	logger.Debug("LCP up", "pkg", "satnet")
	logger.Info("link closed: closed by the app", "pkg", "satnet")
	logger.Warn("simulated gateway: no route", "pkg", "sim", "to", "192.0.2.99")
	logger.Error("no package")
	log.flush()

	want := "[TRACE ipstack] segment sent\n" +
		"[DEBUG satnet] LCP up\n" +
		"[INFO  satnet] link closed: closed by the app\n" +
		"[WARN  sim] simulated gateway: no route to=192.0.2.99\n" +
		"[ERROR ] no package\n"
	if got := stderr.String(); got != want {
		t.Errorf("log lines =\n%s\nwant\n%s", got, want)
	}
}

func TestEachVerboseFlagShowsMore(t *testing.T) {
	tests := []struct {
		verbosity int
		least     slog.Level
	}{
		{0, slog.LevelWarn},
		{1, slog.LevelInfo},
		{2, slog.LevelDebug},
		{3, logtext.LevelTrace},
		{7, logtext.LevelTrace},
	}
	for _, test := range tests {
		handler := newTestLog(t, io.Discard).handler(test.verbosity)
		shown := handler.Enabled(context.Background(), test.least)
		belowShown := handler.Enabled(context.Background(), test.least-1)
		if !shown || belowShown {
			t.Errorf("verbosity %d shows %v: %t, and below it: %t; want only from %v up",
				test.verbosity, test.least, shown, belowShown, test.least)
		}
	}
}

func TestALogLineThatCannotBeWrittenStopsNothing(t *testing.T) {
	log := newTestLog(t, brokenWriter{})
	record := slog.NewRecord(time.Now(), slog.LevelWarn, "link closed", 0)

	if err := log.handler(0).Handle(context.Background(), record); err != nil {
		t.Errorf("Handle = %v, want nil", err)
	}
	log.flush()
}

func TestLogLinesWaitForABlockedStandardErrorWithoutHoldingUpTheLogger(t *testing.T) {
	stderr := newBlockedWriter()
	log := newTestLog(t, stderr)

	finishesWithin(t, time.Second, "logging behind a blocked standard error", func() {
		for i := range 3 {
			log.hand(fmt.Sprintf("line %d\n", i))
		}
	})
	stderr.unblock()
	log.flush()

	if got, want := stderr.String(), "line 0\nline 1\nline 2\n"; got != want {
		t.Errorf("standard error got %q, want %q", got, want)
	}
}

func TestLinesBeyondTheBacklogAreDroppedAndCounted(t *testing.T) {
	stderr := newBlockedWriter()
	log := newTestLog(t, stderr)
	const dropped = 5
	log.hand("line 0\n")
	// The first line is being written: the backlog holds the next ones.
	stderr.awaitWrite(t)

	for i := 1; i <= logBacklog+dropped; i++ {
		log.hand(fmt.Sprintf("line %d\n", i))
	}
	stderr.unblock()
	log.flush()

	written := strings.SplitAfter(stderr.String(), "\n")
	want := []string{"line 0\n", "[5 log lines dropped while standard error was blocked]\n", "line 1\n"}
	if len(written) < len(want) || strings.Join(written[:3], "") != strings.Join(want, "") {
		t.Fatalf("standard error starts %q, want %q", written[:min(3, len(written))], want)
	}
	if last := written[len(written)-2]; last != fmt.Sprintf("line %d\n", logBacklog) {
		t.Errorf("the last line written is %q, want line %d", last, logBacklog)
	}
}

func TestFlushGivesUpOnAStandardErrorThatStaysBlocked(t *testing.T) {
	stderr := newBlockedWriter()
	log := newTestLog(t, stderr)
	t.Cleanup(stderr.unblock)
	log.hand("never written\n")

	started := time.Now()
	log.flush()

	if waited := time.Since(started); waited < flushLimit {
		t.Errorf("flush returned after %v, want it to wait %v", waited, flushLimit)
	}
}

// heldLineWait is how long a test gives the command's line to be written
// while the log line before it is held, to show it waits: a wait for
// something not to happen, which nothing could show sooner.
const heldLineWait = 100 * time.Millisecond

// firstWriteHolder is a standard error that holds its first write until
// released, and lets every later one through at once, as a terminal that
// is slow only now and then would.
type firstWriteHolder struct {
	lockedBuffer
	once sync.Once
	// holding is closed once the first write is held, and released lets it
	// through.
	holding, released chan struct{}
}

func newFirstWriteHolder() *firstWriteHolder {
	return &firstWriteHolder{holding: make(chan struct{}), released: make(chan struct{})}
}

func (w *firstWriteHolder) Write(p []byte) (int, error) {
	first := false
	w.once.Do(func() {
		first = true
		close(w.holding)
	})
	if first {
		<-w.released
	}
	return w.lockedBuffer.Write(p)
}

func TestTheCommandsOwnLinesFollowTheLogLinesMadeBeforeThem(t *testing.T) {
	stderr := newFirstWriteHolder()
	c := newCLI(environment{}, stderr)
	t.Cleanup(c.log.close)
	c.log.hand("log line\n")
	<-stderr.holding

	wrote := make(chan error, 1)
	go func() {
		_, err := io.WriteString(c.streams.stderr, "command's line\n")
		wrote <- err
	}()
	select {
	case err := <-wrote:
		t.Fatalf("the command's line was written (%v) while the log line before it was held", err)
	case <-time.After(heldLineWait):
	}
	close(stderr.released)

	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	if got, want := stderr.String(), "log line\ncommand's line\n"; got != want {
		t.Errorf("standard error got %q, want %q", got, want)
	}
}

func TestTheCommandsOwnLinesDoNotWaitForLinesAClosedLogLeftBehind(t *testing.T) {
	stderr := newBlockedWriter()
	t.Cleanup(stderr.unblock)
	log := newTestLog(t, stderr)
	log.hand("never written\n")
	stderr.awaitWrite(t)

	log.close()

	finishesWithin(t, time.Second, "waiting behind a closed log", func() { log.awaitWritten(time.Time{}) })
}

func TestHandingLinesToAClosedLogNeitherWaitsNorPanics(t *testing.T) {
	stderr := newBlockedWriter()
	log := newStderrLog(stderr)
	t.Cleanup(stderr.unblock)
	log.close()

	finishesWithin(t, time.Second, "logging to a closed log", func() {
		for range 2 * logBacklog {
			log.hand("late\n")
		}
	})
	log.close()
}

// finishesWithin runs do, and fails the test unless it finishes within
// limit; what names it.
func finishesWithin(t *testing.T, limit time.Duration, what string, do func()) {
	t.Helper()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		do()
	}()
	select {
	case <-finished:
	case <-time.After(limit):
		t.Fatalf("%s did not finish within %v", what, limit)
	}
}
