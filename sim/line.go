package sim

import "github.com/anhydrous99/satlink/internal/utf8lossy"

// Assembles command-mode input into command lines.

const (
	// carriageReturn ends a command line (register S3).
	carriageReturn = '\r'
	// lineFeed is what many terminals send after CR; it is never part of a
	// command.
	lineFeed = '\n'
	// maxLineLen is the longest command line kept. Real handsets cap their
	// command buffer too, and the cap stops binary data sent in command mode
	// from growing it without end.
	maxLineLen = 512
)

// line is a command line, finished by CR: its text, or tooLong for a line
// longer than the handset accepts.
type line struct {
	text    string
	tooLong bool
}

// lineAssembler collects bytes until a carriage return completes a line.
// The zero lineAssembler is empty and ready.
type lineAssembler struct {
	bytes      []byte
	overflowed bool
}

// push takes one byte and returns the line it completes, if any.
func (a *lineAssembler) push(b byte) (line, bool) {
	switch {
	case b == carriageReturn:
		return a.finish(), true
	case b == lineFeed:
	case len(a.bytes) < maxLineLen:
		a.bytes = append(a.bytes, b)
	default:
		a.overflowed = true
	}
	return line{}, false
}

// finish returns the line collected so far and starts the next.
func (a *lineAssembler) finish() line {
	finished := line{tooLong: a.overflowed}
	if !finished.tooLong {
		finished.text = utf8lossy.String(a.bytes)
	}
	a.bytes, a.overflowed = a.bytes[:0], false
	return finished
}
