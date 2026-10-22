package tcp

// Go-specific tests (PORTING-ipstack.md, section 6) of what the socket does
// with the segments the interface hands it: Accepts, Process and RSTReply
// (segments.go, process.go, transitions.go, reply.go, reassembly.go).

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/storage"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// =========================================================================
// The segments a socket accepts
// =========================================================================

func TestListenerAcceptsASYNForItsPortFromAnyone(t *testing.T) {
	t.Parallel()
	s := socketListen(t)
	syn := remoteSYN(withSrcPort(remotePort + 1))
	fromOther := ipHeader(otherAddr, localAddr, syn)

	if !s.Accepts(fromOther, syn) {
		t.Error("a listener refused a SYN for its port from another peer")
	}
	if s.Accepts(fromOther, remoteSYN(withDstPort(localPort+1))) {
		t.Error("a listener accepted a SYN for another port")
	}
}

func TestListenerOnAnAddressAcceptsOnlySegmentsToIt(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	if err := s.Listen(localListenEnd); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	syn := remoteSYN()
	to := func(dst netip.Addr) wire.IPv4Repr {
		return ipHeader(remoteAddr, dst, syn)
	}

	if !s.Accepts(to(localAddr), syn) || s.Accepts(to(otherAddr), syn) {
		t.Errorf("Accepts to %v: %v, to %v: %v; want true, false",
			localAddr, s.Accepts(to(localAddr), syn), otherAddr, s.Accepts(to(otherAddr), syn))
	}
}

// A closed socket accepts nothing, not even a segment for the endpoints it
// still holds: a socket closed after listening keeps its listen endpoint,
// and one aborted, timed out or closed while connecting keeps its four-tuple
// until its RST has gone. smoltcp's test_closed_reject and
// test_closed_reject_after_listen offer a SYN to the peer's address, which
// such a socket would refuse whatever its state.
func TestAClosedSocketAcceptsNothing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		socket  func(t *testing.T) *testSocket
		segment wire.TCPRepr
	}{
		{"closed after listening", socketClosedAfterListening, remoteSYN()},
		{"aborted", socketAborted, dataFromRemote(0, "abc")},
		{"timed out", socketTimedOutWhileTheLineIsBusy, dataFromRemote(0, "abc")},
		{"closed while connecting", socketClosedWhileConnecting, remoteSYN(withAck(localSeq.Add(1)))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)
			assertState(t, s, StateClosed)

			accepted := s.Accepts(ipHeader(remoteAddr, localAddr, test.segment), test.segment)

			if accepted {
				t.Errorf("a socket %s accepted %s", test.name, segmentDetails(test.segment))
			}
		})
	}
}

// socketClosedAfterListening returns a socket that listened on
// localListenEnd and was closed.
func socketClosedAfterListening(t *testing.T) *testSocket {
	t.Helper()
	s := socketClosed(t)
	if err := s.Listen(localListenEnd); err != nil {
		t.Fatalf("Listen(%v): %v", localListenEnd, err)
	}
	s.Close()
	return s
}

// socketTimedOutWhileTheLineIsBusy returns an established socket that timed
// out while emit refused its RST.
func socketTimedOutWhileTheLineIsBusy(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	s.SetTimeout(time.Second)
	receiveNothing(t, s)
	dispatchRefused(t, s.at(time.Second))
	return s
}

// socketClosedWhileConnecting returns a socket closed in SYN-SENT, after its
// SYN went out.
func socketClosedWhileConnecting(t *testing.T) *testSocket {
	t.Helper()
	s := socketSynSent(t)
	receive(t, s, initialSYN())
	s.Close()
	return s
}

// =========================================================================
// Segments the state does not take
// =========================================================================

// unseenWindow is a window that no test socket knows the peer's to be, so
// that a socket taking it from a segment shows.
const unseenWindow = remoteTemplateWindow / 2

// strayFIN returns a FIN at the peer's initial sequence number, its
// acknowledgement set by ack, announcing unseenWindow.
func strayFIN(ack segmentOption) wire.TCPRepr {
	return fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq), ack, withWindow(unseenWindow))
}

// inWindowSYN returns a SYN at the start of the receive window, received
// sequence numbers into the peer's stream (its SYN and its FIN take one
// each), acknowledging ack and announcing unseenWindow.
func inWindowSYN(received int, ack wire.TCPSeqNumber) wire.TCPRepr {
	return inWindowSegment(wire.TCPControlSYN, received, ack)
}

// inWindowFIN returns a FIN at the start of the receive window, as
// inWindowSYN returns a SYN.
func inWindowFIN(received int, ack wire.TCPSeqNumber) wire.TCPRepr {
	return inWindowSegment(wire.TCPControlFIN, received, ack)
}

func inWindowSegment(control wire.TCPControl, received int, ack wire.TCPSeqNumber) wire.TCPRepr {
	return fromRemote(withControl(control), withSeq(remoteSeq.Add(received)), withAck(ack), withWindow(unseenWindow))
}

// overrunningFIN returns inWindowFIN with data longer than the receive
// window of any test socket, which is defaultBufferLen octets at most: the
// window cuts the data short before the FIN.
func overrunningFIN(received int, ack wire.TCPSeqNumber) wire.TCPRepr {
	segment := inWindowFIN(received, ack)
	segment.Payload = []byte(strings.Repeat("x", defaultBufferLen+1))
	return segment
}

