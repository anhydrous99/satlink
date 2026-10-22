package simtest

import (
	"math"
	"time"

	"github.com/anhydrous99/satlink"
)

// longestMillis is the longest timeout, in milliseconds, that a
// time.Duration holds; longer ones saturate to it rather than overflow.
const longestMillis = math.MaxInt64 / int64(time.Millisecond)

// ByteTransport is a mobile.ByteTransport over a satlink.Transport, as a
// host app's USB transport is one over the real handset: the way to drive
// package mobile from Go tests. It satisfies mobile.ByteTransport by its
// methods alone, so that this package need not import mobile. Its errors
// are the wrapped transport's, unchanged. The zero ByteTransport is not
// usable; create one with [NewByteTransport].
type ByteTransport struct {
	transport satlink.Transport
}

// NewByteTransport returns a ByteTransport over transport: usually a
// *sim.Handset, perhaps behind a [FaultyTransport].
func NewByteTransport(transport satlink.Transport) *ByteTransport {
	return &ByteTransport{transport: transport}
}

// Write writes all of data.
func (b *ByteTransport) Write(data []byte) error {
	return b.transport.WriteAll(data)
}

// Read returns up to maxLen bytes, waiting at most timeoutMillis
// milliseconds for the first, and an empty slice when nothing arrived in
// time; a maxLen below 1 reads nothing.
func (b *ByteTransport) Read(maxLen int, timeoutMillis int64) ([]byte, error) {
	if maxLen < 1 {
		return []byte{}, nil
	}
	buffer := make([]byte, maxLen)
	count, err := b.transport.ReadTimeout(buffer, millisecondsToDuration(timeoutMillis))
	if err != nil {
		return nil, err
	}
	return buffer[:count], nil
}

// RaiseDTR raises the DTR line, if the wrapped transport has one; without
// it, RaiseDTR fails as satlink.Handset does ("this transport has no DTR
// line").
func (b *ByteTransport) RaiseDTR() error {
	line, err := dtrLineOf(b.transport)
	if err != nil {
		return err
	}
	return line.RaiseDTR()
}

// DropDTR drops the DTR line, if the wrapped transport has one; without it,
// DropDTR fails as RaiseDTR does.
func (b *ByteTransport) DropDTR() error {
	line, err := dtrLineOf(b.transport)
	if err != nil {
		return err
	}
	return line.DropDTR()
}

// millisecondsToDuration converts a timeout in milliseconds, saturating
// instead of overflowing, as mobile's own conversion does.
func millisecondsToDuration(milliseconds int64) time.Duration {
	return time.Duration(max(-longestMillis, min(milliseconds, longestMillis))) * time.Millisecond
}
