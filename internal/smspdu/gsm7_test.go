package smspdu

import (
	"bytes"
	"testing"
)

func TestTheDefaultAlphabetHasOneCharacterPerSeptet(t *testing.T) {
	if len(defaultCharacters) != 128 {
		t.Fatalf("the default alphabet has %d characters, want 128", len(defaultCharacters))
	}
	// Spot checks against TS 23.038 §6.2.1.
	for septet, want := range map[byte]rune{0x00: '@', 0x02: '$', 0x0A: '\n', 0x10: 'Δ', 0x20: ' ', 0x40: '¡', 0x41: 'A', 0x5F: '§', 0x60: '¿', 0x7F: 'à'} {
		if got := defaultCharacters[septet]; got != want {
			t.Errorf("septet %#02x is %q, want %q", septet, got, want)
		}
	}
}

func TestEveryCharacterOfTheAlphabetSurvivesARoundTrip(t *testing.T) {
	var text []rune
	for septet, character := range defaultCharacters {
		if septet != escapeToExtension {
			text = append(text, character)
		}
	}
	for _, character := range extensionTable {
		text = append(text, character)
	}

	septets, ok := gsm7Septets(string(text))

	if !ok {
		t.Fatal("the alphabet's own characters were refused")
	}
	if got := gsm7Text(septets); got != string(text) {
		t.Errorf("round trip gave %q, want %q", got, string(text))
	}
}

func TestExtensionCharactersTakeTwoSeptets(t *testing.T) {
	septets, ok := gsm7Septets("€[]")

	if want := []byte{0x1B, 0x65, 0x1B, 0x3C, 0x1B, 0x3E}; !ok || !bytes.Equal(septets, want) {
		t.Errorf("gsm7Septets = % x, %v; want % x", septets, ok, want)
	}
}

func TestACharacterOutsideTheAlphabetIsRefused(t *testing.T) {
	if _, ok := gsm7Septets("Жук"); ok {
		t.Error("Cyrillic was taken for the GSM 7-bit alphabet")
	}
}

func TestEscapesThatLeadNowhereReadAsTheStandardSays(t *testing.T) {
	tests := map[string]struct {
		septets []byte
		want    string
	}{
		"a septet the extension table lacks": {[]byte{0x1B, 0x41}, "A"},
		"an escape after an escape":          {[]byte{0x1B, 0x1B, 0x41}, " A"},
		"a final escape":                     {[]byte{0x41, 0x1B}, "A"},
	}
	for name, test := range tests {
		if got := gsm7Text(test.septets); got != test.want {
			t.Errorf("%s: %q, want %q", name, got, test.want)
		}
	}
}

func TestSeptetsArePackedLeastSignificantBitFirst(t *testing.T) {
	// "hellohello", as TS 23.040's widely published examples pack it.
	septets, _ := gsm7Septets("hellohello")
	want := []byte{0xE8, 0x32, 0x9B, 0xFD, 0x46, 0x97, 0xD9, 0xEC, 0x37}

	packed := packSeptets(septets)

	if !bytes.Equal(packed, want) {
		t.Errorf("packSeptets = % X, want % X", packed, want)
	}
	if unpacked, ok := unpackSeptets(packed, len(septets)); !ok || !bytes.Equal(unpacked, septets) {
		t.Errorf("unpackSeptets = % x, %v; want % x", unpacked, ok, septets)
	}
}

func TestUnpackingMoreSeptetsThanTheDataHoldsFails(t *testing.T) {
	if _, ok := unpackSeptets([]byte{0xE8, 0x32}, 3); ok {
		t.Error("three septets were read from two octets")
	}
}
