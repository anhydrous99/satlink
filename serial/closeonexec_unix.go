//go:build linux || darwin || freebsd || openbsd

package serial

import (
	"reflect"
	"syscall"

	serialport "go.bug.st/serial"
)

// openCloseOnExec opens the port as go.bug.st/serial's Open does, then marks
// its descriptors close-on-exec (O_CLOEXEC). The library opens without it,
// and so every child process
// started while the port is open — through os/exec, say — would inherit it:
// a tty hangs up, dropping DTR, only on its last close, so a call would stay
// up, billing, as long as such a child lives, Close and a crash of the
// program notwithstanding.
//
// Forks take syscall.ForkLock for writing, so holding it for reading keeps a
// child from starting between the library's open(2) and the marking. That
// delays an exec in another goroutine until the open is done, which does not
// wait for carrier (O_NDELAY). The library's Open must not take the lock
// itself, as a second read lock while a fork waits would deadlock: v1.8.0
// creates its descriptors with x/sys/unix's Open and Pipe, which do not.
func openCloseOnExec(path string, mode *serialport.Mode) (serialport.Port, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	port, err := serialport.Open(path, mode)
	if err != nil {
		return nil, err
	}
	for _, descriptor := range descriptorsOf(port) {
		syscall.CloseOnExec(descriptor)
	}
	return port, nil
}

// descriptorsOf returns the descriptors behind port, a Unix port of
// go.bug.st/serial v1.8.0: the tty, then both ends of the pipe that wakes a
// read when the port closes. The library keeps them in unexported fields,
// read here by reflection; a port of another shape gives none, never a
// panic, and the pty tests of close-on-exec and of select(2)'s limit then
// fail.
func descriptorsOf(port serialport.Port) []int {
	unixPort, ok := structBehind(reflect.ValueOf(port))
	if !ok {
		return nil
	}
	descriptors := intFields(unixPort, "handle")
	if closeSignal, ok := structBehind(unixPort.FieldByName("closeSignal")); ok {
		descriptors = append(descriptors, intFields(closeSignal, "rd", "wr")...)
	}
	return descriptors
}

// structBehind returns the struct that value points to, and whether it
// points to one.
func structBehind(value reflect.Value) (reflect.Value, bool) {
	if value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	return value.Elem(), true
}

// intFields returns the values of structure's int fields called names, in
// order, leaving out a name that is no such field.
func intFields(structure reflect.Value, names ...string) []int {
	var values []int
	for _, name := range names {
		if field := structure.FieldByName(name); field.Kind() == reflect.Int {
			values = append(values, int(field.Int()))
		}
	}
	return values
}