// smoltcp drops a segment whose control flag the socket's state does not
// take, without a reply and without taking anything from it ("expecting a
// SYN|ACK", "unexpected packet"): a FIN in LISTEN or SYN-SENT, a SYN inside
// the window of a synchronized connection, or a FIN at the window's start
// once the peer's own FIN has been taken, also after data that the receive
// window cuts short. smoltcp's own tests send none of them.
func TestSegmentsTheStateDoesNotTakeAreDropped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		socket  func(t *testing.T) *testSocket
		segment wire.TCPRepr
	}{
		{"LISTEN FIN", socketListen, strayFIN(withoutAck())},
		{"SYN-SENT FIN", socketSynSent, strayFIN(withAck(localSeq.Add(1)))},
		{"SYN-RECEIVED SYN", socketSynReceived, inWindowSYN(1, localSeq.Add(1))},
		{"ESTABLISHED SYN", socketEstablished, inWindowSYN(1, localSeq.Add(1))},
		{"FIN-WAIT-1 SYN", socketFinWait1, inWindowSYN(1, localSeq.Add(1))},
		{"FIN-WAIT-2 SYN", socketFinWait2, inWindowSYN(1, localSeq.Add(1+1))},
		{"CLOSE-WAIT SYN", socketCloseWait, inWindowSYN(1+1, localSeq.Add(1))},
		{"CLOSE-WAIT FIN", socketCloseWait, inWindowFIN(1+1, localSeq.Add(1))},
		{"CLOSE-WAIT FIN after cut data", socketCloseWait, overrunningFIN(1+1, localSeq.Add(1))},
		{"CLOSING SYN", socketClosing, inWindowSYN(1+1, localSeq.Add(1))},
		{"CLOSING FIN", socketClosing, inWindowFIN(1+1, localSeq.Add(1))},
		{"CLOSING FIN after cut data", socketClosing, overrunningFIN(1+1, localSeq.Add(1))},
		{"CLOSING FIN after cut data, acknowledging the socket's FIN", socketClosing,
			overrunningFIN(1+1, localSeq.Add(1+1))},
		{"LAST-ACK SYN", socketLastAck, inWindowSYN(1+1, localSeq.Add(1))},
		{"LAST-ACK FIN", socketLastAck, inWindowFIN(1+1, localSeq.Add(1))},
		{"LAST-ACK FIN after cut data", socketLastAckWithItsFINSent, overrunningFIN(1+1, localSeq.Add(1))},
		{"LAST-ACK FIN after cut data, acknowledging the socket's FIN", socketLastAckWithItsFINSent,
			overrunningFIN(1+1, localSeq.Add(1+1))},
		{"TIME-WAIT SYN", socketTimeWaitFromFinWait2, inWindowSYN(1+1, localSeq.Add(1+1))},
		{"TIME-WAIT FIN", socketTimeWaitFromFinWait2, inWindowFIN(1+1, localSeq.Add(1+1))},
		{"TIME-WAIT FIN after cut data", socketTimeWaitFromFinWait2, overrunningFIN(1+1, localSeq.Add(1+1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)

			send(t, s, test.segment)

			sanity(t, s, test.socket(t))
			compareField(t, "ReceiveQueue", s.ReceiveQueue(), 0)
		})
	}
}

// socketLastAckWithItsFINSent returns a socket in LAST-ACK whose FIN has
// gone out, not yet acknowledged.
func socketLastAckWithItsFINSent(t *testing.T) *testSocket {
	t.Helper()
	s := socketLastAck(t)
	receive(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))
	return s
}

// smoltcp disregards a FIN beyond a hole in every state. So CLOSE-WAIT and
// CLOSING, which take no FIN, drop a FIN after data that the receive window
// cuts short when its segment starts at the window's start
// (TestSegmentsTheStateDoesNotTakeAreDropped), but take the segment without
// its FIN when it starts an octet further on: they queue its data in the
// assembler, take its window, and answer it with the ACK that out-of-order
// data draws.
func TestCloseWaitAndClosingTakeASegmentBeyondAHoleWithoutItsFIN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state  State
		socket func(t *testing.T) *testSocket
		// sent is the sequence number after what the socket has sent: its
		// SYN, and in CLOSING its FIN.
		sent wire.TCPSeqNumber
	}{
		{StateCloseWait, socketCloseWait, localSeq.Add(1)},
		{StateClosing, socketClosing, localSeq.Add(1 + 1)},
	}
	for _, test := range tests {
		t.Run(test.state.String(), func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)
			// The peer's SYN and FIN take a sequence number each, and a hole
			// of one octet follows them.
			beyondAHole := overrunningFIN(1+1+1, localSeq.Add(1))

			// The ACK still acknowledges the peer's FIN and no more.
			sendExpectingReply(t, s, beyondAHole, ackFromLocal(1, defaultBufferLen, withSeq(test.sent)))

			assertState(t, s, test.state)
			compareField(t, "remoteWindowLen", s.remoteWindowLen, int(unseenWindow))
			if s.assembler.IsEmpty() {
				t.Error("the assembler is empty: the segment's data was dropped")
			}
		})
	}
}

func TestProcessPanicsOnAnACKThatAcceptsWouldRefuse(t *testing.T) {
	t.Parallel()
	s := socketListen(t)
	defer func() {
		if recover() == nil {
			t.Error("Process took an ACK in LISTEN")
		}
	}()

	s.Process(s.cx, sendIPTemplate, remoteSYN(withAck(localSeq)))
}

// Every segment after the SYNs must carry an ACK, and the socket drops data
// in one that does not. smoltcp's test_established_no_ack sends an empty
// segment, which leaves nothing to show whether the socket took it.
func TestDataWithoutAnACKIsDropped(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	send(t, s, dataFromRemote(0, "abc", withoutAck()))

	compareField(t, "ReceiveQueue", s.ReceiveQueue(), 0)
	receiveNothing(t, s)
}

