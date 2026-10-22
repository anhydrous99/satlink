package storage

import (
	"errors"
	"fmt"
)

// PacketBuffer is a FIFO queue of packets, each a payload with a header of
// type H (a UDP socket keeps the remote endpoint there). It limits both the
// number of packets and their total size, and keeps every payload contiguous:
// a payload that would wrap around the end of the storage is placed at its
// start instead, behind padding that counts as queued octets until it is
// dequeued.
//
// Methods that hand out payloads let callers work in place, as RingBuffer's
// do: the slices share the buffer's storage and are valid only until the
// buffer is next changed. Fill an enqueued payload before then, and copy a
// dequeued one to keep it.
//
// The zero PacketBuffer has no capacity: every enqueue fails.
type PacketBuffer[H any] struct {
	// metadata holds one entry per packet, and one per run of padding.
	metadata RingBuffer[packetMetadata[H]]
	payload  RingBuffer[byte]
}

// packetMetadata describes one packet, or one run of padding, in the payload
// ring.
type packetMetadata[H any] struct {
	size      int
	header    H
	isPadding bool
}

// errNotPadding stops dequeuePadding at a packet; it never leaves the
// package.
var errNotPadding = errors.New("not padding")

// NewPacketBuffer returns an empty buffer for up to packetCapacity packets of
// payloadCapacity octets in total. It panics if either is negative.
func NewPacketBuffer[H any](packetCapacity, payloadCapacity int) *PacketBuffer[H] {
	return &PacketBuffer[H]{
		metadata: RingBuffer[packetMetadata[H]]{storage: make([]packetMetadata[H], packetCapacity)},
		payload:  RingBuffer[byte]{storage: make([]byte, payloadCapacity)},
	}
}

// IsEmpty reports whether no packet is queued.
func (b *PacketBuffer[H]) IsEmpty() bool {
	return b.metadata.IsEmpty()
}

// IsFull reports whether the buffer holds as many packets as it can; there
// may still be no room for a payload when it is not.
func (b *PacketBuffer[H]) IsFull() bool {
	return b.metadata.IsFull()
}

// Enqueue queues a packet of size octets with header and returns its payload
// for the caller to fill, or returns ErrFull when the packet does not fit. It
// panics if size is negative.
func (b *PacketBuffer[H]) Enqueue(size int, header H) ([]byte, error) {
	requireSize("Enqueue", size)
	if !b.admits(size) {
		return nil, ErrFull
	}
	if b.payload.IsEmpty() {
		// An empty payload ring starts over at the front of its storage,
		// where the contiguous room is the largest possible.
		b.payload.Clear()
	}
	if err := b.makeContiguousRoom(size); err != nil {
		return nil, err
	}
	metadata, err := b.metadata.EnqueueOne()
	if err != nil {
		return nil, err
	}
	*metadata = packetMetadata[H]{size: size, header: header}
	return b.payload.EnqueueMany(size), nil
}

// EnqueueWith reserves room for a packet of up to maxSize octets, calls fill
// with it and queues the packet with the length fill returns, which it also
// returns. It returns ErrFull, without calling fill, when maxSize octets do
// not fit, and panics if maxSize is negative or fill returns more than
// maxSize.
func (b *PacketBuffer[H]) EnqueueWith(maxSize int, header H, fill func(payload []byte) int) (int, error) {
	requireSize("EnqueueWith", maxSize)
	if !b.admits(maxSize) {
		return 0, ErrFull
	}
	if err := b.makeContiguousRoom(maxSize); err != nil {
		return 0, err
	}
	metadata, err := b.metadata.EnqueueOne()
	if err != nil {
		return 0, err
	}
	size := b.payload.EnqueueManyWith(func(free []byte) int {
		return checkedCount("EnqueueWith", fill(free[:maxSize:maxSize]), maxSize)
	})
	*metadata = packetMetadata[H]{size: size, header: header}
	return size, nil
}

