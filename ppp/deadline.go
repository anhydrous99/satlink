package ppp

import (
	"math"
	"time"
)

// The deadline arithmetic the protocol timers share. Each timer keeps its
// deadline as a time.Time, the zero Time meaning that the timer is not
// running (Rust's None).

// isDue reports whether deadline has come at now. The zero deadline never
// comes.
func isDue(deadline, now time.Time) bool {
	return !deadline.IsZero() && !now.Before(deadline)
}

// earliest returns the earliest of the deadlines that are set, and the zero
// Time when none is.
func earliest(deadlines ...time.Time) time.Time {
	var first time.Time
	for _, deadline := range deadlines {
		if !deadline.IsZero() && (first.IsZero() || deadline.Before(first)) {
			first = deadline
		}
	}
	return first
}

// scaled returns duration × count, saturating at the longest Duration where
// the product would overflow: a negative product would put a deadline in
// the past. (Rust's Duration, with its far larger range, panics instead.)
func scaled(duration time.Duration, count int) time.Duration {
	if count > 0 && duration > time.Duration(math.MaxInt64)/time.Duration(count) {
		return time.Duration(math.MaxInt64)
	}
	return duration * time.Duration(count)
}