// A RST ends a connection, and the socket queues none of the data it
// carries: not in ESTABLISHED, which the RST closes, nor in a SYN-RECEIVED
// that came from LISTEN, which it sends back to listening, where the data
// would wait for the next connection. smoltcp's tests send RSTs without
// data.
func TestTheDataOfARSTIsNotQueued(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		socket    func(t *testing.T) *testSocket
		wantState State
	}{
		{"ESTABLISHED", socketEstablished, StateClosed},
		{"SYN-RECEIVED from LISTEN", socketSynReceivedFromListen, StateListen},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)

			send(t, s, dataFromRemote(0, "abc", withControl(wire.TCPControlRST)))

			assertState(t, s, test.wantState)
			compareField(t, "ReceiveQueue", s.ReceiveQueue(), 0)
		})
	}
}

// socketSynReceivedFromListen returns a socket that listened on listenEnd,
// took remoteSYN and sent its SYN|ACK, announcing its receive window.
func socketSynReceivedFromListen(t *testing.T) *testSocket {
	t.Helper()
	s := socketListen(t)
	send(t, s, remoteSYN())
	receive(t, s, synACK())
	return s
}

// =========================================================================
// The closing states
// =========================================================================

// CLOSING lasts until the peer acknowledges the socket's FIN (RFC 9293): an
// acknowledgement of what came before the FIN keeps the socket there, and
// the FIN goes out again when its retransmission is due. smoltcp's
// test_closing_ack_fin acknowledges the FIN at once.
func TestClosingLastsUntilItsFINIsAcknowledged(t *testing.T) {
	t.Parallel()
	s := socketClosing(t)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1))))

	assertState(t, s, StateClosing)
	receive(t, s.at(time.Second),
		fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))
}

// =========================================================================
// Replies
// =========================================================================

func TestRSTReplyMatchesTheSegmentItAnswers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		segment wire.TCPRepr
		want    wire.TCPRepr
	}{
		{
			"a SYN without ACK gets RST|ACK of the SYN and its data",
			remoteSYN(withPayload([]byte("ab"))),
			fromLocal(withControl(wire.TCPControlRST), withSeq(0), withAck(remoteSeq.Add(1+2)), withWindow(0)),
		},
		{
			"a segment with an ACK gets a RST at its acknowledgement number",
			fromRemote(withSeq(remoteSeq), withAck(localSeq)),
			fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq), withoutAck(), withWindow(0)),
		},
		{
			"any other segment without ACK gets a bare RST",
			fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq), withoutAck(), withPayload([]byte("ab"))),
			fromLocal(withControl(wire.TCPControlRST), withSeq(0), withoutAck(), withWindow(0)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ip, rst := RSTReply(ipHeader(remoteAddr, localAddr, test.segment), test.segment)

			checkOutgoingIPHeader(t, ip, rst)
			assertSegment(t, rst, test.want)
		})
	}
}

// smoltcp's own tests of the challenge-ACK rate limit look well after its
// second; the next challenge ACK may go as soon as the second is over
// (smoltcp's cx.now() < self.challenge_ack_timer).
func TestChallengeACKsAreLimitedToOnePerSecond(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	outsideWindow := fromRemote(withSeq(remoteSeq.Add(1+256)), withAck(localSeq.Add(1)))
	challengeACK := ackFromLocal(0, defaultBufferLen)
	sendExpectingReply(t, s, outsideWindow, challengeACK)

	send(t, s.at(999*time.Millisecond), outsideWindow)
	sendExpectingReply(t, s.at(time.Second), outsideWindow, challengeACK)
}

// A segment that draws a challenge ACK is dropped whether or not the rate
// limit withholds the ACK: the socket takes nothing from it, as smoltcp's
// process returns at once with what challenge_ack_reply gives, nothing when
// the ACK is withheld. The segments TestChallengeACKsAreLimitedToOnePerSecond
// repeats would change nothing if taken.
func TestASegmentWhoseChallengeACKIsWithheldIsDropped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		socket func(t *testing.T) *testSocket
		// segment announces unseenWindow, so that a socket taking it shows.
		segment      wire.TCPRepr
		challengeACK wire.TCPRepr
	}{
		{
			"an ACK of data never sent",
			socketWithDataInFlight,
			ackFromRemote(len("abc")+10, withWindow(unseenWindow)),
			ackFromLocal(0, defaultBufferLen, withSeq(localSeq.Add(1+len("abc")))),
		},
		{
			"an empty segment outside the receive window",
			socketEstablished,
			ackFromRemote(0, withSeq(remoteSeq.Add(1+256)), withWindow(unseenWindow)),
			ackFromLocal(0, defaultBufferLen),
		},
		{
			"a duplicate ACK in LAST-ACK",
			socketLastAckWithItsFINSent,
			ackFromRemote(0, withSeq(remoteSeq.Add(1+1)), withWindow(unseenWindow)),
			ackFromLocal(1, defaultBufferLen, withSeq(localSeq.Add(1+1))),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)
			sendExpectingReply(t, s, test.segment, test.challengeACK)

			send(t, s.at(500*time.Millisecond), test.segment)

			// Neither segment changed the socket: it is as a fresh one.
			want := test.socket(t)
			sanity(t, s, want)
			compareField(t, "SendQueue", s.SendQueue(), want.SendQueue())
		})
	}
}

// socketWithDataInFlight returns an established socket that has sent "abc",
// not yet acknowledged.
func socketWithDataInFlight(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc"))
	return s
}

// Data outside the receive window is answered every time, even within the
// second that limits challenge ACKs: it is a retransmission whose ACK was
// lost, which the peer stops sending only once an ACK gets through. smoltcp's
// test_old_data_ack_not_rate_limited retransmits plain data; data with FIN
// or PSH is exempt from the limit too.
func TestRetransmittedDataIsAnsweredEveryTime(t *testing.T) {
	t.Parallel()
	const data = "abcdef"
	tests := []struct {
		name    string
		control wire.TCPControl
		// received is how far the socket has received the peer's stream:
		// the data, and its FIN if any.
		received int
	}{
		{"with FIN", wire.TCPControlFIN, len(data) + 1},
		{"with PSH", wire.TCPControlPSH, len(data)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketEstablished(t)
			segment := dataFromRemote(0, data, withControl(test.control))
			send(t, s, segment)
			duplicateACK := ackFromLocal(test.received, uint16(defaultBufferLen-len(data)))
			// The peer misses this ACK, and sends its segment again.
			receive(t, s, duplicateACK)

			sendExpectingReply(t, s.at(100*time.Millisecond), segment, duplicateACK)
			sendExpectingReply(t, s.at(200*time.Millisecond), segment, duplicateACK)
		})
	}
}

