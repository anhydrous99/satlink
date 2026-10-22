package main

import (
	"os"
	"syscall"
)

// isTerminal reports whether file is a console, which has a console mode
// to read, as the Rust version's is_terminal asked. Unlike Rust's, it does
// not take an MSYS or Cygwin terminal, a named pipe, for a console.
func isTerminal(file *os.File) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(file.Fd()), &mode) == nil
}
