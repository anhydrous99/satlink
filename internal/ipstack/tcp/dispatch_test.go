package tcp

// Tests of what Dispatch sends, and when, and of PollAt.

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// =========================================================================
// The handshake
// =========================================================================

// In a simultaneous open the socket's SYN goes out first.
// A peer's SYN that arrives before it, while the socket has announced no
// window yet, would make poll_at panic; the socket answers it with its
// SYN|ACK at once.
func TestAPeersSYNBeforeTheSocketsOwnIsAnsweredAtOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// before is what happens to the socket's own SYN before the peer's
		// arrives.
		before func(t *testing.T, s *testSocket)
	}{
		{"SYN not dispatched", func(*testing.T, *testSocket) {}},
		{"SYN refused by emit", func(t *testing.T, s *testSocket) { dispatchRefused(t, s) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketClosed(t)
			if err := s.Connect(s.cx, remoteEnd, listenEnd); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			test.before(t, s)

			send(t, s, remoteSYN())

			assertState(t, s, StateSynReceived)
			assertPollAt(t, s, socket.PollNow())
			receive(t, s, synACK())
		})
	}
}

// A SYN|ACK that answers a SYN announcing a zero window, from a socket
// without a receive buffer, leaves the receive window ending before it
// starts, as an early SYN of the peer's does, and poll_at would then panic;
// the socket acknowledges the SYN|ACK at once.
func TestASYNACKToASocketWithoutAReceiveBufferIsAcknowledgedAtOnce(t *testing.T) {
	t.Parallel()
	s := socketConnectedWithoutAReceiveBuffer(t)
	assertWindowEndsBeforeItStarts(t, s)

	assertPollAt(t, s, socket.PollNow())
	receive(t, s, ackFromLocal(0, 0))
}

// =========================================================================
// Segment sizes
// =========================================================================

// The local MSS follows the IP MTU of the interface's context: over a link of
// 576 octets, satnet's default MTU, the socket's SYN announces 536 octets,
// and a peer announcing 1460 gets segments of 536 octets all the same.
func TestLocalMSSFollowsTheIPMTU(t *testing.T) {
	t.Parallel()
	const ipMTU, linkMSS = 576, 536
	const queued = 1000
	data := string(bytes.Repeat([]byte("0123456789"), queued/10))
	s := socketClosedWithBufferSizes(t, queued, defaultBufferLen)
	s.cx = newTestContextWith(t, testRandomSeed, ipMTU)
	if err := s.Connect(s.cx, remoteEnd, listenEnd); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	receive(t, s, initialSYN(withMSS(linkMSS)))
	send(t, s, remoteSYN(withAck(localSeq.Add(1)), withMSS(baseMSS), withWindowScale(0), withWindow(queued)))

	mustSendSlice(t, s, []byte(data))

	receive(t, s, dataFromLocal(0, data[:linkMSS]))
}

// The tests compare the MSS the socket takes from the peer's SYN with the
// default and the minimum MSS: a peer whose SYN has no MSS option, or
// an MSS of 0, gets segments of 536 octets, RFC 879's default, and one
// announcing a tiny MSS gets segments of 48 octets.
func TestSegmentsFollowThePeersMSS(t *testing.T) {
	t.Parallel()
	const queued = 1000
	data := string(bytes.Repeat([]byte("0123456789"), queued/10))
	tests := []struct {
		name        string
		syn         wire.TCPRepr
		wantSegment int
	}{
		{"no MSS option", remoteSYN(), 536},
		{"an MSS of 0", remoteSYN(withMSS(0)), 536},
		{"a tiny MSS", remoteSYN(withMSS(tinyMSS)), 48},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketListen(t)
			s.transmitBuffer = NewSocketBuffer(queued)
			send(t, s, test.syn)
			receive(t, s, synACK())
			send(t, s, ackFromRemote(0, withWindow(queued)))

			mustSendSlice(t, s, []byte(data))

			receive(t, s, dataFromLocal(0, data[:test.wantSegment]))
		})
	}
}

// =========================================================================
// Data and its retransmission
// =========================================================================

func TestQueuedDataIsDueAtOnce(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetNagleEnabled(false)
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc"))

	// The socket is not quiet: data is in flight, and the timeout clock runs.
	mustSendSlice(t, s, []byte("def"))

	assertPollAt(t, s, socket.PollNow())
}

