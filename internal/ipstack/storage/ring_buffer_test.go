package storage

import (
	"errors"
	"slices"
	"testing"
)

// newDottedRing returns an empty ring of capacity octets whose storage is all
// '.', so that a test sees which octets an operation wrote, as smoltcp's
// tests do with vec![b'.'; capacity].
func newDottedRing(capacity int) *RingBuffer[byte] {
	ring := NewRingBuffer[byte](capacity)
	for i := range ring.storage {
		ring.storage[i] = '.'
	}
	return ring
}

func assertStorage(t *testing.T, ring *RingBuffer[byte], want string) {
	t.Helper()
	if got := string(ring.storage); got != want {
		t.Errorf("storage = %q, want %q", got, want)
	}
}

func assertLen[T any](t *testing.T, ring *RingBuffer[T], want int) {
	t.Helper()
	if got := ring.Len(); got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

func assertBytes(t *testing.T, what string, got []byte, want string) {
	t.Helper()
	if string(got) != want {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func assertCount(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", what, got, want)
	}
}

// dequeueDotting dequeues count octets one at a time, overwriting each with
// '.', as smoltcp's tests do.
func dequeueDotting(t *testing.T, ring *RingBuffer[byte], count int) {
	t.Helper()
	for range count {
		octet, err := ring.DequeueOne()
		if err != nil {
			t.Fatalf("DequeueOne: %v", err)
		}
		*octet = '.'
	}
}

func failIfCalled(t *testing.T) func(*int) error {
	return func(*int) error {
		t.Error("callback called")
		return nil
	}
}

func mustEnqueueOneWith(t *testing.T, ring *RingBuffer[int], fill func(*int) error) {
	t.Helper()
	if err := ring.EnqueueOneWith(fill); err != nil {
		t.Fatalf("EnqueueOneWith: %v", err)
	}
}

func mustDequeueOneWith(t *testing.T, ring *RingBuffer[int]) int {
	t.Helper()
	var dequeued int
	if err := ring.DequeueOneWith(func(element *int) error { dequeued = *element; return nil }); err != nil {
		t.Fatalf("DequeueOneWith: %v", err)
	}
	return dequeued
}

func assertOneWithFails(t *testing.T, operation string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: %v, want %v", operation, err, want)
	}
}

// assertFillState checks IsEmpty and IsFull together.
func assertFillState[T any](t *testing.T, ring *RingBuffer[T], wantEmpty, wantFull bool) {
	t.Helper()
	if ring.IsEmpty() != wantEmpty || ring.IsFull() != wantFull {
		t.Errorf("IsEmpty %v, IsFull %v; want %v, %v", ring.IsEmpty(), ring.IsFull(), wantEmpty, wantFull)
	}
}

func TestRingBufferLengthChanges(t *testing.T) {
	t.Parallel()
	ring := NewRingBuffer[byte](2)
	cases := []struct {
		length     int
		wantEmpty  bool
		wantFull   bool
		wantWindow int
	}{
		{length: 0, wantEmpty: true, wantFull: false, wantWindow: 2},
		{length: 1, wantEmpty: false, wantFull: false, wantWindow: 1},
		{length: 2, wantEmpty: false, wantFull: true, wantWindow: 0},
	}
	for _, c := range cases {
		ring.length = c.length

		assertFillState(t, ring, c.wantEmpty, c.wantFull)
		assertLen(t, ring, c.length)
		assertCount(t, "Capacity()", ring.Capacity(), 2)
		assertCount(t, "Window()", ring.Window(), c.wantWindow)
	}
}

func TestRingBufferEnqueueDequeueOneWith(t *testing.T) {
	t.Parallel()
	ring := NewRingBuffer[int](5)
	assertOneWithFails(t, "DequeueOneWith on an empty ring", ring.DequeueOneWith(failIfCalled(t)), ErrEmpty)

	mustEnqueueOneWith(t, ring, func(*int) error { return nil })
	assertFillState(t, ring, false, false)
	for i := 1; i < 5; i++ {
		mustEnqueueOneWith(t, ring, func(element *int) error { *element = i; return nil })
		if ring.IsEmpty() {
			t.Errorf("empty after enqueuing %d", i)
		}
	}
	assertFillState(t, ring, false, true)
	assertOneWithFails(t, "EnqueueOneWith on a full ring", ring.EnqueueOneWith(failIfCalled(t)), ErrFull)

	for want := range 5 {
		assertCount(t, "dequeued element", mustDequeueOneWith(t, ring), want)
		if ring.IsFull() {
			t.Errorf("full after dequeuing %d", want)
		}
	}
	assertOneWithFails(t, "DequeueOneWith on an emptied ring", ring.DequeueOneWith(failIfCalled(t)), ErrEmpty)
	assertFillState(t, ring, true, false)
}

func TestRingBufferEnqueueDequeueOne(t *testing.T) {
	t.Parallel()
	ring := NewRingBuffer[int](5)
	_, err := ring.DequeueOne()
	assertOneWithFails(t, "DequeueOne on an empty ring", err, ErrEmpty)

	for i := range 5 {
		element, err := ring.EnqueueOne()
		if err != nil {
			t.Fatalf("EnqueueOne: %v", err)
		}
		*element = i
		if ring.IsEmpty() {
			t.Errorf("empty after enqueuing %d", i)
		}
	}
	assertFillState(t, ring, false, true)
	_, err = ring.EnqueueOne()
	assertOneWithFails(t, "EnqueueOne on a full ring", err, ErrFull)

	for want := range 5 {
		element, err := ring.DequeueOne()
		if err != nil {
			t.Fatalf("DequeueOne: %v", err)
		}
		assertCount(t, "dequeued element", *element, want)
		if ring.IsFull() {
			t.Errorf("full after dequeuing %d", want)
		}
	}
	_, err = ring.DequeueOne()
	assertOneWithFails(t, "DequeueOne on an emptied ring", err, ErrEmpty)
	assertFillState(t, ring, true, false)
}

// enqueueStep is one call of EnqueueManyWith in a test: the size of the free
// area it should be given, what the callback writes into it, and how many of
// those octets it enqueues.
type enqueueStep struct {
	wantFree int
	written  string
	count    int
}

// enqueueWith calls EnqueueManyWith as step says, checking the free area.
func enqueueWith(t *testing.T, ring *RingBuffer[byte], step enqueueStep) {
	t.Helper()
	enqueued := ring.EnqueueManyWith(func(free []byte) int {
		if len(free) != step.wantFree {
			t.Errorf("free area of %d octets, want %d", len(free), step.wantFree)
		}
		copy(free, step.written)
		return step.count
	})
	assertCount(t, "EnqueueManyWith", enqueued, step.count)
}

func TestRingBufferEnqueueManyWith(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(12)

	enqueueWith(t, ring, enqueueStep{wantFree: 12, written: "ab", count: 2})
	assertLen(t, ring, 2)
	assertStorage(t, ring, "ab..........")

	enqueueWith(t, ring, enqueueStep{wantFree: 12 - 2, written: "cdXX", count: 2})
	assertLen(t, ring, 4)
	assertStorage(t, ring, "abcdXX......")

	enqueueWith(t, ring, enqueueStep{wantFree: 12 - 4, written: "efgh", count: 4})
	assertLen(t, ring, 8)
	assertStorage(t, ring, "abcdefgh....")

	dequeueDotting(t, ring, 4)
	assertLen(t, ring, 4)
	assertStorage(t, ring, "....efgh....")

	enqueueWith(t, ring, enqueueStep{wantFree: 12 - 8, written: "ijkl", count: 4})
	assertLen(t, ring, 8)
	assertStorage(t, ring, "....efghijkl")

	enqueueWith(t, ring, enqueueStep{wantFree: 4, written: "abcd", count: 4})
	assertLen(t, ring, 12)
	assertStorage(t, ring, "abcdefghijkl")

	dequeueDotting(t, ring, 4)
	assertLen(t, ring, 8)
	assertStorage(t, ring, "abcd....ijkl")
}

func TestRingBufferEnqueueMany(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(12)

	copy(ring.EnqueueMany(8), "abcdefgh")
	assertLen(t, ring, 8)
	assertStorage(t, ring, "abcdefgh....")

	copy(ring.EnqueueMany(8), "ijkl")
	assertLen(t, ring, 12)
	assertStorage(t, ring, "abcdefghijkl")
}

func TestRingBufferEnqueueSlice(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(12)

	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("abcdefgh")), 8)
	assertLen(t, ring, 8)
	assertStorage(t, ring, "abcdefgh....")

	dequeueDotting(t, ring, 4)
	assertLen(t, ring, 4)
	assertStorage(t, ring, "....efgh....")

	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("ijklabcd")), 8)
	assertLen(t, ring, 12)
	assertStorage(t, ring, "abcdefghijkl")
}

