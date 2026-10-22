//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

// ignoreBrokenPipes makes a write to a pipe that nobody reads any more fail
// with EPIPE, as it failed in the Rust version, whose runtime ignores
// SIGPIPE before main. Go's runtime would end the process by SIGPIPE at
// such a write to standard output or error — `satlink net get … | head`
// once head has had enough — in the middle of a call, before it has hung
// up. The failed write takes the paths the CLI has for it instead: the
// command fails once the call is over, and a best-effort write is dropped.
// It must be asked for: Go's runtime replaces a SIGPIPE ignored on entry
// with its own handler. satlink starts no programs that would inherit the
// ignored signal.
func ignoreBrokenPipes() {
	signal.Ignore(syscall.SIGPIPE)
}
