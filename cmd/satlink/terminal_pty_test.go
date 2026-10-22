//go:build darwin || linux

package main

import (
	"os"
	"syscall"
	"testing"
)

// pseudoTerminalController is the device that opens a new pseudo-terminal
// and is its controlling side.
const pseudoTerminalController = "/dev/ptmx"

// openPseudoTerminal opens a new pseudo-terminal and returns its terminal
// side; the test's end closes both sides. Neither becomes the test's
// controlling terminal. It skips the test where no pseudo-terminal can be
// opened.
func openPseudoTerminal(t *testing.T) *os.File {
	t.Helper()
	controller := openWithoutControllingIt(t, pseudoTerminalController)
	path, err := unlockedTerminalSide(controller)
	if err != nil {
		t.Skipf("cannot unlock the pseudo-terminal's terminal side: %v", err)
	}
	return openWithoutControllingIt(t, path)
}

// openWithoutControllingIt opens the device at path for reading and
// writing, without making it the test's controlling terminal, until the
// test ends; it skips the test where that fails.
func openWithoutControllingIt(t *testing.T, path string) *os.File {
	t.Helper()
	device, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open a pseudo-terminal: %v", err)
	}
	t.Cleanup(func() {
		// Best effort, as for any file a test is done with.
		_ = device.Close()
	})
	return device
}

func TestAPseudoTerminalIsTakenForATerminal(t *testing.T) {
	terminal := openPseudoTerminal(t)

	if !isTerminal(terminal) {
		t.Errorf("%s is not taken for a terminal", terminal.Name())
	}
}

// Help and errors take styles on the standard streams that are terminals,
// and net tcp waits for someone typing at one: each stream must be said to
// be a terminal when it is one, and only then.
func TestEachStandardStreamIsSaidToBeATerminalOnlyWhenItIsOne(t *testing.T) {
	terminal, null := openPseudoTerminal(t), openNullDevice(t)
	tests := []struct {
		name  string
		files standardFiles
		// areTerminals says which of standard input, output and error are.
		areTerminals [3]bool
	}{
		{"standard input", standardFiles{stdin: terminal, stdout: null, stderr: null}, [3]bool{true, false, false}},
		{"standard output", standardFiles{stdin: null, stdout: terminal, stderr: null}, [3]bool{false, true, false}},
		{"standard error", standardFiles{stdin: null, stdout: null, stderr: terminal}, [3]bool{false, false, true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := environmentOf(nil, test.files, noVariables)

			got := [3]bool{env.stdinIsTerminal, env.stdoutIsTerminal, env.stderrIsTerminal}
			if got != test.areTerminals {
				t.Errorf("standard input, output and error are terminals: %v; want %v", got, test.areTerminals)
			}
		})
	}
}
