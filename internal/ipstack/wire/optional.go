package wire

// Optional holds a value that may be absent, for header fields whose zero
// value is meaningful: a sequence number, a window scale or an MSS of 0 are
// all valid on the wire, so "absent" needs its own state. It is Rust's Option
// for the wire Reprs.
//
// The zero Optional is absent. Optional values compare with == when T does.
type Optional[T comparable] struct {
	value   T
	present bool
}

// Some returns an Optional holding value.
func Some[T comparable](value T) Optional[T] {
	return Optional[T]{value: value, present: true}
}

// Get returns the value and whether it is present; the value is T's zero
// value when it is absent.
func (o Optional[T]) Get() (value T, present bool) {
	return o.value, o.present
}

// IsSome reports whether a value is present.
func (o Optional[T]) IsSome() bool {
	return o.present
}

// Or returns the value if it is present, and fallback otherwise.
func (o Optional[T]) Or(fallback T) T {
	if o.present {
		return o.value
	}
	return fallback
}
