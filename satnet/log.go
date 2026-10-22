package satnet

import (
	"context"
	"log/slog"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// Logging, as the module's other logging packages do it: through the
// process-wide slog logger, with this package as the record's "pkg"
// attribute (see "Logging" in docs/PORTING.md).

// logPackage names this package in log records, as the Rust log target
// did; the CLI and mobile.SetLogSink show it as the record's origin.
const logPackage = "satnet"

// logDebug logs msg at debug level, like Rust's log::debug!.
func logDebug(msg string, args ...any) {
	logAt(slog.LevelDebug, msg, args)
}

// logInfo logs msg at info level, like Rust's log::info!.
func logInfo(msg string, args ...any) {
	logAt(slog.LevelInfo, msg, args)
}

// logWarn logs msg at warning level, like Rust's log::warn!.
func logWarn(msg string, args ...any) {
	logAt(slog.LevelWarn, msg, args)
}

// logAt logs msg and args, slog key-value pairs, with the "pkg" attribute.
// The logger is looked up at log time, as Rust's log facade is, so a
// handler installed after the link was built still receives the record.
// When the level is disabled no record is built, but msg is: the callers
// format Rust's text before the call, so a line costs its formatting even
// when it is not logged.
func logAt(level slog.Level, msg string, args []any) {
	logger := slog.Default()
	ctx := context.Background()
	if !logger.Enabled(ctx, level) {
		return
	}
	logger.Log(ctx, level, msg, append([]any{logtext.PackageKey, logPackage}, args...)...)
}
