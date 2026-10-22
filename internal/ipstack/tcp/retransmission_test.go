package tcp

// The tests of smoltcp's socket/tcp.rs for retransmission on packet loss
// (src/socket/tcp.rs, lines 6395-7519).
// test_congestion_window_limits_data_in_flight and
// test_congestion_window_doesnt_limit_fast_retransmit are not ported: they
// test Reno congestion control, which the workspace does not compile
// (PORTING-ipstack.md, sections 3 and 6).

import (
	"math"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// assertDuplicateACKs fails the test, saying why, unless the socket has
// counted want duplicate ACKs in a row.
func assertDuplicateACKs(t *testing.T, s *testSocket, want uint8, why string) {
	t.Helper()
	if s.localReceiveDuplicateACKs != want {
		t.Fatalf("%s: localReceiveDuplicateACKs = %d, want %d", why, s.localReceiveDuplicateACKs, want)
	}
}

// assertRetransmitTimer fails the test, saying when, unless the socket's
// timer is timing a retransmission: smoltcp's
// assert!(s.timer.is_retransmit()).
func assertRetransmitTimer(t *testing.T, s *testSocket, when string) {
	t.Helper()
	if !s.timer.isRetransmit() {
		t.Fatalf("timer = %+v %s, want it timing a retransmission", s.timer, when)
	}
}

func TestDuplicateSeqACK(t *testing.T) {
	t.Parallel()
	s := socketWithReceivedData(t)

	// remote retransmission
	sendExpectingReply(t, s, dataFromRemote(0, "abcdef"), ackFromLocal(6, 58))
}

func TestDataRetransmit(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abcdef"))

	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "abcdef"))
	receiveNothing(t, s.at(1050*time.Millisecond))
	receive(t, s.at(2000*time.Millisecond), dataFromLocal(0, "abcdef"))
}

func TestDataRetransmitBursts(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6
	mustSendSlice(t, s, []byte("abcdef012345"))

	receiveExact(t, s, dataFromLocal(0, "abcdef", withControl(wire.TCPControlNone)))
	receiveExact(t, s, dataFromLocal(6, "012345", withControl(wire.TCPControlPSH)))
	receiveNothing(t, s)

	receiveNothing(t, s.at(50*time.Millisecond))

	receiveExact(t, s.at(1000*time.Millisecond), dataFromLocal(0, "abcdef", withControl(wire.TCPControlNone)))
	receiveExact(t, s.at(1500*time.Millisecond), dataFromLocal(6, "012345", withControl(wire.TCPControlPSH)))
	receiveNothing(t, s.at(1550*time.Millisecond))
}

func TestDataRetransmitBurstsHalfACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6
	mustSendSlice(t, s, []byte("abcdef012345"))
	receiveExact(t, s, dataFromLocal(0, "abcdef", withControl(wire.TCPControlNone)))
	receiveExact(t, s, dataFromLocal(6, "012345", withControl(wire.TCPControlPSH)))

	// Acknowledge the first packet
	send(t, s.at(5*time.Millisecond), ackFromRemote(6, withWindow(6)))

	// The second packet should be re-sent.
	receiveExact(t, s.at(1500*time.Millisecond), dataFromLocal(6, "012345", withControl(wire.TCPControlPSH)))
	receiveNothing(t, s.at(1550*time.Millisecond))
}

func TestRetransmitTimerRestartOnPartialACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6
	mustSendSlice(t, s, []byte("abcdef012345"))
	receiveExact(t, s, dataFromLocal(0, "abcdef", withControl(wire.TCPControlNone)))
	receiveExact(t, s, dataFromLocal(6, "012345", withControl(wire.TCPControlPSH)))

	// Acknowledge the first packet
	send(t, s.at(600*time.Millisecond), ackFromRemote(6, withWindow(6)))

	// The ACK of the first packet should restart the retransmit timer and
	// delay a retransmission.
	receiveNothing(t, s.at(2399*time.Millisecond))
	// The second packet should be re-sent.
	receiveExact(t, s.at(2400*time.Millisecond), dataFromLocal(6, "012345", withControl(wire.TCPControlPSH)))
}

func TestDataRetransmitBurstsHalfACKClose(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6
	mustSendSlice(t, s, []byte("abcdef012345"))
	s.Close()
	receiveExact(t, s, dataFromLocal(0, "abcdef", withControl(wire.TCPControlNone)))
	receiveExact(t, s, dataFromLocal(6, "012345", withControl(wire.TCPControlFIN)))

	// Acknowledge the first packet
	send(t, s.at(5*time.Millisecond), ackFromRemote(6, withWindow(6)))

	// The second packet should be re-sent.
	receiveExact(t, s.at(1500*time.Millisecond), dataFromLocal(6, "012345", withControl(wire.TCPControlFIN)))
	receiveNothing(t, s.at(1550*time.Millisecond))
}

