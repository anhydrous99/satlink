package satnet

import (
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"testing"
	"time"
)

// engineMTU is the MTU the engine tests' stacks have.
const engineMTU = 576

// waitBudget bounds the waits of the engine tests, which should end at once.
const waitBudget = 5 * time.Second

// testEngine returns an engine whose call connected at now, with a stack
// tuned by default.
func testEngine(now time.Time) *engine {
	tuning := DefaultTuning()
	return newEngine(newStack(tuning, now), newNameCache(nil, tuning.DNSCacheLifetime), "", now)
}

// engineUpAt returns an engine whose call connected and whose link came up
// at at, with a stack tuned by default.
func engineUpAt(t *testing.T, at time.Time) *engine {
	t.Helper()
	e := testEngine(at)
	if err := e.linkUp(clientSettings(), engineMTU, at); err != nil {
		t.Fatal(err)
	}
	return e
}

// bareShared returns shared state around an engine without a stack, for
// tests of the waits, which never touch it.
func bareShared() *shared {
	return newShared(newEngine(nil, nil, "", time.Now()))
}

// clientSettings is what IPCP assigns in the engine tests.
func clientSettings() IPv4Settings {
	return IPv4Settings{LocalAddress: netip.AddrFrom4([4]byte{10, 64, 0, 2})}
}

// waitInBackground runs s.waitUntil(terms, try) in another goroutine, and
// returns the channel its result arrives on.
func waitInBackground(s *shared, terms waitTerms, try attempt) <-chan error {
	result := make(chan error, 1)
	go func() { result <- s.waitUntil(terms, try) }()
	return result
}

// awaitResult returns the result of a wait started by waitInBackground,
// failing the test if it does not come within waitBudget.
func awaitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(waitBudget):
		t.Fatal("the wait never ended")
		return nil
	}
}

// awaitPromptly returns what a woken wait delivers on result, failing the
// test unless it comes well within maxWait: the periodic look again ends
// every wait within maxWait anyway, and must not pass for a wake-up.
func awaitPromptly[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(maxWait / 2):
		t.Fatal("the change did not wake the waiter")
		var none T
		return none
	}
}

// never is an attempt that is never over.
func never(*engine) (bool, error) { return false, nil }

func TestNegotiatingIsNotIdlingAndRenegotiatingIsNotTraffic(t *testing.T) {
	connected := time.Now()
	e := testEngine(connected)
	up := connected.Add(20 * time.Second)
	if err := e.linkUp(clientSettings(), engineMTU, up); err != nil {
		t.Fatal(err)
	}
	if idle := e.idleFor(up); idle != 0 {
		t.Fatalf("idle for %v once up", idle)
	}

	// The peer renegotiates; the app has moved nothing since.
	renegotiated := up.Add(10 * time.Second)
	if err := e.linkUp(clientSettings(), engineMTU, renegotiated); err != nil {
		t.Fatal(err)
	}

	if idle := e.idleFor(renegotiated); idle != 10*time.Second {
		t.Errorf("idle for %v after renegotiating, want 10s", idle)
	}
}

// The app's traffic of each kind, over a link that is up, without a driver:
// what restarts the idle clock.

