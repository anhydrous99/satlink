package smspdu

import (
	"errors"
	"strings"
)

// Addresses: the originator or destination of a TPDU (TS 23.040
// §9.1.2.5), and the service centre's address before it (TS 24.011
// §8.2.5.1).

// The type-of-address octets satlink writes: the ISDN numbering plan, and
// international numbers flagged as such.
const (
	typeInternational = 0x91
	typeUnknown       = 0x81
)

// The type of number, in bits 6 to 4 of a type-of-address octet.
const (
	typeOfNumberMask          = 0x70
	typeOfNumberInternational = 0x10
	typeOfNumberAlphanumeric  = 0x50
)

// internationalPrefix marks an international number in text.
const internationalPrefix = "+"

// semiOctetDigits are the characters of the semi-octets 0 to 14 (TS 23.040
// §9.1.2.3); semi-octet 15 fills the last octet of an odd count.
const semiOctetDigits = "0123456789*#abc"

// fillerSemiOctet pads an odd count of digits to whole octets.
const fillerSemiOctet = 0xF

// errNotDialable is what encoding a number with other characters fails
// with.
var errNotDialable = errors.New("a number holds only digits, '*' and '#', after an optional '+'")

// encodeAddress returns number as a TP address: its count of digits, its
// type and its digits in semi-octets. A leading '+' makes it
// international.
func encodeAddress(number string) ([]byte, error) {
	digits, international := strings.CutPrefix(number, internationalPrefix)
	semiOctets, err := encodeDigits(digits)
	if err != nil {
		return nil, err
	}
	addressType := byte(typeUnknown)
	if international {
		addressType = typeInternational
	}
	return append([]byte{byte(len(digits)), addressType}, semiOctets...), nil
}

// EncodeServiceCentre returns number as the service centre's address that
// starts a PDU: its length in octets, its type and its digits. An empty
// number is the empty address, which leaves the choice to the handset. It
// fails if number is not dialable.
func EncodeServiceCentre(number string) ([]byte, error) {
	if number == "" {
		return []byte{0}, nil
	}
	address, err := encodeAddress(number)
	if err != nil {
		return nil, err
	}
	// The type and the digits, without the count of digits.
	return append([]byte{byte(len(address) - 1)}, address[1:]...), nil
}

// encodeDigits returns digits in semi-octets, two to an octet, the first
// in the low half, padded with a filler.
func encodeDigits(digits string) ([]byte, error) {
	if digits == "" {
		return nil, errNotDialable
	}
	octets := make([]byte, (len(digits)+1)/2)
	for i := range len(digits) {
		value := strings.IndexByte(semiOctetDigits[:12], digits[i])
		if value < 0 {
			return nil, errNotDialable
		}
		octets[i/2] |= byte(value) << (4 * (i % 2))
	}
	if len(digits)%2 == 1 {
		octets[len(octets)-1] |= fillerSemiOctet << 4
	}
	return octets, nil
}

// decodeAddress reads a TP address: international numbers get their '+',
// and alphanumeric ones, such as a sender's name, their characters.
func decodeAddress(r *reader) string {
	semiOctets := int(r.octet())
	addressType := r.octet()
	digits := r.take((semiOctets + 1) / 2)
	switch addressType & typeOfNumberMask {
	case typeOfNumberAlphanumeric:
		septets, _ := unpackSeptets(digits, semiOctets*4/septetBits)
		return gsm7Text(septets)
	case typeOfNumberInternational:
		return internationalPrefix + decodeDigits(digits, semiOctets)
	default:
		return decodeDigits(digits, semiOctets)
	}
}

// decodeDigits returns the count digits held in semi-octets.
func decodeDigits(semiOctets []byte, count int) string {
	var digits strings.Builder
	for i := 0; i < count && i/2 < len(semiOctets); i++ {
		value := semiOctets[i/2] >> (4 * (i % 2)) & 0xF
		if value == fillerSemiOctet {
			break
		}
		digits.WriteByte(semiOctetDigits[value])
	}
	return digits.String()
}

// skipServiceCentre reads past the service centre's address: its length
// in octets, then that many octets.
func skipServiceCentre(r *reader) {
	r.take(int(r.octet()))
}
