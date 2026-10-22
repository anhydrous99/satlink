package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	// Before anything is written: a reader that has gone must not end the
	// process in the middle of a call.
	ignoreBrokenPipes()
	// satlink runs whatever started it, Windows' Explorer included, as a shortcut
	// with arguments does; cobra would refuse to run then.
	cobra.MousetrapHelpText = ""
	status := run(context.Background(), processEnvironment())
	if status == exitInterrupted {
		// The call is over: end as interrupted, so that a shell loop or
		// script around the CLI stops too.
		dieInterrupted()
	}
	os.Exit(status)
}
