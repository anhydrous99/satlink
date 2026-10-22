package satlink

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestSpotsAResultCodeSplitAcrossReads(t *testing.T) {
	watch := newResultCodeWatch(noCarrierCode)
	stream := []byte("\x7e\xff\r\nNO CAR")
	for start := 0; start < len(stream); start += 3 {
		watch.observe(stream[start:min(start+3, len(stream))])
	}
	if watch.seen {
		t.Fatal("NO CARRIER seen before it was complete")
	}

	watch.observe([]byte("RIER\r\n"))

	if !watch.seen {
		t.Error("NO CARRIER split across reads went unseen")
	}
}

func TestIgnoresTheWordsWithoutABareCarriageReturn(t *testing.T) {
	watch := newResultCodeWatch(noCarrierCode, okCode)

	watch.observe([]byte("an email saying\nNO CARRIER\nor\nOK\nin its body"))

	if watch.seen {
		t.Error("data without a bare CR passed for a result code")
	}
}

func TestBearerCommand(t *testing.T) {
	bearer := BearerService{Speed: 6, Name: 0, ConnectionElement: 1}

	if command := bearer.command(); command != "AT+CBST=6,0,1" {
		t.Errorf("command = %q, want AT+CBST=6,0,1", command)
	}
}

func TestAnEscapeGuardBeyondAnyHandsetsIsRefused(t *testing.T) {
	if err := validateEscapeGuard(MaxEscapeGuard); err != nil {
		t.Errorf("validateEscapeGuard(MaxEscapeGuard) = %v", err)
	}

	err := validateEscapeGuard(math.MaxInt64)

	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("validateEscapeGuard(MaxInt64) = %v, want ErrInvalidArgument", err)
	}
}

func TestDebugDurationFormat(t *testing.T) {
	tests := map[time.Duration]string{
		10 * time.Second:              "10s",
		90 * time.Second:              "90s",
		10500 * time.Millisecond:      "10.5s",
		time.Second + time.Nanosecond: "1.000000001s",
		math.MaxInt64:                 "9223372036.854775807s",
		500 * time.Millisecond:        "500ms",
		1500 * time.Nanosecond:        "1.5µs",
		0:                             "0ns",
	}
	for duration, want := range tests {
		if got := debugDuration(duration); got != want {
			t.Errorf("debugDuration(%d) = %q, want %q", int64(duration), got, want)
		}
	}
}

func TestAnEscapeWithoutAGuardKeepsNone(t *testing.T) {
	quietSince := time.Now()

	plan := newEscape(-time.Hour, quietSince)

	if plan.guard != 0 || plan.attempts != escapeAttempts || !plan.quietSince.Equal(quietSince) {
		t.Errorf("newEscape = %+v, want no guard and %d attempts", plan, escapeAttempts)
	}
}
