package mobile

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
)

// What the internal and the external tests share: listening to a link,
// waiting for what a link does by itself, and hanging links up when a test
// ends. It is exported, in this test file, so that the external tests can
// use it too: these identifiers exist only in this package's test builds.

// ListenerPatience is how long a link's listener may take to deliver a
// change, or to stop once it has delivered the close: ten of its polls,
// where it needs none.
const ListenerPatience = 10 * listenerPoll

// SlowCallSetup is a call setup slow enough for a test to act while a link
// is being set up — to close its handset, or to cancel the setup — before
// the call comes up.
const SlowCallSetup = 5 * time.Second

// dropPatience is how long CollectGarbageUntil waits for what a dropped
// link does once the garbage collector has found it: hang up, and so give
// its handset back or close its port.
const dropPatience = 30 * time.Second

// collectionInterval is how often CollectGarbageUntil collects garbage:
// often enough not to slow a test down, seldom enough not to busy the
// processor.
const collectionInterval = 50 * time.Millisecond

// CollectGarbageUntil collects garbage, which runs the runtime cleanups of
// what the test let go of, until condition holds, failing the test unless
// it does within dropPatience; what names the awaited state.
func CollectGarbageUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(dropPatience)
	for runtime.GC(); !condition(); runtime.GC() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting until %s", dropPatience, what)
		}
		time.Sleep(collectionInterval)
	}
}

// HangUpAtCleanup hangs link up when the test ends, before its simulator
// closes, in case the test has not.
func HangUpAtCleanup(t *testing.T, link *SatelliteLink) {
	t.Helper()
	t.Cleanup(func() {
		// The test has judged the link already; the cleanup only ends the
		// call, if it is still up.
		_, _ = link.HangUp(linkTimeoutMillis)
	})
}

// StateRecorder is a LinkListener that keeps what it hears, each state as
// DescribeState describes it; the listener goroutine writes while the test
// reads.
type StateRecorder struct {
	mu     sync.Mutex
	states []string
}

// OnStateChanged records state.
func (r *StateRecorder) OnStateChanged(state *LinkState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, DescribeState(state))
	return nil
}

// Heard returns the states heard so far, oldest first.
func (r *StateRecorder) Heard() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.states)
}

// AssertHeardEveryChange fails the test unless listener hears, within
// ListenerPatience, every change of a link that came up and was hung up:
// Up, Closing(Requested), then Closed(Requested).
func AssertHeardEveryChange(t *testing.T, listener *StateRecorder) {
	t.Helper()
	simtest.Eventually(t, ListenerPatience, "the listener hears the link close", func() bool {
		return slices.ContainsFunc(listener.Heard(), describesClosed)
	})
	if heard, want := listener.Heard(), []string{"Up", "Closing(Requested)", "Closed(Requested)"}; !slices.Equal(heard, want) {
		t.Errorf("the listener heard %q, want %q", heard, want)
	}
}

// describesClosed reports whether description, one of DescribeState's, is
// of a closed link, whatever the reason.
func describesClosed(description string) bool {
	return strings.HasPrefix(description, stateNames[LinkStateClosed])
}

// stateNames and reasonNames name the LinkState and LinkCloseReason
// constants, for DescribeState.
var (
	stateNames = map[int]string{
		LinkStateNegotiating: "Negotiating",
		LinkStateUp:          "Up",
		LinkStateClosing:     "Closing",
		LinkStateClosed:      "Closed",
	}
	reasonNames = map[int]string{
		LinkCloseReasonRequested:           "Requested",
		LinkCloseReasonCarrierLost:         "CarrierLost",
		LinkCloseReasonIdleTimeout:         "IdleTimeout",
		LinkCloseReasonCallTimeLimit:       "CallTimeLimit",
		LinkCloseReasonDataLimit:           "DataLimit",
		LinkCloseReasonNegotiationTimedOut: "NegotiationTimedOut",
		LinkCloseReasonPPP:                 "PPP",
		LinkCloseReasonTransport:           "Transport",
		LinkCloseReasonInternal:            "Internal",
	}
)

// DescribeState returns state as the Rust tests' LinkState values print,
// such as "Closed(Requested)", and "nil" for none.
func DescribeState(state *LinkState) string {
	if state == nil {
		return "nil"
	}
	if state.Reason == nil {
		return stateNames[state.Kind]
	}
	return fmt.Sprintf("%s(%s)", stateNames[state.Kind], reasonNames[state.Reason.Kind])
}
