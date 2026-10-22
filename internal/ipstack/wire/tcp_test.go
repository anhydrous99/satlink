package wire

import (
	"bytes"
	"errors"
	"testing"
)

// tcpPacketBytes is a segment with FIN, RST, ACK and URG, a window scale
// option and a payload.
func tcpPacketBytes() []byte {
	return []byte{
		0xbf, 0x00, 0x00, 0x50, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x60, 0x35, 0x01,
		0x23, 0x01, 0xb6, 0x02, 0x01, 0x03, 0x03, 0x0c, 0x01, 0xaa, 0x00, 0x00, 0xff,
	}
}

func tcpOptionBytes() []byte {
	return []byte{0x03, 0x03, 0x0c, 0x01}
}

func tcpPayloadBytes() []byte {
	return []byte{0xaa, 0x00, 0x00, 0xff}
}

// tcpSYNPacketBytes is the segment
// tcpPacketRepr describes.
func tcpSYNPacketBytes() []byte {
	return []byte{
		0xbf, 0x00, 0x00, 0x50, 0x01, 0x23, 0x45, 0x67, 0x00, 0x00, 0x00, 0x00, 0x50, 0x02, 0x01,
		0x23, 0x7a, 0x8d, 0x00, 0x00, 0xaa, 0x00, 0x00, 0xff,
	}
}

func tcpPacketRepr() TCPRepr {
	return TCPRepr{
		SrcPort:   48896,
		DstPort:   80,
		SeqNumber: 0x01234567,
		WindowLen: 0x0123,
		Control:   TCPControlSYN,
		Payload:   tcpPayloadBytes(),
	}
}

// tcpLongPacketBytes is the segment tcpLongPacketRepr describes: 320 octets,
// its payload 300 zero octets. Its length, 0x0140, has a high octet that is
// not zero, as the length of every segment of 256 octets or more has. The
// checksum was worked out by hand: the pseudo-header's words, 0xc0a8 0x0101
// 0xc0a8 0x0102 0x0006 0x0140, and the header's, 0x01bb 0xc000 0x0102 0x0304
// 0x0a0b 0x0c0d 0x5018 0x1000 0x0000 0x0000, add up to 0x2c08a, the payload
// adding nothing; the carries fold in to give 0xc08c, whose complement is the
// checksum, 0x3f73.
func tcpLongPacketBytes() []byte {
	header := []byte{
		0x01, 0xbb, 0xc0, 0x00, 0x01, 0x02, 0x03, 0x04, 0x0a, 0x0b, 0x0c, 0x0d, 0x50, 0x18, 0x10,
		0x00, 0x3f, 0x73, 0x00, 0x00,
	}
	return append(header, make([]byte, 300)...)
}

// tcpLongPacketRepr is a segment with ACK and PSH from port 443 of
// testSrcAddr to port 49152 of testDstAddr.
func tcpLongPacketRepr() TCPRepr {
	return TCPRepr{
		SrcPort:   443,
		DstPort:   49152,
		Control:   TCPControlPSH,
		SeqNumber: 0x01020304,
		AckNumber: Some[TCPSeqNumber](0x0a0b0c0d),
		WindowLen: 4096,
		Payload:   tcpLongPacketBytes()[TCPHeaderLen:],
	}
}

func TestTCPPacketDeconstruct(t *testing.T) {
	t.Parallel()
	packet := TCPPacket(tcpPacketBytes())

	checkFields(t, []field{
		{"SrcPort", packet.SrcPort(), uint16(48896)},
		{"DstPort", packet.DstPort(), uint16(80)},
		{"SeqNumber", packet.SeqNumber(), TCPSeqNumber(0x01234567)},
		{"AckNumber", packet.AckNumber(), TCPSeqNumber(-0x76543211)}, // 0x89abcdef as an i32
		{"HeaderLen", packet.HeaderLen(), 24},
		{"FIN", packet.FIN(), true},
		{"SYN", packet.SYN(), false},
		{"RST", packet.RST(), true},
		{"PSH", packet.PSH(), false},
		{"ACK", packet.ACK(), true},
		{"URG", packet.URG(), true},
		{"WindowLen", packet.WindowLen(), uint16(0x0123)},
		{"UrgentPointer", packet.UrgentPointer(), uint16(0x0201)},
		{"Checksum", packet.Checksum(), uint16(0x01b6)},
		{"VerifyChecksum", packet.VerifyChecksum(testSrcAddr, testDstAddr), true},
	})
	if !bytes.Equal(packet.Options(), tcpOptionBytes()) {
		t.Errorf("Options = % x, want % x", packet.Options(), tcpOptionBytes())
	}
	if !bytes.Equal(packet.Payload(), tcpPayloadBytes()) {
		t.Errorf("Payload = % x, want % x", packet.Payload(), tcpPayloadBytes())
	}
}

