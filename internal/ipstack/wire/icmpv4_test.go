package wire

import (
	"bytes"
	"testing"
)

// icmpv4PortUnreachableBytes answers a UDP datagram from 192.168.1.2:12345
// to port 7 of 192.168.1.1 carrying "hello", quoting all of it. Its
// checksums were computed independently of this package.
func icmpv4PortUnreachableBytes() []byte {
	return []byte{
		0x03, 0x03, 0x88, 0xdd, 0x00, 0x00, 0x00, 0x00, // type, code, checksum, unused
		0x45, 0x00, 0x00, 0x21, 0x00, 0x00, 0x40, 0x00, 0x40, 0x11, 0xb7, 0x78, // the quoted header
		0xc0, 0xa8, 0x01, 0x02, 0xc0, 0xa8, 0x01, 0x01,
		0x30, 0x39, 0x00, 0x07, 0x00, 0x0d, 0x00, 0x00, // the quoted UDP header
		0x68, 0x65, 0x6c, 0x6c, 0x6f, // "hello"
	}
}

func icmpv4PortUnreachableRepr() ICMPv4Repr {
	return ICMPv4Repr{
		Message: ICMPv4DstUnreachable,
		Code:    ICMPv4PortUnreachable,
		Header: IPv4Repr{
			SrcAddr:    testDstAddr,
			DstAddr:    testSrcAddr,
			NextHeader: IPProtocolUDP,
			PayloadLen: 13,
			HopLimit:   64,
		},
		Data: icmpv4PortUnreachableBytes()[28:],
	}
}

func TestICMPv4PacketDataFollowsTheEightOctetHeader(t *testing.T) {
	t.Parallel()
	packet := ICMPv4Packet(make([]byte, 12))

	if packet.HeaderLen() != 8 || len(packet.Data()) != 4 {
		t.Errorf("HeaderLen %d, %d octets of data; want 8 and 4", packet.HeaderLen(), len(packet.Data()))
	}
}

func TestICMPv4ReprPortUnreachableEmit(t *testing.T) {
	t.Parallel()
	repr := icmpv4PortUnreachableRepr()
	packet := ICMPv4Packet(filled(repr.BufferLen(), 0xa5))

	repr.Emit(packet, ChecksumCapabilities{})

	if !bytes.Equal(packet, icmpv4PortUnreachableBytes()) {
		t.Errorf("packet = % x, want % x", []byte(packet), icmpv4PortUnreachableBytes())
	}
}

func TestICMPv4ReprEmitQuotesTheFullLengthOfTheDatagram(t *testing.T) {
	t.Parallel()
	repr := icmpv4PortUnreachableRepr()
	repr.Header.PayloadLen = 1000
	packet := ICMPv4Packet(make([]byte, repr.BufferLen()))

	repr.Emit(packet, ChecksumCapabilities{})

	quoted := IPv4Packet(packet.Data())
	if quoted.TotalLen() != 1020 || !quoted.VerifyChecksum() || !icmpv4ChecksumIsCorrect(packet) {
		t.Errorf("quoted total length %d, checksums %v %v; want 1020 and both correct",
			quoted.TotalLen(), quoted.VerifyChecksum(), icmpv4ChecksumIsCorrect(packet))
	}
}

func TestICMPv4ReprEmitClearsTheUnusedFieldOfAnError(t *testing.T) {
	t.Parallel()
	repr := icmpv4PortUnreachableRepr()
	packet := ICMPv4Packet(filled(repr.BufferLen(), 0xa5))

	repr.Emit(packet, ChecksumCapabilities{ICMPv4: ChecksumNone})

	if unused := packet[4:8]; !bytes.Equal(unused, make([]byte, 4)) {
		t.Errorf("unused field = % x, want zeros", unused)
	}
}

func TestICMPv4ReprEmitWithoutChecksumWritesZero(t *testing.T) {
	t.Parallel()
	repr := icmpv4PortUnreachableRepr()
	packet := ICMPv4Packet(filled(repr.BufferLen(), 0xa5))

	repr.Emit(packet, ChecksumCapabilities{ICMPv4: ChecksumOnReceive})

	if checksum := packet[icmpv4ChecksumOffset:icmpv4UnusedOffset]; !bytes.Equal(checksum, make([]byte, 2)) {
		t.Errorf("checksum = % x, want zeros", checksum)
	}
}

// icmpv4ChecksumIsCorrect reports whether the checksum over the whole of
// packet is correct.
func icmpv4ChecksumIsCorrect(packet ICMPv4Packet) bool {
	return dataChecksum(packet) == correctChecksumSum
}

func TestICMPv4ReprPanicsOnAMessageItDoesNotRepresent(t *testing.T) {
	t.Parallel()
	// A timestamp message (RFC 792).
	repr := ICMPv4Repr{Message: 13}

	expectPanicWith(t, "wire: ICMPv4Repr does not represent messages of type 13", func() { repr.BufferLen() })
	expectPanicWith(t, "wire: ICMPv4Repr does not represent messages of type 13", func() {
		repr.Emit(ICMPv4Packet(make([]byte, 8)), ChecksumCapabilities{})
	})
}

func TestICMPv4ReprEmitPanicsOnAQuotedAddressThatIsNotIPv4(t *testing.T) {
	t.Parallel()
	for name, addr := range addressesThatAreNotIPv4 {
		repr := icmpv4PortUnreachableRepr()
		repr.Header.DstAddr = addr

		expectPanic(t, "Emit quoting a "+name+" destination", func() {
			repr.Emit(ICMPv4Packet(make([]byte, repr.BufferLen())), ChecksumCapabilities{})
		})
	}
}

func TestICMPv4MessageString(t *testing.T) {
	t.Parallel()
	names := map[ICMPv4Message]string{
		ICMPv4DstUnreachable: "destination unreachable",
		ICMPv4Message(11):    "11",
		ICMPv4Message(42):    "42",
	}
	for message, want := range names {
		if got := message.String(); got != want {
			t.Errorf("ICMPv4Message(%d).String() = %q, want %q", uint8(message), got, want)
		}
	}
}
