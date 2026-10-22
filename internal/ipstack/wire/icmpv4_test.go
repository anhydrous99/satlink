package wire

import (
	"bytes"
	"errors"
	"testing"
)

// icmpv4EchoPacketBytes is an echo request.
func icmpv4EchoPacketBytes() []byte {
	return []byte{0x08, 0x00, 0x8e, 0xfe, 0x12, 0x34, 0xab, 0xcd, 0xaa, 0x00, 0x00, 0xff}
}

func icmpv4EchoDataBytes() []byte {
	return []byte{0xaa, 0x00, 0x00, 0xff}
}

func icmpv4EchoPacketRepr() ICMPv4Repr {
	return ICMPv4Repr{
		Message:    ICMPv4EchoRequest,
		Identifier: 0x1234,
		SeqNumber:  0xabcd,
		Data:       icmpv4EchoDataBytes(),
	}
}

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

func TestICMPv4PacketEchoDeconstruct(t *testing.T) {
	t.Parallel()
	packet := ICMPv4Packet(icmpv4EchoPacketBytes())

	checkFields(t, []field{
		{"Type", packet.Type(), ICMPv4EchoRequest},
		{"Code", packet.Code(), uint8(0)},
		{"Checksum", packet.Checksum(), uint16(0x8efe)},
		{"EchoIdentifier", packet.EchoIdentifier(), uint16(0x1234)},
		{"EchoSeqNumber", packet.EchoSeqNumber(), uint16(0xabcd)},
		{"VerifyChecksum", packet.VerifyChecksum(), true},
	})
	if !bytes.Equal(packet.Data(), icmpv4EchoDataBytes()) {
		t.Errorf("Data = % x, want % x", packet.Data(), icmpv4EchoDataBytes())
	}
}

func TestICMPv4PacketEchoConstruct(t *testing.T) {
	t.Parallel()
	packet := ICMPv4Packet(filled(12, 0xa5))

	packet.SetType(ICMPv4EchoRequest)
	packet.SetCode(0)
	packet.SetEchoIdentifier(0x1234)
	packet.SetEchoSeqNumber(0xabcd)
	copy(packet.Data(), icmpv4EchoDataBytes())
	packet.FillChecksum()

	if !bytes.Equal(packet, icmpv4EchoPacketBytes()) {
		t.Errorf("packet = % x, want % x", []byte(packet), icmpv4EchoPacketBytes())
	}
}

func TestICMPv4ReprEchoParse(t *testing.T) {
	t.Parallel()
	repr, err := ParseICMPv4Repr(ICMPv4Packet(icmpv4EchoPacketBytes()), ChecksumCapabilities{})

	if err != nil || !repr.Equal(icmpv4EchoPacketRepr()) {
		t.Errorf("ParseICMPv4Repr = %+v, %v; want %+v", repr, err, icmpv4EchoPacketRepr())
	}
}

func TestICMPv4ReprEchoEmit(t *testing.T) {
	t.Parallel()
	repr := icmpv4EchoPacketRepr()
	packet := ICMPv4Packet(filled(repr.BufferLen(), 0xa5))

	repr.Emit(packet, ChecksumCapabilities{})

	if !bytes.Equal(packet, icmpv4EchoPacketBytes()) {
		t.Errorf("packet = % x, want % x", []byte(packet), icmpv4EchoPacketBytes())
	}
}

