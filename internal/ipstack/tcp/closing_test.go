package tcp

// The tests of smoltcp's socket/tcp.rs for the CLOSING state
// (src/socket/tcp.rs, lines 5705-5735).

import (
	"testing"
	"time"
)

func TestClosingACKFIN(t *testing.T) {
	t.Parallel()
	s := socketClosing(t)
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1))))

	send(t, s.at(1000*time.Millisecond), fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+1))))

	assertState(t, s, StateTimeWait)
	sanity(t, s, socketTimeWaitFromClosing(t))
}

func TestClosingClose(t *testing.T) {
	t.Parallel()
	s := socketClosing(t)

	s.Close()

	assertState(t, s, StateClosing)
}