func TestSendDataAfterSYNACKRetransmit(t *testing.T) {
	t.Parallel()
	s := socketSynReceived(t)
	receive(t, s.at(50*time.Millisecond), synACK())
	receive(t, s.at(1050*time.Millisecond), synACK()) // retransmit

	send(t, s, ackFromRemote(0))
	assertState(t, s, StateEstablished)
	mustSendSlice(t, s, []byte("abcdef"))

	receiveOnly(t, s, dataFromLocal(0, "abcdef"))
}

func TestEstablishedRetransmitForDupACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	// Duplicate ACKs do not replace the retransmission timer
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "abc"))
	// Retransmit timer is on because all data was sent
	assertTransmitBufferLen(t, s, 3)

	// ACK nothing new
	send(t, s, ackFromRemote(0))

	// Retransmit
	receive(t, s.at(4000*time.Millisecond), dataFromLocal(0, "abc"))
}

func TestEstablishedRetransmitResetAfterACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteWindowLen = 6
	mustSendSlice(t, s, []byte("abcdef"))
	mustSendSlice(t, s, []byte("123456"))
	mustSendSlice(t, s, []byte("ABCDEF"))

	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "abcdef"))
	send(t, s.at(1005*time.Millisecond), ackFromRemote(6, withWindow(6)))
	receive(t, s.at(1010*time.Millisecond), dataFromLocal(6, "123456"))
	send(t, s.at(1015*time.Millisecond), ackFromRemote(6+6, withWindow(6)))
	receive(t, s.at(1020*time.Millisecond), dataFromLocal(6+6, "ABCDEF"))
}

func TestEstablishedQueueDuringRetransmission(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6
	mustSendSlice(t, s, []byte("abcdef123456ABCDEF"))

	// this one is dropped
	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "abcdef"))
	// this one is received
	receive(t, s.at(1005*time.Millisecond), dataFromLocal(6, "123456"))
	// also dropped
	receive(t, s.at(1010*time.Millisecond), dataFromLocal(6+6, "ABCDEF"))
	// retransmission
	receive(t, s.at(3000*time.Millisecond), dataFromLocal(0, "abcdef"))
	// acknowledgement of both segments
	send(t, s.at(3005*time.Millisecond), ackFromRemote(6+6))
	// retransmission of only unacknowledged data
	receive(t, s.at(3010*time.Millisecond), dataFromLocal(6+6, "ABCDEF"))
}

func TestCloseWaitRetransmitResetAfterACK(t *testing.T) {
	t.Parallel()
	s := socketCloseWait(t)
	s.remoteWindowLen = 6
	mustSendSlice(t, s, []byte("abcdef"))
	mustSendSlice(t, s, []byte("123456"))
	mustSendSlice(t, s, []byte("ABCDEF"))
	// The peer's FIN took the sequence number after its SYN.
	afterPeerFIN := remoteSeq.Add(1 + 1)

	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "abcdef", withAck(afterPeerFIN)))
	send(t, s.at(1005*time.Millisecond), ackFromRemote(6, withSeq(afterPeerFIN), withWindow(6)))
	receive(t, s.at(1010*time.Millisecond), dataFromLocal(6, "123456", withAck(afterPeerFIN)))
	send(t, s.at(1015*time.Millisecond), ackFromRemote(6+6, withSeq(afterPeerFIN), withWindow(6)))
	receive(t, s.at(1020*time.Millisecond), dataFromLocal(6+6, "ABCDEF", withAck(afterPeerFIN)))
}

func TestFinWait1RetransmitResetAfterACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteWindowLen = 6
	mustSendSlice(t, s, []byte("abcdef"))
	mustSendSlice(t, s, []byte("123456"))
	mustSendSlice(t, s, []byte("ABCDEF"))
	s.Close()

	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "abcdef"))
	send(t, s.at(1005*time.Millisecond), ackFromRemote(6, withWindow(6)))
	receive(t, s.at(1010*time.Millisecond), dataFromLocal(6, "123456"))
	send(t, s.at(1015*time.Millisecond), ackFromRemote(6+6, withWindow(6)))
	receive(t, s.at(1020*time.Millisecond), dataFromLocal(6+6, "ABCDEF", withControl(wire.TCPControlFIN)))
}