func TestTCPPacketConstruct(t *testing.T) {
	t.Parallel()
	packet := TCPPacket(filled(len(tcpPacketBytes()), 0xa5))

	packet.SetSrcPort(48896)
	packet.SetDstPort(80)
	packet.SetSeqNumber(0x01234567)
	packet.SetAckNumber(-0x76543211) // 0x89abcdef as an i32
	packet.SetHeaderLen(24)
	packet.ClearFlags()
	packet.SetFIN(true)
	packet.SetSYN(false)
	packet.SetRST(true)
	packet.SetPSH(false)
	packet.SetACK(true)
	packet.SetURG(true)
	packet.SetWindowLen(0x0123)
	packet.SetUrgentPointer(0x0201)
	packet.SetChecksum(0xeeee)
	copy(packet.Options(), tcpOptionBytes())
	copy(packet.Payload(), tcpPayloadBytes())
	packet.FillChecksum(testSrcAddr, testDstAddr)

	if !bytes.Equal(packet, tcpPacketBytes()) {
		t.Errorf("packet = % x, want % x", []byte(packet), tcpPacketBytes())
	}
}

func TestTCPPacketTruncated(t *testing.T) {
	t.Parallel()
	packet := TCPPacket(tcpPacketBytes()[:23])

	if err := packet.CheckLen(); !errors.Is(err, ErrMalformed) {
		t.Errorf("CheckLen = %v, want ErrMalformed", err)
	}
}

func TestTCPPacketImpossibleLen(t *testing.T) {
	t.Parallel()
	packet := TCPPacket(make([]byte, 20))
	packet.SetHeaderLen(10)

	if err := packet.CheckLen(); !errors.Is(err, ErrMalformed) {
		t.Errorf("CheckLen = %v, want ErrMalformed", err)
	}
}

func TestTCPReprParse(t *testing.T) {
	t.Parallel()
	repr, err := ParseTCPRepr(TCPPacket(tcpSYNPacketBytes()), testSrcAddr, testDstAddr, ChecksumCapabilities{})

	if err != nil || !repr.Equal(tcpPacketRepr()) {
		t.Errorf("ParseTCPRepr = %v, %v; want %v", repr, err, tcpPacketRepr())
	}
}

func TestTCPReprEmit(t *testing.T) {
	t.Parallel()
	repr := tcpPacketRepr()
	packet := TCPPacket(filled(repr.BufferLen(), 0xa5))

	repr.Emit(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{})

	if !bytes.Equal(packet, tcpSYNPacketBytes()) {
		t.Errorf("packet = % x, want % x", []byte(packet), tcpSYNPacketBytes())
	}
}

func TestTCPReprHeaderLenMultipleOf4(t *testing.T) {
	t.Parallel()
	repr := tcpPacketRepr()
	repr.WindowScale = Some[uint8](0) // This TCP option needs 3 octets.

	if got := repr.HeaderLen(); got%4 != 0 {
		t.Errorf("HeaderLen = %d, want a multiple of 4 (28 instead of 27)", got)
	}
}