// dequeueWith calls DequeueManyWith, checks the run it was given, dots out
// the first count octets of it, and dequeues them.
func dequeueWith(t *testing.T, ring *RingBuffer[byte], wantRun string, count int) {
	t.Helper()
	dequeued := ring.DequeueManyWith(func(allocated []byte) int {
		assertBytes(t, "allocated run", allocated, wantRun)
		copy(allocated[:count], "............")
		return count
	})
	assertCount(t, "DequeueManyWith", dequeued, count)
}

func TestRingBufferDequeueManyWith(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(12)
	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("abcdefghijkl")), 12)

	dequeueWith(t, ring, "abcdefghijkl", 4)
	assertLen(t, ring, 8)
	assertStorage(t, ring, "....efghijkl")

	dequeueWith(t, ring, "efghijkl", 4)
	assertLen(t, ring, 4)
	assertStorage(t, ring, "........ijkl")

	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("abcd")), 4)
	assertLen(t, ring, 8)

	dequeueWith(t, ring, "ijkl", 4)
	dequeueWith(t, ring, "abcd", 4)
	assertLen(t, ring, 0)
	assertStorage(t, ring, "............")
}

func TestRingBufferDequeueMany(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(12)
	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("abcdefghijkl")), 12)

	dequeued := ring.DequeueMany(8)
	assertBytes(t, "DequeueMany(8)", dequeued, "abcdefgh")
	copy(dequeued, "........")
	assertLen(t, ring, 4)
	assertStorage(t, ring, "........ijkl")

	dequeued = ring.DequeueMany(8)
	assertBytes(t, "DequeueMany(8)", dequeued, "ijkl")
	copy(dequeued, "....")
	assertLen(t, ring, 0)
	assertStorage(t, ring, "............")
}