// backoffSchedule is when a timer that starts at the initial RTO, 1 s, and
// doubles every time it fires, up to the largest RTO, 60 s, fires: the times
// after it started.
var backoffSchedule = []time.Duration{
	1 * time.Second,
	3 * time.Second,
	7 * time.Second,
	15 * time.Second,
	31 * time.Second,
	63 * time.Second,
	123 * time.Second,
	183 * time.Second,
}

// receiveOnScheduleOnly expects the socket to send want at each time of
// schedule, after the epoch, and nothing a millisecond before.
func receiveOnScheduleOnly(t *testing.T, s *testSocket, schedule []time.Duration, want wire.TCPRepr) {
	t.Helper()
	for _, due := range schedule {
		receiveNothing(t, s.at(due-time.Millisecond))
		receive(t, s.at(due), want)
	}
}

func TestRetransmissionsBackOffUpToSixtySeconds(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc"))
	data := dataFromLocal(0, "abc")
	receive(t, s, data)

	receiveOnScheduleOnly(t, s, backoffSchedule, data)
}

// RFC 6298 (5.1) starts the retransmission timer with a segment only when it
// is not running already, so that it expires an RTO after the oldest
// unacknowledged data went out. The tests send every segment before a
// timeout at the same instant, where a timer restarted by each would expire
// at the same time.
func TestLaterDataDoesNotRestartTheRetransmissionTimer(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetNagleEnabled(false)
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc"))
	mustSendSlice(t, s, []byte("def"))
	receive(t, s.at(500*time.Millisecond), dataFromLocal(3, "def"))

	receiveNothing(t, s.at(999*time.Millisecond))
	receive(t, s.at(time.Second), dataFromLocal(0, "abcdef"))
}

// Karn's algorithm (RFC 6298, section 3): the acknowledgement of data sent
// twice may be for either transmission, so it measures no round trip.
func TestRetransmittedDataIsNotTimed(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc"))
	receive(t, s.at(time.Second), dataFromLocal(0, "abc"))

	send(t, s.at(1500*time.Millisecond), ackFromRemote(3))

	// The RTO stays as the retransmission doubled it.
	if rto := s.rttEstimator.retransmissionTimeout(); rto != 2*time.Second {
		t.Errorf("RTO = %v after the retransmitted data was acknowledged, want 2s", rto)
	}
}

// The estimator only samples. Each acknowledgement of a
// timed segment ends that sample, so the next round trip is timed from a
// segment of its own: the age of the connection does not grow the RTO. The
// second round trip starts long after the first, as the smallest RTO, 1 s,
// would hide a measurement of the time since the first segment shorter than
// about 0.8 s, and it takes longer than the first, as after two equal round
// trips the RTO would be what the first alone gave.
func TestEachRoundTripIsTimedFromItsOwnSegment(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc"))
	send(t, s.at(100*time.Millisecond), ackFromRemote(3))
	mustSendSlice(t, s, []byte("def"))
	receive(t, s.at(20*time.Second), dataFromLocal(3, "def"))

	send(t, s.at(20900*time.Millisecond), ackFromRemote(6))

	// Round trips of 100 ms and 900 ms: SRTT 200 ms and RTTVAR 238 ms, for an
	// RTO of 200 + 4 × 238 = 1152 ms. The first alone gives SRTT 100 ms and
	// RTTVAR 50 ms, for an RTO of 300 ms, rounded up to 1 s.
	if rto := s.rttEstimator.retransmissionTimeout(); rto != 1152*time.Millisecond {
		t.Errorf("RTO = %v after round trips of 100 ms and 900 ms, want 1.152s", rto)
	}
}

// An ACK of later data acknowledges the timed segment too, so it measures the
// segment's round trip: a delayed ACK of two segments, as Linux sends in a
// bulk transfer, measures the first. The tests acknowledge exactly the
// end of the timed segment.
func TestAnACKBeyondTheTimedSegmentMeasuresItsRoundTrip(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetNagleEnabled(false)
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc"))
	mustSendSlice(t, s, []byte("def"))
	receive(t, s, dataFromLocal(3, "def"))

	send(t, s.at(500*time.Millisecond), ackFromRemote(6))

	// A round trip of 500 ms: SRTT 500 ms and RTTVAR 250 ms, for an RTO of
	// 500 + 4 × 250 = 1500 ms. Without a measurement, the RTO would stay at
	// the initial 1 s.
	if rto := s.rttEstimator.retransmissionTimeout(); rto != 1500*time.Millisecond {
		t.Errorf("RTO = %v after an ACK beyond the timed segment at 500 ms, want 1.5s", rto)
	}
}

