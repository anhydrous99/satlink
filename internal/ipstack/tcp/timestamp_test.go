package tcp

// The tests of smoltcp's socket/tcp.rs for TCP timestamps: its "Timestamp
// tests" section (src/socket/tcp.rs, lines 9607-9841). Two of them are
// ported, without their timestamp_enabled() assertions (PORTING-ipstack.md,
// section 6): without a generator, the socket never enables timestamps, and
// what the tests check is that it does not echo the peer's.
// test_tsval_established_connection, test_tsval_disabled_in_remote_client
// and test_tsval_disabled_in_remote_server are not ported: they need the
// omitted timestamp generator (sections 3 and 6).

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
