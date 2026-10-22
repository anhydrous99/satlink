package sim

import (
	"math"
	"testing"
	"time"
)

func TestLaterAddsOrdinaryWaitsAndCapsAbsurdOnes(t *testing.T) {
	t.Parallel()
	start := time.Now()
	cases := []struct {
		wait time.Duration
		want time.Time
	}{
		{wait: 5 * time.Millisecond, want: start.Add(5 * time.Millisecond)},
		{wait: math.MaxInt64, want: start.Add(longestWait)},
		{wait: -time.Second, want: start},
	}

	for _, test := range cases {
		if got := later(start, test.wait); !got.Equal(test.want) {
			t.Errorf("later(start, %v) = start + %v; want start + %v", test.wait, got.Sub(start), test.want.Sub(start))
		}
	}
}
