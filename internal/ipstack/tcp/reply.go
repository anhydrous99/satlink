package tcp

import (
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// challengeACKInterval rate-limits challenge ACKs to 1 per second max, as
// RFC 5961 (7) asks: they exist to break ACK loops between desynchronised
// peers, and must not sustain one.
const challengeACKInterval = time.Second

// reply is a segment to send at once in answer to one received, with its IP
// header.
type reply struct {
	ip      wire.IPv4Repr
	segment wire.TCPRepr
}

// newReply returns an empty segment answering segment, which arrived in ip:
// the ports and addresses swapped, no flags, sequence number 0, no
// acknowledgement and a zero window.
func newReply(ip wire.IPv4Repr, segment wire.TCPRepr) reply {
	answer := wire.TCPRepr{SrcPort: segment.DstPort, DstPort: segment.SrcPort}
	return reply{
		ip: wire.IPv4Repr{
			SrcAddr:    ip.DstAddr,
			DstAddr:    ip.SrcAddr,
			NextHeader: wire.IPProtocolTCP,
			PayloadLen: answer.BufferLen(),
			HopLimit:   wire.DefaultHopLimit,
		},
		segment: answer,
	}
}

// rstReply returns the RST that answers segment, which must not itself be a
// RST.
func rstReply(ip wire.IPv4Repr, segment wire.TCPRepr) reply {
	rst := newReply(ip, segment)
	// See https://www.snellman.net/blog/archive/2016-02-01-tcp-rst/ for
	// explanation of why we sometimes send an RST and sometimes an RST|ACK
	rst.segment.Control = wire.TCPControlRST
	rst.segment.SeqNumber = segment.AckNumber.Or(0)
	if segment.Control == wire.TCPControlSYN && !segment.AckNumber.IsSome() {
		rst.segment.AckNumber = wire.Some(segment.SeqNumber.Add(segment.SegmentLen()))
	}
	return rst
}

// ackReply returns an ACK answering segment, with the window and, if the peer
// supports it, the SACK block the socket has now, and records it as the
// last acknowledgement and window sent.
func (s *Socket) ackReply(ip wire.IPv4Repr, segment wire.TCPRepr) reply {
	ack := newReply(ip, segment)
	// From RFC 793: [...] an empty acknowledgment segment containing the
	// current send-sequence number and an acknowledgment indicating the next
	// sequence number expected to be received.
	ack.segment.SeqNumber = s.remoteLastSeq
	ack.segment.AckNumber = wire.Some(s.nextExpectedSeq())
	s.remoteLastAck = ack.segment.AckNumber
	// From RFC 1323: The window field [...] of every outgoing segment, with
	// the exception of SYN segments, is right-shifted by [advertised scale
	// value] bits[...]
	ack.segment.WindowLen = s.scaledWindow()
	s.remoteLastWindow = ack.segment.WindowLen
	// If the remote supports selective acknowledgement, add the option to the
	// outgoing segment.
	if s.remoteHasSACK {
		netlog.Debug("sending sACK option with current assembler ranges")
		ack.segment.SACKRanges[0] = s.sackRange(s.nextExpectedSeq())
	}
	// Since the sACK option may have changed the length of the payload,
	// update that.
	ack.ip.PayloadLen = ack.segment.BufferLen()
	return ack
}

// sackRange returns the SACK block to send with an acknowledgement of
// ackNumber, if any data beyond it has arrived.
//
// RFC 2018: The first SACK block (i.e., the one immediately following the
// kind and length fields in the option) MUST specify the contiguous block of
// data containing the segment which triggered this ACK, unless that segment
// advanced the Acknowledgment Number field in the header.
//
// When the matching segment was removed from the assembler, meaning the
// acknowledgement number has advanced, or there was no previous SACK, the
// block is the one with the lowest sequence numbers. While the RFC says we
// SHOULD keep a list of reported SACK ranges, and iterate through those, that
// is currently infeasible; the lowest block hints at what segments would most
// quickly advance the acknowledgement number.
func (s *Socket) sackRange(ackNumber wire.TCPSeqNumber) wire.Optional[wire.SACKRange] {
	lastSeq, haveLastSeq := s.localReceiveLastSeq.Get()
	var lowest wire.Optional[wire.SACKRange]
	for start, end := range s.assembler.DataRanges() {
		left, right := ackNumber.Add(start), ackNumber.Add(end)
		block := wire.Some(wire.SACKRange{Left: uint32(left), Right: uint32(right)})
		if haveLastSeq && left.Compare(lastSeq) <= 0 && right.Compare(lastSeq) >= 0 {
			return block
		}
		if !lowest.IsSome() {
			lowest = block
		}
	}
	return lowest
}

// challengeACKReply returns the ACK that answers an unacceptable segment,
// unless one went out less than challengeACKInterval ago.
func (s *Socket) challengeACKReply(cx *socket.Context, ip wire.IPv4Repr, segment wire.TCPRepr) (reply, bool) {
	if cx.Now().Before(s.challengeACKTimer) {
		return reply{}, false
	}
	s.challengeACKTimer = cx.Now().Add(challengeACKInterval)
	return s.ackReply(ip, segment), true
}
