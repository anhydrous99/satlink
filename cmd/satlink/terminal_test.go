package main

import (
	"os"
	"testing"
)

// openNullDevice opens the null device, a character device that is no
// terminal, until the test ends.
func openNullDevice(t *testing.T) *os.File {
	t.Helper()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Best effort: a file closed before only says so.
		_ = null.Close()
	})
	return null
}

func TestFilesThatAreNoTerminalsAreNotTakenForOne(t *testing.T) {
	reader, pipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Best effort, as for any file a test is done with.
		_ = reader.Close()
		_ = pipe.Close()
	})
	closed := openNullDevice(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	for name, file := range map[string]*os.File{"a pipe": pipe, "the null device": openNullDevice(t), "a closed file": closed} {
		if isTerminal(file) {
			t.Errorf("%s is taken for a terminal", name)
		}
	}
}
