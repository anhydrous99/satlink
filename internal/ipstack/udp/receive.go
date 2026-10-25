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
