package satnet

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// A TCPConn over an engine without a driver: the tests deliver segments and
// poll the stack by hand, so that nothing is sent or acknowledged behind
// their back.

// openTCPConn returns a TCPConn over a connection to connectionServer, its
// handshake done by hand on the stack of sharedEngine, a link that is up,
// with our port and where our data starts, as connected does. The test's
// end closes it.
func openTCPConn(t *testing.T, sharedEngine *shared) (*TCPConn, uint16, int32) {
	t.Helper()
	var (
		conn  *TCPConn
		port  uint16
		first int32
	)
	sharedEngine.update(func(e *engine) {
		var handle ipstack.SocketHandle
		handle, port, first = connected(t, e.stack, time.Now())
		conn = newTCPConn(sharedEngine, handle, netip.AddrPort{}, connectionServer)
	})
	t.Cleanup(func() {
		// Closing a TCPConn cannot fail.
		_ = conn.Close()
	})
	return conn, port, first
}

// newAddress is the address IPCP assigns when it renegotiates in the tests.
var newAddress = ipv4(10, 64, 0, 3)

// moveToANewAddress has IPCP assign newAddress, as when PPP renegotiates,
// then polls the stack, which resets the connections on the old address.
func moveToANewAddress(t *testing.T, sharedEngine *shared) {
	t.Helper()
	var renegotiated error
	sharedEngine.update(func(e *engine) {
		renegotiated = e.linkUp(IPv4Settings{LocalAddress: newAddress}, engineMTU, time.Now())
		e.stack.setLineReady(true)
		e.stack.poll(time.Now())
	})
	if renegotiated != nil {
		t.Fatal(renegotiated)
	}
}

func TestWaitAcknowledgedReportsDataANewAddressCutOff(t *testing.T) {
	for _, c := range []struct {
		name string
		// handOver moves what was written on before the address changes.
		handOver func(s *stack)
	}{
		{"still in the outbox", func(*stack) {}},
		{"sent by the stack, unacknowledged", func(s *stack) {
			s.setLineReady(true)
			s.poll(time.Now())
			drain(s)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sharedEngine := upEngine(t)
			conn, _, _ := openTCPConn(t, sharedEngine)
			if _, err := conn.Write([]byte("on its way")); err != nil {
				t.Fatal(err)
			}
			sharedEngine.update(func(e *engine) { c.handOver(e.stack) })
			moveToANewAddress(t, sharedEngine)
			expectResetWithNothingLeft(t, sharedEngine, conn)

			err := conn.WaitAcknowledged(time.Now().Add(waitBudget))

			if !errors.Is(err, ErrConnectionReset) {
				t.Errorf("WaitAcknowledged = %v, want ErrConnectionReset: the data never arrived", err)
			}
		})
	}
}

// expectResetWithNothingLeft checks that conn's connection was reset, and
// that nothing but the record of the cut-off shows its data was lost.
func expectResetWithNothingLeft(t *testing.T, sharedEngine *shared, conn *TCPConn) {
	t.Helper()
	e := sharedEngine.lock()
	defer sharedEngine.unlock()
	handle := conn.conn.handle
	if e.stack.tcp(handle).IsOpen() || e.stack.tcpUnacknowledged(handle) != 0 {
		t.Fatalf("open %t with %d bytes unacknowledged; want a reset that left nothing",
			e.stack.tcp(handle).IsOpen(), e.stack.tcpUnacknowledged(handle))
	}
}

// connectionEnding is a way a connection ends at once, and the kind of the
// error that says so.
type connectionEnding struct {
	name string
	end  func(t *testing.T, sharedEngine *shared, conn *TCPConn)
	kind ErrorKind
}

// droppedCall is why the link hangs up in the tests of how connections end.
var droppedCall = CloseReason{Kind: CloseReasonCarrierLost}