func TestNewTCPPacketChecksTheHeaderLength(t *testing.T) {
	t.Parallel()
	// The header length counts 32-bit words, so 16 octets is the longest
	// below the fixed header.
	shortHeaderLen := TCPPacket(tcpPacketBytes())
	shortHeaderLen.SetHeaderLen(TCPHeaderLen - 4)
	cases := map[string][]byte{
		"a truncated header":                     tcpPacketBytes()[:TCPHeaderLen-1],
		"a header length below the fixed header": shortHeaderLen,
	}
	for name, buffer := range cases {
		if _, err := NewTCPPacket(buffer); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: NewTCPPacket = %v, want ErrMalformed", name, err)
		}
	}
	if _, err := NewTCPPacket(tcpPacketBytes()); err != nil {
		t.Errorf("NewTCPPacket of a valid segment = %v", err)
	}
}

func TestTCPPacketFlagsAreIndependent(t *testing.T) {
	t.Parallel()
	flags := []struct {
		name string
		set  func(TCPPacket, bool)
		get  func(TCPPacket) bool
	}{
		{"FIN", TCPPacket.SetFIN, TCPPacket.FIN},
		{"SYN", TCPPacket.SetSYN, TCPPacket.SYN},
		{"RST", TCPPacket.SetRST, TCPPacket.RST},
		{"PSH", TCPPacket.SetPSH, TCPPacket.PSH},
		{"ACK", TCPPacket.SetACK, TCPPacket.ACK},
		{"URG", TCPPacket.SetURG, TCPPacket.URG},
		{"ECE", TCPPacket.SetECE, TCPPacket.ECE},
		{"CWR", TCPPacket.SetCWR, TCPPacket.CWR},
		{"NS", TCPPacket.SetNS, TCPPacket.NS},
	}
	for _, flag := range flags {
		packet := TCPPacket(make([]byte, TCPHeaderLen))
		packet.SetHeaderLen(TCPHeaderLen)

		flag.set(packet, true)
		for _, other := range flags {
			if got := other.get(packet); got != (other.name == flag.name) {
				t.Errorf("after setting %s: %s = %v", flag.name, other.name, got)
			}
		}
		flag.set(packet, false)
		if flag.get(packet) || packet.HeaderLen() != TCPHeaderLen {
			t.Errorf("after clearing %s: flag %v, header length %d", flag.name, flag.get(packet), packet.HeaderLen())
		}
	}
}

func TestTCPPacketClearFlagsKeepsTheHeaderLength(t *testing.T) {
	t.Parallel()
	packet := TCPPacket(filled(TCPHeaderLen, 0xff))
	packet.SetHeaderLen(TCPHeaderLen)

	packet.ClearFlags()

	if packet.HeaderLen() != TCPHeaderLen || packet.dataOffsetAndFlags()&tcpFlagsMask != 0 {
		t.Errorf("header length %d, flags %#x; want 20 and none", packet.HeaderLen(), packet.dataOffsetAndFlags()&tcpFlagsMask)
	}
}

func TestTCPPacketSegmentLenCountsSYNAndFIN(t *testing.T) {
	t.Parallel()
	packet := TCPPacket(make([]byte, TCPHeaderLen+3))
	packet.SetHeaderLen(TCPHeaderLen)

	withoutFlags := packet.SegmentLen()
	packet.SetSYN(true)
	withSYN := packet.SegmentLen()
	packet.SetFIN(true)
	withSYNAndFIN := packet.SegmentLen()

	if withoutFlags != 3 || withSYN != 4 || withSYNAndFIN != 5 {
		t.Errorf("SegmentLen without flags, with SYN, with SYN and FIN = %d, %d, %d; want 3, 4, 5",
			withoutFlags, withSYN, withSYNAndFIN)
	}
}

