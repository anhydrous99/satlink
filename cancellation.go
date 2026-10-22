package satlink

import "sync/atomic"

// Stopping a data call while it is being set up.

// Cancellation stops a data call that is being set up, from any goroutine —
// a cancel button's, or a signal handler's. A dial or answer still waiting
// for CONNECT is aborted, as one that timed out is: a byte to the handset
// ends the attempt, and a call that connects all the same is ended, so none
// is left up to bill. The setup then fails with an [*Error] of kind
// [ErrCancelled]. The [DataCallSettings] of the dial or answer carry it;
// [DefaultDataCallSettings] makes a new one.
//
// Copies of the settings share their *Cancellation, and so one request,
// which cannot be withdrawn: use a new Cancellation for each call.
//
// A context can drive it: context.AfterFunc calls Cancel once the context is
// done, and its stop function lets the context go once the call is set up.
//
//	settings := satlink.DefaultDataCallSettings()
//	stop := context.AfterFunc(ctx, settings.Cancellation.Cancel)
//	call, err := handset.DialData(number, settings)
//	stop()
//
// The zero Cancellation is ready to use, not cancelled; it must not be copied
// once used. A Cancellation is safe for use by several goroutines.
type Cancellation struct {
	requested atomic.Bool
}

// NewCancellation returns a Cancellation that has not been cancelled.
func NewCancellation() *Cancellation {
	return &Cancellation{}
}

// Cancel asks the call setup to stop. It is harmless once the call is up or
// over, and on a Cancellation cancelled before. Cancel panics on a nil
// Cancellation: settings without one cannot be cancelled.
func (c *Cancellation) Cancel() {
	c.requested.Store(true)
}

// Cancelled reports whether Cancel has been called. A nil Cancellation is
// never cancelled, so that settings without one can be asked too.
func (c *Cancellation) Cancelled() bool {
	return c != nil && c.requested.Load()
}