func TestSACKBlockNamesTheLatestSegmentOrElseTheLowestBlock(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteHasSACK = true
	// Segments beyond holes: each ACK names the block of the segment that
	// triggered it.
	for _, offset := range []int{10, 20, 30} {
		sendExpectingReply(t, s,
			fromRemote(withSeq(remoteSeq.Add(1+offset)), withAck(localSeq.Add(1)), withPayload([]byte("xx"))),
			fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)),
				withSACKRanges(sackRangeOf(remoteSeq.Add(1+offset), remoteSeq.Add(1+offset+2)))))
	}

	// A segment that advances the acknowledgement number is in no block: the
	// ACK names the lowest one left.
	sendExpectingReply(t, s,
		fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("aaaaaaaaaa"))),
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+12)), withWindow(defaultBufferLen-12),
			withSACKRanges(sackRangeOf(remoteSeq.Add(1+20), remoteSeq.Add(1+22)))))
}

// RFC 2018 has the first SACK block hold the segment that triggered the ACK,
// so a segment that extends a block is named by the whole block, whether it
// extends the lower of two blocks or the higher. The higher one shows it:
// when no block holds the segment, the ACK names the lowest block instead
// (TestSACKBlockNamesTheLatestSegmentOrElseTheLowestBlock). smoltcp's tests
// extend only a lone block, which is the lowest as well.
func TestSACKBlockOfASegmentThatExtendsABlockIsTheWholeBlock(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteHasSACK = true
	// sackOf names the octets from start to end of the peer's stream.
	sackOf := func(start, end int) segmentOption {
		return withSACKRanges(sackRangeOf(remoteSeq.Add(1+start), remoteSeq.Add(1+end)))
	}
	sendExpectingReply(t, s, dataFromRemote(10, "0123456789"), ackFromLocal(0, defaultBufferLen, sackOf(10, 20)))
	sendExpectingReply(t, s, dataFromRemote(30, "0123456789"), ackFromLocal(0, defaultBufferLen, sackOf(30, 40)))

	sendExpectingReply(t, s, dataFromRemote(20, "01234"), ackFromLocal(0, defaultBufferLen, sackOf(10, 25)))
	sendExpectingReply(t, s, dataFromRemote(40, "01234"), ackFromLocal(0, defaultBufferLen, sackOf(30, 45)))
}

// =========================================================================
// The receive window
// =========================================================================

// Until its SYN|ACK goes out, a socket in SYN-RECEIVED has announced no
// receive window, so it takes no data: it answers data with an ACK and stays
// in SYN-RECEIVED. smoltcp's tests send data only after the SYN|ACK.
func TestNoDataIsTakenBeforeTheSYNACKAnnouncesAWindow(t *testing.T) {
	t.Parallel()
	s := socketSynReceived(t)

	sendExpectingReply(t, s, dataFromRemote(0, "abc"), fromLocal(withSeq(localSeq), withAck(remoteSeq.Add(1))))

	assertState(t, s, StateSynReceived)
	compareField(t, "ReceiveQueue", s.ReceiveQueue(), 0)
}

// smoltcp's own tests leave the edges of RFC 9293's acceptability test
// unchecked. With "abc" received but not acknowledged, the window the socket
// last announced, 64 octets from its ACK of the SYN, runs from octet 3 to
// octet 64 of the peer's stream.
func TestSegmentsAtTheReceiveWindowsEnd(t *testing.T) {
	t.Parallel()
	const windowEnd = defaultBufferLen
	tests := []struct {
		name       string
		segment    wire.TCPRepr
		answered   bool
		wantQueued int
	}{
		{"data up to the end is taken", dataFromRemote(0, string(make([]byte, windowEnd))), false, windowEnd},
		{"data from the end is answered", dataFromRemote(windowEnd, "x"), true, len("abc")},
		{"an empty segment at the end is answered", ackFromRemote(0, withSeq(remoteSeq.Add(1+windowEnd))), true, len("abc")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketEstablished(t)
			send(t, s, dataFromRemote(0, "abc"))

			if test.answered {
				sendExpectingReply(t, s, test.segment, ackFromLocal(3, defaultBufferLen-3))
			} else {
				send(t, s, test.segment)
			}

			compareField(t, "ReceiveQueue", s.ReceiveQueue(), test.wantQueued)
		})
	}
}

// The window field announces the window shifted right by the window scale,
// so a socket with a scaled window takes data up to the field's value
// shifted left again.
func TestScaledReceiveWindowTakesDataUpToItsScaledEnd(t *testing.T) {
	t.Parallel()
	// 65536 octets need a window scale of 1: the field says 32768.
	const bufferLen, announcedWindow = 1 << 16, 1 << 15
	const offsetPastTheField = announcedWindow + 10_000
	s := socketEstablishedWithBufferSizes(t, defaultBufferLen, bufferLen)
	sendExpectingReply(t, s, dataFromRemote(offsetPastTheField, "xyz"), ackFromLocal(0, announcedWindow))

	sendExpectingReply(t, s, dataFromRemote(0, string(make([]byte, offsetPastTheField))),
		ackFromLocal(offsetPastTheField+3, (bufferLen-offsetPastTheField-3)>>1))

	compareField(t, "ReceiveQueue", s.ReceiveQueue(), offsetPastTheField+3)
}

