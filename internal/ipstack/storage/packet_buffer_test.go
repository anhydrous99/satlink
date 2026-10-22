package storage

import (
	"errors"
	"testing"
)

// newTestPacketBuffer returns a buffer of four packets with sixteen payload
// octets.
func newTestPacketBuffer() *PacketBuffer[struct{}] {
	return NewPacketBuffer[struct{}](4, 16)
}

func mustEnqueue(t *testing.T, buffer *PacketBuffer[struct{}], size int) []byte {
	t.Helper()
	payload, err := buffer.Enqueue(size, struct{}{})
	if err != nil {
		t.Fatalf("Enqueue(%d): %v", size, err)
	}
	return payload
}

func mustDequeue(t *testing.T, buffer *PacketBuffer[struct{}]) []byte {
	t.Helper()
	_, payload, err := buffer.Dequeue()
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	return payload
}

func assertEnqueueFull(t *testing.T, buffer *PacketBuffer[struct{}], size int) {
	t.Helper()
	if _, err := buffer.Enqueue(size, struct{}{}); !errors.Is(err, ErrFull) {
		t.Errorf("Enqueue(%d): %v, want ErrFull", size, err)
	}
}

func assertMetadataLen(t *testing.T, buffer *PacketBuffer[struct{}], want int) {
	t.Helper()
	if got := buffer.metadata.Len(); got != want {
		t.Errorf("%d metadata entries, want %d", got, want)
	}
}

// recoverPanic runs action and returns the value it panicked with, or nil.
func recoverPanic(action func()) (recovered any) {
	defer func() { recovered = recover() }()
	action()
	return nil
}

func TestPacketBufferSimple(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()

	copy(mustEnqueue(t, buffer, 6), "abcdef")
	assertEnqueueFull(t, buffer, 16)
	assertMetadataLen(t, buffer, 1)

	assertBytes(t, "dequeued payload", mustDequeue(t, buffer), "abcdef")
	if _, _, err := buffer.Dequeue(); !errors.Is(err, ErrEmpty) {
		t.Errorf("Dequeue: %v, want ErrEmpty", err)
	}
}

func TestPacketBufferPeek(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	if _, _, err := buffer.Peek(); !errors.Is(err, ErrEmpty) {
		t.Fatalf("Peek on an empty buffer: %v, want ErrEmpty", err)
	}

	copy(mustEnqueue(t, buffer, 6), "abcdef")
	assertMetadataLen(t, buffer, 1)
	_, peeked, err := buffer.Peek()
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	assertBytes(t, "peeked payload", peeked, "abcdef")
	assertBytes(t, "dequeued payload", mustDequeue(t, buffer), "abcdef")

	if _, _, err := buffer.Peek(); !errors.Is(err, ErrEmpty) {
		t.Errorf("Peek on an emptied buffer: %v, want ErrEmpty", err)
	}
}

func TestPacketBufferPadding(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 6)
	mustEnqueue(t, buffer, 8)
	mustDequeue(t, buffer)

	copy(mustEnqueue(t, buffer, 4), "abcd")
	assertMetadataLen(t, buffer, 3)
	mustDequeue(t, buffer)

	assertBytes(t, "dequeued payload", mustDequeue(t, buffer), "abcd")
	assertMetadataLen(t, buffer, 0)
}

func TestPacketBufferPaddingWithLargePayload(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 12)
	mustDequeue(t, buffer)

	payload := mustEnqueue(t, buffer, 12)

	assertCount(t, "payload length", len(payload), 12)
}

func TestPacketBufferDequeueWith(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 6)
	mustEnqueue(t, buffer, 8)
	mustDequeue(t, buffer)
	copy(mustEnqueue(t, buffer, 4), "abcd")
	assertMetadataLen(t, buffer, 3)
	mustDequeue(t, buffer)

	refused := errors.New("refused")
	if err := buffer.DequeueWith(func(*struct{}, []byte) error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("DequeueWith: %v, want the callback's error", err)
	}
	assertMetadataLen(t, buffer, 1)

	err := buffer.DequeueWith(func(_ *struct{}, payload []byte) error {
		assertBytes(t, "payload", payload, "abcd")
		return nil
	})
	if err != nil {
		t.Fatalf("DequeueWith: %v", err)
	}
	assertMetadataLen(t, buffer, 0)
}

