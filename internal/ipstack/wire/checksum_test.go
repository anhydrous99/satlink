package wire

import (
	"net/netip"
	"testing"
)

func TestChecksumDirections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		checksum          Checksum
		onReceive, onSend bool
	}{
		{"Both", ChecksumBoth, true, true},
		{"OnReceive", ChecksumOnReceive, true, false},
		{"OnSend", ChecksumOnSend, false, true},
		{"None", ChecksumNone, false, false},
	}
	for _, c := range cases {
		checkFields(t, []field{
			{c.name + " OnReceive", c.checksum.OnReceive(), c.onReceive},
			{c.name + " OnSend", c.checksum.OnSend(), c.onSend},
		})
	}
}

func TestChecksumCapabilitiesZeroValueVerifiesAndComputesEverything(t *testing.T) {
	t.Parallel()
	var capabilities ChecksumCapabilities

	for name, checksum := range map[string]Checksum{
		"IPv4": capabilities.IPv4, "UDP": capabilities.UDP, "TCP": capabilities.TCP, "ICMPv4": capabilities.ICMPv4,
	} {
		if checksum != ChecksumBoth {
			t.Errorf("%s = %v, want ChecksumBoth", name, checksum)
		}
	}
}

// The numerical example of RFC 1071 § 3.
func TestDataChecksumOfTheRFC1071Example(t *testing.T) {
	t.Parallel()
	data := []byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6, 0xf7}

	if got := dataChecksum(data); got != 0xddf2 {
		t.Errorf("dataChecksum = %#04x, want 0xddf2", got)
	}
}

func TestDataChecksumPadsAnOddOctet(t *testing.T) {
	t.Parallel()
	checkFields(t, []field{
		{"empty", dataChecksum(nil), uint16(0)},
		{"one octet", dataChecksum([]byte{0xab}), uint16(0xab00)},
		{"three octets", dataChecksum([]byte{0x00, 0x01, 0xf2}), uint16(0xf201)},
	})
}

func TestDataChecksumOfLongDataKeepsEveryCarry(t *testing.T) {
	t.Parallel()
	// 2¹⁷ words of 0xffff overflow a 32-bit sum; their one's complement sum
	// is still 0xffff.
	data := filled(1<<18, 0xff)

	if got := dataChecksum(data); got != 0xffff {
		t.Errorf("dataChecksum = %#04x, want 0xffff", got)
	}
}

func TestCombineChecksumsFoldsTheCarry(t *testing.T) {
	t.Parallel()
	checkFields(t, []field{
		{"none", combineChecksums(), uint16(0)},
		{"0xffff + 1", combineChecksums(0xffff, 0x0001), uint16(0x0001)},
		{"0x8000 + 0x8000", combineChecksums(0x8000, 0x8000), uint16(0x0001)},
		{"parts", combineChecksums(dataChecksum([]byte{0x00, 0x01, 0xf2, 0x03}), dataChecksum([]byte{0xf4, 0xf5, 0xf6, 0xf7})), uint16(0xddf2)},
	})
}

func TestPseudoHeaderChecksumAddsAddressesProtocolAndLength(t *testing.T) {
	t.Parallel()
	// The pseudo-header's words, 0xc0a8 0x0101 0xc0a8 0x0102 0x0006 0x0118,
	// add up to 0x18471, whose carry folds in to give 0x8472. The length, 280
	// octets, has a high octet that is not zero, as the length of every
	// segment of 256 octets or more has.
	const want uint16 = 0x8472

	checkFields(t, []field{
		{"280 octets", pseudoHeaderChecksum(testSrcAddr, testDstAddr, IPProtocolTCP, 0x0118), want},
		{"only 16 bits of length", pseudoHeaderChecksum(testSrcAddr, testDstAddr, IPProtocolTCP, 0x10118), want},
	})
}

func TestTransportChecksumCoversThePseudoHeaderAndTheSegment(t *testing.T) {
	t.Parallel()
	segment := []byte{0x30, 0x39, 0x00, 0x07, 0x00, 0x0a, 0x00, 0x00, 0x68, 0x69}

	got := transportChecksum(testSrcAddr, testDstAddr, IPProtocolUDP, segment)

	// The pseudo-header's words, 0xc0a8 0x0101 0xc0a8 0x0102 0x0011 0x000a,
	// and the segment's, 0x3039 0x0007 0x000a 0x0000 0x6869, add up to
	// 0x21c21, whose carry folds in to give 0x1c23.
	if got != 0x1c23 {
		t.Errorf("transportChecksum = %#04x, want 0x1c23", got)
	}
}

func TestPseudoHeaderChecksumPanicsOnAddressesThatAreNotIPv4(t *testing.T) {
	t.Parallel()
	for name, addr := range addressesThatAreNotIPv4 {
		expectPanic(t, "a "+name+" source", func() { pseudoHeaderChecksum(addr, testDstAddr, IPProtocolUDP, 8) })
		expectPanic(t, "a "+name+" destination", func() {
			pseudoHeaderChecksum(testSrcAddr, addr, IPProtocolUDP, 8)
		})
	}
	expectPanic(t, "an IPv6 pair", func() {
		pseudoHeaderChecksum(netip.IPv6Loopback(), netip.IPv6Loopback(), IPProtocolTCP, 20)
	})
}
