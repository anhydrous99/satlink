package smspdu

// The GSM 7-bit default alphabet, its extension table, and how septets are
// packed into octets (TS 23.038 §6.1.2.1 and §6.2.1).

// septetBits is the size of a GSM 7-bit character.
const septetBits = 7

// escapeToExtension is the septet that makes the next one a character of
// the extension table.
const escapeToExtension = 0x1B

// defaultAlphabet is the default alphabet in septet order: the character
// at index n is septet n. Index 0x1B is the escape, which stands for no
// character of its own.
const defaultAlphabet = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ\x1bÆæßÉ" +
	" !\"#¤%&'()*+,-./0123456789:;<=>?" +
	"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§" +
	"¿abcdefghijklmnopqrstuvwxyzäöñüà"

// extensionTable maps the septets that follow the escape to their
// characters. A receiver shows any other septet after an escape as the
// default alphabet has it.
var extensionTable = map[byte]rune{
	0x0A: '\f', 0x14: '^', 0x28: '{', 0x29: '}', 0x2F: '\\',
	0x3C: '[', 0x3D: '~', 0x3E: ']', 0x40: '|', 0x65: '€',
}

// The alphabet both ways, built once from the tables above.
var (
	defaultCharacters = []rune(defaultAlphabet)
	defaultSeptets    = septetsOf(defaultCharacters)
	extensionSeptets  = extensionSeptetsOf(extensionTable)
)

// septetsOf returns the septet of each character of the default alphabet,
// leaving out the escape.
func septetsOf(characters []rune) map[rune]byte {
	septets := make(map[rune]byte, len(characters))
	for septet, character := range characters {
		if septet != escapeToExtension {
			septets[character] = byte(septet)
		}
	}
	return septets
}

// extensionSeptetsOf returns the septet of each character of the extension
// table.
func extensionSeptetsOf(table map[byte]rune) map[rune]byte {
	septets := make(map[rune]byte, len(table))
	for septet, character := range table {
		septets[character] = septet
	}
	return septets
}

// gsm7Septets returns text in septets, a character of the extension table
// taking two, and false if text holds a character the alphabet lacks.
func gsm7Septets(text string) ([]byte, bool) {
	septets := make([]byte, 0, len(text))
	for _, character := range text {
		if septet, ok := defaultSeptets[character]; ok {
			septets = append(septets, septet)
		} else if septet, ok := extensionSeptets[character]; ok {
			septets = append(septets, escapeToExtension, septet)
		} else {
			return nil, false
		}
	}
	return septets, true
}

// gsm7Text returns the characters septets stand for. A septet after an
// escape that the extension table lacks reads as the default alphabet has
// it; an escape after an escape reads as a space, and a final escape as
// nothing (TS 23.038 §6.2.1.1).
func gsm7Text(septets []byte) string {
	text := make([]rune, 0, len(septets))
	for i := 0; i < len(septets); i++ {
		septet := septets[i]
		if septet != escapeToExtension {
			text = append(text, defaultCharacters[septet])
			continue
		}
		if i++; i == len(septets) {
			break
		}
		text = append(text, extendedCharacter(septets[i]))
	}
	return string(text)
}

// extendedCharacter returns the character septet stands for after an
// escape.
func extendedCharacter(septet byte) rune {
	if character, ok := extensionTable[septet]; ok {
		return character
	}
	if septet == escapeToExtension {
		return ' '
	}
	return defaultCharacters[septet]
}

// packSeptets packs septets into octets, least significant bit first.
func packSeptets(septets []byte) []byte {
	packed := make([]byte, (len(septets)*septetBits+7)/8)
	for i, septet := range septets {
		bit := i * septetBits
		value := uint16(septet&0x7F) << (bit % 8)
		packed[bit/8] |= byte(value)
		if high := byte(value >> 8); high != 0 {
			packed[bit/8+1] |= high
		}
	}
	return packed
}

// unpackSeptets returns the count septets packed in data, and false if
// data is too short to hold them.
func unpackSeptets(data []byte, count int) ([]byte, bool) {
	if (count*septetBits+7)/8 > len(data) {
		return nil, false
	}
	septets := make([]byte, count)
	for i := range septets {
		bit := i * septetBits
		value := uint16(data[bit/8]) >> (bit % 8)
		if bit%8 > 8-septetBits {
			value |= uint16(data[bit/8+1]) << (8 - bit%8)
		}
		septets[i] = byte(value) & 0x7F
	}
	return septets, true
}
