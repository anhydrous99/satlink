package mobile

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// Forwards the library's log records — dialling, PPP negotiation, why a
// link closed — to the app, for diagnosing links on real hardware.

// The levels of [LogSink.Log] and [SetLogSink], most severe first.
const (
	// LogLevelError is for failures.
	LogLevelError = iota
	// LogLevelWarn is for problems the library works around.
	LogLevelWarn
	// LogLevelInfo is for what the link is doing.
	LogLevelInfo
	// LogLevelDebug is for details.
	LogLevelDebug
	// LogLevelTrace is for the finest details.
	LogLevelTrace
)

// LogSink receives log records, on a thread of its own.
//
// In Objective-C and Swift, adopt the protocol SatlinkMobileLogSink (Swift:
// SatlinkMobileLogSinkProtocol) in a class of your own, as for
// ByteTransport: the class of that name is not for apps to make.
type LogSink interface {
	// Log receives one record: its level (a LogLevel constant), the package
	// that logged it, such as "satnet", and its text — the message,
	// followed by any details as " key=value", as the command-line tool
	// prints them. An error it returns is ignored: there is nowhere better
	// to report it.
	Log(level int, target string, message string) error
}

// logQueueLimit is how many records may wait for the sink; beyond it they
// are dropped rather than letting a slow sink hold up the link.
const logQueueLimit = 256

// logGoroutine names the goroutine that delivers records to the sink, as
// the Rust bindings named its thread.
const logGoroutine = "satlink-log"

// logSinkSet records that SetLogSink has installed its bridge: the process
// has one default logger, as Rust's log crate has one logger.
var logSinkSet atomic.Bool

// SetLogSink sends the records at level and above, one of the LogLevel
// constants, to sink, from a thread of their own so that a slow sink never
// stalls the link: at most 256 records wait, and while the sink is behind
// further records are dropped rather than holding up the link. It installs
// the process's default slog handler.
//
// Only one sink can be set per process: another call fails with
// SatlinkErrorInvalidArgument, "a log sink is already set". An unknown
// level, or a nil sink, fails with SatlinkErrorInvalidArgument too.
func SetLogSink(sink LogSink, level int) (err error) {
	defer recoverPanic(&err)
	if sink == nil {
		return nilArgument("sink")
	}
	minimum, err := slogLevelOf(level)
	if err != nil {
		return err
	}
	if !logSinkSet.CompareAndSwap(false, true) {
		return invalidArgument("a log sink is already set")
	}
	slog.SetDefault(slog.New(newLogBridge(sink, minimum)))
	return nil
}

// logEntry is a record on its way to the sink.
type logEntry struct {
	// level is the record's LogLevel constant.
	level int
	// target is the package that logged the record.
	target string
	// text is the record's message and attributes.
	text string
}

// newLogBridge returns a handler that renders the records at minimum and
// above as the command-line tool does and queues them for sink, without
// ever waiting, and starts the goroutine that hands them to sink.
func newLogBridge(sink LogSink, minimum slog.Level) slog.Handler {
	queue := make(chan logEntry, logQueueLimit)
	go labelled(logGoroutine, func() {
		deliverLogs(queue, sink)
	})
	return logtext.NewHandler(minimum, func(level slog.Level, pkg, text string) {
		select {
		case queue <- logEntry{level: logLevelOf(level), target: pkg, text: text}:
		default:
			// A full queue means the sink is behind: drop the record, not
			// the link's pace.
		}
	})
}

// deliverLogs hands every record queued to sink, oldest first.
func deliverLogs(queue <-chan logEntry, sink LogSink) {
	for entry := range queue {
		deliverLog(sink, entry)
	}
}

// deliverLog hands one record to sink. A sink that fails, by an error or a
// panic, has nowhere better to report to: the record is dropped, and the
// next one is tried.
func deliverLog(sink LogSink, entry logEntry) {
	defer func() {
		// A panicking sink is a failing sink; logging its panic would only
		// hand it back to the same sink.
		_ = recover()
	}()
	// Nowhere better to report a failing sink.
	_ = sink.Log(entry.level, entry.target, entry.text)
}

// slogLevels are the slog levels of the LogLevel constants.
var slogLevels = [...]slog.Level{
	LogLevelError: slog.LevelError,
	LogLevelWarn:  slog.LevelWarn,
	LogLevelInfo:  slog.LevelInfo,
	LogLevelDebug: slog.LevelDebug,
	LogLevelTrace: logtext.LevelTrace,
}

// slogLevelOf returns the slog level of level, a LogLevel constant.
func slogLevelOf(level int) (slog.Level, error) {
	if level < 0 || level >= len(slogLevels) {
		return 0, invalidArgument(fmt.Sprintf("%d is not a log level", level))
	}
	return slogLevels[level], nil
}

// logLevelOf returns the LogLevel constant of a record's level, as
// logtext.LevelName names levels: error and above, warn, info and debug
// from those levels up, and trace below debug.
func logLevelOf(level slog.Level) int {
	switch {
	case level >= slog.LevelError:
		return LogLevelError
	case level >= slog.LevelWarn:
		return LogLevelWarn
	case level >= slog.LevelInfo:
		return LogLevelInfo
	case level >= slog.LevelDebug:
		return LogLevelDebug
	default:
		return LogLevelTrace
	}
}
