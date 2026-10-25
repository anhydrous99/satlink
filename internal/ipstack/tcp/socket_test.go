package tcp

// Tests of the socket itself: its construction and settings, its states and
// their predicates, and its endpoints. They also check the behaviour that the
// consumers rely on, as "What the consumers rely on" in docs/IPSTACK.md
// describes.

import (
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// =========================================================================
// Construction and settings
// =========================================================================

func TestNewSocketStartsClosedWithoutEndpoints(t *testing.T) {
	t.Parallel()

	s := NewSocket(NewSocketBuffer(10), NewSocketBuffer(20))

	if s.State() != StateClosed {
		t.Errorf("State = %v, want %v", s.State(), StateClosed)
	}
	if s.LocalEndpoint().IsValid() || s.RemoteEndpoint().IsValid() || s.listenEndpoint != (wire.ListenEndpoint{}) {
		t.Errorf("endpoints %v, %v, %v; want zero", s.LocalEndpoint(), s.RemoteEndpoint(), s.listenEndpoint)
	}
	if s.receiveBuffer.Capacity() != 10 || s.SendCapacity() != 20 {
		t.Errorf("capacities %d and %d, want 10 and 20", s.receiveBuffer.Capacity(), s.SendCapacity())
	}
}

func TestNewSocketHasTheDefaultSettings(t *testing.T) {
	t.Parallel()

	s := NewSocket(NewSocketBuffer(10), NewSocketBuffer(20))

	compareField(t, "nagleEnabled", s.nagleEnabled, true)
	compareField(t, "ackDelay", s.ackDelay, 10*time.Millisecond)
	compareField(t, "timeout", s.timeout, 0)
	compareField(t, "keepAlive", s.keepAlive, 0)
}

func TestDurationOptionsTakeZeroAndNegativeAsNone(t *testing.T) {
	t.Parallel()
	options := []struct {
		name string
		set  func(s *Socket, d time.Duration)
		get  func(s *Socket) time.Duration
	}{
		{"timeout", (*Socket).SetTimeout, func(s *Socket) time.Duration { return s.timeout }},
		{"ACK delay", (*Socket).SetAckDelay, func(s *Socket) time.Duration { return s.ackDelay }},
		{"keep-alive", (*Socket).SetKeepAlive, func(s *Socket) time.Duration { return s.keepAlive }},
	}
	settings := []struct {
		set, want time.Duration
	}{
		{time.Second, time.Second},
		{0, 0},
		{-time.Second, 0},
	}
	for _, option := range options {
		for _, setting := range settings {
			s := NewSocket(NewSocketBuffer(defaultBufferLen), NewSocketBuffer(defaultBufferLen))

			option.set(s, setting.set)

			if got := option.get(s); got != setting.want {
				t.Errorf("%s set to %v reads %v, want %v", option.name, setting.set, got, setting.want)
			}
		}
	}
}

func TestSetNagleEnabledTurnsNagleOff(t *testing.T) {
	t.Parallel()
	s := NewSocket(NewSocketBuffer(defaultBufferLen), NewSocketBuffer(defaultBufferLen))

	s.SetNagleEnabled(false)

	if s.nagleEnabled {
		t.Error("Nagle's algorithm is still on")
	}
}

// RFC 7323 allows a window scale of at most 14, enough for any receive buffer
// smaller than 1 GiB. NewSocket takes one of exactly 1 GiB too, and offers it
// a scale of 15, which peers read as 14.
func TestWindowScaleOfTheLargestReceiveBuffers(t *testing.T) {
	t.Parallel()
	for capacity, want := range map[int]uint8{maxReceiveBufferLen - 1: 14, maxReceiveBufferLen: 15} {
		if got := windowShiftFor(capacity); got != want {
			t.Errorf("windowShiftFor(%d) = %d, want %d", capacity, got, want)
		}
	}
}

func TestStateStringNamesEveryState(t *testing.T) {
	t.Parallel()
	names := map[State]string{
		StateClosed:      "CLOSED",
		StateListen:      "LISTEN",
		StateSynSent:     "SYN-SENT",
		StateSynReceived: "SYN-RECEIVED",
		StateEstablished: "ESTABLISHED",
		StateFinWait1:    "FIN-WAIT-1",
		StateFinWait2:    "FIN-WAIT-2",
		StateCloseWait:   "CLOSE-WAIT",
		StateClosing:     "CLOSING",
		StateLastAck:     "LAST-ACK",
		StateTimeWait:    "TIME-WAIT",
		State(11):        "State(11)",
		State(-1):        "State(-1)",
	}
	for state, want := range names {
		if got := state.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", int(state), got, want)
		}
	}
}

