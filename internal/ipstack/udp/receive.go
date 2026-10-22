package udp

import "github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"

// Receive dequeues the oldest received datagram and returns its payload and
// metadata. The payload shares the receive buffer's storage and is valid only
// until the socket is next used, by the application or by the interface's
// next poll, which may put a later datagram there: copy it to keep it. It
// returns ErrExhausted when none is waiting.
func (s *Socket) Receive() ([]byte, Metadata, error) {
	meta, payload, err := s.receiveBuffer.Dequeue()
	if err != nil {
		return nil, Metadata{}, ErrExhausted
	}
	netlog.Trace("udp: receive buffered octets", "local", s.endpoint, "remote", meta.Endpoint, "octets", len(payload))
	return payload, meta, nil
}

// ReceiveSlice dequeues the oldest received datagram, copies its payload into
// data and returns the length with its metadata. A datagram larger than data
// is dropped with ErrTruncated. It returns ErrExhausted when none is waiting.
func (s *Socket) ReceiveSlice(data []byte) (int, Metadata, error) {
	payload, meta, err := s.Receive()
	if err != nil {
		return 0, Metadata{}, err
	}
	return copyDatagram(data, payload, meta)
}

// Peek returns the oldest received datagram like Receive, without dequeuing
// it.
func (s *Socket) Peek() ([]byte, Metadata, error) {
	meta, payload, err := s.receiveBuffer.Peek()
	if err != nil {
		return nil, Metadata{}, ErrExhausted
	}
	netlog.Trace("udp: peek buffered octets", "local", s.endpoint, "remote", meta.Endpoint, "octets", len(payload))
	return payload, meta, nil
}

// PeekSlice copies the oldest received datagram into data like ReceiveSlice,
// without dequeuing it; a datagram larger than data stays queued, and
// nothing is copied, with ErrTruncated.
func (s *Socket) PeekSlice(data []byte) (int, Metadata, error) {
	payload, meta, err := s.Peek()
	if err != nil {
		return 0, Metadata{}, err
	}
	return copyDatagram(data, payload, meta)
}

// ReceiveQueue returns how many received payload octets wait to be read,
// counting padding as SendQueue does.
func (s *Socket) ReceiveQueue() int {
	return s.receiveBuffer.PayloadBytesCount()
}

// copyDatagram copies a datagram's payload into data and returns its length
// with meta, or fails with ErrTruncated, copying nothing, when data is too
// short to hold it.
func copyDatagram(data, payload []byte, meta Metadata) (int, Metadata, error) {
	if len(data) < len(payload) {
		return 0, Metadata{}, ErrTruncated
	}
	return copy(data, payload), meta, nil
}
