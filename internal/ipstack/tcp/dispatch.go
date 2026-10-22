package tcp

import (
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// transmission is a segment Dispatch is about to emit, with its IP header,
// and what kind of segment it is.
type transmission struct {
	ip      wire.IPv4Repr
	segment wire.TCPRepr
	kind    segmentKind
}

// segmentKind is the kind of segment Dispatch sends, which decides what it
// records once the segment is out: an ordinary segment counts as sent, while
// a zero-window probe or a keep-alive leaves the sequence numbers and the
// retransmission timer alone.
type segmentKind int

const (
	// ordinarySegment is a SYN, data, a FIN, an ACK, a window update or a
	// RST: everything but the two kinds below.
	ordinarySegment segmentKind = iota
	// zeroWindowProbeSegment probes the peer's zero window with one octet
	// beyond it.
	zeroWindowProbeSegment
	// keepAliveSegment carries a fake octet the peer has already
	// acknowledged.
	keepAliveSegment
)

// keepAlivePayload returns the payload of a keep-alive segment: one garbage
// octet, as RFC 1122 (4.2.3.6) says we should send, for peers that ignore
// segments without data.
func keepAlivePayload() []byte {
	return []byte{0}
}

// startTimeoutClock starts counting the timeout when the socket stopped being
// quiet.
func (s *Socket) startTimeoutClock(now time.Time) {
	if s.remoteLastTimestamp.IsSome() {
		return
	}
	// We get here in exactly two cases:
	//  1. This socket just transitioned into SYN-SENT.
	//  2. This socket had an empty transmit buffer and some data was added
	//     there.
	// Both are similar in that the socket has been quiet for an indefinite
	// period of time, it isn't anymore, and the local endpoint is talking.
	// So, we start counting the timeout not from the last received packet but
	// from the first transmitted one.
	s.remoteLastTimestamp = wire.Some(now)
}

// expireTimers checks if any state needs to be changed because of a timer.
func (s *Socket) expireTimers(now time.Time) {
	if s.timedOut(now) {
		// If a timeout expires, we should abort the connection.
		netlog.Debug("timeout exceeded")
		s.setState(StateClosed)
		return
	}
	if s.timer.shouldRetransmit(now) {
		s.startRetransmission(now)
	}
}

// startRetransmission arranges for unacknowledged data to be sent again
// after the retransmission timer expired.
func (s *Socket) startRetransmission(now time.Time) {
	if s.timer.kind == timerRetransmit {
		// If a retransmit timer expired, we should resend data starting at
		// the last ACK.
		netlog.Debug("retransmitting after rto")
		// Rewind "last sequence number sent", as if we never had sent them.
		// This will cause all data in the queue to be sent again.
		s.remoteLastSeq = s.localSeqNumber
		// Inform RTTE, so that it can handle RTO backoff
		s.rttEstimator.onRTO()
	} else {
		// If a fast retransmit timer expired, we should resend only the
		// earliest unACKed segment
		netlog.Debug("retransmitting for fast-retransmit")
		s.pendingFastRetransmit = true
	}
	// Clear the shouldRetransmit state. If we can't retransmit right now for
	// whatever reason (like zero window), this avoids an infinite polling
	// loop where PollAt returns now but Dispatch can't actually do anything.
	s.timer.setForIdle(now, s.keepAlive)
	// Inform RTTE, so that it can avoid bogus measurements.
	s.rttEstimator.onRetransmit()
}

// hasSegmentDue decides whether we're sending a packet.
func (s *Socket) hasSegmentDue(cx *socket.Context) bool {
	now := cx.Now()
	switch {
	case s.seqToTransmit(cx):
		// If we have data to transmit and it fits into partner's window, do
		// it.
		netlog.Trace("outgoing segment will send data or flags")
	case s.ackToTransmit() && s.ackDelayTimer.expired(now):
		// If we have data to acknowledge, do it.
		netlog.Trace("outgoing segment will acknowledge")
	case s.windowToUpdate():
		// If we have window length increase to advertise, do it.
		netlog.Trace("outgoing segment will update window")
	case s.state == StateClosed:
		// If we need to abort the connection, do it.
		netlog.Trace("outgoing segment will abort connection")
	case s.timer.shouldKeepAlive(now):
		// If we need to transmit a keep-alive packet, do it.
		netlog.Trace("keep-alive timer expired")
	case s.timer.shouldZeroWindowProbe(now):
		netlog.Trace("sending zero-window probe")
	default:
		return false
	}
	return true
}

// closeIfTimeWaitIsOver closes the socket if we have spent enough time in the
// TIME-WAIT state.
func (s *Socket) closeIfTimeWaitIsOver(now time.Time) {
	if s.timer.shouldClose(now) {
		netlog.Trace("TIME-WAIT timer expired")
		s.reset()
	}
}

// emitDueSegment builds the segment that is due, emits it, and records it as
// sent if emit took it.
func (s *Socket) emitDueSegment(cx *socket.Context, tuple fourTuple, emit EmitFunc) error {
	if s.state == StateListen {
		// We never transmit anything in the LISTEN state.
		return nil
	}
	out := s.buildTransmission(cx, tuple)
	// Actually send the packet. If this succeeds, it means the packet is in
	// the device buffer, and its transmission is imminent. If not, the line
	// has no room for it now.
	//
	// Bailing out if the packet isn't placed in the device buffer allows us
	// to not waste time waiting for the retransmit timer on packets that we
	// know for sure will not be successfully transmitted.
	if err := emit(out.ip, out.segment); err != nil {
		return err
	}
	s.recordTransmission(cx.Now(), out)
	return nil
}

// buildTransmission constructs the segment to send in the socket's state.
func (s *Socket) buildTransmission(cx *socket.Context, tuple fourTuple) transmission {
	// Start from an empty ACK packet; we'll adjust it to be more specific as
	// needed.
	out := s.emptyACK(tuple)
	switch s.state {
	case StateClosed:
		// We transmit an RST in the CLOSED state. If we ended up in the
		// CLOSED state with a specified endpoint, it means that the socket
		// was aborted.
		out.segment.Control = wire.TCPControlRST
	case StateSynSent, StateSynReceived:
		// We transmit a SYN in the SYN-SENT state, and a SYN|ACK in the
		// SYN-RECEIVED state.
		s.fillSYN(cx, &out.segment)
	case StateEstablished, StateFinWait1, StateClosing, StateCloseWait, StateLastAck:
		// We transmit data in all states where we may have data in the
		// buffer, or the transmit half of the connection is still open.
		out.kind = s.fillData(cx, &out.segment)
	default:
		// In FIN-WAIT-2 and TIME-WAIT states we may only transmit ACKs for
		// incoming data or FIN.
	}
	if s.makeKeepAlive(cx.Now(), &out.segment) {
		out.kind = keepAliveSegment
	}
	s.traceTransmission(out)
	out.ip.PayloadLen = out.segment.BufferLen()
	return out
}

// emptyACK returns the basic TCP representation of a segment to the peer of
// tuple, an empty ACK packet, with its lowered IP representation, whose
// payload length is set once the segment is complete.
func (s *Socket) emptyACK(tuple fourTuple) transmission {
	return transmission{
		ip: wire.IPv4Repr{
			SrcAddr:    tuple.local.Addr(),
			DstAddr:    tuple.remote.Addr(),
			NextHeader: wire.IPProtocolTCP,
			HopLimit:   wire.DefaultHopLimit,
		},
		segment: wire.TCPRepr{
			SrcPort:   tuple.local.Port(),
			DstPort:   tuple.remote.Port(),
			SeqNumber: s.remoteLastSeq,
			AckNumber: wire.Some(s.nextExpectedSeq()),
			WindowLen: s.scaledWindow(),
		},
	}
}

// fillSYN makes the segment a SYN, or a SYN|ACK in SYN-RECEIVED, with the
// options the socket offers.
func (s *Socket) fillSYN(cx *socket.Context, segment *wire.TCPRepr) {
	segment.Control = wire.TCPControlSYN
	segment.SeqNumber = s.localSeqNumber
	// window len must NOT be scaled in SYNs.
	segment.WindowLen = windowField(s.receiveBuffer.Window())
	segment.MaxSegmentSize = wire.Some(uint16(localMSS(cx)))
	if s.state == StateSynSent {
		segment.AckNumber = wire.Optional[wire.TCPSeqNumber]{}
		segment.WindowScale = wire.Some(s.remoteWindowShift)
		segment.SACKPermitted = true
		return
	}
	segment.SACKPermitted = s.remoteHasSACK
	if s.remoteWindowScale.IsSome() {
		segment.WindowScale = wire.Some(s.remoteWindowShift)
	}
}

// fillData extracts as much data as the remote side can receive in this
// packet from the transmit buffer, and returns the segment's kind: a
// zero-window probe or an ordinary segment.
//
// The size is limited by 3 factors (the congestion window is unlimited
// without congestion control): the remote window, the MSS the remote is
// willing to accept, probably determined by their MTU, and the MSS we can
// send, determined by our MTU.
func (s *Socket) fillData(cx *socket.Context, segment *wire.TCPRepr) segmentKind {
	maxSize := effectiveMSS(cx, s.remoteMSS)
	if s.pendingFastRetransmit {
		s.fillFastRetransmission(segment, maxSize)
		s.markEndOfData(segment, 0)
		return ordinarySegment
	}
	offset, kind := s.fillNextData(cx.Now(), segment, maxSize)
	s.markEndOfData(segment, offset)
	return kind
}

// fillFastRetransmission puts the earliest unacknowledged data into the
// segment, and clears the pending fast retransmission.
func (s *Socket) fillFastRetransmission(segment *wire.TCPRepr, maxSize int) {
	segment.SeqNumber = s.localSeqNumber
	segment.Payload = s.transmitBuffer.Allocated(0, min(maxSize, s.transmitBuffer.Len()))
	s.pendingFastRetransmit = false
}

// fillNextData puts the data after what is in flight into the segment, as
// much as the remote window allows, and returns the data's offset in the
// transmit buffer and the segment's kind: a zero-window probe when the
// window is closed and a probe is due, an ordinary segment otherwise.
func (s *Socket) fillNextData(now time.Time, segment *wire.TCPRepr, maxSize int) (int, segmentKind) {
	// Right edge of window, ie the max sequence number we're allowed to send.
	windowRightEdge := s.localSeqNumber.Add(s.remoteWindowLen)
	// Max amount of octets we're allowed to send according to the remote
	// window. It is 0 if we've sent some data and later the remote side has
	// shrunk its window so that data is no longer inside the window. This
	// should be very rare and is strongly discouraged by the RFCs, but it
	// does happen in practice.
	// http://www.tcpipguide.com/free/t_TCPWindowManagementIssues.htm
	windowLimit := 0
	if windowRightEdge.Compare(s.remoteLastSeq) >= 0 {
		windowLimit = windowRightEdge.Sub(s.remoteLastSeq)
	}
	// To send a zero-window-probe, force the window limit to at least 1 byte.
	kind := ordinarySegment
	if windowLimit == 0 && s.timer.shouldZeroWindowProbe(now) {
		windowLimit = 1
		kind = zeroWindowProbeSegment
	}
	offset := s.flightSize()
	segment.Payload = s.transmitBuffer.Allocated(offset, min(windowLimit, maxSize))
	return offset, kind
}

// markEndOfData follows the segment's data, if it is the last in the buffer,
// with the PSH or FIN flags, depending on whether the transmit half of the
// connection is open. offset is where the segment's data starts in the
// transmit buffer.
func (s *Socket) markEndOfData(segment *wire.TCPRepr, offset int) {
	if offset+len(segment.Payload) != s.transmitBuffer.Len() {
		return
	}
	switch {
	case s.wantsFIN():
		segment.Control = wire.TCPControlFIN
	case s.MaySend() && len(segment.Payload) > 0:
		segment.Control = wire.TCPControlPSH
	}
}

// makeKeepAlive turns an empty segment into a keep-alive when one is due, and
// reports whether it did. There might be more than one reason to send a
// packet. E.g. the keep-alive timer has expired, and we also have data in
// transmit buffer. Since any packet that occupies sequence space will elicit
// an ACK, we only need to send an explicit packet if we couldn't fill the
// sequence space with anything.
func (s *Socket) makeKeepAlive(now time.Time, segment *wire.TCPRepr) bool {
	if !s.timer.shouldKeepAlive(now) || !segment.IsEmpty() {
		return false
	}
	segment.SeqNumber = segment.SeqNumber.Add(-1)
	segment.Payload = keepAlivePayload()
	return true
}

// traceTransmission traces a summary of what will be sent.
func (s *Socket) traceTransmission(out transmission) {
	switch {
	case out.kind == keepAliveSegment:
		netlog.Trace("sending a keep-alive")
	case len(out.segment.Payload) > 0:
		netlog.Trace("tx buffer: sending octets", "octets", len(out.segment.Payload), "offset", s.flightSize())
	}
	if out.segment.Control != wire.TCPControlNone || len(out.segment.Payload) == 0 {
		netlog.Trace("sending", "flags", flagsOf(out.segment))
	}
}

// flagsOf names the flags of a segment the socket sends, for traces.
func flagsOf(segment wire.TCPRepr) string {
	if !segment.AckNumber.IsSome() {
		if segment.Control == wire.TCPControlSYN {
			return "SYN"
		}
		return "<unreachable>"
	}
	switch segment.Control {
	case wire.TCPControlSYN:
		return "SYN|ACK"
	case wire.TCPControlFIN:
		return "FIN|ACK"
	case wire.TCPControlRST:
		return "RST|ACK"
	case wire.TCPControlPSH:
		return "PSH|ACK"
	default:
		return "ACK"
	}
}

// recordTransmission updates the timers and, unless the segment carries a
// fake or probing octet, the internal state, now that the segment is sent.
func (s *Socket) recordTransmission(now time.Time, out transmission) {
	// We've sent something, whether useful data or a keep-alive packet, so
	// rewind the keep-alive timer.
	s.timer.rewindKeepAlive(now, s.keepAlive)
	s.stopDelayedACK()
	switch out.kind {
	case zeroWindowProbeSegment:
		// Leave the rest of the state intact if sending a zero-window probe.
		s.timer.rewindZeroWindowProbe(now)
	case keepAliveSegment:
		// Leave the rest of the state intact if sending a keep-alive packet,
		// since those carry a fake segment.
	default:
		s.recordSentSegment(now, out.segment)
	}
}

// recordSentSegment updates the internal state now that a packet has been
// sent successfully.
func (s *Socket) recordSentSegment(now time.Time, segment wire.TCPRepr) {
	// Use Max so a fast-retransmit segment (whose sequence number is
	// localSeqNumber, well behind the current frontier) doesn't rewind the
	// tracked "highest sent" sequence.
	segmentEnd := segment.SeqNumber.Add(segment.SegmentLen())
	s.remoteLastSeq = s.remoteLastSeq.Max(segmentEnd)
	s.remoteLastAck = segment.AckNumber
	s.remoteLastWindow = segment.WindowLen
	if segment.SegmentLen() > 0 {
		s.rttEstimator.onSend(now, segmentEnd)
		if !s.timer.isRetransmit() {
			// RFC 6298 (5.1) Every time a packet containing data is sent
			// (including a retransmission), if the timer is not running,
			// start it running so that it will expire after RTO seconds.
			s.timer.setForRetransmit(now, s.rttEstimator.retransmissionTimeout())
		}
	}
	if s.state == StateClosed {
		// When aborting a connection, forget about it after sending a single
		// RST packet.
		s.forgetTuple()
	}
}
