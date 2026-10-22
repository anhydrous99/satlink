package tcp

// Tests of the FIN-WAIT-1 and FIN-WAIT-2 states.

import (
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// =========================================================================
// Tests for the FIN-WAIT-1 state.
// =========================================================================

func TestFinWait1FINACK(t *testing.T) {
	t.Parallel()
	s := socketFinWait1(t)
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+1))))

	assertState(t, s, StateFinWait2)
	sanity(t, s, socketFinWait2(t))
}

func TestFinWait1FINFIN(t *testing.T) {
	t.Parallel()
	s := socketFinWait1(t)
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))

	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))

	assertState(t, s, StateClosing)
	sanity(t, s, socketClosing(t))
}

func TestFinWait1FINWithDataQueued(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteWindowLen = 6
	mustSendSlice(t, s, []byte("abcdef123456"))
	s.Close()

	receive(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abcdef"))))
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+6))))

	assertState(t, s, StateFinWait1)
}

func TestFinWait1Recv(t *testing.T) {
	t.Parallel()
	s := socketFinWait1(t)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))

	assertState(t, s, StateFinWait1)
	receiveExactly(t, s, "abc")
}

func TestFinWait1Close(t *testing.T) {
	t.Parallel()
	s := socketFinWait1(t)

	s.Close()

	assertState(t, s, StateFinWait1)
}

// =========================================================================
// Tests for the FIN-WAIT-2 state.
// =========================================================================

func TestFinWait2FIN(t *testing.T) {
	t.Parallel()
	s := socketFinWait2(t)

	send(t, s.at(1000*time.Millisecond),
		fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+1))))

	assertState(t, s, StateTimeWait)
	sanity(t, s, socketTimeWaitFromFinWait2(t))
}

func TestFinWait2Recv(t *testing.T) {
	t.Parallel()
	s := socketFinWait2(t)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+1)), withPayload([]byte("abc"))))

	assertState(t, s, StateFinWait2)
	receiveExactly(t, s, "abc")
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+3))))
}

func TestFinWait2Close(t *testing.T) {
	t.Parallel()
	s := socketFinWait2(t)

	s.Close()

	assertState(t, s, StateFinWait2)
}
