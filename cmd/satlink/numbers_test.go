package main

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestUnsignedNumbersAreReadAsRustReadsAU64(t *testing.T) {
	tests := []struct {
		text string
		want uint64
		err  error
	}{
		{"300", 300, nil},
		{"+5", 5, nil},
		{"010", 10, nil},
		{"18446744073709551615", math.MaxUint64, nil},
		{"", 0, errEmptyNumber},
		{"+", 0, errInvalidDigit},
		{"-1", 0, errInvalidDigit},
		{"0x10", 0, errInvalidDigit},
		{"1_000", 0, errInvalidDigit},
		{" 5", 0, errInvalidDigit},
		{"18446744073709551616", 0, errNumberTooLarge},
	}
	for _, test := range tests {
		got, err := parseUnsigned(test.text)
		if got != test.want || !errors.Is(err, test.err) {
			t.Errorf("parseUnsigned(%q) = %d, %v, want %d, %v", test.text, got, err, test.want, test.err)
		}
	}
}

func TestSignedNumbersAreReadAsRustReadsAnI64(t *testing.T) {
	tests := []struct {
		text string
		want int64
		err  error
	}{
		{"-5", -5, nil},
		{"+5", 5, nil},
		{"-0", 0, nil},
		{"", 0, errEmptyNumber},
		{"-", 0, errInvalidDigit},
		{"5x", 0, errInvalidDigit},
		{"99999999999999999999", 0, errNumberTooLarge},
		{"-99999999999999999999", 0, errNumberTooSmall},
	}
	for _, test := range tests {
		got, err := parseSigned(test.text)
		if got != test.want || !errors.Is(err, test.err) {
			t.Errorf("parseSigned(%q) = %d, %v, want %d, %v", test.text, got, err, test.want, test.err)
		}
	}
}

func TestAnUnsignedValueRefusesNumbersBelowItsMinimum(t *testing.T) {
	number := uint64(300)
	value := unsignedValue{target: &number, minimum: 1}

	err := value.Set("0")

	want := "0 is not in 1..18446744073709551615"
	if err == nil || err.Error() != want || number != 300 {
		t.Errorf("Set(0) = %v, number %d, want %s and 300 kept", err, number, want)
	}
}

func TestAnUnsignedValueKeepsWhatItReads(t *testing.T) {
	var number uint64
	value := unsignedValue{target: &number}

	if err := value.Set("+0"); err != nil || number != 0 {
		t.Errorf("Set(+0) = %v, number %d, want 0", err, number)
	}
	if err := value.Set("x"); !errors.Is(err, errInvalidDigit) {
		t.Errorf("Set(x) = %v, want %v", err, errInvalidDigit)
	}
}

func TestABoundedValueChecksTheRangeOfItsType(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"65536", "65536 is not in 0..=65535"},
		{"-1", "-1 is not in 0..=65535"},
		{"99999999999999999999", "number too large to fit in target type"},
		{"seven", "invalid digit found in string"},
	}
	for _, test := range tests {
		port := uint16(7)

		err := boundedValue[uint16]{target: &port}.Set(test.text)

		if err == nil || err.Error() != test.want || port != 7 {
			t.Errorf("Set(%q) = %v, port %d, want %s and 7 kept", test.text, err, port, test.want)
		}
	}
}

func TestABoundedValueTakesWhatFitsItsType(t *testing.T) {
	var baud uint32

	if err := (boundedValue[uint32]{target: &baud}).Set("+4294967295"); err != nil || baud != math.MaxUint32 {
		t.Errorf("Set(+4294967295) = %v, baud %d, want the largest uint32", err, baud)
	}
	if err := (boundedValue[uint32]{target: &baud}).Set("4294967296"); err == nil ||
		err.Error() != "4294967296 is not in 0..=4294967295" {
		t.Errorf("Set(4294967296) = %v, want it out of range", err)
	}
}

func TestDurationsSaturateInsteadOfOverflowing(t *testing.T) {
	tests := []struct {
		count uint64
		unit  time.Duration
		want  time.Duration
	}{
		{300, time.Second, 5 * time.Minute},
		{60, time.Minute, time.Hour},
		{math.MaxInt64 / uint64(time.Minute), time.Minute, math.MaxInt64 / time.Minute * time.Minute},
		{math.MaxInt64/uint64(time.Minute) + 1, time.Minute, math.MaxInt64},
		{math.MaxUint64, time.Second, math.MaxInt64},
	}
	for _, test := range tests {
		if got := saturatingDuration(test.count, test.unit); got != test.want {
			t.Errorf("saturatingDuration(%d, %v) = %v, want %v", test.count, test.unit, got, test.want)
		}
	}
}

func TestProductsSaturateInsteadOfOverflowing(t *testing.T) {
	if got := saturatingProduct(3, 1024); got != 3072 {
		t.Errorf("3 × 1024 = %d, want 3072", got)
	}
	if got := saturatingProduct(math.MaxUint64/1024+1, 1024); got != math.MaxUint64 {
		t.Errorf("an overflowing product = %d, want the largest uint64", got)
	}
}
