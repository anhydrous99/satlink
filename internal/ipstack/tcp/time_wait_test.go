package tcp

// The tests of smoltcp's socket/tcp.rs for the TIME-WAIT state
// (src/socket/tcp.rs, lines 5736-5810).

import (
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestTimeWaitFromFinWait2ACK(t *testing.T) {
	t.Parallel()
	s := socketTimeWaitFromFinWait2(t)

	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1))))
}

func TestTimeWaitFromClosingNoACK(t *testing.T) {
	t.Parallel()
	s := socketTimeWaitFromClosing(t)

	receiveNothing(t, s)
}

func TestTimeWaitClose(t *testing.T) {
	t.Parallel()
	s := socketTimeWaitFromFinWait2(t)

	s.Close()

	assertState(t, s, StateTimeWait)
}

func TestTimeWaitRetransmit(t *testing.T) {
	t.Parallel()
	s := socketTimeWaitFromFinWait2(t)
	finalACK := fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1)))
	receiveOnly(t, s, finalACK)

	sendExpectingReply(t, s.at(5000*time.Millisecond),
		fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+1))),
		finalACK)

	assertTimer(t, s.timer, timer{kind: timerClose, expiresAt: instant(5 * time.Second).Add(closeDelay)})
}

func TestTimeWaitTimeout(t *testing.T) {
	t.Parallel()
	s := socketTimeWaitFromFinWait2(t)
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1))))
	assertState(t, s, StateTimeWait)

	receiveNothing(t, s.at(60_000*time.Millisecond))

	assertState(t, s, StateClosed)
}
