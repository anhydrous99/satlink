//go:build darwin || freebsd || openbsd

package main

import "syscall"

// readTerminalSettings is the ioctl request that reads a terminal's
// settings, and fails on any other file: TIOCGETA, as the BSD libcs'
// isatty uses.
const readTerminalSettings = syscall.TIOCGETA
