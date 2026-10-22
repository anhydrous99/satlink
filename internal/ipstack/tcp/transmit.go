package tcp

import (
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// seqToTransmit reports whether the socket has something that takes sequence
// space to send: a SYN, data, or a FIN.
func (s *Socket) seqToTransmit(cx *socket.Context) bool {
	// A fast retransmission goes out at once, whatever Nagle's algorithm or
	// the peer's window say.
	if s.pendingFastRetransmit && !s.transmitBuffer.IsEmpty() {
		return true
	}
	// If we want to send a SYN and we haven't done so, do it!
	if (s.state == StateSynSent || s.state == StateSynReceived) && !s.hasDataInFlight() {
		return true
	}
	return s.dataToTransmit(cx) || s.finToTransmit()
}

// hasDataInFlight reports whether we have sent data that hasn't been ACKed
// yet.
func (s *Socket) hasDataInFlight() bool {
	return s.remoteLastSeq != s.localSeqNumber
}

// flightSize returns the number of octets transmitted but not yet ACKed.
func (s *Socket) flightSize() int {
	return s.remoteLastSeq.Sub(s.localSeqNumber)
}

// dataToTransmit reports whether we can send at least one octet of data: the
// remote window has room for queued data, and Nagle's algorithm does not
// hold it back.
func (s *Socket) dataToTransmit(cx *socket.Context) bool {
	sendable := s.sendableOctets()
	// If we're applying the Nagle algorithm we don't want to send more until
	// one of:
	// * There's no data in flight
	// * We can send a full packet
	// * We have all the data we'll ever send (we're closing send)
	if s.nagleEnabled && s.hasDataInFlight() && sendable < effectiveMSS(cx, s.remoteMSS) && !s.wantsFIN() {
		return false
	}
	return sendable != 0
}

// sendableOctets returns the max amount of octets we can send: what is
// queued and fits into the remote window, less what is in flight.
func (s *Socket) sendableOctets() int {
	// max sequence number we can send.
	maxSendSeq := s.localSeqNumber.Add(min(s.remoteWindowLen, s.transmitBuffer.Len()))
	if maxSendSeq.Compare(s.remoteLastSeq) < 0 {
		return 0
	}
	return maxSendSeq.Sub(s.remoteLastSeq)
}

// localMSS returns the largest segment payload the socket's own link takes:
// the IP MTU without the IPv4 and TCP headers. It is also the MSS the
// socket's SYN announces, as RFC 6691 has the option count the fixed headers
// only, never IP or TCP options.
func localMSS(cx *socket.Context) int {
	return cx.IPMTU() - wire.IPv4HeaderLen - wire.TCPHeaderLen
}

// effectiveMSS returns the largest segment payload, taking into account the
// local and remote limits: the local MSS and the peer's. No TCP options
// shorten it, as this port never sends timestamps.
func effectiveMSS(cx *socket.Context, remoteMSS int) int {
	return min(localMSS(cx), remoteMSS)
}

// wantsFIN reports whether we have to send a FIN.
func (s *Socket) wantsFIN() bool {
	return s.state == StateFinWait1 || s.state == StateClosing || s.state == StateLastAck
}

// finToTransmit reports whether we can actually send the FIN. We can send it
// if:
//  1. We have unsent data that fits in the remote window.
//  2. We have no unsent data.
//
// This condition matches only if #2, because #1 is already covered by
// dataToTransmit, which seqToTransmit ORs with it.
func (s *Socket) finToTransmit() bool {
	return s.wantsFIN() && s.remoteLastSeq == s.localSeqNumber.Add(s.transmitBuffer.Len())
}

// ackToTransmit reports whether we have received data not yet acknowledged.
func (s *Socket) ackToTransmit() bool {
	lastAck, ok := s.remoteLastAck.Get()
	return ok && lastAck.Compare(s.nextExpectedSeq()) < 0
}

// timeoutDeadline returns when the timeout aborts the connection: timeout
// after the peer was last heard from or, after a quiet spell, after the
// Dispatch that restarted the clock (startTimeoutClock). It returns false
// when no timeout is set or the clock has yet to start.
func (s *Socket) timeoutDeadline() (time.Time, bool) {
	lastTimestamp, ok := s.remoteLastTimestamp.Get()
	if !ok || s.timeout <= 0 {
		return time.Time{}, false
	}
	return lastTimestamp.Add(s.timeout), true
}

// timedOut reports whether the peer has stayed silent for the timeout.
func (s *Socket) timedOut(now time.Time) bool {
	deadline, ok := s.timeoutDeadline()
	return ok && !now.Before(deadline)
}

// timeoutPollAt returns when the timeout expires, which it does whether the
// socket is sending, retransmitting or idle. It is on ingress only when no
// timeout is set or the clock has yet to start.
func (s *Socket) timeoutPollAt() socket.PollAt {
	if deadline, ok := s.timeoutDeadline(); ok {
		return socket.PollAtTime(deadline)
	}
	return socket.PollOnIngress()
}