// RFC 9293's acceptability test lets a zero window take an empty segment
// only at its start; smoltcp's own tests send none elsewhere.
func TestZeroWindowTakesAnEmptySegmentOnlyAtItsStart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		offset   int
		answered bool
	}{
		{"at the window's start", smallBufferLen, false},
		{"beyond the window's start", smallBufferLen + 1, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketEstablished(t)
			replaceReceiveBuffer(s, smallBufferLen)
			send(t, s, dataFromRemote(0, "abcdef"))
			closedWindowACK := ackFromLocal(smallBufferLen, 0)
			receiveOnly(t, s, closedWindowACK)
			segment := ackFromRemote(0, withSeq(remoteSeq.Add(1+test.offset)))

			if test.answered {
				sendExpectingReply(t, s, segment, closedWindowACK)
			} else {
				send(t, s, segment)
			}
		})
	}
}

// The assembler tracks at most storage.AssemblerMaxSegmentCount runs of data;
// smoltcp drops a segment that would need one more, without an ACK.
func TestSegmentBeyondTooManyHolesIsDroppedUnanswered(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	outOfOrderACK := ackFromLocal(0, defaultBufferLen)
	// Each segment leaves a hole of one octet before it.
	for run := range storage.AssemblerMaxSegmentCount {
		sendExpectingReply(t, s, dataFromRemote(1+2*run, "x"), outOfOrderACK)
	}

	send(t, s, dataFromRemote(1+2*storage.AssemblerMaxSegmentCount, "x"))
}

// A SYN announces its window unscaled, which smoltcp reads with the
// socket's scale until the socket sends its next segment: with a receive
// buffer that needs a scale, it took data beyond the buffer's end then, and
// queuing that data panicked or handed the application octets the peer never
// sent. The port takes data up to the buffer's end only (PORTING-ipstack.md,
// section 9).
func TestTheWindowOfASYNReachesNoFurtherThanTheBuffer(t *testing.T) {
	t.Parallel()
	// A buffer of 128 KiB needs a window scale of 2: the window field of the
	// socket's SYN, 65535, read with it, would reach past 256 KiB.
	const bufferLen, shift = 128 << 10, 2
	tests := []struct {
		name string
		// handshake brings the socket to ESTABLISHED; it has sent nothing
		// since its SYN or SYN|ACK.
		handshake func(t *testing.T, s *testSocket)
	}{
		{"passive open", func(t *testing.T, s *testSocket) {
			if err := s.Listen(listenEnd); err != nil {
				t.Fatalf("Listen: %v", err)
			}
			send(t, s, remoteSYN(withWindowScale(0)))
			receive(t, s, synACK(withWindowScale(shift), withWindow(unscaledWindow(bufferLen))))
			send(t, s, ackFromRemote(0))
		}},
		{"active open", func(t *testing.T, s *testSocket) {
			if err := s.Connect(s.cx, remoteEnd, listenEnd); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			receive(t, s, initialSYN(withWindowScale(shift), withWindow(unscaledWindow(bufferLen))))
			send(t, s, remoteSYN(withAck(localSeq.Add(1)), withWindowScale(0)))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketClosedWithBufferSizes(t, defaultBufferLen, bufferLen)
			test.handshake(t, s)
			assertState(t, s, StateEstablished)

			sendExpectingReply(t, s, dataFromRemote(bufferLen, "beyond"), ackFromLocal(0, bufferLen>>shift))

			if !s.assembler.IsEmpty() {
				t.Fatalf("the assembler holds %v: data beyond the receive buffer was taken", &s.assembler)
			}
			fillInOrder(t, s, bufferLen)
			compareField(t, "ReceiveQueue", s.ReceiveQueue(), bufferLen)
		})
	}
}

// fillInOrder has the peer send the socket length octets in order, from the
// start of its stream, a segment of baseMSS octets at a time.
func fillInOrder(t *testing.T, s *testSocket, length int) {
	t.Helper()
	segment := make([]byte, baseMSS)
	for offset := 0; offset < length; offset += len(segment) {
		send(t, s, dataFromRemote(offset, string(segment[:min(len(segment), length-offset)])))
	}
}

// smoltcp's acceptability tests compare sequence numbers modulo 2³², where of
// two numbers 2³¹ apart each compares as before the other. So in a receive
// window that ends before it starts they take a segment 2³¹ − 1 or 2³¹ after
// the window's start for one inside it, and data that starts or ends there.
// smoltcp then panics as it trims the segment to the window, takes a RST from
// there, or places the data far beyond the receive buffer, where its SACK
// block overflows. The port finds every such segment outside the window
// (PORTING-ipstack.md, section 9): it drops a RST, answers anything else with
// an ACK, and takes nothing from it.
func TestAWindowThatEndsBeforeItStartsTakesNoSegmentHalfTheSequenceSpaceAway(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		control wire.TCPControl
		// offset is where the segment starts, relative to the sequence number
		// maxSeqOffset, 2³¹ − 1, after the window's start.
		offset  int
		payload string
	}{
		{"an empty RST 2³¹ − 1 away", wire.TCPControlRST, 0, ""},
		{"a RST whose data starts 2³¹ − 1 away", wire.TCPControlRST, 0, "ab"},
		{"a RST whose data ends 2³¹ − 1 away", wire.TCPControlRST, -len("ab"), "ab"},
		{"an empty ACK 2³¹ − 1 away", wire.TCPControlNone, 0, ""},
		{"data that starts 2³¹ − 1 away", wire.TCPControlNone, 0, "ab"},
		{"data that ends 2³¹ − 1 away", wire.TCPControlNone, -len("ab"), "ab"},
		// Data that ends 2³¹ away starts 2³¹ − 2 away, so that neither of its
		// ends lies 2³¹ − 1 away.
		{"an empty RST 2³¹ away", wire.TCPControlRST, 1, ""},
		{"a RST whose data starts 2³¹ away", wire.TCPControlRST, 1, "ab"},
		{"a RST whose data ends 2³¹ away", wire.TCPControlRST, 1 - len("ab"), "ab"},
		{"an empty ACK 2³¹ away", wire.TCPControlNone, 1, ""},
		{"data that starts 2³¹ away", wire.TCPControlNone, 1, "ab"},
		{"data that ends 2³¹ away", wire.TCPControlNone, 1 - len("ab"), "ab"},
	}
	for _, window := range windowsEndingBeforeTheyStart {
		t.Run(window.name, func(t *testing.T) {
			t.Parallel()
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					t.Parallel()
					s := window.socket(t)
					assertWindowEndsBeforeItStarts(t, s)
					far := s.nextExpectedSeq().Add(maxSeqOffset)
					// A RST needs no acknowledgement number, so a forged one
					// need not guess it.
					acknowledgement := withAck(s.remoteLastSeq)
					if test.control == wire.TCPControlRST {
						acknowledgement = withoutAck()
					}
					segment := fromRemote(withControl(test.control), withSeq(far.Add(test.offset)), acknowledgement,
						withWindow(unseenWindow), withPayload([]byte(test.payload)))

					if test.control == wire.TCPControlRST {
						send(t, s, segment)
					} else {
						sendExpectingReply(t, s, segment, window.ack)
					}

					assertTookNothing(t, s, window.socket(t))
				})
			}
		})
	}
}

