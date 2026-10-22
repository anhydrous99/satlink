package tcp

// Tests of the RTT estimator: the bounds of the RTO and its smallest margin,
// what a measurement measures, that it ends its sample, and the estimate's
// reset after repeated backoffs.

import (
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestRTTEstimator(t *testing.T) {
	t.Parallel()
	const roundTripMillis = 2000
	// The RTO after each measurement of a steady 2 s round trip, in
	// milliseconds: RFC 6298's SRTT + 4 × RTTVAR, falling as the variance
	// shrinks.
	wantRTOMillis := []int{
		6000, 5000, 4252, 3692, 3272, 2956, 2720, 2540, 2408, 2308, 2232, 2176, 2132, 2100,
		2076, 2060, 2048, 2036, 2028, 2024, 2020, 2016, 2012, 2012,
	}
	r := newRTTEstimator()

	for measurement, wantMillis := range wantRTOMillis {
		r.sample(roundTripMillis)

		want := time.Duration(wantMillis) * time.Millisecond
		if got := r.retransmissionTimeout(); got != want {
			t.Fatalf("RTO after measurement %d = %v, want %v", measurement+1, got, want)
		}
	}
}

func TestRTTEstimatorKeepsTheRTOWithinItsBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		roundTripMillis uint32
		wantRTO         time.Duration
	}{
		// SRTT + 4 × RTTVAR = 100 + 4 × 50 = 300 ms, rounded up to 1 s.
		{100, time.Second},
		// 30 + 4 × 15 = 90 s, capped at 60 s.
		{30_000, time.Minute},
	}
	for _, test := range tests {
		r := newRTTEstimator()

		r.sample(test.roundTripMillis)

		if got := r.retransmissionTimeout(); got != test.wantRTO {
			t.Errorf("RTO after a first measurement of %d ms = %v, want %v", test.roundTripMillis, got, test.wantRTO)
		}
	}
}

// However steady the round trip, the RTO keeps a margin of 5 ms over SRTT:
// once RTTVAR is down to 1 ms, RFC 6298's 4 × RTTVAR would leave only 4 ms.
func TestRTTEstimatorKeepsAMarginOfFiveMilliseconds(t *testing.T) {
	t.Parallel()
	r := rttEstimator{haveMeasurement: true, srtt: 2000, rttvar: 1, rto: 2005}

	r.sample(2000)

	// RTTVAR stays 1 ms (3/4 of it, rounded up), and SRTT 2000 ms.
	if got := r.retransmissionTimeout(); got != 2005*time.Millisecond {
		t.Errorf("RTO = %v with SRTT 2000 ms and RTTVAR 1 ms, want 2.005s", got)
	}
}

// The distance between two instants is their difference, so even a clock that
// steps back gives a round-trip time.
func TestRTTEstimatorMeasuresTheDistanceBetweenSendAndACK(t *testing.T) {
	t.Parallel()
	const seq = wire.TCPSeqNumber(10)
	r := newRTTEstimator()
	r.onSend(instant(2500*time.Millisecond), seq)

	r.onACK(instant(1000*time.Millisecond), seq)

	// A measurement of 1500 ms: SRTT 1500 ms and RTTVAR 750 ms, for an RTO
	// of 1500 + 4 × 750 = 4500 ms.
	if got := r.retransmissionTimeout(); got != 4500*time.Millisecond {
		t.Errorf("RTO = %v, want 4.5s", got)
	}
}

// The estimator only samples. The acknowledgement of the
// segment being timed ends the sample it measures: a later one measures
// nothing until another segment is timed.
func TestRTTEstimatorMeasuresASampleOnce(t *testing.T) {
	t.Parallel()
	const seq = wire.TCPSeqNumber(10)
	r := newRTTEstimator()
	r.onSend(instant(0), seq)
	// A measurement of 100 ms: SRTT 100 ms and RTTVAR 50 ms, for an RTO of
	// 300 ms, which rounds up to the smallest, 1 s.
	r.onACK(instant(100*time.Millisecond), seq)

	r.onACK(instant(30*time.Second), seq)

	if got := r.retransmissionTimeout(); got != time.Second {
		t.Errorf("RTO = %v after a second ACK of the timed segment, want 1s", got)
	}
}

func TestRTTEstimatorStartsAfreshAfterThreeBackoffs(t *testing.T) {
	t.Parallel()
	// After a first measurement of 2 s (SRTT 2000 ms, RTTVAR 1000 ms), a
	// measurement of 100 ms updates the estimate by RFC 6298 (2.3): SRTT
	// 1763 ms and RTTVAR 1225 ms, for an RTO of 1763 + 4 × 1225 = 6663 ms.
	// Once the estimate is forgotten, it is the first measurement again
	// (2.2): SRTT 100 ms and RTTVAR 50 ms, for an RTO of 300 ms, which rounds
	// up to the smallest, 1 s.
	tests := []struct {
		backoffs int
		wantRTO  time.Duration
	}{
		{0, 6663 * time.Millisecond},
		{2, 6663 * time.Millisecond},
		{3, time.Second},
	}
	for _, test := range tests {
		r := newRTTEstimator()
		r.sample(2000)
		for range test.backoffs {
			r.onRTO()
		}

		r.sample(100)

		if got := r.retransmissionTimeout(); got != test.wantRTO {
			t.Errorf("RTO after %d backoffs and a new measurement = %v, want %v", test.backoffs, got, test.wantRTO)
		}
	}
}

func TestRTTEstimatorCountsOnlyTheBackoffsSinceTheLastMeasurement(t *testing.T) {
	t.Parallel()
	r := newRTTEstimator()
	r.sample(2000)
	r.onRTO()
	r.onRTO()
	r.sample(2000) // SRTT 2000 ms, RTTVAR 750 ms
	r.onRTO()

	r.sample(100)

	// Three backoffs, but a measurement among them: the estimate is updated
	// by RFC 6298 (2.3), to SRTT 1763 ms and RTTVAR 1038 ms, for an RTO of
	// 1763 + 4 × 1038 = 5915 ms.
	if got := r.retransmissionTimeout(); got != 5915*time.Millisecond {
		t.Errorf("RTO = %v, want 5.915s", got)
	}
}
