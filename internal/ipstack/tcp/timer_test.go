package tcp

// Tests of the retransmission timer.

import (
	"testing"
	"time"
)

// assertRetransmissionDue fails the test unless the timer asks for a
// retransmission at each of the times given, as durations after the epoch.
func assertRetransmissionDue(t *testing.T, r timer, times ...time.Duration) {
	t.Helper()
	for _, elapsed := range times {
		if !r.shouldRetransmit(instant(elapsed)) {
			t.Errorf("timer %+v asks for no retransmission at %v, want one", r, elapsed)
		}
	}
}

// assertNoRetransmissionDue fails the test if the timer asks for a
// retransmission at any of the times given, as durations after the epoch.
func assertNoRetransmissionDue(t *testing.T, r timer, times ...time.Duration) {
	t.Helper()
	for _, elapsed := range times {
		if r.shouldRetransmit(instant(elapsed)) {
			t.Errorf("timer %+v asks for a retransmission at %v, want none", r, elapsed)
		}
	}
}

func TestTimerRetransmit(t *testing.T) {
	t.Parallel()
	const rto = 100 * time.Millisecond
	var r timer
	assertNoRetransmissionDue(t, r, time.Second)

	r.setForRetransmit(instant(1000*time.Millisecond), rto)
	assertNoRetransmissionDue(t, r, 1000*time.Millisecond, 1050*time.Millisecond)
	assertRetransmissionDue(t, r, 1101*time.Millisecond)

	r.setForRetransmit(instant(1101*time.Millisecond), rto)
	assertNoRetransmissionDue(t, r, 1101*time.Millisecond, 1150*time.Millisecond, 1200*time.Millisecond)
	assertRetransmissionDue(t, r, 1301*time.Millisecond)

	r.setForIdle(instant(1301*time.Millisecond), 0)
	assertNoRetransmissionDue(t, r, 1350*time.Millisecond)
}
