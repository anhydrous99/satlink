package tcp

// The tests of smoltcp's socket/tcp.rs for zero-window probes
// (src/socket/tcp.rs, lines 8201-8560).
// test_zero_window_probe_not_capped_by_cwnd is not ported: it tests Reno
// congestion control, which the workspace does not compile
// (PORTING-ipstack.md, sections 3 and 6).

import (
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// dataForAClosedWindow is what the zero-window probe tests queue for a peer
// whose window is closed.
const dataForAClosedWindow = "abcdef123456!@#$%^"

// peerWindow returns the peer's acknowledgement of everything before ack,
// announcing a receive window of window octets.
func peerWindow(ack wire.TCPSeqNumber, window uint16) wire.TCPRepr {
	return fromRemote(withSeq(remoteSeq.Add(1)), withAck(ack), withWindow(window))
}

// probeCarrying returns the zero-window probe that carries octet, the one at
// seq: the single octet beyond the peer's closed window.
func probeCarrying(seq wire.TCPSeqNumber, octet string) wire.TCPRepr {
	return fromLocal(withSeq(seq), withAck(remoteSeq.Add(1)), withPayload([]byte(octet)))
}

// socketProbingAClosedWindow returns an established socket with
// dataForAClosedWindow queued, whose peer closed its window at the epoch.
// That starts the zero-window probe timer: the first probe is due one RTO,
// 1 s, later.
func socketProbingAClosedWindow(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte(dataForAClosedWindow))
	send(t, s, peerWindow(localSeq.Add(1), 0))
	return s
}

// assertProbing fails the test unless the socket's timer is timing a
// zero-window probe: smoltcp's assert!(s.timer.is_zero_window_probe()).
func assertProbing(t *testing.T, s *testSocket) {
	t.Helper()
	if !s.timer.isZeroWindowProbe() {
		t.Fatalf("timer = %+v, want a zero-window probe timer", s.timer)
	}
}

// assertNotProbing fails the test if the socket's timer is timing a
// zero-window probe: smoltcp's assert!(!s.timer.is_zero_window_probe()).
func assertNotProbing(t *testing.T, s *testSocket) {
	t.Helper()
	if s.timer.isZeroWindowProbe() {
		t.Fatalf("timer = %+v, want no zero-window probe timer", s.timer)
	}
}

// receiveOnlyWhenDue expects the socket to send nothing a millisecond
// before due, and then only want at due, a duration after the epoch: a
// segment whose timer fires exactly then. It is smoltcp's
// recv_nothing!(s, time due - 1) followed by recv!(s, time due, [want]).
func receiveOnlyWhenDue(t *testing.T, s *testSocket, due time.Duration, want wire.TCPRepr) {
	t.Helper()
	receiveNothing(t, s.at(due-time.Millisecond))
	receiveOnly(t, s.at(due), want)
}

func TestZeroWindowProbeEnterOnWinUpdate(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	assertNotProbing(t, s)
	mustSendSlice(t, s, []byte(dataForAClosedWindow))
	assertNotProbing(t, s)

	send(t, s, peerWindow(localSeq.Add(1), 0))

	assertProbing(t, s)
}

func TestZeroWindowProbeEnterOnSend(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	send(t, s, peerWindow(localSeq.Add(1), 0))
	assertNotProbing(t, s)

	mustSendSlice(t, s, []byte(dataForAClosedWindow))

	assertProbing(t, s)
}

func TestZeroWindowProbeExit(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte(dataForAClosedWindow))
	assertNotProbing(t, s)
	send(t, s, peerWindow(localSeq.Add(1), 0))
	assertProbing(t, s)

	send(t, s, peerWindow(localSeq.Add(1), 6))

	assertNotProbing(t, s)
}

func TestZeroWindowProbeExitACK(t *testing.T) {
	t.Parallel()
	s := socketProbingAClosedWindow(t)
	receiveOnly(t, s.at(1000*time.Millisecond), probeCarrying(localSeq.Add(1), "a"))

	send(t, s.at(1010*time.Millisecond), peerWindow(localSeq.Add(2), 6))

	receiveOnly(t, s.at(1010*time.Millisecond),
		fromLocal(withSeq(localSeq.Add(2)), withAck(remoteSeq.Add(1)), withPayload([]byte("bcdef1"))))
}

func TestZeroWindowProbeBackoffNACKReply(t *testing.T) {
	t.Parallel()
	s := socketProbingAClosedWindow(t)
	probe := probeCarrying(localSeq.Add(1), "a")
	windowStillClosed := peerWindow(localSeq.Add(1), 0)

	// The interval between probes starts at the RTO, 1 s, and doubles with
	// every probe: replies that keep the window closed do not reset it.
	receiveOnlyWhenDue(t, s, 1000*time.Millisecond, probe)
	send(t, s.at(1100*time.Millisecond), windowStillClosed)
	receiveOnlyWhenDue(t, s, 3000*time.Millisecond, probe)
	send(t, s.at(3100*time.Millisecond), windowStillClosed)
	receiveOnlyWhenDue(t, s, 7000*time.Millisecond, probe)
}

func TestZeroWindowProbeBackoffNoReply(t *testing.T) {
	t.Parallel()
	s := socketProbingAClosedWindow(t)
	probe := probeCarrying(localSeq.Add(1), "a")

	receiveOnlyWhenDue(t, s, 1000*time.Millisecond, probe)
	receiveOnlyWhenDue(t, s, 3000*time.Millisecond, probe)
}

func TestZeroWindowProbeShift(t *testing.T) {
	t.Parallel()
	s := socketProbingAClosedWindow(t)
	receiveOnlyWhenDue(t, s, 1000*time.Millisecond, probeCarrying(localSeq.Add(1), "a"))
	receiveOnlyWhenDue(t, s, 3000*time.Millisecond, probeCarrying(localSeq.Add(1), "a"))

	// Acknowledge the probe's octet, but still advertise a zero window: this
	// should restart the zero-window probe timer.
	send(t, s.at(3100*time.Millisecond), peerWindow(localSeq.Add(2), 0))

	// The next probe should go at 3100 + 1000 = 4100 ms.
	receiveOnlyWhenDue(t, s, 4100*time.Millisecond, probeCarrying(localSeq.Add(2), "b"))
}