// lookUpAName starts a lookup over sharedEngine, whose stack asks
// lookupServer, and gives up on it at once.
func lookUpAName(t *testing.T, sharedEngine *shared) {
	t.Helper()
	sharedEngine.update(func(e *engine) {
		if err := e.stack.configure(IPv4Settings{LocalAddress: lookupLocal, DNSServers: []netip.Addr{lookupServer}}, engineMTU, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	link := &Link{shared: sharedEngine, policy: DenyAll()}
	if _, err := link.resolveName(context.Background(), "echo.test", time.Now()); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("resolveName = %v, want the lookup given up on at once", err)
	}
}

// openAConnection starts a connection over sharedEngine, and gives up on
// it at once.
func openAConnection(t *testing.T, sharedEngine *shared) {
	t.Helper()
	link := &Link{shared: sharedEngine, policy: DenyAll()}
	if _, err := link.connectTo(context.Background(), connectionServer, time.Now()); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("connectTo = %v, want the connection given up on at once", err)
	}
}

// writeOnTCP writes on a TCP connection over sharedEngine.
func writeOnTCP(t *testing.T, sharedEngine *shared) {
	t.Helper()
	conn, _, _ := openTCPConn(t, sharedEngine)
	if _, err := conn.Write([]byte("traffic")); err != nil {
		t.Fatal(err)
	}
}

// readFromTCP reads what the server sent on a TCP connection over
// sharedEngine.
func readFromTCP(t *testing.T, sharedEngine *shared) {
	t.Helper()
	conn, port, first := openTCPConn(t, sharedEngine)
	sharedEngine.update(func(e *engine) {
		e.stack.deliver(fromServer(port, serverFirst, first, []byte("traffic")))
		e.stack.poll(time.Now())
	})
	if _, err := conn.Read(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
}

// writeOnUDP sends a datagram over sharedEngine.
func writeOnUDP(t *testing.T, sharedEngine *shared) {
	t.Helper()
	conn, _ := openUDPConn(t, sharedEngine)
	if _, err := conn.Write([]byte("traffic")); err != nil {
		t.Fatal(err)
	}
}

// readFromUDP reads a datagram the peer sent over sharedEngine.
func readFromUDP(t *testing.T, sharedEngine *shared) {
	t.Helper()
	conn, port := openUDPConn(t, sharedEngine)
	deliverAndPoll(sharedEngine, udpDatagram(udpPeer, port, []byte("traffic")))
	if _, err := conn.Read(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
}

func TestTheAppsTrafficRestartsTheIdleClock(t *testing.T) {
	for name, traffic := range map[string]func(*testing.T, *shared){
		"a lookup":     lookUpAName,
		"a connection": openAConnection,
		"a TCP write":  writeOnTCP,
		"a TCP read":   readFromTCP,
		"a UDP write":  writeOnUDP,
		"a UDP read":   readFromUDP,
	} {
		t.Run(name, func(t *testing.T) {
			sharedEngine := upEngine(t)
			sharedEngine.update(func(e *engine) { e.lastActivity = time.Now().Add(-time.Hour) })
			before := time.Now()

			traffic(t, sharedEngine)

			e := sharedEngine.lock()
			restarted := !e.lastActivity.Before(before)
			sharedEngine.unlock()
			if !restarted {
				t.Error("the idle clock still runs from an hour ago")
			}
		})
	}
}

func TestOnlyALinkWhosePPPNeverCameUpIsNegotiating(t *testing.T) {
	now := time.Now()
	e := testEngine(now)
	if !e.isNegotiating() {
		t.Fatal("a new link is not negotiating")
	}

	if err := e.linkUp(clientSettings(), engineMTU, now); err != nil {
		t.Fatal(err)
	}

	if e.isNegotiating() {
		t.Error("a link that came up is still negotiating")
	}
}

func TestWaitsUnderWayOutliveARequestedCloseButNothingNewStarts(t *testing.T) {
	now := time.Now()
	e := testEngine(now)
	if err := e.linkUp(clientSettings(), engineMTU, now); err != nil {
		t.Fatal(err)
	}

	e.beginDraining()

	if err := e.checkAlive(); err != nil {
		t.Errorf("a draining link is not alive: %v", err)
	}
	if err := e.checkUsable(); err == nil {
		t.Error("a draining link takes new work")
	}
	e.beginClosing(CloseReason{Kind: CloseReasonIdleTimeout})
	if err := e.checkAlive(); err == nil {
		t.Error("a link hanging up is alive")
	}
}

func TestWaitUntilChecksTheClosedFlagFirst(t *testing.T) {
	s := bareShared()
	closed := true
	tried := false

	err := s.waitUntil(waitTerms{operation: operationTCPRead, until: fixedDeadline(time.Time{}), closed: &closed},
		func(*engine) (bool, error) {
			tried = true
			return true, nil
		})

	if !errors.Is(err, net.ErrClosed) || tried {
		t.Errorf("waitUntil = %v after trying = %t, want net.ErrClosed without a try", err, tried)
	}
}

func TestWaitUntilPrefersTheAttemptToAnExpiredDeadline(t *testing.T) {
	s := bareShared()
	expired := fixedDeadline(time.Now().Add(-time.Second))

	err := s.waitUntil(waitTerms{operation: operationTCPRead, until: expired}, func(*engine) (bool, error) {
		return true, nil
	})

	if err != nil {
		t.Errorf("waitUntil = %v, want the attempt's success", err)
	}
}

func TestWaitUntilTimesOutWithTheOperationsName(t *testing.T) {
	s := bareShared()

	err := s.waitUntil(waitTerms{operation: operationDNSLookup, until: fixedDeadline(time.Now())}, never)

	var timeout *Error
	if !errors.As(err, &timeout) || timeout.Kind != ErrTimedOut || timeout.Operation != operationDNSLookup {
		t.Errorf("waitUntil = %v, want a DNS lookup timeout", err)
	}
}

func TestWaitUntilFailsOnceTheLinkIsDown(t *testing.T) {
	s := bareShared()
	s.engine.setState(LinkState{Kind: LinkStateClosing, Reason: CloseReason{Kind: CloseReasonCarrierLost}})

	err := s.waitUntil(waitTerms{operation: operationTCPRead, until: fixedDeadline(time.Time{})}, never)

	var linkErr *Error
	if !errors.As(err, &linkErr) || linkErr.Kind != ErrLinkDown || linkErr.Reason.Kind != CloseReasonCarrierLost {
		t.Errorf("waitUntil = %v, want the link down for the dropped call", err)
	}
}

func TestWaitUntilCarriesOnWhileARequestedCloseDrains(t *testing.T) {
	s := bareShared()
	s.engine.beginDraining()

	err := s.waitUntil(waitTerms{operation: operationTCPRead, until: fixedDeadline(time.Now())}, never)

	if !errors.Is(err, ErrTimedOut) {
		t.Errorf("waitUntil = %v, want the timeout of a wait that carried on", err)
	}
}

func TestAChangeWakesAWaiter(t *testing.T) {
	s := bareShared()
	result := waitInBackground(s, waitTerms{operation: operationTCPRead, until: fixedDeadline(time.Time{})},
		func(e *engine) (bool, error) {
			return e.connectMessage == "ready", nil
		})
	s.awaitWaitingCall(t, waitBudget)

	s.update(func(e *engine) { e.connectMessage = "ready" })

	if err := awaitPromptly(t, result); err != nil {
		t.Errorf("waitUntil = %v", err)
	}
}

func TestADeadlineMovedWhileWaitingApplies(t *testing.T) {
	s := bareShared()
	var moved atomicTime
	moved.Store(time.Now().Add(time.Hour))
	result := waitInBackground(s, waitTerms{operation: operationTCPRead, until: moved.Load}, never)
	s.awaitWaitingCall(t, waitBudget)

	// Changed under the engine's lock, as a connection's deadline setters
	// change it.
	s.update(func(*engine) { moved.Store(time.Now()) })

	if err := awaitPromptly(t, result); !errors.Is(err, ErrTimedOut) {
		t.Errorf("waitUntil = %v, want a timeout at the moved deadline", err)
	}
}

func TestWaitUntilGivesUpWhenItsContextEnds(t *testing.T) {
	s := bareShared()
	ctx, cancel := context.WithCancel(context.Background())
	result := waitInBackground(s, waitTerms{operation: operationTCPConnect, until: fixedDeadline(time.Time{}), ctx: ctx}, never)
	s.awaitWaitingCall(t, waitBudget)

	cancel()

	if err := awaitResult(t, result); !errors.Is(err, context.Canceled) {
		t.Errorf("waitUntil = %v, want context.Canceled", err)
	}
}

func TestAWatcherKeepsWaitingWhileTheLinkGoesDown(t *testing.T) {
	s := bareShared()
	s.engine.setState(LinkState{Kind: LinkStateClosing, Reason: CloseReason{Kind: CloseReasonCarrierLost}})
	found := make(chan bool, 1)
	go func() {
		found <- s.waitFor(time.Now().Add(waitBudget), func(e *engine) bool {
			return e.state.Kind == LinkStateClosed
		})
	}()
	s.awaitWaitingCall(t, waitBudget)

	s.update(func(e *engine) { e.setState(LinkState{Kind: LinkStateClosed}) })

	if !awaitPromptly(t, found) {
		t.Error("the watcher stopped waiting while the link went down")
	}
}

func TestChangesCloseTheChannelWaitersHold(t *testing.T) {
	for name, change := range map[string]func(s *shared){
		"an update":           func(s *shared) { s.update(func(*engine) {}) },
		"a bare notification": (*shared).notify,
	} {
		t.Run(name, func(t *testing.T) {
			s := bareShared()
			changed := s.changed.wait()

			change(s)

			select {
			case <-changed:
			default:
				t.Error("the waiters' channel is still open")
			}
		})
	}
}

func TestAWatcherGivesUpAtItsDeadline(t *testing.T) {
	s := bareShared()

	if s.waitFor(time.Now(), func(*engine) bool { return false }) {
		t.Error("waitFor found what is never there")
	}
}

func TestTimeToWaitIsCappedAndEndsAtTheDeadline(t *testing.T) {
	if pause, ok := timeToWait(deadlineAfter(math.MaxInt64)); !ok || pause != maxWait {
		t.Errorf("timeToWait = %v, %t; want %v, true", pause, ok, maxWait)
	}
	if pause, ok := timeToWait(time.Time{}); !ok || pause != maxWait {
		t.Errorf("timeToWait(no deadline) = %v, %t", pause, ok)
	}
	if _, ok := timeToWait(time.Now().Add(-time.Nanosecond)); ok {
		t.Error("a passed deadline leaves time")
	}
	if pause, ok := timeToWait(time.Now().Add(maxWait / 2)); !ok || pause > maxWait/2 {
		t.Errorf("timeToWait(half a second) = %v, %t", pause, ok)
	}
}

func TestStateChangesQueueUpOldestFirst(t *testing.T) {
	e := newEngine(nil, nil, "", time.Now())
	e.setState(LinkState{Kind: LinkStateUp})
	e.beginDraining()

	for _, want := range []LinkState{{Kind: LinkStateUp}, closingState(CloseReasonRequested)} {
		if got, ok := e.takeStateChange(); !ok || got != want {
			t.Errorf("takeStateChange() = %v, %t; want %v", got, ok, want)
		}
	}
	if change, ok := e.takeStateChange(); ok {
		t.Errorf("a change too many: %v", change)
	}
}

func TestStatusHandsOutItsOwnSettings(t *testing.T) {
	e := newEngine(nil, nil, "19200", time.Now())
	dnsServer := netip.AddrFrom4([4]byte{10, 64, 0, 1})
	e.ipv4 = copyIPv4Settings(IPv4Settings{LocalAddress: clientSettings().LocalAddress, DNSServers: []netip.Addr{dnsServer}})

	status := e.status()
	status.IPv4.DNSServers[0] = netip.Addr{}

	if again := e.status(); again.IPv4.DNSServers[0] != dnsServer || again.ConnectMessage != "19200" {
		t.Errorf("status() = %+v", again)
	}
}

func TestADeadConnectionIsTheLinksFaultWhileItGoesDown(t *testing.T) {
	e := newEngine(nil, nil, "", time.Now())
	if err := e.connectionError(); !errors.Is(err, ErrConnectionReset) {
		t.Errorf("connectionError() = %v on an up link", err)
	}

	e.setState(LinkState{Kind: LinkStateClosed, Reason: CloseReason{Kind: CloseReasonDataLimit}})

	if err := e.connectionError(); !errors.Is(err, ErrLinkDown) {
		t.Errorf("connectionError() = %v on a closed link", err)
	}
}

func TestIdleTimeIsNeverNegative(t *testing.T) {
	now := time.Now()
	e := newEngine(nil, nil, "", now)

	if idle := e.idleFor(now.Add(-time.Minute)); idle != 0 {
		t.Errorf("idleFor(earlier) = %v", idle)
	}
}

func TestAnAbandonedLinkReportsItselfClosedWithTheCallPerhapsUp(t *testing.T) {
	now := time.Now()
	for name, c := range map[string]struct {
		before LinkState
		want   CloseReason
	}{
		"up":      {LinkState{Kind: LinkStateUp}, CloseReason{Kind: CloseReasonInternal, Detail: "a transport bug"}},
		"closing": {closingState(CloseReasonRequested), CloseReason{Kind: CloseReasonRequested}},
	} {
		t.Run(name, func(t *testing.T) {
			e := testEngine(now)
			e.setState(c.before)

			e.abandon("a transport bug")

			if e.state != (LinkState{Kind: LinkStateClosed, Reason: c.want}) || !e.callMayStillBeUp {
				t.Errorf("state %v, call may still be up: %t", e.state, e.callMayStillBeUp)
			}
		})
	}
}

// panics reports whether f panics.
func panics(f func()) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	f()
	return false
}

func TestALinkWhoseConnectionsCannotBeAbortedStillSaysWhyItCloses(t *testing.T) {
	e := testEngine(time.Now())
	// Without its device, the stack panics as it aborts the connections.
	e.stack.device = nil
	reason := CloseReason{Kind: CloseReasonCarrierLost}

	panicked := panics(func() { e.beginClosing(reason) })

	if !panicked || e.state != (LinkState{Kind: LinkStateClosing, Reason: reason}) {
		t.Errorf("panicked: %t, state %v; want a panic, with the link closing for the dropped call", panicked, e.state)
	}
}

func TestAnAbandonedLinkWhoseConnectionsCannotBeAbortedStillReportsItselfClosed(t *testing.T) {
	e := testEngine(time.Now())
	reason := CloseReason{Kind: CloseReasonCarrierLost}
	e.setState(LinkState{Kind: LinkStateClosing, Reason: reason})
	// Without its device, the stack panics as it aborts the connections.
	e.stack.device = nil

	panicked := panics(func() { e.abandon("a log handler bug") })

	if !panicked || e.state != (LinkState{Kind: LinkStateClosed, Reason: reason}) || !e.callMayStillBeUp {
		t.Errorf("panicked: %t, state %v, call may still be up: %t; want a panic, with the link closed for the dropped call and the call perhaps up",
			panicked, e.state, e.callMayStillBeUp)
	}
}

func TestAbandoningAClosedLinkChangesNothing(t *testing.T) {
	e := testEngine(time.Now())
	e.finishClosing(CloseReason{Kind: CloseReasonCarrierLost}, false)

	e.abandon("too late")

	if e.state.Reason.Kind != CloseReasonCarrierLost || e.callMayStillBeUp {
		t.Errorf("state %v, call may still be up: %t", e.state, e.callMayStillBeUp)
	}
}

func TestHangingUpForTheDrainingReasonReportsNoNewState(t *testing.T) {
	e := testEngine(time.Now())
	e.beginDraining()

	e.beginClosing(CloseReason{Kind: CloseReasonRequested})

	if _, ok := e.takeStateChange(); !ok {
		t.Fatal("no change for the drain")
	}
	if change, ok := e.takeStateChange(); ok {
		t.Errorf("a second change: %v", change)
	}
}

func TestRenegotiatingWhileClosingDoesNotReopenTheLink(t *testing.T) {
	now := time.Now()
	e := testEngine(now)
	e.beginDraining()

	if err := e.linkUp(clientSettings(), engineMTU, now); err != nil {
		t.Fatal(err)
	}

	if e.state != closingState(CloseReasonRequested) {
		t.Errorf("state %v", e.state)
	}
}
