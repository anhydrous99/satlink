package sim

import (
	"log/slog"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// Logging, as the module's other logging packages do it: through the
// process-wide slog logger, with this package as the record's "pkg"
// attribute (see "Logging" in docs/DESIGN.md).

// logPackage names this package in log records; the CLI and
// mobile.SetLogSink show it as the record's origin.
const logPackage = "sim"

// logDebug logs msg at debug level.
func logDebug(msg string) {
	logtext.Log(logPackage, slog.LevelDebug, msg)
}

// logInfo logs msg at info level.
func logInfo(msg string) {
	logtext.Log(logPackage, slog.LevelInfo, msg)
}

// logWarn logs msg at warning level.
func logWarn(msg string) {
	logtext.Log(logPackage, slog.LevelWarn, msg)
}
