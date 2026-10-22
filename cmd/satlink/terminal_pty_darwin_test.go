package main

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

// terminalSideNameSize is the size of the buffer that TIOCPTYGNAME writes
// the terminal side's path to.
const terminalSideNameSize = 128

// unlockedTerminalSide makes the terminal side of the pseudo-terminal that
// controller controls ready to open, as grantpt(3) and unlockpt(3) do, and
// returns its path, as ptsname(3) does.
func unlockedTerminalSide(controller *os.File) (string, error) {
	for _, request := range []uintptr{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, controller.Fd(), request, 0); errno != 0 {
			return "", errno
		}
	}
	var name [terminalSideNameSize]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, controller.Fd(), syscall.TIOCPTYGNAME,
		uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		return "", errno
	}
	path, _, _ := bytes.Cut(name[:], []byte{0})
	return string(path), nil
}
