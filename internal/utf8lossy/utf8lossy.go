package utf8lossy

import (
	"strings"
	"unicode/utf8"
)

// replacementCharacter stands in for bytes that are not UTF-8: text
// replaces them, never refuses them.
const replacementCharacter = "�"

// String returns data as text, each maximal subpart of an ill-formed
// sequence replaced by U+FFFD.
func String(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var text strings.Builder
	text.Grow(len(data))
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			text.WriteString(replacementCharacter)
			size = maximalSubpart(data)
		} else {
			text.Write(data[:size])
		}
		data = data[size:]
	}
	return text.String()
}

// maximalSubpart returns how many bytes at the start of data, where no
// well-formed sequence starts, one U+FFFD replaces: the start of a
// well-formed sequence that data breaks off or cuts short, or else one
// byte. utf8.FullRune reports such a start as not yet full, as more bytes
// could still complete it, and anything ill-formed as full.
func maximalSubpart(data []byte) int {
	length := 1
	for length < len(data) && !utf8.FullRune(data[:length+1]) {
		length++
	}
	return length
}
