package main

import (
	"os"
	"syscall"
)

// interruptionSignals are the signals that interrupt a net call on
// Windows, which has no SIGHUP: os.Interrupt is Ctrl-C or Ctrl-Break, and
// syscall.SIGTERM closing the console window, logging off or shutting down,
// after which Windows ends the process a few seconds later whatever it is
// doing.
var interruptionSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// dieInterrupted ends the process with the exit status of one interrupted:
// Windows has no SIGINT to end by.
func dieInterrupted() {
	os.Exit(exitInterrupted)
}
