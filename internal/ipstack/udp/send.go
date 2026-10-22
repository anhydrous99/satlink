package udp

import (
	"fmt"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
)

// Send queues a datagram of size octets for meta.Endpoint and returns its
// payload for the caller to fill. The payload shares the transmit buffer's
// storage: fill it before the socket is next used, by the application or by
// the interface's next poll, which may send the datagram as it then is. It
// returns ErrUnaddressable when the socket is unbound, the destination
// address is unspecified or not IPv4 (Is4; the zero Addr and IPv4-mapped
// forms are not), the destination port is 0, or meta.LocalAddress is set but
// not IPv4; and ErrBufferFull when the datagram does not fit. It panics if
// size is negative.
func (s *Socket) Send(size int, meta Metadata) ([]byte, error) {
	requireSize("Send", size)
	if err := s.checkAddressable(meta); err != nil {
		return nil, err
	}
	payload, err := s.transmitBuffer.Enqueue(size, meta)
	if err != nil {
		return nil, ErrBufferFull
	}
	s.traceQueued(meta, size)
	return payload, nil
}

// SendWith reserves room for a datagram of up to maxSize octets for
// meta.Endpoint, calls fill with it and queues the datagram with the length
// fill returns, which it also returns. The room shares the transmit buffer's
// storage: fill writes the payload in place, and must not keep the slice. It
// fails like Send, without calling fill, and panics if maxSize is negative or
// fill returns more than it was given.
func (s *Socket) SendWith(maxSize int, meta Metadata, fill func(payload []byte) int) (int, error) {
	requireSize("SendWith", maxSize)
	if err := s.checkAddressable(meta); err != nil {
		return 0, err
	}
	size, err := s.transmitBuffer.EnqueueWith(maxSize, meta, fill)
	if err != nil {
		return 0, ErrBufferFull
	}
	s.traceQueued(meta, size)
	return size, nil
}

// SendSlice queues a copy of data as one datagram for meta.Endpoint. It fails
// like Send.
func (s *Socket) SendSlice(data []byte, meta Metadata) error {
	payload, err := s.Send(len(data), meta)
	if err != nil {
		return err
	}
	copy(payload, data)
	return nil
}

// SendQueue returns how many payload octets wait to be sent, counting the
// padding the buffer may insert to keep payloads contiguous.
func (s *Socket) SendQueue() int {
	return s.transmitBuffer.PayloadBytesCount()
}

// checkAddressable returns ErrUnaddressable unless a datagram with meta can
// be sent: the socket must be bound, and the destination a usable IPv4
// endpoint. The address checks beyond smoltcp's keep out what its types
// cannot hold: an address that is not IPv4.
func (s *Socket) checkAddressable(meta Metadata) error {
	destination := meta.Endpoint.Addr()
	switch {
	case s.endpoint.Port == 0,
		!destination.Is4(),
		destination.IsUnspecified(),
		meta.Endpoint.Port() == 0,
		!isOpenOrIPv4(meta.LocalAddress):
		return ErrUnaddressable
	default:
		return nil
	}
}

// traceQueued logs a datagram of size octets queued for meta.Endpoint.
func (s *Socket) traceQueued(meta Metadata, size int) {
	netlog.Trace("udp: buffer to send octets", "local", s.endpoint, "remote", meta.Endpoint, "octets", size)
}

// requireSize panics unless size, given to operation, is a size: smoltcp's
// sizes are unsigned, and the packet buffer must not take a negative one.
func requireSize(operation string, size int) {
	if size < 0 {
		panic(fmt.Sprintf("udp: %s: size %d is negative", operation, size))
	}
}