func TestPacketBufferMetadataFullEmpty(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	if !buffer.IsEmpty() || buffer.IsFull() {
		t.Fatal("a new buffer is not empty, or is full")
	}

	for enqueued := 1; enqueued <= 4; enqueued++ {
		mustEnqueue(t, buffer, 1)
		if buffer.IsEmpty() {
			t.Errorf("empty after %d enqueues", enqueued)
		}
		if wantFull := enqueued == 4; buffer.IsFull() != wantFull {
			t.Errorf("after %d enqueues IsFull() = %v, want %v", enqueued, buffer.IsFull(), wantFull)
		}
	}
	assertMetadataLen(t, buffer, 4)
	assertEnqueueFull(t, buffer, 1)
}

func TestPacketBufferWindowTooSmall(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 4)
	mustEnqueue(t, buffer, 8)
	mustDequeue(t, buffer)

	assertEnqueueFull(t, buffer, 16)
	assertMetadataLen(t, buffer, 1)
}

func TestPacketBufferContiguousWindowTooSmall(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 4)
	mustEnqueue(t, buffer, 8)
	mustDequeue(t, buffer)

	assertEnqueueFull(t, buffer, 8)
	assertMetadataLen(t, buffer, 1)
}

func TestPacketBufferContiguousWindowWrap(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 15)
	mustDequeue(t, buffer)

	mustEnqueue(t, buffer, 16)
}

func TestPacketBufferCapacityTooSmall(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()

	assertEnqueueFull(t, buffer, 32)
}

func TestPacketBufferContiguousWindowPrioritized(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 4)
	mustDequeue(t, buffer)

	mustEnqueue(t, buffer, 5)
}

// With room for the payload only behind padding, and a single packet slot
// left, the padding takes the slot and EnqueueWith fails without calling
// fill.
func TestPacketBufferEnqueueWithFullMetadataDoesNotCallFill(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 12)
	mustEnqueue(t, buffer, 1)
	mustEnqueue(t, buffer, 1)
	mustEnqueue(t, buffer, 1)
	assertCount(t, "dequeued payload length", len(mustDequeue(t, buffer)), 12)

	_, err := buffer.EnqueueWith(5, struct{}{}, func([]byte) int {
		t.Error("fill called although the enqueue fails")
		return 0
	})

	if err == nil {
		t.Error("EnqueueWith succeeded, want an error")
	}
}

// Padding added for a packet that then finds no packet slot stays queued, and is dropped in turn when it reaches the front.
func TestPacketBufferEnqueueFailsWhenPaddingTakesTheLastSlot(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 12)
	mustEnqueue(t, buffer, 1)
	mustEnqueue(t, buffer, 1)
	mustEnqueue(t, buffer, 1)
	mustDequeue(t, buffer)

	assertEnqueueFull(t, buffer, 5)

	assertMetadataLen(t, buffer, 4)
	assertCount(t, "PayloadBytesCount()", buffer.PayloadBytesCount(), 3+1)
}

func TestPacketBufferRefusesAPayloadOneOctetLargerThanEitherFreeArea(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 4)
	mustEnqueue(t, buffer, 8)
	mustDequeue(t, buffer)
	// Four octets are free at the end of the storage and four at its front:
	// a payload of five, one octet more than either, fits whole into neither.

	assertEnqueueFull(t, buffer, 5)
	assertMetadataLen(t, buffer, 1)
}

func TestPacketBufferEnqueueWithRefusesMoreThanTheWindow(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 10)

	_, err := buffer.EnqueueWith(8, struct{}{}, func([]byte) int {
		t.Error("fill called although the enqueue fails")
		return 0
	})

	if !errors.Is(err, ErrFull) {
		t.Errorf("EnqueueWith: %v, want ErrFull", err)
	}
}

func TestPacketBufferEnqueueWithRefusesWhatCannotFitAtAll(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		queued  int
		maxSize int
	}{
		"more than the payload storage": {queued: 0, maxSize: 17},
		"every packet slot taken":       {queued: 4, maxSize: 1},
	}
	for name, test := range tests {
		buffer := newTestPacketBuffer()
		for range test.queued {
			mustEnqueue(t, buffer, 1)
		}

		_, err := buffer.EnqueueWith(test.maxSize, struct{}{}, func([]byte) int {
			t.Errorf("%s: fill called although the enqueue fails", name)
			return 0
		})

		if !errors.Is(err, ErrFull) {
			t.Errorf("%s: EnqueueWith(%d): %v, want ErrFull", name, test.maxSize, err)
		}
		assertMetadataLen(t, buffer, test.queued)
	}
}

