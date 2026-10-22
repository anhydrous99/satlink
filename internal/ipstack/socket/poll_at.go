package socket

import (
	"fmt"
	"time"
)

// pollKind orders the three answers a socket can give: polling now comes
// before polling at any time, which comes before waiting for ingress.
type pollKind int

const (
	pollNow pollKind = iota
	pollAtTime
	pollOnIngress
)

// PollAt says when a socket next needs the interface to poll it: now, at a
// given time, or only once something arrives for it.
//
// The zero PollAt means now. PollAt values compare with == when their times
// do; Equal compares the times with time.Time.Equal.
type PollAt struct {
	kind pollKind
	at   time.Time
}

// PollNow says the socket has something to do right away.
func PollNow() PollAt {
	return PollAt{kind: pollNow}
}

// PollAtTime says the socket has something to do at t, such as a timer
// expiring.
func PollAtTime(t time.Time) PollAt {
	return PollAt{kind: pollAtTime, at: t}
}

// PollOnIngress says the socket has nothing to do until a packet arrives for
// it or its owner uses it.
func PollOnIngress() PollAt {
	return PollAt{kind: pollOnIngress}
}

// IsNow reports whether the socket wants to be polled right away.
func (p PollAt) IsNow() bool {
	return p.kind == pollNow
}

// Time returns when the socket wants to be polled, and false unless p came
// from PollAtTime.
func (p PollAt) Time() (time.Time, bool) {
	return p.at, p.kind == pollAtTime
}

// IsOnIngress reports whether the socket waits for something to arrive.
func (p PollAt) IsOnIngress() bool {
	return p.kind == pollOnIngress
}

// Deadline returns when to poll given the current time: now itself for
// PollNow, the time for PollAtTime, and false for PollOnIngress, which sets
// no deadline.
func (p PollAt) Deadline(now time.Time) (time.Time, bool) {
	switch p.kind {
	case pollNow:
		return now, true
	case pollAtTime:
		return p.at, true
	default:
		return time.Time{}, false
	}
}

// String names the answer with the variant names of smoltcp's derived
// Debug: "Now", "Ingress", or "Time(…)" with the time as time.Time prints it
// (smoltcp's Debug prints the Instant's microseconds instead).
func (p PollAt) String() string {
	switch p.kind {
	case pollNow:
		return "Now"
	case pollAtTime:
		return fmt.Sprintf("Time(%v)", p.at)
	default:
		return "Ingress"
	}
}

// Equal reports whether p and other give the same answer, comparing times
// with time.Time.Equal.
func (p PollAt) Equal(other PollAt) bool {
	return p.kind == other.kind && p.at.Equal(other.at)
}

// Earliest returns the most urgent of polls: PollNow if any says now, else
// the earliest PollAtTime, else PollOnIngress (also when polls is empty).
func Earliest(polls ...PollAt) PollAt {
	earliest := PollOnIngress()
	for _, poll := range polls {
		if poll.isBefore(earliest) {
			earliest = poll
		}
	}
	return earliest
}

func (p PollAt) isBefore(other PollAt) bool {
	if p.kind != other.kind {
		return p.kind < other.kind
	}
	return p.kind == pollAtTime && p.at.Before(other.at)
}
