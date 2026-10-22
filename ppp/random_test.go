package ppp

import (
	"bytes"
	"testing"
)

func TestFirstNonzeroSkipsZeros(t *testing.T) {
	draws := []uint32{0, 0, 0x5A5A_5A5A, 7}
	draw := func() uint32 {
		value := draws[0]
		draws = draws[1:]
		return value
	}

	if got := firstNonzero(draw); got != 0x5A5A_5A5A {
		t.Errorf("firstNonzero = %#x, want 0x5a5a5a5a", got)
	}
}

func TestMagicNumbersAreNeverZero(t *testing.T) {
	for range 1000 {
		if nonzeroUint32() == 0 {
			t.Fatal("nonzeroUint32 returned zero")
		}
	}
}

func TestRandomBytesHaveTheLengthAskedForAndDiffer(t *testing.T) {
	first, second := randomBytes(challengeLen), randomBytes(challengeLen)

	if len(first) != challengeLen || len(second) != challengeLen {
		t.Fatalf("lengths = %d and %d, want %d", len(first), len(second), challengeLen)
	}
	// Two equal 128-bit draws would happen once in 2^128 runs.
	if bytes.Equal(first, second) {
		t.Errorf("two draws were both % X", first)
	}
}