// smoltcp's acceptability tests find an empty segment at the start of a
// receive window that ends before it starts to lie outside the window, so
// the socket answers it with a challenge ACK and takes nothing from it:
// neither the end of the handshake, nor an acknowledgement of its data, nor
// the peer's window. The port keeps that: smoltcp neither panics on the
// segment nor corrupts a stream with it.
func TestAWindowThatEndsBeforeItStartsAnswersASegmentAtItsStartWithAChallengeACK(t *testing.T) {
	t.Parallel()
	for _, window := range windowsEndingBeforeTheyStart {
		t.Run(window.name, func(t *testing.T) {
			t.Parallel()
			s := window.socket(t)
			assertWindowEndsBeforeItStarts(t, s)
			atTheStart := fromRemote(withSeq(s.nextExpectedSeq()), withAck(s.remoteLastSeq), withWindow(unseenWindow))

			sendExpectingReply(t, s, atTheStart, window.ack)

			assertTookNothing(t, s, window.socket(t))
		})
	}
}

// windowEndingBeforeItStarts is a socket whose receive window ends before it
// starts, as one of the ways there leaves it (windowEndsBeforeItStarts).
type windowEndingBeforeItStarts struct {
	name   string
	socket func(t *testing.T) *testSocket
	// ack is the ACK with which the socket answers a segment outside its
	// receive window.
	ack wire.TCPRepr
}

// windowsEndingBeforeTheyStart are the ways to a receive window that ends
// before it starts. smoltcp's tests take none of them.
var windowsEndingBeforeTheyStart = []windowEndingBeforeItStarts{
	{"a peer's SYN before the socket's own", socketWithAPeersSYNBeforeItsOwn, ackFromLocal(0, defaultBufferLen)},
	{"a FIN right at the window's end", socketWithAFINAtTheWindowsEnd,
		ackFromLocal(smallBufferLen+1, 0, withSeq(localSeq.Add(1+len("abc"))))},
	{"a SYN|ACK to a socket without a receive buffer", socketConnectedWithoutAReceiveBuffer, ackFromLocal(0, 0)},
}

// socketWithAPeersSYNBeforeItsOwn returns a connecting socket that took
// remoteSYN, SACK permitted, before its own SYN went out: it is in
// SYN-RECEIVED, and has announced no window.
func socketWithAPeersSYNBeforeItsOwn(t *testing.T) *testSocket {
	t.Helper()
	s := socketClosed(t)
	if err := s.Connect(s.cx, remoteEnd, listenEnd); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	send(t, s, remoteSYN(withSACKPermitted()))
	return s
}

// socketWithAFINAtTheWindowsEnd returns a socket in CLOSE-WAIT, the "abc" it
// sent in flight, that took the peer's data and FIN in one segment, the data
// filling its receive window of smallBufferLen octets.
func socketWithAFINAtTheWindowsEnd(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablishedWithBufferSizes(t, defaultBufferLen, smallBufferLen)
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc", withWindow(smallBufferLen)))
	send(t, s, dataFromRemote(0, "abcdef", withControl(wire.TCPControlFIN)))
	return s
}

// socketConnectedWithoutAReceiveBuffer returns a socket with a receive buffer
// of no octets that connected: its SYN announced a zero window, and the
// peer's SYN|ACK has come.
func socketConnectedWithoutAReceiveBuffer(t *testing.T) *testSocket {
	t.Helper()
	s := socketClosedWithBufferSizes(t, defaultBufferLen, 0)
	if err := s.Connect(s.cx, remoteEnd, listenEnd); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	receive(t, s, initialSYN(withWindow(0)))
	send(t, s, remoteSYN(withAck(localSeq.Add(1))))
	return s
}

// assertWindowEndsBeforeItStarts fails the test unless the receive window
// that s last recorded, its acknowledgement number and window field, ends a
// sequence number before the next one s expects from the peer.
func assertWindowEndsBeforeItStarts(t *testing.T, s *testSocket) {
	t.Helper()
	lastAck, recorded := s.remoteLastAck.Get()
	windowEnd := lastAck.Add(int(s.remoteLastWindow) << s.remoteWindowShift)
	if !recorded || windowEnd != s.nextExpectedSeq().Add(-1) {
		t.Fatalf("the receive window ends at %v (recorded: %v), and the next sequence number expected is %v; "+
			"want the window to end a sequence number before it", windowEnd, recorded, s.nextExpectedSeq())
	}
}

