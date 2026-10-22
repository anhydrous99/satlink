package tcp

// Tests of TCP timestamps. Without a timestamp generator the socket never
// enables them, so the tests check that it does not echo the peer's.

import (
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestTsvalDisabledInLocalServer(t *testing.T) {
	t.Parallel()
	s := socketListen(t)
	send(t, s, remoteSYN(withTimestamp(500, 0)))
	assertState(t, s, StateSynReceived)
	assertTuple(t, s, testTuple)
	// The SYN|ACK does not echo the peer's timestamp.
	receiveOnly(t, s, synACK())

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))

	assertState(t, s, StateEstablished)
	compareField(t, "localSeqNumber", s.localSeqNumber, localSeq.Add(1))
	compareField(t, "remoteSeqNumber", s.remoteSeqNumber, remoteSeq.Add(1))
}

func TestTsvalDisabledInLocalClient(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	if err := s.Connect(s.cx, remoteEnd, wire.ListenEndpoint{Port: localPort}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	assertTuple(t, s, testTuple)
	receiveOnly(t, s, initialSYN())
	send(t, s, remoteSYN(withAck(localSeq.Add(1)), withMSS(peerMSS), withWindowScale(0), withTimestamp(500, 0)))

	mustSendSlice(t, s, []byte("abcdef"))

	// The data carries no timestamp, though the peer's SYN|ACK had one.
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abcdef"))))
}
