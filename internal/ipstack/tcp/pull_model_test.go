package tcp

// Tests of the pull model: what Dispatch leaves undone when emit refuses a
// segment, and what it keeps.

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// errLineBusy is what the tests' emit returns when it refuses a segment, as
// a device without a transmit slot makes the interface's emit do.
var errLineBusy = errors.New("the line is busy")

// dispatchRefused runs Dispatch with an emit that refuses the segment, and
// returns the segment the socket offered, its payload copied. It fails the
// test unless Dispatch offered exactly one segment and returned emit's error
// unchanged.
func dispatchRefused(t *testing.T, s *testSocket) wire.TCPRepr {
	t.Helper()
	s.cx.SetNow(instant(s.elapsed))
	var offered []wire.TCPRepr
	err := s.Dispatch(s.cx, func(ip wire.IPv4Repr, segment wire.TCPRepr) error {
		checkOutgoingIPHeader(t, ip, segment)
		segment.Payload = bytes.Clone(segment.Payload)
		offered = append(offered, segment)
		return errLineBusy
	})
	if err != errLineBusy {
		t.Fatalf("Dispatch returned %v, want emit's error unchanged", err)
	}
	if len(offered) != 1 {
		t.Fatalf("Dispatch offered %d segments, want 1", len(offered))
	}
	return offered[0]
}

// socketWithDataToSend returns an established socket with "abcdef" queued.
func socketWithDataToSend(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abcdef"))
	return s
}

// socketWithDataToAcknowledge returns an established socket that received
// "abc" and has yet to acknowledge it.
func socketWithDataToAcknowledge(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))
	return s
}

// socketAborted returns an established socket that was aborted, and is to
// send its RST.
func socketAborted(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	s.Abort()
	return s
}

// socketWithKeepAliveDue returns an idle established socket whose keep-alive
// was just turned on, and is due at once.
func socketWithKeepAliveDue(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	s.SetKeepAlive(time.Second)
	return s
}

// socketFacingAZeroWindow returns an established socket with "abcdef" queued
// for a peer whose window is closed. Send times the first probe from the
// epoch, so it is due one RTO, 1 s, after it.
func socketFacingAZeroWindow(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	s.remoteWindowLen = 0
	mustSendSlice(t, s, []byte("abcdef"))
	return s
}

func TestRefusedSegmentGoesOutUnchangedAtTheNextDispatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		socket func(t *testing.T) *testSocket
		// due is when the segment is due.
		due time.Duration
	}{
		{"SYN", socketSynSent, 0},
		{"SYN|ACK", socketSynReceived, 0},
		{"data", socketWithDataToSend, 0},
		{"FIN", socketFinWait1, 0},
		{"ACK", socketWithDataToAcknowledge, 0},
		{"RST", socketAborted, 0},
		{"keep-alive", socketWithKeepAliveDue, 0},
		{"zero-window probe", socketFacingAZeroWindow, time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)
			offered := dispatchRefused(t, s.at(test.due))

			receiveExact(t, s.at(test.due), offered)
		})
	}
}

func TestRetransmissionTimeoutWhileRefusedBacksOffOnce(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abcdef"))
	data := fromLocal(withControl(wire.TCPControlPSH), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)),
		withPayload([]byte("abcdef")))
	receiveExact(t, s, data)

	// The RTO of 1 s expires while the line is busy: the socket rewinds and
	// backs off, but sends nothing.
	retransmission := dispatchRefused(t, s.at(time.Second))
	assertSegment(t, retransmission, data)
	if rto := s.rttEstimator.retransmissionTimeout(); rto != 2*time.Second {
		t.Fatalf("RTO = %v after the refused retransmission, want 2s", rto)
	}

	// The retransmission goes out at the next Dispatch, without a second
	// backoff, and the timer runs from then.
	receiveExact(t, s.at(time.Second), data)
	if rto := s.rttEstimator.retransmissionTimeout(); rto != 2*time.Second {
		t.Errorf("RTO = %v after the retransmission, want 2s", rto)
	}
	assertTimer(t, s.timer, timer{kind: timerRetransmit, expiresAt: instant(3 * time.Second)})
}

func TestRefusedSYNStartsTheTimeoutClock(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)
	s.SetTimeout(10 * time.Second)

	dispatchRefused(t, s)
	receive(t, s.at(8*time.Second), initialSYN())

	// The timeout runs from the refused SYN, not from the one sent.
	receive(t, s.at(10*time.Second), fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(1)), withAck(0)))
	assertState(t, s, StateClosed)
}

func TestTimeoutClosesAtOnceWhileEmitIsRefused(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetTimeout(time.Second)
	mustSendSlice(t, s, []byte("abcdef"))
	receive(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abcdef"))))
	rst := fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(1+6)), withAck(remoteSeq.Add(1)))

	offered := dispatchRefused(t, s.at(time.Second))

	assertSegment(t, offered, rst)
	assertState(t, s, StateClosed)
	assertEndpoints(t, s, localEnd, remoteEnd)
	receiveOnly(t, s.at(time.Second), rst)
	assertEndpoints(t, s, netip.AddrPort{}, netip.AddrPort{})
}

func TestRefusedFastRetransmissionIsNotRepeated(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 3
	mustSendSlice(t, s, []byte("aaaBBBccc"))
	receive(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("aaa"))))
	receive(t, s, fromLocal(withSeq(localSeq.Add(1+3)), withAck(remoteSeq.Add(1)), withPayload([]byte("BBB"))))
	receive(t, s, fromLocal(withSeq(localSeq.Add(1+6)), withAck(remoteSeq.Add(1)), withPayload([]byte("ccc"))))
	duplicateACK := fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)))
	for range 1 + fastRetransmitThreshold {
		send(t, s, duplicateACK)
	}

	fastRetransmission := dispatchRefused(t, s)

	assertSegment(t, fastRetransmission,
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("aaa"))))
	receiveNothing(t, s)
}

func TestRefusedDataIsTimedFromItsTransmission(t *testing.T) {
	t.Parallel()
	s := socketWithDataToSend(t)
	data := dataFromLocal(0, "abcdef")
	dispatchRefused(t, s)

	receive(t, s.at(500*time.Millisecond), data)

	// The retransmission timer runs from the transmission at 500 ms, not from
	// the refused segment: there is no retransmission at 1 s.
	receiveNothing(t, s.at(time.Second))
	// The round trip is measured from the transmission too: 900 ms, for an
	// RTO of 900 + 4 × 450 = 2700 ms (from the refused segment it would be
	// 1400 ms, for an RTO of 4200 ms).
	send(t, s.at(1400*time.Millisecond), ackFromRemote(6))
	if rto := s.rttEstimator.retransmissionTimeout(); rto != 2700*time.Millisecond {
		t.Errorf("RTO = %v after a round trip of 900 ms, want 2.7s", rto)
	}
}
