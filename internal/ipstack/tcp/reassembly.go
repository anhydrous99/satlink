package tcp

import (
	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
)

// receivePayload puts the payload into the receive buffer, through the
// assembler so that segments may arrive out of order, and returns the ACK to
// send at once, if any.
func (s *Socket) receivePayload(a *arrival) (reply, bool) {
	if len(a.payload) == 0 {
		return reply{}, false
	}
	assemblerWasEmpty := s.assembler.IsEmpty()
	// Try adding payload octets to the assembler.
	contiguous, err := s.assembler.AddThenRemoveFront(a.payloadOffset, len(a.payload))
	if err != nil {
		netlog.Debug("assembler: too many holes to add octets",
			"octets", len(a.payload), "offset", a.payloadOffset)
		return reply{}, false
	}
	s.placePayload(a.payload, a.payloadOffset, contiguous)
	if s.ackDelay > 0 && s.ackToTransmit() {
		s.delayACK(a.cx.Now())
	}
	// Per RFC 5681, we should send an immediate ACK when either: 1) an
	// out-of-order segment is received, or 2) a segment arrives that fills in
	// all or part of a gap in sequence space.
	if s.assembler.IsEmpty() && assemblerWasEmpty {
		return reply{}, false
	}
	// Note that we change the transmitter state here. This is fine because
	// smoltcp assumes that it can always transmit zero or one packets for
	// every packet it receives.
	netlog.Trace("ACKing incoming segment")
	return s.ackReply(a.ip, a.segment), true
}

// placePayload writes payload into the free space of the receive buffer at
// offset octets past the data already there, and makes the contiguous
// octets at the front of that space, which the assembler reported, readable.
func (s *Socket) placePayload(payload []byte, offset, contiguous int) {
	netlog.Trace("rx buffer: receiving octets", "octets", len(payload), "offset", offset)
	s.receiveBuffer.WriteUnallocated(offset, payload)
	if contiguous != 0 {
		// Enqueue the contiguous data octets in front of the buffer.
		netlog.Trace("rx buffer: enqueueing octets",
			"octets", contiguous, "now", s.receiveBuffer.Len()+contiguous)
		s.receiveBuffer.EnqueueUnallocated(contiguous)
	}
	if !s.assembler.IsEmpty() {
		// Print the ranges recorded in the assembler.
		netlog.Trace("assembler", "ranges", &s.assembler)
	}
}
