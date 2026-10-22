package smspdu

import (
	"fmt"
	"time"
)

// The service centre's timestamp of an SMS-DELIVER (TS 23.040 §9.2.3.11):
// seven octets of two semi-octets each, the last the zone's offset from
// UTC in quarter hours.

// timestampOctets is the size of a timestamp.
const timestampOctets = 7

// negativeOffset is the bit of the zone's octet that makes the offset
// negative: the high bit of its first semi-octet.
const negativeOffset = 0x08

// quarterHour is the unit of the zone's offset.
const quarterHour = 15 * time.Minute

// decodeTimestamp returns a timestamp as text mode writes one:
// yy/MM/dd,hh:mm:ss±zz, zz in quarter hours.
func decodeTimestamp(octets []byte) string {
	if len(octets) != timestampOctets {
		return ""
	}
	sign, zone := '+', octets[6]
	if zone&negativeOffset != 0 {
		sign = '-'
	}
	return fmt.Sprintf("%s/%s/%s,%s:%s:%s%c%s",
		swapped(octets[0]), swapped(octets[1]), swapped(octets[2]),
		swapped(octets[3]), swapped(octets[4]), swapped(octets[5]),
		sign, swapped(zone&^negativeOffset))
}

// swapped returns the two semi-octets of octet, low half first.
func swapped(octet byte) string {
	return string([]byte{semiOctetCharacter(octet & 0xF), semiOctetCharacter(octet >> 4)})
}

// semiOctetCharacter returns the character of a semi-octet, and '?' for
// the filler, which a timestamp should never hold.
func semiOctetCharacter(value byte) byte {
	if int(value) < len(semiOctetDigits) {
		return semiOctetDigits[value]
	}
	return '?'
}

// encodeTimestamp returns moment as a timestamp in its own zone, to the
// second.
func encodeTimestamp(moment time.Time) []byte {
	_, offsetSeconds := moment.Zone()
	quarters := int(time.Duration(offsetSeconds) * time.Second / quarterHour)
	zone := swappedDigits(abs(quarters))
	if quarters < 0 {
		zone |= negativeOffset
	}
	return []byte{
		swappedDigits(moment.Year() % 100), swappedDigits(int(moment.Month())), swappedDigits(moment.Day()),
		swappedDigits(moment.Hour()), swappedDigits(moment.Minute()), swappedDigits(moment.Second()),
		zone,
	}
}

// swappedDigits returns the two decimal digits of value, 0 to 99, as an
// octet, the tens in the low half.
func swappedDigits(value int) byte {
	return byte(value/10) | byte(value%10)<<4
}

// abs returns the magnitude of value.
func abs(value int) int {
	return max(value, -value)
}