func TestRingBufferDequeueSlice(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(12)
	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("abcdefghijkl")), 12)

	data := make([]byte, 8)
	assertCount(t, "DequeueSlice", ring.DequeueSlice(data), 8)
	assertBytes(t, "dequeued", data, "abcdefgh")
	assertLen(t, ring, 4)

	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("abcd")), 4)

	data = make([]byte, 8)
	assertCount(t, "DequeueSlice", ring.DequeueSlice(data), 8)
	assertBytes(t, "dequeued", data, "ijklabcd")
	assertLen(t, ring, 0)
}

// Ported from smoltcp's test_buffer_get_unallocated.
func TestRingBufferUnallocated(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(12)

	assertBytes(t, "Unallocated(16, 4)", ring.Unallocated(16, 4), "")

	copy(ring.Unallocated(0, 4), "abcd")
	assertStorage(t, ring, "abcd........")

	assertCount(t, "EnqueueMany(4)", len(ring.EnqueueMany(4)), 4)
	assertLen(t, ring, 4)

	copy(ring.Unallocated(4, 8), "ijkl")
	assertStorage(t, ring, "abcd....ijkl")

	copy(ring.EnqueueMany(8), "EFGHIJKL")
	copy(ring.DequeueMany(4), "abcd")
	assertLen(t, ring, 8)
	assertStorage(t, ring, "abcdEFGHIJKL")

	copy(ring.Unallocated(0, 8), "ABCD")
	assertStorage(t, ring, "ABCDEFGHIJKL")
}

func TestRingBufferWriteUnallocated(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(12)
	copy(ring.EnqueueMany(6), "abcdef")
	copy(ring.DequeueMany(6), "ABCDEF")

	assertCount(t, "WriteUnallocated(0)", ring.WriteUnallocated(0, []byte("ghi")), 3)
	assertBytes(t, "Unallocated(0, 3)", ring.Unallocated(0, 3), "ghi")

	assertCount(t, "WriteUnallocated(3)", ring.WriteUnallocated(3, []byte("jklmno")), 6)
	assertBytes(t, "Unallocated(3, 3)", ring.Unallocated(3, 3), "jkl")

	assertCount(t, "WriteUnallocated(9)", ring.WriteUnallocated(9, []byte("pqrstu")), 3)
	assertBytes(t, "Unallocated(9, 3)", ring.Unallocated(9, 3), "pqr")
}

