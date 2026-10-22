package tcp

import (
	"math"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// fastRetransmitThreshold is the number of duplicate ACKs in a row that make
// the socket retransmit the earliest unacknowledged segment at once
// (RFC 5681 § 3.2).
const fastRetransmitThreshold = 3

// arrival is a segment that Process is working through, with what Process
// has found out about it so far; Process's steps share it.
type arrival struct {
	cx      *socket.Context
	ip      wire.IPv4Repr
	segment wire.TCPRepr
	// unacknowledged are the control flags the socket sent that the peer has
	// yet to acknowledge, which take sequence space in front of or after the
	// transmit buffer.
	unacknowledged controlFlags
	// windowStart and windowEnd bound the receive window last advertised: the
	// sequence numbers the socket accepts data for.
	windowStart, windowEnd wire.TCPSeqNumber
	// payload is the part of the segment's payload inside the receive window,
	// and payloadOffset its distance from windowStart.
	payload       []byte
	payloadOffset int
	// control is the segment's control flag as the socket acts on it.
	control wire.TCPControl
	// acknowledged is what the segment acknowledges of what the socket sent.
	acknowledged acknowledgement
}

// controlFlags says which of SYN and FIN a socket has sent.
type controlFlags struct {
	syn, fin bool
}

// sequenceLen returns how much sequence space the flags take: one number
// each.
func (c controlFlags) sequenceLen() int {
	length := 0
	if c.syn {
		length++
	}
	if c.fin {
		length++
	}
	return length
}

// acknowledgement is what a segment acknowledges of what the socket sent.
type acknowledgement struct {
	// length is how many octets of the transmit buffer it acknowledges.
	length int
	// ofFIN says whether it acknowledges the socket's FIN too.
	ofFIN bool
	// all says whether it acknowledges everything sent.
	all bool
}

// outcome is what a step of Process decided about a segment: carry on with
// it, or stop processing it, perhaps sending a reply.
type outcome struct {
	done     bool
	replying bool
	reply    reply
}

func carryOn() outcome {
	return outcome{}
}

func dropSegment() outcome {
	return outcome{done: true}
}

func respond(r reply) outcome {
	return outcome{done: true, replying: true, reply: r}
}

func (o outcome) result() (reply, bool) {
	return o.reply, o.replying
}

// challengeACK stops processing the segment, answering it with a challenge
// ACK unless the rate limit withholds one.
func (s *Socket) challengeACK(a *arrival) outcome {
	if answer, ok := s.challengeACKReply(a.cx, a.ip, a.segment); ok {
		return respond(answer)
	}
	return dropSegment()
}

// process does the work of Process, returning the reply, if any, in one
// piece.
func (s *Socket) process(cx *socket.Context, ip wire.IPv4Repr, segment wire.TCPRepr) (reply, bool) {
	a := &arrival{cx: cx, ip: ip, segment: segment, unacknowledged: s.unacknowledgedControl()}
	if o := s.screenAcknowledgement(a); o.done {
		return o.result()
	}
	s.locateReceiveWindow(a)
	if o := s.trimToReceiveWindow(a); o.done {
		return o.result()
	}
	a.acknowledged = s.acknowledgementOf(a)
	a.control = s.effectiveControl(a)
	if o := s.transition(a); o.done {
		return o.result()
	}
	s.updateRemoteState(a)
	s.updateTimers(a)
	return s.receivePayload(a)
}

// unacknowledgedControl returns the control flags we sent that the peer has
// yet to acknowledge: by them, the sequence number space differs from the
// transmit buffer space.
func (s *Socket) unacknowledgedControl() controlFlags {
	switch s.state {
	case StateSynSent, StateSynReceived:
		// In SYN-SENT or SYN-RECEIVED, we've just sent a SYN.
		return controlFlags{syn: true}
	case StateFinWait1, StateLastAck, StateClosing:
		// In FIN-WAIT-1, LAST-ACK, or CLOSING, we've just sent a FIN.
		return controlFlags{fin: true}
	default:
		// In all other states we've already got acknowledgements for all of
		// the control flags we sent.
		return controlFlags{}
	}
}

// screenAcknowledgement rejects unacceptable acknowledgements.
func (s *Socket) screenAcknowledgement(a *arrival) outcome {
	ackNumber, hasACK := a.segment.AckNumber.Get()
	switch {
	case a.segment.Control == wire.TCPControlRST:
		return s.screenRST(a)
	case s.state == StateListen:
		if hasACK {
			panic("tcp: Process given a segment with an ACK in LISTEN, which Accepts refuses")
		}
		// The initial SYN cannot contain an acknowledgement.
		return carryOn()
	case s.state == StateSynSent:
		return s.screenSynSentSegment(a)
	case !hasACK:
		// Every packet after the initial SYN must be an acknowledgement.
		netlog.Debug("expecting an ACK")
		return dropSegment()
	case s.state == StateSynReceived:
		// ACK in the SYN-RECEIVED state must have the exact ACK number, or we
		// RST it.
		if ackNumber != s.localSeqNumber.Add(1) {
			netlog.Debug("unacceptable ACK in response to SYN|ACK")
			return respond(rstReply(a.ip, a.segment))
		}
		return carryOn()
	default:
		return s.screenACKRange(a, ackNumber)
	}
}

// screenRST accepts a RST received in response to the initial SYN only if it
// acknowledges the initial SYN. Any other RST need only have a valid sequence
// number.
func (s *Socket) screenRST(a *arrival) outcome {
	if s.state != StateSynSent {
		return carryOn()
	}
	ackNumber, hasACK := a.segment.AckNumber.Get()
	switch {
	case !hasACK:
		netlog.Debug("unacceptable RST (expecting RST|ACK) in response to initial SYN")
		return dropSegment()
	case ackNumber != s.localSeqNumber.Add(1):
		netlog.Debug("unacceptable RST|ACK in response to initial SYN")
		return dropSegment()
	default:
		return carryOn()
	}
}

// screenSynSentSegment accepts only a SYN in the SYN-SENT state.
func (s *Socket) screenSynSentSegment(a *arrival) outcome {
	ackNumber, hasACK := a.segment.AckNumber.Get()
	acksOurSYN := hasACK && ackNumber == s.localSeqNumber.Add(1)
	switch {
	case a.segment.Control == wire.TCPControlSYN && hasACK && !acksOurSYN:
		// SYN|ACK in the SYN-SENT state must have the exact ACK number.
		netlog.Debug("unacceptable SYN|ACK in response to initial SYN")
		return respond(rstReply(a.ip, a.segment))
	case a.segment.Control == wire.TCPControlSYN:
		// A SYN|ACK, or a SYN alone: TCP simultaneous open. This is required
		// by RFC 9293, which states "A TCP implementation MUST support
		// simultaneous open attempts (MUST-10)."
		return carryOn()
	case a.segment.Control == wire.TCPControlNone && acksOurSYN:
		// ACKs in the SYN-SENT state are invalid. If the sequence number
		// matches, ignore it instead of RSTing. It may be a workaround for
		// broken TCP servers, or a defense against reordering. Either way, if
		// Linux does it, we do too.
		netlog.Debug("expecting a SYN|ACK, received an ACK with the right ack_number, ignoring.")
		return dropSegment()
	case a.segment.Control == wire.TCPControlNone && hasACK:
		netlog.Debug("expecting a SYN|ACK, received an ACK with the wrong ack_number, sending RST.")
		return respond(rstReply(a.ip, a.segment))
	default:
		// Anything else in the SYN-SENT state is invalid.
		netlog.Debug("expecting a SYN|ACK")
		return dropSegment()
	}
}

// screenACKRange accepts an acknowledgement only for transmitted but
// unacknowledged data.
func (s *Socket) screenACKRange(a *arrival, ackNumber wire.TCPSeqNumber) outcome {
	// Acceptable ACK range (both inclusive)
	ackMin := s.localSeqNumber
	ackMax := s.localSeqNumber.Add(s.transmitBuffer.Len() + a.unacknowledged.sequenceLen())
	// If we have sent a SYN, it MUST be acknowledged.
	if a.unacknowledged.syn {
		ackMin = ackMin.Add(1)
	}
	switch {
	case ackNumber.Compare(ackMin) < 0:
		netlog.Debug("duplicate ACK", "ack", ackNumber, "min", ackMin, "max", ackMax)
		return dropSegment()
	case ackNumber.Compare(ackMax) > 0:
		netlog.Debug("unacceptable ACK", "ack", ackNumber, "min", ackMin, "max", ackMax)
		return s.challengeACK(a)
	default:
		return carryOn()
	}
}

// locateReceiveWindow finds the receive window last advertised, cut back to
// the free space of the receive buffer.
func (s *Socket) locateReceiveWindow(a *arrival) {
	a.windowStart = s.nextExpectedSeq()
	a.windowEnd = a.windowStart
	if lastAck, ok := s.remoteLastAck.Get(); ok {
		a.windowEnd = lastAck.Add(int(s.remoteLastWindow) << s.remoteWindowShift)
	}
	// A SYN announces its window unscaled, yet the socket reads every window
	// it sent with its own scale, as smoltcp does. With a receive buffer that
	// needs a scale, the window of its SYN therefore reaches beyond the buffer
	// until the socket sends its next segment. smoltcp takes data up to there,
	// and queuing it panics or hands the application octets the peer never
	// sent; the port takes no more than the buffer can hold
	// (PORTING-ipstack.md, section 9).
	if bufferEnd := a.windowStart.Add(s.receiveBuffer.Window()); a.windowEnd.Compare(bufferEnd) > 0 {
		a.windowEnd = bufferEnd
	}
}

// trimToReceiveWindow keeps the part of the segment's payload that falls
// inside the receive window, and notes the segment as the latest one
// received, or answers a segment outside it. In LISTEN and SYN-SENT states,
// we have not yet synchronized with the remote end, so the socket takes no
// payload.
func (s *Socket) trimToReceiveWindow(a *arrival) outcome {
	if s.state == StateListen || s.state == StateSynSent {
		return carryOn()
	}
	if !a.segmentInWindow() {
		return s.answerOutOfWindowSegment(a)
	}
	segmentStart := a.segment.SeqNumber
	overlapStart := a.windowStart.Max(segmentStart)
	overlapEnd := a.windowEnd.Min(a.segmentEnd())
	// The first SACK block of the ACKs that follow names the run of data that
	// holds this segment (sackRange).
	s.localReceiveLastSeq = wire.Some(segmentStart)
	a.payload = a.segment.Payload[overlapStart.Sub(segmentStart):overlapEnd.Sub(segmentStart)]
	a.payloadOffset = overlapStart.Sub(a.windowStart)
	return carryOn()
}

func (a *arrival) segmentEnd() wire.TCPSeqNumber {
	return a.segment.SeqNumber.Add(len(a.segment.Payload))
}

// segmentInWindow runs the segment acceptability tests of RFC 9293
// (https://www.rfc-editor.org/rfc/rfc9293.html#name-segment-acceptability-tests).
func (a *arrival) segmentInWindow() bool {
	if a.segment.SeqNumber == a.segmentEnd() {
		return a.emptySegmentInWindow()
	}
	if a.windowStart == a.windowEnd {
		netlog.Debug("non-zero-length segment with zero receive window, will only send an ACK")
		return false
	}
	start, end := a.segment.SeqNumber, a.segmentEnd()
	startsInside := a.windowStart.Compare(start) <= 0 && start.Compare(a.windowEnd) < 0
	endsInside := a.windowStart.Compare(end) < 0 && end.Compare(a.windowEnd) <= 0
	if !a.windowEndsBeforeItStarts() && (startsInside || endsInside) {
		return true
	}
	netlog.Debug("segment not in receive window, will send challenge ACK",
		"segment_start", start, "segment_end", end, "window_start", a.windowStart, "window_end", a.windowEnd)
	return false
}

func (a *arrival) emptySegmentInWindow() bool {
	start := a.segment.SeqNumber
	switch {
	case start == a.windowStart.Add(-1):
		netlog.Debug("received a keep-alive or window probe packet, will send an ACK")
		return false
	case a.windowStart == a.windowEnd:
		if start == a.windowStart {
			return true
		}
		netlog.Debug("zero-length segment not inside zero-length window, will send an ACK.")
		return false
	case !a.windowEndsBeforeItStarts() && a.windowStart.Compare(start) <= 0 && start.Compare(a.windowEnd) < 0:
		return true
	default:
		netlog.Debug("zero-length segment not inside window, will send an ACK.")
		return false
	}
}

// windowEndsBeforeItStarts reports whether the receive window ends before it
// starts. It does, by one sequence number, when the peer's SYN or FIN takes
// the first sequence number beyond the window last announced, until the
// socket's next segment announces one afresh: a SYN that reaches a connecting
// socket before the socket's own SYN has announced a window, or that answers
// a SYN announcing a zero window (a socket without a receive buffer), and a
// FIN right at the window's end. Such a window holds no sequence number, but
// the acceptability tests, which compare modulo 2³², take a segment that
// starts or ends 2³¹ − 1 or 2³¹ after the window's start for one inside it:
// those numbers lie 2³¹ from the window's end or its start, and of two
// numbers 2³¹ apart, each compares as before the other. smoltcp then panics,
// places the segment's data far beyond the receive buffer, or takes a RST
// from there and ends the connection; the port refuses the segment
// (PORTING-ipstack.md, section 9).
func (a *arrival) windowEndsBeforeItStarts() bool {
	return a.windowEnd.Compare(a.windowStart) < 0
}

// answerOutOfWindowSegment answers a segment that failed the acceptability
// tests.
func (s *Socket) answerOutOfWindowSegment(a *arrival) outcome {
	// Out-of-window RSTs are silently dropped, per RFC 9293 (3.10.7.4) and
	// RFC 5961 (3.2): no reply is sent, and the TIME-WAIT timer below is not
	// refreshed. RST senders don't need a reply to make progress.
	if a.segment.Control == wire.TCPControlRST {
		netlog.Debug("dropping out-of-window RST")
		return dropSegment()
	}
	// If we're in the TIME-WAIT state, restart the TIME-WAIT timeout, since
	// the remote end may not have realized we've closed the connection.
	if s.state == StateTimeWait {
		s.timer.setForClose(a.cx.Now())
	}
	// Segments carrying data are exempt from challenge ACK rate limiting: an
	// out-of-window data segment is a retransmission whose ACK was lost, or a
	// window probe, and per RFC 9293 (3.10.7.4, 3.8.6.1) it should elicit an
	// ACK so the remote can make progress. Withholding these ACKs strands the
	// remote in retransmission backoff or persist state. The rate limit
	// exists to break ACK loops between desynced peers, and exempting data
	// segments cannot sustain such a loop: the remote paces them with its
	// retransmission and persist timers, and the data it may send in response
	// to a duplicate ACK of ours (fast recovery) is bounded by its send
	// window, which never advances during a desync.
	//
	// The exemption covers FIN (a retransmitted final segment is the same
	// lost-ACK situation) but not SYN: a SYN in a synchronized state is a
	// challenge ACK situation (RFC 5961 4.2), and challenge ACKs should be
	// throttled (RFC 5961 7). One per second is ample for a restarted peer to
	// complete the challenge exchange, since it retransmits its SYN on its
	// own timer.
	if len(a.segment.Payload) > 0 && isDataControl(a.segment.Control) {
		return respond(s.ackReply(a.ip, a.segment))
	}
	return s.challengeACK(a)
}

// isDataControl reports whether a segment with this control flag counts as a
// data segment for the exemption from challenge ACK rate limiting: no flag,
// PSH or FIN, but neither SYN nor RST.
func isDataControl(control wire.TCPControl) bool {
	switch control {
	case wire.TCPControlNone, wire.TCPControlPSH, wire.TCPControlFIN:
		return true
	default:
		return false
	}
}

// acknowledgementOf computes the amount of acknowledged octets, removing the
// SYN and FIN bits from the sequence space.
func (s *Socket) acknowledgementOf(a *arrival) acknowledgement {
	ackNumber, hasACK := a.segment.AckNumber.Get()
	if a.segment.Control == wire.TCPControlRST || !hasACK {
		return acknowledgement{}
	}
	// Sequence number corresponding to the first byte in the transmit
	// buffer. This normally equals localSeqNumber, but is 1 higher if we
	// have sent a SYN, as the SYN occupies 1 sequence number "before" the
	// data.
	bufferStart := s.localSeqNumber
	if a.unacknowledged.syn {
		bufferStart = bufferStart.Add(1)
	}
	if ackNumber.Compare(bufferStart) < 0 {
		return acknowledgement{}
	}
	acknowledged := acknowledgement{
		length: ackNumber.Sub(bufferStart),
		all:    s.remoteLastSeq.Compare(ackNumber) <= 0,
	}
	// We could've sent data before the FIN, so only remove FIN from the
	// sequence space if all of that data is acknowledged.
	if a.unacknowledged.fin && s.transmitBuffer.Len()+1 == acknowledged.length {
		acknowledged.length--
		acknowledged.ofFIN = true
		netlog.Trace("received ACK of FIN")
	}
	return acknowledged
}

// effectiveControl disregards control flags we don't care about or shouldn't
// act on yet: PSH, and a FIN received at the end of the current segment while
// the socket does not hold all the data before it, because of a hole in the
// assembler before the current segment or, in a state that takes a FIN,
// because the receive window cut the segment's data short.
func (s *Socket) effectiveControl(a *arrival) wire.TCPControl {
	control := a.segment.Control.QuashPSH()
	if control != wire.TCPControlFIN {
		return control
	}
	switch {
	case a.windowStart.Compare(a.segment.SeqNumber) < 0:
		netlog.Trace("ignoring FIN because we don't have full data yet",
			"window_start", a.windowStart, "segment_start", a.segment.SeqNumber)
		return wire.TCPControlNone
	case a.windowEnd.Compare(a.segmentEnd()) < 0 && s.takesFIN():
		// trimToReceiveWindow took the segment's data only up to the window's
		// end, and the FIN follows the octets it cut off. smoltcp takes the
		// FIN anyway, ending the stream early and acknowledging the first
		// octet cut off as delivered; RFC 9293 (3.10.7.4) trims a segment to
		// the window "including SYN and FIN" (PORTING-ipstack.md, section 9).
		// The peer sends the FIN again with the rest of its data. A FIN right
		// at the window's end is still taken: it needs no room in the buffer
		// (smoltcp's test_zero_window_fin). A state that takes no FIN keeps
		// it, and transition drops the segment whole, as smoltcp does.
		netlog.Trace("ignoring FIN because its data was cut at the receive window's end",
			"window_end", a.windowEnd, "segment_end", a.segmentEnd())
		return wire.TCPControlNone
	default:
		return control
	}
}

// updateRemoteState takes what the segment says about the peer: that it is
// alive, its window, and what it acknowledges.
func (s *Socket) updateRemoteState(a *arrival) {
	s.remoteLastTimestamp = wire.Some(a.cx.Now())
	isWindowUpdate := s.updateRemoteWindow(a.segment)
	if a.acknowledged.length > 0 {
		// Dequeue acknowledged octets.
		netlog.Trace("tx buffer: dequeueing octets",
			"octets", a.acknowledged.length, "now", s.transmitBuffer.Len()-a.acknowledged.length)
		s.transmitBuffer.DequeueAllocated(a.acknowledged.length)
	}
	ackNumber, hasACK := a.segment.AckNumber.Get()
	if !hasACK {
		return
	}
	if s.isDuplicateACK(a, ackNumber, isWindowUpdate) {
		s.countDuplicateACK(ackNumber)
	} else {
		s.acceptFreshACK(a, ackNumber)
	}
	// We've processed everything in the incoming segment, so advance the
	// local sequence number past it.
	s.localSeqNumber = ackNumber
	// During retransmission, if an earlier segment got lost but later was
	// successfully received, localSeqNumber can move past remoteLastSeq. Do
	// not attempt to retransmit the latter segments; not only this is
	// pointless in theory but also impossible in practice, since they have
	// been already deallocated from the buffer.
	if s.remoteLastSeq.Compare(s.localSeqNumber) < 0 {
		s.remoteLastSeq = s.localSeqNumber
	}
}

// updateRemoteWindow takes the window the segment announces, and reports
// whether it differs from the one known. RFC 1323: The window field (SEG.WND)
// in the header of every incoming segment, with the exception of SYN
// segments, is left-shifted by Snd.Wind.Scale bits before updating SND.WND.
func (s *Socket) updateRemoteWindow(segment wire.TCPRepr) bool {
	var scale uint8
	if segment.Control != wire.TCPControlSYN {
		scale = s.remoteWindowScale.Or(0)
	}
	windowLen := int(segment.WindowLen) << scale
	isWindowUpdate := windowLen != s.remoteWindowLen
	s.remoteWindowLen = windowLen
	return isWindowUpdate
}

// isDuplicateACK reports whether the segment is a duplicate ACK: one without
// payload that acknowledges nothing new while data is in flight, and does
// not move the send window.
func (s *Socket) isDuplicateACK(a *arrival, ackNumber wire.TCPSeqNumber, isWindowUpdate bool) bool {
	lastAck, ok := s.localReceiveLastAck.Get()
	return ok &&
		len(a.segment.Payload) == 0 &&
		lastAck == ackNumber &&
		ackNumber.Compare(s.remoteLastSeq) < 0 &&
		!isWindowUpdate
}

// countDuplicateACK increments the duplicate ACK count, saturating, and sets
// the timer for a fast retransmission on the third.
func (s *Socket) countDuplicateACK(ackNumber wire.TCPSeqNumber) {
	if s.localReceiveDuplicateACKs < math.MaxUint8 {
		s.localReceiveDuplicateACKs++
	}
	netlog.Debug("received duplicate ACK", "seq", ackNumber,
		"duplicate", s.localReceiveDuplicateACKs, "saturated", s.localReceiveDuplicateACKs == math.MaxUint8)
	if s.localReceiveDuplicateACKs == fastRetransmitThreshold {
		s.timer.setForFastRetransmit()
		netlog.Debug("started fast retransmit")
	}
}

// acceptFreshACK resets the duplicate ACK count, as a fresh ACK is no
// duplicate, and lets the RTT estimator take its measurement.
func (s *Socket) acceptFreshACK(a *arrival, ackNumber wire.TCPSeqNumber) {
	if s.localReceiveDuplicateACKs > 0 {
		s.localReceiveDuplicateACKs = 0
		netlog.Debug("reset duplicate ACK count")
	}
	s.localReceiveLastAck = wire.Some(ackNumber)
	s.rttEstimator.onACK(a.cx.Now(), ackNumber)
}

// updateTimers restarts or stops the retransmission, keep-alive and
// zero-window-probe timers after a segment.
func (s *Socket) updateTimers(a *arrival) {
	now := a.cx.Now()
	switch {
	case s.timer.isRetransmit() && a.acknowledged.all:
		// RFC 6298: (5.2) ACK of all outstanding data turn off the retransmit
		// timer.
		s.timer.setForIdle(now, s.keepAlive)
	case s.timer.isRetransmit() && a.acknowledged.length > 0:
		// (5.3) ACK of new data in ESTABLISHED state restart the retransmit
		// timer.
		s.timer.setForRetransmit(now, s.rttEstimator.retransmissionTimeout())
	case s.timer.isIdle():
		// any packet on idle refresh the keepalive timer.
		s.timer.setForIdle(now, s.keepAlive)
	}
	s.updateZeroWindowProbeTimer(a)
}

// updateZeroWindowProbeTimer starts or stops the zero-window-probe timer as
// the peer's window closes or opens.
func (s *Socket) updateZeroWindowProbeTimer(a *arrival) {
	now := a.cx.Now()
	if s.remoteWindowLen == 0 && !s.transmitBuffer.IsEmpty() && (s.timer.isIdle() || a.acknowledged.length > 0) {
		delay := s.rttEstimator.retransmissionTimeout()
		netlog.Trace("starting zero-window-probe timer", "delay", delay)
		s.timer.setForZeroWindowProbe(now, delay)
	}
	if s.remoteWindowLen != 0 && s.timer.isZeroWindowProbe() {
		netlog.Trace("stopping zero-window-probe timer")
		s.timer.setForIdle(now, s.keepAlive)
	}
}
