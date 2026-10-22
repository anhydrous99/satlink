package tcp

// The tests of smoltcp's socket/tcp.rs for the CLOSE-WAIT state
// (src/socket/tcp.rs, lines 5811-5845).

import (
	"testing"
)

func TestCloseWaitACK(t *testing.T) {
	t.Parallel()
	s := socketCloseWait(t)
	mustSendSlice(t, s, []byte("abcdef"))

	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1)), withPayload([]byte("abcdef"))))

	// send fails the test if the socket replies.
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+6))))
}

func TestCloseWaitClose(t *testing.T) {
	t.Parallel()
	s := socketCloseWait(t)

	s.Close()

	assertState(t, s, StateLastAck)
	sanity(t, s, socketLastAck(t))
}
