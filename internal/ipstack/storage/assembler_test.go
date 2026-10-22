package storage

import (
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
)

// assemblerOf builds an assembler from (hole, data) pairs.
func assemblerOf(contigs ...contig) Assembler {
	var assembler Assembler
	copy(assembler.contigs[:], contigs)
	return assembler
}

func assertAssembler(t *testing.T, got Assembler, want ...contig) {
	t.Helper()
	if expected := assemblerOf(want...); got != expected {
		t.Errorf("assembler = %v, want %v", got.contigs, expected.contigs)
	}
}

func mustAdd(t *testing.T, assembler *Assembler, offset, size int) {
	t.Helper()
	if err := assembler.Add(offset, size); err != nil {
		t.Fatalf("Add(%d, %d): %v", offset, size, err)
	}
}

// addToEmpty adds one run to a new assembler.
func addToEmpty(t *testing.T, offset, size int) Assembler {
	t.Helper()
	var assembler Assembler
	mustAdd(t, &assembler, offset, size)
	return assembler
}

// addTo adds one run to the assembler made of contigs.
func addTo(t *testing.T, contigs []contig, offset, size int) Assembler {
	t.Helper()
	assembler := assemblerOf(contigs...)
	mustAdd(t, &assembler, offset, size)
	return assembler
}

// fullAssembler records AssemblerMaxSegmentCount runs of 3 octets, at 10, 20,
// 30…, so that no further hole fits.
func fullAssembler(t *testing.T) Assembler {
	t.Helper()
	var assembler Assembler
	for run := 1; run <= AssemblerMaxSegmentCount; run++ {
		mustAdd(t, &assembler, run*10, 3)
	}
	return assembler
}

func collectRanges(assembler *Assembler) [][2]int {
	var ranges [][2]int
	for start, end := range assembler.DataRanges() {
		ranges = append(ranges, [2]int{start, end})
	}
	return ranges
}

func assertRanges(t *testing.T, assembler Assembler, want ...[2]int) {
	t.Helper()
	if got := collectRanges(&assembler); !slices.Equal(got, want) {
		t.Errorf("DataRanges() = %v, want %v", got, want)
	}
}

func TestAssemblerNew(t *testing.T) {
	t.Parallel()
	var assembler Assembler

	assertAssembler(t, assembler)
}

func TestAssemblerAddToEmpty(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		offset, size int
		want         contig
	}{
		"empty_add_full":  {offset: 0, size: 16, want: contig{0, 16}},
		"empty_add_front": {offset: 0, size: 4, want: contig{0, 4}},
		"empty_add_back":  {offset: 12, size: 4, want: contig{12, 4}},
		"empty_add_mid":   {offset: 4, size: 8, want: contig{4, 8}},
	}
	for caseName, c := range cases {
		t.Run(caseName, func(t *testing.T) {
			t.Parallel()
			assertAssembler(t, addToEmpty(t, c.offset, c.size), c.want)
		})
	}
}

func TestAssemblerAddToPartial(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		offset, size int
		want         contig
	}{
		"partial_add_front":               {offset: 0, size: 4, want: contig{0, 12}},
		"partial_add_back":                {offset: 12, size: 4, want: contig{4, 12}},
		"partial_add_front_overlap":       {offset: 0, size: 8, want: contig{0, 12}},
		"partial_add_front_overlap_split": {offset: 2, size: 6, want: contig{2, 10}},
		"partial_add_back_overlap":        {offset: 8, size: 8, want: contig{4, 12}},
		"partial_add_back_overlap_split":  {offset: 10, size: 4, want: contig{4, 10}},
		"partial_add_both_overlap":        {offset: 0, size: 16, want: contig{0, 16}},
		"partial_add_both_overlap_split":  {offset: 2, size: 12, want: contig{2, 12}},
	}
	for caseName, c := range cases {
		t.Run(caseName, func(t *testing.T) {
			t.Parallel()
			assertAssembler(t, addTo(t, []contig{{4, 8}}, c.offset, c.size), c.want)
		})
	}
}

