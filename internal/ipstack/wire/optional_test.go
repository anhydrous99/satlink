package wire

import "testing"

func TestOptionalZeroValueIsAbsent(t *testing.T) {
	t.Parallel()
	var absent Optional[uint16]

	value, present := absent.Get()

	checkFields(t, []field{
		{"Get value", value, uint16(0)},
		{"Get present", present, false},
		{"IsSome", absent.IsSome(), false},
		{"Or", absent.Or(536), uint16(536)},
	})
}

func TestOptionalHoldsAZeroValue(t *testing.T) {
	t.Parallel()
	scale := Some[uint8](0)

	value, present := scale.Get()

	checkFields(t, []field{
		{"Get value", value, uint8(0)},
		{"Get present", present, true},
		{"IsSome", scale.IsSome(), true},
		{"Or", scale.Or(14), uint8(0)},
	})
}

func TestOptionalComparesWithEquals(t *testing.T) {
	t.Parallel()
	one, anotherOne, two, zero := Some(1), Some(1), Some(2), Some(0)
	var absent, anotherAbsent Optional[int]
	block, sameBlock := Some(SACKRange{Left: 1, Right: 2}), Some(SACKRange{Left: 1, Right: 2})

	checkFields(t, []field{
		{"Some(1) == Some(1)", one == anotherOne, true},
		{"Some(1) == Some(2)", one == two, false},
		{"Some(0) == absent", zero == absent, false},
		{"absent == absent", absent == anotherAbsent, true},
		{"Some(block) == Some(block)", block == sameBlock, true},
	})
}
