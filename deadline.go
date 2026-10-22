package satlink

import "time"

// Deadlines for blocking waits on the handset. A timeout too long to add to
// the clock — math.MaxInt64, say — means no deadline, never an overflow.

// longestRead is the longest single read while waiting, so the deadline is
// looked at again and a transport is never handed an absurd timeout.
const longestRead = time.Second

// deadlineAfter returns when a wait of timeout that starts now ends.
//
// time.Time.Add cannot overflow: for the longest timeout, math.MaxInt64, it
// drops the monotonic reading and gives a wall-clock time some 292 years
// ahead, which is no deadline in effect. A timeout
// of zero or less gives a deadline that has already passed.
func deadlineAfter(timeout time.Duration) time.Time {
	return time.Now().Add(timeout)
}

// timeToWait returns how long to read before looking at deadline again, at
// most longestRead, and false once the deadline has passed.
func timeToWait(deadline time.Time) (time.Duration, bool) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, false
	}
	return min(remaining, longestRead), true
}
