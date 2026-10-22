package satnet

import (
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
)

// What the tests, internal and external, need to see of the engine: whether
// a call has started to wait. A call that started only after a Close or a
// deadline change meant to wake it would fail at once and test nothing.

// waitingCalls returns how many calls wait for the engine to change.
func (s *shared) waitingCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiting
}

// awaitWaitingCall waits until a call waits for the engine to change,
// failing the test if none does within budget.
func (s *shared) awaitWaitingCall(t *testing.T, budget time.Duration) {
	t.Helper()
	simtest.Eventually(t, budget, "a call waits for the link", func() bool {
		return s.waitingCalls() > 0
	})
}

// PendingLookups returns how many name lookups link's stack has under way.
func PendingLookups(link *Link) int {
	e := link.shared.lock()
	defer link.shared.unlock()
	return len(e.stack.lookups)
}

// HangUpTime returns how long hanging up takes with config: what the call
// time limit allows for.
func HangUpTime(config Config) time.Duration {
	return config.hangUpTime()
}

// AwaitWaitingCall waits until a call on link — a connection's or a
// Watcher's — waits for the link to change, failing the test if none does
// within budget.
func AwaitWaitingCall(t *testing.T, link *Link, budget time.Duration) {
	t.Helper()
	link.shared.awaitWaitingCall(t, budget)
}