// Ported from smoltcp's test_buffer_get_allocated.
func TestRingBufferAllocated(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(12)

	assertBytes(t, "Allocated(16, 4)", ring.Allocated(16, 4), "")
	assertBytes(t, "Allocated(0, 4)", ring.Allocated(0, 4), "")

	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("abcd")), 4)
	assertBytes(t, "Allocated(0, 8)", ring.Allocated(0, 8), "abcd")

	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("efghijkl")), 8)
	copy(ring.DequeueMany(4), "....")
	assertBytes(t, "Allocated(4, 8)", ring.Allocated(4, 8), "ijkl")

	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("abcd")), 4)
	assertBytes(t, "Allocated(4, 8)", ring.Allocated(4, 8), "ijkl")
}

func TestRingBufferReadAllocated(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(12)
	copy(ring.EnqueueMany(12), "abcdefghijkl")

	data := make([]byte, 6)
	assertCount(t, "ReadAllocated(0)", ring.ReadAllocated(0, data), 6)
	assertBytes(t, "read", data, "abcdef")

	copy(ring.DequeueMany(6), "ABCDEF")
	copy(ring.EnqueueMany(3), "mno")

	data = make([]byte, 6)
	assertCount(t, "ReadAllocated(3)", ring.ReadAllocated(3, data), 6)
	assertBytes(t, "read", data, "jklmno")

	data = make([]byte, 6)
	assertCount(t, "ReadAllocated(6)", ring.ReadAllocated(6, data), 3)
	assertBytes(t, "read", data, "mno\x00\x00\x00")
}

func TestRingBufferWithNoCapacity(t *testing.T) {
	t.Parallel()
	noCapacity := NewRingBuffer[byte](0)

	// Every method that computes an index modulo the capacity, with a
	// capacity of 0.
	assertBytes(t, "Unallocated(0, 0)", noCapacity.Unallocated(0, 0), "")
	assertBytes(t, "Allocated(0, 0)", noCapacity.Allocated(0, 0), "")
	noCapacity.DequeueAllocated(0)
	assertBytes(t, "EnqueueMany(0)", noCapacity.EnqueueMany(0), "")
	if _, err := noCapacity.EnqueueOne(); !errors.Is(err, ErrFull) {
		t.Errorf("EnqueueOne: %v, want ErrFull", err)
	}
	assertCount(t, "ContiguousWindow()", noCapacity.ContiguousWindow(), 0)
}

// Use the buffer a bit, then empty it and enqueue as much as it holds: seeing
// that it is empty, the buffer starts over at the front of its storage.
func TestRingBufferWriteWholly(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(8)
	copy(ring.EnqueueMany(2), "ab")
	copy(ring.EnqueueMany(2), "cd")
	assertLen(t, ring, 4)
	assertBytes(t, "DequeueMany(4)", ring.DequeueMany(4), "abcd")
	assertLen(t, ring, 0)

	assertCount(t, "EnqueueMany(8)", len(ring.EnqueueMany(8)), 8)
}

func TestRingBufferZeroValueHasNoCapacity(t *testing.T) {
	t.Parallel()
	var ring RingBuffer[byte]

	if !ring.IsEmpty() || !ring.IsFull() || ring.Capacity() != 0 {
		t.Errorf("zero ring: empty %v, full %v, capacity %d", ring.IsEmpty(), ring.IsFull(), ring.Capacity())
	}
	assertCount(t, "EnqueueSlice", ring.EnqueueSlice([]byte("a")), 0)
}

