package logtext

import "log/slog"

// LevelTrace is the level of the finest details, Rust's log::trace!: four
// below debug, as slog spaces its own levels.
const LevelTrace = slog.LevelDebug - 4

// PackageKey is the attribute that names the package a record comes from,
// as the Rust log target did: "satnet", "sim" or "ipstack".
const PackageKey = "pkg"

// LevelName returns the name a level is shown with: "ERROR" at error and
// above, then "WARN", "INFO" and "DEBUG" from those levels up, and "TRACE"
// below debug.
func LevelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARN"
	case level >= slog.LevelInfo:
		return "INFO"
	case level >= slog.LevelDebug:
		return "DEBUG"
	default:
		return "TRACE"
	}
}
