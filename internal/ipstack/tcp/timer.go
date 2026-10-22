package tcp

import (
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// closeDelay is how long a socket stays in TIME-WAIT, so that the peer can
// still have a lost final ACK repeated, before it closes. RFC 9293 asks for
// twice the maximum segment lifetime; this waits 10 s.
const closeDelay = 10 * time.Second

// timerKind is what the socket's one timer is timing.
type timerKind int

const (
	// timerIdle times nothing but, with keep-alive on, the next keep-alive.
	timerIdle timerKind = iota
	// timerRetransmit fires when the oldest unacknowledged data is due to be
	// sent again.
	timerRetransmit
	// timerFastRetransmit asks for the earliest unacknowledged segment to be
	// sent again at once, after three duplicate ACKs.
	timerFastRetransmit
	// timerZeroWindowProbe fires when a probe of the peer's zero window is
	// due.
	timerZeroWindowProbe
	// timerClose fires when TIME-WAIT is over.
	timerClose
)

// timer is the socket's retransmission timer, which also times keep-alives,
// zero-window probes and TIME-WAIT. The zero timer is idle,
// without keep-alive.
//
// Every change leaves the fields its kind does not use zero, so that timers
// compare with ==.
type timer struct {
	kind timerKind
	// keepAliveAt is when an idle timer sends the next keep-alive; absent
	// when keep-alive is off.
	keepAliveAt wire.Optional[time.Time]
	// expiresAt is when a retransmit, zero-window-probe or close timer fires.
	expiresAt time.Time
	// delay is the zero-window probe's current interval, which doubles with
	// every probe.
	delay time.Duration
}

// keepAliveDeadline returns when to send the next keep-alive after now, for
// a keep-alive interval of 0 (off) or more.
func keepAliveDeadline(now time.Time, interval time.Duration) wire.Optional[time.Time] {
	if interval <= 0 {
		return wire.Optional[time.Time]{}
	}
	return wire.Some(now.Add(interval))
}

func (t *timer) shouldKeepAlive(now time.Time) bool {
	keepAliveAt, armed := t.keepAliveAt.Get()
	return t.kind == timerIdle && armed && !now.Before(keepAliveAt)
}

func (t *timer) shouldRetransmit(now time.Time) bool {
	return t.kind == timerFastRetransmit || (t.kind == timerRetransmit && !now.Before(t.expiresAt))
}

func (t *timer) shouldClose(now time.Time) bool {
	return t.kind == timerClose && !now.Before(t.expiresAt)
}

func (t *timer) shouldZeroWindowProbe(now time.Time) bool {
	return t.kind == timerZeroWindowProbe && !now.Before(t.expiresAt)
}

// pollAt returns when the timer next needs the socket polled.
func (t *timer) pollAt() socket.PollAt {
	switch t.kind {
	case timerIdle:
		if keepAliveAt, armed := t.keepAliveAt.Get(); armed {
			return socket.PollAtTime(keepAliveAt)
		}
		return socket.PollOnIngress()
	case timerFastRetransmit:
		return socket.PollNow()
	default:
		return socket.PollAtTime(t.expiresAt)
	}
}

// setForIdle stops timing anything but the next keep-alive, if any.
func (t *timer) setForIdle(now time.Time, keepAlive time.Duration) {
	*t = timer{kind: timerIdle, keepAliveAt: keepAliveDeadline(now, keepAlive)}
}

// setKeepAlive makes an idle timer without a keep-alive send one at once:
// the epoch is a time already past.
func (t *timer) setKeepAlive() {
	if t.kind == timerIdle && !t.keepAliveAt.IsSome() {
		t.keepAliveAt = wire.Some(time.Time{})
	}
}

// rewindKeepAlive makes an idle timer send the next keep-alive a whole
// interval after now.
func (t *timer) rewindKeepAlive(now time.Time, keepAlive time.Duration) {
	if t.kind == timerIdle {
		t.keepAliveAt = keepAliveDeadline(now, keepAlive)
	}
}

// setForRetransmit (re)starts the retransmission timer, unless the socket is
// timing TIME-WAIT.
func (t *timer) setForRetransmit(now time.Time, delay time.Duration) {
	if t.kind != timerClose {
		*t = timer{kind: timerRetransmit, expiresAt: now.Add(delay)}
	}
}

func (t *timer) setForFastRetransmit() {
	*t = timer{kind: timerFastRetransmit}
}

func (t *timer) setForClose(now time.Time) {
	*t = timer{kind: timerClose, expiresAt: now.Add(closeDelay)}
}

func (t *timer) setForZeroWindowProbe(now time.Time, delay time.Duration) {
	*t = timer{kind: timerZeroWindowProbe, expiresAt: now.Add(delay), delay: delay}
}

// rewindZeroWindowProbe times the next zero-window probe after one was sent,
// backing off: the interval doubles, up to the largest RTO.
func (t *timer) rewindZeroWindowProbe(now time.Time) {
	if t.kind == timerZeroWindowProbe {
		t.setForZeroWindowProbe(now, min(t.delay*backoffFactor, maxRTO))
	}
}

func (t *timer) isIdle() bool {
	return t.kind == timerIdle
}

func (t *timer) isZeroWindowProbe() bool {
	return t.kind == timerZeroWindowProbe
}

func (t *timer) isRetransmit() bool {
	return t.kind == timerRetransmit || t.kind == timerFastRetransmit
}
