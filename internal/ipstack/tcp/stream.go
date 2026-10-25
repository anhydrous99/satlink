package tcp

import (
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// Send calls fill with the largest contiguous free area of the transmit
// buffer and queues as many octets as fill returns, which it also returns.
// It returns ErrInvalidState, without calling fill, unless MaySend holds. It
// panics if fill returns more than len(free).
func (s *Socket) Send(fill func(free []byte) int) (int, error) {
	return s.sendWith(func(buffer *SocketBuffer) int {
		return buffer.EnqueueManyWith(fill)
	})
}

// SendSlice queues as much of data as the transmit buffer has room for and
// returns how much that was, possibly 0. It returns ErrInvalidState unless
// MaySend holds.
func (s *Socket) SendSlice(data []byte) (int, error) {
	return s.sendWith(func(buffer *SocketBuffer) int {
		return buffer.EnqueueSlice(data)
	})
}

// sendWith lets enqueue add octets to the transmit buffer, and returns how
// many it added.
func (s *Socket) sendWith(enqueue func(buffer *SocketBuffer) int) (int, error) {
	if !s.MaySend() {
		return 0, ErrInvalidState
	}
	wasEmpty := s.transmitBuffer.IsEmpty()
	size := enqueue(s.transmitBuffer)
	if size == 0 {
		return 0, nil
	}
	if wasEmpty {
		// The connection might have been idle for a long time, and so
		// remoteLastTimestamp would be far in the past. Unless we clear it
		// here, we'll abort the connection down over in Dispatch by
		// erroneously detecting it as timed out.
		s.remoteLastTimestamp = wire.Optional[time.Time]{}
	}
	// If the remote window is zero and we go from having no data to some
	// data pending to send, start the zero window probe timer.
	if s.remoteWindowLen == 0 && s.timer.isIdle() {
		delay := s.rttEstimator.retransmissionTimeout()
		netlog.Trace("starting zero-window-probe timer", "delay", delay)
		// We don't have access to the current time here, so time the probe
		// from the epoch, long past on a real clock. This will cause the
		// first zero-window probe to be sent immediately, but that's okay.
		s.timer.setForZeroWindowProbe(time.Time{}, delay)
	}
	return size, nil
}

// Receive calls consume with the largest contiguous run of received octets
// and dequeues as many as consume returns, which it also returns. It fails
// like ReceiveSlice, without calling consume. It panics if consume returns
// more than len(data).
func (s *Socket) Receive(consume func(data []byte) int) (int, error) {
	return s.receiveWith(func(buffer *SocketBuffer) int {
		return buffer.DequeueManyWith(consume)
	})
}

// ReceiveSlice moves as many received octets into data as fit and returns how
// many that was, possibly 0. Unless MayReceive holds, it returns ErrFinished
// if the peer closed gracefully, in which case everything it sent has been
// read, and ErrInvalidState otherwise, in which case the data received may
// be incomplete. Data that arrives with a SYN is neither taken nor
// acknowledged, so the peer sends it again: the socket does not support TCP
// Fast Open. When this side closed before the peer did, the end of TIME-WAIT
// resets the socket (see Close): octets still unread then are discarded, and
// ReceiveSlice fails with ErrInvalidState.
func (s *Socket) ReceiveSlice(data []byte) (int, error) {
	return s.receiveWith(func(buffer *SocketBuffer) int {
		return buffer.DequeueSlice(data)
	})
}

// receiveWith lets dequeue take octets from the receive buffer, and returns
// how many it took.
func (s *Socket) receiveWith(dequeue func(buffer *SocketBuffer) int) (int, error) {
	if err := s.checkReceivable(); err != nil {
		return 0, err
	}
	size := dequeue(s.receiveBuffer)
	s.remoteSeqNumber = s.remoteSeqNumber.Add(size)
	return size, nil
}

// checkReceivable returns the error a receive fails with, or nil while
// MayReceive holds: the receive half is open, or data is still buffered.
func (s *Socket) checkReceivable() error {
	// Before the connection is established there is nothing to read. The
	// socket takes no data that arrives with a SYN (it does not support TCP
	// Fast Open).
	if s.MayReceive() {
		return nil
	}
	if s.finReceived {
		return ErrFinished
	}
	return ErrInvalidState
}

// SendQueue returns how many octets wait in the transmit buffer: queued but
// not yet acknowledged by the peer.
func (s *Socket) SendQueue() int {
	return s.transmitBuffer.Len()
}
