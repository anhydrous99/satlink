//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// sigintDeliveryGrace is how long the process waits to die of the SIGINT
// it sent itself before ending with exitInterrupted instead: the signal
// arrives at once unless it is ignored or blocked.
const sigintDeliveryGrace = time.Second

// interruptionSignals are the signals that interrupt a net call: Ctrl-C,
// SIGTERM and SIGHUP.
var interruptionSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// dieInterrupted ends the process as an unhandled Ctrl-C does: a shell loop
// or script around the CLI stops only for a command that died of SIGINT,
// and would otherwise carry on and dial again. Standard output is not
// buffered, so dying by a signal loses none of it.
func dieInterrupted() {
	// Go's runtime ends the process by a SIGINT that nobody catches.
	signal.Reset(syscall.SIGINT)
	// Best effort: the exit status below says the same.
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	time.Sleep(sigintDeliveryGrace)
	// Where SIGINT is blocked, or ignored: the process was started with it
	// ignored, which Go's runtime keeps.
	os.Exit(exitInterrupted)
}
