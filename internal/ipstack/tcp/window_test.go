package tcp

// Tests of window management.

import (
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestMaximumSegmentSize(t *testing.T) {
	t.Parallel()
	s := socketListen(t)
	s.transmitBuffer = NewSocketBuffer(32767)
	send(t, s, remoteSYN(withMSS(1000)))
	receiveOnly(t, s, synACK())
	send(t, s, ackFromRemote(0, withWindow(32767)))

	mustSendSlice(t, s, make([]byte, 1200))

	receive(t, s, dataFromLocal(0, string(make([]byte, 1000))))
}

func TestRecvOutOfRecvWin(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetAckDelay(defaultACKDelay)
	s.remoteMSS = 32

	// No ACKs are sent due to the ACK delay.
	send(t, s, dataFromRemote(0, string(make([]byte, 32)), withControl(wire.TCPControlPSH)))
	receiveNothing(t, s)

	// RMSS+1 bytes of data has been received, so ACK is sent without delay.
	send(t, s, dataFromRemote(32, string(make([]byte, 1)), withControl(wire.TCPControlPSH)))
	receive(t, s, ackFromLocal(33, 31))

	// This frees up a byte in the receive buffer. However, the remote
	// shouldn't be aware of this since no ACKs are sent.
	mustReceiveSlice(t, s, make([]byte, 1))
	receiveNothing(t, s)

	// Now, if the remote wants to send one byte outside of the receive window
	// that we previously advertised, it should not succeed.
	send(t, s, dataFromRemote(33, string(make([]byte, 32)), withControl(wire.TCPControlPSH)))
	receive(t, s, ackFromLocal(64, 1)) // The last byte isn't accepted.
}

func TestCloseWaitNoWindowUpdate(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	send(t, s, dataFromRemote(0, "\x01\x02\x03\x04", withControl(wire.TCPControlFIN)))
	assertState(t, s, StateCloseWait)
	// we ack the FIN, with the reduced window size.
	receive(t, s, ackFromLocal(4+1, 60))

	if received := mustReceiveSlice(t, s, make([]byte, 32)); received != 4 {
		t.Fatalf("ReceiveSlice took %d octets, want 4", received)
	}

	// check that we do NOT send a window update even if it has changed.
	receiveNothing(t, s)
}

func TestTimeWaitNoWindowUpdate(t *testing.T) {
	t.Parallel()
	s := socketFinWait2(t)
	send(t, s, dataFromRemote(0, "\x01\x02\x03\x04", withControl(wire.TCPControlFIN), withAck(localSeq.Add(2))))
	assertState(t, s, StateTimeWait)
	// we ack the FIN, with the reduced window size.
	receive(t, s, ackFromLocal(4+1, 60, withSeq(localSeq.Add(2))))

	if received := mustReceiveSlice(t, s, make([]byte, 32)); received != 4 {
		t.Fatalf("ReceiveSlice took %d octets, want 4", received)
	}

	// check that we do NOT send a window update even if it has changed.
	receiveNothing(t, s)
}
