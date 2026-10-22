package main

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// terminalSides is the directory of the pseudo-terminals' terminal sides,
// by number.
const terminalSides = "/dev/pts/"

// unlockedTerminalSide makes the terminal side of the pseudo-terminal that
// controller controls ready to open, as unlockpt(3) does, and returns its
// path, as ptsname(3) does.
func unlockedTerminalSide(controller *os.File) (string, error) {
	var locked int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, controller.Fd(), syscall.TIOCSPTLCK,
		uintptr(unsafe.Pointer(&locked))); errno != 0 {
		return "", errno
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, controller.Fd(), syscall.TIOCGPTN,
		uintptr(unsafe.Pointer(&number))); errno != 0 {
		return "", errno
	}
	return terminalSides + strconv.FormatUint(uint64(number), 10), nil
}
