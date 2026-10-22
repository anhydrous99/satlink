// Package netlog is the logging every ipstack package shares: smoltcp's
// net_trace! and net_debug! macros, written to the default slog logger with
// the "pkg" attribute set to "ipstack".
//
// The logger is looked up at log time, as Rust's global log facade is, so a
// handler installed after the stack was built still receives its messages.
// Attribute values are formatted by the handler, and only when the level is
// enabled. A call's arguments are built all the same, where smoltcp's macros
// evaluate nothing with the level off: a value that an interface cannot hold
// without allocating, such as an address or a Repr, still costs an
// allocation per call. Keep such calls out of loops over octets.
package netlog

import (
	"context"
	"log/slog"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// LevelTrace is the level of smoltcp's net_trace! messages: the module's
// trace level, logtext.LevelTrace, below debug as Rust's trace level is.
const LevelTrace = logtext.LevelTrace

// packageName is the "pkg" attribute of every message, the Rust log target.
const packageName = "ipstack"

// Trace logs msg at LevelTrace, like smoltcp's net_trace!.
func Trace(msg string, args ...any) {
	logAt(LevelTrace, msg, args)
}

// Debug logs msg at slog.LevelDebug, like smoltcp's net_debug!.
func Debug(msg string, args ...any) {
	logAt(slog.LevelDebug, msg, args)
}

func logAt(level slog.Level, msg string, args []any) {
	logger := slog.Default()
	ctx := context.Background()
	if !logger.Enabled(ctx, level) {
		return
	}
	logger.Log(ctx, level, msg, append([]any{logtext.PackageKey, packageName}, args...)...)
}