// =========================================================================
// The state predicates
// =========================================================================

// socketsInEveryState returns a test socket in each state of the state
// machine.
func socketsInEveryState(t *testing.T) map[State]*testSocket {
	t.Helper()
	return map[State]*testSocket{
		StateClosed:      socketClosed(t),
		StateListen:      socketListen(t),
		StateSynSent:     socketSynSent(t),
		StateSynReceived: socketSynReceived(t),
		StateEstablished: socketEstablished(t),
		StateFinWait1:    socketFinWait1(t),
		StateFinWait2:    socketFinWait2(t),
		StateCloseWait:   socketCloseWait(t),
		StateClosing:     socketClosing(t),
		StateLastAck:     socketLastAck(t),
		StateTimeWait:    socketTimeWaitFromFinWait2(t),
	}
}

func TestPredicatesFollowTheState(t *testing.T) {
	t.Parallel()
	type predicates struct{ listening, open, active, maySend, mayReceive bool }
	want := map[State]predicates{
		StateClosed:      {},
		StateListen:      {listening: true, open: true},
		StateSynSent:     {open: true, active: true},
		StateSynReceived: {open: true, active: true},
		StateEstablished: {open: true, active: true, maySend: true, mayReceive: true},
		StateFinWait1:    {open: true, active: true, mayReceive: true},
		StateFinWait2:    {open: true, active: true, mayReceive: true},
		StateCloseWait:   {open: true, active: true, maySend: true},
		StateClosing:     {open: true, active: true},
		StateLastAck:     {open: true, active: true},
		StateTimeWait:    {},
	}
	for state, s := range socketsInEveryState(t) {
		got := predicates{s.IsListening(), s.IsOpen(), s.IsActive(), s.MaySend(), s.MayReceive()}

		if got != want[state] {
			t.Errorf("%v: predicates %+v, want %+v", state, got, want[state])
		}
		if s.CanSend() != want[state].maySend || s.CanReceive() {
			t.Errorf("%v: CanSend %v, CanReceive %v with empty buffers; want %v, false",
				state, s.CanSend(), s.CanReceive(), want[state].maySend)
		}
	}
}

func TestBufferedDataCanBeReceivedInAnyState(t *testing.T) {
	t.Parallel()
	for state, s := range socketsInEveryState(t) {
		s.receiveBuffer.EnqueueSlice([]byte("abc"))

		if !s.CanReceive() || !s.MayReceive() {
			t.Errorf("%v: CanReceive %v, MayReceive %v with data buffered; want true", state, s.CanReceive(), s.MayReceive())
		}
	}
}

// =========================================================================
// Endpoints
// =========================================================================

// A socket without a connection has no endpoints, and sends nothing, not
// even a RST. The LAST-ACK test checks only the state that the last ACK leaves.
func TestEndpointsAreZeroWithoutAConnection(t *testing.T) {
	t.Parallel()
	closed := socketClosed(t)
	listening := socketClosed(t)
	if err := listening.Listen(listenEnd); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	reset := socketEstablished(t)
	send(t, reset, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1))))
	finished := socketLastAck(t)
	receive(t, finished, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+1))))
	send(t, finished, fromRemote(withSeq(remoteSeq.Add(1+1)), withAck(localSeq.Add(1+1))))

	sockets := map[string]*testSocket{"closed": closed, "listening": listening, "reset": reset, "finished in LAST-ACK": finished}
	for name, s := range sockets {
		if s.LocalEndpoint().IsValid() || s.RemoteEndpoint().IsValid() {
			t.Errorf("%s socket: endpoints %v and %v, want zero", name, s.LocalEndpoint(), s.RemoteEndpoint())
		}
		receiveNothing(t, s)
	}
	if listening.listenEndpoint != listenEnd {
		t.Errorf("listenEndpoint = %v, want %v", listening.listenEndpoint, listenEnd)
	}
}

