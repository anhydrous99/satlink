package wire

import (
	"bytes"
	"errors"
	"testing"
)

// udpPacketBytes is smoltcp's PACKET_BYTES: a DNS datagram with a checksum.
func udpPacketBytes() []byte {
	return []byte{0xbf, 0x00, 0x00, 0x35, 0x00, 0x0c, 0x12, 0x4d, 0xaa, 0x00, 0x00, 0xff}
}

// udpNoChecksumPacketBytes is smoltcp's NO_CHECKSUM_PACKET: the same datagram
// without a checksum.
func udpNoChecksumPacketBytes() []byte {
	return []byte{0xbf, 0x00, 0x00, 0x35, 0x00, 0x0c, 0x00, 0x00, 0xaa, 0x00, 0x00, 0xff}
}

func udpPayloadBytes() []byte {
	return []byte{0xaa, 0x00, 0x00, 0xff}
}

func udpPacketRepr() UDPRepr {
	return UDPRepr{SrcPort: 48896, DstPort: 53}
}

// udpLongPacketBytes is a datagram of 308 octets, its payload 300 zero
// octets, from port 12345 of testSrcAddr to port 7 of testDstAddr. Its
// length, 0x0134, has a high octet that is not zero, as the length of every
// datagram of 256 octets or more has. The checksum was worked out by hand:
// the pseudo-header's words, 0xc0a8 0x0101 0xc0a8 0x0102 0x0011 0x0134, and
// the header's, 0x3039 0x0007 0x0134 0x0000, add up to 0x1b60c, the payload
// adding nothing; the carry folds in to give 0xb60d, whose complement is the
// checksum, 0x49f2.
func udpLongPacketBytes() []byte {
	header := []byte{0x30, 0x39, 0x00, 0x07, 0x01, 0x34, 0x49, 0xf2}
	return append(header, make([]byte, 300)...)
}

// Ported from smoltcp's wire/udp.rs test_deconstruct.
func TestUDPPacketDeconstruct(t *testing.T) {
	t.Parallel()
	packet := UDPPacket(udpPacketBytes())

	checkFields(t, []field{
		{"SrcPort", packet.SrcPort(), uint16(48896)},
		{"DstPort", packet.DstPort(), uint16(53)},
		{"Length", packet.Length(), uint16(12)},
		{"Checksum", packet.Checksum(), uint16(0x124d)},
		{"VerifyChecksum", packet.VerifyChecksum(testSrcAddr, testDstAddr), true},
	})
	if !bytes.Equal(packet.Payload(), udpPayloadBytes()) {
		t.Errorf("Payload = % x, want % x", packet.Payload(), udpPayloadBytes())
	}
}

// Ported from smoltcp's wire/udp.rs test_construct.
func TestUDPPacketConstruct(t *testing.T) {
	t.Parallel()
	packet := UDPPacket(filled(12, 0xa5))

	packet.SetSrcPort(48896)
	packet.SetDstPort(53)
	packet.SetLength(12)
	packet.SetChecksum(0xffff)
	copy(packet.Payload(), udpPayloadBytes())
	packet.FillChecksum(testSrcAddr, testDstAddr)

	if !bytes.Equal(packet, udpPacketBytes()) {
		t.Errorf("packet = % x, want % x", []byte(packet), udpPacketBytes())
	}
}

// Ported from smoltcp's wire/udp.rs test_impossible_len.
func TestUDPPacketImpossibleLen(t *testing.T) {
	t.Parallel()
	packet := UDPPacket(make([]byte, 12))
	packet.SetLength(4)

	if err := packet.CheckLen(); !errors.Is(err, ErrMalformed) {
		t.Errorf("CheckLen = %v, want ErrMalformed", err)
	}
}

// Ported from smoltcp's wire/udp.rs test_zero_checksum.
func TestUDPPacketZeroChecksum(t *testing.T) {
	t.Parallel()
	packet := UDPPacket(make([]byte, 8))
	packet.SetSrcPort(1)
	packet.SetDstPort(31881)
	packet.SetLength(8)

	packet.FillChecksum(testSrcAddr, testDstAddr)

	if packet.Checksum() != 0xffff {
		t.Errorf("Checksum = %#04x, want 0xffff", packet.Checksum())
	}
}

