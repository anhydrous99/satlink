package mobile

import "github.com/anhydrous99/satlink/iridium"

// Stopping a link while it is being set up.

// Cancellation stops a link being set up — from a cancel button, say —
// while one of the EstablishLink functions blocks on another thread: a dial
// still in progress is aborted, and a call that came up is hung up, so none
// is left to bill. The EstablishLink function then fails with
// SatlinkErrorCancelled. Use a new one for each link; once the link is up,
// SatelliteLink.HangUp ends it instead. A Cancellation is safe for use from
// several threads.
type Cancellation struct {
	// core is the handset's own cancellation, which the link's call
	// settings point to. Its zero value is ready to use, and so is a zero
	// Cancellation, such as gomobile's constructor would make.
	core iridium.Cancellation
}

// NewCancellation returns a Cancellation that has not been cancelled.
func NewCancellation() *Cancellation {
	return &Cancellation{}
}

// Cancel stops the setup, from any thread; it is harmless once the setup is
// over.
func (c *Cancellation) Cancel() {
	defer survivePanic("Cancellation.Cancel")
	c.core.Cancel()
}

// Cancelled reports whether Cancel has been called.
func (c *Cancellation) Cancelled() (cancelled bool) {
	defer survivePanic("Cancellation.Cancelled")
	return c.core.Cancelled()
}