func TestAssemblerRejectedAddKeepsState(t *testing.T) {
	t.Parallel()
	assembler := fullAssembler(t)
	before := assembler

	err := assembler.Add(1, 3)

	if !errors.Is(err, ErrTooManyHoles) {
		t.Errorf("Add: %v, want ErrTooManyHoles", err)
	}
	if assembler != before {
		t.Errorf("assembler changed to %v", assembler.contigs)
	}
}

func TestAssemblerEmptyRemoveFront(t *testing.T) {
	t.Parallel()
	var assembler Assembler

	assertCount(t, "RemoveFront()", assembler.RemoveFront(), 0)
}

func TestAssemblerTrailingHoleRemoveFront(t *testing.T) {
	t.Parallel()
	assembler := assemblerOf(contig{0, 4})

	assertCount(t, "RemoveFront()", assembler.RemoveFront(), 4)
	assertAssembler(t, assembler)
}

func TestAssemblerTrailingDataRemoveFront(t *testing.T) {
	t.Parallel()
	assembler := assemblerOf(contig{0, 4}, contig{4, 4})

	assertCount(t, "RemoveFront()", assembler.RemoveFront(), 4)
	assertAssembler(t, assembler, contig{4, 4})
}

func TestAssemblerBoundaryCaseRemoveFront(t *testing.T) {
	t.Parallel()
	var assembler Assembler
	for i := range assembler.contigs {
		assembler.contigs[i] = contig{1, 1}
	}
	assembler.contigs[0] = contig{0, 2}

	assertCount(t, "RemoveFront()", assembler.RemoveFront(), 2)

	var want Assembler
	for i := range want.contigs[:AssemblerMaxSegmentCount-1] {
		want.contigs[i] = contig{1, 1}
	}
	if assembler != want {
		t.Errorf("assembler = %v, want %v", assembler.contigs, want.contigs)
	}
}

func TestAssemblerShrinkNextHole(t *testing.T) {
	t.Parallel()
	var assembler Assembler
	mustAdd(t, &assembler, 100, 10)
	mustAdd(t, &assembler, 50, 10)
	mustAdd(t, &assembler, 40, 30)

	assertAssembler(t, assembler, contig{40, 30}, contig{30, 10})
}

func TestAssemblerJoinTwo(t *testing.T) {
	t.Parallel()
	var assembler Assembler
	mustAdd(t, &assembler, 10, 10)
	mustAdd(t, &assembler, 50, 10)
	mustAdd(t, &assembler, 15, 40)

	assertAssembler(t, assembler, contig{10, 50})
}

func TestAssemblerJoinTwoReversed(t *testing.T) {
	t.Parallel()
	var assembler Assembler
	mustAdd(t, &assembler, 50, 10)
	mustAdd(t, &assembler, 10, 10)
	mustAdd(t, &assembler, 15, 40)

	assertAssembler(t, assembler, contig{10, 50})
}

func TestAssemblerJoinTwoOverlong(t *testing.T) {
	t.Parallel()
	var assembler Assembler
	mustAdd(t, &assembler, 50, 10)
	mustAdd(t, &assembler, 10, 10)
	mustAdd(t, &assembler, 15, 60)

	assertAssembler(t, assembler, contig{10, 65})
}

func TestAssemblerIterEmpty(t *testing.T) {
	t.Parallel()
	var assembler Assembler

	assertRanges(t, assembler)
}

func TestAssemblerIterFull(t *testing.T) {
	t.Parallel()
	assertRanges(t, addToEmpty(t, 0, 16), [2]int{0, 16})
}

func TestAssemblerIterOneFront(t *testing.T) {
	t.Parallel()
	assertRanges(t, addToEmpty(t, 0, 4), [2]int{0, 4})
}

func TestAssemblerIterOneBack(t *testing.T) {
	t.Parallel()
	assertRanges(t, addToEmpty(t, 12, 4), [2]int{12, 16})
}

func TestAssemblerIterOneMid(t *testing.T) {
	t.Parallel()
	assertRanges(t, addToEmpty(t, 4, 8), [2]int{4, 12})
}

func TestAssemblerIterOneTrailingGap(t *testing.T) {
	t.Parallel()
	assertRanges(t, assemblerOf(contig{4, 8}), [2]int{4, 12})
}

