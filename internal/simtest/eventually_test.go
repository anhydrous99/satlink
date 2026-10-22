package simtest

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestEventuallyReturnsOnceTheConditionHolds(t *testing.T) {
	t.Parallel()
	var checks atomic.Int32

	Eventually(t, 5*time.Second, "the third check", func() bool {
		return checks.Add(1) == 3
	})

	if checks.Load() != 3 {
		t.Errorf("checked %d times; want 3", checks.Load())
	}
}

func TestEventuallyFailsWithWhatItAwaitedAfterTheTimeout(t *testing.T) {
	t.Parallel()
	recorder := &failureRecorder{TB: t}
	start := time.Now()

	Eventually(recorder, 30*time.Millisecond, "the handset is free", func() bool { return false })

	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("gave up after %v; want at least the 30ms timeout", elapsed)
	}
	want := "timed out after 30ms waiting until the handset is free"
	if len(recorder.failures) != 1 || recorder.failures[0] != want {
		t.Errorf("failures %q; want [%q]", recorder.failures, want)
	}
}

func TestEventuallyChecksAtLeastOnceEvenWithoutTime(t *testing.T) {
	t.Parallel()
	recorder := &failureRecorder{TB: t}

	Eventually(recorder, 0, "it holds", func() bool { return true })

	if len(recorder.failures) != 0 {
		t.Errorf("failed with %q though the condition held", recorder.failures)
	}
}
