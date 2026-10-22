package storage

import "fmt"

// RingBuffer is a fixed-capacity FIFO queue of elements, used both as a
// simple queue and, through its random-access methods, as a TCP reassembly
// buffer.
//
// It offers three interfaces: one element at a time, where an empty or full
// buffer is an error; contiguous slices, where it simply yields shorter
// slices; and random access to the allocated and unallocated areas. Methods
// that hand out slices let callers work in place; the slices share the
// buffer's storage and are valid only until the buffer is next changed.
// Every offset, size and count is a number of elements, never negative.
//
// The zero RingBuffer has no capacity: every enqueue fails.
type RingBuffer[T any] struct {
	storage []T
	// readAt is the index of the oldest element in storage.
	readAt int
	// length is how many elements are queued, from readAt on, wrapping
	// around the end of storage.
	length int
}

// NewRingBuffer returns an empty ring buffer that holds up to capacity
// elements. It panics if capacity is negative.
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	return &RingBuffer[T]{storage: make([]T, capacity)}
}

// Clear empties the buffer.
func (r *RingBuffer[T]) Clear() {
	r.readAt = 0
	r.length = 0
}

// Capacity returns the most elements the buffer holds.
func (r *RingBuffer[T]) Capacity() int {
	return len(r.storage)
}

// Len returns how many elements the buffer holds.
func (r *RingBuffer[T]) Len() int {
	return r.length
}

// Window returns how many elements can still be added.
func (r *RingBuffer[T]) Window() int {
	return r.Capacity() - r.length
}

// ContiguousWindow returns how many elements can be added without wrapping
// around the end of the storage, that is, in a single EnqueueManyWith call.
func (r *RingBuffer[T]) ContiguousWindow() int {
	return min(r.Window(), r.Capacity()-r.index(r.length))
}

// IsEmpty reports whether the buffer holds no elements.
func (r *RingBuffer[T]) IsEmpty() bool {
	return r.length == 0
}

// IsFull reports whether the buffer has no room left.
func (r *RingBuffer[T]) IsFull() bool {
	return r.Window() == 0
}

// EnqueueOneWith calls fill with the next free element and enqueues it if
// fill returns nil. It returns ErrFull, without calling fill, if the buffer
// is full, and fill's error otherwise.
func (r *RingBuffer[T]) EnqueueOneWith(fill func(element *T) error) error {
	if r.IsFull() {
		return ErrFull
	}
	if err := fill(&r.storage[r.index(r.length)]); err != nil {
		return err
	}
	r.length++
	return nil
}

// EnqueueOne enqueues the next free element and returns it for the caller
// to fill, or returns ErrFull.
func (r *RingBuffer[T]) EnqueueOne() (*T, error) {
	var enqueued *T
	err := r.EnqueueOneWith(func(element *T) error {
		enqueued = element
		return nil
	})
	return enqueued, err
}

// DequeueOneWith calls take with the oldest element and dequeues it if take
// returns nil. It returns ErrEmpty, without calling take, if the buffer is
// empty, and take's error otherwise.
func (r *RingBuffer[T]) DequeueOneWith(take func(element *T) error) error {
	if r.IsEmpty() {
		return ErrEmpty
	}
	if err := take(&r.storage[r.readAt]); err != nil {
		return err
	}
	r.readAt = r.index(1)
	r.length--
	return nil
}

// DequeueOne dequeues the oldest element and returns it, or returns
// ErrEmpty.
func (r *RingBuffer[T]) DequeueOne() (*T, error) {
	var dequeued *T
	err := r.DequeueOneWith(func(element *T) error {
		dequeued = element
		return nil
	})
	return dequeued, err
}

// EnqueueManyWith calls fill with the largest contiguous free area and
// enqueues as many elements as fill returns, which it also returns. It
// panics if fill returns more than it was given.
func (r *RingBuffer[T]) EnqueueManyWith(fill func(free []T) int) int {
	if r.length == 0 {
		// An empty ring starts over at the front of its storage, where the
		// free area is the largest possible.
		r.readAt = 0
	}
	free := r.area(r.index(r.length), r.ContiguousWindow())
	count := checkedCount("EnqueueManyWith", fill(free), len(free))
	r.length += count
	return count
}

// EnqueueMany enqueues up to size elements of the contiguous free area and
// returns them for the caller to fill; the slice is shorter than size when
// the free area wraps around or is too small.
func (r *RingBuffer[T]) EnqueueMany(size int) []T {
	var enqueued []T
	r.EnqueueManyWith(func(free []T) int {
		enqueued = free[:min(size, len(free))]
		return len(enqueued)
	})
	return enqueued
}

// EnqueueSlice enqueues as much of data as fits and returns how many
// elements that was.
func (r *RingBuffer[T]) EnqueueSlice(data []T) int {
	// The free space may wrap around the end of the storage: fill the part
	// up to the end first, then the part at the front.
	enqueued := r.enqueueContiguous(data)
	return enqueued + r.enqueueContiguous(data[enqueued:])
}