func TestFastRetransmitAfterTripleDuplicateACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 3
	// Normal ACK of previously received segment
	send(t, s, ackFromRemote(0))
	// Send a long string of text divided into several packets because of the
	// small remote MSS
	mustSendSlice(t, s, []byte("aaaBBBcccDDDeeeFFF"))
	// This packet is lost
	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "aaa"))
	// These packets arrive
	receive(t, s.at(1005*time.Millisecond), dataFromLocal(3, "BBB"))
	receive(t, s.at(1010*time.Millisecond), dataFromLocal(3*2, "ccc"))
	receive(t, s.at(1015*time.Millisecond), dataFromLocal(3*3, "DDD"))

	// Duplicate ACKs trigger fast retransmit after 3rd successive one
	send(t, s.at(1050*time.Millisecond), ackFromRemote(0))
	send(t, s.at(1055*time.Millisecond), ackFromRemote(0))
	send(t, s.at(1060*time.Millisecond), ackFromRemote(0))

	// Fast retransmit should have triggered
	receive(t, s.at(1100*time.Millisecond), dataFromLocal(0, "aaa"))
	// Transmission should continue as normal after re-transmitting the first
	// segment
	receive(t, s.at(1105*time.Millisecond), dataFromLocal(3*4, "eee"))
	receive(t, s.at(1110*time.Millisecond), dataFromLocal(3*5, "FFF"))
	// ACK all received segments
	send(t, s.at(1120*time.Millisecond), ackFromRemote(3*5))
}

func TestFastRetransmitDuplicateDetectionWithData(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc")) // This is lost
	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "abc"))
	// Normal ACK of previously received segment
	send(t, s, ackFromRemote(0))
	// First duplicate
	send(t, s, ackFromRemote(0))
	// Second duplicate
	send(t, s, ackFromRemote(0))
	assertDuplicateACKs(t, s, 2, "duplicate ACK counter is not set")

	// This packet has content, hence should not be detected as a duplicate
	// ACK and should reset the duplicate ACK count
	send(t, s, ackFromRemote(0, withPayload([]byte("xxxxxx"))))

	receiveOnly(t, s, ackFromLocal(6, 58, withSeq(localSeq.Add(1+3))))
	assertDuplicateACKs(t, s, 0, "duplicate ACK counter is not reset when receiving data")
}

func TestFastRetransmitDuplicateDetectionWithWindowUpdate(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc")) // This is lost
	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "abc"))
	// Normal ACK of previously received segment
	send(t, s, ackFromRemote(0))
	// First duplicate
	send(t, s, ackFromRemote(0))
	// Second duplicate
	send(t, s, ackFromRemote(0))
	assertDuplicateACKs(t, s, 2, "duplicate ACK counter is not set")

	// This packet has a window update, hence should not be detected as a
	// duplicate ACK and should reset the duplicate ACK count
	send(t, s, ackFromRemote(0, withWindow(400)))

	assertDuplicateACKs(t, s, 0, "duplicate ACK counter is not reset when receiving a window update")
}

func TestFastRetransmitDuplicateDetection(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6
	// Normal ACK of previously received segment
	send(t, s, ackFromRemote(0))
	// First duplicate, should not be counted as there is nothing to resend
	send(t, s, ackFromRemote(0))
	assertDuplicateACKs(t, s, 0, "duplicate ACK counter is set but would not transmit data")
	// Send a long string of text divided into several packets because of
	// the small remote MSS
	mustSendSlice(t, s, []byte("xxxxxxyyyyyywwwwwwzzzzzz"))
	// This packet is reordered in network
	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "xxxxxx"))
	receive(t, s.at(1005*time.Millisecond), dataFromLocal(6, "yyyyyy"))
	receive(t, s.at(1010*time.Millisecond), dataFromLocal(6*2, "wwwwww"))
	receive(t, s.at(1015*time.Millisecond), dataFromLocal(6*3, "zzzzzz"))

	// First duplicate ACK
	send(t, s.at(1050*time.Millisecond), ackFromRemote(0))
	// Second duplicate ACK
	send(t, s.at(1055*time.Millisecond), ackFromRemote(0))
	// Reordered packet arrives which should reset duplicate ACK count
	send(t, s.at(1060*time.Millisecond), ackFromRemote(6*3))

	assertDuplicateACKs(t, s, 0,
		"duplicate ACK counter is not reset when receiving ACK which updates send window")
	// ACK all received segments
	send(t, s.at(1120*time.Millisecond), ackFromRemote(6*4))
}

