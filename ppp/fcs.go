package ppp

import "math"

// The Frame Check Sequence for PPP in HDLC-like framing (RFC 1662 §C.2).
//
// This is CRC-16/X.25: reflected polynomial 0x8408, initial value 0xFFFF,
// complemented before transmission and sent least significant byte first.

// fcsInitial is the value a running FCS calculation starts from.
const fcsInitial uint16 = 0xFFFF

// fcsGoodResidue is what remains when the transmitted FCS is included in
// the calculation: the receiver's integrity check.
const fcsGoodResidue uint16 = 0xF0B8

// fcsReflectedPolynomial is CRC-16/X.25's polynomial with its bits
// reversed, as the FCS is computed least significant bit first.
const fcsReflectedPolynomial uint16 = 0x8408

// bitsPerByte is how many times the table construction shifts each entry.
const bitsPerByte = 8

// fcsTable holds the FCS update of every byte value, computed once.
var fcsTable = buildFCSTable()

// updateFCS feeds data into the running FCS fcs and returns the result.
func updateFCS(fcs uint16, data []byte) uint16 {
	for _, b := range data {
		fcs = (fcs >> bitsPerByte) ^ fcsTable[byte(fcs)^b]
	}
	return fcs
}

// buildFCSTable computes the update of every byte value, one entry each.
func buildFCSTable() [math.MaxUint8 + 1]uint16 {
	var table [math.MaxUint8 + 1]uint16
	for index := range table {
		value := uint16(index)
		for range bitsPerByte {
			if value&1 == 1 {
				value = (value >> 1) ^ fcsReflectedPolynomial
			} else {
				value >>= 1
			}
		}
		table[index] = value
	}
	return table
}
