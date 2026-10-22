package main

import (
	"errors"
	"io"
	"os"
	"strconv"
	"syscall"

	"github.com/anhydrous99/satlink/internal/errnotext"
)

// The process's standard streams, whose failures read as
// "error: Broken pipe (os error 32)" once standard output is
// a pipe that nobody reads any more, where Go would write "write
// /dev/stdout: broken pipe".

// processStream is one of the process's standard streams. A read or write
// that the operating system fails with an error number returns it as an
// osError; the end of the stream stays io.EOF. A failure with badDescriptor —
// on Unix, a descriptor not open
// for the direction, such as a standard output opened read-only; on
// Windows, a NULL handle — reads as the end of the stream and takes every
// write: standard input then ends there, and output is dropped without an
// error.
type processStream struct {
	file *os.File
}

// Read reads from the stream.
func (s processStream) Read(p []byte) (int, error) {
	count, err := s.file.Read(p)
	if isBadDescriptor(err) {
		return count, io.EOF
	}
	return count, withOSErrorText(err)
}

// Write writes to the stream.
func (s processStream) Write(p []byte) (int, error) {
	count, err := s.file.Write(p)
	if isBadDescriptor(err) {
		return len(p), nil
	}
	return count, withOSErrorText(err)
}

// isBadDescriptor reports whether err is badDescriptor, which makes a standard
// stream an empty input and a sink.
func isBadDescriptor(err error) bool {
	return errors.Is(err, badDescriptor)
}

// withOSErrorText returns err as an osError when an error number from the
// operating system is why a read or write of a file failed, and err itself
// otherwise.
func withOSErrorText(err error) error {
	var failure *os.PathError
	var errno syscall.Errno
	if errors.As(err, &failure) && errors.As(failure.Err, &errno) {
		return osError{errno: errno}
	}
	return err
}

// osError is an error number from the operating system, written as its
// message: the C library's text, which is Go's with a capital
// letter, and the number — "Broken pipe (os error 32)".
type osError struct {
	errno syscall.Errno
}

// Error returns the error number's text and the number.
func (e osError) Error() string {
	return errnotext.Text(e.errno) + " (os error " + strconv.Itoa(int(e.errno)) + ")"
}

// Unwrap returns the error number, so that errors.Is matches it and the
// io/fs errors it stands for.
func (e osError) Unwrap() error {
	return e.errno
}
