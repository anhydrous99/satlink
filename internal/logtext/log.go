package logtext

import (
	"context"
	"log/slog"
)

// Log logs msg at level through slog.Default, with pkg as the record's
// [PackageKey] attribute, followed by args, slog key-value pairs. It is how
// the module's packages log.
//
// The logger is looked up at log time, so a handler installed after the
// caller's values were built still receives the record. At a level the
// logger does not enable, no record is built; the caller has built msg and
// args all the same.
func Log(pkg string, level slog.Level, msg string, args ...any) {
	logger := slog.Default()
	ctx := context.Background()
	if !logger.Enabled(ctx, level) {
		return
	}
	logger.Log(ctx, level, msg, append([]any{PackageKey, pkg}, args...)...)
}
