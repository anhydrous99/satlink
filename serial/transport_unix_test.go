//go:build linux || darwin || freebsd || openbsd

package serial

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
)

func TestAReadThatTheLibraryCannotWaitForFailsInsteadOfPanicking(t *testing.T) {
	// The port's descriptor, 1024 (FD_SETSIZE), is the first that
	// select(2)'s sets cannot hold.
	port := &libraryShapedPort{fakePort: newFakePort(), handle: 1024}
	port.readPanic = func() { setInDescriptorSet(port.handle) }
	transport := &Transport{port: port}

	received, err := transport.ReadTimeout(make([]byte, 8), time.Second)

	want := "transport error: the serial library cannot wait for this port (select(2) watches no descriptor from 1024 up): " +
		"runtime error: index out of range [32] with length 32"
	if received != 0 || !errors.Is(err, satlink.ErrTransport) || fmt.Sprint(err) != want {
		t.Errorf("ReadTimeout = %d, %q; want 0, %q", received, err, want)
	}
	var runtimeErr runtime.Error
	if !errors.As(err, &runtimeErr) {
		t.Errorf("%v does not wrap the library's runtime error", err)
	}
	if err := transport.Close(); err != nil || port.closeCount() != 1 {
		t.Errorf("Close = %v after %d port closes; want the port closed", err, port.closeCount())
	}
}

// setInDescriptorSet sets descriptor in a select(2) descriptor set as
// x/sys/unix's FdSet does, which panics for a descriptor beyond its 1024
// bits, as go.bug.st/serial's Unix Read then does.
func setInDescriptorSet(descriptor int) {
	const bitsPerWord = 32
	var set [1024 / bitsPerWord]int32
	set[descriptor/bitsPerWord] |= 1 << (descriptor % bitsPerWord)
}
