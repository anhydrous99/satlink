package satnet

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// A UDPConn over an engine without a driver: the tests deliver datagrams and
// poll the stack by hand, so that nothing drains or fills the socket behind
// their back.

// udpPeer is the peer of the tests' UDP sockets.
var udpPeer = netip.AddrPortFrom(ipv4(192, 0, 2, 7), 7)

// openUDPConn opens a UDP socket on the stack of sharedEngine, a link that
// is up, and returns the UDPConn talking to udpPeer through it, and its
// local port. The test's end closes it.
func openUDPConn(t *testing.T, sharedEngine *shared) (*UDPConn, uint16) {
	t.Helper()
	return openUDPConnTo(t, sharedEngine, udpPeer)
}

// openUDPConnTo is openUDPConn for a socket talking to peer.
func openUDPConnTo(t *testing.T, sharedEngine *shared, peer netip.AddrPort) (*UDPConn, uint16) {
	t.Helper()
	var (
		conn *UDPConn
		port uint16
	)
	err := sharedEngine.withLock(func(e *engine) error {
		handle, err := e.stack.openUDP()
		if err != nil {
			return err
		}
		local := localUDPEndpoint(e.stack, handle)
		port = local.Port()
		conn = newUDPConn(sharedEngine, handle, local, peer)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Closing a UDPConn cannot fail.
		_ = conn.Close()
	})
	return conn, port
}

// deliverAndPoll hands the stack of sharedEngine datagrams that arrived,
// and polls it.
func deliverAndPoll(sharedEngine *shared, datagrams ...[]byte) {
	sharedEngine.update(func(e *engine) {
		for _, datagram := range datagrams {
			e.stack.deliver(datagram)
		}
		e.stack.poll(time.Now())
	})
}

// sentOverAReadyLine polls the stack of sharedEngine with the line ready,
// and returns what went.
func sentOverAReadyLine(sharedEngine *shared) [][]byte {
	var sent [][]byte
	sharedEngine.update(func(e *engine) {
		e.stack.setLineReady(true)
		e.stack.poll(time.Now())
		sent = drain(e.stack)
	})
	return sent
}

// pollWithTheLineBusy polls the stack of sharedEngine as the driver does
// while the line is busy: what is urgent goes, and the rest waits.
func pollWithTheLineBusy(sharedEngine *shared) {
	sharedEngine.update(func(e *engine) {
		e.stack.setLineReady(false)
		e.stack.poll(time.Now())
		drain(e.stack)
	})
}

// writeWhileTheLineIsBusy writes up to attempts datagrams on conn, each by
// a deadline shortWait away, polling the stack of sharedEngine after each
// as the driver does, with the line busy. It returns how many were written,
// and the error of the write that failed, if one did.
func writeWhileTheLineIsBusy(t *testing.T, sharedEngine *shared, conn *UDPConn, attempts int) (int, error) {
	t.Helper()
	for written := range attempts {
		if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte("more")); err != nil {
			return written, err
		}
		pollWithTheLineBusy(sharedEngine)
	}
	return attempts, nil
}

// fillUDPSendBuffer writes a datagram into each slot of conn's send buffer,
// which no poll empties.
func fillUDPSendBuffer(t *testing.T, conn *UDPConn) {
	t.Helper()
	for range udpSlots {
		if _, err := conn.Write([]byte("queued")); err != nil {
			t.Fatal(err)
		}
	}
}

// writeInBackground writes p on conn in another goroutine, and returns the
// channel its error arrives on.
func writeInBackground(conn *UDPConn, p []byte) <-chan error {
	written := make(chan error, 1)
	go func() {
		_, err := conn.Write(p)
		written <- err
	}()
	return written
}

