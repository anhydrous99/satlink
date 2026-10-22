package satnet

import (
	"context"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// State shared by the link's driver goroutine and the app's connections.

// maxWait is the longest a blocked operation sleeps before looking again,
// even unprompted.
const maxWait = time.Second

// shared is the engine behind its lock, with the signal that wakes whoever
// waits for it to change: Rust's Mutex<Engine> and Condvar.
type shared struct {
	mu      sync.Mutex
	engine  *engine
	changed broadcast
	// waiting counts the calls waiting in waitForChange; mu guards it.
	// Tests read it to act only once a call waits.
	waiting int
}

// newShared returns the shared state around engine.
func newShared(e *engine) *shared {
	s := &shared{engine: e}
	s.changed.start()
	return s
}

// lock locks the engine and returns it; unlock releases it. Callers unlock
// with defer: a panic while the engine is locked ends the link, and the
// driver still hangs up, so the engine must stay usable for that, and for
// the app to see the link close.
func (s *shared) lock() *engine {
	s.mu.Lock()
	return s.engine
}

// unlock releases the engine that lock locked.
func (s *shared) unlock() {
	s.mu.Unlock()
}

// notify wakes everything waiting for the engine to change. It may be
// called with the engine locked or not.
func (s *shared) notify() {
	s.changed.notify()
}

// update runs change with the engine locked, then wakes everything waiting
// for the engine to change.
func (s *shared) update(change func(e *engine)) {
	defer s.notify()
	e := s.lock()
	defer s.unlock()
	change(e)
}

// status returns a snapshot of the link.
func (s *shared) status() Status {
	e := s.lock()
	defer s.unlock()
	return e.status()
}

// withLock runs f with the engine locked and returns its error.
func (s *shared) withLock(f func(e *engine) error) error {
	e := s.lock()
	defer s.unlock()
	return f(e)
}

// requestClose asks the driver to close the link: what was written is still
// delivered, then the call is hung up.
func (s *shared) requestClose() {
	s.update((*engine).requestShutdown)
}

// broadcast wakes every goroutine waiting for a change, as a condition
// variable's broadcast does, through a channel that a waiter can select
// together with a timer and a context: each waiter takes the current
// channel, and notify closes it and starts a new one.
type broadcast struct {
	current atomic.Pointer[chan struct{}]
}

// start makes the first channel.
func (b *broadcast) start() {
	first := make(chan struct{})
	b.current.Store(&first)
}

// wait returns the channel that the next notify closes.
func (b *broadcast) wait() <-chan struct{} {
	return *b.current.Load()
}

// notify closes the channel waiters hold, and starts a new one.
func (b *broadcast) notify() {
	next := make(chan struct{})
	close(*b.current.Swap(&next))
}

// deadline is when a wait gives up, looked up again every time the wait
// wakes, so that a deadline moved meanwhile applies at once. It returns the
// zero time for none.
type deadline func() time.Time

// fixedDeadline returns a deadline that never moves: at, or none for the
// zero time.
func fixedDeadline(at time.Time) deadline {
	return func() time.Time { return at }
}

// deadlineAfter returns the moment timeout from now. The longest timeout is
// no deadline in effect: time.Time.Add drops the monotonic reading rather
// than overflow, which leaves a moment some 292 years ahead.
func deadlineAfter(timeout time.Duration) time.Time {
	return time.Now().Add(timeout)
}

// timeToWait returns how long to sleep before looking again: until deadline
// (the zero time: none) but never more than maxWait, and false once it has
// passed.
func timeToWait(deadline time.Time) (time.Duration, bool) {
	if deadline.IsZero() {
		return maxWait, true
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, false
	}
	return min(remaining, maxWait), true
}

// waitTerms are what a blocking operation waits under.
type waitTerms struct {
	// operation names the operation in its ErrTimedOut, such as "TCP read".
	operation string
	// until is when the operation gives up.
	until deadline
	// ctx abandons the wait once it is done (Link.DialContext's); nil
	// for none.
	ctx context.Context
	// closed is the closed flag of the connection that waits, guarded by
	// the engine's lock; nil for a wait of the link itself.
	closed *bool
}

// attempt tries an operation once, with the engine locked, and reports
// whether it is over (done), successfully or with err. Not done and no
// error mean: wait for the engine to change, and try again.
type attempt func(e *engine) (done bool, err error)

// waitUntil runs try after every change of the engine until it is over.
// Before each try it checks whether the connection has been closed
// (net.ErrClosed); after a try that is not over, whether the link is still
// alive, then whether the deadline has passed (ErrTimedOut), so that data
// already received beats an expired deadline. It also gives up when the
// wait's context is done, with the context's error.
func (s *shared) waitUntil(w waitTerms, try attempt) error {
	e := s.lock()
	defer s.unlock()
	for {
		if w.closed != nil && *w.closed {
			return net.ErrClosed
		}
		if done, err := try(e); done || err != nil {
			return err
		}
		if err := e.checkAlive(); err != nil {
			return err
		}
		pause, ok := timeToWait(w.until())
		if !ok {
			return &Error{Kind: ErrTimedOut, Operation: w.operation}
		}
		if cancelled := s.waitForChange(doneChannel(w.ctx), pause); cancelled {
			return w.ctx.Err()
		}
	}
}

// waitFor runs found after every change of the engine until it finds what
// it looks for, or until passes, which it reports with false. Unlike
// waitUntil, it keeps waiting while the link goes down: that is what a
// Watcher watches.
func (s *shared) waitFor(until time.Time, found func(e *engine) bool) bool {
	e := s.lock()
	defer s.unlock()
	for !found(e) {
		pause, ok := timeToWait(until)
		if !ok {
			return false
		}
		// Nothing can cancel a Watcher's wait: no channel.
		s.waitForChange(nil, pause)
	}
	return true
}

// waitForChange sleeps, with the engine unlocked, until the engine changes,
// pause passes or cancelled delivers, and reports whether cancelled did.
// The engine is locked again when it returns.
func (s *shared) waitForChange(cancelled <-chan struct{}, pause time.Duration) bool {
	changed := s.changed.wait()
	s.waiting++
	s.mu.Unlock()
	defer s.endWait()
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-changed:
		return false
	case <-timer.C:
		return false
	case <-cancelled:
		return true
	}
}

