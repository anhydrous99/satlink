//go:build unix

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// signalWait is how long a signal sent to this process may take to be
// relayed.
const signalWait = 10 * time.Second

func TestTheProcessesInterruptionsAreRelayedInsteadOfEndingIt(t *testing.T) {
	signals := make(chan os.Signal, 1)
	stop := notifyInterruptions(signals)
	defer stop()

	// Were it not relayed, it would end the test binary.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-signals:
		if got != syscall.SIGTERM {
			t.Errorf("relayed %v, want SIGTERM", got)
		}
	case <-time.After(signalWait):
		t.Fatalf("SIGTERM was not relayed within %v", signalWait)
	}
}
