package simtest

import (
	"testing"
	"time"
)

// checkInterval is how often Eventually looks at its condition: often
// enough not to slow a test down, seldom enough not to busy the processor.
const checkInterval = 5 * time.Millisecond

// Eventually checks condition every few milliseconds until it holds, and
// fails the test at once if it still does not after timeout. what names the
// awaited state in that failure, as in "timed out after 10s waiting until
// the handset is free". It is the way to wait for something to happen
// without sleeping for a guess.
func Eventually(t testing.TB, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out after %v waiting until %s", timeout, what)
			return
		}
		time.Sleep(checkInterval)
	}
}