// endWait locks the engine again after a wait in waitForChange, and counts
// the wait as over.
func (s *shared) endWait() {
	s.mu.Lock()
	s.waiting--
}

// doneChannel returns ctx's Done channel, or nil — which never delivers —
// for a nil ctx.
func doneChannel(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

// engine is the link's state: the TCP/IP stack, the name cache, and what
// the app observes. It is not safe for concurrent use: shared guards it.
type engine struct {
	stack *stack
	names *nameCache
	// state is where the link is in its life.
	state LinkState
	// stateChanges are the changes no Watcher has taken yet, oldest first.
	stateChanges []LinkState
	// connectMessage is what followed CONNECT.
	connectMessage string
	// ipv4 is what PPP agreed on; nil until it has.
	ipv4  *IPv4Settings
	usage Usage
	// lastActivity is when the app last moved data, for the idle timeout.
	lastActivity time.Time
	// shutdownRequested reports that the app asked to close the link.
	shutdownRequested bool
	// callMayStillBeUp reports that the hang-up could not be confirmed.
	callMayStillBeUp bool
}

// newEngine returns the engine of a link whose call connected at now, with
// connectMessage, while PPP negotiates.
func newEngine(stack *stack, names *nameCache, connectMessage string, now time.Time) *engine {
	return &engine{
		stack:          stack,
		names:          names,
		state:          LinkState{Kind: LinkStateNegotiating},
		connectMessage: connectMessage,
		lastActivity:   now,
	}
}

// status returns a snapshot of the link, with its own copy of the IPv4
// settings.
func (e *engine) status() Status {
	status := Status{
		State:            e.state,
		ConnectMessage:   e.connectMessage,
		Usage:            e.usage,
		CallMayStillBeUp: e.callMayStillBeUp,
	}
	if e.ipv4 != nil {
		status.IPv4 = copyIPv4Settings(*e.ipv4)
	}
	return status
}

// isUp reports whether IP traffic flows.
func (e *engine) isUp() bool {
	return e.state.Kind == LinkStateUp
}

// isNegotiating reports whether PPP has yet to come up for the first time.
func (e *engine) isNegotiating() bool {
	return e.state.Kind == LinkStateNegotiating
}

// checkUsable returns nil unless the link is going or gone, in which case it
// returns ErrLinkDown with the reason.
func (e *engine) checkUsable() error {
	switch e.state.Kind {
	case LinkStateClosing, LinkStateClosed:
		return &Error{Kind: ErrLinkDown, Reason: e.state.Reason}
	default:
		return nil
	}
}

// checkAlive returns nil while connections still carry data: the link is
// usable, or delivering what was written after a requested close. Starting
// anything new needs checkUsable.
func (e *engine) checkAlive() error {
	if e.state == closingState(CloseReasonRequested) {
		return nil
	}
	return e.checkUsable()
}

// connectionError is the error of an operation on a connection that died:
// the link's fault if it is going down, otherwise the peer's.
func (e *engine) connectionError() error {
	if err := e.checkUsable(); err != nil {
		return err
	}
	return &Error{Kind: ErrConnectionReset}
}

// linkUp records that PPP agreed on addresses (again), and that the peer
// takes packets of up to mtu bytes: IP traffic can flow.
func (e *engine) linkUp(settings IPv4Settings, mtu int, now time.Time) error {
	if err := e.stack.configure(settings, mtu, now); err != nil {
		return err
	}
	e.ipv4 = copyIPv4Settings(settings)
	if e.state.Kind == LinkStateNegotiating {
		// Negotiating is not idling; renegotiating later is not traffic.
		e.lastActivity = now
	}
	// Renegotiating while closing must not reopen the link.
	if e.checkUsable() == nil {
		e.setState(LinkState{Kind: LinkStateUp})
	}
	return nil
}

// beginDraining records that the app asked to close: nothing new may start,
// but data already written may still leave.
func (e *engine) beginDraining() {
	e.setState(closingState(CloseReasonRequested))
}

// beginClosing records that the hang-up starts, for reason: every
// connection ends now. Closing first, as abandon puts Closed first: should
// aborting the connections fail, the link still says why it closes while
// the driver hangs up all the same. Both happen under the engine's lock,
// so nobody sees them in the other order.
func (e *engine) beginClosing(reason CloseReason) {
	closing := LinkState{Kind: LinkStateClosing, Reason: reason}
	if e.state != closing {
		e.setState(closing)
	}
	e.stack.abortAll()
}

// finishClosing records that the hang-up is over; if it could not be
// confirmed, the call may still be up.
func (e *engine) finishClosing(reason CloseReason, callMayStillBeUp bool) {
	e.callMayStillBeUp = callMayStillBeUp
	e.setState(LinkState{Kind: LinkStateClosed, Reason: reason})
}

// abandon records that the driver died before it could finish closing:
// whoever waits for the link must still hear that it closed — for the
// reason it was closing for, or for an internal error, message, if it was
// not closing yet — and nothing confirmed that the call ended.
func (e *engine) abandon(message string) {
	var reason CloseReason
	switch e.state.Kind {
	case LinkStateClosed:
		return
	case LinkStateClosing:
		reason = e.state.Reason
	default:
		reason = CloseReason{Kind: CloseReasonInternal, Detail: message}
	}
	// Closed first, so that the link is reported closed even should
	// aborting the connections fail too.
	e.finishClosing(reason, true)
	e.stack.abortAll()
}

// requestShutdown records that the app asked to close the link.
func (e *engine) requestShutdown() {
	e.shutdownRequested = true
}

// recordActivity records that the app moved data at now: the link is not
// idle.
func (e *engine) recordActivity(now time.Time) {
	e.lastActivity = now
}

// idleFor returns how long the app has moved no data, at now.
func (e *engine) idleFor(now time.Time) time.Duration {
	return max(now.Sub(e.lastActivity), 0)
}

// takeStateChange returns the oldest state change not yet taken, and false
// when there is none.
func (e *engine) takeStateChange() (LinkState, bool) {
	if len(e.stateChanges) == 0 {
		return LinkState{}, false
	}
	change := e.stateChanges[0]
	e.stateChanges = e.stateChanges[1:]
	return change, true
}

// setState moves the link to state and queues the change for the Watchers.
func (e *engine) setState(state LinkState) {
	e.state = state
	e.stateChanges = append(e.stateChanges, state)
}

// closingState is the state of a link closing for a reason without details.
func closingState(kind CloseReasonKind) LinkState {
	return LinkState{Kind: LinkStateClosing, Reason: CloseReason{Kind: kind}}
}

// copyIPv4Settings returns a copy of settings that shares nothing with it.
func copyIPv4Settings(settings IPv4Settings) *IPv4Settings {
	settings.DNSServers = slices.Clone(settings.DNSServers)
	return &settings
}
