package tcp

// Tests of the SYN-RECEIVED state.

import (
	"fmt"
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestSynReceivedACK(t *testing.T) {
	t.Parallel()
	s := socketSynReceived(t)
	receiveOnly(t, s, synACK())

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))

	assertState(t, s, StateEstablished)
	sanity(t, s, socketEstablished(t))
}

func TestSynReceivedACKTooLow(t *testing.T) {
	t.Parallel()
	s := socketSynReceived(t)
	receiveOnly(t, s, synACK())

	sendExpectingReply(t, s,
		fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq)), // wrong
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq), withoutAck(), withWindow(0)))

	assertState(t, s, StateSynReceived)
}

func TestSynReceivedACKTooHigh(t *testing.T) {
	t.Parallel()
	s := socketSynReceived(t)
	receiveOnly(t, s, synACK())

	sendExpectingReply(t, s,
		fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(2))), // wrong
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(2)), withoutAck(), withWindow(0)))

	assertState(t, s, StateSynReceived)
}

func TestSynReceivedFIN(t *testing.T) {
	t.Parallel()
	s := socketSynReceived(t)
	receiveOnly(t, s, synACK())

	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)),
		withPayload([]byte("abcdef"))))

	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+6+1)), withWindow(58)))
	assertState(t, s, StateCloseWait)
	want := socketCloseWait(t)
	want.remoteLastAck = wire.Some(remoteSeq.Add(1 + 6 + 1))
	want.remoteLastWindow = 58
	sanity(t, s, want)
}

func TestSynReceivedRST(t *testing.T) {
	t.Parallel()
	s := socketSynReceived(t)
	s.listenEndpoint = listenEnd
	receiveOnly(t, s, synACK())

	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1)), withAck(localSeq)))

	assertState(t, s, StateListen)
	if s.listenEndpoint != listenEnd {
		t.Errorf("listenEndpoint = %v, want %v", s.listenEndpoint, listenEnd)
	}
	assertNoTuple(t, s)
}

func TestSynReceivedNoWindowScaling(t *testing.T) {
	t.Parallel()
	s := socketListen(t)
	send(t, s, remoteSYN())
	assertState(t, s, StateSynReceived)
	assertTuple(t, s, testTuple)
	receiveOnly(t, s, synACK())

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))

	if s.remoteWindowShift != 0 {
		t.Errorf("remoteWindowShift = %d, want 0", s.remoteWindowShift)
	}
	if scale, ok := s.remoteWindowScale.Get(); ok {
		t.Errorf("remoteWindowScale = %d, want none", scale)
	}
}

func TestSynReceivedWindowScaling(t *testing.T) {
	t.Parallel()
	// It tries every scale below RFC 7323's largest, 14.
	const scales = 14
	for scale := range uint8(scales) {
		t.Run(fmt.Sprint(scale), func(t *testing.T) {
			t.Parallel()
			s := socketListen(t)
			send(t, s, remoteSYN(withWindowScale(scale)))
			assertState(t, s, StateSynReceived)
			assertTuple(t, s, testTuple)
			receiveOnly(t, s, synACK(withWindowScale(0)))

			send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))

			if got, ok := s.remoteWindowScale.Get(); !ok || got != scale {
				t.Errorf("remoteWindowScale = %d, %v; want %d", got, ok, scale)
			}
		})
	}
}

func TestSynReceivedClose(t *testing.T) {
	t.Parallel()
	s := socketSynReceived(t)

	s.Close()

	assertState(t, s, StateFinWait1)
}