// Ported from smoltcp's wire/udp.rs test_no_checksum.
func TestUDPPacketNoChecksum(t *testing.T) {
	t.Parallel()
	packet := UDPPacket(make([]byte, 8))
	packet.SetSrcPort(1)
	packet.SetDstPort(31881)
	packet.SetLength(8)
	packet.SetChecksum(0)

	if !packet.VerifyChecksum(testSrcAddr, testDstAddr) {
		t.Error("VerifyChecksum of a datagram without checksum = false")
	}
}

// Ported from smoltcp's wire/udp.rs test_parse.
func TestUDPReprParse(t *testing.T) {
	t.Parallel()
	repr, err := ParseUDPRepr(UDPPacket(udpPacketBytes()), testSrcAddr, testDstAddr, ChecksumCapabilities{})

	if err != nil || repr != udpPacketRepr() {
		t.Errorf("ParseUDPRepr = %v, %v; want %v", repr, err, udpPacketRepr())
	}
}

// Ported from smoltcp's wire/udp.rs test_emit.
func TestUDPReprEmit(t *testing.T) {
	t.Parallel()
	repr := udpPacketRepr()
	packet := UDPPacket(filled(repr.HeaderLen()+len(udpPayloadBytes()), 0xa5))

	repr.Emit(packet, testSrcAddr, testDstAddr, udpPayloadBytes(), ChecksumCapabilities{})

	if !bytes.Equal(packet, udpPacketBytes()) {
		t.Errorf("packet = % x, want % x", []byte(packet), udpPacketBytes())
	}
}

// Ported from smoltcp's wire/udp.rs test_checksum_omitted.
func TestUDPReprChecksumOmitted(t *testing.T) {
	t.Parallel()
	repr, err := ParseUDPRepr(UDPPacket(udpNoChecksumPacketBytes()), testSrcAddr, testDstAddr, ChecksumCapabilities{})

	if err != nil || repr != udpPacketRepr() {
		t.Errorf("ParseUDPRepr = %v, %v; want %v", repr, err, udpPacketRepr())
	}
}

func TestNewUDPPacketChecksTheLengthField(t *testing.T) {
	t.Parallel()
	lengthBelowTheHeader := UDPPacket(udpPacketBytes())
	lengthBelowTheHeader.SetLength(UDPHeaderLen - 1)
	cases := map[string][]byte{
		"shorter than a header":     udpPacketBytes()[:UDPHeaderLen-1],
		"shorter than its length":   udpPacketBytes()[:11],
		"a length below the header": lengthBelowTheHeader,
	}
	for name, buffer := range cases {
		if _, err := NewUDPPacket(buffer); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: NewUDPPacket = %v, want ErrMalformed", name, err)
		}
	}
	if _, err := NewUDPPacket(udpPacketBytes()); err != nil {
		t.Errorf("NewUDPPacket of a valid datagram = %v", err)
	}
}

func TestUDPPacketPayloadEndsWithTheLengthField(t *testing.T) {
	t.Parallel()
	overlong := UDPPacket(append(udpPacketBytes(), 0x01, 0x02))

	if !bytes.Equal(overlong.Payload(), udpPayloadBytes()) || !overlong.VerifyChecksum(testSrcAddr, testDstAddr) {
		t.Errorf("Payload = % x, checksum verified %v; want % x, true",
			overlong.Payload(), overlong.VerifyChecksum(testSrcAddr, testDstAddr), udpPayloadBytes())
	}
}

