package mobile

import (
	"math"
	"time"
)

// Conversions between the app's int64 milliseconds, seconds and counts and
// Go's durations and unsigned counts. They saturate instead of overflowing,
// so that an absurd value means "as long as possible" (or, for settings,
// meets the configuration's "at most a year" check) rather than wrapping
// around into a negative one.

// shortestTransportWaitMillis is the shortest wait a ByteTransport's Read
// is asked for: UsbDeviceConnection.bulkTransfer takes a timeout of zero as
// "wait forever", so even a read of only what has arrived waits a
// millisecond.
const shortestTransportWaitMillis = 1

// longestTransportWaitMillis is the longest wait a ByteTransport's Read is
// asked for: the largest Java int, the type of bulkTransfer's timeout.
const longestTransportWaitMillis = math.MaxInt32

// timeoutFromMillis returns the duration of an app's timeout. A negative
// timeout counts as zero, and one too long for a time.Duration saturates at
// the longest, which means no deadline.
func timeoutFromMillis(millis int64) time.Duration {
	return saturatingDuration(max(millis, 0), time.Millisecond)
}

// deadlineAfterMillis returns the deadline timeoutMillis from now; a
// timeout of zero or less is a deadline of now, which takes only what has
// already arrived.
func deadlineAfterMillis(timeoutMillis int64) time.Time {
	return time.Now().Add(timeoutFromMillis(timeoutMillis))
}

// durationFromSeconds returns the duration of a setting's seconds,
// saturating at the longest and shortest durations, so that an absurd value
// is still refused as too long, or as negative, when the link is
// established.
func durationFromSeconds(seconds int64) time.Duration {
	return saturatingDuration(seconds, time.Second)
}

// saturatingDuration returns count units, or the longest or shortest
// duration where that would overflow.
func saturatingDuration(count int64, unit time.Duration) time.Duration {
	switch {
	case count > int64(math.MaxInt64/unit):
		return math.MaxInt64
	case count < int64(math.MinInt64/unit):
		return math.MinInt64
	default:
		return time.Duration(count) * unit
	}
}

// transportWaitMillis returns the milliseconds a ByteTransport's Read may
// wait for timeout: whole milliseconds, at least
// shortestTransportWaitMillis and at most longestTransportWaitMillis.
func transportWaitMillis(timeout time.Duration) int64 {
	return min(max(timeout.Milliseconds(), shortestTransportWaitMillis), longestTransportWaitMillis)
}

// wholeSeconds returns duration in whole seconds, as the app's settings
// and link status count time, dropping any fraction.
func wholeSeconds(duration time.Duration) int64 {
	return int64(duration / time.Second)
}

// countFromUnsigned returns an unsigned count as an int64, which gomobile
// can bind, saturating at the largest int64.
func countFromUnsigned(count uint64) int64 {
	return int64(min(count, math.MaxInt64))
}
