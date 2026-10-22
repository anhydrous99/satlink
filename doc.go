// Package satlink is a transport-agnostic client for Iridium satellite
// handsets.
//
// Iridium handsets (9555, 9555A, 9575 family, ROC service port) expose a USB
// CDC-ACM virtual serial port that speaks a Hayes/3GPP-style AT command set.
// This package implements that protocol on top of any byte [Transport], so
// the same code runs over a desktop serial port (package serial), an Android
// USB-host connection (package mobile), or a test double such as the
// simulated handset of package sim.
//
// A [Handset] covers the handset itself: identity, signal, battery,
// registration, the clock, SMS in text mode, voice calls and raw commands.
// Unsolicited result codes (incoming SMS, RING, …) seen while waiting for
// responses are queued; fetch them with [Handset.TakeURCs] or wait for new
// ones with [Handset.PollURCs].
//
// [Handset.DialData] and [Handset.AnswerData] place circuit-switched data
// calls; the [Cancellation] in their [DataCallSettings] stops one while it
// is being set up, from another goroutine. A [DataCall] is a [Transport] of
// its own: what is read and written through it belongs to the remote end,
// not to the AT command interpreter. Package satnet runs an app-private IP
// link over one.
//
// # Blocking
//
// Every method blocks until the handset has answered or its timeout has
// passed. Ordinary commands wait five seconds by default
// ([Handset.SetTimeout] changes that); SMS submission waits up to two
// minutes, as it goes over the satellite link; dialling waits up to ninety
// seconds. A timeout too long for the clock, such as math.MaxInt64, means no
// deadline; while waiting, the handset reads for at most a second at a
// time, so a transport is never handed an absurd timeout. Call the methods
// off a user interface's main thread.
//
// # Data calls and the handset
//
// Every path out of a data call must really end it: a forgotten data call
// keeps airtime billing by the minute. While a data call holds the handset —
// directly, or through a satnet link — the handset's methods that talk to it
// fail at once with an [*Error] of kind [ErrBusy] instead of corrupting the
// call. Hanging up, successfully or not, releases the handset.
//
// A hang-up can fail: neither ATH after the escape nor a DTR drop is
// confirmed. The call may then still be up and billing, and the error says
// so: errors.Is(err, [ErrCallMayStillBeUp]) holds, for this package's
// errors and for satnet's. So it does when [Handset.Initialize] finds a
// call a previous session left up and cannot end it, or [Handset.Answer]
// cannot end a data call it answered by mistake. [Handset.EndDataCall]
// tries again once the handset answers; power-cycling the handset is the
// last resort.
//
// # Concurrency
//
// A Handset is safe for use by several goroutines: each method holds the
// handset for its whole exchange, so concurrent commands run one after
// another. A DataCall is used by one goroutine at a time.
//
// # Errors
//
// Errors are [*Error] values. Each [ErrorKind] constant is also a sentinel:
// errors.Is(err, satlink.ErrTimeout) tests the kind, and errors.As gives the
// details. [ErrCallMayStillBeUp] matches more than its kind: also a failed
// hang-up, whatever its kind.
package satlink
