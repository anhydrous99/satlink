package main

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// Whole numbers from the command line, read as the Rust version's clap read
// them, and their conversions, which saturate instead of overflowing.

// The texts of Rust's ParseIntError, which the Rust version's clap gave for
// a number it could not read.
var (
	errEmptyNumber    = errors.New("cannot parse integer from empty string")
	errInvalidDigit   = errors.New("invalid digit found in string")
	errNumberTooLarge = errors.New("number too large to fit in target type")
	errNumberTooSmall = errors.New("number too small to fit in target type")
)

// parseUnsigned reads text as Rust reads a u64: decimal digits after an
// optional '+', so "010" is ten and "0x10" no number at all.
func parseUnsigned(text string) (uint64, error) {
	if text == "" {
		return 0, errEmptyNumber
	}
	number, err := strconv.ParseUint(strings.TrimPrefix(text, "+"), 10, 64)
	switch {
	case err == nil:
		return number, nil
	case errors.Is(err, strconv.ErrRange):
		return 0, errNumberTooLarge
	default:
		return 0, errInvalidDigit
	}
}

// parseSigned reads text as Rust reads an i64: decimal digits after an
// optional sign.
func parseSigned(text string) (int64, error) {
	if text == "" {
		return 0, errEmptyNumber
	}
	number, err := strconv.ParseInt(text, 10, 64)
	switch {
	case err == nil:
		return number, nil
	case !errors.Is(err, strconv.ErrRange):
		return 0, errInvalidDigit
	case strings.HasPrefix(text, "-"):
		return 0, errNumberTooSmall
	default:
		return 0, errNumberTooLarge
	}
}

// unsignedValue keeps a whole number of at least minimum: a u64 argument
// with clap's range(minimum..), as the net command's limits are, or any u64
// when minimum is 0.
type unsignedValue struct {
	target  *uint64
	minimum uint64
}

// Set reads text into the target, refusing a number below the minimum as
// clap does: "0 is not in 1..18446744073709551615".
func (v unsignedValue) Set(text string) error {
	number, err := parseUnsigned(text)
	if err != nil {
		return err
	}
	if number < v.minimum {
		return fmt.Errorf("%d is not in %d..%d", number, v.minimum, uint64(math.MaxUint64))
	}
	*v.target = number
	return nil
}

// unsignedInteger is a narrow unsigned type, such as a TCP port's.
type unsignedInteger interface {
	~uint16 | ~uint32
}

// boundedValue keeps a number of type T. clap read such numbers — a u16 or
// u32 of the Rust version — as an i64 and then checked their range, so a
// negative number is out of range rather than not a number: "-1 is not in
// 0..=65535".
type boundedValue[T unsignedInteger] struct {
	target *T
}

// Set reads text into the target.
func (v boundedValue[T]) Set(text string) error {
	number, err := parseSigned(text)
	if err != nil {
		return err
	}
	largest := ^T(0)
	if number < 0 || uint64(number) > uint64(largest) {
		return fmt.Errorf("%d is not in 0..=%d", number, largest)
	}
	*v.target = T(number)
	return nil
}

// saturatingDuration returns count units, or the longest duration when that
// would overflow: an absurd limit must be refused as longer than a year, not
// wrap round into a negative one.
func saturatingDuration(count uint64, unit time.Duration) time.Duration {
	if count > uint64(math.MaxInt64/unit) {
		return math.MaxInt64
	}
	return time.Duration(count) * unit
}

// saturatingProduct returns a times b, or the largest uint64 when that would
// overflow, as Rust's saturating_mul does.
func saturatingProduct(a, b uint64) uint64 {
	high, low := bits.Mul64(a, b)
	if high != 0 {
		return math.MaxUint64
	}
	return low
}
