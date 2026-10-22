package ppp

import "testing"

func TestFCSMatchesTheCRC16X25CheckValue(t *testing.T) {
	if got := fcsOf([]byte("123456789")); got != 0x906E {
		t.Errorf("FCS = %#04x, want 0x906e", got)
	}
}

func TestAFrameFollowedByItsFCSLeavesTheGoodResidue(t *testing.T) {
	frame := []byte{0xFF, 0x03, 0xC0, 0x21, 0x01, 0x01, 0x00, 0x04}
	fcs := fcsOf(frame)
	frame = append(frame, byte(fcs), byte(fcs>>8)) // least significant byte first

	if got := updateFCS(fcsInitial, frame); got != fcsGoodResidue {
		t.Errorf("residue = %#04x, want %#04x", got, fcsGoodResidue)
	}
}
