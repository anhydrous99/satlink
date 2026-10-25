package tcp

// Tests of the ESTABLISHED state.

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// logTransmitState logs where the socket stands in sending; go test shows it for a failed
// test, or with -v.
func logTransmitState(t *testing.T, s *testSocket) {
	t.Helper()
	t.Logf("localSeqNumber=%v remoteWindowLen=%d remoteLastSeq=%v", s.localSeqNumber, s.remoteWindowLen, s.remoteLastSeq)
}

// setupRFC2018Cases returns the socket that the tests of RFC 2018's cases
// start from, and the segment they send.
func setupRFC2018Cases(t *testing.T) (*testSocket, []byte) {
	t.Helper()
	// This is a utility function used by the tests for RFC 2018 cases. It
	// configures a socket in a particular way suitable for those cases.
	//
	// RFC 2018: Assume the left window edge is 5000 and that the data
	// transmitter sends [...] segments, each containing 500 data bytes.
	s := socketEstablishedWithBufferSizes(t, 4000, 4000)
	s.remoteHasSACK = true

	// create a segment that is 500 bytes long
	segment := bytes.Repeat([]byte("abcdefghij"), 50)

	// move the last ack to 5000 by sending ten of them
	for offset := 0; offset < 5000; offset += 500 {
		send(t, s, fromRemote(withSeq(remoteSeq.Add(1+offset)), withAck(localSeq.Add(1)), withPayload(segment)))
		receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+offset+500)), withWindow(3500)))
		receiveExactly(t, s, string(segment))
	}
	if s.remoteLastWindow != 3500 {
		t.Fatalf("remoteLastWindow = %d, want 3500", s.remoteLastWindow)
	}
	return s, segment
}

func TestEstablishedRecv(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abcdef"))))

	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+6)), withWindow(58)))
	if got := s.receiveBuffer.DequeueMany(6); string(got) != "abcdef" {
		t.Errorf("the receive buffer holds %q, want %q", got, "abcdef")
	}
}

func TestEstablishedRFC2018Cases(t *testing.T) {
	t.Parallel()
	// This test case verifies the exact scenarios described on pages 8-9 of
	// RFC 2018. Please ensure its behavior does not deviate from those
	// scenarios.
	s, segment := setupRFC2018Cases(t)

	// RFC 2018:
	//
	// Case 2: The first segment is dropped but the remaining 7 are received.
	//
	// Upon receiving each of the last seven packets, the data receiver will
	// return a TCP ACK segment that acknowledges sequence number 5000 and
	// contains a SACK option specifying one block of queued data:
	//
	//	Triggering   ACK      Left Edge  Right Edge
	//	Segment
	//
	//	5000         (lost)
	//	5500         5000     5500       6000
	//	6000         5000     5500       6500
	//	6500         5000     5500       7000
	//	7000         5000     5500       7500
	//	7500         5000     5500       8000
	//	8000         5000     5500       8500
	//	8500         5000     5500       9000
	for offset := 500; offset < 3500; offset += 500 {
		sendExpectingReply(t, s,
			fromRemote(withSeq(remoteSeq.Add(1+offset+5000)), withAck(localSeq.Add(1)), withPayload(segment)),
			fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+5000)), withWindow(4000),
				withSACKRanges(sackRangeOf(remoteSeq.Add(1+5500), remoteSeq.Add(1+5500+offset)))))
	}
}

func TestEstablishedSACKNoOverflowOnNearMaxSeqnumber(t *testing.T) {
	t.Parallel()
	// -4 is 2³² − 4 on the wire: the segment and its SACK block start past
	// the wrap-around of the sequence space.
	nearMaxSeq := wire.TCPSeqNumber(-4)
	s := socketEstablished(t)
	s.remoteHasSACK = true
	s.remoteSeqNumber = nearMaxSeq
	s.remoteLastAck = wire.Some(nearMaxSeq)

	// Send an out-of-order segment 10 bytes past the expected sequence,
	// creating a 10-byte hole at the front of the assembler.
	sendExpectingReply(t, s,
		fromRemote(withSeq(nearMaxSeq.Add(10)), withAck(localSeq.Add(1)), withPayload([]byte("AAAAAAAAAA"))),
		fromLocal(withSeq(localSeq.Add(1)), withAck(nearMaxSeq), withWindow(64),
			withSACKRanges(sackRangeOf(nearMaxSeq.Add(10), nearMaxSeq.Add(20)))))
}

func TestEstablishedSlidingWindowRecv(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	// Update our scaling parameters for a TCP with a scaled buffer.
	if got := s.receiveBuffer.Len(); got != 0 {
		t.Fatalf("the receive buffer holds %d octets, want 0", got)
	}
	replaceReceiveBuffer(s, 262143)
	s.remoteWindowScale = wire.Some[uint8](0)
	s.remoteLastWindow = 65535
	s.remoteWindowShift = 2

	// Create a TCP segment that will mostly fill an IP frame.
	segment := bytes.Repeat([]byte("abcdefghijklmn"), 100)
	if len(segment) != 1400 {
		t.Fatalf("the segment carries %d octets, want 1400", len(segment))
	}

	// Send the frame
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload(segment)))

	// Ensure that the received window size is shifted right by 2:
	// (262143 - 1400) >> 2 = 65185.
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1400)), withWindow(65185)))
}

