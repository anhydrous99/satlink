package iridium

// The handset's lock and its claim by a data call: concurrent methods run
// one after another, and while a data call holds the handset they fail at
// once instead of corrupting the call.

// exchange runs talk holding the handset's lock, so that concurrent methods
// run one after another, and returns its result. While a data call holds
// the handset, exchange fails at once with ErrBusy instead.
func exchange[T any](h *Handset, talk func() (T, error)) (T, error) {
	if err := h.lock(); err != nil {
		var nothing T
		return nothing, err
	}
	defer h.mu.Unlock()
	return talk()
}

// perform is exchange for talk that returns nothing but its error.
func (h *Handset) perform(talk func() error) error {
	if err := h.lock(); err != nil {
		return err
	}
	defer h.mu.Unlock()
	return talk()
}

// lock takes the handset's lock for one exchange; the caller unlocks h.mu.
// While a data call holds the handset, lock fails with ErrBusy: at once,
// and again once it has the lock, as a dial may have claimed the handset
// while it waited.
func (h *Handset) lock() error {
	if h.claimed.Load() {
		return busyError()
	}
	h.mu.Lock()
	if h.claimed.Load() {
		h.mu.Unlock()
		return busyError()
	}
	return nil
}

// claim makes the handset busy for a data call being set up. It fails with
// ErrBusy if a data call holds the handset already.
//
// A dial claims the handset before it waits for the lock, so that a method
// called meanwhile fails at once rather than wait behind a setup that may
// take ninety seconds. While the dial still waits for an exchange in
// progress, such as a PollURCs, other methods fail with ErrBusy, and its
// Cancellation is looked at only once it has the lock.
func (h *Handset) claim() error {
	if !h.claimed.CompareAndSwap(false, true) {
		return busyError()
	}
	return nil
}

// release gives the handset back to its own methods: the data call is over,
// or never came up.
func (h *Handset) release() {
	h.claimed.Store(false)
}
