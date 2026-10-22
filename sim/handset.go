package sim

import (
	"io"
	"net/netip"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anhydrous99/satlink"
)

// The simulated handset as the app sees it, a transport, and the control
// handle that steers it from a test or a user interface. Both share one
// modem behind a mutex; only this file's code reads the clock, while
// holding the lock, so the simulation sees time move forward even when
// several goroutines use it.

// pollInterval is how long a waiting ReadTimeout sleeps before advancing
// the simulation again: short enough to keep simulated latency and pacing
// accurate to a few milliseconds, long enough not to busy the processor.
const pollInterval = 2 * time.Millisecond

// closedMessage is what I/O on a closed Handset fails with.
const closedMessage = "the simulated handset is closed"

// Handset is a simulated Iridium 9555/9575 handset.
//
// It speaks the AT command set as a satlink.Transport, stores and sends
// SMS, and connects data calls to a PPP gateway with a small internet
// behind it. It also implements satlink.DTRController and io.Closer. Steer
// it with the handle from [Handset.Control]. A Handset is safe for use by
// several goroutines. The zero Handset is not usable; create one with
// [New].
//
// Close it as a port is closed. A Handset garbage-collected without Close
// drops DTR all the same, as a port does when the app that held it is gone,
// but only once the collector has noticed: an app that wants its call to
// end calls Close.
type Handset struct {
	shared *sharedModem
	closed atomic.Bool
}

// A Handset is a transport with a DTR line that closes like a port.
var (
	_ satlink.Transport     = (*Handset)(nil)
	_ satlink.DTRController = (*Handset)(nil)
	_ io.Closer             = (*Handset)(nil)
)

// sharedModem is what a Handset and its Controls hold. Once none of them
// is left, a cleanup ends what the modem runs in the background: the TLS
// sessions of a call still up, whose goroutines would otherwise keep them
// alive, as Rust frees them with the last handle to the modem.
type sharedModem struct {
	locked *lockedModem
}

// lockedModem is the modem and the mutex that guards it. The cleanup
// takes the mutex too, so that it sees what the last user did.
type lockedModem struct {
	mu    sync.Mutex
	modem *modem
}

// New returns a handset in command mode, with echo on, factory settings and
// an empty SIM. A configuration the PPP gateway cannot start with is not
// refused here: data calls then fail with NO CARRIER, as on a real network.
func New(config Config) *Handset {
	locked := &lockedModem{modem: newModem(cloneConfig(config))}
	shared := &sharedModem{locked: locked}
	runtime.AddCleanup(shared, (*lockedModem).shutDown, locked)
	handset := &Handset{shared: shared}
	// The Rust handset dropped DTR when it was dropped, ending a call after
	// AT&D2 even if the app crashed; a Handset nobody can reach any more
	// does the same. After Close it changes nothing: DTR is down already.
	runtime.AddCleanup(handset, (*lockedModem).dropDTR, locked)
	return handset
}

// cloneConfig returns a copy of config that shares nothing with it, so
// that the caller may change its own afterwards.
func cloneConfig(config Config) Config {
	config.Network.DNSServers = slices.Clone(config.Network.DNSServers)
	config.Network.SilentAddresses = slices.Clone(config.Network.SilentAddresses)
	config.Network.Hosts = slices.Clone(config.Network.Hosts)
	if config.Authentication != nil {
		authentication := *config.Authentication
		config.Authentication = &authentication
	}
	return config
}

// Control returns a handle for steering the simulation from tests or a user
// interface.
func (h *Handset) Control() *Control {
	return &Control{shared: h.shared}
}

// WriteAll hands p to the handset, which takes everything at once and
// never blocks. After Close it fails with a *satlink.Error of kind
// ErrTransport, "the simulated handset is closed".
func (h *Handset) WriteAll(p []byte) error {
	if h.closed.Load() {
		return closedError()
	}
	h.shared.locked.use(func(m *modem, now time.Time) { m.receive(p, now) })
	return nil
}