func TestParseTCPReprRejects(t *testing.T) {
	t.Parallel()
	cases := map[string]func(packet TCPPacket){
		"a zero source port":      func(packet TCPPacket) { packet.SetSrcPort(0) },
		"a zero destination port": func(packet TCPPacket) { packet.SetDstPort(0) },
		"SYN and FIN":             func(packet TCPPacket) { packet.SetFIN(true) },
		"SYN and RST":             func(packet TCPPacket) { packet.SetRST(true) },
		"FIN and RST": func(packet TCPPacket) {
			packet.SetSYN(false)
			packet.SetFIN(true)
			packet.SetRST(true)
		},
		"a malformed option": func(packet TCPPacket) {
			packet.SetHeaderLen(TCPHeaderLen + 4)
			copy(packet.Options(), []byte{tcpOptionKindMSS, 3, 0, 0})
		},
		"a truncated last option": func(packet TCPPacket) {
			packet.SetHeaderLen(TCPHeaderLen + 4)
			copy(packet.Options(), []byte{tcpOptionKindNOP, tcpOptionKindNOP, tcpOptionKindNOP, tcpOptionKindMSS})
		},
	}
	for name, corrupt := range cases {
		packet := TCPPacket(tcpSYNPacketBytes())
		corrupt(packet)
		packet.FillChecksum(testSrcAddr, testDstAddr)

		if _, err := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{}); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: ParseTCPRepr = %v, want ErrMalformed", name, err)
		}
	}
	if _, err := ParseTCPRepr(TCPPacket(tcpSYNPacketBytes()[:19]), testSrcAddr, testDstAddr, ChecksumCapabilities{}); !errors.Is(err, ErrMalformed) {
		t.Errorf("ParseTCPRepr of a truncated segment = %v, want ErrMalformed", err)
	}
}

func TestParseTCPReprVerifiesTheChecksumOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	packet := TCPPacket(tcpSYNPacketBytes())
	packet.SetChecksum(packet.Checksum() + 1)

	if _, err := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{}); !errors.Is(err, ErrMalformed) {
		t.Errorf("ParseTCPRepr with a bad checksum = %v, want ErrMalformed", err)
	}
	repr, err := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{TCP: ChecksumOnSend})
	if err != nil || !repr.Equal(tcpPacketRepr()) {
		t.Errorf("ParseTCPRepr not verifying = %v, %v; want %v", repr, err, tcpPacketRepr())
	}
}

func TestParseTCPReprControl(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		flags func(TCPPacket)
		want  TCPControl
	}{
		{"no flag", func(TCPPacket) {}, TCPControlNone},
		{"PSH alone", func(p TCPPacket) { p.SetPSH(true) }, TCPControlPSH},
		{"SYN with PSH", func(p TCPPacket) { p.SetSYN(true); p.SetPSH(true) }, TCPControlSYN},
		{"FIN with PSH", func(p TCPPacket) { p.SetFIN(true); p.SetPSH(true) }, TCPControlFIN},
		{"RST with PSH", func(p TCPPacket) { p.SetRST(true); p.SetPSH(true) }, TCPControlRST},
	}
	for _, c := range cases {
		packet := TCPPacket(tcpSYNPacketBytes())
		packet.ClearFlags()
		c.flags(packet)

		repr, err := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{TCP: ChecksumNone})
		if err != nil || repr.Control != c.want {
			t.Errorf("%s: Control = %v, %v; want %v", c.name, repr.Control, err, c.want)
		}
	}
}

func TestParseTCPReprReadsTheAckNumberOnlyWithACK(t *testing.T) {
	t.Parallel()
	packet := TCPPacket(tcpSYNPacketBytes())
	packet.SetAckNumber(1234)

	withoutACK, errWithout := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{TCP: ChecksumNone})
	packet.SetACK(true)
	withACK, errWith := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{TCP: ChecksumNone})

	if errWithout != nil || withoutACK.AckNumber.IsSome() {
		t.Errorf("without ACK: AckNumber = %v, %v; want none", withoutACK.AckNumber, errWithout)
	}
	if errWith != nil || withACK.AckNumber != Some[TCPSeqNumber](1234) {
		t.Errorf("with ACK: AckNumber = %v, %v; want 1234", withACK.AckNumber, errWith)
	}
}

// synWithEveryOption is a SYN|ACK with every option the stack understands.
func synWithEveryOption() TCPRepr {
	repr := tcpPacketRepr()
	repr.AckNumber = Some[TCPSeqNumber](0x89abcdef - 1<<32)
	repr.MaxSegmentSize = Some[uint16](1460)
	repr.WindowScale = Some[uint8](7)
	repr.SACKPermitted = true
	repr.Timestamp = Some(TCPTimestamp{Value: 5000000, EchoReply: 7000000})
	return repr
}

