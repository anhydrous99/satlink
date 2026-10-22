package sim

// xorShift is the xorshift64 generator with which the tests make arbitrary
// input that is the same on every run.
type xorShift struct {
	state uint64
}

func newXorShift(seed uint64) *xorShift {
	return &xorShift{state: seed}
}

// next returns the next number of the sequence.
func (x *xorShift) next() uint64 {
	x.state ^= x.state << 13
	x.state ^= x.state >> 7
	x.state ^= x.state << 17
	return x.state
}
