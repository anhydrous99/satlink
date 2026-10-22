package tcp

import (
	"math"
	"math/bits"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// windowFieldBits is the width of the window field of the TCP header. A
// receive buffer whose capacity needs more bits is announced with window
// scaling, shifted right by the excess.
const windowFieldBits = 16

// windowShiftFor returns the window scale the socket offers for a receive
// buffer of capacity octets: as many bits as the capacity needs beyond the
// window field, so that the whole buffer can be announced.
func windowShiftFor(capacity int) uint8 {
	return uint8(max(bits.Len(uint(capacity))-windowFieldBits, 0))
}

// windowField returns length as a value of the window field, saturating at
// the largest one.
func windowField(length int) uint16 {
	return uint16(min(length, math.MaxUint16))
}

// nextExpectedSeq returns the sequence number of the next octet the socket
// expects from the peer: the acknowledgement number it sends.
func (s *Socket) nextExpectedSeq() wire.TCPSeqNumber {
	return s.remoteSeqNumber.Add(s.receiveBuffer.Len())
}

// scaledWindow returns the current window field value, including scaling
// according to RFC 1323. It is used in internal calculations as well as
// packet generation.
func (s *Socket) scaledWindow() uint16 {
	return windowField(s.receiveBuffer.Window() >> s.remoteWindowShift)
}

// lastScaledWindow returns the last window field value, including scaling
// according to RFC 1323, and false if none was sent.
//
// Unlike remoteLastWindow, it takes into account new packets received (but
// not acknowledged) since the last window update and adjusts the window
// length accordingly. This ensures a fair comparison between the last window
// length and the new window length we're going to advertise.
//
// Nothing is left of that window once the peer's sequence numbers reach past
// its end. That happens when a peer's SYN reaches a connecting socket before
// the socket's own SYN has announced a window, or answers a SYN announcing a
// zero window (a socket without a receive buffer). The subtraction would
// underflow and panic then, so the socket returns 0 instead.
func (s *Socket) lastScaledWindow() (uint16, bool) {
	lastAck, ok := s.remoteLastAck.Get()
	if !ok {
		return 0, false
	}
	lastWindowEnd := lastAck.Add(int(s.remoteLastWindow) << s.remoteWindowShift)
	if lastWindowEnd.Compare(s.nextExpectedSeq()) < 0 {
		return 0, true
	}
	lastWindowAdjusted := lastWindowEnd.Sub(s.nextExpectedSeq())
	return windowField(lastWindowAdjusted >> s.remoteWindowShift), true
}

// significantWindowGrowth is how many times larger the receive window must
// have grown since it was last announced for a window update to go out at
// once. According to the Linux kernel implementation, "significant" means
// doubling the receive window
// (https://elixir.bootlin.com/linux/v6.9.9/source/net/ipv4/tcp.c#L1472).
const significantWindowGrowth = 2

// windowToUpdate reports whether we should send an ACK immediately due to a
// significant window update. ACKs with significant window updates should be
// sent immediately to let the sender know that more data can be sent.
func (s *Socket) windowToUpdate() bool {
	switch s.state {
	case StateSynSent, StateSynReceived, StateEstablished, StateFinWait1, StateFinWait2:
		newWindow := s.scaledWindow()
		lastWindow, ok := s.lastScaledWindow()
		return ok && newWindow > 0 && newWindow/significantWindowGrowth >= lastWindow
	default:
		return false
	}
}
