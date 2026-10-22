package sim

import (
	"bytes"
	"time"
)

// Spots the Hayes escape sequence in online-data mode: +++ with a guard
// time of silence before and after it (registers S2 and S12).
//
// Escape characters are held back while they may still turn out to be an
// escape. If they do not — more data follows, or the next + is too slow —
// they are passed on as data after all, so data is never lost.

const (
	// escapeCharacter is the escape character (register S2).
	escapeCharacter = '+'
	// escapeLength is how many escape characters make the sequence.
	escapeLength = 3
)

// guardExpiryKind says what the passing of time decided about held escape
// characters.
type guardExpiryKind int

const (
	// guardPending means nothing is held, or the guard time has not passed
	// yet.
	guardPending guardExpiryKind = iota
	// guardEscape means the escape sequence was followed by silence: back
	// to command mode.
	guardEscape
	// guardReleasedData means the held escape characters turned out to be
	// data.
	guardReleasedData
)

// guardExpiry is what [escapeDetector.expire] decided.
type guardExpiry struct {
	kind guardExpiryKind
	// data are the escape characters that were data after all
	// (guardReleasedData).
	data []byte
}

// escapeDetector watches what the app writes in online-data mode for the
// escape sequence.
type escapeDetector struct {
	guard time.Duration
	// lastInput is when the app last wrote anything, or when online-data
	// mode began.
	lastInput time.Time
	// held counts the escape characters held back, at most escapeLength.
	held int
}

// newEscapeDetector starts watching at now, which counts as the last
// input. A negative guard counts as none: silence is never negative, so
// it would never let the characters of the sequence follow each other.
func newEscapeDetector(guard time.Duration, now time.Time) *escapeDetector {
	return &escapeDetector{guard: max(guard, 0), lastInput: now}
}

// filter takes the bytes the app wrote at now and returns those that are
// data. Call [escapeDetector.expire] first, so that an escape whose closing
// guard time has passed is seen before the bytes that follow it.
func (d *escapeDetector) filter(input []byte, now time.Time) []byte {
	data := make([]byte, 0, len(input)+escapeLength)
	for _, b := range input {
		data = d.accept(b, now, data)
	}
	return data
}

// expire settles held escape characters once the guard time has passed
// without input.
func (d *escapeDetector) expire(now time.Time) guardExpiry {
	if d.held == 0 || d.silenceAt(now) < d.guard {
		return guardExpiry{kind: guardPending}
	}
	held := d.held
	d.held = 0
	if held == escapeLength {
		return guardExpiry{kind: guardEscape}
	}
	return guardExpiry{kind: guardReleasedData, data: escapeCharacters(held)}
}

// accept takes one byte and appends to data what of it, and of the
// characters held so far, is data.
func (d *escapeDetector) accept(b byte, now time.Time, data []byte) []byte {
	silence := d.silenceAt(now)
	// The characters of the sequence follow each other within the guard
	// time.
	extendsSequence := b == escapeCharacter && d.held >= 1 && d.held < escapeLength && silence <= d.guard
	// A sequence only starts after the guard time of silence.
	startsSequence := b == escapeCharacter && silence >= d.guard
	if !extendsSequence {
		data = d.releaseHeld(data)
	}
	if extendsSequence || startsSequence {
		d.held++
	} else {
		data = append(data, b)
	}
	d.lastInput = now
	return data
}

// releaseHeld appends the held escape characters to data, as data.
func (d *escapeDetector) releaseHeld(data []byte) []byte {
	data = append(data, escapeCharacters(d.held)...)
	d.held = 0
	return data
}

// silenceAt returns how long the app has been quiet at now.
func (d *escapeDetector) silenceAt(now time.Time) time.Duration {
	return max(now.Sub(d.lastInput), 0)
}

// escapeCharacters returns count escape characters.
func escapeCharacters(count int) []byte {
	return bytes.Repeat([]byte{escapeCharacter}, count)
}
