package sim

import (
	"math"
	"testing"
	"time"
)

func testLine(delayMillis, bytesPerSecond int) *delayLine {
	return newDelayLine(LinkProfile{
		CallSetup:      0,
		OneWayDelay:    time.Duration(delayMillis) * time.Millisecond,
		BytesPerSecond: bytesPerSecond,
	})
}

// assertReceived fails the test unless line delivers want at now.
func assertReceived(t *testing.T, line *delayLine, now time.Time, want string) {
	t.Helper()
	if got := line.receive(now); string(got) != want {
		t.Errorf("received %q; want %q", got, want)
	}
}

func TestAnInstantLineDeliversAtOnce(t *testing.T) {
	t.Parallel()
	start := time.Now()
	line := testLine(0, 0)

	line.send([]byte("hello"), start)

	assertReceived(t, line, start, "hello")
	assertReceived(t, line, start, "")
}

func TestDelayHoldsBytesBackInOrder(t *testing.T) {
	t.Parallel()
	start := time.Now()
	line := testLine(100, 0)

	line.send([]byte("first "), start)
	line.send([]byte("second"), at(start, 10))

	assertReceived(t, line, at(start, 99), "")
	assertReceived(t, line, at(start, 100), "first ")
	assertReceived(t, line, at(start, 110), "second")
}

func TestARateCapPacesBytesOneByOne(t *testing.T) {
	t.Parallel()
	start := time.Now()
	// 100 bytes per second: one byte every 10 ms, after 50 ms of delay.
	line := testLine(50, 100)

	line.send([]byte("abcd"), start)

	assertReceived(t, line, at(start, 59), "")
	assertReceived(t, line, at(start, 60), "a")
	assertReceived(t, line, at(start, 75), "b")
	assertReceived(t, line, at(start, 90), "cd")
}

func TestLaterBytesQueueBehindABusyLine(t *testing.T) {
	t.Parallel()
	start := time.Now()
	line := testLine(0, 100)

	line.send([]byte("ab"), start)
	line.send([]byte("c"), at(start, 5))

	// "ab" occupies the line until 20 ms, so "c" is due at 30 ms.
	assertReceived(t, line, at(start, 29), "ab")
	assertReceived(t, line, at(start, 30), "c")
}

func TestANegativeRateStillDeliversSlowly(t *testing.T) {
	t.Parallel()
	start := time.Now()
	// Zero is unlimited, and a negative rate is the slowest one.
	line := testLine(0, -1)

	line.send([]byte("x"), start)

	assertReceived(t, line, at(start, 999), "")
	assertReceived(t, line, at(start, 1000), "x")
}

func TestAHugeRateDeliversWithoutOverflowing(t *testing.T) {
	t.Parallel()
	start := time.Now()
	line := testLine(0, math.MaxInt)

	line.send([]byte("fast"), start)

	// Hours of elapsed time times the largest rate overflow 64 bits, which
	// the 128-bit arithmetic of mulDiv never does.
	assertReceived(t, line, start.Add(3*time.Hour), "fast")
}

func TestBytesCarriedCountsWholeBytesAndSaturatesAtTheLargestInt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		rate    int
		elapsed time.Duration
		want    int
	}{
		{name: "a part of a byte", rate: 100, elapsed: 9 * time.Millisecond, want: 0},
		{name: "whole bytes", rate: 100, elapsed: 55 * time.Millisecond, want: 5},
		{name: "Iridium's second", rate: 300, elapsed: time.Second, want: 300},
		// More than an int holds on any platform; on a 32-bit one, even
		// before the 64-bit arithmetic saturates.
		{name: "beyond an int", rate: math.MaxInt, elapsed: 3 * time.Hour, want: math.MaxInt},
	}

	for _, test := range cases {
		if got := bytesCarried(test.rate, test.elapsed); got != test.want {
			t.Errorf("%s: %d bytes; want %d", test.name, got, test.want)
		}
	}
}

func TestMulDivSaturatesWhenTheQuotientDoesNotFit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		got      uint64
		expected uint64
	}{
		{name: "floor", got: mulDiv(10, 3, 4), expected: 7},
		{name: "ceiling", got: mulDivCeil(10, 3, 4), expected: 8},
		{name: "exact ceiling", got: mulDivCeil(8, 3, 4), expected: 6},
		{name: "128-bit product", got: mulDiv(math.MaxUint64, 1000, 1_000_000), expected: math.MaxUint64 / 1000},
		{name: "huge floor", got: mulDiv(math.MaxUint64, math.MaxUint64, 2), expected: math.MaxInt64},
		{name: "huge ceiling", got: mulDivCeil(math.MaxInt64, 2, 1), expected: math.MaxInt64},
	}

	for _, test := range cases {
		if test.got != test.expected {
			t.Errorf("%s: %d; want %d", test.name, test.got, test.expected)
		}
	}
}