// Close keeps the endpoint a socket listened on, but whatever resets the
// socket forgets it: Connect, the end of TIME-WAIT and the loss of the local
// address.
func TestTheListenEndpointIsForgottenWhenTheSocketResets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		reset func(t *testing.T, s *testSocket)
	}{
		{"Connect", func(t *testing.T, s *testSocket) {
			s.Close()
			if s.listenEndpoint != listenEnd {
				t.Fatalf("listenEndpoint = %v after Close, want %v", s.listenEndpoint, listenEnd)
			}
			if err := s.Connect(s.cx, remoteEnd, listenEnd); err != nil {
				t.Fatalf("Connect: %v", err)
			}
		}},
		{"the end of TIME-WAIT", func(t *testing.T, s *testSocket) {
			acceptConnection(t, s)
			s.Close()
			receive(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
			send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+1))))
			assertState(t, s, StateTimeWait)
			receive(t, s, fromLocal(withSeq(localSeq.Add(1+1)), withAck(remoteSeq.Add(1+1))))
			receiveNothing(t, s.at(closeDelay))
		}},
		{"the loss of the local address", func(t *testing.T, s *testSocket) {
			acceptConnection(t, s)
			if err := s.cx.SetIPAddrs([]netip.Prefix{netip.PrefixFrom(otherAddr, 24)}); err != nil {
				t.Fatalf("SetIPAddrs: %v", err)
			}
			receiveNothing(t, s)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketClosed(t)
			if err := s.Listen(listenEnd); err != nil {
				t.Fatalf("Listen: %v", err)
			}

			test.reset(t, s)

			if s.listenEndpoint != (wire.ListenEndpoint{}) {
				t.Errorf("listenEndpoint = %v after %s, want zero", s.listenEndpoint, test.name)
			}
		})
	}
}

// acceptConnection completes the handshake of a listening socket with the
// tests' peer.
func acceptConnection(t *testing.T, s *testSocket) {
	t.Helper()
	send(t, s, remoteSYN())
	receive(t, s, synACK())
	send(t, s, ackFromRemote(0))
	assertState(t, s, StateEstablished)
}

func TestEndpointsLastUntilTheRSTOfAnAbortIsSent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		socket  func(t *testing.T) *testSocket
		wantRST wire.TCPRepr
	}{
		{"Abort", func(t *testing.T) *testSocket {
			s := socketEstablished(t)
			s.Abort()
			return s
		}, fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)))},
		{"Close in SYN-SENT", func(t *testing.T) *testSocket {
			s := socketSynSent(t)
			s.Close()
			return s
		}, fromLocal(withControl(wire.TCPControlRST), withSeq(localSeq), withAck(0))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := test.socket(t)
			assertState(t, s, StateClosed)
			assertEndpoints(t, s, localEnd, remoteEnd)

			receiveOnly(t, s, test.wantRST)

			assertEndpoints(t, s, netip.AddrPort{}, netip.AddrPort{})
		})
	}
}

func TestEndpointsLastInTimeWait(t *testing.T) {
	t.Parallel()
	timeWaitSockets := map[string]func(t *testing.T) *testSocket{
		"from FIN-WAIT-2": socketTimeWaitFromFinWait2,
		"from CLOSING":    socketTimeWaitFromClosing,
	}
	for name, timeWait := range timeWaitSockets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := timeWait(t)

			// The final ACK goes out, if it has yet to.
			dispatchCollecting(t, s)

			assertEndpoints(t, s, localEnd, remoteEnd)
			// TIME-WAIT began at 1 s.
			receiveNothing(t, s.at(time.Second+closeDelay))
			assertState(t, s, StateClosed)
			assertEndpoints(t, s, netip.AddrPort{}, netip.AddrPort{})
		})
	}
}
