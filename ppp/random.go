package ppp

import (
	"crypto/rand"
	"encoding/binary"
)

// Unpredictable values for magic numbers and CHAP challenges, drawn from
// crypto/rand, which is seeded by the operating system: unguessable from
// outside the process.

// nonzeroUint32 returns a random non-zero number, as LCP magic numbers
// must be (RFC 1661 §6.4).
func nonzeroUint32() uint32 {
	return firstNonzero(randomUint32)
}

// firstNonzero returns the first value draw returns that is not zero.
func firstNonzero(draw func() uint32) uint32 {
	for {
		if value := draw(); value != 0 {
			return value
		}
	}
}

// randomUint32 returns a uniformly random number.
func randomUint32() uint32 {
	return binary.BigEndian.Uint32(randomBytes(uint32Len))
}

// randomBytes returns length random bytes.
func randomBytes(length int) []byte {
	random := make([]byte, length)
	// Read never fails: it fills the slice entirely or crashes the program
	// (crypto/rand, Go 1.24 and later), so its error is always nil.
	_, _ = rand.Read(random)
	return random
}