func TestPacketBufferPanicsOnANegativeSizeBeforeChangingAnything(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		enqueue   func(buffer *PacketBuffer[struct{}])
		wantPanic string
	}{
		"Enqueue": {
			enqueue:   func(buffer *PacketBuffer[struct{}]) { _, _ = buffer.Enqueue(-1, struct{}{}) },
			wantPanic: "storage: Enqueue: size -1 is negative",
		},
		"EnqueueWith": {
			enqueue: func(buffer *PacketBuffer[struct{}]) {
				_, _ = buffer.EnqueueWith(-1, struct{}{}, func([]byte) int { return 0 })
			},
			wantPanic: "storage: EnqueueWith: size -1 is negative",
		},
	}
	for name, test := range tests {
		buffer := newTestPacketBuffer()

		recovered := recoverPanic(func() { test.enqueue(buffer) })

		if recovered != test.wantPanic {
			t.Errorf("%s(-1) panicked with %v, want %q", name, recovered, test.wantPanic)
		}
		if !buffer.IsEmpty() || buffer.PayloadBytesCount() != 0 {
			t.Errorf("after %s(-1): %d packets and %d payload octets queued, want none", name, buffer.metadata.Len(), buffer.PayloadBytesCount())
		}
	}
}

func TestPacketBufferClear(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 6)
	if buffer.IsEmpty() {
		t.Fatal("empty after an enqueue")
	}

	buffer.Clear()

	if !buffer.IsEmpty() {
		t.Error("not empty after Clear")
	}
}

func TestPacketBufferEnqueueWithQueuesWhatFillWrote(t *testing.T) {
	t.Parallel()
	buffer := NewPacketBuffer[string](2, 8)

	size, err := buffer.EnqueueWith(6, "peer", func(payload []byte) int {
		assertCount(t, "room offered", len(payload), 6)
		return copy(payload, "abc")
	})
	if err != nil || size != 3 {
		t.Fatalf("EnqueueWith = %d, %v; want 3, nil", size, err)
	}

	header, payload, err := buffer.Dequeue()
	if err != nil || header != "peer" {
		t.Fatalf("Dequeue = %q, %v; want the header %q", header, err, "peer")
	}
	assertBytes(t, "payload", payload, "abc")
}

func TestPacketBufferEnqueueWithPanicsWhenFillOverstates(t *testing.T) {
	t.Parallel()
	buffer := NewPacketBuffer[struct{}](2, 8)
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()

	_, _ = buffer.EnqueueWith(4, struct{}{}, func([]byte) int { return 5 })
}

func TestPacketBufferCountsPaddingUntilPeekDropsIt(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()
	mustEnqueue(t, buffer, 6)
	mustEnqueue(t, buffer, 8)
	mustDequeue(t, buffer)
	// Only 2 octets are left before the end of the storage, so the next
	// payload goes to the front, behind 2 octets of padding.
	copy(mustEnqueue(t, buffer, 4), "abcd")
	mustDequeue(t, buffer)
	assertCount(t, "PayloadBytesCount()", buffer.PayloadBytesCount(), 2+4)

	_, peeked, err := buffer.Peek()

	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	assertBytes(t, "peeked payload", peeked, "abcd")
	assertCount(t, "PayloadBytesCount()", buffer.PayloadBytesCount(), 4)
}

func TestPacketBufferCapacities(t *testing.T) {
	t.Parallel()
	buffer := NewPacketBuffer[struct{}](3, 10)

	assertCount(t, "PacketCapacity()", buffer.PacketCapacity(), 3)
	assertCount(t, "PayloadCapacity()", buffer.PayloadCapacity(), 10)
}

func TestPacketBufferZeroValueHasNoCapacity(t *testing.T) {
	t.Parallel()
	var buffer PacketBuffer[struct{}]

	assertEnqueueFull(t, &buffer, 0)
	if _, _, err := buffer.Dequeue(); !errors.Is(err, ErrEmpty) {
		t.Errorf("Dequeue: %v, want ErrEmpty", err)
	}
}
