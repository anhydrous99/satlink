package debugtext_test

import (
	"testing"

	"github.com/anhydrous99/satlink/internal/debugtext"
)

// Each want is the quoted text the quoting rules give for the string.
func TestQuoteWritesStringsInDebugStyle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want string
	}{
		{"nothing", "", `""`},
		{"printable ASCII", "AT+CSQ", `"AT+CSQ"`},
		{"the space", "a b", `"a b"`},
		{"the characters escaped with a backslash", "\x00\t\r\n\\\"", `"\0\t\r\n\\\""`},
		{"a single quote, which is left alone", "it's", `"it's"`},
		// Go's %q writes these "\a", "\v", "\f", "\x1b" and "\x7f".
		{"other control characters", "\a\v\f\x1b\x7f", `"\u{7}\u{b}\u{c}\u{1b}\u{7f}"`},
		{"a vertical tab in a URL", "http://web.test/a\vb", `"http://web.test/a\u{b}b"`},
		{"C1 control characters", "\u0085\u009c", `"\u{85}\u{9c}"`},
		{"separators other than the space", "\u00a0\u2028\u2029\u3000", `"\u{a0}\u{2028}\u{2029}\u{3000}"`},
		{"format characters", "\u200b\u200d\ufeff\u00ad", `"\u{200b}\u{200d}\u{feff}\u{ad}"`},
		{"a private-use character", "\ue000", `"\u{e000}"`},
		{"an unassigned code point", "\U000e0fff", `"\u{e0fff}"`},
		{"combining marks, wherever they stand", "\u0301a\u0301\u20dd", `"\u{301}a\u{301}\u{20dd}"`},
		{"a spacing mark that extends a grapheme", "\u09be", `"\u{9be}"`},
		{"letters and symbols beyond ASCII", "\u00e9\u2713\u011c\U0001f600", "\"\u00e9\u2713\u011c\U0001f600\""},
		{"the replacement character, which a lossy line holds", "a\ufffdb", "\"a\ufffdb\""},
		{"default-ignorable letters: the Hangul fillers", "\u115f\u1160\u3164\uffa0", `"\u{115f}\u{1160}\u{3164}\u{ffa0}"`},
		{"the halfwidth katakana sound marks, shown though they extend a grapheme", "\uff76\uff9e\uff8a\uff9f", "\"\uff76\uff9e\uff8a\uff9f\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := debugtext.Quote(test.text); got != test.want {
				t.Errorf("Quote(%q) = %s, want %s", test.text, got, test.want)
			}
		})
	}
}

// Quote writes a byte that is not part of valid UTF-8 as \xNN, in lower
// case, while the replacement character before it, which is valid UTF-8,
// is written as it is. The text after the byte is still quoted: its
// vertical tab is written \u{b}.
func TestQuoteWritesAByteThatIsNotUTF8AsAnEscape(t *testing.T) {
	t.Parallel()
	text, want := "a\ufffdb\xff\v", "\"a\ufffdb\\xff\\u{b}\""
	if got := debugtext.Quote(text); got != want {
		t.Errorf("Quote(%q) = %s, want %s", text, got, want)
	}
}