func TestAUDPConnReadsOnlyItsPeersDatagrams(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, port := openUDPConn(t, sharedEngine)
	deliverAndPoll(sharedEngine,
		udpDatagram(netip.AddrPortFrom(ipv4(198, 51, 100, 9), udpPeer.Port()), port, []byte("another host")),
		udpDatagram(netip.AddrPortFrom(udpPeer.Addr(), udpPeer.Port()+1), port, []byte("another port")),
		udpDatagram(udpPeer, port, []byte("the peer")))
	// A datagram that has arrived is read even with its deadline passed.
	if err := conn.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)

	read, err := conn.Read(buffer)

	if err != nil || string(buffer[:read]) != "the peer" {
		t.Errorf("Read = %q, %v; want the peer's datagram", buffer[:read], err)
	}
	if _, again := conn.Read(buffer); !errors.Is(again, ErrTimedOut) {
		t.Errorf("a second Read = %v, want a timeout: the other datagrams are dropped", again)
	}
}

func TestAWriteWaitsForRoomInAFullUDPSendBuffer(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _ := openUDPConn(t, sharedEngine)
	fillUDPSendBuffer(t, conn)
	written := writeInBackground(conn, []byte("one more"))
	sharedEngine.awaitWaitingCall(t, waitBudget)

	// The stack hands what waits to the device, which makes room, and the
	// line takes it, a datagram per slot.
	for range udpSlots {
		sentOverAReadyLine(sharedEngine)
	}

	if err := awaitResult(t, written); err != nil {
		t.Errorf("the waiting Write = %v, want it taken once there was room", err)
	}
}

func TestUDPWritesWaitForABusyLineOnceAFewDatagramsWaitForIt(t *testing.T) {
	for name, peer := range map[string]netip.AddrPort{
		"to an echo service": udpPeer,
		// The app's, not one of the stack's own queries: it waits for the
		// line as any data does.
		"to a DNS server": netip.AddrPortFrom(lookupServer, dnsPort),
	} {
		t.Run(name, func(t *testing.T) {
			sharedEngine := upEngine(t)
			conn, _ := openUDPConnTo(t, sharedEngine, peer)

			written, err := writeWhileTheLineIsBusy(t, sharedEngine, conn, 2*udpSlots)

			var timedOut *Error
			if !errors.As(err, &timedOut) || timedOut.Kind != ErrTimedOut || timedOut.Operation != operationUDPSend {
				t.Fatalf("after %d datagrams, Write = %v; want the UDP send timed out", written, err)
			}
			if written > udpSlots {
				t.Errorf("%d datagrams were taken for a busy line, want at most %d", written, udpSlots)
			}
		})
	}
}

func TestAWaitingUDPWriteGoesOnceTheLineTakesTheDatagramBeforeIt(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _ := openUDPConn(t, sharedEngine)
	if _, err := conn.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	pollWithTheLineBusy(sharedEngine)
	written := writeInBackground(conn, []byte("second"))
	sharedEngine.awaitWaitingCall(t, waitBudget)

	sent := sentOverAReadyLine(sharedEngine)

	if err := awaitResult(t, written); err != nil || len(sent) != 1 {
		t.Errorf("once the line took %d datagrams, the waiting Write = %v; want the first taken, then the second", len(sent), err)
	}
}

func TestAUDPWriteTimesOutAtItsDeadlineWhileTheSendBufferStaysFull(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _ := openUDPConn(t, sharedEngine)
	fillUDPSendBuffer(t, conn)
	if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}

	err := awaitResult(t, writeInBackground(conn, []byte("one more")))

	var timedOut *Error
	if !errors.As(err, &timedOut) || timedOut.Kind != ErrTimedOut || timedOut.Operation != operationUDPSend {
		t.Errorf("Write = %v, want the UDP send timed out", err)
	}
}

