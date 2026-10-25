package tcp

// Tests of Listen and Connect, of connections whose initial sequence number
// lies anywhere in sequence space, and of what starts a connection afresh.

import (
	"errors"
	"math"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// =========================================================================
// Listen and Connect
// =========================================================================

func TestListenRefusesAddressesThatAreNotIPv4(t *testing.T) {
	t.Parallel()
	for _, addr := range []netip.Addr{netip.IPv6Loopback(), netip.AddrFrom16(localAddr.As16())} {
		s := socketClosed(t)

		err := s.Listen(wire.ListenEndpoint{Addr: addr, Port: localPort})

		if !errors.Is(err, ErrUnaddressable) || s.state != StateClosed {
			t.Errorf("Listen on %v = %v in %v, want %v in CLOSED", addr, err, s.state, ErrUnaddressable)
		}
	}
}

func TestConnectRefusesAddressesThatAreNotIPv4(t *testing.T) {
	t.Parallel()
	mappedRemote := netip.AddrFrom16(remoteAddr.As16())
	tests := []struct {
		name   string
		remote netip.AddrPort
		local  wire.ListenEndpoint
	}{
		{"IPv6 remote", netip.AddrPortFrom(netip.IPv6Loopback(), remotePort), listenEnd},
		{"IPv4-mapped remote", netip.AddrPortFrom(mappedRemote, remotePort), listenEnd},
		{"zero remote", netip.AddrPort{}, listenEnd},
		{"IPv6 local", remoteEnd, wire.ListenEndpoint{Addr: netip.IPv6Loopback(), Port: localPort}},
		{"IPv4-mapped local", remoteEnd, wire.ListenEndpoint{Addr: netip.AddrFrom16(localAddr.As16()), Port: localPort}},
	}
	for _, test := range tests {
		s := socketClosed(t)

		err := s.Connect(s.cx, test.remote, test.local)

		if !errors.Is(err, ErrUnaddressable) || s.state != StateClosed {
			t.Errorf("%s: Connect = %v in %v, want %v in CLOSED", test.name, err, s.state, ErrUnaddressable)
		}
	}
}

func TestConnectNeedsAnAddressToSendFrom(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	if err := s.cx.SetIPAddrs(nil); err != nil {
		t.Fatalf("SetIPAddrs: %v", err)
	}

	err := s.Connect(s.cx, remoteEnd, listenEnd)

	if !errors.Is(err, ErrUnaddressable) {
		t.Errorf("Connect without interface addresses = %v, want %v", err, ErrUnaddressable)
	}
}

// The unspecified remote address is tried only together with port 0, which
// alone makes Connect fail.
func TestConnectRefusesTheUnspecifiedRemoteAddress(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)

	err := s.Connect(s.cx, netip.AddrPortFrom(netip.IPv4Unspecified(), remotePort), listenEnd)

	if !errors.Is(err, ErrUnaddressable) || s.state != StateClosed {
		t.Errorf("Connect to 0.0.0.0 = %v in %v, want %v in CLOSED", err, s.state, ErrUnaddressable)
	}
}

func TestInitialSequenceNumbersComeFromTheContextsRandomSource(t *testing.T) {
	t.Parallel()
	// With random seed 0x5EED, the context's first Uint32 is 0xaf7268a4 (the
	// golden values of socket.Rand's tests): its top bits show that the
	// initial sequence number is all of it.
	const randomSeed = 0x5EED
	// Typed, so that t.Errorf does not take it for an int, which has 32
	// bits on linux/386 and linux/arm.
	const firstRandomUint32 uint32 = 0xaf7268a4
	connecting := socketDrawingISNsFrom(t, randomSeed)
	listening := socketDrawingISNsFrom(t, randomSeed)
	if err := connecting.Connect(connecting.cx, remoteEnd, listenEnd); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := listening.Listen(listenEnd); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	send(t, listening, remoteSYN())

	if uint32(connecting.localSeqNumber) != firstRandomUint32 || uint32(listening.localSeqNumber) != firstRandomUint32 {
		t.Errorf("initial sequence numbers %#x and %#x, want %#x",
			uint32(connecting.localSeqNumber), uint32(listening.localSeqNumber), firstRandomUint32)
	}
}

