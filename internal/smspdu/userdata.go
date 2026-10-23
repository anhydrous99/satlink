package smspdu

import (
	"errors"
	"unicode/utf16"

	"github.com/anhydrous99/satlink/internal/utf8lossy"
)

// Reading a PDU, and its user data: TP-UDL, then a header if TP-UDHI says
// so, then the text in the alphabet the coding scheme names (TS 23.040
// §9.2.3.24, TS 23.038 §4).

// The errors of PDUs that do not hold together.
var (
	// errTruncated is a PDU that ends before its fields do.
	errTruncated = errors.New("the PDU ends early")
	// errBadHeader is a user data header longer than the user data.
	errBadHeader = errors.New("the user data header is longer than the user data")
)

// reader reads a PDU field by field. Reading past its end gives zeros, or
// what is left, and records errTruncated, so that a decoder checks once.
type reader struct {
	data []byte
	err  error
}

// octet reads one octet.
func (r *reader) octet() byte {
	if len(r.data) == 0 {
		r.err = errTruncated
		return 0
	}
	value := r.data[0]
	r.data = r.data[1:]
	return value
}

// take reads count octets, or what is left of them.
func (r *reader) take(count int) []byte {
	if count > len(r.data) {
		r.err = errTruncated
		count = len(r.data)
	}
	taken := r.data[:count]
	r.data = r.data[count:]
	return taken
}

// alphabet is the character set of a message's user data.
type alphabet int

const (
	alphabetGSM7 alphabet = iota
	alphabetEightBit
	alphabetUCS2
)

// The coding groups of TS 23.038 §4, in the high half of TP-DCS.
const (
	// codingGroupMask selects the general data coding groups, 00xx and
	// 01xx, whose alphabet is in bits 3 and 2.
	codingGroupMask    = 0xC0
	generalCoding      = 0x00
	autoDeletedCoding  = 0x40
	alphabetShift      = 2
	alphabetMask       = 0x03
	compressedText     = 0x20
	dataCodingGroup    = 0xF0
	dataCodingEightBit = 0x04
	ucs2WaitingGroup   = 0xE0
)

// generalAlphabets are the alphabets of the general data coding groups, by
// bits 3 and 2; the reserved value reads as GSM 7-bit.
var generalAlphabets = [4]alphabet{alphabetGSM7, alphabetEightBit, alphabetUCS2, alphabetGSM7}

// alphabetOf returns the alphabet coding names. Compressed text, which no
// handset is known to send, reads as 8-bit data; reserved coding groups
// read as GSM 7-bit, as TS 23.038 asks.
func alphabetOf(coding byte) alphabet {
	switch group := coding & 0xF0; {
	case coding&codingGroupMask == generalCoding || coding&codingGroupMask == autoDeletedCoding:
		if coding&compressedText != 0 {
			return alphabetEightBit
		}
		return generalAlphabets[coding>>alphabetShift&alphabetMask]
	case group == dataCodingGroup && coding&dataCodingEightBit != 0:
		return alphabetEightBit
	case group == ucs2WaitingGroup:
		return alphabetUCS2
	default:
		return alphabetGSM7
	}
}

// decodeUserData reads TP-UDL and the user data, and returns its text
// without the header that firstOctet may announce.
func decodeUserData(r *reader, firstOctet, coding byte) (string, error) {
	length := int(r.octet())
	data := r.take(len(r.data))
	if r.err != nil {
		return "", r.err
	}
	hasHeader := firstOctet&headerIndicator != 0
	switch alphabetOf(coding) {
	case alphabetGSM7:
		return gsm7UserData(data, length, hasHeader)
	case alphabetUCS2:
		octets, err := octetUserData(data, length, hasHeader)
		return ucs2Text(octets), err
	default:
		octets, err := octetUserData(data, length, hasHeader)
		return utf8lossy.String(octets), err
	}
}

// gsm7UserData returns the text of length septets packed in data. A header
// takes the septets its octets fill, padding included.
func gsm7UserData(data []byte, length int, hasHeader bool) (string, error) {
	septets, ok := unpackSeptets(data, length)
	if !ok {
		return "", errTruncated
	}
	if hasHeader {
		headerSeptets := (headerLength(data)*8 + septetBits - 1) / septetBits
		if headerSeptets > len(septets) {
			return "", errBadHeader
		}
		septets = septets[headerSeptets:]
	}
	return gsm7Text(septets), nil
}

// octetUserData returns the length octets of data, without their header.
func octetUserData(data []byte, length int, hasHeader bool) ([]byte, error) {
	if length > len(data) {
		return nil, errTruncated
	}
	octets := data[:length]
	if !hasHeader {
		return octets, nil
	}
	header := headerLength(octets)
	if header > len(octets) {
		return nil, errBadHeader
	}
	return octets[header:], nil
}

// headerLength is how many octets a user data header takes, its length
// octet (TP-UDHL) included; more than data holds when data is empty.
func headerLength(data []byte) int {
	if len(data) == 0 {
		return 1
	}
	return 1 + int(data[0])
}

// ucs2Text returns the text of big-endian UTF-16 octets. A final odd octet
// is dropped, and unpaired surrogates read as U+FFFD.
func ucs2Text(octets []byte) string {
	units := make([]uint16, len(octets)/2)
	for i := range units {
		units[i] = uint16(octets[2*i])<<8 | uint16(octets[2*i+1])
	}
	return string(utf16.Decode(units))
}
