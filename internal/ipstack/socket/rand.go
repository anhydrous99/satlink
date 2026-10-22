package socket

// Rand is the pseudo-random generator behind TCP initial sequence numbers and
// DNS query ids and source ports: smoltcp's sPCG32. The interface's random
// seed makes its values differ between runs, but it is not
// cryptographically secure, and its values are predictable: every peer of a
// TCP connection sees its initial sequence number, a few of those reveal the
// generator's state, and the state gives every later value, the next DNS
// query's transaction id and source port included.
//
// Get one from Context.Rand. The zero Rand is the generator seeded with 0.
type Rand struct {
	state uint64
}

// The constants of sPCG32 (https://www.pcg-random.org/paper.html; see also
// https://nullprogram.com/blog/2017/09/21/), as smoltcp has them.
const (
	// pcgMultiplier and pcgIncrement advance the 64-bit state, a linear
	// congruential generator.
	pcgMultiplier = 0xbb2efcec3c39611d
	pcgIncrement  = 0x7590ef39
	// pcgShiftSelector picks the top three bits of the state, which choose
	// how far the state is shifted before its low 32 bits are output.
	pcgShiftSelector = 61
	// pcgMaxShift is the largest of those shifts: 64 bits of state, less the
	// 32 output bits and the 3 selector bits.
	pcgMaxShift = 29
)

// highestExcludedPort is the largest port SourcePort never returns: the
// well-known ports below 1024, where servers listen, and 1024 itself, as
// smoltcp has it.
const highestExcludedPort = 1024

// Uint32 returns the next 32 random bits.
func (r *Rand) Uint32() uint32 {
	r.state = r.state*pcgMultiplier + pcgIncrement
	shift := pcgMaxShift - r.state>>pcgShiftSelector
	return uint32(r.state >> shift)
}

// Uint16 returns the next 16 random bits, folded from Uint32 (its high half
// XORed into its low half).
func (r *Rand) Uint16() uint16 {
	bits := r.Uint32()
	return uint16(bits ^ bits>>16)
}

// SourcePort returns a random port above 1024, for a socket that needs one
// of its own. It draws Uint16 values until one is above 1024.
func (r *Rand) SourcePort() uint16 {
	for {
		if port := r.Uint16(); port > highestExcludedPort {
			return port
		}
	}
}
