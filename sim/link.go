package sim

import (
	"math"
	"math/bits"
	"time"
)

// One direction of the satellite link: bytes arrive after a delay and,
// with a throughput cap, no faster than the line carries them.

const (
	// unlimitedRate is a line without a throughput cap.
	unlimitedRate = 0
	// slowestRate is what a configured rate that would never deliver
	// anything means: in Go a negative one, where Rust had Some(0).
	slowestRate = 1
	// nanosPerSecond converts between rates and durations.
	nanosPerSecond = uint64(time.Second)
)

// delayLine holds bytes on their way across one direction of the link.
type delayLine struct {
	delay time.Duration
	// rate is the throughput cap in bytes per second, or unlimitedRate.
	rate     int
	inFlight []chunk
	// busyUntil is when the last byte sent so far comes out of the line:
	// the zero time before anything was sent, which is earlier than every
	// arrival.
	busyUntil time.Time
}

// chunk is bytes sent together, delivered from start on.
type chunk struct {
	start     time.Time
	bytes     []byte
	delivered int
}

// newDelayLine returns an empty line with the latency and throughput of
// profile.
func newDelayLine(profile LinkProfile) *delayLine {
	return &delayLine{delay: profile.OneWayDelay, rate: lineRate(profile.BytesPerSecond)}
}

// lineRate returns the rate a line carries for a configured cap: zero is
// unlimited, and a negative cap would never deliver anything, so it means
// the slowest rate.
func lineRate(bytesPerSecond int) int {
	if bytesPerSecond < 0 {
		return slowestRate
	}
	return bytesPerSecond
}

// send puts data on the line at now. It starts to come out one delay
// later, or when the bytes ahead of it are through, whichever is later;
// with a throughput cap each byte then takes 1/rate seconds.
func (l *delayLine) send(data []byte, now time.Time) {
	if len(data) == 0 {
		return
	}
	arrival := later(now, l.delay)
	start := arrival
	if l.busyUntil.After(arrival) {
		start = l.busyUntil
	}
	l.busyUntil = later(start, l.transmissionTime(len(data)))
	l.inFlight = append(l.inFlight, chunk{start: start, bytes: append([]byte(nil), data...)})
}

// receive returns everything that has come out of the line by now.
func (l *delayLine) receive(now time.Time) []byte {
	var received []byte
	for len(l.inFlight) > 0 {
		front := &l.inFlight[0]
		due := front.bytesDue(now, l.rate)
		received = append(received, front.bytes[front.delivered:due]...)
		front.delivered = due
		if due < len(front.bytes) {
			break
		}
		l.inFlight[0] = chunk{} // Let the delivered bytes go.
		l.inFlight = l.inFlight[1:]
	}
	return received
}

// transmissionTime returns how long length bytes occupy the line, rounded
// up so that all of them are due once it has passed.
func (l *delayLine) transmissionTime(length int) time.Duration {
	if l.rate == unlimitedRate {
		return 0
	}
	return time.Duration(mulDivCeil(uint64(length), nanosPerSecond, uint64(l.rate)))
}

// bytesDue returns how many of the chunk's bytes have come out of the line
// by now; never fewer than were already delivered.
func (c *chunk) bytesDue(now time.Time, rate int) int {
	if now.Before(c.start) {
		return c.delivered
	}
	carried := len(c.bytes)
	if rate != unlimitedRate {
		carried = min(carried, bytesCarried(rate, now.Sub(c.start)))
	}
	return max(carried, c.delivered)
}

// bytesCarried returns the whole bytes a line of rate bytes per second
// carries in elapsed, saturating at the largest int, as the Rust version's
// conversion to usize does: on a 32-bit platform an int holds less than
// the count, which would otherwise wrap around.
func bytesCarried(rate int, elapsed time.Duration) int {
	return int(min(mulDiv(uint64(elapsed), uint64(rate), nanosPerSecond), math.MaxInt))
}

// mulDiv returns a×b/divisor rounded down, computed in 128 bits as the
// Rust version's u128 arithmetic is, and saturating at math.MaxInt64.
func mulDiv(a, b, divisor uint64) uint64 {
	quotient, _, fits := divideProduct(a, b, divisor)
	if !fits {
		return math.MaxInt64
	}
	return quotient
}

// mulDivCeil is mulDiv rounded up.
func mulDivCeil(a, b, divisor uint64) uint64 {
	quotient, remainder, fits := divideProduct(a, b, divisor)
	if !fits {
		return math.MaxInt64
	}
	if remainder != 0 {
		quotient++
	}
	return min(quotient, math.MaxInt64)
}

// divideProduct divides the 128-bit product a×b by divisor, and reports
// whether the quotient fits an int64.
func divideProduct(a, b, divisor uint64) (quotient, remainder uint64, fits bool) {
	high, low := bits.Mul64(a, b)
	if high >= divisor {
		return 0, 0, false
	}
	quotient, remainder = bits.Div64(high, low, divisor)
	return quotient, remainder, quotient <= math.MaxInt64
}
