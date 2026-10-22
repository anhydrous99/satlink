package ppp

import (
	"math"
	"testing"
	"time"
)

func TestADeadlineIsDueFromItsInstantOn(t *testing.T) {
	deadline := testStart.Add(time.Second)
	tests := []struct {
		name     string
		deadline time.Time
		now      time.Time
		want     bool
	}{
		{"before the deadline", deadline, deadline.Add(-time.Nanosecond), false},
		{"at the deadline", deadline, deadline, true},
		{"after the deadline", deadline, deadline.Add(time.Hour), true},
		{"without a deadline", time.Time{}, deadline, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isDue(test.deadline, test.now); got != test.want {
				t.Errorf("isDue = %t, want %t", got, test.want)
			}
		})
	}
}

func TestTheEarliestDeadlineIgnoresTimersThatAreNotRunning(t *testing.T) {
	soon, later := testStart.Add(time.Second), testStart.Add(time.Minute)

	if got := earliest(time.Time{}, later, soon, time.Time{}); got != soon {
		t.Errorf("earliest = %v, want %v", got, soon)
	}
	if got := earliest(time.Time{}, time.Time{}); !got.IsZero() {
		t.Errorf("earliest of no deadlines = %v, want the zero Time", got)
	}
}

func TestScaledDurationsSaturateInsteadOfOverflowing(t *testing.T) {
	longest := time.Duration(math.MaxInt64)
	tests := []struct {
		duration time.Duration
		count    int
		want     time.Duration
	}{
		{5 * time.Second, 10, 50 * time.Second},
		{longest / 2, 2, longest - 1},
		{longest / 2, 3, longest},
		{longest, 10, longest},
	}
	for _, test := range tests {
		if got := scaled(test.duration, test.count); got != test.want {
			t.Errorf("scaled(%v, %d) = %v, want %v", test.duration, test.count, got, test.want)
		}
	}
}
