// Package logtext renders log records for people. It is the one rendering
// that the command-line tool and mobile.SetLogSink share, so that a record
// reads the same in a terminal and in an app's log.
//
// A record becomes three parts:
//   - its level, named as the Rust log crate names levels — ERROR, WARN,
//     INFO, DEBUG, and TRACE below debug, where slog itself would say
//     DEBUG-4 ([LevelName]);
//   - the package that logged it: the record's [PackageKey] attribute,
//     which the module's logging packages (satnet, sim, ipstack) add as the
//     Rust version's log target, or one added with slog.Logger.With; empty
//     when there is none;
//   - its text: the message, then " key=value" for every other attribute
//     in order, quoted as slog.TextHandler quotes them, with groups written
//     as group.key.
//
// [NewHandler] makes a slog.Handler that renders records so and hands the
// three parts to a function: the command-line tool prints them as
// "[LEVEL pkg] text", and mobile passes them to the app's log sink.
package logtext
