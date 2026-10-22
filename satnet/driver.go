package satnet

import (
	"context"
	"errors"
	"fmt"
	"runtime/pprof"
	"time"

	"github.com/anhydrous99/satlink/internal/debugtext"
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/ppp"
)

// The link's driver goroutine: it owns the data call, runs PPP and the IP
// stack, enforces the limits, and hangs up when the link ends.

// pollInterval is the longest the driver waits on the handset before
// looking at the app's sockets again: short against a satellite round trip,
// cheap on the CPU.
const pollInterval = 20 * time.Millisecond

// readChunk is how much the driver reads from the handset at a time.
const readChunk = 1024

// framingOverhead is PPP's framing around a datagram — flags, header and
// FCS — counted when estimating when the datagram will have left the line.
const framingOverhead = 8

// driverName names the driver goroutine in profiles.
const driverName = "satlink-link"

// ipcpProtocol names IPCP in the close reason of an address the stack
// cannot use.
const ipcpProtocol = "IPCP"

// The details of internal close reasons: Close's once a panic got past the
// driver's run, and that of a panic whose value says nothing — neither text
// nor an error that can describe itself.
const (
	panickedDriverDetail   = "the link's driver thread panicked"
	undescribedPanicDetail = "the link driver panicked"
)

// driver runs one data call. Only its own goroutine uses it, apart from the
// engine, which it shares.
type driver struct {
	call    *iridium.DataCall
	ppp     *ppp.Endpoint
	shared  *shared
	limits  Limits
	tuning  Tuning
	pacer   *pacer
	repeats *repeatFilter
	// connectedAt is when the call connected.
	connectedAt time.Time
	// negotiationDeadline is when PPP must be up by; the zero time once it
	// is.
	negotiationDeadline time.Time
	// drainDeadline is when a requested close stops waiting for what was
	// written to be delivered; the zero time until one starts draining.
	drainDeadline time.Time
	// hangUpTime is how long hanging up takes, so that the call time limit
	// can allow for it.
	hangUpTime time.Duration
	// cancellation stops the link while PPP comes up, as it stopped the
	// dial; nil for none.
	cancellation *iridium.Cancellation
	// readBuffer receives what the handset sends.
	readBuffer []byte
}

// driverOutcome is how a driver's run ended, for Link.Close: nil once the
// call is hung up, otherwise why the hang-up failed. It is set once.
type driverOutcome struct {
	done chan struct{}
	err  error
}

// newDriverOutcome returns an outcome still to come.
func newDriverOutcome() *driverOutcome {
	return &driverOutcome{done: make(chan struct{})}
}

// complete sets the outcome to err and wakes whoever waits for it.
func (o *driverOutcome) complete(err error) {
	o.err = err
	close(o.done)
}

// wait waits for the outcome and returns it.
func (o *driverOutcome) wait() error {
	<-o.done
	return o.err
}

// startDriver starts the driver of call, which just connected, with PPP
// endpoint and config, and returns the engine it shares with the link and
// its outcome.
func startDriver(call *iridium.DataCall, endpoint *ppp.Endpoint, config *Config) (*shared, *driverOutcome) {
	now := time.Now()
	linkEngine := newEngine(
		newStack(config.Tuning, now),
		newNameCache(config.Hosts, config.Tuning.DNSCacheLifetime),
		call.ConnectMessage(),
		now,
	)
	sharedEngine := newShared(linkEngine)
	outcome := newDriverOutcome()
	driver := &driver{
		call:                call,
		ppp:                 endpoint,
		shared:              sharedEngine,
		limits:              config.Limits,
		tuning:              config.Tuning,
		pacer:               newPacer(config.Tuning.LinkRate, now),
		repeats:             newRepeatFilter(config.Tuning.RepeatSuppression),
		connectedAt:         now,
		negotiationDeadline: now.Add(config.Tuning.NegotiationTimeout),
		hangUpTime:          config.hangUpTime(),
		cancellation:        config.Call.Cancellation,
		readBuffer:          make([]byte, readChunk),
	}
	go pprof.Do(context.Background(), pprof.Labels("goroutine", driverName), func(context.Context) {
		driver.run(outcome)
	})
	return sharedEngine, outcome
}

