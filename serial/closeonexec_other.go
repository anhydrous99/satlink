//go:build !(linux || darwin || freebsd || openbsd)

package serial

import serialport "go.bug.st/serial"

// openCloseOnExec opens the port as go.bug.st/serial's Open does: off Unix,
// child processes cannot inherit it. On Windows the library's CreateFile
// gives a handle that is not inheritable, and os/exec hands a child only
// the handles it lists.
func openCloseOnExec(path string, mode *serialport.Mode) (serialport.Port, error) {
	return serialport.Open(path, mode)
}

// descriptorsOf returns none: off Unix the library's ports have handles,
// not descriptors, and it waits for input without select(2).
func descriptorsOf(serialport.Port) []int {
	return nil
}