// connectionEndings returns the ways a connection ends at once: the hang-up,
// as it starts and once it is over, the driver abandoning the link, and an
// abort.
func connectionEndings() []connectionEnding {
	return []connectionEnding{
		{"the hang-up", func(_ *testing.T, sharedEngine *shared, _ *TCPConn) {
			sharedEngine.update(func(e *engine) { e.beginClosing(droppedCall) })
		}, ErrLinkDown},
		{"the hang-up, once over", func(_ *testing.T, sharedEngine *shared, _ *TCPConn) {
			sharedEngine.update(func(e *engine) {
				e.beginClosing(droppedCall)
				e.finishClosing(droppedCall, false)
			})
		}, ErrLinkDown},
		{"the driver abandoning the link", func(_ *testing.T, sharedEngine *shared, _ *TCPConn) {
			sharedEngine.update(func(e *engine) { e.abandon("a transport bug") })
		}, ErrLinkDown},
		{"an abort", func(t *testing.T, _ *shared, conn *TCPConn) {
			if err := conn.Abort(); err != nil {
				t.Fatal(err)
			}
		}, ErrConnectionReset},
	}
}

func TestWaitAcknowledgedReportsDataTheConnectionsEndDroppedBeforeItLeft(t *testing.T) {
	for _, ending := range connectionEndings() {
		t.Run(ending.name, func(t *testing.T) {
			sharedEngine := upEngine(t)
			conn, _, _ := openTCPConn(t, sharedEngine)
			// Written since the stack was last polled, it waits in the outbox,
			// and the send buffer is empty.
			if _, err := conn.Write([]byte("never sent")); err != nil {
				t.Fatal(err)
			}
			woken := make(chan error, 1)
			go func() { woken <- conn.WaitAcknowledged(time.Time{}) }()
			sharedEngine.awaitWaitingCall(t, waitBudget)

			ending.end(t, sharedEngine, conn)

			if err := awaitPromptly(t, woken); !errors.Is(err, ending.kind) {
				t.Errorf("the WaitAcknowledged it woke = %v, want %v: the data never left", err, ending.kind)
			}
			if err := conn.WaitAcknowledged(time.Now().Add(waitBudget)); !errors.Is(err, ending.kind) {
				t.Errorf("a later WaitAcknowledged = %v, want %v", err, ending.kind)
			}
		})
	}
}

func TestWhatWasAcknowledgedBeforeTheConnectionEndedStaysDelivered(t *testing.T) {
	for _, ending := range connectionEndings() {
		t.Run(ending.name, func(t *testing.T) {
			sharedEngine := upEngine(t)
			conn, port, first := openTCPConn(t, sharedEngine)
			report := []byte("report")
			if _, err := conn.Write(report); err != nil {
				t.Fatal(err)
			}
			// The stack sends it, and the server acknowledges it.
			sharedEngine.update(func(e *engine) {
				e.stack.setLineReady(true)
				e.stack.poll(time.Now())
				drain(e.stack)
				e.stack.deliver(fromServer(port, serverFirst, first+int32(len(report)), nil))
				e.stack.poll(time.Now())
			})
			if err := conn.WaitAcknowledged(time.Now()); err != nil {
				t.Fatalf("WaitAcknowledged before the end = %v, want the report delivered", err)
			}

			ending.end(t, sharedEngine, conn)

			if err := conn.WaitAcknowledged(time.Now().Add(waitBudget)); err != nil {
				t.Errorf("WaitAcknowledged = %v, want the report still delivered", err)
			}
		})
	}
}

// writeResult is what a Write returned.
type writeResult struct {
	written int
	err     error
}

// The last words of the close tests: two segments' worth at the tests' MTU,
// which leave, and the FIN with them, within finPolls polls of a ready line.
const (
	lastWordsLen = 1000
	finPolls     = 4
)

// expectWrittenThenTheFIN checks that the one connection on sharedEngine's
// stack sends the written bytes, then its FIN, within polls polls of a
// ready line.
func expectWrittenThenTheFIN(t *testing.T, sharedEngine *shared, written, polls int) {
	t.Helper()
	var (
		sent     int
		finished bool
	)
	sharedEngine.update(func(e *engine) {
		sent, finished = sentBeforeTheFIN(t, e.stack, time.Now(), polls)
	})
	if !finished || sent != written {
		t.Errorf("%d bytes went, then a FIN: %t; want the %d bytes written, then the FIN", sent, finished, written)
	}
}

