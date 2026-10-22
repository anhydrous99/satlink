//go:build darwin || linux

package serial

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/anhydrous99/satlink/iridium"
)

// The serial library on a real tty, a pseudo-terminal's slave
// standing in for the handset's device. Its master side sees the slave's
// last close, which is when the line hangs up and DTR drops.

// selectDescriptorLimit is FD_SETSIZE: select(2)'s descriptor sets hold the
// descriptors below it.
const selectDescriptorLimit = 1024

// hangUpWithin bounds how long the line takes to hang up once the port's
// last holder has closed it: no time at all.
const hangUpWithin = 5 * time.Second

func TestTheSerialPortsDescriptorsAreClosedOnExec(t *testing.T) {
	_, slave := openPTY(t)
	transport := openTransport(t, slave)

	descriptors := descriptorsOf(transport.port)

	if len(descriptors) != 3 {
		t.Fatalf("descriptors %v, want the port's and both of its close pipe's: has the serial library changed?", descriptors)
	}
	for _, descriptor := range descriptors {
		if !isCloseOnExec(t, descriptor) {
			t.Errorf("descriptor %d is not closed on exec: child processes inherit it", descriptor)
		}
	}
}

func TestAChildProcessDoesNotKeepTheLineUpOnceThePortCloses(t *testing.T) {
	master, slave := openPTY(t)
	transport := openTransport(t, slave)
	startChildProcess(t)
	hungUp := hangUpOf(master)

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-hungUp:
	case <-time.After(hangUpWithin):
		t.Fatal("the line did not hang up when the port closed: the child process holds the port")
	}
}

func TestAnEmptyReadOfAPortWithInputWaitingReadsNothing(t *testing.T) {
	// The library's Unix Read takes the nothing that read(2) gives an empty
	// buffer for a port that hung up: "Port has been closed".
	master, slave := openPTY(t)
	transport := openTransport(t, slave)
	if _, err := master.WriteString("OK\r\n"); err != nil {
		t.Fatalf("writing to the pseudo-terminal: %v", err)
	}

	received, err := transport.ReadTimeout(nil, time.Second)

	if received != 0 || err != nil {
		t.Errorf("ReadTimeout(nil) = %d, %v; want 0, nil: the port is open", received, err)
	}
	buf := make([]byte, 16)
	received, err = transport.ReadTimeout(buf, time.Second)
	if err != nil || string(buf[:received]) != "OK\r\n" {
		t.Errorf("ReadTimeout = %q, %v; want the input left waiting", buf[:received], err)
	}
}

func TestAPortBeyondSelectsDescriptorsFailsItsReadsInsteadOfPanicking(t *testing.T) {
	_, slave := openPTY(t)
	occupyDescriptorsBelow(t, selectDescriptorLimit)
	transport := openTransport(t, slave)

	_, err := transport.ReadTimeout(make([]byte, 8), time.Millisecond)

	if !errors.Is(err, iridium.ErrTransport) || !strings.Contains(fmt.Sprint(err), "select(2)") {
		t.Errorf("ReadTimeout = %v, want the transport error of a port beyond select(2)'s descriptors", err)
	}
}

// openPTYMaster opens the master side of a new pseudo-terminal, closed again
// at cleanup, skipping the test where there are none.
func openPTYMaster(t *testing.T) *os.File {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	t.Cleanup(func() {
		// The test is over; a failure to close changes nothing it checked.
		_ = master.Close()
	})
	return master
}

// ioctlPTY makes the ioctl(2) request of master, with arg, failing the test
// if it fails.
func ioctlPTY(t *testing.T, master *os.File, request uintptr, arg unsafe.Pointer) {
	t.Helper()
	raw, err := master.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var errno syscall.Errno
	err = raw.Control(func(descriptor uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, descriptor, request, uintptr(arg))
	})
	if err != nil || errno != 0 {
		t.Fatalf("ioctl %#x of the pseudo-terminal: %v, %v", request, err, errno)
	}
}

// openTransport opens the serial device at path, closed again at cleanup.
func openTransport(t *testing.T, path string) *Transport {
	t.Helper()
	transport, err := Open(path, DefaultBaud)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() {
		// Idempotent; the tests that close it check the result themselves.
		_ = transport.Close()
	})
	return transport
}

// isCloseOnExec reports whether descriptor is closed on exec.
func isCloseOnExec(t *testing.T, descriptor int) bool {
	t.Helper()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(descriptor), syscall.F_GETFD, 0)
	if errno != 0 {
		t.Fatalf("fcntl(%d, F_GETFD): %v", descriptor, errno)
	}
	return flags&syscall.FD_CLOEXEC != 0
}

// startChildProcess starts a process that lives until the test ends, as a
// helper an app starts would, skipping the test where there is none to
// start.
func startChildProcess(t *testing.T) {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep command to start: %v", err)
	}
	child := exec.Command(sleep, "60")
	if err := child.Start(); err != nil {
		t.Fatalf("starting %s: %v", sleep, err)
	}
	t.Cleanup(func() {
		// Killed, the child reports that it was: nothing to check.
		_ = child.Process.Kill()
		_ = child.Wait()
	})
}

// hangUpOf returns a channel closed once the slave side of master hangs up:
// once its last holder closes it, a read of the master reports the end of
// the file (macOS) or EIO (Linux).
func hangUpOf(master *os.File) <-chan struct{} {
	hungUp := make(chan struct{})
	go func() {
		defer close(hungUp)
		buf := make([]byte, 64)
		for {
			if _, err := master.Read(buf); err != nil {
				return
			}
		}
	}()
	return hungUp
}

// occupyDescriptorsBelow opens /dev/null until every descriptor below lowest
// is in use, so that the next file opened gets lowest or more; they are
// closed again at cleanup. It skips the test where the process may not
// open that many files.
func occupyDescriptorsBelow(t *testing.T, lowest int) {
	t.Helper()
	const headroom = 64 // for the port, its pipe and the test's own files
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil || uint64(limit.Cur) < uint64(lowest+headroom) {
		t.Skipf("the process may not open %d files: %+v, %v", lowest+headroom, limit, err)
	}
	var files []*os.File
	t.Cleanup(func() {
		for _, file := range files {
			// Only held to take up descriptors.
			_ = file.Close()
		}
	})
	for {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatalf("opening %s: %v", os.DevNull, err)
		}
		files = append(files, file)
		if int(file.Fd()) >= lowest-1 {
			return
		}
	}
}
