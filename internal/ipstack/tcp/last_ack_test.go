package tcp

// Tests of the LAST-ACK state.

import (
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestLastAckFINACK(t *testing.T) {
	t.Parallel()
	s := socketLastAck(t)
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))
	assertState(t, s, StateLastAck)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+1))))

	assertState(t, s, StateClosed)
}

func TestLastAckACKNotOfFIN(t *testing.T) {
	t.Parallel()
	s := socketLastAck(t)
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))
	assertState(t, s, StateLastAck)

	// A duplicate ACK (AckNumber == SND.UNA, not the FIN ACK) must elicit a
	// challenge ACK per RFC 9293 §3.10.7.4 and must keep the state in
	// LAST-ACK.
	sendExpectingReply(t, s,
		fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1))),
		fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1))))
	assertState(t, s, StateLastAck)

	// ACK received of fin: socket should change to Closed.
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+1))))
	assertState(t, s, StateClosed)
}

// RFC 9293 §3.10.7.4: duplicate ACK in LAST-ACK must elicit a challenge ACK,
// not be silently dropped.
func TestLastAckDuplicateACKChallengeACK(t *testing.T) {
	t.Parallel()
	s := socketLastAck(t)
	// Trigger dispatch so our FIN is sent and remoteLastSeq advances.
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))
	assertState(t, s, StateLastAck)

	// Remote re-sends an ACK for SND.UNA (not the FIN). RFC 9293 requires a
	// challenge ACK in response so the remote can learn the current state.
	wantChallenge := fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1)))
	challenge, replied := deliver(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1))))
	if !replied || !challenge.Equal(wantChallenge) {
		t.Fatalf("expected challenge ACK in response to duplicate ACK in LAST-ACK: replied %v with %s, want %s",
			replied, segmentDetails(challenge), segmentDetails(wantChallenge))
	}
	// State must remain LAST-ACK: we have not received the FIN ACK.
	assertState(t, s, StateLastAck)

	// A second duplicate in the same second is rate-limited; the FIN ACK
	// must still be correctly accepted regardless.
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+1))))
	assertState(t, s, StateClosed)
}

// A partial ACK in LAST-ACK (acknowledged.length > 0 but not the FIN ACK)
// advances SND.UNA without a challenge ACK; the FIN will be retransmitted by
// the timer.
func TestLastAckPartialACKNoChallengeACK(t *testing.T) {
	t.Parallel()
	// Build a LAST-ACK socket that has one byte of data still unacknowledged
	// before the FIN. We manually wire the state so we can send a partial
	// ACK.
	s := socketLastAck(t)
	// Push one byte into the transmit buffer to simulate data that preceded
	// the FIN.
	s.transmitBuffer.EnqueueSlice([]byte("x"))
	// Mark it as already sent (remoteLastSeq is past the data byte and the
	// FIN).
	s.remoteLastSeq = localSeq.Add(1 + 1 + 1) // data(1) + FIN(1)

	// Remote ACKs just the data byte, not the FIN (partial ACK).
	// AckNumber = localSeqNumber + 1, so acknowledged.length = 1 and
	// acknowledged.ofFIN = false. Per RFC 9293, a valid partial ACK should
	// advance SND.UNA normally; no challenge ACK should be emitted.
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+1)))) // acks the data byte, not FIN

	// State remains LAST-ACK; FIN retransmission is handled by the timer.
	assertState(t, s, StateLastAck)
	// SND.UNA has advanced to the partial ACK number.
	compareField(t, "localSeqNumber", s.localSeqNumber, localSeq.Add(1+1))
}

func TestLastAckClose(t *testing.T) {
	t.Parallel()
	s := socketLastAck(t)

	s.Close()

	assertState(t, s, StateLastAck)
}
