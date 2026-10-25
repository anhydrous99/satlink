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

func TestPacketBufferPanicsOnANegativeSizeBeforeChangingAnything(t *testing.T) {
	t.Parallel()
	buffer := newTestPacketBuffer()

	recovered := recoverPanic(func() { _, _ = buffer.Enqueue(-1, struct{}{}) })

	if want := "storage: Enqueue: size -1 is negative"; recovered != want {
		t.Errorf("Enqueue(-1) panicked with %v, want %q", recovered, want)
	}
	if !buffer.IsEmpty() || buffer.PayloadBytesCount() != 0 {
		t.Errorf("after Enqueue(-1): %d packets and %d payload octets queued, want none", buffer.metadata.Len(), buffer.PayloadBytesCount())
	}
}

func TestPacketBufferCountsPaddingUntilItIsDequeued(t *testing.T) {
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

	assertBytes(t, "dequeued payload", mustDequeue(t, buffer), "abcd")
	assertCount(t, "PayloadBytesCount()", buffer.PayloadBytesCount(), 0)
}

func TestPacketBufferZeroValueHasNoCapacity(t *testing.T) {
	t.Parallel()
	var buffer PacketBuffer[struct{}]

	assertEnqueueFull(t, &buffer, 0)
	if _, _, err := buffer.Dequeue(); !errors.Is(err, ErrEmpty) {
		t.Errorf("Dequeue: %v, want ErrEmpty", err)
	}
}