// run drives the link until it ends, then hangs up, and completes outcome
// with the hang-up's result. Should a panic get this far — from the
// connections aborted as the hang-up starts, say, or from the hang-up
// itself, a transport bug — the link still reports itself closed, with the
// call perhaps still up, and a call the panic kept from being hung up is
// hung up then (see endCallLeftUp). The panic goes no further, as a panic
// that leaves a goroutine ends the process.
func (d *driver) run(outcome *driverOutcome) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		// Hang up first: a link reported closed has ended its call and
		// released the handset, as Watcher.WaitClosed promises.
		d.endCallLeftUp()
		d.abandon(panicMessage(recovered))
		outcome.complete(&Error{
			Kind:             ErrLinkDown,
			Reason:           CloseReason{Kind: CloseReasonInternal, Detail: panickedDriverDetail},
			CallMayStillBeUp: true,
		})
	}()
	reason := d.runUntilClosed()
	outcome.complete(hangUpError(d.hangUp(reason)))
}

// endCallLeftUp hangs up a call that a panic kept from being hung up.
// Unwinding the driver would drop the data call and with it the transport,
// whose DTR drop ends the call; the transport stays with the app here, so
// only hanging up ends it — otherwise the call would stay up
// and billing, and the handset claimed for good. Once its hang-up has run,
// even one that panicked, a call only answers that it is over, without a
// word to the handset: this is harmless then. A hang-up that panicked
// pulsed DTR as the panic left it, which ends the call after AT&D2, but
// nothing confirmed that, so the link reports the call as perhaps still
// up, for the app to make sure. It runs while a panic is recovered, where
// a second one would end the process: one from this hang-up is dropped.
func (d *driver) endCallLeftUp() {
	defer func() {
		// The link reports that the call may still be up whatever this
		// hang-up did.
		_ = recover()
	}()
	// Its error says no more than that report: the call may still be up.
	_ = d.call.HangUp()
}

// abandon reports the link closed once a panic got past the driver's run,
// with the usage until now, so that the airtime counts the hang-up that
// endCallLeftUp ran, as it counts every hang-up. It runs while that panic is
// recovered, where a second one would end the process: one from the stack,
// aborting the connections once the link is reported closed, is dropped.
func (d *driver) abandon(message string) {
	defer func() {
		// The link is reported closed by then; nothing is left to undo.
		_ = recover()
	}()
	d.shared.update(func(e *engine) {
		// The usage first, so that it is kept even should aborting the
		// connections panic.
		e.usage = d.usage(time.Now())
		e.abandon(message)
	})
}

// runUntilClosed pumps until the link must end, and returns why. A panic in
// the protocol code must still end the call, so it becomes a close reason
// like any other.
func (d *driver) runUntilClosed() (reason CloseReason) {
	defer func() {
		if recovered := recover(); recovered != nil {
			reason = CloseReason{Kind: CloseReasonInternal, Detail: panicMessage(recovered)}
		}
	}()
	d.ppp.Open(time.Now())
	for {
		if reason, closing := d.pump(); closing {
			return reason
		}
	}
}

// pump runs one pass, moving bytes between the handset, PPP and the IP
// stack, and returns why the link must end, if it must.
func (d *driver) pump() (CloseReason, bool) {
	if err := d.writeOutput(); err != nil {
		return transportFailure(err), true
	}
	received, err := d.call.ReadTimeout(d.readBuffer, pollInterval)
	if err != nil {
		return transportFailure(err), true
	}
	now := time.Now()
	d.ppp.HandleInput(d.readBuffer[:received], now)
	if due, ok := d.ppp.NextTimeout(); ok && !due.After(now) {
		d.ppp.HandleTimeout(now)
	}
	reason, closing := d.updateEngine(now)
	if err := d.writeOutput(); err != nil {
		return transportFailure(err), true
	}
	return reason, closing
}

// transportFailure is the reason a link ends when talking to the handset
// failed with err.
func transportFailure(err error) CloseReason {
	return CloseReason{Kind: CloseReasonTransport, Detail: err.Error()}
}

// updateEngine takes PPP's news to the engine and the engine's datagrams to
// PPP, at now, and returns why the link must end, if it must.
func (d *driver) updateEngine(now time.Time) (reason CloseReason, closing bool) {
	d.shared.update(func(e *engine) {
		reason, closing = d.handlePPPEvents(e, now)
		if !closing {
			reason, closing = d.reasonToClose(e, now)
		}
		d.exchangeDatagrams(e, now)
		e.usage = d.usage(now)
	})
	return reason, closing
}