func TestEstablishedSend(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	// First roundtrip after establishing.
	mustSendSlice(t, s, []byte("abcdef"))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abcdef"))))
	assertTransmitBufferLen(t, s, 6)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+6))))
	assertTransmitBufferLen(t, s, 0)

	// Second roundtrip.
	mustSendSlice(t, s, []byte("foobar"))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+6)), withAck(remoteSeq.Add(1)), withPayload([]byte("foobar"))))
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+6+6))))
	assertTransmitBufferLen(t, s, 0)
}

func TestEstablishedSendNoACKSend(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetNagleEnabled(false)

	mustSendSlice(t, s, []byte("abcdef"))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abcdef"))))

	mustSendSlice(t, s, []byte("foobar"))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+6)), withAck(remoteSeq.Add(1)), withPayload([]byte("foobar"))))
}

func TestEstablishedSendBufGtWin(t *testing.T) {
	t.Parallel()
	data := make([]byte, 32)
	for i := range data {
		data[i] = byte(i)
	}
	s := socketEstablished(t)
	s.remoteWindowLen = 16

	mustSendSlice(t, s, data)

	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload(data[0:16])))
}

// TestEstablishedLocalMSSLimitsSegments is
// test_established_options_reduce_payload_when_remote_mss_limited without its
// timestamps: a peer whose MSS exceeds what the local link takes gets
// segments of the local MSS, the IP MTU less the IPv4 and TCP headers. With
// Nagle's algorithm on, a second full segment follows the first while that
// is in flight, and the shorter rest waits. Without timestamps, one
// segment's worth of data would fit whichever MSS applied, so the test
// queues more.
func TestEstablishedLocalMSSLimitsSegments(t *testing.T) {
	t.Parallel()
	const queued = 4000
	data := string(bytes.Repeat([]byte("0123456789"), queued/10))
	s := socketEstablishedWithBufferSizes(t, queued, defaultBufferLen)
	s.remoteMSS = 9999
	s.remoteWindowLen = 9999

	mustSendSlice(t, s, []byte(data))

	receiveOnly(t, s,
		dataFromLocal(0, data[:baseMSS]),
		dataFromLocal(int(baseMSS), data[baseMSS:2*baseMSS]))
}

func TestEstablishedSendWindowShrink(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	// 6 octets fit on the remote side's window, so we send them.
	mustSendSlice(t, s, []byte("abcdef"))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abcdef"))))
	assertTransmitBufferLen(t, s, 6)
	logTransmitState(t, s)

	// - Peer doesn't ack them yet
	// - Sends data so we need to reply with an ACK
	// - ...AND and sends a window announcement that SHRINKS the window, so
	//   data we've previously sent is now outside the window. Yes, this is
	//   allowed by TCP.
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withWindow(3),
		withPayload([]byte("xyzxyz"))))
	assertTransmitBufferLen(t, s, 6)
	logTransmitState(t, s)

	// More data should not get sent since it doesn't fit in the window
	mustSendSlice(t, s, []byte("foobar"))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+6)), withAck(remoteSeq.Add(1+6)), withWindow(64-6)))
}

func TestEstablishedReceivePartiallyOutsideWindow(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))
	receiveExactly(t, s, "abc")

	// Peer decides to retransmit (perhaps because the ACK was lost) and also
	// pushed data.
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abcdef"))))

	receiveExactly(t, s, "def")
}

func TestEstablishedReceivePartiallyOutsideWindowFIN(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))
	receiveExactly(t, s, "abc")

	// Peer decides to retransmit (perhaps because the ACK was lost) and also
	// pushed data, and sent a FIN.
	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)),
		withPayload([]byte("abcdef"))))

	receiveExactly(t, s, "def")
	// We should accept the FIN, because even though the last packet was
	// partially outside the receive window, there is no hole after adding its
	// data to the assembler.
	assertState(t, s, StateCloseWait)
}

func TestEstablishedSendWrap(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	localSeqStart := wire.TCPSeqNumber(math.MaxInt32 - 1)
	s.localSeqNumber = localSeqStart.Add(1)
	s.remoteLastSeq = localSeqStart.Add(1)

	mustSendSlice(t, s, []byte("abc"))

	receive(t, s.at(1000*time.Millisecond),
		fromLocal(withSeq(localSeqStart.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abc"))))
}

func TestEstablishedNoACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	// send fails the test if the socket replies.
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withoutAck()))
}

func TestEstablishedBadACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	// Already acknowledged data.
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(-1))))
	compareField(t, "localSeqNumber", s.localSeqNumber, localSeq.Add(1))

	// Data not yet transmitted.
	sendExpectingReply(t, s,
		fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(10))),
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
	compareField(t, "localSeqNumber", s.localSeqNumber, localSeq.Add(1))
}