// assertTookNothing fails the test unless s took nothing from the segments it
// was given since it was as fresh is: no change of state, and none of the
// peer's data, its window, or its acknowledgement of what s sent.
func assertTookNothing(t *testing.T, s, fresh *testSocket) {
	t.Helper()
	assertState(t, s, fresh.state)
	compareField(t, "ReceiveQueue", s.ReceiveQueue(), fresh.ReceiveQueue())
	compareField(t, "SendQueue", s.SendQueue(), fresh.SendQueue())
	compareField(t, "remoteWindowLen", s.remoteWindowLen, fresh.remoteWindowLen)
	if !s.assembler.IsEmpty() {
		t.Errorf("the assembler holds %v: the segment's data was taken", &s.assembler)
	}
}

// RFC 9293 (3.10.7.4) trims a segment to the receive window "including SYN
// and FIN", so a FIN that follows data the window cut off is not taken.
// smoltcp takes it, ending the stream at the window's end and acknowledging
// the first octet cut off as delivered, which the peer then never sends
// again (PORTING-ipstack.md, section 9). The port ignores that FIN, and the
// peer's retransmission of the rest brings it again.
func TestAFINAfterDataCutAtTheWindowsEndWaitsForTheRest(t *testing.T) {
	t.Parallel()
	// A buffer of 128 KiB needs a window scale of 2.
	const scaledBufferLen, scaledBufferShift = 128 << 10, 2
	tests := []struct {
		name             string
		receiveBufferLen int
		// stream is everything the peer sends. Its last segment, from
		// lastSegmentStart on, carries the FIN.
		stream           []byte
		lastSegmentStart int
		// arrange has the peer send what comes before the last segment.
		arrange func(t *testing.T, s *testSocket)
		// windowEnd is where the receive window the socket last announced
		// ends, as an offset into the stream: the window cuts the last
		// segment there.
		windowEnd int
	}{
		{
			name:             "a peer that overruns the window",
			receiveBufferLen: defaultBufferLen,
			stream:           []byte(strings.Repeat("x", defaultBufferLen) + "y"),
			arrange:          func(*testing.T, *testSocket) {},
			windowEnd:        defaultBufferLen,
		},
		{
			// The window field counts units of 4 octets, and announces a
			// free space that is no multiple of 4 rounded down. With 8
			// octets free, the window ends at the buffer's end; the octet
			// "a" leaves 7, announced as 4, which moves the window's end 3
			// octets back. The peer sent "bcdefgh" and its FIN within the
			// window announced before.
			name:             "a window that scaling moved back",
			receiveBufferLen: scaledBufferLen,
			stream:           append(make([]byte, scaledBufferLen-8), "abcdefgh"...),
			lastSegmentStart: scaledBufferLen - 7,
			arrange: func(t *testing.T, s *testSocket) {
				fillInOrder(t, s, scaledBufferLen-8)
				receive(t, s, ackFromLocal(scaledBufferLen-8, 8>>scaledBufferShift))
				send(t, s, dataFromRemote(scaledBufferLen-8, "a"))
				receive(t, s, ackFromLocal(scaledBufferLen-7, 7>>scaledBufferShift))
			},
			windowEnd: scaledBufferLen - 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketEstablishedWithBufferSizes(t, defaultBufferLen, test.receiveBufferLen)
			test.arrange(t, s)
			announced := func(free int) uint16 { return uint16(free >> s.remoteWindowShift) }

			send(t, s, dataFromRemote(test.lastSegmentStart, string(test.stream[test.lastSegmentStart:]),
				withControl(wire.TCPControlFIN)))

			// The socket takes the data up to the window's end, and
			// acknowledges no more than that.
			assertState(t, s, StateEstablished)
			receive(t, s, ackFromLocal(test.windowEnd, 0))
			received := readQueued(t, s)
			receive(t, s, ackFromLocal(test.windowEnd, announced(test.receiveBufferLen)))
			// The peer sends the rest again, from that acknowledgement on.
			rest := test.stream[test.windowEnd:]
			send(t, s, dataFromRemote(test.windowEnd, string(rest), withControl(wire.TCPControlFIN)))
			assertState(t, s, StateCloseWait)
			receive(t, s, ackFromLocal(len(test.stream)+1, announced(test.receiveBufferLen-len(rest))))
			received = append(received, readQueued(t, s)...)
			if !bytes.Equal(received, test.stream) {
				t.Errorf("the application read %d octets, ending %q; want the %d the peer sent, ending %q",
					len(received), tail(received), len(test.stream), tail(test.stream))
			}
			assertReceiveFails(t, s, ErrFinished)
		})
	}
}

// Every state that takes a FIN ignores one that follows data the receive
// window cut off, and moves only as the segment's acknowledgement says. The
// states that take none handle such a segment as smoltcp does: they drop it
// whole (TestSegmentsTheStateDoesNotTakeAreDropped), but for one beyond a
// hole, whose FIN smoltcp disregards in every state
// (TestCloseWaitAndClosingTakeASegmentBeyondAHoleWithoutItsFIN).
func TestAFINAfterDataCutAtTheWindowsEndIsIgnoredInEveryStateThatTakesAFIN(t *testing.T) {
	t.Parallel()
	overrun := strings.Repeat("x", defaultBufferLen) + "y"
	tests := []struct {
		state State
		// socket returns a socket in state that has announced a window of
		// defaultBufferLen octets.
		socket func(t *testing.T) *testSocket
		// acknowledged is what the peer's segment acknowledges: the socket's
		// SYN, and in FIN-WAIT-2 its FIN.
		acknowledged wire.TCPSeqNumber
		// wantState is where that acknowledgement alone takes the socket.
		wantState State
	}{
		{StateSynReceived, socketSynReceivedWithItsSYNACKSent, localSeq.Add(1), StateEstablished},
		{StateEstablished, socketEstablished, localSeq.Add(1), StateEstablished},
		{StateFinWait1, socketFinWait1WithItsFINSent, localSeq.Add(1), StateFinWait1},
		{StateFinWait2, socketFinWait2, localSeq.Add(1 + 1), StateFinWait2},
	}
	for _, test := range tests {
		t.Run(test.state.String(), func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)

			send(t, s, dataFromRemote(0, overrun, withControl(wire.TCPControlFIN), withAck(test.acknowledged)))

			assertState(t, s, test.wantState)
			if s.finReceived {
				t.Error("the socket took the FIN")
			}
			compareField(t, "ReceiveQueue", s.ReceiveQueue(), defaultBufferLen)
		})
	}
}