func TestAssemblerIterTwoSplit(t *testing.T) {
	t.Parallel()
	assertRanges(t, assemblerOf(contig{2, 6}, contig{4, 1}), [2]int{2, 8}, [2]int{12, 13})
}

func TestAssemblerIterThreeSplit(t *testing.T) {
	t.Parallel()
	assertRanges(t, assemblerOf(contig{2, 6}, contig{2, 1}, contig{2, 2}),
		[2]int{2, 8}, [2]int{10, 11}, [2]int{13, 15})
}

// A run that fills the hole between two others.
func TestAssemblerFillsTheHoleBetweenTwoRuns(t *testing.T) {
	t.Parallel()
	var assembler Assembler
	mustAdd(t, &assembler, 0, 1)
	mustAdd(t, &assembler, 2, 1)
	mustAdd(t, &assembler, 1, 1)
}

func TestAssemblerAddThenRemoveFront(t *testing.T) {
	t.Parallel()
	assembler := addToEmpty(t, 50, 10)

	removed, err := assembler.AddThenRemoveFront(10, 10)

	if err != nil || removed != 0 {
		t.Errorf("AddThenRemoveFront = %d, %v; want 0, nil", removed, err)
	}
	assertAssembler(t, assembler, contig{10, 10}, contig{30, 10})
}

func TestAssemblerAddThenRemoveFrontAtFront(t *testing.T) {
	t.Parallel()
	assembler := addToEmpty(t, 50, 10)

	removed, err := assembler.AddThenRemoveFront(0, 10)

	if err != nil || removed != 10 {
		t.Errorf("AddThenRemoveFront = %d, %v; want 10, nil", removed, err)
	}
	assertAssembler(t, assembler, contig{40, 10})
}

func TestAssemblerAddThenRemoveFrontAtFrontTouch(t *testing.T) {
	t.Parallel()
	assembler := addToEmpty(t, 50, 10)

	removed, err := assembler.AddThenRemoveFront(0, 50)

	if err != nil || removed != 60 {
		t.Errorf("AddThenRemoveFront = %d, %v; want 60, nil", removed, err)
	}
	assertAssembler(t, assembler)
}

func TestAssemblerAddThenRemoveFrontAtFrontFull(t *testing.T) {
	t.Parallel()
	assembler := fullAssembler(t)
	before := assembler

	_, err := assembler.AddThenRemoveFront(1, 3)

	if !errors.Is(err, ErrTooManyHoles) {
		t.Errorf("AddThenRemoveFront: %v, want ErrTooManyHoles", err)
	}
	if assembler != before {
		t.Errorf("assembler changed to %v", assembler.contigs)
	}
}

func TestAssemblerAddThenRemoveFrontAtFrontFullOffset0(t *testing.T) {
	t.Parallel()
	assembler := fullAssembler(t)

	removed, err := assembler.AddThenRemoveFront(0, 3)

	if err != nil || removed != 3 {
		t.Errorf("AddThenRemoveFront = %d, %v; want 3, nil", removed, err)
	}
}

// AddThenRemoveFront takes data at offset 0 even when the assembler is full,
// on both sides of the first hole's end: 9 octets only shrink the 10-octet
// hole, and 10 reach the first run.
func TestAssemblerAddThenRemoveFrontNeverRefusesDataAtOffsetZero(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		size        int
		wantRemoved int
		wantLeft    []contig
	}{
		"one octet":                  {size: 1, wantRemoved: 1, wantLeft: []contig{{9, 3}, {7, 3}, {7, 3}, {7, 3}}},
		"three octets":               {size: 3, wantRemoved: 3, wantLeft: []contig{{7, 3}, {7, 3}, {7, 3}, {7, 3}}},
		"one octet short of the run": {size: 9, wantRemoved: 9, wantLeft: []contig{{1, 3}, {7, 3}, {7, 3}, {7, 3}}},
		"up to the run":              {size: 10, wantRemoved: 13, wantLeft: []contig{{7, 3}, {7, 3}, {7, 3}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assembler := fullAssembler(t)

			removed, err := assembler.AddThenRemoveFront(0, c.size)

			if err != nil || removed != c.wantRemoved {
				t.Errorf("AddThenRemoveFront(0, %d) = %d, %v; want %d, nil", c.size, removed, err, c.wantRemoved)
			}
			assertAssembler(t, assembler, c.wantLeft...)
		})
	}
}

