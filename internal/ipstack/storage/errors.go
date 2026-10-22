package storage

import "errors"

var (
	// ErrFull reports an enqueue into a buffer without room.
	ErrFull = errors.New("buffer full")
	// ErrEmpty reports a dequeue from an empty buffer.
	ErrEmpty = errors.New("buffer empty")
)
