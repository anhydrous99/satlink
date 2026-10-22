package serial

import (
	"fmt"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY opens a pseudo-terminal, closed again at cleanup, and returns its
// master side and the path of its slave, which stands in for a handset's
// serial device: unlock the slave, then ask for its number.
func openPTY(t *testing.T) (master *os.File, slavePath string) {
	t.Helper()
	master = openPTYMaster(t)
	var unlocked int32
	ioctlPTY(t, master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlocked))
	var number uint32
	ioctlPTY(t, master, syscall.TIOCGPTN, unsafe.Pointer(&number))
	return master, fmt.Sprintf("/dev/pts/%d", number)
}