func TestRingBufferOneWithKeepsTheElementWhenTheCallbackFails(t *testing.T) {
	t.Parallel()
	refused := errors.New("refused")
	ring := NewRingBuffer[int](2)

	if err := ring.EnqueueOneWith(func(*int) error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("EnqueueOneWith: %v, want the callback's error", err)
	}
	assertLen(t, ring, 0)

	element, _ := ring.EnqueueOne()
	*element = 7
	if err := ring.DequeueOneWith(func(*int) error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("DequeueOneWith: %v, want the callback's error", err)
	}
	assertLen(t, ring, 1)
	if got, _ := ring.DequeueOne(); *got != 7 {
		t.Errorf("dequeued %d, want 7", *got)
	}
}

func TestRingBufferSlicesCannotGrowIntoTheStorage(t *testing.T) {
	t.Parallel()
	ring := newDottedRing(8)
	copy(ring.EnqueueMany(4), "abcd")

	allocated := ring.Allocated(0, 2)
	_ = append(allocated, 'X')
	free := ring.Unallocated(0, 2)
	_ = append(free, 'Y')

	assertStorage(t, ring, "abcd....")
}

// A TCP socket reassembles segments into the free area and reads
// retransmissions from the allocated one at offsets that come from the
// network. The offset before an area's end gets its last element; the end,
// and any offset past it, gets nothing and must not panic.
func TestRingBufferRandomAccessGetsNothingPastTheEndOfItsArea(t *testing.T) {
	t.Parallel()
	window := func(ring *RingBuffer[byte]) int { return ring.Window() }
	length := func(ring *RingBuffer[byte]) int { return ring.Len() }
	tests := []struct {
		name   string
		access func(ring *RingBuffer[byte], offset int) int
		// end returns the offset where the area accessed ends.
		end func(ring *RingBuffer[byte]) int
	}{
		{"Unallocated", func(ring *RingBuffer[byte], offset int) int { return len(ring.Unallocated(offset, 4)) }, window},
		{"WriteUnallocated", func(ring *RingBuffer[byte], offset int) int { return ring.WriteUnallocated(offset, []byte("wxyz")) }, window},
		{"Allocated", func(ring *RingBuffer[byte], offset int) int { return len(ring.Allocated(offset, 4)) }, length},
		{"ReadAllocated", func(ring *RingBuffer[byte], offset int) int { return ring.ReadAllocated(offset, make([]byte, 4)) }, length},
	}
	for _, test := range tests {
		for _, step := range []struct {
			pastTheEnd, want int
		}{{-1, 1}, {0, 0}, {1, 0}, {16, 0}} {
			// The ring holds "ghij" from index 6; the 8 free elements after
			// it wrap around the end of the storage.
			ring := newDottedRing(12)
			copy(ring.EnqueueMany(10), "abcdefghij")
			ring.DequeueMany(6)
			offset := test.end(ring) + step.pastTheEnd

			if got := test.access(ring, offset); got != step.want {
				t.Errorf("%s at %d, %d past the end of its area: %d elements, want %d", test.name, offset, step.pastTheEnd, got, step.want)
			}
		}
	}
}

func TestRingBufferPanicsOnOutOfRangeCounts(t *testing.T) {
	t.Parallel()
	cases := map[string]func(ring *RingBuffer[byte]){
		"EnqueueManyWith returning too many": func(ring *RingBuffer[byte]) {
			ring.EnqueueManyWith(func(free []byte) int { return len(free) + 1 })
		},
		"EnqueueManyWith returning a negative count": func(ring *RingBuffer[byte]) {
			ring.EnqueueManyWith(func([]byte) int { return -1 })
		},
		"DequeueManyWith returning too many": func(ring *RingBuffer[byte]) {
			ring.DequeueManyWith(func(allocated []byte) int { return len(allocated) + 1 })
		},
		"EnqueueUnallocated beyond the window": func(ring *RingBuffer[byte]) {
			ring.EnqueueUnallocated(ring.Window() + 1)
		},
		"DequeueAllocated beyond the length": func(ring *RingBuffer[byte]) {
			ring.DequeueAllocated(ring.Len() + 1)
		},
	}
	for name, operation := range cases {
		t.Run(name, func(t *testing.T) {
			ring := newDottedRing(4)
			copy(ring.EnqueueMany(2), "ab")
			before := slices.Clone(ring.storage)
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
				if !slices.Equal(ring.storage, before) || ring.Len() != 2 {
					t.Errorf("the ring changed: %q, length %d", ring.storage, ring.Len())
				}
			}()
			operation(ring)
		})
	}
}