// DequeueWith calls take with the oldest packet and dequeues it if take
// returns nil; otherwise the packet stays queued and take's error is
// returned. It returns ErrEmpty, without calling take, when no packet is
// queued.
func (b *PacketBuffer[H]) DequeueWith(take func(header *H, payload []byte) error) error {
	b.dequeuePadding()
	return b.metadata.DequeueOneWith(func(metadata *packetMetadata[H]) error {
		if err := take(&metadata.header, b.payload.Allocated(0, metadata.size)); err != nil {
			return err
		}
		b.payload.DequeueAllocated(metadata.size)
		// Drop the header, so that the buffer does not keep what it refers
		// to alive.
		*metadata = packetMetadata[H]{}
		return nil
	})
}

// Dequeue dequeues the oldest packet and returns its header and payload, or
// returns ErrEmpty.
func (b *PacketBuffer[H]) Dequeue() (H, []byte, error) {
	var header H
	var payload []byte
	err := b.DequeueWith(func(dequeuedHeader *H, dequeuedPayload []byte) error {
		header, payload = *dequeuedHeader, dequeuedPayload
		return nil
	})
	return header, payload, err
}

// Peek returns the header and payload of the oldest packet without
// dequeuing it, or returns ErrEmpty. It drops padding at the front of the
// buffer first, which PayloadBytesCount then no longer counts.
func (b *PacketBuffer[H]) Peek() (H, []byte, error) {
	b.dequeuePadding()
	oldest := b.metadata.Allocated(0, 1)
	if len(oldest) == 0 {
		var none H
		return none, nil, ErrEmpty
	}
	return oldest[0].header, b.payload.Allocated(0, oldest[0].size), nil
}

// PacketCapacity returns the most packets the buffer holds.
func (b *PacketBuffer[H]) PacketCapacity() int {
	return b.metadata.Capacity()
}

// PayloadCapacity returns the most payload octets the buffer holds.
func (b *PacketBuffer[H]) PayloadCapacity() int {
	return b.payload.Capacity()
}

// PayloadBytesCount returns how many payload octets are queued, padding
// included.
func (b *PacketBuffer[H]) PayloadBytesCount() int {
	return b.payload.Len()
}

// Clear drops every queued packet.
func (b *PacketBuffer[H]) Clear() {
	b.payload.Clear()
	b.metadata.Clear()
}

// requireSize panics if size, the payload size given to operation, is
// negative, before the buffer changes: smoltcp's sizes are unsigned, and a
// packet of a negative size would be queued before its payload failed.
func requireSize(operation string, size int) {
	if size < 0 {
		panic(fmt.Sprintf("storage: %s: size %d is negative", operation, size))
	}
}

// admits reports whether a packet of size octets could fit at all: it is not
// larger than the payload storage, and a packet slot is free.
func (b *PacketBuffer[H]) admits(size int) bool {
	return size <= b.payload.Capacity() && !b.metadata.IsFull()
}

// makeContiguousRoom makes sure that the next size payload octets can be
// enqueued in one piece. When the free space up to the end of the storage is
// too small, but the free space at its front is not, it fills the space up to
// the end with padding. It returns ErrFull when neither is large enough.
func (b *PacketBuffer[H]) makeContiguousRoom(size int) error {
	window := b.payload.Window()
	contiguousWindow := b.payload.ContiguousWindow()
	switch {
	case contiguousWindow >= size:
		return nil
	case window < size || window-contiguousWindow < size:
		return ErrFull
	default:
		return b.enqueuePadding(contiguousWindow)
	}
}

// enqueuePadding fills the next size payload octets, which nothing reads,
// with padding; it takes a packet slot.
func (b *PacketBuffer[H]) enqueuePadding(size int) error {
	padding, err := b.metadata.EnqueueOne()
	if err != nil {
		return err
	}
	*padding = packetMetadata[H]{size: size, isPadding: true}
	b.payload.EnqueueMany(size)
	return nil
}

// dequeuePadding drops the padding at the front of the buffer, if any, so
// that the oldest packet's payload is at the front of the payload ring.
func (b *PacketBuffer[H]) dequeuePadding() {
	// The error is ErrEmpty or errNotPadding: either way there is no padding
	// at the front, and nothing to do.
	_ = b.metadata.DequeueOneWith(func(metadata *packetMetadata[H]) error {
		if !metadata.isPadding {
			return errNotPadding
		}
		b.payload.DequeueMany(metadata.size)
		return nil
	})
}