// An ACK of part of the timed segment does not acknowledge the segment, so it
// measures nothing: the round trip ends with the ACK that reaches the
// segment's end.
func TestAnACKShortOfTheTimedSegmentMeasuresNothing(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc"))

	send(t, s.at(100*time.Millisecond), ackFromRemote(1))
	send(t, s.at(500*time.Millisecond), ackFromRemote(3))

	// A round trip of 500 ms: SRTT 500 ms and RTTVAR 250 ms, for an RTO of
	// 500 + 4 × 250 = 1500 ms. One of 100 ms, ended by the partial ACK, would
	// give SRTT 100 ms and RTTVAR 50 ms, for an RTO of 300 ms, rounded up to
	// 1 s.
	if rto := s.rttEstimator.retransmissionTimeout(); rto != 1500*time.Millisecond {
		t.Errorf("RTO = %v after a partial ACK at 100 ms and the full one at 500 ms, want 1.5s", rto)
	}
}

func TestFastRetransmissionIsDueAtOnce(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc"))
	send(t, s, ackFromRemote(0))

	for range fastRetransmitThreshold {
		send(t, s, ackFromRemote(0))
	}

	assertPollAt(t, s, socket.PollNow())
}

func TestOnlyTheThirdDuplicateACKTriggersAFastRetransmission(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 3
	mustSendSlice(t, s, []byte("aaaBBBccc"))
	receiveOnly(t, s, dataFromLocal(0, "aaa"), dataFromLocal(3, "BBB"), dataFromLocal(6, "ccc"))
	send(t, s, ackFromRemote(0))
	for range fastRetransmitThreshold {
		send(t, s, ackFromRemote(0))
	}
	receiveOnly(t, s, dataFromLocal(0, "aaa"))

	send(t, s, ackFromRemote(0))

	receiveNothing(t, s)
}

// A fast retransmission of the last data queued ends it as its first
// transmission did: with PSH while the socket is open, and with the FIN
// once it has closed. Fast retransmission resends data with more queued
// behind it, and quashes PSH.
func TestAFastRetransmissionOfTheLastDataEndsItAsItsFirstTransmissionDid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// closeAfterQueuing says whether the application closes the socket
		// once it has queued the data.
		closeAfterQueuing bool
		wantControl       wire.TCPControl
	}{
		{"with PSH while open", false, wire.TCPControlPSH},
		{"with the FIN once closed", true, wire.TCPControlFIN},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketEstablished(t)
			mustSendSlice(t, s, []byte("abc"))
			if test.closeAfterQueuing {
				s.Close()
			}
			data := dataFromLocal(0, "abc", withControl(test.wantControl))
			receiveExact(t, s, data)
			// The first ACK of nothing new is fresh; the next ones are
			// duplicates.
			send(t, s, ackFromRemote(0))
			for range fastRetransmitThreshold {
				send(t, s, ackFromRemote(0))
			}

			receiveExact(t, s, data)
		})
	}
}

// =========================================================================
// Other timers
// =========================================================================

func TestZeroWindowProbesBackOffUpToSixtySeconds(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc"))
	// The peer's window closes at the epoch, which starts the zero-window
	// probe timer.
	send(t, s, ackFromRemote(0, withWindow(0)))

	receiveOnScheduleOnly(t, s, backoffSchedule, dataFromLocal(0, "a"))
}

// Data queued while the socket probes a closed window leaves the probes'
// schedule alone: a send starts the probe timer only when the socket times
// nothing else, so it neither makes a probe due at once nor resets the
// backoff. The tests queue data only before the window closes.
func TestDataQueuedWhileProbingKeepsTheProbeSchedule(t *testing.T) {
	t.Parallel()
	s := socketProbingAClosedWindow(t)
	probe := probeCarrying(localSeq.Add(1), "a")
	receiveOnlyWhenDue(t, s, time.Second, probe)

	mustSendSlice(t, s, []byte("more"))

	receiveOnlyWhenDue(t, s, 3*time.Second, probe)
}