func TestTCPReprWithOptionsSurvivesEmitAndParse(t *testing.T) {
	t.Parallel()
	reprs := map[string]TCPRepr{
		"every option": synWithEveryOption(),
		"SACK blocks": func() TCPRepr {
			repr := tcpPacketRepr()
			repr.Control = TCPControlNone
			repr.AckNumber = Some[TCPSeqNumber](1)
			repr.SACKRanges = [MaxSACKRanges]Optional[SACKRange]{
				Some(SACKRange{Left: 500, Right: 1500}),
				Some(SACKRange{Left: 2000, Right: 3000}),
			}
			return repr
		}(),
	}
	for name, repr := range reprs {
		packet := TCPPacket(filled(repr.BufferLen(), 0xa5))

		repr.Emit(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{})
		parsed, err := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{})

		if err != nil || !parsed.Equal(repr) {
			t.Errorf("%s: parsed %+v, %v; want %+v", name, parsed, err, repr)
		}
	}
}

func TestTCPReprControlSurvivesEmitAndParse(t *testing.T) {
	t.Parallel()
	for _, control := range []TCPControl{TCPControlNone, TCPControlPSH, TCPControlSYN, TCPControlFIN, TCPControlRST} {
		repr := tcpPacketRepr()
		repr.Control = control
		packet := TCPPacket(filled(repr.BufferLen(), 0xa5))

		repr.Emit(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{})
		parsed, err := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{})

		if err != nil || !parsed.Equal(repr) {
			t.Errorf("%v: parsed %v, %v; want %v", control, parsed, err, repr)
		}
	}
}

func TestTCPReprEmitWritesSACKBlocksOnlyOnAnAcknowledgementWithoutSACKPermitted(t *testing.T) {
	t.Parallel()
	blocks := [MaxSACKRanges]Optional[SACKRange]{{}, Some(SACKRange{Left: 500, Right: 1500})}
	sackOption := []byte{0x05, 0x0a, 0x00, 0x00, 0x01, 0xf4, 0x00, 0x00, 0x05, 0xdc, 0x00, 0x00}
	cases := []struct {
		name          string
		ack           Optional[TCPSeqNumber]
		sackPermitted bool
		wantOptions   []byte
	}{
		{"an acknowledgement", Some[TCPSeqNumber](1), false, sackOption},
		{"no acknowledgement", Optional[TCPSeqNumber]{}, false, make([]byte, 12)},
		{"SACK permitted", Some[TCPSeqNumber](1), true, append([]byte{0x04, 0x02}, make([]byte, 10)...)},
	}
	for _, c := range cases {
		repr := TCPRepr{SrcPort: 1, DstPort: 2, AckNumber: c.ack, SACKPermitted: c.sackPermitted, SACKRanges: blocks}
		packet := TCPPacket(filled(repr.BufferLen(), 0xa5))

		repr.Emit(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{})

		if !bytes.Equal(packet.Options(), c.wantOptions) {
			t.Errorf("%s: options = % x, want % x", c.name, packet.Options(), c.wantOptions)
		}
	}
}

func TestTCPReprEmitWithoutChecksumWritesZero(t *testing.T) {
	t.Parallel()
	repr := tcpPacketRepr()
	packet := TCPPacket(filled(repr.BufferLen(), 0xa5))

	repr.Emit(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{TCP: ChecksumOnReceive})

	if packet.Checksum() != 0 {
		t.Errorf("Checksum = %#04x, want 0", packet.Checksum())
	}
}

func TestTCPChecksumOfALongSegmentCoversTheHighOctetOfItsLength(t *testing.T) {
	t.Parallel()
	segment := tcpLongPacketBytes()
	repr := tcpLongPacketRepr()
	emitted := TCPPacket(filled(repr.BufferLen(), 0xa5))

	repr.Emit(emitted, testSrcAddr, testDstAddr, ChecksumCapabilities{})
	parsed, err := ParseTCPRepr(TCPPacket(segment), testSrcAddr, testDstAddr, ChecksumCapabilities{})

	if !bytes.Equal(emitted, segment) {
		t.Errorf("Emit wrote the header % x, want % x", emitted[:TCPHeaderLen], segment[:TCPHeaderLen])
	}
	if err != nil || !parsed.Equal(repr) {
		t.Errorf("ParseTCPRepr = %v, %v; want %v", parsed, err, repr)
	}
}

