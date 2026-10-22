package tcp

// Tests of the SYN-SENT state.

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestConnectValidation(t *testing.T) {
	t.Parallel()
	unspecified := netip.IPv4Unspecified()
	invalid := []struct {
		name   string
		remote netip.AddrPort
		local  wire.ListenEndpoint
	}{
		{"unspecified local address and port 0", remoteEnd, wire.ListenEndpoint{Addr: unspecified, Port: 0}},
		{"unspecified local address", remoteEnd, wire.ListenEndpoint{Addr: unspecified, Port: 1024}},
		{"unspecified remote address and port 0", netip.AddrPortFrom(unspecified, 0), localListenEnd},
	}
	s := socketClosed(t)
	for _, test := range invalid {
		if err := s.Connect(s.cx, test.remote, test.local); !errors.Is(err, ErrUnaddressable) {
			t.Errorf("%s: Connect(%v, %v) = %v, want %v", test.name, test.remote, test.local, err, ErrUnaddressable)
		}
	}

	if err := s.Connect(s.cx, remoteEnd, localListenEnd); err != nil {
		t.Fatalf("Connect failed with valid parameters: %v", err)
	}
	assertTuple(t, s, testTuple)
}

func TestConnect(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	if err := s.Connect(s.cx, remoteEnd, wire.ListenEndpoint{Port: localPort}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	assertTuple(t, s, testTuple)
	receiveOnly(t, s, initialSYN())

	send(t, s, remoteSYN(withAck(localSeq.Add(1)), withMSS(peerMSS), withWindowScale(0)))

	assertTuple(t, s, testTuple)
}

func TestConnectSYNACKTinyMSSIsClamped(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	if err := s.Connect(s.cx, remoteEnd, wire.ListenEndpoint{Port: localPort}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	receiveOnly(t, s, initialSYN())

	send(t, s, remoteSYN(withAck(localSeq.Add(1)), withMSS(tinyMSS), withWindowScale(0)))

	assertState(t, s, StateEstablished)
	if s.remoteMSS != minRemoteMSS {
		t.Errorf("remoteMSS = %d, want %d", s.remoteMSS, minRemoteMSS)
	}
}

func TestConnectSYNACKZeroMSSIsIgnored(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	if err := s.Connect(s.cx, remoteEnd, wire.ListenEndpoint{Port: localPort}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	receiveOnly(t, s, initialSYN())

	send(t, s, remoteSYN(withAck(localSeq.Add(1)), withMSS(0), withWindowScale(0)))

	assertState(t, s, StateEstablished)
	if s.remoteMSS != defaultMSS {
		t.Errorf("remoteMSS = %d, want %d", s.remoteMSS, defaultMSS)
	}
}

func TestConnectUnspecifiedLocal(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)

	err := s.Connect(s.cx, remoteEnd, wire.ListenEndpoint{Port: localPort})

	if err != nil {
		t.Errorf("Connect without a local address = %v, want nil", err)
	}
}

func TestConnectSpecifiedLocal(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)

	err := s.Connect(s.cx, remoteEnd, wire.ListenEndpoint{Addr: remoteAddr, Port: localPort})

	if err != nil {
		t.Errorf("Connect from %v = %v, want nil", remoteAddr, err)
	}
}

func TestConnectTwice(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	if err := s.Connect(s.cx, remoteEnd, wire.ListenEndpoint{Port: localPort}); err != nil {
		t.Fatalf("first Connect: %v", err)
	}

	err := s.Connect(s.cx, remoteEnd, wire.ListenEndpoint{Port: localPort})

	if !errors.Is(err, ErrInvalidState) {
		t.Errorf("second Connect = %v, want %v", err, ErrInvalidState)
	}
}

func TestSynSentSanity(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)

	if err := s.Connect(s.cx, remoteEnd, localListenEnd); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	sanity(t, s, socketSynSent(t))
}

func TestSynSentSYNACK(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)
	receiveOnly(t, s, initialSYN())

	send(t, s, remoteSYN(withAck(localSeq.Add(1)), withMSS(peerMSS), withWindowScale(0)))

	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
	receiveNothing(t, s.at(1000*time.Millisecond))
	assertState(t, s, StateEstablished)
	sanity(t, s, socketEstablished(t))
}

func TestSynSentSynReceivedACK(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)
	receiveOnly(t, s, initialSYN())

	// A SYN packet changes the SYN-SENT state to SYN-RECEIVED.
	send(t, s, remoteSYN(withMSS(peerMSS), withWindowScale(0)))
	assertState(t, s, StateSynReceived)

	// The socket will then send a SYN|ACK packet.
	receiveOnly(t, s, synACK(withWindowScale(0)))
	receiveNothing(t, s)

	// The socket may retransmit the SYN|ACK packet.
	receive(t, s.at(1001*time.Millisecond), synACK(withWindowScale(0)))

	// An ACK packet changes the SYN-RECEIVED state to ESTABLISHED.
	send(t, s, fromRemote(withControl(wire.TCPControlNone), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))
	assertState(t, s, StateEstablished)
	sanity(t, s, socketEstablished(t))
}

func TestSynSentSYNACKNotIncremented(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)
	receiveOnly(t, s, initialSYN())

	sendExpectingReply(t, s,
		remoteSYN(withAck(localSeq), withMSS(peerMSS), withWindowScale(0)), // WRONG
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq), withoutAck(), withWindow(0)))

	assertState(t, s, StateSynSent)
}