// A retransmission timeout leaves the timer idle even when nothing can be
// sent again, as the peer's window has closed since the data went out, and
// the idle timer times keep-alives again: the next one goes a whole interval
// after the timeout, and draws an ACK that tells the window. The tests
// of retransmission run without keep-alive.
func TestKeepAliveResumesAfterARetransmissionTimeoutIntoAClosedWindow(t *testing.T) {
	t.Parallel()
	const interval, timedOutAt = 10 * time.Second, time.Second
	s := socketWithDataInFlight(t)
	s.SetKeepAlive(interval)
	send(t, s.at(100*time.Millisecond), ackFromRemote(0, withWindow(0)))

	receiveNothing(t, s.at(timedOutAt))

	assertPollAt(t, s, socket.PollAtTime(instant(timedOutAt+interval)))
	receiveNothing(t, s.at(timedOutAt+interval-time.Millisecond))
	receive(t, s.at(timedOutAt+interval), keepAliveFromLocal(localSeq.Add(1)))
}

func TestSetKeepAliveArmsAnIdleTimerOnlyOnce(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	keepAlive := keepAliveFromLocal(localSeq.Add(1))
	// Turning keep-alive on makes one due at once, and the next one an
	// interval after it.
	s.SetKeepAlive(time.Second)
	assertTimer(t, s.timer, timer{kind: timerIdle, keepAliveAt: wire.Some(instant(0))})
	receive(t, s, keepAlive)

	s.SetKeepAlive(2 * time.Second)

	// A new interval makes no keep-alive due at once: the one due goes out on
	// time, and the new interval runs from it.
	receiveNothing(t, s.at(time.Second-time.Millisecond))
	receive(t, s.at(time.Second), keepAlive)
	assertPollAt(t, s, socket.PollAtTime(instant(3*time.Second)))
}

// Turning keep-alive off on a socket without it arms none: only turning it
// on winds the timer up, with a keep-alive due at once, at the epoch, as
// setting keep-alive does.
func TestSetKeepAliveOffArmsNoKeepAlive(t *testing.T) {
	t.Parallel()
	for _, interval := range []time.Duration{0, -time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			t.Parallel()
			s := socketEstablished(t)

			s.SetKeepAlive(interval)

			receiveNothing(t, s)
			assertPollAt(t, s, socket.PollOnIngress())
		})
	}
}

// Turning keep-alive off leaves the timer alone: a keep-alive already timed goes out on time, and only then is
// keep-alive off.
func TestTurningKeepAliveOffLetsTheKeepAliveAlreadyTimedGo(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	keepAlive := keepAliveFromLocal(localSeq.Add(1))
	s.SetKeepAlive(time.Second)
	receive(t, s, keepAlive)

	s.SetKeepAlive(0)

	receiveNothing(t, s.at(time.Second-time.Millisecond))
	receive(t, s.at(time.Second), keepAlive)
	assertPollAt(t, s, socket.PollOnIngress())
}

func TestTimeoutPollsAtTheDeadlineWhileTalking(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)
	s.SetTimeout(5 * time.Second)

	assertPollAt(t, s, socket.PollNow())
	receive(t, s, initialSYN())

	// The SYN's retransmission comes first, then the timeout.
	assertPollAt(t, s, socket.PollAtTime(instant(time.Second)))
	receive(t, s.at(time.Second), initialSYN())
	receive(t, s.at(3*time.Second), initialSYN())
	assertPollAt(t, s, socket.PollAtTime(instant(5*time.Second)))
}

// satnet closes a connection with keep-alive off so that a peer that never
// says goodbye is timed out instead: the timeout aborts an idle connection
// too, in FIN-WAIT-2 as in ESTABLISHED, once the peer has been silent for it.
func TestTimeoutAbortsAnIdleConnection(t *testing.T) {
	t.Parallel()
	const timeout = 120 * time.Second
	tests := []struct {
		name   string
		socket func(t *testing.T) *testSocket
		// sent is the sequence number after what the socket has sent: its
		// SYN, and in FIN-WAIT-2 its FIN.
		sent wire.TCPSeqNumber
	}{
		{"ESTABLISHED", socketEstablished, localSeq.Add(1)},
		{"FIN-WAIT-2", socketFinWait2, localSeq.Add(1 + 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)
			s.SetKeepAlive(0)
			s.SetTimeout(timeout)

			// The peer's last word: an ACK of everything, at the epoch.
			send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(test.sent)))

			receiveNothing(t, s.at(timeout-time.Millisecond))
			receive(t, s.at(timeout), fromLocal(withControl(wire.TCPControlRST), withSeq(test.sent), withAck(remoteSeq.Add(1))))
			assertState(t, s, StateClosed)
		})
	}
}

