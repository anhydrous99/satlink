package mobile

import (
	"math"
	"testing"
	"time"
)

func TestTimeoutsSaturateAndNegativeOnesCountAsZero(t *testing.T) {
	cases := []struct {
		millis int64
		want   time.Duration
	}{
		{millis: math.MinInt64, want: 0},
		{millis: -1, want: 0},
		{millis: 0, want: 0},
		{millis: 1500, want: 1500 * time.Millisecond},
		{millis: math.MaxInt64 / int64(time.Millisecond), want: time.Duration(math.MaxInt64/int64(time.Millisecond)) * time.Millisecond},
		{millis: math.MaxInt64/int64(time.Millisecond) + 1, want: math.MaxInt64},
		{millis: math.MaxInt64, want: math.MaxInt64},
	}
	for _, c := range cases {
		if got := timeoutFromMillis(c.millis); got != c.want {
			t.Errorf("timeoutFromMillis(%d) = %v, want %v", c.millis, got, c.want)
		}
	}
}

func TestSettingSecondsSaturateBothWays(t *testing.T) {
	cases := []struct {
		seconds int64
		want    time.Duration
	}{
		{seconds: math.MinInt64, want: math.MinInt64},
		{seconds: math.MinInt64/int64(time.Second) - 1, want: math.MinInt64},
		{seconds: -1, want: -time.Second},
		{seconds: 0, want: 0},
		{seconds: 120, want: 2 * time.Minute},
		{seconds: math.MaxInt64/int64(time.Second) + 1, want: math.MaxInt64},
		{seconds: math.MaxInt64, want: math.MaxInt64},
	}
	for _, c := range cases {
		if got := durationFromSeconds(c.seconds); got != c.want {
			t.Errorf("durationFromSeconds(%d) = %v, want %v", c.seconds, got, c.want)
		}
	}
}

func TestADeadlineOfNoTimeIsNow(t *testing.T) {
	before := time.Now()

	deadline := deadlineAfterMillis(-5)

	if deadline.Before(before) || deadline.After(time.Now()) {
		t.Errorf("deadline %v is not now", deadline)
	}
}

func TestTheLongestTimeoutIsNoDeadlineInEffect(t *testing.T) {
	deadline := deadlineAfterMillis(math.MaxInt64)

	if aCentury := time.Now().AddDate(100, 0, 0); deadline.Before(aCentury) {
		t.Errorf("deadline %v is less than a century away", deadline)
	}
}

func TestDurationsCountInWholeSeconds(t *testing.T) {
	cases := []struct {
		duration time.Duration
		want     int64
	}{
		{duration: 0, want: 0},
		{duration: 999 * time.Millisecond, want: 0},
		{duration: 61900 * time.Millisecond, want: 61},
		{duration: time.Hour, want: 3600},
		{duration: math.MaxInt64, want: math.MaxInt64 / int64(time.Second)},
	}
	for _, c := range cases {
		if got := wholeSeconds(c.duration); got != c.want {
			t.Errorf("wholeSeconds(%v) = %d, want %d", c.duration, got, c.want)
		}
	}
}

func TestUnsignedCountsSaturate(t *testing.T) {
	cases := []struct {
		count uint64
		want  int64
	}{
		{count: 0, want: 0},
		{count: 1234, want: 1234},
		{count: math.MaxInt64, want: math.MaxInt64},
		{count: math.MaxUint64, want: math.MaxInt64},
	}
	for _, c := range cases {
		if got := countFromUnsigned(c.count); got != c.want {
			t.Errorf("countFromUnsigned(%d) = %d, want %d", c.count, got, c.want)
		}
	}
}