// ReadTimeout advances the simulation and returns what the handset has to
// say; when there is nothing, it keeps advancing the simulation, every 2 ms,
// until something arrives or timeout passes, and then returns 0 and a nil
// error. Timeouts longer than a year count as a year. After Close it fails
// as WriteAll does, and so does a ReadTimeout that Close interrupts.
func (h *Handset) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	deadline := later(time.Now(), timeout)
	for {
		if h.closed.Load() {
			return 0, closedError()
		}
		var received int
		var now time.Time
		h.shared.locked.use(func(m *modem, at time.Time) {
			now = at
			m.advance(now)
			received = m.readInto(p)
		})
		if received > 0 || len(p) == 0 || !now.Before(deadline) {
			return received, nil
		}
		time.Sleep(min(pollInterval, deadline.Sub(now)))
	}
}

// RaiseDTR raises DTR, which changes nothing on the simulated handset.
// After Close it fails as WriteAll does.
func (h *Handset) RaiseDTR() error {
	if h.closed.Load() {
		return closedError()
	}
	return nil
}

// DropDTR drops DTR: once the app has asked for that with AT&D2, it ends
// any call. After Close it fails as WriteAll does.
func (h *Handset) DropDTR() error {
	if h.closed.Load() {
		return closedError()
	}
	h.shared.locked.dropDTR()
	return nil
}

// Close closes the port, which drops DTR: once the app has asked for that
// with AT&D2, any call ends, even if the app crashed. Afterwards WriteAll
// and ReadTimeout fail, while the [Control] keeps working. Close is
// idempotent.
func (h *Handset) Close() error {
	if h.closed.Swap(true) {
		return nil
	}
	h.shared.locked.dropDTR()
	return nil
}

// Control steers a [Handset] from outside: the network's side of calls and
// messages, and what the handset has seen. It is usable from any goroutine.
// The zero Control is not usable; [Handset.Control] makes one.
type Control struct {
	shared *sharedModem
}

// DropCall ends the data call, or the attempt to set one up, from the
// network side: the handset reports NO CARRIER and returns to command
// mode. It does nothing when no call is up.
func (c *Control) DropCall() {
	c.shared.locked.use(func(m *modem, _ time.Time) { m.dropCall() })
}

// RestartPPP makes the gateway start PPP over on the data call and assign
// clientAddress this time, as RFC 1332 allows; whatever was open on the old
// address is lost. Without a call, nothing happens.
//
// It fails, changing nothing, with a *[ConfigError] when the gateway cannot
// start PPP with clientAddress: one that is not IPv4.
func (c *Control) RestartPPP(clientAddress netip.Addr) error {
	var err error
	c.shared.locked.use(func(m *modem, now time.Time) { err = m.restartPPP(clientAddress, now) })
	return err
}

// ReceiveSMS delivers a message from the sender from to the inbox; the
// handset announces it with +CMTI: "SM",<index>, using the lowest free
// storage index.
//
// The announcement waits while a call is up or an SMS is being typed, and
// until the app has read the handset's earlier output.
func (c *Control) ReceiveSMS(from, text string) {
	c.shared.locked.use(func(m *modem, now time.Time) { m.receiveSMS(from, text, now) })
}

// Stats returns what the handset has done so far, as a copy for the caller
// to keep.
func (c *Control) Stats() Stats {
	var stats Stats
	c.shared.locked.use(func(m *modem, _ time.Time) {
		stats = m.stats
		stats.SMSSent = slices.Clone(m.stats.SMSSent)
	})
	return stats
}

// use runs step on the modem while holding the lock, at the time it
// reads once it holds it: so the modem never sees time go backwards, even
// when several goroutines use it.
func (l *lockedModem) use(step func(m *modem, now time.Time)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	step(l.modem, time.Now())
}

// dropDTR drops DTR, as lowering it or closing the port does.
func (l *lockedModem) dropDTR() {
	l.use(func(m *modem, _ time.Time) { m.dropDTR() })
}

// shutDown ends what the modem runs in the background, once nothing can
// use it any more.
func (l *lockedModem) shutDown() {
	l.use(func(m *modem, _ time.Time) { m.shutDown() })
}

// closedError returns what I/O on a closed Handset fails with.
func closedError() error {
	return &satlink.Error{Kind: satlink.ErrTransport, Message: closedMessage}
}
