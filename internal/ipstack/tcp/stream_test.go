package tcp

// Tests of the application's side of the stream: receiving and sending.

import (
	"errors"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// =========================================================================
// Receiving
// =========================================================================

func TestReceiveSliceReturnsZeroWhenNothingIsBuffered(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	n, err := s.ReceiveSlice(make([]byte, 8))

	if n != 0 || err != nil {
		t.Errorf("ReceiveSlice = %d, %v; want 0, nil", n, err)
	}
}

func TestReceiveConsumesWhatTheCallbackTakes(t *testing.T) {
	t.Parallel()
	s := socketWithReceivedData(t)

	taken := mustReceive(t, s, func(data []byte) int { return 2 })

	if taken != 2 || s.receiveBuffer.Len() != 4 || s.remoteSeqNumber != remoteSeq.Add(1+2) {
		t.Errorf("took %d, %d left, remote sequence %v; want 2, 4, %v",
			taken, s.receiveBuffer.Len(), s.remoteSeqNumber, remoteSeq.Add(1+2))
	}
}

func TestReceiveFailsWithoutCallingTheCallback(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	called := false

	_, err := s.Receive(func([]byte) int { called = true; return 0 })

	if !errors.Is(err, ErrInvalidState) || called {
		t.Errorf("Receive = %v, callback called %v; want %v and not called", err, called, ErrInvalidState)
	}
}

// The socket does not support TCP Fast Open: it takes no data that arrives
// with a SYN and acknowledges the SYN alone, so the peer sends the data
// again, which the socket then takes.
func TestDataArrivingWithASYNIsNeitherTakenNorAcknowledged(t *testing.T) {
	t.Parallel()
	const early = "early"
	tests := []struct {
		name string
		// handshake returns an established socket whose peer's SYN carried
		// early; it checks that the socket acknowledged the SYN alone.
		handshake func(t *testing.T) *testSocket
	}{
		{"passive open", func(t *testing.T) *testSocket {
			s := socketListen(t)
			send(t, s, remoteSYN(withPayload([]byte(early))))
			receive(t, s, synACK())
			send(t, s, ackFromRemote(0))
			return s
		}},
		{"active open", func(t *testing.T) *testSocket {
			s := socketSynSent(t)
			receive(t, s, initialSYN())
			send(t, s, remoteSYN(withAck(localSeq.Add(1)), withPayload([]byte(early))))
			receive(t, s, ackFromLocal(0, defaultBufferLen))
			return s
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.handshake(t)
			assertState(t, s, StateEstablished)
			compareField(t, "receiveBuffer.Len()", s.receiveBuffer.Len(), 0)

			send(t, s, dataFromRemote(0, early))

			receiveExactly(t, s, early)
		})
	}
}

// When this side closed before the peer did, the end of TIME-WAIT resets the
// socket: what the application has yet to read is lost, and receiving fails
// as for a connection cut short, not as for the end of the stream.
func TestTheEndOfTimeWaitDiscardsUnreadData(t *testing.T) {
	t.Parallel()
	const response = "response"
	s := socketFinWait2(t)
	send(t, s, dataFromRemote(0, response, withControl(wire.TCPControlFIN), withAck(localSeq.Add(1+1))))
	assertState(t, s, StateTimeWait)
	receive(t, s, ackFromLocal(len(response)+1, uint16(defaultBufferLen-len(response)), withSeq(localSeq.Add(1+1))))
	receivePrefix(t, s, "resp")

	receiveNothing(t, s.at(closeDelay))

	assertState(t, s, StateClosed)
	compareField(t, "receiveBuffer.Len()", s.receiveBuffer.Len(), 0)
	if _, err := s.ReceiveSlice(make([]byte, len(response))); !errors.Is(err, ErrInvalidState) {
		t.Errorf("ReceiveSlice after TIME-WAIT = %v, want %v", err, ErrInvalidState)
	}
}

// =========================================================================
// Sending
// =========================================================================

func TestSendRefusedUnlessTheTransmitHalfIsOpen(t *testing.T) {
	t.Parallel()
	transmitHalfOpen := map[State]bool{StateEstablished: true, StateCloseWait: true}
	for state, s := range socketsInEveryState(t) {
		filled := false

		_, sendErr := s.Send(func([]byte) int { filled = true; return 0 })
		_, sliceErr := s.SendSlice([]byte("abc"))

		wantErr := ErrInvalidState
		if transmitHalfOpen[state] {
			wantErr = nil
		}
		if !errors.Is(sendErr, wantErr) || !errors.Is(sliceErr, wantErr) || filled != transmitHalfOpen[state] {
			t.Errorf("%v: Send %v (fill called %v), SendSlice %v; want %v (fill called %v)",
				state, sendErr, filled, sliceErr, wantErr, transmitHalfOpen[state])
		}
	}
}

func TestSendQueuesWhatTheCallbackFills(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	queued, err := s.Send(func(free []byte) int { return copy(free, "abcd") })

	if queued != 4 || err != nil || s.SendQueue() != 4 {
		t.Errorf("Send = %d, %v with %d queued; want 4, nil, 4", queued, err, s.SendQueue())
	}
}

func TestSendSliceQueuesNothingWhenTheBufferIsFull(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, make([]byte, defaultBufferLen))

	queued, err := s.SendSlice([]byte("more"))

	if queued != 0 || err != nil || s.CanSend() {
		t.Errorf("SendSlice on a full buffer = %d, %v (CanSend %v); want 0, nil, false", queued, err, s.CanSend())
	}
}

// Only data queued into an empty transmit buffer ends a quiet spell and
// restarts the timeout clock (see SetTimeout); a send that queues nothing
// leaves the clock running from the peer's last segment.
func TestSendingNothingKeepsTheTimeoutClockRunning(t *testing.T) {
	t.Parallel()
	const timeout = 10 * time.Second
	s := socketEstablished(t)
	s.SetTimeout(timeout)
	send(t, s, ackFromRemote(0))

	mustSendSlice(t, s, nil)

	assertPollAt(t, s, socket.PollAtTime(instant(timeout)))
}

func TestSendQueueCountsOctetsUntilTheyAreAcknowledged(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abcdef"))

	receive(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abcdef"))))
	queuedWhileInFlight := s.SendQueue()
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+6))))

	if queuedWhileInFlight != 6 || s.SendQueue() != 0 {
		t.Errorf("SendQueue %d in flight and %d acknowledged, want 6 and 0", queuedWhileInFlight, s.SendQueue())
	}
}
