//go:build unix

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// pipeEnds returns the ends of a new pipe, closed when the test ends.
func pipeEnds(t *testing.T) (reader, writer *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Best effort, as for any file a test is done with.
		_ = reader.Close()
		_ = writer.Close()
	})
	return reader, writer
}

func TestAStreamThatTheSystemFailsSaysWhyAsTheRustVersionDid(t *testing.T) {
	reader, nobodyReads := pipeEnds(t)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Best effort, as for any file a test is done with.
		_ = directory.Close()
	})

	// The test binary's SIGPIPE ends nothing: only its own standard output
	// and error would.
	_, writeErr := processStream{file: nobodyReads}.Write([]byte("lost"))
	_, readErr := processStream{file: directory}.Read(make([]byte, 1))

	tests := []struct {
		name  string
		err   error
		errno syscall.Errno
		want  string
	}{
		{"a pipe nobody reads", writeErr, syscall.EPIPE, fmt.Sprintf("Broken pipe (os error %d)", syscall.EPIPE)},
		{"a directory", readErr, syscall.EISDIR, fmt.Sprintf("Is a directory (os error %d)", syscall.EISDIR)},
	}
	for _, test := range tests {
		if test.err == nil || test.err.Error() != test.want || !errors.Is(test.err, test.errno) {
			t.Errorf("%s: %v, want %q matching the error number", test.name, test.err, test.want)
		}
	}
}

// As Rust's standard library takes EBADF: a standard output opened
// read-only drops what is written to it, and a standard input opened
// write-only has ended (std's handle_ebadf).
func TestAStreamNotOpenForTheDirectionIsAnEmptyInputAndASink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly := openForTheTest(t, path, os.O_RDONLY)
	writeOnly := openForTheTest(t, path, os.O_WRONLY)

	written, writeErr := processStream{file: readOnly}.Write([]byte("dropped"))
	read, readErr := processStream{file: writeOnly}.Read(make([]byte, 8))

	if written != len("dropped") || writeErr != nil {
		t.Errorf("Write to a read-only stream = %d, %v; want all of it taken", written, writeErr)
	}
	if read != 0 || readErr != io.EOF {
		t.Errorf("Read of a write-only stream = %d, %v; want the end of the stream", read, readErr)
	}
}

// openForTheTest opens path with flag, closed when the test ends.
func openForTheTest(t *testing.T, path string, flag int) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, flag, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Best effort, as for any file a test is done with.
		_ = file.Close()
	})
	return file
}

func TestTheEndOfAStreamStaysTheEnd(t *testing.T) {
	reader, writer := pipeEnds(t)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := processStream{file: reader}.Read(make([]byte, 1))

	if err != io.EOF {
		t.Errorf("read %v, want io.EOF itself", err)
	}
}

func TestAFailureWithoutAnErrorNumberKeepsGosText(t *testing.T) {
	_, closed := pipeEnds(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := processStream{file: closed}.Write([]byte("late"))

	if !errors.Is(err, os.ErrClosed) || !strings.HasPrefix(err.Error(), "write ") {
		t.Errorf("wrote %v, want Go's error for a closed file", err)
	}
}
