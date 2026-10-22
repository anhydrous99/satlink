package satnet

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/netip"
	"runtime"
	"time"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/ppp"
)

// An IP link that exists only inside this app.

// receiveACCM holds the control characters the gateway must escape: PPP
// asks it to escape CR, so that the handset's own "\r\nNO CARRIER\r\n" can
// never appear inside a frame and is always recognised; XON and XOFF are
// escaped in case software flow control is on. Escaping three of 256 byte
// values costs about 1% on encrypted data.
const receiveACCM uint32 = 1<<'\r' | 1<<xon | 1<<xoff

// xon and xoff are the software flow control characters.
const (
	xon  = 0x11
	xoff = 0x13
)

// Link is an IP link over an Iridium data call, visible only to this app.
//
// The operating system never learns the link exists: PPP and TCP/IP run in
// this process, so there is no network interface, route or DNS entry that
// another program could use. Every destination must be allowed by the
// link's [Policy], and [Limits] hang up on idle links and cap airtime and
// data.
//
// Closing first lets data already written reach the other end, for at most
// [Tuning.CloseGrace], then hangs up. [Link.Close] waits for that;
// [Link.RequestClose] does not. A Link that is garbage collected without
// Close still hangs up, in the background. Its [Watcher]s and connections
// do not keep it reachable: they refer to the link's engine only, and once
// a collected Link has hung up they report ErrLinkDown with reason
// CloseReasonRequested.
//
// A Link is safe for use by several goroutines. The zero Link is not
// usable; create one with [Establish].
type Link struct {
	shared *shared
	policy Policy
	// driver is the hang-up's outcome, once the driver has finished.
	driver *driverOutcome
}

// Establish dials handset, brings up PPP, and returns once IP traffic can
// flow. It blocks for the whole call setup — typically half a minute over
// Iridium — unless config.Call.Cancellation stops it, from another
// goroutine: it then fails with ErrHandset wrapping an *iridium.Error of
// kind iridium.ErrCancelled, whether the call had connected or not — a
// dial still in progress is aborted, and a call that came up is hung up.
// Once the link is up, close it instead. A context can drive the
// cancellation:
//
//	stop := context.AfterFunc(ctx, config.Call.Cancellation.Cancel)
//	link, err := satnet.Establish(handset, config)
//	stop()
//
// config is checked before dialling, as [Config.Validate] does, so a
// configuration that cannot work never costs airtime; the link keeps its
// own copy of it. From the dial on, handset is busy: its own methods fail
// with iridium.ErrBusy until the link has hung up.
//
// On failure handset is released. The call has been hung up and the
// handset is in command mode, unless that hang-up failed too: the error
// then still says why the link did not come up, and its CallMayStillBeUp is set — errors.Is(err,
// iridium.ErrCallMayStillBeUp) — as the call may still be up and billing;
// the handset's EndDataCall retries once the handset answers again, and
// power-cycling it is the last resort. The error is an [*Error]:
// ErrInvalidConfig for a configuration that cannot work, ErrHandset when
// the dial fails (wrapping the *iridium.Error, for example ErrBusy or a
// refused number, and carrying its CallMayStillBeUp), and ErrLinkDown when
// the call came up but the link did not — PPP failed or timed out, the call
// dropped — with Reason saying why.
func Establish(handset *iridium.Handset, config Config) (*Link, error) {
	// The map is the caller's still: the copy is taken before it is checked
	// and before the dial, during which the caller may change it.
	config.Hosts = maps.Clone(config.Hosts)
	endpoint, err := prepare(&config)
	if err != nil {
		return nil, err
	}
	// startDriver below runs after the dial; a goroutine cannot fail to
	// start, so this order costs no airtime.
	call, err := handset.DialData(config.Number, config.Call)
	if err != nil {
		return nil, handsetError(err)
	}
	sharedEngine, outcome := startDriver(call, endpoint, &config)
	if err := awaitUp(sharedEngine); err != nil {
		return nil, failedToComeUp(setupFailure(err, config.Call.Cancellation), outcome)
	}
	link := &Link{shared: sharedEngine, policy: config.Policy, driver: outcome}
	// A Link dropped without Close still hangs up: the cleanup holds the
	// engine, never the Link.
	runtime.AddCleanup(link, (*shared).requestClose, sharedEngine)
	return link, nil
}

// prepare checks the configuration and builds the PPP endpoint, before any
// airtime is spent.
func prepare(config *Config) (*ppp.Endpoint, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	endpoint, err := ppp.NewEndpoint(pppClientConfig(config))
	if err != nil {
		return nil, &Error{Kind: ErrInvalidConfig, Err: err}
	}
	return endpoint, nil
}

