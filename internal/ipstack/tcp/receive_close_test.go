package tcp

// The tests of smoltcp's socket/tcp.rs for the graceful (FIN) and the
// ungraceful (RST) end of the received stream: its "graceful vs ungraceful
// rx close" section (src/socket/tcp.rs, lines 8966-9159).

import (
	"errors"
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// assertReceiveFails fails the test unless Receive fails with want:
// smoltcp's assert_eq!(s.recv(|_| (0, ())), Err(want)).
func assertReceiveFails(t *testing.T, s *testSocket, want error) {
	t.Helper()
	if _, err := s.Receive(func([]byte) int { return 0 }); !errors.Is(err, want) {
		t.Errorf("Receive = %v, want %v", err, want)
	}
}

func TestRxCloseFIN(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)),
		withPayload([]byte("abc"))))

	receiveExactly(t, s, "abc")

	assertReceiveFails(t, s, ErrFinished)
}

func TestRxCloseFINInFinWait1(t *testing.T) {
	t.Parallel()
	s := socketFinWait1(t)
	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)),
		withPayload([]byte("abc"))))
	assertState(t, s, StateClosing)

	receiveExactly(t, s, "abc")

	assertReceiveFails(t, s, ErrFinished)
}

func TestRxCloseFINInFinWait2(t *testing.T) {
	t.Parallel()
	s := socketFinWait2(t)
	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+1)),
		withPayload([]byte("abc"))))
	assertState(t, s, StateTimeWait)

	receiveExactly(t, s, "abc")

	assertReceiveFails(t, s, ErrFinished)
}

func TestRxCloseFINWithHole(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))
	sendExpectingReply(t, s,
		fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1+6)), withAck(localSeq.Add(1)),
			withPayload([]byte("ghi"))),
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+3)), withWindow(61)))
	receiveExactly(t, s, "abc")
	receiveExactly(t, s, "")

	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1+9)), withAck(localSeq.Add(1))))

	// The error must be ErrInvalidState even though a FIN was received,
	// because data is missing.
	assertReceiveFails(t, s, ErrInvalidState)
}

func TestRxCloseRST(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))

	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1+3)), withAck(localSeq.Add(1))))

	receiveExactly(t, s, "abc")
	assertReceiveFails(t, s, ErrInvalidState)
}

func TestRxCloseRSTWithHole(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))
	sendExpectingReply(t, s,
		fromRemote(withSeq(remoteSeq.Add(1+6)), withAck(localSeq.Add(1)), withPayload([]byte("ghi"))),
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+3)), withWindow(61)))

	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1+9)), withAck(localSeq.Add(1))))

	receiveExactly(t, s, "abc")
	assertReceiveFails(t, s, ErrInvalidState)
}