// The tests of TIME-WAIT compute its end from closeDelay, or look a
// minute later: TIME-WAIT lasts 10 s.
func TestTimeWaitLastsTenSeconds(t *testing.T) {
	t.Parallel()
	s := socketFinWait2(t)
	send(t, s.at(time.Second),
		fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+1))))
	receive(t, s.at(time.Second), fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1))))

	receiveNothing(t, s.at(10999*time.Millisecond))
	assertState(t, s, StateTimeWait)
	receiveNothing(t, s.at(11*time.Second))
	assertState(t, s, StateClosed)
}

// =========================================================================
// Acknowledgements and window updates
// =========================================================================

// The tests of delayed ACKs look a millisecond after the delay; the ACK is
// due when the delay is over, not after.
func TestDelayedACKIsDueWhenItExpires(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetAckDelay(defaultACKDelay)

	send(t, s, dataFromRemote(0, "abc"))

	assertPollAt(t, s, socket.PollAtTime(instant(defaultACKDelay)))
	receiveNothing(t, s.at(defaultACKDelay-time.Millisecond))
	receive(t, s.at(defaultACKDelay), ackFromLocal(3, defaultBufferLen-3))
}

func TestACKIsDueAtOnceUnlessDelayed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// receive gives the socket data to acknowledge.
		receive func(t *testing.T, s *testSocket)
	}{
		{"without an ACK delay", func(t *testing.T, s *testSocket) {
			send(t, s, dataFromRemote(0, "abc"))
		}},
		{"with the delay cut short by more than an MSS of data", func(t *testing.T, s *testSocket) {
			s.SetAckDelay(defaultACKDelay)
			s.remoteMSS = 2
			send(t, s, dataFromRemote(0, "a"))
			send(t, s, dataFromRemote(1, "bc"))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketEstablished(t)

			test.receive(t, s)

			assertPollAt(t, s, socket.PollNow())
		})
	}
}

func TestDelayedACKStopsWhenAnACKGoesOut(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetAckDelay(defaultACKDelay)
	send(t, s, dataFromRemote(0, "abc"))
	mustSendSlice(t, s, []byte("xyz"))
	// The data carries the ACK of "abc", which stops the delay.
	receive(t, s, dataFromLocal(0, "xyz", withAck(remoteSeq.Add(1+3)), withWindow(defaultBufferLen-3)))
	const arrival = 5 * time.Millisecond

	send(t, s.at(arrival), dataFromRemote(3, "def"))

	// The ACK of "def" waits a whole delay after its arrival.
	receiveNothing(t, s.at(arrival+defaultACKDelay-time.Millisecond))
	receive(t, s.at(arrival+defaultACKDelay), ackFromLocal(6, defaultBufferLen-6, withSeq(localSeq.Add(1+3))))
}

// More data arriving while an ACK waits does not restart the delay: the ACK
// of both goes when the delay the first started is over. The tests
// check only that enough data cuts the delay short.
func TestMoreDataDoesNotRestartTheACKDelay(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetAckDelay(10 * time.Millisecond)
	send(t, s, dataFromRemote(0, "abc"))

	send(t, s.at(5*time.Millisecond), dataFromRemote(3, "def"))

	receiveNothing(t, s.at(9*time.Millisecond))
	receive(t, s.at(10*time.Millisecond), ackFromLocal(6, defaultBufferLen-6))
}

// Out-of-order data does not advance the acknowledgement number, so no
// acknowledgement is owed and no ACK delay starts; the duplicate ACK that
// RFC 5681 asks for goes at once.
func TestOutOfOrderDataStartsNoACKDelay(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetAckDelay(defaultACKDelay)

	sendExpectingReply(t, s, dataFromRemote(3, "def"), ackFromLocal(0, defaultBufferLen))

	compareField(t, "ackDelayTimer", s.ackDelayTimer, ackDelayTimer{})
}