// pppClientConfig returns the configuration of the link's PPP endpoint, a
// client with config's credentials, which asks the gateway to escape
// receiveACCM and takes packets of up to the MTU.
func pppClientConfig(config *Config) ppp.Config {
	pppConfig := ppp.NewConfig(ppp.ClientRole(config.Credentials))
	pppConfig.ReceiveACCM = receiveACCM
	// Valid configurations keep the MTU within 296 to 1500 bytes.
	pppConfig.MRU = uint16(config.Tuning.MTU)
	return pppConfig
}

// awaitUp waits until IP traffic can flow, and fails with ErrLinkDown when
// the link goes down first. The driver's negotiation timeout bounds the
// wait.
func awaitUp(sharedEngine *shared) error {
	return sharedEngine.waitUntil(waitTerms{operation: operationLinkUp, until: fixedDeadline(time.Time{})},
		func(e *engine) (bool, error) { return e.isUp(), nil })
}

// setupFailure is the error of a link that did not come up for err: the
// handset's ErrCancelled once cancellation was cancelled — hung up for it,
// with the same error as when cancelled before CONNECT — and otherwise err.
func setupFailure(err error, cancellation *iridium.Cancellation) error {
	if cancellation.Cancelled() {
		return handsetError(&iridium.Error{Kind: iridium.ErrCancelled})
	}
	return err
}

// failedToComeUp waits for the driver to hang up the call of a link that did
// not come up, so that the handset is released, and returns err, why the
// link did not come up: with CallMayStillBeUp set if the hang-up failed too,
// as the caller must hear of it.
func failedToComeUp(err error, outcome *driverOutcome) error {
	hangUpErr := outcome.wait()
	var linkErr *Error
	if errors.Is(hangUpErr, iridium.ErrCallMayStillBeUp) && errors.As(err, &linkErr) {
		linkErr.CallMayStillBeUp = true
	}
	return err
}

// Status returns the link's state, addresses and usage right now.
func (l *Link) Status() Status {
	return l.shared.status()
}

// Watcher returns a handle for following the link's state from another
// goroutine.
func (l *Link) Watcher() *Watcher {
	return &Watcher{shared: l.shared}
}

// RequestClose starts closing without waiting; [Watcher.WaitClosed] tells
// when the call has ended. Nothing new can be sent from now on — lookups,
// new connections and writes fail with ErrLinkDown — but what was already
// written is still delivered within [Tuning.CloseGrace], and reads and
// waits for acknowledgement carry on meanwhile, until the hang-up starts.
func (l *Link) RequestClose() {
	l.shared.requestClose()
}

// Resolve returns the IPv4 addresses of host, finishing within timeout.
//
// An IPv4 literal is returned as it is. A name must be allowed by the
// policy for some port ([Policy.AllowsLookup]); it is answered from the
// configured hosts or the DNS cache when possible, and otherwise asked of
// the DNS servers PPP announced, in turn, which costs a satellite round
// trip.
//
// It fails with ErrInvalidHost, ErrPolicyDenied (Destination is the name),
// ErrLinkDown, ErrNoDNSServer, ErrNameNotFound only when a DNS server said
// the name has no IPv4 address, or ErrTimedOut for operation "DNS lookup":
// with no answer in time — each of up to two servers gets 10 s — or only a
// server failure, which is worth retrying.
func (l *Link) Resolve(host string, timeout time.Duration) ([]netip.Addr, error) {
	return l.resolve(context.Background(), host, deadlineAfter(timeout))
}

// Dial opens a TCP connection to host, a name or an IPv4 address, on port,
// finishing within timeout.
//
// host:port must be allowed by the policy, otherwise it fails with
// ErrPolicyDenied whose Destination is "host:port". A name is resolved as
// [Link.Resolve] does, and its addresses are tried in turn until one
// connects; a timeout or the link going down ends the attempts at once.
//
// Besides Resolve's errors, it fails with ErrConnectionFailed when every
// address refused (Addr is the last one tried), ErrTimedOut for operation
// "TCP connect", and ErrLinkDown when the link went down meanwhile.
func (l *Link) Dial(host string, port uint16, timeout time.Duration) (*TCPConn, error) {
	return l.dial(context.Background(), host, port, deadlineAfter(timeout))
}

