package tcp

// Tests of keep-alive.

import (
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
)

func TestRespondsToKeepAlive(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	sendExpectingReply(t, s,
		fromRemote(withSeq(remoteSeq), withAck(localSeq.Add(1))),
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
}

func TestSendsKeepAlive(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetKeepAlive(100 * time.Millisecond)
	keepAlive := keepAliveFromLocal(localSeq.Add(1))

	// Drain the keep-alive that turning keep-alive on forces at once.
	assertPollAt(t, s, socket.PollNow())
	receive(t, s, keepAlive)

	assertPollAt(t, s, socket.PollAtTime(instant(100*time.Millisecond)))
	receiveNothing(t, s.at(95*time.Millisecond))
	receive(t, s.at(100*time.Millisecond), keepAlive)

	assertPollAt(t, s, socket.PollAtTime(instant(200*time.Millisecond)))
	receiveNothing(t, s.at(195*time.Millisecond))
	receive(t, s.at(200*time.Millisecond), keepAlive)

	// Any segment from the peer restarts the keep-alive interval.
	send(t, s.at(250*time.Millisecond), fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))
	assertPollAt(t, s, socket.PollAtTime(instant(350*time.Millisecond)))
	receiveNothing(t, s.at(345*time.Millisecond))
	receive(t, s.at(350*time.Millisecond), keepAlive)
}
