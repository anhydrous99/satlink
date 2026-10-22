package main

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// A minimal logger that prints to standard error, from a goroutine of its
// own.

// logLineFormat is how a log record is printed, "[LEVEL pkg] text", the
// level padded to five characters as the Rust version's "[{:<5} {}] {}".
const logLineFormat = "[%-5s %s] %s\n"

// droppedLinesFormat says how many lines standard error could not take, in
// front of the next line it takes.
const droppedLinesFormat = "[%d log lines dropped while standard error was blocked]\n"

// logBacklog is how many lines may wait for standard error; any more are
// dropped, and counted.
const logBacklog = 1000

// flushLimit is the longest flush waits for the lines still waiting to be
// written, and flushCheck how often a wait for written lines looks.
const (
	flushLimit = time.Second
	flushCheck = 10 * time.Millisecond
)

// stderrLog hands log lines to a goroutine of their own, which writes them
// to standard error. Standard error can block — a pipe nobody reads, such
// as one to a pager — and the link's driver logs too: it must not stop
// hanging up and enforcing the limits meanwhile. The goroutine starts with
// the first line and ends with close. The zero stderrLog is not usable;
// newStderrLog makes one.
type stderrLog struct {
	stderr  io.Writer
	backlog chan string
	// handed and written count the lines handed over and those written;
	// dropped counts the lines dropped since the last one written.
	handed, written, dropped atomic.Int64
	// startWriting starts the goroutine that writes, and closed ends it.
	startWriting sync.Once
	closeOnce    sync.Once
	closed       chan struct{}
}

// newStderrLog returns a log that writes to stderr, which must be safe for
// concurrent use.
func newStderrLog(stderr io.Writer) *stderrLog {
	return &stderrLog{stderr: stderr, backlog: make(chan string, logBacklog), closed: make(chan struct{})}
}

// handler returns the handler that hands log records to the log, from the
// least severe level that verbosity shows.
func (l *stderrLog) handler(verbosity int) slog.Handler {
	return logtext.NewHandler(logLevelFor(verbosity), func(level slog.Level, pkg, text string) {
		l.hand(fmt.Sprintf(logLineFormat, logtext.LevelName(level), pkg, text))
	})
}

// logLevelFor shows warnings, plus more with each -v: info, debug, and from
// -vvv on, trace.
func logLevelFor(verbosity int) slog.Level {
	switch {
	case verbosity <= 0:
		return slog.LevelWarn
	case verbosity == 1:
		return slog.LevelInfo
	case verbosity == 2:
		return slog.LevelDebug
	default:
		return logtext.LevelTrace
	}
}

// hand passes line on to be written, without waiting: once the backlog is
// full, the line is dropped, and counted.
func (l *stderrLog) hand(line string) {
	l.startWriting.Do(func() { go l.writeLines() })
	select {
	case l.backlog <- line:
		l.handed.Add(1)
	default:
		l.dropped.Add(1)
	}
}

// writeLines writes the lines handed over, in order, until the log is
// closed.
func (l *stderrLog) writeLines() {
	for {
		select {
		case line := <-l.backlog:
			l.write(line)
		case <-l.closed:
			return
		}
	}
}

// write writes line to standard error, after saying how many lines were
// dropped since the last one, if any.
func (l *stderrLog) write(line string) {
	if dropped := l.dropped.Swap(0); dropped > 0 {
		line = fmt.Sprintf(droppedLinesFormat, dropped) + line
	}
	// Best effort: standard error may be a closed pipe or a dead terminal.
	_, _ = io.WriteString(l.stderr, line)
	l.written.Add(1)
}

// flush gives the lines handed over so far a moment to reach standard
// error before the CLI ends: it waits until they are written, but not for
// longer than flushLimit.
func (l *stderrLog) flush() {
	l.awaitWritten(time.Now().Add(flushLimit))
}

// awaitWritten waits until the lines handed over so far are written — not
// those handed over meanwhile — or the log is closed, or deadline passes,
// unless it is the zero time.
func (l *stderrLog) awaitWritten(deadline time.Time) {
	handed := l.handed.Load()
	for l.written.Load() < handed && !l.isClosed() && (deadline.IsZero() || time.Now().Before(deadline)) {
		time.Sleep(flushCheck)
	}
}

// isClosed reports whether the log is closed, so that its lines are no
// longer written.
func (l *stderrLog) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

// close ends the writing goroutine: lines handed over from now on wait for
// nobody, and those that do not fit are dropped.
func (l *stderrLog) close() {
	l.closeOnce.Do(func() { close(l.closed) })
}

// writerBehind returns the standard error that the CLI writes its own lines
// to. Each write first waits for the log lines handed over before it to be
// written, however long standard error blocks them, as it would block the
// write itself; so the lines come in the order they were made. The Rust
// version's logger thread wrote a line before the command's next one, where
// Go's scheduler would let the command's goroutine write first.
func (l *stderrLog) writerBehind() io.Writer {
	return writerBehindLog{log: l}
}

// writerBehindLog writes to standard error behind the log's lines.
type writerBehindLog struct {
	log *stderrLog
}

// Write writes p to standard error once the log lines handed over before it
// are written, or the log is closed.
func (w writerBehindLog) Write(p []byte) (int, error) {
	w.log.awaitWritten(time.Time{})
	return w.log.stderr.Write(p)
}
