package wire

import (
	"math"
	"strconv"
	"testing"
)

// The sequence numbers on either side of the point where int32 wraps
// around: in sequence space, the second is one after the first.
const (
	beforeWrap TCPSeqNumber = math.MaxInt32
	afterWrap  TCPSeqNumber = math.MinInt32
)

func TestTCPSeqNumberAddWrapsAround(t *testing.T) {
	t.Parallel()
	checkFields(t, []field{
		{"MaxInt32 + 1", beforeWrap.Add(1), afterWrap},
		{"2³² − 1 + 1", TCPSeqNumber(-1).Add(1), TCPSeqNumber(0)},
		{"0 − 1", TCPSeqNumber(0).Add(-1), TCPSeqNumber(-1)},
		{"MinInt32 − 1", afterWrap.Add(-1), beforeWrap},
		{"the largest step", TCPSeqNumber(1).Add(math.MaxInt32), afterWrap},
		{"the largest step back", TCPSeqNumber(0).Add(-math.MaxInt32), TCPSeqNumber(math.MinInt32 + 1)},
	})
}

func TestTCPSeqNumberAddPanicsBeyondHalfTheSequenceSpace(t *testing.T) {
	t.Parallel()
	t.Run("forwards", func(t *testing.T) {
		if strconv.IntSize == 32 {
			t.Skip("an int cannot exceed math.MaxInt32 on this platform")
		}
		// Computed at run time: as a constant, MaxInt32 + 1 would not
		// compile where int has 32 bits.
		tooFar := math.MaxInt32
		tooFar++
		expectPanicWith(t, "attempt to add to sequence number with unsigned overflow",
			func() { TCPSeqNumber(0).Add(tooFar) })
	})
	t.Run("backwards", func(t *testing.T) {
		expectPanicWith(t, "attempt to subtract to sequence number with unsigned overflow",
			func() { TCPSeqNumber(0).Add(-math.MaxInt32 - 1) })
	})
}

func TestTCPSeqNumberSub(t *testing.T) {
	t.Parallel()
	checkFields(t, []field{
		{"10 − 3", TCPSeqNumber(10).Sub(3), 7},
		{"equal", TCPSeqNumber(5).Sub(5), 0},
		{"across the wrap", afterWrap.Add(9).Sub(beforeWrap), 10},
		{"across zero", TCPSeqNumber(4).Sub(-6), 10},
	})
}

func TestTCPSeqNumberSubPanicsWhenTheNumberIsBeforeTheOther(t *testing.T) {
	t.Parallel()
	expectPanicWith(t, "attempt to subtract sequence numbers with underflow",
		func() { TCPSeqNumber(3).Sub(10) })
	expectPanicWith(t, "attempt to subtract sequence numbers with underflow",
		func() { beforeWrap.Sub(afterWrap) })
}

func TestTCPSeqNumberCompareAcrossTheWrap(t *testing.T) {
	t.Parallel()
	checkFields(t, []field{
		{"1 vs 2", TCPSeqNumber(1).Compare(2), -1},
		{"2 vs 1", TCPSeqNumber(2).Compare(1), 1},
		{"equal", TCPSeqNumber(7).Compare(7), 0},
		{"before the wrap vs after it", beforeWrap.Compare(afterWrap), -1},
		{"after the wrap vs before it", afterWrap.Compare(beforeWrap), 1},
		{"2³² − 1 vs 0", TCPSeqNumber(-1).Compare(0), -1},
	})
}

func TestTCPSeqNumberMaxAndMinAcrossTheWrap(t *testing.T) {
	t.Parallel()
	checkFields(t, []field{
		{"Max", beforeWrap.Max(afterWrap), afterWrap},
		{"Max, the other way", afterWrap.Max(beforeWrap), afterWrap},
		{"Min", beforeWrap.Min(afterWrap), beforeWrap},
		{"Min, the other way", afterWrap.Min(beforeWrap), beforeWrap},
		{"Max of equals", TCPSeqNumber(3).Max(3), TCPSeqNumber(3)},
	})
}

func TestTCPSeqNumberStringIsTheUnsignedValue(t *testing.T) {
	t.Parallel()
	checkFields(t, []field{
		{"0", TCPSeqNumber(0).String(), "0"},
		{"MaxInt32", beforeWrap.String(), "2147483647"},
		{"MinInt32", afterWrap.String(), "2147483648"},
		{"-1", TCPSeqNumber(-1).String(), "4294967295"},
	})
}