// socketDrawingISNsFrom returns a closed socket that draws its initial
// sequence numbers, as NewSocket has it, from a test context with
// randomSeed.
func socketDrawingISNsFrom(t *testing.T, randomSeed uint64) *testSocket {
	t.Helper()
	return &testSocket{
		Socket: NewSocket(NewSocketBuffer(defaultBufferLen), NewSocketBuffer(defaultBufferLen)),
		cx:     newTestContextWith(t, randomSeed, testIPMTU),
	}
}

// Port 0 is tried only together with an unspecified address, which alone
// makes Connect fail.
func TestConnectRefusesPortZeroOnEitherSide(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		remote netip.AddrPort
		local  wire.ListenEndpoint
	}{
		{"local port 0", remoteEnd, wire.ListenEndpoint{Port: 0}},
		{"local port 0 on an address", remoteEnd, wire.ListenEndpoint{Addr: localAddr, Port: 0}},
		{"remote port 0", netip.AddrPortFrom(remoteAddr, 0), listenEnd},
	}
	for _, test := range tests {
		s := socketClosed(t)

		err := s.Connect(s.cx, test.remote, test.local)

		if !errors.Is(err, ErrUnaddressable) || s.state != StateClosed {
			t.Errorf("%s: Connect = %v in %v, want %v in CLOSED", test.name, err, s.state, ErrUnaddressable)
		}
	}
}

// Listening again is allowed only on the same endpoint; on
// any other, Listen fails and the socket goes on listening where it did.
func TestListenOnAnotherEndpointWhileListeningIsRefused(t *testing.T) {
	t.Parallel()
	for _, other := range []wire.ListenEndpoint{{Port: localPort + 1}, localListenEnd} {
		s := socketClosed(t)
		if err := s.Listen(listenEnd); err != nil {
			t.Fatalf("Listen(%v): %v", listenEnd, err)
		}

		err := s.Listen(other)

		if !errors.Is(err, ErrInvalidState) || s.listenEndpoint != listenEnd || !s.IsListening() {
			t.Errorf("Listen(%v) while listening on %v = %v, now listening %v on %v; want %v, still on %v",
				other, listenEnd, err, s.IsListening(), s.listenEndpoint, ErrInvalidState, listenEnd)
		}
	}
}

// =========================================================================
// Initial sequence numbers anywhere in sequence space
// =========================================================================

// isnsAcrossSequenceSpace are initial sequence numbers in both halves of
// sequence space, around its middle and its wrap. The tests fix the
// ISN at 10000 (localSeq), but a socket draws it at random (randomISN). An
// absent wire.Optional holds sequence number 0, which comes before an ISN in
// the lower half and after one in the upper half, so code that reads it
// without checking goes wrong in one half only.
var isnsAcrossSequenceSpace = []wire.TCPSeqNumber{localSeq, math.MaxInt32, math.MinInt32, -0x70000000, -2, -1}

// socketConnectedWithISN returns a socket that connected with the initial
// sequence number isn, and sent its SYN at the epoch.
func socketConnectedWithISN(t *testing.T, isn wire.TCPSeqNumber) *testSocket {
	t.Helper()
	s := socketClosed(t)
	s.chooseISN = func(*socket.Context) wire.TCPSeqNumber { return isn }
	if err := s.Connect(s.cx, remoteEnd, listenEnd); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	receive(t, s, initialSYN(withSeq(isn)))
	return s
}

// The peer's SYN in a simultaneous open carries no ACK, so it acknowledges
// nothing, whatever the ISN: the socket's SYN stays unacknowledged, and its
// retransmission stays due an RTO after it went out.
func TestAPeersSYNWithoutAnACKAcknowledgesNothingWhateverTheISN(t *testing.T) {
	t.Parallel()
	for _, isn := range isnsAcrossSequenceSpace {
		t.Run(isn.String(), func(t *testing.T) {
			t.Parallel()
			s := socketConnectedWithISN(t, isn)

			send(t, s, remoteSYN())

			assertState(t, s, StateSynReceived)
			compareField(t, "localSeqNumber", s.localSeqNumber, isn)
			assertTimer(t, s.timer, timer{kind: timerRetransmit, expiresAt: instant(time.Second)})
		})
	}
}