// socketSynReceivedWithItsSYNACKSent returns a socket in SYN-RECEIVED whose
// SYN|ACK, announcing its window, has gone out.
func socketSynReceivedWithItsSYNACKSent(t *testing.T) *testSocket {
	t.Helper()
	s := socketSynReceived(t)
	receive(t, s, synACK())
	return s
}

// readQueued reads every octet the socket has received and not yet read.
func readQueued(t *testing.T, s *testSocket) []byte {
	t.Helper()
	data := make([]byte, s.ReceiveQueue())
	if received := mustReceiveSlice(t, s, data); received != len(data) {
		t.Fatalf("ReceiveSlice read %d of the %d octets queued", received, len(data))
	}
	return data
}

// tail returns the last octets of data, for test failures.
func tail(data []byte) []byte {
	const tailLen = 8
	return data[max(len(data)-tailLen, 0):]
}

// =========================================================================
// The peer's window
// =========================================================================

// smoltcp's tests of window scaling check the scale a peer's SYN announces,
// and that the SYN's own window is not scaled. The window of every later
// segment is shifted left by that scale (RFC 7323).
func TestThePeersWindowScaleAppliesToItsLaterSegments(t *testing.T) {
	t.Parallel()
	const scale, synWindow = 7, 42
	tests := []struct {
		name string
		// handshake brings the socket to the peer's SYN with a window scale.
		handshake func(t *testing.T) *testSocket
	}{
		{"passive open", func(t *testing.T) *testSocket {
			s := socketListen(t)
			send(t, s, remoteSYN(withWindowScale(scale), withWindow(synWindow)))
			receive(t, s, synACK(withWindowScale(0)))
			return s
		}},
		{"active open", func(t *testing.T) *testSocket {
			s := socketSynSent(t)
			receive(t, s, initialSYN())
			send(t, s, remoteSYN(withAck(localSeq.Add(1)), withWindowScale(scale), withWindow(synWindow)))
			return s
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.handshake(t)
			compareField(t, "remoteWindowLen after the SYN", s.remoteWindowLen, synWindow)

			send(t, s, ackFromRemote(0, withWindow(5)))

			compareField(t, "remoteWindowLen", s.remoteWindowLen, 5<<scale)
		})
	}
}

// =========================================================================
// Keep-alive after a segment
// =========================================================================

// RFC 6298 (5.2) turns the retransmission timer off once everything sent is
// acknowledged, and the idle timer it leaves behind times keep-alives again:
// the next one goes a whole interval after the acknowledgement. smoltcp's
// test_sends_keep_alive stays idle throughout.
func TestKeepAliveResumesOnceEverythingSentIsAcknowledged(t *testing.T) {
	t.Parallel()
	const interval, acknowledgedAt = 10 * time.Second, 100 * time.Millisecond
	s := socketWithDataInFlight(t)
	// While the retransmission timer runs, keep-alive waits.
	s.SetKeepAlive(interval)

	send(t, s.at(acknowledgedAt), ackFromRemote(len("abc")))

	assertPollAt(t, s, socket.PollAtTime(instant(acknowledgedAt+interval)))
	receiveNothing(t, s.at(acknowledgedAt+interval-time.Millisecond))
	receive(t, s.at(acknowledgedAt+interval), keepAliveFromLocal(localSeq.Add(1+len("abc"))))
}

// The zero-window probe timer stops when the peer's window opens, and the
// idle timer it leaves behind times keep-alives again. Here the probe
// carries the last octet queued, which the window update acknowledges, so
// that nothing is left to send: the next keep-alive goes a whole interval
// after the update. smoltcp's tests of zero-window probes run without
// keep-alive.
func TestKeepAliveResumesWhenTheProbedWindowOpens(t *testing.T) {
	t.Parallel()
	const interval, openedAt = 10 * time.Second, 1100 * time.Millisecond
	s := socketEstablished(t)
	s.SetKeepAlive(interval)
	send(t, s, peerWindow(localSeq.Add(1), 0))
	mustSendSlice(t, s, []byte("a"))
	receiveOnly(t, s.at(time.Second), probeCarrying(localSeq.Add(1), "a"))

	send(t, s.at(openedAt), peerWindow(localSeq.Add(1+1), defaultBufferLen))

	assertPollAt(t, s, socket.PollAtTime(instant(openedAt+interval)))
	receiveNothing(t, s.at(openedAt+interval-time.Millisecond))
	receive(t, s.at(openedAt+interval), keepAliveFromLocal(localSeq.Add(1+1)))
}

// =========================================================================
// Options
// =========================================================================

// The port has no timestamp generator (PORTING-ipstack.md, section 3), so it
// never sends the timestamps option, not even to a peer that sends it.
func TestTimestampsAreNeverSent(t *testing.T) {
	t.Parallel()
	s := socketListen(t)

	send(t, s, remoteSYN(withTimestamp(500, 0)))
	receiveOnly(t, s, synACK())
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withTimestamp(501, 1)))
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withTimestamp(502, 1),
		withPayload([]byte("abc"))))

	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+3)), withWindow(defaultBufferLen-3)))
}