// Data that fills a gap advances the acknowledgement number, so it starts
// the ACK delay, which the socket decides before the ACK that RFC 5681 asks
// for goes out at once. That ACK leaves the delay running, so
// that the next data is acknowledged when the delay is over: less than a
// whole delay after it arrived.
func TestDataThatFillsAGapStartsAnACKDelayItsImmediateACKLeavesRunning(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetAckDelay(defaultACKDelay)
	sendExpectingReply(t, s, dataFromRemote(3, "def"), ackFromLocal(0, defaultBufferLen))
	sendExpectingReply(t, s, dataFromRemote(0, "abc"), ackFromLocal(6, defaultBufferLen-6))
	const arrival = 5 * time.Millisecond

	send(t, s.at(arrival), dataFromRemote(6, "g"))

	receiveNothing(t, s.at(defaultACKDelay-time.Millisecond))
	receive(t, s.at(defaultACKDelay), ackFromLocal(7, defaultBufferLen-7))
}

// PSH is set on the last segment of the buffered data only if it
// carries data.
func TestBareACKCarriesNoPSH(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	send(t, s, dataFromRemote(0, "abc"))

	receiveExact(t, s, ackFromLocal(3, defaultBufferLen-3))
}

// A peer whose SYN offers no window scale reads every window field as it
// is (RFC 7323), so the socket announces a receive buffer larger than the
// field holds as the field's largest value, rather than the buffer's size
// cut to 16 bits, which would announce a window closed or nearly so. That
// holds for the ACKs Dispatch sends and for those that answer a segment at
// once. The SYN-SENT test without window scaling stops at the
// peer's SYN|ACK, before the socket announces any window.
func TestALargeWindowSaturatesTheFieldForAPeerWithoutWindowScaling(t *testing.T) {
	t.Parallel()
	// largeBufferLen needs a window scale of 5, which the peer does not take.
	const offeredShift = 5
	tests := []struct {
		name string
		// handshake brings a socket with a receive buffer of largeBufferLen
		// octets to ESTABLISHED, with a peer whose SYN has no window scale.
		handshake func(t *testing.T, s *testSocket)
	}{
		{"active open", func(t *testing.T, s *testSocket) {
			if err := s.Connect(s.cx, remoteEnd, listenEnd); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			receive(t, s, initialSYN(withWindowScale(offeredShift), withWindow(unscaledWindow(largeBufferLen))))
			send(t, s, remoteSYN(withAck(localSeq.Add(1))))
			receive(t, s, ackFromLocal(0, math.MaxUint16))
		}},
		{"passive open", func(t *testing.T, s *testSocket) {
			if err := s.Listen(listenEnd); err != nil {
				t.Fatalf("Listen: %v", err)
			}
			send(t, s, remoteSYN())
			receive(t, s, synACK(withWindow(unscaledWindow(largeBufferLen))))
			send(t, s, ackFromRemote(0))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketClosedWithBufferSizes(t, defaultBufferLen, largeBufferLen)
			test.handshake(t, s)
			assertState(t, s, StateEstablished)

			// 1 MiB less 3 octets free, cut to 16 bits, would be 65533.
			send(t, s, dataFromRemote(0, "abc"))
			receive(t, s, ackFromLocal(3, math.MaxUint16))
			sendExpectingReply(t, s, dataFromRemote(4, "e"), ackFromLocal(3, math.MaxUint16))
		})
	}
}

func TestWindowUpdateIsDueAtOnce(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	replaceReceiveBuffer(s, smallBufferLen)
	send(t, s, dataFromRemote(0, "abcdef"))
	receiveOnly(t, s, ackFromLocal(6, 0))
	assertPollAt(t, s, socket.PollOnIngress())

	mustReceive(t, s, consumeAll)

	assertPollAt(t, s, socket.PollNow())
}

