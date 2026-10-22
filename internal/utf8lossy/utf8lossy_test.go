package utf8lossy

import "testing"

func TestEachMaximalSubpartOfAnIllFormedSequenceBecomesOneReplacement(t *testing.T) {
	t.Parallel()
	const r = replacementCharacter
	tests := []struct {
		name  string
		bytes string
		want  string
	}{
		{name: "valid text", bytes: "AT+CSQ ✓", want: "AT+CSQ ✓"},
		{name: "nothing", bytes: "", want: ""},
		{name: "a stray byte", bytes: "AT\xff", want: "AT" + r},
		{name: "two stray bytes", bytes: "\xff\xfe", want: r + r},
		{name: "two Latin-1 letters", bytes: "caf\xe9\xe9", want: "caf" + r + r},
		{name: "a truncated sequence", bytes: "\xe2\x82", want: r},
		{name: "a truncated sequence and a stray byte", bytes: "\xe2\x82\xff", want: r + r},
		{name: "a sequence broken off by ASCII", bytes: "\xe2\x82A\xffB", want: r + "A" + r + "B"},
		{name: "a truncated four-byte sequence", bytes: "\xf0\x90\x80", want: r},
		{name: "a four-byte sequence broken off by ASCII", bytes: "\xf0\x90\x80A", want: r + "A"},
		{name: "a surrogate", bytes: "\xed\xa0\x80", want: r + r + r},
		{name: "an overlong form", bytes: "\xc0\xaf", want: r + r},
		{name: "an overlong three-byte form", bytes: "\xe0\x80\xaf", want: r + r + r},
		{name: "beyond U+10FFFF", bytes: "\xf4\x90\x80\x80", want: r + r + r + r},
		{name: "a literal replacement character", bytes: r, want: r},
		{name: "valid text around an emoji", bytes: "ok\xf0\x9f\x98\x80!", want: "ok\U0001F600!"},
		// Unicode's own example (Table 3-8).
		{name: "Unicode's example", bytes: "\x61\xF1\x80\x80\xE1\x80\xC2\x62\x80\x63\x80\xBF\x64",
			want: "a" + r + r + r + "b" + r + "c" + r + r + "d"},
	}
	for _, test := range tests {
		if got := String([]byte(test.bytes)); got != test.want {
			t.Errorf("%s: String(%q) = %q, want %q", test.name, test.bytes, got, test.want)
		}
	}
}
