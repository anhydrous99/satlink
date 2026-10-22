package rustdebug

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Quote returns text as Rust's {:?} of a str writes it, the form in which
// the Rust version's messages quote a command, a phone number, a host name,
// a rule or a URL. The text is put in double quotes, and \0, \t, \r, \n, \\
// and \" are escaped with a backslash. A character is written as \u{…}, its
// code point in lower-case hexadecimal without leading zeros, when it is
// not printable — a control or format character, a separator other than
// the space, a private-use or unassigned code point — when it extends a
// grapheme, as a combining accent does, or when it is default-ignorable,
// as a Hangul filler is, a letter that shows nothing. Every other character
// is written as it is, and so are the two halfwidth katakana sound marks,
// though they extend a grapheme.
//
// Go's %q differs for the characters that cannot be shown as they are:
// it writes "\v" where Rust writes "\u{b}", "\x1b" for "\u{1b}", "\u0085"
// for "\u{85}" and "\x00" for "\0", and it shows the marks that extend a
// grapheme and the Hangul fillers. A byte that is not part of valid UTF-8,
// which a Rust str cannot hold, is written \xNN in lower-case hexadecimal,
// as Rust's u8::escape_ascii writes a byte (an OsStr's Debug writes \xFF).
//
// These are the rules of char::escape_debug_ext in rustc 1.99.1, with the
// Unicode properties taken from Go's tables.
func Quote(text string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for len(text) > 0 {
		character, size := utf8.DecodeRuneInString(text)
		if character == utf8.RuneError && size == 1 {
			fmt.Fprintf(&quoted, `\x%02x`, text[0])
		} else {
			writeEscaped(&quoted, character)
		}
		text = text[size:]
	}
	quoted.WriteByte('"')
	return quoted.String()
}

// backslashEscapes are the characters Rust's Debug escapes with a
// backslash and a character of their own.
var backslashEscapes = map[rune]string{
	0:    `\0`,
	'\t': `\t`,
	'\r': `\r`,
	'\n': `\n`,
	'\\': `\\`,
	'"':  `\"`,
}

// The halfwidth katakana voiced and semi-voiced sound marks extend a
// grapheme, but Rust's Debug writes them as they are.
const (
	halfwidthVoicedSoundMark     = '\uff9e'
	halfwidthSemiVoicedSoundMark = '\uff9f'
)

// writeEscaped writes character to quoted as Rust's Debug writes it inside
// a str.
func writeEscaped(quoted *strings.Builder, character rune) {
	if escape, ok := backslashEscapes[character]; ok {
		quoted.WriteString(escape)
		return
	}
	if isWrittenAsCodePoint(character) {
		fmt.Fprintf(quoted, `\u{%x}`, character)
		return
	}
	quoted.WriteRune(character)
}

// isWrittenAsCodePoint reports whether Rust's Debug writes character as
// \u{…}: when it is not printable, extends a grapheme or is
// default-ignorable, but for the halfwidth katakana sound marks. Go's
// tables have no Default_Ignorable_Code_Point; Unicode derives it from the
// format characters, which are not printable, the variation selectors,
// which extend a grapheme, and Other_Default_Ignorable_Code_Point, which
// holds the rest, such as the Hangul fillers.
func isWrittenAsCodePoint(character rune) bool {
	if character == halfwidthVoicedSoundMark || character == halfwidthSemiVoicedSoundMark {
		return false
	}
	return !unicode.IsPrint(character) || extendsAGrapheme(character) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, character)
}

// extendsAGrapheme reports whether character has Unicode's Grapheme_Extend
// property: the enclosing and non-spacing marks, and the other characters
// that Unicode counts with them. Rust's Debug escapes them wherever they
// stand, but for the halfwidth katakana sound marks (see
// isWrittenAsCodePoint).
func extendsAGrapheme(character rune) bool {
	return unicode.In(character, unicode.Mn, unicode.Me, unicode.Other_Grapheme_Extend)
}
