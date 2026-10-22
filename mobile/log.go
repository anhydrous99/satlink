package mobile

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// Logging, as the module's other logging packages do it: through the
// process-wide slog logger, with this package as the record's "pkg"
// attribute (see "Logging" in docs/PORTING.md). This package logs only the
// panics it recovers where no caller can be told; SetLogSink forwards
// every package's records to the app.

// logPackage names this package in log records; SetLogSink hands it to the
// app's sink as the record's target.
const logPackage = "mobile"

// logError logs msg at error level, like Rust's log::error!.
func logError(msg string) {
	logger := slog.Default()
	ctx := context.Background()
	if !logger.Enabled(ctx, slog.LevelError) {
		return
	}
	logger.Log(ctx, slog.LevelError, msg, logtext.PackageKey, logPackage)
}

// logPanic recovers a panic in progress in a goroutine this package
// started, and logs it: a panic that ended the goroutine would end the app.
// goroutine says what the goroutine does, such as "the link listener".
func logPanic(goroutine string) {
	if recovered := recover(); recovered != nil {
		logError(fmt.Sprintf("%s panicked: %v", goroutine, recovered))
	}
}
