package tcp

// Tests of timeouts.

import (
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestListenTimeout(t *testing.T) {
	t.Parallel()
	s := socketListen(t)

	s.SetTimeout(100 * time.Millisecond)

	assertPollAt(t, s, socket.PollOnIngress())
}

func TestConnectTimeout(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	if err := s.Connect(s.cx, remoteEnd, wire.ListenEndpoint{Port: localPort}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	s.SetTimeout(100 * time.Millisecond)

	// The timeout runs from the SYN, which goes out at 150 ms.
	receive(t, s.at(150*time.Millisecond), initialSYN())
	assertState(t, s, StateSynSent)
	assertPollAt(t, s, socket.PollAtTime(instant(250*time.Millisecond)))
	receive(t, s.at(250*time.Millisecond),
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(1)), withAck(0)))

	assertState(t, s, StateClosed)
}

func TestEstablishedTimeout(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetTimeout(2000 * time.Millisecond)
	receiveNothing(t, s.at(250*time.Millisecond))
	assertPollAt(t, s, socket.PollAtTime(instant(2250*time.Millisecond)))
	data := fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abcdef")))

	// Queuing data after a quiet spell restarts the timeout, which runs from
	// the first transmission; the retransmission one RTO later does not
	// restart it.
	mustSendSlice(t, s, []byte("abcdef"))
	assertPollAt(t, s, socket.PollNow())
	receive(t, s.at(255*time.Millisecond), data)
	assertPollAt(t, s, socket.PollAtTime(instant(1255*time.Millisecond)))
	receive(t, s.at(1255*time.Millisecond), data)
	assertPollAt(t, s, socket.PollAtTime(instant(2255*time.Millisecond)))
	receive(t, s.at(2255*time.Millisecond),
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(1+6)), withAck(remoteSeq.Add(1))))

	assertState(t, s, StateClosed)
}

func TestEstablishedKeepAliveTimeout(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetKeepAlive(50 * time.Millisecond)
	s.SetTimeout(100 * time.Millisecond)
	keepAlive := keepAliveFromLocal(localSeq.Add(1))
	receiveOnly(t, s.at(100*time.Millisecond), keepAlive)
	assertPollAt(t, s, socket.PollAtTime(instant(150*time.Millisecond)))

	// The peer's ACK restarts both the keep-alive interval and the timeout.
	send(t, s.at(105*time.Millisecond), fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))
	assertPollAt(t, s, socket.PollAtTime(instant(155*time.Millisecond)))
	receiveOnly(t, s.at(155*time.Millisecond), keepAlive)
	assertPollAt(t, s, socket.PollAtTime(instant(205*time.Millisecond)))

	// Without another answer, the timeout runs out 100 ms after the ACK.
	receiveNothing(t, s.at(200*time.Millisecond))
	receiveOnly(t, s.at(205*time.Millisecond),
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
	assertState(t, s, StateClosed)
}

func TestFinWait1Timeout(t *testing.T) {
	t.Parallel()
	s := socketFinWait1(t)
	s.SetTimeout(1000 * time.Millisecond)
	receive(t, s.at(100*time.Millisecond),
		fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))

	receive(t, s.at(1100*time.Millisecond),
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1))))

	assertState(t, s, StateClosed)
}

func TestLastAckTimeout(t *testing.T) {
	t.Parallel()
	s := socketLastAck(t)
	s.SetTimeout(1000 * time.Millisecond)
	receive(t, s.at(100*time.Millisecond),
		fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))

	receive(t, s.at(1100*time.Millisecond),
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1))))

	assertState(t, s, StateClosed)
}

func TestClosedTimeout(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetTimeout(200 * time.Millisecond)
	s.remoteLastTimestamp = wire.Some(instant(100 * time.Millisecond))
	s.Abort()
	assertPollAt(t, s, socket.PollNow())

	receive(t, s.at(100*time.Millisecond),
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))

	assertPollAt(t, s, socket.PollOnIngress())
}
