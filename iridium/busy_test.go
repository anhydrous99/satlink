package iridium

import (
	"bytes"
	"errors"
	"runtime"
	"testing"
	"time"
)

// The second busy check, once a method has the lock.

func TestAMethodThatWaitedForTheLockWhileADialClaimedTheHandsetIsBusy(t *testing.T) {
	// It passed the first check before the dial claimed the handset, then
	// waited for the lock while the dial connected: its command must not go
	// into the call.
	transport := &silentTransport{}
	handset := NewHandset(transport)
	handset.SetTimeout(100 * time.Millisecond)
	handset.mu.Lock()
	commandEnded := make(chan error, 1)
	go func() {
		_, err := handset.Command(attentionCommand)
		commandEnded <- err
	}()
	awaitWaitingForTheLock(t)

	// What a dial that connected leaves: the claim, and the lock let go.
	if err := handset.claim(); err != nil {
		t.Fatalf("claim: %v", err)
	}
	t.Cleanup(handset.release)
	handset.mu.Unlock()

	select {
	case err := <-commandEnded:
		if !errors.Is(err, ErrBusy) {
			t.Errorf("Command = %v, want ErrBusy", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command did not end")
	}
	if len(transport.written) != 0 {
		t.Errorf("written %q into the data call", transport.written)
	}
}

// awaitWaitingForTheLock returns once a goroutine the test started waits
// for a handset's lock, failing the test if none does within a few
// seconds. Only the goroutines' stacks show it: started and checked at
// once, the method would mostly not have reached the lock, and the test
// would prove nothing.
func awaitWaitingForTheLock(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !isWaitingForTheLock(goroutineStacks(), "created by github.com/anhydrous99/satlink/iridium."+t.Name()+" ") {
		if time.Now().After(deadline) {
			t.Fatal("no goroutine of the test waits for the handset's lock")
		}
		time.Sleep(time.Millisecond)
	}
}

// isWaitingForTheLock reports whether stacks, every goroutine's, show one
// whose creation line starts with creator waiting in Handset.lock for its
// mutex.
func isWaitingForTheLock(stacks []byte, creator string) bool {
	for goroutine := range bytes.SplitSeq(stacks, []byte("\n\n")) {
		if bytes.Contains(goroutine, []byte("[sync.Mutex.Lock")) &&
			bytes.Contains(goroutine, []byte("iridium.(*Handset).lock(")) &&
			bytes.Contains(goroutine, []byte(creator)) {
			return true
		}
	}
	return false
}

// goroutineStacks returns the stacks of every goroutine.
func goroutineStacks() []byte {
	buf := make([]byte, 1<<16)
	for {
		written := runtime.Stack(buf, true)
		if written < len(buf) {
			return buf[:written]
		}
		buf = make([]byte, 2*len(buf))
	}
}