func TestAUDPWriteIsRefusedWhileTheLinkDrains(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _ := openUDPConn(t, sharedEngine)
	sharedEngine.update(func(e *engine) { e.beginDraining() })

	_, err := conn.Write([]byte("too late"))

	// Taken, it would hold the drain, and the billed call, up.
	var linkErr *Error
	if !errors.As(err, &linkErr) || linkErr.Kind != ErrLinkDown || linkErr.Reason != (CloseReason{Kind: CloseReasonRequested}) {
		t.Errorf("Write while the link drains = %v, want ErrLinkDown for the requested close", err)
	}
	e := sharedEngine.lock()
	undelivered := e.stack.hasUndeliveredData()
	sharedEngine.unlock()
	if undelivered {
		t.Error("the datagram waits to be sent: the drain would wait for it")
	}
}

func TestAUDPConnsLocalAddressIsTheOneItsDatagramsLeaveFrom(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _ := openUDPConn(t, sharedEngine)
	moveToANewAddress(t, sharedEngine)

	// Bound to its port only, the socket carries on from the new address.
	if _, err := conn.Write([]byte("from where?")); err != nil {
		t.Fatal(err)
	}
	sent := sentOverAReadyLine(sharedEngine)
	if len(sent) != 1 {
		t.Fatalf("%d datagrams went, want the one written", len(sent))
	}
	packet := wire.IPv4Packet(sent[0])
	source := netip.AddrPortFrom(packet.SrcAddr(), wire.UDPPacket(packet.Payload()).SrcPort())
	if local := conn.LocalAddr().(*net.UDPAddr).AddrPort(); local != source {
		t.Errorf("LocalAddr() = %v, but the datagram left from %v", local, source)
	}
}

func TestAClosedUDPSocketGoesOnceItsQueueIsSent(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _ := openUDPConn(t, sharedEngine)
	if _, err := conn.Write([]byte("last words")); err != nil {
		t.Fatal(err)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	udpSockets := func() int {
		e := sharedEngine.lock()
		defer sharedEngine.unlock()
		return len(e.stack.appUDPPorts())
	}
	if count := udpSockets(); count != 1 {
		t.Fatalf("%d UDP sockets before the queue left, want the closed one", count)
	}
	deliverAndPoll(sharedEngine)
	if count := udpSockets(); count != 0 {
		t.Errorf("%d UDP sockets once the queue left, want none", count)
	}
}

func TestADatagramStillQueuedIsUndelivered(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _ := openUDPConn(t, sharedEngine)

	if _, err := conn.Write([]byte("on its way")); err != nil {
		t.Fatal(err)
	}

	e := sharedEngine.lock()
	undelivered := e.stack.hasUndeliveredData()
	sharedEngine.unlock()
	if !undelivered {
		t.Error("a datagram not yet handed to the line counts as delivered")
	}
}

func TestADatagramWaitingForTheLineIsUndelivered(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _ := openUDPConn(t, sharedEngine)
	for range 2 {
		if _, err := conn.Write([]byte("on its way")); err != nil {
			t.Fatal(err)
		}
	}
	// The stack hands both datagrams to the device, whose line takes one.
	sent := sentOverAReadyLine(sharedEngine)

	e := sharedEngine.lock()
	defer sharedEngine.unlock()
	if queued := e.stack.udp(conn.conn.handle).SendQueue(); len(sent) != 1 || queued != 0 {
		t.Fatalf("%d datagrams went and %d bytes stayed in the socket; want one gone, and the other waiting in the device", len(sent), queued)
	}
	if !e.stack.hasUndeliveredData() {
		t.Error("a datagram waiting for the line counts as delivered: a requested close would drop it")
	}
}

func TestTheLargestDatagramFillsOnePacket(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _ := openUDPConn(t, sharedEngine)

	if _, err := conn.Write(make([]byte, conn.MaxDatagramLen())); err != nil {
		t.Fatalf("Write of MaxDatagramLen() bytes = %v", err)
	}

	var lengths []int
	for _, datagram := range sentOverAReadyLine(sharedEngine) {
		lengths = append(lengths, len(datagram))
	}
	if !slices.Equal(lengths, []int{engineMTU}) {
		t.Errorf("packets of %v bytes went, want one of the %d-byte MTU", lengths, engineMTU)
	}
}
