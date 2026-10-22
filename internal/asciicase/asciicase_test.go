package asciicase

import "testing"

// Letters beyond ASCII that Unicode maps onto ASCII ones, and bytes that
// are no text at all.
const (
	// longS, U+017F, upper-cases and folds to 'S'.
	longS = "\u017f"
	// dotlessI, U+0131, upper-cases to 'I'.
	dotlessI = "\u0131"
	// kelvinSign, U+212A, lower-cases and folds to 'k'.
	kelvinSign = "\u212a"
	// notUTF8 is two bytes that begin no UTF-8 sequence.
	notUTF8 = "\xff\xfe"
)

func TestEqualFoldIgnoresTheCaseOfASCIILettersAlone(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want bool
	}{
		{"AT+CSQ", "at+csq", true},
		{"Transfer-Encoding", "transfer-ENCODING", true},
		{"", "", true},
		{"AT", "ATD", false},
		{"AT+C" + longS + "Q", "AT+CSQ", false},
		{"AT+C" + kelvinSign + "PD", "AT+CKPD", false},
		{"web." + dotlessI, "web.I", false},
		{"@", "`", false},
		{notUTF8, notUTF8, true},
	}
	for _, test := range tests {
		if got := EqualFold(test.a, test.b); got != test.want {
			t.Errorf("EqualFold(%q, %q) = %v, want %v", test.a, test.b, got, test.want)
		}
	}
}

func TestLowerMapsASCIICapitalsAlone(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"Secure.TEST":            "secure.test",
		"already small, 0-9 _@[": "already small, 0-9 _@[",
		kelvinSign + "B":         kelvinSign + "b",
		"\u00c9COLE":             "\u00c9cole",
		notUTF8:                  notUTF8,
		"":                       "",
	}
	for text, want := range tests {
		if got := Lower(text); got != want {
			t.Errorf("Lower(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestUpperMapsASCIISmallLettersAlone(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"at+cmgs":                "AT+CMGS",
		"ALREADY CAPITAL, 0-9 `": "ALREADY CAPITAL, 0-9 `",
		"at+c" + longS + "q":     "AT+C" + longS + "Q",
		dotlessI + "p":           dotlessI + "P",
		"\u00e9cole":             "\u00e9COLE",
		notUTF8:                  notUTF8,
		"":                       "",
	}
	for text, want := range tests {
		if got := Upper(text); got != want {
			t.Errorf("Upper(%q) = %q, want %q", text, got, want)
		}
	}
}