// DialUDP opens a UDP socket that exchanges datagrams with host on port
// only. host:port must be allowed by the policy, as for [Link.Dial]; a name
// is resolved within timeout and its first address used.
func (l *Link) DialUDP(host string, port uint16, timeout time.Duration) (*UDPConn, error) {
	return l.dialUDP(context.Background(), host, port, deadlineAfter(timeout))
}

// DialContext connects to address on the named network, for net/http and
// other code written for net.Dialer: networks "tcp" and "tcp4" give a
// [*TCPConn], "udp" and "udp4" a [*UDPConn]. address is "host:port", and
// the policy applies as for [Link.Dial].
//
// The dial finishes by ctx's deadline, if it has one, and is abandoned when
// ctx is done. Then, as with net.Dialer, the error is
// &net.OpError{Op: "dial", Net: network, Err: ctx.Err()}, so that
// errors.Is(err, context.DeadlineExceeded) or context.Canceled holds,
// rather than the ErrTimedOut of the operation that was waiting. Other
// networks fail with a net.UnknownNetworkError, and a malformed address
// with a *net.AddrError; everything else is an [*Error] as for
// [Link.Dial] and [Link.DialUDP].
//
// For HTTPS through net/http, set the transport's TLSClientConfig to
// [TLSConnector.Config], so that its session cache and trust settings
// apply.
func (l *Link) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dial, ok := l.dialerFor(network)
	if !ok {
		return nil, net.UnknownNetworkError(network)
	}
	host, port, err := splitHostPort(address)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, &net.OpError{Op: "dial", Net: network, Err: err}
	}
	// Without a deadline, ctx gives the zero time, which means none.
	deadline, _ := ctx.Deadline()
	conn, err := dial(ctx, host, port, deadline)
	if err != nil {
		return nil, dialError(ctx, network, err)
	}
	return conn, nil
}

// Close lets data already written arrive (within [Tuning.CloseGrace]),
// hangs up, and waits for the hang-up. The handset is then released, in
// command mode.
//
// If the hang-up cannot be confirmed (neither ATH after the escape nor a
// DTR drop was confirmed), the call may still be up and billing:
// Close returns an [*Error] of kind ErrHandset wrapping the handset's
// failure, with CallMayStillBeUp set — errors.Is(err,
// iridium.ErrCallMayStillBeUp) — and [Status].CallMayStillBeUp is true.
// The handset is released all the same: retry with its EndDataCall once it
// answers again, or power-cycle it. Should the hang-up itself panic (a
// faulty transport), the link still reports itself closed, and Close
// returns ErrLinkDown, with Reason CloseReasonInternal "the link's driver
// thread panicked" and CallMayStillBeUp set: the panic may have cut the
// hang-up short, so the call may still be up and billing, as after a
// hang-up that failed. (The hang-up pulses DTR as the panic leaves it,
// which ends the call after AT&D2 where the transport's DTR line reaches
// the handset; nothing confirms that it did.)
//
// A link that has already closed — by itself, or after
// [Link.RequestClose] — returns the hang-up's result at once. Close is
// idempotent: later calls return the first call's result.
func (l *Link) Close() error {
	l.RequestClose()
	return l.driver.wait()
}

// Watcher follows a link's state; it is usable from any goroutine. All the
// watchers of a link share one queue of state changes. A Watcher refers to
// the link's engine only, so holding one does not keep the [Link]
// reachable: a Link the app drops still hangs up, and the Watcher sees it
// close. The zero Watcher is not usable; [Link.Watcher] makes one.
type Watcher struct {
	shared *shared
}

// Status returns the link's state, addresses and usage right now.
func (w *Watcher) Status() Status {
	return w.shared.status()
}

// NextStateChange waits up to timeout for the link's state to change and
// returns the new state, and false if none came in time. Changes queue up
// until taken, oldest first; each is reported once, to whichever caller
// takes it. A timeout of zero or less only takes a change already queued.
func (w *Watcher) NextStateChange(timeout time.Duration) (LinkState, bool) {
	var change LinkState
	found := w.shared.waitFor(deadlineAfter(timeout), func(e *engine) bool {
		var taken bool
		change, taken = e.takeStateChange()
		return taken
	})
	return change, found
}

// WaitClosed waits up to timeout for the call to end and returns why it
// ended, and false if it is still going. Unlike connection operations, it
// keeps waiting while the link goes down: that is what is being watched.
func (w *Watcher) WaitClosed(timeout time.Duration) (CloseReason, bool) {
	var reason CloseReason
	found := w.shared.waitFor(deadlineAfter(timeout), func(e *engine) bool {
		if e.state.Kind != LinkStateClosed {
			return false
		}
		reason = e.state.Reason
		return true
	})
	return reason, found
}