// With a receive buffer that needs a window scale, the socket compares the
// window it would announce with the one it last announced in the field's
// scaled units: a window that has doubled is announced at once, as without
// scaling. The tests of window updates use buffers that need no scale.
func TestScaledWindowGrowthIsAnnouncedAtOnce(t *testing.T) {
	t.Parallel()
	// A buffer of 128 KiB needs a window scale of 2: the window field counts
	// units of 4 octets.
	const bufferLen, shift = 128 << 10, 2
	// free is the room the peer's data leaves in the buffer.
	const free = 4096
	s := socketEstablishedWithBufferSizes(t, defaultBufferLen, bufferLen)
	fillInOrder(t, s, bufferLen-free)
	receive(t, s, ackFromLocal(bufferLen-free, free>>shift))

	mustReceiveSlice(t, s, make([]byte, free))

	// The window field doubles, from 1024 to 2048.
	receive(t, s, ackFromLocal(bufferLen-free, (2*free)>>shift))
}

// The receive half stays open in FIN-WAIT-1 and FIN-WAIT-2, so a window that
// has doubled since it was announced is announced at once there, as in
// ESTABLISHED: the peer may still have data to send.
// TestWindowGrowthIsNotAnnouncedAfterThePeersFIN checks the states where the
// peer has finished.
func TestWindowGrowthIsAnnouncedWhileThePeerMaySend(t *testing.T) {
	t.Parallel()
	// received is what the peer sends: all but 4 octets of the receive
	// buffer.
	const received = defaultBufferLen - 4
	tests := []struct {
		name   string
		socket func(t *testing.T) *testSocket
		// acknowledged is what the peer's data acknowledges: in FIN-WAIT-1,
		// not the socket's FIN.
		acknowledged wire.TCPSeqNumber
	}{
		{"FIN-WAIT-1", socketFinWait1WithItsFINSent, localSeq.Add(1)},
		{"FIN-WAIT-2", socketFinWait2, localSeq.Add(1 + 1)},
	}
	// The socket has sent its SYN and its FIN.
	sent := localSeq.Add(1 + 1)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)
			send(t, s, dataFromRemote(0, string(make([]byte, received)), withAck(test.acknowledged)))
			receive(t, s, ackFromLocal(received, defaultBufferLen-received, withSeq(sent)))

			mustReceiveSlice(t, s, make([]byte, received))

			receive(t, s, ackFromLocal(received, defaultBufferLen, withSeq(sent)))
		})
	}
}

// socketFinWait1WithItsFINSent returns a socket in FIN-WAIT-1 whose FIN has
// gone out, not yet acknowledged.
func socketFinWait1WithItsFINSent(t *testing.T) *testSocket {
	t.Helper()
	s := socketFinWait1(t)
	receive(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
	return s
}

// TestWindowGrowthIsNotAnnouncedAfterThePeersFIN checks that, once the peer
// has sent its FIN, the socket announces no window update: the peer sends no
// more data. The window tests in window_test.go (TestCloseWaitNoWindowUpdate
// and TestTimeWaitNoWindowUpdate) grow the window from 60 to 64 octets, which
// has not doubled, so they pass whatever the state. Here the window grows from
// 4 to 64 octets.
func TestWindowGrowthIsNotAnnouncedAfterThePeersFIN(t *testing.T) {
	t.Parallel()
	// received is what the peer sends before its FIN: all but 4 octets of
	// the receive buffer.
	const received = defaultBufferLen - 4
	tests := []struct {
		name   string
		socket func(t *testing.T) *testSocket
		// sent is the sequence number after what the socket has sent: its
		// SYN, and in FIN-WAIT-2 its FIN.
		sent  wire.TCPSeqNumber
		state State
	}{
		{"CLOSE-WAIT", socketEstablished, localSeq.Add(1), StateCloseWait},
		{"TIME-WAIT", socketFinWait2, localSeq.Add(1 + 1), StateTimeWait},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)
			send(t, s, dataFromRemote(0, string(make([]byte, received)),
				withControl(wire.TCPControlFIN), withAck(test.sent)))
			assertState(t, s, test.state)
			receive(t, s, ackFromLocal(received+1, defaultBufferLen-received, withSeq(test.sent)))

			mustReceiveSlice(t, s, make([]byte, received))

			if last, _ := s.lastScaledWindow(); s.scaledWindow()/significantWindowGrowth < last {
				t.Fatalf("the window grew from %d to %d octets, too little to be announced in any state",
					last, s.scaledWindow())
			}
			receiveNothing(t, s)
		})
	}
}
