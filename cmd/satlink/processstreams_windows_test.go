package main

import (
	"io"
	"os"
	"testing"
)

// ERROR_INVALID_HANDLE ends a standard stream whose handle is NULL, in a
// process without a console: it drops what is written to it and has ended.
func TestAStreamWithoutAHandleIsAnEmptyInputAndASink(t *testing.T) {
	// Such a stream is a File over the NULL handle: Go keeps what
	// GetStdHandle returns.
	missing := processStream{file: os.NewFile(0, "NULL")}

	written, writeErr := missing.Write([]byte("dropped"))
	read, readErr := missing.Read(make([]byte, 8))

	if written != len("dropped") || writeErr != nil {
		t.Errorf("Write to a stream without a handle = %d, %v; want all of it taken", written, writeErr)
	}
	if read != 0 || readErr != io.EOF {
		t.Errorf("Read of a stream without a handle = %d, %v; want the end of the stream", read, readErr)
	}
}
