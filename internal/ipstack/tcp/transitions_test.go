package tcp

// Tests of transitions through multiple states.

import (
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestListen(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)

	if err := s.Listen(listenEnd); err != nil {
		t.Fatalf("Listen(%v): %v", listenEnd, err)
	}

	assertState(t, s, StateListen)
}

func TestThreeWayHandshake(t *testing.T) {
	t.Parallel()
	s := socketListen(t)

	send(t, s, remoteSYN())
	if got := s.State(); got != StateSynReceived {
		t.Fatalf("State() = %v after the SYN, want %v", got, StateSynReceived)
	}
	assertTuple(t, s, testTuple)
	receiveOnly(t, s, synACK())
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))

	if got := s.State(); got != StateEstablished {
		t.Fatalf("State() = %v after the ACK, want %v", got, StateEstablished)
	}
	compareField(t, "localSeqNumber", s.localSeqNumber, localSeq.Add(1))
	compareField(t, "remoteSeqNumber", s.remoteSeqNumber, remoteSeq.Add(1))
}

func TestRemoteClose(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))
	assertState(t, s, StateCloseWait)
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))

	s.Close()
	assertState(t, s, StateLastAck)
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+1))))
	assertState(t, s, StateClosed)
}

func TestLocalClose(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	s.Close()
	assertState(t, s, StateFinWait1)
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+1))))
	assertState(t, s, StateFinWait2)

	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+1))))
	assertState(t, s, StateTimeWait)
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1))))
}

func TestSimultaneousClose(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	s.Close()
	assertState(t, s, StateFinWait1)
	// Due to reordering, this is logically located...
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))

	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))
	assertState(t, s, StateClosing)
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1))))

	// ... at this point
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+1))))
	assertState(t, s, StateTimeWait)
	receiveNothing(t, s)
}

func TestSimultaneousCloseCombinedFINACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	s.Close()
	assertState(t, s, StateFinWait1)
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))

	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+1))))
	assertState(t, s, StateTimeWait)
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1))))
}

func TestSimultaneousCloseRaced(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.Close()
	assertState(t, s, StateFinWait1)

	// Socket receives FIN before it has a chance to send its own FIN
	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))
	assertState(t, s, StateClosing)

	// FIN + ack-of-FIN
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))
	assertState(t, s, StateClosing)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+1))))
	assertState(t, s, StateTimeWait)
	receiveNothing(t, s)
}

func TestSimultaneousCloseRacedWithData(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abcdef"))
	s.Close()
	assertState(t, s, StateFinWait1)

	// Socket receives FIN before it has a chance to send its own data+FIN
	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))
	assertState(t, s, StateClosing)

	// data + FIN + ack-of-FIN
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1)),
		withPayload([]byte("abcdef"))))
	assertState(t, s, StateClosing)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+6+1))))
	assertState(t, s, StateTimeWait)
	receiveNothing(t, s)
}

func TestFINWithData(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abcdef"))

	s.Close()

	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)),
		withPayload([]byte("abcdef"))))
}

func TestMutualCloseWithData1(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abcdef"))
	s.Close()
	assertState(t, s, StateFinWait1)
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)),
		withPayload([]byte("abcdef"))))

	// send fails the test if the socket replies.
	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+6+1))))
}

func TestMutualCloseWithData2(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abcdef"))
	s.Close()
	assertState(t, s, StateFinWait1)
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)),
		withPayload([]byte("abcdef"))))

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+6+1))))
	assertState(t, s, StateFinWait2)

	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+6+1))))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+6+1)), withAck(remoteSeq.Add(1+1))))
	assertState(t, s, StateTimeWait)
}
