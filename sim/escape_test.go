package sim

import (
	"testing"
	"time"
)

const testGuard = 100 * time.Millisecond

// at returns the time millis milliseconds after start.
func at(start time.Time, millis int) time.Time {
	return start.Add(time.Duration(millis) * time.Millisecond)
}

// assertExpiry fails the test unless got is of kind want, carrying data.
func assertExpiry(t *testing.T, got guardExpiry, want guardExpiryKind, data string) {
	t.Helper()
	if got.kind != want || string(got.data) != data {
		t.Errorf("expiry %v %q; want %v %q", got.kind, got.data, want, data)
	}
}

// assertData fails the test unless data is want.
func assertData(t *testing.T, data []byte, want string) {
	t.Helper()
	if string(data) != want {
		t.Errorf("data %q; want %q", data, want)
	}
}

func TestEscapeNeedsSilenceBeforeAndAfter(t *testing.T) {
	t.Parallel()
	start := time.Now()
	detector := newEscapeDetector(testGuard, start)

	assertData(t, detector.filter([]byte("+++"), at(start, 100)), "")
	assertExpiry(t, detector.expire(at(start, 150)), guardPending, "")
	assertExpiry(t, detector.expire(at(start, 200)), guardEscape, "")
	assertExpiry(t, detector.expire(at(start, 400)), guardPending, "")
}

func TestPlusesInsideDataAreData(t *testing.T) {
	t.Parallel()
	start := time.Now()
	detector := newEscapeDetector(testGuard, start)

	assertData(t, detector.filter([]byte("a+++"), at(start, 500)), "a+++")
	assertExpiry(t, detector.expire(at(start, 900)), guardPending, "")
}

func TestDataRightAfterTheSequenceCancelsIt(t *testing.T) {
	t.Parallel()
	start := time.Now()
	detector := newEscapeDetector(testGuard, start)

	assertData(t, detector.filter([]byte("+++"), at(start, 100)), "")
	assertData(t, detector.filter([]byte("x"), at(start, 150)), "+++x")
	assertExpiry(t, detector.expire(at(start, 400)), guardPending, "")
}

func TestAnUnfinishedSequenceBecomesDataAfterTheGuardTime(t *testing.T) {
	t.Parallel()
	start := time.Now()
	detector := newEscapeDetector(testGuard, start)

	assertData(t, detector.filter([]byte("++"), at(start, 100)), "")
	assertExpiry(t, detector.expire(at(start, 200)), guardReleasedData, "++")
}

func TestASlowPlusStartsANewSequence(t *testing.T) {
	t.Parallel()
	start := time.Now()
	detector := newEscapeDetector(testGuard, start)

	assertData(t, detector.filter([]byte("+"), at(start, 100)), "")
	// Without a call to expire in between, the slow + still releases the
	// first.
	assertData(t, detector.filter([]byte("+"), at(start, 250)), "+")
	assertData(t, detector.filter([]byte("++"), at(start, 260)), "")
	assertExpiry(t, detector.expire(at(start, 360)), guardEscape, "")
}

func TestAZeroGuardTimeNeedsNoSilence(t *testing.T) {
	t.Parallel()
	start := time.Now()
	detector := newEscapeDetector(0, start)

	assertData(t, detector.filter([]byte("ab+++"), start), "ab")
	assertExpiry(t, detector.expire(start), guardEscape, "")
}

func TestANegativeGuardTimeCountsAsNone(t *testing.T) {
	t.Parallel()
	start := time.Now()
	detector := newEscapeDetector(-time.Second, start)

	assertData(t, detector.filter([]byte("ab+++"), start), "ab")
	assertExpiry(t, detector.expire(start), guardEscape, "")
}
