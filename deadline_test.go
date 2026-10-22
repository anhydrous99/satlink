package satlink

import (
	"math"
	"testing"
	"time"
)

func TestTheLongestTimeoutMeansNoDeadline(t *testing.T) {
	wait, ok := timeToWait(deadlineAfter(math.MaxInt64))

	if !ok || wait != longestRead {
		t.Errorf("timeToWait = %v, %v; want %v, true", wait, ok, longestRead)
	}
}

func TestAPassedDeadlineLeavesNoTime(t *testing.T) {
	if wait, ok := timeToWait(time.Now()); ok {
		t.Errorf("timeToWait(now) = %v, true; want false", wait)
	}

	soon := 100 * time.Millisecond
	if wait, ok := timeToWait(deadlineAfter(soon)); !ok || wait > soon {
		t.Errorf("timeToWait(soon) = %v, %v; want at most %v, true", wait, ok, soon)
	}
}

func TestATimeoutOfZeroOrLessHasPassedAlready(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second, math.MinInt64} {
		if wait, ok := timeToWait(deadlineAfter(timeout)); ok {
			t.Errorf("timeToWait(deadlineAfter(%v)) = %v, true; want false", timeout, wait)
		}
	}
}
