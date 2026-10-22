package serial

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY opens a pseudo-terminal, closed again at cleanup, and returns its
// master side and the path of its slave, which stands in for a handset's
// serial device: grant and unlock the slave, then ask for its name.
func openPTY(t *testing.T) (master *os.File, slavePath string) {
	t.Helper()
	master = openPTYMaster(t)
	for _, request := range []uintptr{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK} {
		ioctlPTY(t, master, request, nil)
	}
	var name [128]byte // the size TIOCPTYGNAME writes
	ioctlPTY(t, master, syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0]))
	end := bytes.IndexByte(name[:], 0)
	if end < 0 {
		t.Fatal("the pseudo-terminal's name has no end")
	}
	return master, string(name[:end])
}
