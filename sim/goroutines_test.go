package sim

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// collectorPatience bounds the wait for what the garbage collector sets off:
// goroutines that end once their connections are closed, runtime cleanups.
const collectorPatience = 10 * time.Second

// AwaitGoroutines waits until no more goroutines run than at baseline,
// failing the test if they do not end in time. It runs the garbage
// collector meanwhile, so that runtime cleanups run too. It is exported,
// in a test file, for the external tests as well.
//
// runtime.NumGoroutine counts the goroutines of the whole test binary:
// the tests that use it must not run in parallel.
func AwaitGoroutines(t testing.TB, baseline int) {
	t.Helper()
	awaitAfterCollecting(t, func() bool { return runtime.NumGoroutine() <= baseline }, func() string {
		return fmt.Sprintf("%d goroutines still run; want %d", runtime.NumGoroutine(), baseline)
	})
}

// awaitAfterCollecting runs the garbage collector, and with it the runtime
// cleanups, until condition holds, failing the test with the text failure
// returns if it does not within collectorPatience.
func awaitAfterCollecting(t testing.TB, condition func() bool, failure func() string) {
	t.Helper()
	deadline := time.Now().Add(collectorPatience)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(failure())
		}
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
}
