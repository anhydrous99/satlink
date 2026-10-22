package errnotext

import (
	"syscall"
	"unicode"
	"unicode/utf8"
)

// Text returns errno's text as the C library writes it: Go's, with a
// capital first letter.
func Text(errno syscall.Errno) string {
	text := errno.Error()
	first, size := utf8.DecodeRuneInString(text)
	return string(unicode.ToUpper(first)) + text[size:]
}
