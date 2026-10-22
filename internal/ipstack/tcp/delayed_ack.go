package tcp

import (
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
)

// ackDelayTimerKind is what the delayed-ACK timer is doing.
type ackDelayTimerKind int

const (
	// ackDelayIdle delays nothing: an ACK may go at once.
	ackDelayIdle ackDelayTimerKind = iota
	// ackDelayWaiting holds an ACK back until the timer's expiresAt.
	ackDelayWaiting
	// ackDelayImmediate was holding an ACK back, but enough data has arrived
	// since that the ACK must go at once.
	ackDelayImmediate
)

// ackDelayTimer holds back segments that would carry only an ACK or a window
// update, hoping the ACK can ride on outgoing data instead: smoltcp's
// AckDelayTimer. The zero ackDelayTimer is idle.
type ackDelayTimer struct {
	kind ackDelayTimerKind
	// expiresAt is when a waiting timer lets its ACK go.
	expiresAt time.Time
}

// expired reports whether an ACK may go at now.
func (t ackDelayTimer) expired(now time.Time) bool {
	return t.kind != ackDelayWaiting || !t.expiresAt.After(now)
}

// pollAt returns when an ACK waiting for this timer may go.
func (t ackDelayTimer) pollAt() socket.PollAt {
	if t.kind == ackDelayWaiting {
		return socket.PollAtTime(t.expiresAt)
	}
	return socket.PollNow()
}

// delayedACKPollAt returns when a delayed ACK may go, if one waits.
func (s *Socket) delayedACKPollAt() socket.PollAt {
	if !s.ackToTransmit() {
		return socket.PollOnIngress()
	}
	return s.ackDelayTimer.pollAt()
}

// immediateACKToTransmit reports whether to send an ACK immediately due to
// the amount of unacknowledged data.
//
// RFC 9293 states "An ACK SHOULD be generated for at least every second
// full-sized segment or 2*RMSS bytes of new data (where RMSS is the MSS
// specified by the TCP endpoint receiving the segments to be acknowledged,
// or the default value if not specified) (SHLD-19)."
//
// Note that the RFC above only says "at least 2*RMSS bytes", which is not a
// hard requirement. In practice, we follow the Linux kernel's empirical value
// of sending an ACK for every RMSS byte of new data. For details, see
// https://elixir.bootlin.com/linux/v6.11.4/source/net/ipv4/tcp_input.c#L5747.
func (s *Socket) immediateACKToTransmit() bool {
	lastAck, ok := s.remoteLastAck.Get()
	return ok && lastAck.Add(s.remoteMSS).Compare(s.nextExpectedSeq()) < 0
}

// delayACK starts holding back the ACK of the data just received, or makes it
// go at once when too much data waits to be acknowledged.
func (s *Socket) delayACK(now time.Time) {
	switch s.ackDelayTimer.kind {
	case ackDelayIdle:
		netlog.Trace("starting delayed ack timer")
		s.ackDelayTimer = ackDelayTimer{kind: ackDelayWaiting, expiresAt: now.Add(s.ackDelay)}
	case ackDelayWaiting:
		if s.immediateACKToTransmit() {
			netlog.Trace("delayed ack timer already started, forcing expiry")
			s.ackDelayTimer = ackDelayTimer{kind: ackDelayImmediate}
			return
		}
		netlog.Trace("waiting until delayed ack timer expires")
	case ackDelayImmediate:
		netlog.Trace("delayed ack timer already force-expired")
	}
}

// stopDelayedACK resets the delayed-ACK timer once a segment, which carries
// an ACK, has been sent.
func (s *Socket) stopDelayedACK() {
	switch s.ackDelayTimer.kind {
	case ackDelayWaiting:
		netlog.Trace("stop delayed ack timer")
	case ackDelayImmediate:
		netlog.Trace("stop delayed ack timer (was force-expired)")
	}
	s.ackDelayTimer = ackDelayTimer{}
}
