package tcp

// The tests of smoltcp's socket/tcp.rs for flow control
// (src/socket/tcp.rs, lines 7791-8200).

import (
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// assertLastWindowIsCurrent fails the test unless the window the socket
// last announced is its receive buffer's window now.
func assertLastWindowIsCurrent(t *testing.T, s *testSocket) {
	t.Helper()
	if window := uint16(s.receiveBuffer.Window()); s.remoteLastWindow != window {
		t.Fatalf("remoteLastWindow = %d, want the receive window, %d", s.remoteLastWindow, window)
	}
}

func TestPSHTransmit(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6
	mustSendSlice(t, s, []byte("abcdef"))
	mustSendSlice(t, s, []byte("123456"))

	receiveExact(t, s, dataFromLocal(0, "abcdef", withControl(wire.TCPControlNone)))
	receiveExact(t, s, dataFromLocal(6, "123456", withControl(wire.TCPControlPSH)))
}

func TestPSHReceive(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	send(t, s, dataFromRemote(0, "abcdef", withControl(wire.TCPControlPSH)))

	receiveOnly(t, s, ackFromLocal(6, 58))
}

func TestZeroWindowACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	replaceReceiveBuffer(s, smallBufferLen)
	send(t, s, dataFromRemote(0, "abcdef"))
	receiveOnly(t, s, ackFromLocal(6, 0))

	sendExpectingReply(t, s, dataFromRemote(6, "123456"), ackFromLocal(6, 0))
}

func TestZeroWindowACKNotRateLimited(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	replaceReceiveBuffer(s, smallBufferLen)
	send(t, s, dataFromRemote(0, "abcdef"))
	receiveOnly(t, s, ackFromLocal(6, 0))
	sendExpectingReply(t, s, dataFromRemote(6, "123456"), ackFromLocal(6, 0))

	// The remote retransmits into the zero window again within a second,
	// e.g. because the ACK above was lost. The ACK must not be withheld by
	// challenge ACK rate limiting: it is the remote's only way to learn the
	// window state, and a data segment cannot cause an ACK loop.
	sendExpectingReply(t, s.at(100*time.Millisecond), dataFromRemote(6, "123456"), ackFromLocal(6, 0))
}

func TestZeroWindowFIN(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	replaceReceiveBuffer(s, smallBufferLen)
	s.ackDelay = 0
	send(t, s, dataFromRemote(0, "abcdef"))
	receiveOnly(t, s, ackFromLocal(6, 0))

	// Even though the sequence space for the FIN itself is outside the
	// window, it is not data, so FIN must be accepted when window full.
	send(t, s, fromRemote(withControl(wire.TCPControlFIN), withSeq(remoteSeq.Add(1+6)), withAck(localSeq.Add(1))))

	assertState(t, s, StateCloseWait)
	receiveOnly(t, s, ackFromLocal(6+1, 0))
}

func TestZeroWindowACKOnWindowGrowth(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	replaceReceiveBuffer(s, smallBufferLen)
	send(t, s, dataFromRemote(0, "abcdef"))
	receiveOnly(t, s, ackFromLocal(6, 0))
	receiveNothing(t, s)

	receivePrefix(t, s, "abc")
	receive(t, s, ackFromLocal(6, 3))
	receiveNothing(t, s)

	receiveExactly(t, s, "def")
	receive(t, s, ackFromLocal(6, 6))
}

func TestWindowUpdateWithDelayACK(t *testing.T) {
	t.Parallel()
	s := socketEstablishedWithBufferSizes(t, 6, 6)
	s.ackDelay = 10 * time.Millisecond
	send(t, s, dataFromRemote(0, "abcdef"))
	receiveNothing(t, s.at(5*time.Millisecond))

	receivePrefix(t, s, "ab")
	receive(t, s.at(5*time.Millisecond), ackFromLocal(6, 2))

	receivePrefix(t, s, "c")
	receiveNothing(t, s.at(5*time.Millisecond))

	receivePrefix(t, s, "d")
	receive(t, s.at(5*time.Millisecond), ackFromLocal(6, 4))
}

func TestFillPeerWindow(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6

	mustSendSlice(t, s, []byte("abcdef123456!@#$%^"))

	receiveOnly(t, s,
		dataFromLocal(0, "abcdef"),
		dataFromLocal(6, "123456"),
		dataFromLocal(6+6, "!@#$%^"))
}

func TestAnnounceWindowAfterRead(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	replaceReceiveBuffer(s, smallBufferLen)
	send(t, s, dataFromRemote(0, "abc"))
	receiveOnly(t, s, ackFromLocal(3, 3))
	// Test that Dispatch updates remoteLastWindow
	assertLastWindowIsCurrent(t, s)
	mustReceive(t, s, consumeAll)
	if !s.windowToUpdate() {
		t.Fatal("windowToUpdate() = false once the buffer was read empty, want true")
	}
	receiveOnly(t, s, ackFromLocal(3, 6))
	assertLastWindowIsCurrent(t, s)

	// Provoke immediate ACK to test that Process updates remoteLastWindow
	sendExpectingReply(t, s, dataFromRemote(6, "def"), ackFromLocal(3, 6))
	sendExpectingReply(t, s, dataFromRemote(3, "abc"), ackFromLocal(9, 0))

	assertLastWindowIsCurrent(t, s)
	mustReceive(t, s, consumeAll)
	if !s.windowToUpdate() {
		t.Fatal("windowToUpdate() = false once the full buffer was read empty, want true")
	}
}
