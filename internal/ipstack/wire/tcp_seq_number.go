package wire

import (
	"math"
	"strconv"
)

// TCPSeqNumber is a TCP sequence number: an integer modulo 2³² that compares
// across the wrap-around, so that a number just past 2³¹ − 1 is after it.
type TCPSeqNumber int32

// Add returns the sequence number n octets after s; a negative n goes back.
// It panics if n is outside ±(2³¹ − 1): no segment or window spans half the
// sequence space.
func (s TCPSeqNumber) Add(n int) TCPSeqNumber {
	switch {
	case n > math.MaxInt32:
		panic("attempt to add to sequence number with unsigned overflow")
	case n < -math.MaxInt32:
		panic("attempt to subtract to sequence number with unsigned overflow")
	}
	return TCPSeqNumber(int32(s) + int32(n))
}

// Sub returns how many octets s is after earlier. It panics if s is before
// earlier.
func (s TCPSeqNumber) Sub(earlier TCPSeqNumber) int {
	difference := int32(s) - int32(earlier)
	if difference < 0 {
		panic("attempt to subtract sequence numbers with underflow")
	}
	return int(difference)
}

// Compare returns -1 if s is before other, 0 if they are equal and +1 if s is
// after other, in sequence space.
func (s TCPSeqNumber) Compare(other TCPSeqNumber) int {
	difference := int32(s) - int32(other)
	switch {
	case difference < 0:
		return -1
	case difference > 0:
		return 1
	default:
		return 0
	}
}

// Max returns the later of s and other.
func (s TCPSeqNumber) Max(other TCPSeqNumber) TCPSeqNumber {
	if s.Compare(other) > 0 {
		return s
	}
	return other
}

// Min returns the earlier of s and other.
func (s TCPSeqNumber) Min(other TCPSeqNumber) TCPSeqNumber {
	if s.Compare(other) < 0 {
		return s
	}
	return other
}

// String formats the number as the unsigned value on the wire.
func (s TCPSeqNumber) String() string {
	return strconv.FormatUint(uint64(uint32(s)), 10)
}
