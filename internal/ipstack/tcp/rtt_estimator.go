package tcp

import (
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The retransmission timeout of RFC 6298, in milliseconds as the estimator
// computes it.
const (
	// initialRTOMillis is the RTO before any measurement. RFC 6298 (2.1):
	// Until a round-trip time (RTT) measurement has been made for a segment
	// sent between the sender and receiver, the sender SHOULD set
	// RTO <- 1 second.
	initialRTOMillis uint32 = 1000
	// minRTOMarginMillis is the minimum "safety margin" for the RTO that
	// kicks in when the variance gets very low.
	minRTOMarginMillis uint32 = 5
	// rttVarianceFactor is K, according to RFC 6298: how many times the RTT
	// variation the RTO allows for.
	rttVarianceFactor uint32 = 4
	// minRTOMillis is the smallest RTO. RFC 6298 (2.4): Whenever RTO is
	// computed, if it is less than 1 second, then the RTO SHOULD be rounded
	// up to 1 second.
	minRTOMillis uint32 = 1000
	// maxRTOMillis is the largest RTO. RFC 6298 (2.5): A maximum value MAY be
	// placed on RTO provided it is at least 60 seconds.
	maxRTOMillis uint32 = 60_000
)

// maxRTO is maxRTOMillis as a duration; it caps the zero-window probe's
// backoff too.
const maxRTO = time.Duration(maxRTOMillis) * time.Millisecond

// The gains of RFC 6298 (2.3), alpha = 1/8 and beta = 1/4, as the
// denominators of the weights the estimator gives a new measurement.
const (
	// smoothedRTTGainDenominator: SRTT <- (1 - alpha) * SRTT + alpha * R'.
	smoothedRTTGainDenominator uint32 = 8
	// rttVarianceGainDenominator: RTTVAR <- (1 - beta) * RTTVAR +
	// beta * |SRTT - R'|.
	rttVarianceGainDenominator uint32 = 4
)

// initialRTTVarianceDivisor sets the first variance from the first
// measurement. RFC 6298 (2.2): RTTVAR <- R/2.
const initialRTTVarianceDivisor uint32 = 2

// backoffFactor multiplies the RTO on every retransmission timeout. RFC 6298
// (5.5): The host MUST set RTO <- RTO * 2 ("back off the timer"). Zero-window
// probes back off the same way.
const backoffFactor = 2

// backoffsBeforeForgettingRTT is how many timeouts in a row make the
// estimator discard SRTT and RTTVAR. RFC 6298: a TCP implementation MAY clear
// SRTT and RTTVAR after backing off the timer multiple times as it is likely
// that the current SRTT and RTTVAR are bogus in this situation.
const backoffsBeforeForgettingRTT = 3

// rttEstimator computes the retransmission timeout from round-trip time
// measurements, as RFC 6298 specifies: smoltcp's RttEstimator. Times are
// whole milliseconds, as smoltcp keeps them.
type rttEstimator struct {
	// haveMeasurement is true if we have made at least one RTT measurement.
	haveMeasurement bool
	// srtt is the smoothed RTT.
	srtt uint32
	// rttvar is the RTT variance.
	rttvar uint32
	// rto is the retransmission timeout.
	rto uint32
	// pendingSample is the segment whose acknowledgement will be the next
	// RTT measurement.
	pendingSample wire.Optional[rttSample]
	// maxSeqSent is the end of the latest data sent.
	maxSeqSent wire.Optional[wire.TCPSeqNumber]
	// rtoCount counts the retransmission timeouts since the last
	// measurement.
	rtoCount uint8
}

// rttSample is a segment being timed: when it was sent, and the sequence
// number its acknowledgement will reach.
type rttSample struct {
	sentAt time.Time
	seq    wire.TCPSeqNumber
}

// newRTTEstimator returns an estimator without measurements: smoltcp's
// RttEstimator::default.
func newRTTEstimator() rttEstimator {
	return rttEstimator{rto: initialRTOMillis}
}

func (e *rttEstimator) retransmissionTimeout() time.Duration {
	return time.Duration(e.rto) * time.Millisecond
}

// sample takes one round-trip time measurement, in milliseconds.
func (e *rttEstimator) sample(newRTT uint32) {
	if e.haveMeasurement {
		// RFC 6298 (2.3) When a subsequent RTT measurement R' is made, a host
		// MUST set (...)
		difference := absoluteDifference(e.srtt, newRTT)
		e.rttvar = blend(e.rttvar, difference, rttVarianceGainDenominator)
		e.srtt = blend(e.srtt, newRTT, smoothedRTTGainDenominator)
	} else {
		// RFC 6298 (2.2) When the first RTT measurement R is made, the host
		// MUST set (...)
		e.haveMeasurement = true
		e.srtt = newRTT
		e.rttvar = newRTT / initialRTTVarianceDivisor
	}
	// RFC 6298 (2.2), (2.3)
	margin := max(minRTOMarginMillis, e.rttvar*rttVarianceFactor)
	e.rto = min(max(e.srtt+margin, minRTOMillis), maxRTOMillis)
	e.rtoCount = 0
	netlog.Trace("rtte: sample", "sample", newRTT, "srtt", e.srtt, "rttvar", e.rttvar, "rto", e.rto)
}

// onSend records that data up to seq was sent at now, and starts timing it
// unless a segment is being timed already.
func (e *rttEstimator) onSend(now time.Time, seq wire.TCPSeqNumber) {
	if maxSeqSent, ok := e.maxSeqSent.Get(); ok && seq.Compare(maxSeqSent) <= 0 {
		return
	}
	e.maxSeqSent = wire.Some(seq)
	if !e.pendingSample.IsSome() {
		e.pendingSample = wire.Some(rttSample{sentAt: now, seq: seq})
		netlog.Trace("rtte: sampling", "seq", seq)
	}
}

// onACK takes the measurement when the acknowledgement of the segment being
// timed arrives.
func (e *rttEstimator) onACK(now time.Time, seq wire.TCPSeqNumber) {
	sample, ok := e.pendingSample.Get()
	if !ok || seq.Compare(sample.seq) < 0 {
		return
	}
	e.sample(uint32(elapsedBetween(sample.sentAt, now).Milliseconds()))
	e.pendingSample = wire.Optional[rttSample]{}
}

// onRTO backs off the timer after a retransmission timeout.
func (e *rttEstimator) onRTO() {
	// RFC 6298 (5.5) The host MUST set RTO <- RTO * 2 ("back off the
	// timer"). The maximum value discussed in (2.5) above may be used to
	// provide an upper bound to this doubling operation.
	e.rto = min(e.rto*backoffFactor, maxRTOMillis)
	netlog.Trace("rtte: doubling rto", "rto", e.rto)

	// RFC 6298: a TCP implementation MAY clear SRTT and RTTVAR after backing
	// off the timer multiple times as it is likely that the current SRTT and
	// RTTVAR are bogus in this situation. Once SRTT and RTTVAR are cleared,
	// they should be initialized with the next RTT sample taken per (2.2)
	// rather than using (2.3).
	e.rtoCount++
	if e.rtoCount >= backoffsBeforeForgettingRTT {
		e.rtoCount = 0
		e.haveMeasurement = false
		netlog.Trace("rtte: too many retransmissions, clearing srtt, rttvar.")
	}
}

// onRetransmit stops timing: the acknowledgement of a segment sent twice
// could be for either copy, so it would be a bogus measurement.
func (e *rttEstimator) onRetransmit() {
	if e.pendingSample.IsSome() {
		netlog.Trace("rtte: abort sampling due to retransmit")
	}
	e.pendingSample = wire.Optional[rttSample]{}
}

// blend returns the weighted average (denominator-1)/denominator * average +
// 1/denominator * measurement, rounded up, as RFC 6298 (2.3) updates SRTT and
// RTTVAR.
func blend(average, measurement, denominator uint32) uint32 {
	return divideRoundingUp(average*(denominator-1)+measurement, denominator)
}

func divideRoundingUp(dividend, divisor uint32) uint32 {
	quotient := dividend / divisor
	if dividend%divisor != 0 {
		quotient++
	}
	return quotient
}

func absoluteDifference(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// elapsedBetween returns how far apart two times are, whichever comes first,
// as smoltcp's Instant subtraction does.
func elapsedBetween(earlier, later time.Time) time.Duration {
	elapsed := later.Sub(earlier)
	if elapsed < 0 {
		return -elapsed
	}
	return elapsed
}