func TestICMPv4PacketCheckLen(t *testing.T) {
	t.Parallel()
	buffer := []byte{0x0b, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

	if _, err := NewICMPv4Packet(nil); !errors.Is(err, ErrMalformed) {
		t.Errorf("NewICMPv4Packet(empty) = %v, want ErrMalformed", err)
	}
	if _, err := NewICMPv4Packet(buffer[:4]); !errors.Is(err, ErrMalformed) {
		t.Errorf("NewICMPv4Packet(4 octets) = %v, want ErrMalformed", err)
	}
	if _, err := NewICMPv4Packet(buffer); err != nil {
		t.Errorf("NewICMPv4Packet(8 octets) = %v", err)
	}
}

func TestICMPv4PacketHeaderLenIsEightForEveryType(t *testing.T) {
	t.Parallel()
	for _, message := range []ICMPv4Message{ICMPv4EchoReply, ICMPv4DstUnreachable, icmpv4Redirect, 0xff} {
		packet := ICMPv4Packet(make([]byte, 12))
		packet.SetType(message)

		if packet.HeaderLen() != 8 || len(packet.Data()) != 4 {
			t.Errorf("%v: HeaderLen %d, %d octets of data; want 8 and 4", message, packet.HeaderLen(), len(packet.Data()))
		}
	}
}

func TestICMPv4ReprEchoReplySurvivesEmitAndParse(t *testing.T) {
	t.Parallel()
	reply := icmpv4EchoPacketRepr()
	reply.Message = ICMPv4EchoReply
	packet := ICMPv4Packet(make([]byte, reply.BufferLen()))

	reply.Emit(packet, ChecksumCapabilities{})
	parsed, err := ParseICMPv4Repr(packet, ChecksumCapabilities{})

	if err != nil || !parsed.Equal(reply) {
		t.Errorf("ParseICMPv4Repr = %+v, %v; want %+v", parsed, err, reply)
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

func TestICMPv4ReprPortUnreachableParse(t *testing.T) {
	t.Parallel()
	repr, err := ParseICMPv4Repr(ICMPv4Packet(icmpv4PortUnreachableBytes()), ChecksumCapabilities{})

	if err != nil || !repr.Equal(icmpv4PortUnreachableRepr()) {
		t.Errorf("ParseICMPv4Repr = %+v, %v; want %+v", repr, err, icmpv4PortUnreachableRepr())
	}
}

func TestICMPv4ReprTimeExceededSurvivesEmitAndParse(t *testing.T) {
	t.Parallel()
	repr := icmpv4PortUnreachableRepr()
	repr.Message, repr.Code = ICMPv4TimeExceeded, 0
	packet := ICMPv4Packet(make([]byte, repr.BufferLen()))

	repr.Emit(packet, ChecksumCapabilities{})
	parsed, err := ParseICMPv4Repr(packet, ChecksumCapabilities{})

	if err != nil || !parsed.Equal(repr) {
		t.Errorf("ParseICMPv4Repr = %+v, %v; want %+v", parsed, err, repr)
	}
}

func TestICMPv4ReprEmitQuotesTheFullLengthOfTheDatagram(t *testing.T) {
	t.Parallel()
	repr := icmpv4PortUnreachableRepr()
	repr.Header.PayloadLen = 1000
	packet := ICMPv4Packet(make([]byte, repr.BufferLen()))

	repr.Emit(packet, ChecksumCapabilities{})

	quoted := IPv4Packet(packet.Data())
	if quoted.TotalLen() != 1020 || !quoted.VerifyChecksum() || !packet.VerifyChecksum() {
		t.Errorf("quoted total length %d, checksums %v %v; want 1020 and both correct",
			quoted.TotalLen(), quoted.VerifyChecksum(), packet.VerifyChecksum())
	}
	// The parser accepts only a quote that holds everything its header
	// announces, so it cannot read such an error back.
	if _, err := ParseICMPv4Repr(packet, ChecksumCapabilities{}); !errors.Is(err, ErrMalformed) {
		t.Errorf("ParseICMPv4Repr = %v, want ErrMalformed", err)
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
	repr := icmpv4EchoPacketRepr()
	packet := ICMPv4Packet(filled(repr.BufferLen(), 0xa5))

	repr.Emit(packet, ChecksumCapabilities{ICMPv4: ChecksumOnReceive})

	if packet.Checksum() != 0 {
		t.Errorf("Checksum = %#04x, want 0", packet.Checksum())
	}
}

func TestParseICMPv4ReprRejects(t *testing.T) {
	t.Parallel()
	echoWithCode := ICMPv4Packet(icmpv4EchoPacketBytes())
	echoWithCode.SetCode(1)
	echoWithCode.FillChecksum()
	badChecksum := ICMPv4Packet(icmpv4EchoPacketBytes())
	badChecksum.SetChecksum(0x8eff)
	redirect := ICMPv4Packet(icmpv4PortUnreachableBytes())
	redirect.SetType(icmpv4Redirect)
	redirect.FillChecksum()
	shortQuote := ICMPv4Packet(icmpv4PortUnreachableBytes()[:35])
	IPv4Packet(shortQuote.Data()).SetTotalLen(27)
	shortQuote.FillChecksum()
	malformedQuote := ICMPv4Packet(icmpv4PortUnreachableBytes())
	IPv4Packet(malformedQuote.Data()).SetHeaderLen(16)
	malformedQuote.FillChecksum()
	// With its checksum right, only the length check can reject it.
	shortEcho := ICMPv4Packet(icmpv4EchoPacketBytes()[:7])
	shortEcho.FillChecksum()
	cases := map[string]ICMPv4Packet{
		"an echo request with a code":     echoWithCode,
		"a bad checksum":                  badChecksum,
		"a type it does not represent":    redirect,
		"fewer than 8 quoted octets":      shortQuote,
		"a malformed quoted header":       malformedQuote,
		"a message shorter than a header": shortEcho,
	}
	for name, packet := range cases {
		if repr, err := ParseICMPv4Repr(packet, ChecksumCapabilities{}); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: ParseICMPv4Repr = %+v, %v; want ErrMalformed", name, repr, err)
		}
	}
}

func TestParseICMPv4ReprVerifiesTheChecksumOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	packet := ICMPv4Packet(icmpv4EchoPacketBytes())
	packet.SetChecksum(0x8eff)

	repr, err := ParseICMPv4Repr(packet, ChecksumCapabilities{ICMPv4: ChecksumOnSend})

	if err != nil || !repr.Equal(icmpv4EchoPacketRepr()) {
		t.Errorf("ParseICMPv4Repr not verifying = %+v, %v", repr, err)
	}
}

func TestICMPv4ReprPanicsOnAMessageItDoesNotRepresent(t *testing.T) {
	t.Parallel()
	repr := ICMPv4Repr{Message: icmpv4Timestamp}

	expectPanicWith(t, "wire: ICMPv4Repr does not represent timestamp (type 13) messages", func() { repr.BufferLen() })
	expectPanicWith(t, "wire: ICMPv4Repr does not represent timestamp (type 13) messages", func() {
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

func TestICMPv4ReprEqual(t *testing.T) {
	t.Parallel()
	changes := map[string]func(*ICMPv4Repr){
		"message":    func(r *ICMPv4Repr) { r.Message = ICMPv4TimeExceeded },
		"identifier": func(r *ICMPv4Repr) { r.Identifier++ },
		"sequence":   func(r *ICMPv4Repr) { r.SeqNumber++ },
		"code":       func(r *ICMPv4Repr) { r.Code++ },
		"header":     func(r *ICMPv4Repr) { r.Header.PayloadLen++ },
		"data":       func(r *ICMPv4Repr) { r.Data = r.Data[1:] },
	}
	if !icmpv4PortUnreachableRepr().Equal(icmpv4PortUnreachableRepr()) {
		t.Error("a Repr differs from an identical one")
	}
	for name, change := range changes {
		changed := icmpv4PortUnreachableRepr()
		change(&changed)
		if changed.Equal(icmpv4PortUnreachableRepr()) {
			t.Errorf("a Repr with another %s is equal", name)
		}
	}
}

func TestICMPv4MessageString(t *testing.T) {
	t.Parallel()
	names := map[ICMPv4Message]string{
		ICMPv4EchoReply:      "echo reply",
		ICMPv4DstUnreachable: "destination unreachable",
		icmpv4Redirect:       "message redirect",
		ICMPv4EchoRequest:    "echo request",
		icmpv4RouterAdvert:   "router advertisement",
		icmpv4RouterSolicit:  "router solicitation",
		ICMPv4TimeExceeded:   "time exceeded",
		icmpv4ParamProblem:   "parameter problem",
		icmpv4Timestamp:      "timestamp",
		icmpv4TimestampReply: "timestamp reply",
		ICMPv4Message(42):    "42",
	}
	for message, want := range names {
		if got := message.String(); got != want {
			t.Errorf("ICMPv4Message(%d).String() = %q, want %q", uint8(message), got, want)
		}
	}
}
