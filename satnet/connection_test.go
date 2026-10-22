package satnet

import (
	"net/netip"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/simtest"
)

// cleanupBudget bounds how long a collected connection may take to be closed.
const cleanupBudget = 10 * time.Second

// echoEndpoint is the peer of the cleanup tests' connections.
var echoEndpoint = netip.MustParseAddrPort("192.0.2.7:7")

// upEngine returns the shared state of an engine whose link is up.
func upEngine(t *testing.T) *shared {
	t.Helper()
	return newShared(engineUpAt(t, time.Now()))
}

// dropTCPConn makes a TCPConn over handle and lets it go.
func dropTCPConn(sharedEngine *shared, handle ipstack.SocketHandle) {
	newTCPConn(sharedEngine, handle, netip.AddrPort{}, echoEndpoint)
}

// dropUDPConn makes a UDPConn over handle and lets it go.
func dropUDPConn(sharedEngine *shared, handle ipstack.SocketHandle) {
	newUDPConn(sharedEngine, handle, netip.AddrPort{}, echoEndpoint)
}

// awaitClosing collects garbage until the stack is closing the socket handle
// names, failing the test if it never does.
func awaitClosing(t *testing.T, sharedEngine *shared, closing func(*stack) []ipstack.SocketHandle, handle ipstack.SocketHandle) {
	t.Helper()
	simtest.Eventually(t, cleanupBudget, "the collected connection is closed", func() bool {
		runtime.GC()
		e := sharedEngine.lock()
		defer sharedEngine.unlock()
		return slices.Contains(closing(e.stack), handle)
	})
}

func TestACollectedTCPConnIsClosedGracefully(t *testing.T) {
	sharedEngine := upEngine(t)
	var handle ipstack.SocketHandle
	sharedEngine.update(func(e *engine) {
		handle, _, _ = connected(t, e.stack, time.Now())
		if _, err := e.stack.sendTCP(handle, make([]byte, lastWordsLen)); err != nil {
			t.Fatal(err)
		}
	})

	dropTCPConn(sharedEngine, handle)

	awaitClosing(t, sharedEngine, func(s *stack) []ipstack.SocketHandle { return s.closingTCP }, handle)
	expectWrittenThenTheFIN(t, sharedEngine, lastWordsLen, finPolls)
}

func TestACollectedUDPConnLetsItsSocketGo(t *testing.T) {
	sharedEngine := upEngine(t)
	handle, err := sharedEngine.engine.stack.openUDP()
	if err != nil {
		t.Fatal(err)
	}

	dropUDPConn(sharedEngine, handle)

	awaitClosing(t, sharedEngine, func(s *stack) []ipstack.SocketHandle { return s.closingUDP }, handle)
}
