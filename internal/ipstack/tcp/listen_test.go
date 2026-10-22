package tcp

// Tests of the LISTEN state.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestListenSACKOption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		syn        wire.TCPRepr
		wantSACK   bool
		wantSYNACK wire.TCPRepr
	}{
		{"without SACK", remoteSYN(), false, synACK()},
		{"with SACK", remoteSYN(withSACKPermitted()), true, synACK(withSACKPermitted())},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := socketListen(t)

			send(t, s, test.syn)

			if s.remoteHasSACK != test.wantSACK {
				t.Errorf("remoteHasSACK = %v, want %v", s.remoteHasSACK, test.wantSACK)
			}
			receiveOnly(t, s, test.wantSYNACK)
		})
	}
}

func TestListenSYNWinScaleBuffers(t *testing.T) {
	t.Parallel()
	for _, test := range receiveBufferWindowShifts {
		t.Run(fmt.Sprint(test.bufferLen), func(t *testing.T) {
			t.Parallel()
			s := socketClosedWithBufferSizes(t, defaultBufferLen, test.bufferLen)
			s.state = StateListen
			s.listenEndpoint = listenEnd
			if s.remoteWindowShift != test.shift {
				t.Fatalf("remoteWindowShift = %d before the SYN, want %d", s.remoteWindowShift, test.shift)
			}

			send(t, s, remoteSYN(withWindowScale(0)))

			if s.remoteWindowShift != test.shift {
				t.Fatalf("remoteWindowShift = %d after the SYN, want %d", s.remoteWindowShift, test.shift)
			}
			receiveOnly(t, s, synACK(withWindowScale(test.shift), withWindow(unscaledWindow(test.bufferLen))))
		})
	}
}

func TestListenSYNTinyMSSIsClamped(t *testing.T) {
	t.Parallel()
	s := socketListen(t)

	send(t, s, remoteSYN(withMSS(tinyMSS)))

	assertState(t, s, StateSynReceived)
	if s.remoteMSS != minRemoteMSS {
		t.Errorf("remoteMSS = %d, want %d", s.remoteMSS, minRemoteMSS)
	}
}

func TestListenSYNZeroMSSIsIgnored(t *testing.T) {
	t.Parallel()
	s := socketListen(t)

	send(t, s, remoteSYN(withMSS(0)))

	assertState(t, s, StateSynReceived)
	if s.remoteMSS != defaultMSS {
		t.Errorf("remoteMSS = %d, want %d", s.remoteMSS, defaultMSS)
	}
}

func TestListenSanity(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)

	if err := s.Listen(wire.ListenEndpoint{Port: localPort}); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	sanity(t, s, socketListen(t))
}

func TestListenValidation(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)

	err := s.Listen(wire.ListenEndpoint{Port: 0})

	if !errors.Is(err, ErrUnaddressable) {
		t.Errorf("Listen on port 0 = %v, want %v", err, ErrUnaddressable)
	}
}

func TestListenTwice(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	endpoint := wire.ListenEndpoint{Port: localPort}
	if err := s.Listen(endpoint); err != nil {
		t.Fatalf("first Listen: %v", err)
	}

	// Multiple calls to listen are okay if it's the same local endpoint and
	// the state is still listening.
	if err := s.Listen(endpoint); err != nil {
		t.Errorf("Listen while listening on the same endpoint = %v, want nil", err)
	}
	s.setState(StateSynReceived) // state change, simulate incoming connection
	if err := s.Listen(endpoint); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Listen in SYN-RECEIVED = %v, want %v", err, ErrInvalidState)
	}
}

func TestListenSYN(t *testing.T) {
	t.Parallel()
	s := socketListen(t)

	send(t, s, remoteSYN())

	sanity(t, s, socketSynReceived(t))
}

func TestListenSYNRejectACK(t *testing.T) {
	t.Parallel()
	s := socketListen(t)

	accepted := s.Accepts(sendIPTemplate, remoteSYN(withAck(localSeq)))

	if accepted {
		t.Error("a listening socket accepted a SYN with an ACK")
	}
	assertState(t, s, StateListen)
}

func TestListenRST(t *testing.T) {
	t.Parallel()
	s := socketListen(t)

	accepted := s.Accepts(sendIPTemplate, fromRemote(withControl(wire.TCPControlRST), withSeq(remoteSeq), withoutAck()))

	if accepted {
		t.Error("a listening socket accepted a RST")
	}
	assertState(t, s, StateListen)
}

func TestListenClose(t *testing.T) {
	t.Parallel()
	s := socketListen(t)

	s.Close()

	assertState(t, s, StateClosed)
}