func TestFastRetransmitDupACKsCounter(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc")) // This is lost
	receive(t, s, dataFromLocal(0, "abc"))
	send(t, s, ackFromRemote(0))
	// A lot of retransmits happen here
	s.localReceiveDuplicateACKs = math.MaxUint8 - 1

	// Send 3 more ACKs, which could overflow localReceiveDuplicateACKs, but
	// intended behaviour is that we saturate the bounds of
	// localReceiveDuplicateACKs
	send(t, s, ackFromRemote(0))
	send(t, s, ackFromRemote(0))
	send(t, s, ackFromRemote(0))

	assertDuplicateACKs(t, s, math.MaxUint8, "duplicate ACK count should not overflow but saturate")
}

func TestFastRetransmitZeroWindow(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	send(t, s.at(1000*time.Millisecond), ackFromRemote(0))
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc"))

	// 3 dup acks
	send(t, s.at(1050*time.Millisecond), ackFromRemote(0))
	send(t, s.at(1050*time.Millisecond), ackFromRemote(0))
	send(t, s.at(1050*time.Millisecond), ackFromRemote(0, withWindow(0))) // boom

	// even though we're in "fast retransmit", we shouldn't force-send
	// anything because the remote's window is full.
	receiveNothing(t, s)
}

func TestRetransmitExponentialBackoff(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abcdef"))
	receive(t, s, dataFromLocal(0, "abcdef"))

	expectedRetransmission := s.rttEstimator.retransmissionTimeout()
	receiveNothing(t, s.at(expectedRetransmission-time.Millisecond))
	receive(t, s.at(expectedRetransmission), dataFromLocal(0, "abcdef"))

	// "current time" is expectedRetransmission, and we want to wait
	// 2 * retransmission timeout
	expectedRetransmission = 3 * expectedRetransmission
	receiveNothing(t, s.at(expectedRetransmission-time.Millisecond))
	receive(t, s.at(expectedRetransmission), dataFromLocal(0, "abcdef"))
}

func TestDataRetransmitACKMoreThanExpected(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6
	mustSendSlice(t, s, []byte("aaaaaabbbbbbcccccc"))
	receive(t, s, dataFromLocal(0, "aaaaaa"))
	receive(t, s, dataFromLocal(6, "bbbbbb"))
	receive(t, s, dataFromLocal(12, "cccccc"))
	receiveNothing(t, s)
	receiveNothing(t, s.at(50*time.Millisecond))

	// retransmit timer expires, we want to retransmit all 3 packets but we
	// only manage to retransmit 2 (due to e.g. lack of device buffer space)
	assertRetransmitTimer(t, s, "before the retransmission")
	receive(t, s.at(1000*time.Millisecond), dataFromLocal(0, "aaaaaa"))
	receive(t, s.at(1000*time.Millisecond), dataFromLocal(6, "bbbbbb"))

	// ack first packet.
	send(t, s.at(3000*time.Millisecond), ackFromRemote(6))
	// this should keep retransmit timer on, because there's still unacked
	// data.
	assertRetransmitTimer(t, s, "with data unacknowledged")

	// ack all three packets. This might confuse the TCP stack because after
	// the retransmit it "thinks" the 3rd packet hasn't been transmitted yet,
	// but it is getting acked.
	send(t, s.at(3000*time.Millisecond), ackFromRemote(18))
	// this should exit retransmit mode.
	if s.timer.isRetransmit() {
		t.Fatalf("timer = %+v with every octet acknowledged, want it not retransmitting", s.timer)
	}
	// and consider all data ACKed.
	assertTransmitBufferLen(t, s, 0)
	receiveNothing(t, s.at(5000*time.Millisecond))
}

func TestRetransmitFIN(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.Close()
	fin := fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)))
	receive(t, s, fin)

	receiveNothing(t, s.at(999*time.Millisecond))
	receive(t, s.at(1000*time.Millisecond), fin)
}

func TestRetransmitFinWait(t *testing.T) {
	t.Parallel()
	s := socketFinWait1(t)
	// we send FIN
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
	// remote also sends FIN, does NOT ack ours.
	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))
	// we ack it
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlNone), withSeq(localSeq.Add(2)), withAck(remoteSeq.Add(2))))

	// we haven't got an ACK for our FIN, we should retransmit.
	finAfterPeerFIN := fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(2)))
	receiveNothing(t, s.at(999*time.Millisecond))
	receiveOnly(t, s.at(1000*time.Millisecond), finAfterPeerFIN)
	receiveNothing(t, s.at(2999*time.Millisecond))
	receiveOnly(t, s.at(3000*time.Millisecond), finAfterPeerFIN)
}