// bitmapContigs returns the contigs an assembler should hold for the octets
// marked in filled, which must end with an unfilled octet.
func bitmapContigs(filled []bool) []contig {
	var contigs []contig
	var current contig
	for _, isFilled := range filled {
		switch {
		case isFilled:
			current.dataSize++
		case current.hasData():
			contigs = append(contigs, current)
			current = contig{holeSize: 1}
		default:
			current.holeSize++
		}
	}
	return contigs
}

// Random additions, checked against an obviously correct bitmap. The seed is
// fixed, so that the test repeats.
func TestAssemblerRandom(t *testing.T) {
	t.Parallel()
	const bitmapSize = 256
	const addsPerRound = 60
	const rounds = 300
	random := rand.New(rand.NewPCG(694, 0x5EED))

	for _, maxSize := range []int{2, 5, 10, 100} {
		for range rounds {
			var assembler Assembler
			var filled [bitmapSize]bool
			for range addsPerRound {
				offset := random.IntN(bitmapSize - maxSize - 1)
				size := 1 + random.IntN(maxSize)

				err := assembler.Add(offset, size)

				candidate := filled
				for i := offset; i < offset+size; i++ {
					candidate[i] = true
				}
				contigs := bitmapContigs(candidate[:])
				wantError := len(contigs) > AssemblerMaxSegmentCount
				if wantError != errors.Is(err, ErrTooManyHoles) {
					t.Fatalf("Add(%d, %d) to %v: %v, want an error: %v", offset, size, assembler.contigs, err, wantError)
				}
				if err == nil {
					filled = candidate
					assertAssembler(t, assembler, contigs...)
				}
			}
		}
	}
}

func TestAssemblerString(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		assembler Assembler
		want      string
	}{
		"empty":            {assembler: Assembler{}, want: "[ ]"},
		"data then a hole": {assembler: assemblerOf(contig{0, 100}, contig{200, 100}), want: "[ 100 (200) 100 ]"},
		"leading hole":     {assembler: assemblerOf(contig{4, 8}), want: "[ (4) 8 ]"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := c.assembler.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAssemblerPeekFrontAndIsEmpty(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		assembler     Assembler
		wantPeekFront int
		wantEmpty     bool
	}{
		"empty":           {assembler: Assembler{}, wantPeekFront: 0, wantEmpty: true},
		"data at front":   {assembler: assemblerOf(contig{0, 4}), wantPeekFront: 4, wantEmpty: false},
		"hole at front":   {assembler: assemblerOf(contig{2, 4}), wantPeekFront: 0, wantEmpty: false},
		"front then more": {assembler: assemblerOf(contig{0, 4}, contig{2, 3}), wantPeekFront: 4, wantEmpty: false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertCount(t, "PeekFront()", c.assembler.PeekFront(), c.wantPeekFront)
			if got := c.assembler.IsEmpty(); got != c.wantEmpty {
				t.Errorf("IsEmpty() = %v, want %v", got, c.wantEmpty)
			}
		})
	}
}

func TestAssemblerAddOfNothingChangesNothing(t *testing.T) {
	t.Parallel()
	assembler := fullAssembler(t)
	before := assembler

	if err := assembler.Add(1, 0); err != nil || assembler != before {
		t.Errorf("Add(1, 0) = %v and changed the assembler to %v", err, assembler.contigs)
	}
}

func TestAssemblerClear(t *testing.T) {
	t.Parallel()
	assembler := fullAssembler(t)

	assembler.Clear()

	assertAssembler(t, assembler)
}

func TestAssemblerDataRangesStopsWhenAsked(t *testing.T) {
	t.Parallel()
	assembler := assemblerOf(contig{2, 6}, contig{4, 1})

	var starts []int
	for start := range assembler.DataRanges() {
		starts = append(starts, start)
		break
	}

	if !slices.Equal(starts, []int{2}) {
		t.Errorf("visited %v, want only the first range", starts)
	}
}