// DequeueManyWith calls take with the largest contiguous run of allocated
// elements and dequeues as many as take returns, which it also returns. It
// panics if take returns more than it was given.
func (r *RingBuffer[T]) DequeueManyWith(take func(allocated []T) int) int {
	allocated := r.area(r.readAt, min(r.length, r.Capacity()-r.readAt))
	count := checkedCount("DequeueManyWith", take(allocated), len(allocated))
	r.readAt = r.index(count)
	r.length -= count
	return count
}

// DequeueMany dequeues up to size elements of the contiguous allocated run
// and returns them; the slice is shorter than size when the run wraps around
// or is too short.
func (r *RingBuffer[T]) DequeueMany(size int) []T {
	var dequeued []T
	r.DequeueManyWith(func(allocated []T) int {
		dequeued = allocated[:min(size, len(allocated))]
		return len(dequeued)
	})
	return dequeued
}

// DequeueSlice dequeues as many elements as fit into data, copies them there
// and returns how many that was.
func (r *RingBuffer[T]) DequeueSlice(data []T) int {
	// The allocated elements may wrap around the end of the storage: take
	// the part up to the end first, then the part at the front.
	dequeued := r.dequeueContiguous(data)
	return dequeued + r.dequeueContiguous(data[dequeued:])
}

// Unallocated returns the largest contiguous free area that starts offset
// elements past the end of the allocated ones, up to size elements long; it
// is empty when offset is beyond the free space.
func (r *RingBuffer[T]) Unallocated(offset, size int) []T {
	if offset > r.Window() {
		return nil
	}
	start := r.index(r.length + offset)
	return r.area(start, min(size, r.Window()-offset, r.Capacity()-start))
}

// WriteUnallocated copies as much of data as fits into the free space
// starting offset elements past the end of the allocated ones, without
// enqueuing it, and returns how many elements it wrote.
func (r *RingBuffer[T]) WriteUnallocated(offset int, data []T) int {
	// The free space may wrap around the end of the storage, so it takes up
	// to two contiguous areas.
	written := copy(r.Unallocated(offset, len(data)), data)
	return written + copy(r.Unallocated(offset+written, len(data)-written), data[written:])
}

// EnqueueUnallocated enqueues count free elements, as written by
// WriteUnallocated. It panics if count exceeds Window.
func (r *RingBuffer[T]) EnqueueUnallocated(count int) {
	r.length += checkedCount("EnqueueUnallocated", count, r.Window())
}

// Allocated returns the largest contiguous run of allocated elements that
// starts offset elements past the oldest one, up to size elements long; it is
// empty when offset is beyond the allocated elements.
func (r *RingBuffer[T]) Allocated(offset, size int) []T {
	if offset > r.length {
		return nil
	}
	start := r.index(offset)
	return r.area(start, min(size, r.length-offset, r.Capacity()-start))
}

// ReadAllocated copies as many allocated elements as fit into data, starting
// offset elements past the oldest one, without dequeuing them, and returns
// how many it copied.
func (r *RingBuffer[T]) ReadAllocated(offset int, data []T) int {
	// The allocated elements may wrap around the end of the storage, so they
	// take up to two contiguous runs.
	read := copy(data, r.Allocated(offset, len(data)))
	return read + copy(data[read:], r.Allocated(offset+read, len(data)-read))
}

// DequeueAllocated dequeues count elements without reading them. It panics if
// count exceeds Len.
func (r *RingBuffer[T]) DequeueAllocated(count int) {
	r.length -= checkedCount("DequeueAllocated", count, r.length)
	r.readAt = r.index(count)
}

// index returns the storage index of the element offset elements past the
// oldest one, wrapping around the end of the storage.
func (r *RingBuffer[T]) index(offset int) int {
	if r.Capacity() == 0 {
		return 0
	}
	return (r.readAt + offset) % r.Capacity()
}

// area returns length elements of the storage from start. Its capacity is
// its length, so appending to it reallocates instead of overwriting the
// elements that follow.
func (r *RingBuffer[T]) area(start, length int) []T {
	return r.storage[start : start+length : start+length]
}

func (r *RingBuffer[T]) enqueueContiguous(data []T) int {
	return r.EnqueueManyWith(func(free []T) int {
		return copy(free, data)
	})
}

func (r *RingBuffer[T]) dequeueContiguous(data []T) int {
	return r.DequeueManyWith(func(allocated []T) int {
		return copy(data, allocated)
	})
}

// checkedCount returns count after checking that it is between 0 and limit;
// a count outside that range is a programming error in the caller of
// operation, as smoltcp's assertions treat it.
func checkedCount(operation string, count, limit int) int {
	if count < 0 || count > limit {
		panic(fmt.Sprintf("storage: %s: count %d is outside 0 to %d", operation, count, limit))
	}
	return count
}