func TestCloseSendsWhatWasWrittenThenTheFIN(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _, _ := openTCPConn(t, sharedEngine)
	if _, err := conn.Write(make([]byte, lastWordsLen)); err != nil {
		t.Fatal(err)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	expectWrittenThenTheFIN(t, sharedEngine, lastWordsLen, finPolls)
}

func TestWhatAWriteQueuedBeforeACloseWriteStoppedItStillGoesBeforeTheFIN(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _, _ := openTCPConn(t, sharedEngine)
	sendBuffer := DefaultTuning().TCPSendBuffer
	written := make(chan writeResult, 1)
	go func() {
		count, err := conn.Write(make([]byte, 3*sendBuffer))
		written <- writeResult{count, err}
	}()
	// Nothing polls the stack: the write fills the send buffer, then waits
	// for room.
	sharedEngine.awaitWaitingCall(t, waitBudget)

	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	if got := awaitPromptly(t, written); got.written != sendBuffer || !errors.Is(got.err, ErrConnectionReset) {
		t.Fatalf("Write = %d, %v; want the %d bytes it queued, and ErrConnectionReset", got.written, got.err, sendBuffer)
	}
	expectWrittenThenTheFIN(t, sharedEngine, sendBuffer, sendBuffer/segmentData+2)
}

// openedTCP is a connection that openTCPConn opened, with the engine it runs
// over, our port, and where our data starts.
type openedTCP struct {
	shared *shared
	conn   *TCPConn
	port   uint16
	first  int32
}

// endOfSending is a way a connection stops sending while a call waits for
// room in its send buffer.
type endOfSending struct {
	name string
	end  func(t *testing.T, opened openedTCP)
}

// endsOfSending returns the ways a connection stops sending by itself, with
// the link still up: an abort, the peer's reset, and a new address, on which
// the stack resets the connection.
func endsOfSending() []endOfSending {
	return []endOfSending{
		{"an abort", func(t *testing.T, opened openedTCP) {
			if err := opened.conn.Abort(); err != nil {
				t.Fatal(err)
			}
		}},
		{"the peer's reset", func(_ *testing.T, opened openedTCP) {
			deliverAndPoll(opened.shared, resetFromServer(opened.port, opened.first))
		}},
		{"a new address", func(t *testing.T, opened openedTCP) {
			moveToANewAddress(t, opened.shared)
		}},
	}
}

// resetFromServer returns the server's reset of the connection at our port,
// acknowledging what our data starts at, first.
func resetFromServer(port uint16, first int32) []byte {
	reset := fromServer(port, serverFirst, first, nil)
	packet := wire.IPv4Packet(reset)
	segment := wire.TCPPacket(packet.Payload())
	segment.SetRST(true)
	segment.FillChecksum(packet.SrcAddr(), packet.DstAddr())
	return reset
}

// TestAWaitForRoomEndsOnceTheConnectionNoLongerSends checks what a TLS
// session's wait for room in TCP relies on: no room comes once the
// connection stops sending, and the call that waits for it must not wait
// for as long as the link lives.
func TestAWaitForRoomEndsOnceTheConnectionNoLongerSends(t *testing.T) {
	for _, ending := range endsOfSending() {
		t.Run(ending.name, func(t *testing.T) {
			sharedEngine := upEngine(t)
			conn, port, first := openTCPConn(t, sharedEngine)
			// Nothing polls the stack: the send buffer stays full.
			if _, err := conn.Write(make([]byte, DefaultTuning().TCPSendBuffer)); err != nil {
				t.Fatal(err)
			}
			woken := make(chan error, 1)
			go func() { woken <- conn.waitWritable(fixedDeadline(time.Now().Add(time.Minute))) }()
			sharedEngine.awaitWaitingCall(t, waitBudget)

			ending.end(t, openedTCP{shared: sharedEngine, conn: conn, port: port, first: first})

			// The wait is over; the next try at sending says why.
			if err := awaitPromptly(t, woken); err != nil {
				t.Errorf("waitWritable = %v, want the wait over", err)
			}
			if _, err := conn.trySend([]byte("more")); !errors.Is(err, ErrConnectionReset) {
				t.Errorf("trySend after the wait = %v, want ErrConnectionReset", err)
			}
		})
	}
}

// abortedConnection returns a TCPConn that openTCPConn opened and that was
// then aborted, with the engine it runs over.
func abortedConnection(t *testing.T) (*shared, *TCPConn) {
	t.Helper()
	sharedEngine := upEngine(t)
	conn, _, _ := openTCPConn(t, sharedEngine)
	if err := conn.Abort(); err != nil {
		t.Fatal(err)
	}
	return sharedEngine, conn
}

// sendTheReset polls the stack of sharedEngine with the line ready, failing
// the test unless a reset leaves: an aborted socket keeps its peer, and is
// not finished, until then.
func sendTheReset(t *testing.T, sharedEngine *shared) {
	t.Helper()
	for _, datagram := range sentOverAReadyLine(sharedEngine) {
		if segmentOf(t, datagram).RST() {
			return
		}
	}
	t.Fatal("no reset left")
}

// holdsSocket reports whether the stack of sharedEngine still holds conn's
// socket: its outbox goes with it, and only with it.
func holdsSocket(sharedEngine *shared, conn *TCPConn) bool {
	e := sharedEngine.lock()
	defer sharedEngine.unlock()
	return e.stack.outbox(conn.conn.handle) != nil
}

// TestAnAbortedConnectionKeepsItsSocketUntilClosed checks that Abort resets
// the connection and keeps its socket,
// rather than letting it go once the reset has left, as the stack's abortTCP
// does: the TCPConn's handle must name its own socket until Close, not none,
// on which its calls would panic, nor a new connection's that took the slot.
func TestAnAbortedConnectionKeepsItsSocketUntilClosed(t *testing.T) {
	sharedEngine, conn := abortedConnection(t)

	sendTheReset(t, sharedEngine)

	if !holdsSocket(sharedEngine, conn) {
		t.Fatal("the stack let the socket go before the connection was closed")
	}
	if _, err := conn.Write([]byte("too late")); !errors.Is(err, ErrConnectionReset) {
		t.Errorf("Write = %v, want ErrConnectionReset", err)
	}
	// Nothing was written, so nothing was dropped.
	if err := conn.WaitAcknowledged(time.Now()); err != nil {
		t.Errorf("WaitAcknowledged = %v, want nil", err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Errorf("CloseWrite = %v, want nil", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	sentOverAReadyLine(sharedEngine)
	if holdsSocket(sharedEngine, conn) {
		t.Error("the socket outlives the connection's Close")
	}
}

// TestAnAbortedConnectionClosedBeforeItsResetLeftLetsItsSocketGoOnce is Go
// only: were the stack to let the socket's handle go a second time, its
// polls would panic, and so would the driver's, ending the link.
func TestAnAbortedConnectionClosedBeforeItsResetLeftLetsItsSocketGoOnce(t *testing.T) {
	sharedEngine, conn := abortedConnection(t)

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	sendTheReset(t, sharedEngine)
	sentOverAReadyLine(sharedEngine)
	if holdsSocket(sharedEngine, conn) {
		t.Error("the socket outlives the connection's Close")
	}
}

// TestNothingNewIsSendableWhileTheLinkDrains checks what a TLS write relies
// on to refuse what could never leave: while a requested close drains, the
// link takes nothing new, though the connection itself is still open.
func TestNothingNewIsSendableWhileTheLinkDrains(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _, _ := openTCPConn(t, sharedEngine)
	if err := conn.checkSendable(); err != nil {
		t.Fatalf("checkSendable on a link that is up = %v", err)
	}

	sharedEngine.update(func(e *engine) { e.beginDraining() })

	var linkErr *Error
	if err := conn.checkSendable(); !errors.As(err, &linkErr) || linkErr.Kind != ErrLinkDown ||
		linkErr.Reason != (CloseReason{Kind: CloseReasonRequested}) {
		t.Errorf("checkSendable while the link drains = %v, want ErrLinkDown for the requested close", err)
	}
}

func TestWaitAcknowledgedTimesOutAsATCPFlush(t *testing.T) {
	sharedEngine := upEngine(t)
	conn, _, _ := openTCPConn(t, sharedEngine)
	if _, err := conn.Write([]byte("never acknowledged")); err != nil {
		t.Fatal(err)
	}

	err := conn.WaitAcknowledged(time.Now())

	var timedOut *Error
	if !errors.As(err, &timedOut) || timedOut.Kind != ErrTimedOut || timedOut.Operation != operationTCPFlush {
		t.Errorf("WaitAcknowledged = %v, want the TCP flush timed out", err)
	}
}
