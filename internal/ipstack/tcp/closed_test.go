package tcp

// The tests of smoltcp's socket/tcp.rs for the CLOSED state
// (src/socket/tcp.rs, lines 3313-3347).

import (
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestClosedReject(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	assertState(t, s, StateClosed)

	accepted := s.Accepts(sendIPTemplate, fromRemote(withControl(wire.TCPControlSYN)))

	if accepted {
		t.Error("a closed socket accepted a SYN")
	}
}

func TestClosedRejectAfterListen(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)
	if err := s.Listen(localListenEnd); err != nil {
		t.Fatalf("Listen(%v): %v", localListenEnd, err)
	}
	s.Close()

	accepted := s.Accepts(sendIPTemplate, fromRemote(withControl(wire.TCPControlSYN)))

	if accepted {
		t.Error("a socket closed after listening accepted a SYN")
	}
}

func TestClosedClose(t *testing.T) {
	t.Parallel()
	s := socketClosed(t)

	s.Close()

	assertState(t, s, StateClosed)
}
