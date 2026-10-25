package tcp

import (
	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The methods in this file are the interface's side of the socket: it hands
// in received segments and asks for segments to send. Applications do not
// call them.

// EmitFunc sends one segment for Dispatch. ip.PayloadLen is the segment's
// BufferLen, and segment.Payload shares the socket's transmit buffer, so the
// segment must be serialised before EmitFunc returns. The socket records the
// segment as sent once EmitFunc returns.
type EmitFunc func(ip wire.IPv4Repr, segment wire.TCPRepr)

// Accepts reports whether a segment that arrived in datagram ip belongs to
// this socket: it matches the connection's four-tuple or, while listening,
// the listen endpoint. A listening socket does not accept a segment with an
// ACK or a RST, which another socket may be waiting for. The interface gives
// a segment to the first socket in the set that accepts it.
func (s *Socket) Accepts(ip wire.IPv4Repr, segment wire.TCPRepr) bool {
	if s.state == StateClosed {
		return false
	}
	// If we're still listening for SYNs and the packet has an ACK or a RST,
	// it cannot be destined to this socket, but another one may well listen
	// on the same local endpoint.
	if s.state == StateListen && (segment.AckNumber.IsSome() || segment.Control == wire.TCPControlRST) {
		return false
	}
	if tuple, ok := s.tuple.Get(); ok {
		// Reject packets not matching the four-tuple.
		return tuple.matches(ip, segment)
	}
	// We're listening, reject packets not matching the listen endpoint.
	return s.listenEndpointMatches(ip, segment)
}

// matches reports whether a segment that arrived in datagram ip belongs to
// the connection with this four-tuple.
func (t fourTuple) matches(ip wire.IPv4Repr, segment wire.TCPRepr) bool {
	return ip.DstAddr == t.local.Addr() && segment.DstPort == t.local.Port() &&
		ip.SrcAddr == t.remote.Addr() && segment.SrcPort == t.remote.Port()
}

// listenEndpointMatches reports whether a segment that arrived in datagram ip
// is for the endpoint the socket listens on.
func (s *Socket) listenEndpointMatches(ip wire.IPv4Repr, segment wire.TCPRepr) bool {
	addressMatches := !s.listenEndpoint.Addr.IsValid() || ip.DstAddr == s.listenEndpoint.Addr
	return addressMatches && segment.DstPort != 0 && segment.DstPort == s.listenEndpoint.Port
}

// Process takes a segment Accepts accepted and returns the segment to send
// back at once, if any: an ACK (for out-of-order data, a duplicate, or a
// challenge), or a RST for an unacceptable acknowledgement. The reply has no
// payload, and its IP header is new: ip's addresses swapped, NextHeader
// wire.IPProtocolTCP, HopLimit wire.DefaultHopLimit, and PayloadLen the
// reply's BufferLen (SACK blocks included). The interface sends the reply
// through the slot of the received datagram, which is always usable: the
// interface can send zero or one packets for every packet it receives.
// segment.Payload is copied, not kept.
func (s *Socket) Process(cx *socket.Context, ip wire.IPv4Repr, segment wire.TCPRepr) (wire.IPv4Repr, wire.TCPRepr, bool) {
	answer, ok := s.process(cx, ip, segment)
	return answer.ip, answer.segment, ok
}

// Dispatch emits at most one segment that is due: a SYN, data, a FIN, an
// ACK, a window update, a RST after Abort or a timeout, a keep-alive or a
// zero window probe; it also runs the retransmission, keep-alive, TIME-WAIT
// and timeout timers. A socket whose local address the interface no longer
// has is reset without a RST.
func (s *Socket) Dispatch(cx *socket.Context, emit EmitFunc) {
	tuple, ok := s.tuple.Get()
	if !ok {
		return
	}
	// Check if the interface still has our source IP address. If not (e.g.
	// the interface's IP changed), reset the socket. We use reset instead of
	// moving to CLOSED to avoid sending an RST packet with the now-invalid
	// source IP.
	if !cx.HasIPAddr(tuple.local.Addr()) {
		netlog.Debug("source IP address no longer available, closing socket")
		s.reset()
		return
	}
	now := cx.Now()
	s.startTimeoutClock(now)
	s.expireTimers(now)
	if !s.hasSegmentDue(cx) {
		s.closeIfTimeWaitIsOver(now)
		return
	}
	s.emitDueSegment(cx, tuple, emit)
}

// PollAt returns when the socket next needs Dispatch: now when a segment is
// due, at the earliest timer otherwise, and on ingress when only incoming
// segments can change anything.
func (s *Socket) PollAt(cx *socket.Context) socket.PollAt {
	// The logic here mirrors the beginning of Dispatch closely.
	switch {
	case !s.tuple.IsSome():
		// No one to talk to, nothing to transmit.
		return socket.PollOnIngress()
	case !s.remoteLastTimestamp.IsSome():
		// Socket stopped being quiet recently, we need to acquire a
		// timestamp.
		return socket.PollNow()
	case s.state == StateClosed:
		// Socket was aborted, we have an RST packet to transmit.
		return socket.PollNow()
	case s.seqToTransmit(cx):
		// We have a data or flag packet to transmit.
		return socket.PollNow()
	case s.windowToUpdate():
		// The receive window has been raised significantly.
		return socket.PollNow()
	default:
		// We wait for the earliest of our timers to fire.
		return socket.Earliest(s.timer.pollAt(), s.timeoutPollAt(), s.delayedACKPollAt())
	}
}

// RSTReply returns the RST that answers segment, which arrived in datagram ip
// and was accepted by no socket: RST|ACK for a SYN without ACK, a bare RST
// carrying the segment's acknowledgement number otherwise. Its IP header is
// new, as Process builds one: ip's addresses swapped, NextHeader
// wire.IPProtocolTCP, HopLimit wire.DefaultHopLimit, and PayloadLen the RST's
// BufferLen. segment must not itself be a RST: a RST is never answered with
// one.
func RSTReply(ip wire.IPv4Repr, segment wire.TCPRepr) (wire.IPv4Repr, wire.TCPRepr) {
	rst := rstReply(ip, segment)
	return rst.ip, rst.segment
}
