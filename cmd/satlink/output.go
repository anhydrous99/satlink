package main

import (
	"fmt"
	"io"
	"sync"
)

// What the commands write. Output that cannot be written fails the command;
// the few writes that are best effort say why where they are made.

// streams are the standard streams a command uses. stderr is safe for
// concurrent use: log lines, the relay's sender and the messages that
// interruptions call for are written from goroutines of their own.
type streams struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	// stdinIsTerminal says whether someone may be typing stdin.
	stdinIsTerminal bool
}

// writef writes one formatted piece of output and returns the write's error.
func writef(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

// printer writes formatted output until a write fails; from then on it
// writes nothing, and err says why. A command prints its lines through one
// and returns err. Its arguments are evaluated all the same, so a line whose
// value must first be asked of the handset is written with writef instead,
// stopping at the first failure (printStatus).
type printer struct {
	w   io.Writer
	err error
}

// printf writes one formatted piece of output, unless an earlier one failed.
func (p *printer) printf(format string, args ...any) {
	if p.err == nil {
		p.err = writef(p.w, format, args...)
	}
}

// printLines writes each of lines on a line of its own.
func printLines(w io.Writer, lines []string) error {
	p := printer{w: w}
	for _, line := range lines {
		p.printf("%s\n", line)
	}
	return p.err
}

// syncWriter serialises the writes to w, so that each line arrives whole
// when several goroutines write.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// Write writes p to the underlying writer while no other Write does.
func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