// The socket times its SYN as it times any segment, whatever the ISN: the
// SYN|ACK 900 ms after it gives the first measurement, SRTT 900 ms and
// RTTVAR 450 ms, for an RTO of 900 + 4 × 450 = 2700 ms.
func TestTheSYNIsTimedWhateverTheISN(t *testing.T) {
	t.Parallel()
	for _, isn := range isnsAcrossSequenceSpace {
		t.Run(isn.String(), func(t *testing.T) {
			t.Parallel()
			s := socketConnectedWithISN(t, isn)

			send(t, s.at(900*time.Millisecond), remoteSYN(withAck(isn.Add(1))))

			if rto := s.rttEstimator.retransmissionTimeout(); rto != 2700*time.Millisecond {
				t.Errorf("RTO = %v after a SYN|ACK 900 ms after the SYN, want 2.7s", rto)
			}
		})
	}
}

// =========================================================================
// What starts a connection afresh
// =========================================================================

// satnet relies on the reset that the loss of the socket's local address
// causes to empty the send buffer, as if the peer had acknowledged everything.
// Checking the state alone would not show that.
func TestLosingTheLocalAddressEmptiesBothBuffers(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc"))
	receive(t, s, dataFromLocal(0, "abc"))
	send(t, s, dataFromRemote(0, "xyz"))
	newAddrs := []netip.Prefix{netip.PrefixFrom(otherAddr, 24)}
	if err := s.cx.SetIPAddrs(newAddrs); err != nil {
		t.Fatalf("SetIPAddrs(%v): %v", newAddrs, err)
	}

	receiveNothing(t, s)

	assertState(t, s, StateClosed)
	compareField(t, "SendQueue", s.SendQueue(), 0)
	compareField(t, "receiveBuffer.Len()", s.receiveBuffer.Len(), 0)
}

// socketAbortedWithDataLeft returns a socket aborted with data in both of its
// buffers and a run of data beyond a hole in its assembler, whose RST has
// gone: CLOSED, ready to be used again.
func socketAbortedWithDataLeft(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	mustSendSlice(t, s, []byte("abc"))
	send(t, s, dataFromRemote(0, "xyz"))
	// "XY" leaves a hole of 3 octets after "xyz", which the ACK it gets at
	// once still acknowledges.
	sendExpectingReply(t, s, dataFromRemote(3+3, "XY"), ackFromLocal(3, defaultBufferLen-3))
	s.Abort()
	// The RST goes, and the socket forgets its peer.
	dispatchCollecting(t, s)
	return s
}

// A CLOSED socket may be used again: Listen and Connect start the next
// connection with empty buffers
// and an empty assembler, whatever the last one left in them.
func TestAReusedSocketStartsWithNothingLeftOver(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		reuse func(s *testSocket) error
	}{
		{"Listen", func(s *testSocket) error { return s.Listen(listenEnd) }},
		{"Connect", func(s *testSocket) error { return s.Connect(s.cx, remoteEnd, listenEnd) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketAbortedWithDataLeft(t)

			if err := test.reuse(s); err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}

			compareField(t, "SendQueue", s.SendQueue(), 0)
			compareField(t, "receiveBuffer.Len()", s.receiveBuffer.Len(), 0)
			if !s.assembler.IsEmpty() {
				t.Errorf("the assembler holds %v, want it empty", &s.assembler)
			}
		})
	}
}

// The data of the next connection that ends just where the last one's hole
// began takes nothing of what the assembler held.
func TestAReusedSocketReceivesOnlyTheNewConnectionsData(t *testing.T) {
	t.Parallel()
	s := socketAbortedWithDataLeft(t)
	if err := s.Listen(listenEnd); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	send(t, s, remoteSYN())
	receive(t, s, synACK())
	send(t, s, ackFromRemote(0))

	send(t, s, dataFromRemote(0, "new"))

	receiveExactly(t, s, "new")
}
