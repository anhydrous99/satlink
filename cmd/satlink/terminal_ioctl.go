//go:build darwin || freebsd || linux || openbsd

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// The Unix platforms the CLI builds for: those that go.bug.st/serial
// supports. terminal_windows.go has Windows.

// isTerminal reports whether file is a terminal, as isatty(3) does: whether
// the file has terminal settings to read. /dev/null and other character devices
// have none.
func isTerminal(file *os.File) bool {
	connection, err := file.SyscallConn()
	if err != nil {
		return false
	}
	hasSettings := false
	// Control hands over the descriptor without making it blocking, which
	// File.Fd would.
	err = connection.Control(func(descriptor uintptr) {
		var settings syscall.Termios
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, descriptor, readTerminalSettings,
			uintptr(unsafe.Pointer(&settings)))
		hasSettings = errno == 0
	})
	return err == nil && hasSettings
}