func TestSynSentSynReceivedRST(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)
	receiveOnly(t, s, initialSYN())

	// A SYN packet changes the SYN-SENT state to SYN-RECEIVED.
	send(t, s, remoteSYN(withMSS(peerMSS), withWindowScale(0)))
	assertState(t, s, StateSynReceived)

	// A RST packet changes the SYN-RECEIVED state to CLOSED.
	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1)), withAck(localSeq)))
	assertState(t, s, StateClosed)
}

func TestSynSentRST(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)

	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq), withAck(localSeq.Add(1))))

	assertState(t, s, StateClosed)
}

func TestSynSentRSTNoACK(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)

	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq), withoutAck()))

	assertState(t, s, StateSynSent)
}

func TestSynSentRSTBadACK(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)

	send(t, s, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq), withAck(1234)))

	assertState(t, s, StateSynSent)
}

func TestSynSentBadACK(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)
	receiveOnly(t, s, initialSYN())

	send(t, s, fromRemote(
		withControl(wire.TCPControlNone), // Unexpected
		withSeq(remoteSeq),
		withAck(localSeq.Add(1)), // Correct
	))

	// It should trigger no response and change no state
	receiveNothing(t, s)
	assertState(t, s, StateSynSent)
}

func TestSynSentBadACKSeq1(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)
	receiveOnly(t, s, initialSYN())

	sendExpectingReply(t, s,
		fromRemote(withControl(wire.TCPControlNone), withSeq(remoteSeq), withAck(localSeq)), // WRONG
		// The RST's sequence number matches the ack_number of the unexpected
		// ACK.
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq), withoutAck(), withWindow(0)))

	// It should trigger a RST, and change no state
	assertState(t, s, StateSynSent)
}

func TestSynSentBadACKSeq2(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)
	receiveOnly(t, s, initialSYN())

	sendExpectingReply(t, s,
		fromRemote(withControl(wire.TCPControlNone), withSeq(remoteSeq), withAck(localSeq.Add(123456))), // WRONG
		// The RST's sequence number matches the ack_number of the unexpected
		// ACK.
		fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(123456)), withoutAck(), withWindow(0)))

	// It should trigger a RST, and change no state
	assertState(t, s, StateSynSent)
}

func TestSynSentClose(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)

	s.Close()

	assertState(t, s, StateClosed)
}

func TestSynSentSACKOption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		synACK   wire.TCPRepr
		wantSACK bool
	}{
		{"with SACK", remoteSYN(withAck(localSeq.Add(1)), withMSS(peerMSS), withWindowScale(0), withSACKPermitted()), true},
		{"without SACK", remoteSYN(withAck(localSeq.Add(1)), withMSS(peerMSS), withWindowScale(0)), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketSynSent(t)
			receiveOnly(t, s, initialSYN())

			send(t, s, test.synACK)

			if s.remoteHasSACK != test.wantSACK {
				t.Errorf("remoteHasSACK = %v, want %v", s.remoteHasSACK, test.wantSACK)
			}
		})
	}
}

func TestSynSentWinScaleBuffers(t *testing.T) {
	t.Parallel()
	for _, test := range receiveBufferWindowShifts {
		t.Run(fmt.Sprint(test.bufferLen), func(t *testing.T) {
			t.Parallel()
			s := socketClosedWithBufferSizes(t, defaultBufferLen, test.bufferLen)
			if s.remoteWindowShift != test.shift {
				t.Fatalf("remoteWindowShift = %d, want %d", s.remoteWindowShift, test.shift)
			}

			if err := s.Connect(s.cx, remoteEnd, localListenEnd); err != nil {
				t.Fatalf("Connect: %v", err)
			}

			receiveOnly(t, s, initialSYN(withWindowScale(test.shift), withWindow(unscaledWindow(test.bufferLen))))
		})
	}
}

func TestSynSentSYNACKNoWindowScaling(t *testing.T) {
	t.Parallel()
	s := socketSynSentWithBufferSizes(t, largeBufferLen, largeBufferLen)
	// scaling does NOT apply to the window value in SYN packets
	receiveOnly(t, s, initialSYN(withWindow(math.MaxUint16), withWindowScale(5)))
	if s.remoteWindowShift != 5 {
		t.Fatalf("remoteWindowShift = %d, want 5", s.remoteWindowShift)
	}

	send(t, s, remoteSYN(withAck(localSeq.Add(1)), withMSS(peerMSS), withWindow(42)))

	assertState(t, s, StateEstablished)
	if s.remoteWindowShift != 0 {
		t.Errorf("remoteWindowShift = %d, want 0", s.remoteWindowShift)
	}
	if scale, ok := s.remoteWindowScale.Get(); ok {
		t.Errorf("remoteWindowScale = %d, want none", scale)
	}
	if s.remoteWindowLen != 42 {
		t.Errorf("remoteWindowLen = %d, want 42", s.remoteWindowLen)
	}
}

func TestSynSentSYNACKWindowScaling(t *testing.T) {
	t.Parallel()
	s := socketSynSent(t)
	receiveOnly(t, s, initialSYN())

	send(t, s, remoteSYN(withAck(localSeq.Add(1)), withMSS(peerMSS), withWindowScale(7), withWindow(42)))

	assertState(t, s, StateEstablished)
	if scale, ok := s.remoteWindowScale.Get(); !ok || scale != 7 {
		t.Errorf("remoteWindowScale = %d, %v; want 7", scale, ok)
	}
	// scaling does NOT apply to the window value in SYN packets
	if s.remoteWindowLen != 42 {
		t.Errorf("remoteWindowLen = %d, want 42", s.remoteWindowLen)
	}
}