func TestEstablishedBadSeq(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	outsideWindow := fromRemote(withSeq(remoteSeq.Add(1+256)), withAck(localSeq.Add(1)))
	challengeACK := fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)))

	// Data outside of receive window.
	sendExpectingReply(t, s, outsideWindow, challengeACK)
	compareField(t, "remoteSeqNumber", s.remoteSeqNumber, remoteSeq.Add(1))

	// Challenge ACKs are rate-limited, we don't get a second one immediately.
	send(t, s.at(100*time.Millisecond), outsideWindow)

	// If we wait a bit, we do get a new one.
	sendExpectingReply(t, s.at(2000*time.Millisecond), outsideWindow, challengeACK)
	compareField(t, "remoteSeqNumber", s.remoteSeqNumber, remoteSeq.Add(1))
}

func TestOldDataACKNotRateLimited(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	data := fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abcdef")))
	send(t, s, data)
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+6)), withWindow(58)))
	receiveExactly(t, s, "abcdef")

	// The remote retransmits data we already acknowledged, e.g. because the
	// ACK above was lost. Each retransmission must elicit a duplicate ACK,
	// even within the challenge ACK rate limit window: withholding it strands
	// the remote in retransmission backoff.
	duplicateACK := fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+6)))
	sendExpectingReply(t, s.at(100*time.Millisecond), data, duplicateACK)
	sendExpectingReply(t, s.at(200*time.Millisecond), data, duplicateACK)
}

func TestBadSeqRSTDroppedSilently(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	// Out-of-window RSTs are silently dropped, per RFC 9293 (3.10.7.4) and
	// RFC 5961 (3.2): no challenge ACK, no state change.
	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1+256)), withAck(localSeq.Add(1))))
	assertState(t, s, StateEstablished)

	// A payload doesn't make it eligible for the data segment exemption from
	// challenge ACK rate limiting either: still no reply.
	send(t, s.at(100*time.Millisecond), fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1+256)),
		withAck(localSeq.Add(1)), withPayload([]byte("abcdef"))))
	assertState(t, s, StateEstablished)
}

func TestBadSeqSYNWithDataRateLimited(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	synWithData := fromRemote(withControl(wire.TCPControlSYN), withSeq(remoteSeq.Add(1+256)), withAck(localSeq.Add(1)),
		withPayload([]byte("abcdef")))

	// An out-of-window SYN carrying data must not be exempt from challenge
	// ACK rate limiting: RFC 5961 (4.2) says challenge ACKs sent in response
	// to SYNs should be throttled.
	sendExpectingReply(t, s, synWithData, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))

	// The second one within the rate limit window gets no reply.
	send(t, s.at(100*time.Millisecond), synWithData)
	assertState(t, s, StateEstablished)
}

func TestEstablishedFIN(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))

	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))
	assertState(t, s, StateCloseWait)
	sanity(t, s, socketCloseWait(t))
}

func TestEstablishedFINAfterMissing(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	sendExpectingReply(t, s,
		fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1+6)), withAck(localSeq.Add(1)),
			withPayload([]byte("123456"))),
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
	assertState(t, s, StateEstablished)

	sendExpectingReply(t, s,
		fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abcdef"))),
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+6+6)), withWindow(52)))
	assertState(t, s, StateEstablished)
}

func TestEstablishedSendFIN(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abcdef"))

	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))

	assertState(t, s, StateCloseWait)
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1)), withPayload([]byte("abcdef"))))
}

func TestEstablishedRST(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))

	assertState(t, s, StateClosed)
}

func TestEstablishedRSTNoACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1)), withoutAck()))

	assertState(t, s, StateClosed)
}

func TestEstablishedClose(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	s.Close()

	assertState(t, s, StateFinWait1)
	sanity(t, s, socketFinWait1(t))
}

func TestEstablishedAbort(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	s.Abort()

	assertState(t, s, StateClosed)
	receiveOnly(t, s, fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
}

func TestEstablishedRSTBadSeq(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	// Out-of-window RSTs are dropped silently, per RFC 9293 (3.10.7.4) and
	// RFC 5961 (3.2).
	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq), withoutAck())) // Wrong seq
	assertState(t, s, StateEstablished)

	// An in-window RST still resets the connection.
	send(t, s.at(2000*time.Millisecond),
		fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1)), withoutAck())) // Correct seq
	assertState(t, s, StateClosed)
}

func TestEstablishedBadSeqChallengeACKUpdated(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	wrongSeq := fromRemote(withSeq(remoteSeq), withAck(localSeq.Add(1)))
	sendExpectingReply(t, s, wrongSeq, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
	assertState(t, s, StateEstablished)

	// Send something to advance seq by 1
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("a")))) // correct seq

	// Send the wrong seq again, check that the challenge ack is correctly
	// updated. The ack number must be updated even if we don't call dispatch
	// on the socket.
	sendExpectingReply(t, s.at(2000*time.Millisecond), wrongSeq,
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(2)), withWindow(63))) // this has changed
}