func TestParseTCPReprCapsTheWindowScaleAndLogs(t *testing.T) {
	logs := recordLogs(t)
	packet := TCPPacket(make([]byte, TCPHeaderLen+4))
	tcpPacketRepr().emitHeader(packet)
	packet.SetHeaderLen(TCPHeaderLen + 4)
	copy(packet.Options(), []byte{tcpOptionKindWindowScale, 3, 15, tcpOptionKindEnd})

	repr, err := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{TCP: ChecksumNone})

	if err != nil || repr.WindowScale != Some[uint8](14) {
		t.Errorf("WindowScale = %v, %v; want 14", repr.WindowScale, err)
	}
	want := "parsed window scaling factor >14, setting to 14 pkg=ipstack src=192.168.1.1:48896 dst=192.168.1.2:80"
	if got := logs.messages(); len(got) != 1 || got[0] != want {
		t.Errorf("logged %q, want %q", got, want)
	}
}

func TestParseTCPReprStopsAtTheEndOfTheOptions(t *testing.T) {
	t.Parallel()
	packet := TCPPacket(make([]byte, TCPHeaderLen+8))
	tcpPacketRepr().emitHeader(packet)
	packet.SetHeaderLen(TCPHeaderLen + 8)
	copy(packet.Options(), []byte{tcpOptionKindNOP, 0x0c, 2, tcpOptionKindEnd, tcpOptionKindMSS, 1, 2, 3})

	repr, err := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{TCP: ChecksumNone})

	want := tcpPacketRepr()
	want.Payload = nil
	if err != nil || !repr.Equal(want) {
		t.Errorf("ParseTCPRepr = %+v, %v; want %+v (unknown option skipped, the rest after the end ignored)", repr, err, want)
	}
}

func TestTCPReprHeaderLenCountsEveryOption(t *testing.T) {
	t.Parallel()
	withSACK := TCPRepr{SACKRanges: [MaxSACKRanges]Optional[SACKRange]{Some(SACKRange{}), Some(SACKRange{})}}
	cases := map[string]struct {
		repr TCPRepr
		want int
	}{
		"no option":    {TCPRepr{}, 20},
		"every option": {synWithEveryOption(), 40},
		"two blocks":   {withSACK, 40},
		"MSS only":     {TCPRepr{MaxSegmentSize: Some[uint16](536)}, 24},
	}
	for name, c := range cases {
		if got := c.repr.HeaderLen(); got != c.want {
			t.Errorf("%s: HeaderLen = %d, want %d", name, got, c.want)
		}
	}
}

func TestTCPReprLengths(t *testing.T) {
	t.Parallel()
	syn := tcpPacketRepr()
	ack := TCPRepr{AckNumber: Some[TCPSeqNumber](1), Control: TCPControlPSH}
	fin := TCPRepr{Control: TCPControlFIN}

	checkFields(t, []field{
		{"SYN BufferLen", syn.BufferLen(), 24},
		{"SYN SegmentLen", syn.SegmentLen(), 5},
		{"SYN IsEmpty", syn.IsEmpty(), false},
		{"ACK SegmentLen", ack.SegmentLen(), 0},
		{"ACK IsEmpty", ack.IsEmpty(), true},
		{"FIN SegmentLen", fin.SegmentLen(), 1},
		{"FIN IsEmpty", fin.IsEmpty(), false},
		{"RST IsEmpty", TCPRepr{Control: TCPControlRST}.IsEmpty(), false},
		{"data IsEmpty", TCPRepr{Payload: []byte{1}}.IsEmpty(), false},
	})
}

