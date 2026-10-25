package main

import (
	"context"
	"os"
)

func main() {
	// Before anything is written: a reader that has gone must not end the
	// process in the middle of a call.
	ignoreBrokenPipes()
	status := run(context.Background(), processEnvironment())
	if status == exitInterrupted {
		// The call is over: end as interrupted, so that a shell loop or
		// script around the CLI stops too.
		dieInterrupted()
	}
	os.Exit(status)
}