func TestParseUDPReprRejects(t *testing.T) {
	t.Parallel()
	zeroDestination := UDPPacket(udpNoChecksumPacketBytes())
	zeroDestination.SetDstPort(0)
	badChecksum := UDPPacket(udpPacketBytes())
	badChecksum.SetChecksum(0x124e)
	cases := map[string]UDPPacket{
		"a zero destination port": zeroDestination,
		"a bad checksum":          badChecksum,
		"a truncated datagram":    UDPPacket(udpPacketBytes()[:10]),
	}
	for name, packet := range cases {
		if _, err := ParseUDPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{}); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: ParseUDPRepr = %v, want ErrMalformed", name, err)
		}
	}
}

func TestParseUDPReprAcceptsAZeroSourcePortAndUnverifiedChecksums(t *testing.T) {
	t.Parallel()
	noReplyExpected := UDPPacket(udpNoChecksumPacketBytes())
	noReplyExpected.SetSrcPort(0)
	badChecksum := UDPPacket(udpPacketBytes())
	badChecksum.SetChecksum(0x124e)

	repr, err := ParseUDPRepr(noReplyExpected, testSrcAddr, testDstAddr, ChecksumCapabilities{})
	if err != nil || repr != (UDPRepr{DstPort: 53}) {
		t.Errorf("ParseUDPRepr from port 0 = %v, %v", repr, err)
	}
	repr, err = ParseUDPRepr(badChecksum, testSrcAddr, testDstAddr, ChecksumCapabilities{UDP: ChecksumNone})
	if err != nil || repr != udpPacketRepr() {
		t.Errorf("ParseUDPRepr not verifying = %v, %v", repr, err)
	}
}

func TestUDPReprEmitWithoutChecksumWritesZero(t *testing.T) {
	t.Parallel()
	packet := UDPPacket(filled(UDPHeaderLen+len(udpPayloadBytes()), 0xa5))

	udpPacketRepr().Emit(packet, testSrcAddr, testDstAddr, udpPayloadBytes(), ChecksumCapabilities{UDP: ChecksumOnReceive})

	if !bytes.Equal(packet, udpNoChecksumPacketBytes()) {
		t.Errorf("packet = % x, want % x", []byte(packet), udpNoChecksumPacketBytes())
	}
}

func TestUDPChecksumOfALongDatagramCoversTheHighOctetOfItsLength(t *testing.T) {
	t.Parallel()
	datagram := udpLongPacketBytes()
	repr := UDPRepr{SrcPort: 12345, DstPort: 7}
	emitted := UDPPacket(filled(len(datagram), 0xa5))

	repr.Emit(emitted, testSrcAddr, testDstAddr, datagram[UDPHeaderLen:], ChecksumCapabilities{})
	parsed, err := ParseUDPRepr(UDPPacket(datagram), testSrcAddr, testDstAddr, ChecksumCapabilities{})

	if !bytes.Equal(emitted, datagram) {
		t.Errorf("Emit wrote the header % x, want % x", emitted[:UDPHeaderLen], datagram[:UDPHeaderLen])
	}
	if err != nil || parsed != repr {
		t.Errorf("ParseUDPRepr = %v, %v; want %v", parsed, err, repr)
	}
}

func TestUDPReprHeaderLenAndString(t *testing.T) {
	t.Parallel()
	checkFields(t, []field{
		{"HeaderLen", udpPacketRepr().HeaderLen(), 8},
		{"String", udpPacketRepr().String(), "UDP src=48896 dst=53"},
	})
}

func TestUDPChecksumFunctionsPanicOnAddressesThatAreNotIPv4(t *testing.T) {
	t.Parallel()
	for name, addr := range addressesThatAreNotIPv4 {
		packet := UDPPacket(udpNoChecksumPacketBytes())
		expectPanic(t, "FillChecksum from "+name, func() { packet.FillChecksum(addr, testDstAddr) })
		expectPanic(t, "VerifyChecksum of no checksum to "+name, func() { packet.VerifyChecksum(testSrcAddr, addr) })
		expectPanic(t, "Emit from "+name+" without a checksum", func() {
			udpPacketRepr().Emit(packet, addr, testDstAddr, udpPayloadBytes(), ChecksumCapabilities{UDP: ChecksumNone})
		})
	}
}