func TestTCPReprEqual(t *testing.T) {
	t.Parallel()
	changes := map[string]func(*TCPRepr){
		"source port":    func(r *TCPRepr) { r.SrcPort++ },
		"control":        func(r *TCPRepr) { r.Control = TCPControlFIN },
		"sequence":       func(r *TCPRepr) { r.SeqNumber++ },
		"acknowledgment": func(r *TCPRepr) { r.AckNumber = Optional[TCPSeqNumber]{} },
		"window":         func(r *TCPRepr) { r.WindowLen++ },
		"window scale":   func(r *TCPRepr) { r.WindowScale = Some[uint8](8) },
		"MSS":            func(r *TCPRepr) { r.MaxSegmentSize = Some[uint16](536) },
		"SACK permitted": func(r *TCPRepr) { r.SACKPermitted = false },
		"SACK blocks":    func(r *TCPRepr) { r.SACKRanges[2] = Some(SACKRange{Left: 1, Right: 2}) },
		"timestamp":      func(r *TCPRepr) { r.Timestamp = Optional[TCPTimestamp]{} },
		"payload":        func(r *TCPRepr) { r.Payload = []byte{0xaa, 0x00, 0x00, 0xfe} },
	}
	if !synWithEveryOption().Equal(synWithEveryOption()) {
		t.Error("a Repr differs from an identical one")
	}
	for name, change := range changes {
		changed := synWithEveryOption()
		change(&changed)
		if changed.Equal(synWithEveryOption()) {
			t.Errorf("a Repr with another %s is equal", name)
		}
	}
}

func TestTCPReprString(t *testing.T) {
	t.Parallel()
	ack := TCPRepr{SrcPort: 7, DstPort: 49152, Control: TCPControlPSH, SeqNumber: -1, AckNumber: Some[TCPSeqNumber](10), WindowLen: 1024}

	checkFields(t, []field{
		{"SYN", tcpPacketRepr().String(), "TCP src=48896 dst=80 syn seq=19088743 win=291 len=4"},
		{"SYN|ACK", synWithEveryOption().String(), "TCP src=48896 dst=80 syn seq=19088743 ack=2309737967 win=291 len=4 mss=1460"},
		{"PSH|ACK", ack.String(), "TCP src=7 dst=49152 psh seq=4294967295 ack=10 win=1024 len=0"},
		{"FIN", TCPRepr{Control: TCPControlFIN}.String(), "TCP src=0 dst=0 fin seq=0 win=0 len=0"},
		{"RST", TCPRepr{Control: TCPControlRST}.String(), "TCP src=0 dst=0 rst seq=0 win=0 len=0"},
		{"bare", TCPRepr{}.String(), "TCP src=0 dst=0 seq=0 win=0 len=0"},
	})
}

func TestTCPControl(t *testing.T) {
	t.Parallel()
	cases := []struct {
		control TCPControl
		len     int
		quashed TCPControl
		name    string
	}{
		{TCPControlNone, 0, TCPControlNone, "None"},
		{TCPControlPSH, 0, TCPControlNone, "Psh"},
		{TCPControlSYN, 1, TCPControlSYN, "Syn"},
		{TCPControlFIN, 1, TCPControlFIN, "Fin"},
		{TCPControlRST, 0, TCPControlRST, "Rst"},
		{TCPControl(9), 0, TCPControl(9), "TCPControl(9)"},
	}
	for _, c := range cases {
		checkFields(t, []field{
			{c.name + " Len", c.control.Len(), c.len},
			{c.name + " QuashPSH", c.control.QuashPSH(), c.quashed},
			{c.name + " String", c.control.String(), c.name},
		})
	}
}

func TestTCPChecksumFunctionsPanicOnAddressesThatAreNotIPv4(t *testing.T) {
	t.Parallel()
	repr := tcpPacketRepr()
	packet := TCPPacket(make([]byte, repr.BufferLen()))
	for name, addr := range addressesThatAreNotIPv4 {
		expectPanic(t, "FillChecksum from "+name, func() { packet.FillChecksum(addr, testDstAddr) })
		expectPanic(t, "VerifyChecksum to "+name, func() { packet.VerifyChecksum(testSrcAddr, addr) })
		expectPanic(t, "Emit from "+name, func() { repr.Emit(packet, addr, testDstAddr, ChecksumCapabilities{}) })
		expectPanic(t, "Emit to "+name+" without a checksum", func() {
			repr.Emit(packet, testSrcAddr, addr, ChecksumCapabilities{TCP: ChecksumNone})
		})
	}
}