// handlePPPEvents takes in what PPP reports, and returns why the link must
// end, if PPP says it must.
func (d *driver) handlePPPEvents(e *engine, now time.Time) (CloseReason, bool) {
	for event, ok := d.ppp.PollEvent(); ok; event, ok = d.ppp.PollEvent() {
		switch event.Kind {
		case ppp.EventUp:
			if reason, failed := d.ipv4Up(e, event.IPv4, now); failed {
				return reason, true
			}
		case ppp.EventDown:
			return CloseReason{Kind: CloseReasonPPP, PPP: event.Reason}, true
		}
	}
	return CloseReason{}, false
}

// ipv4Up adopts the addresses IPCP agreed on, and reports a failure when the
// stack cannot use them.
func (d *driver) ipv4Up(e *engine, settings IPv4Settings, now time.Time) (CloseReason, bool) {
	logInfo("IPv4 up: " + debugtext.IPv4Settings(settings.LocalAddress, settings.PeerAddress, settings.DNSServers))
	mtu := min(d.tuning.MTU, d.ppp.MTU())
	if err := e.linkUp(settings, mtu, now); err != nil {
		reason := DownReason{Kind: ppp.DownReasonNegotiationFailed, Protocol: ipcpProtocol, Detail: err.Error()}
		return CloseReason{Kind: CloseReasonPPP, PPP: reason}, true
	}
	d.negotiationDeadline = time.Time{}
	return CloseReason{}, false
}

// reasonToClose returns why the link must end, at now, if it must: the call
// dropped, a limit was reached, the app asked to close and what it wrote
// has been delivered, or the call setup was cancelled.
func (d *driver) reasonToClose(e *engine, now time.Time) (CloseReason, bool) {
	if d.call.CarrierLost() {
		return CloseReason{Kind: CloseReasonCarrierLost}, true
	}
	if kind, reached := d.limitReached(e, now); reached {
		return CloseReason{Kind: kind}, true
	}
	if d.requestedCloseIsDue(e, now) || d.setupCancelled(e) {
		return CloseReason{Kind: CloseReasonRequested}, true
	}
	return CloseReason{}, false
}

// setupCancelled reports whether the call setup was cancelled before the
// link came up: nothing has been written that could still need delivering.
func (d *driver) setupCancelled(e *engine) bool {
	return e.isNegotiating() && d.cancellation.Cancelled()
}

// limitReached returns the limit reached at now, if any, checked in order:
// the negotiation timeout, the call time, the data volume, the idle time.
func (d *driver) limitReached(e *engine, now time.Time) (CloseReasonKind, bool) {
	usage := d.usage(now)
	switch {
	case d.negotiationTimedOut(now):
		return CloseReasonNegotiationTimedOut, true
	case d.callTimeLimitReached(usage):
		return CloseReasonCallTimeLimit, true
	case d.dataLimitReached(usage):
		return CloseReasonDataLimit, true
	case d.idleTimeoutReached(e, now):
		return CloseReasonIdleTimeout, true
	default:
		return 0, false
	}
}

// negotiationTimedOut reports whether PPP is still not up at its deadline.
func (d *driver) negotiationTimedOut(now time.Time) bool {
	return !d.negotiationDeadline.IsZero() && !now.Before(d.negotiationDeadline)
}

// callTimeLimitReached reports whether the hang-up must start for the call
// to end within its time limit.
func (d *driver) callTimeLimitReached(usage Usage) bool {
	limit := d.limits.MaxCallDuration
	return limit != 0 && usage.ConnectedFor >= max(limit-d.hangUpTime, 0)
}

// dataLimitReached reports whether the data volume is used up.
func (d *driver) dataLimitReached(usage Usage) bool {
	limit := d.limits.MaxBytes
	return limit != 0 && usage.BytesSent+usage.BytesReceived >= limit
}

// idleTimeoutReached reports whether the link has carried no application
// traffic for too long. Only an up link idles: negotiating is not idling.
func (d *driver) idleTimeoutReached(e *engine, now time.Time) bool {
	limit := d.limits.IdleTimeout
	return e.isUp() && limit != 0 && e.idleFor(now) >= limit
}

// requestedCloseIsDue reports whether a requested close should hang up now.
// It first lets data already written reach the other end, for at most the
// close grace period.
func (d *driver) requestedCloseIsDue(e *engine, now time.Time) bool {
	if !e.shutdownRequested {
		return false
	}
	if d.drainDeadline.IsZero() {
		d.startDraining(e, now)
	}
	return !now.Before(d.drainDeadline) || d.isDrained(e, now)
}

// startDraining starts delivering what was written, within the close grace
// period — none while PPP still negotiates, as nothing can have been
// written. The deadline is never the zero time, as now is not.
func (d *driver) startDraining(e *engine, now time.Time) {
	grace := time.Duration(0)
	if e.isUp() {
		grace = d.tuning.CloseGrace
	}
	e.beginDraining()
	d.drainDeadline = now.Add(grace)
}

// isDrained reports whether everything written has been acknowledged, and
// the line has had time to send what it was given.
func (d *driver) isDrained(e *engine, now time.Time) bool {
	return !e.stack.hasUndeliveredData() && d.pacer.isReady(now)
}

// exchangeDatagrams hands received datagrams to the stack and outgoing ones
// to PPP. The stack may hand over a datagram only when the line can take it
// at once, so what goes out is decided at the last moment.
func (d *driver) exchangeDatagrams(e *engine, now time.Time) {
	for datagram, ok := d.ppp.ReceiveIPv4(); ok; datagram, ok = d.ppp.ReceiveIPv4() {
		e.stack.deliver(datagram)
	}
	e.stack.setLineReady(d.pacer.isReady(now))
	e.stack.poll(now)
	for datagram, ok := e.stack.nextOutgoing(); ok; datagram, ok = e.stack.nextOutgoing() {
		leavesLineAt := d.pacer.completionOf(len(datagram)+framingOverhead, now)
		if d.repeats.admit(datagram, now, leavesLineAt) {
			d.sendDatagram(datagram)
		}
	}
}

// sendDatagram frames datagram for the peer, or logs why it cannot.
func (d *driver) sendDatagram(datagram []byte) {
	err := d.ppp.SendIPv4(datagram)
	switch {
	case err == nil:
	case errors.Is(err, ppp.ErrTooLarge):
		// The stack's packets are sized to fit; this one never will.
		logWarn("datagram dropped: " + err.Error())
	default:
		// PPP is renegotiating; TCP will send it again.
		logDebug("datagram dropped: " + err.Error())
	}
}

// writeOutput writes what PPP has to send to the handset.
func (d *driver) writeOutput() error {
	output := d.ppp.TakeOutput()
	// After NO CARRIER the handset is in command mode and would read these
	// bytes as commands.
	if len(output) == 0 || d.call.CarrierLost() {
		return nil
	}
	if err := d.call.WriteAll(output); err != nil {
		return err
	}
	d.pacer.sent(len(output), time.Now())
	return nil
}

// usage returns the airtime and traffic at now.
func (d *driver) usage(now time.Time) Usage {
	stats := d.ppp.Stats()
	return Usage{
		ConnectedFor:  max(now.Sub(d.connectedAt), 0),
		BytesSent:     stats.BytesSent,
		BytesReceived: stats.BytesReceived,
	}
}

// hangUp ends the call, for reason, and returns the hang-up's error. There
// is no PPP goodbye first: the gateway notices the call ending, and a
// Terminate exchange would cost another round trip of airtime. Should what
// comes before the hang-up panic — the connections aborted as it starts
// log through ipstack, and a log handler can fail too — run still hangs up.
func (d *driver) hangUp(reason CloseReason) error {
	d.shared.update(func(e *engine) { e.beginClosing(reason) })
	traffic := d.usage(time.Now())

	// First of all, before even a log line of ours: a log handler can fail
	// too.
	err := d.call.HangUp()

	if err == nil {
		logInfo("link closed: " + reason.String())
	} else {
		logWarn(fmt.Sprintf("link closed (%v), but the hang-up was not confirmed, so the call may still be up: %v",
			reason, err))
	}
	d.shared.update(func(e *engine) {
		traffic.ConnectedFor = time.Since(d.connectedAt)
		e.usage = traffic
		e.finishClosing(reason, err != nil)
	})
	return err
}

// hangUpError is what Link.Close reports for the hang-up's error err: nil,
// or the handset's failure.
func hangUpError(err error) error {
	if err == nil {
		return nil
	}
	return handsetError(err)
}

// panicMessage returns what a recovered panic says: its text, its error's
// text, or that the driver panicked. It never panics itself, as it runs
// while a panic is recovered: an error whose Error method panics — a nil
// pointer, say — only says that the driver panicked.
func panicMessage(recovered any) (message string) {
	defer func() {
		if recover() != nil {
			message = undescribedPanicDetail
		}
	}()
	switch value := recovered.(type) {
	case string:
		return value
	case error:
		return value.Error()
	default:
		return undescribedPanicDetail
	}
}
