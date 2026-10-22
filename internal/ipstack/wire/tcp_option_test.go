package wire

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// sackOption returns a SACK option holding blocks.
func sackOption(blocks ...SACKRange) tcpOption {
	option := tcpOption{kind: tcpOptionSACKRange}
	for i, block := range blocks {
		option.sackRanges[i] = Some(block)
	}
	return option
}

// assertOptionParses checks that encoded parses as option, with nothing left
// over, and that option emits as encoded (smoltcp's assert_option_parses!).
func assertOptionParses(t *testing.T, option tcpOption, encoded []byte) {
	t.Helper()
	parsed, rest, err := parseTCPOption(encoded)
	if err != nil || len(rest) != 0 || !reflect.DeepEqual(parsed, option) {
		t.Errorf("parseTCPOption(% x) = %+v, % x, %v; want %+v", encoded, parsed, rest, err, option)
	}
	buffer := make([]byte, 40)[:option.bufferLen()]
	if rest := option.emit(buffer); len(rest) != 0 {
		t.Errorf("emit of %+v left % x", option, rest)
	}
	if !bytes.Equal(buffer, encoded) {
		t.Errorf("emit of %+v = % x, want % x", option, buffer, encoded)
	}
}

// Ported from smoltcp's wire/tcp.rs test_tcp_options.
func TestTCPOptions(t *testing.T) {
	t.Parallel()
	assertOptionParses(t, tcpOption{kind: tcpOptionEndOfList}, []byte{0x00})
	assertOptionParses(t, tcpOption{kind: tcpOptionNoOperation}, []byte{0x01})
	assertOptionParses(t, tcpOption{kind: tcpOptionMaxSegmentSize, maxSegmentSize: 1500},
		[]byte{0x02, 0x04, 0x05, 0xdc})
	assertOptionParses(t, tcpOption{kind: tcpOptionWindowScale, windowScale: 12}, []byte{0x03, 0x03, 0x0c})
	assertOptionParses(t, tcpOption{kind: tcpOptionSACKPermitted}, []byte{0x4, 0x02})
	assertOptionParses(t, sackOption(SACKRange{500, 1500}),
		[]byte{0x05, 0x0a, 0x00, 0x00, 0x01, 0xf4, 0x00, 0x00, 0x05, 0xdc})
	assertOptionParses(t, sackOption(SACKRange{875, 1225}, SACKRange{1500, 2500}),
		[]byte{
			0x05, 0x12, 0x00, 0x00, 0x03, 0x6b, 0x00, 0x00, 0x04, 0xc9, 0x00, 0x00, 0x05, 0xdc,
			0x00, 0x00, 0x09, 0xc4,
		})
	assertOptionParses(t, sackOption(SACKRange{875000, 1225000}, SACKRange{1500000, 2500000}, SACKRange{876543210, 876654320}),
		[]byte{
			0x05, 0x1a, 0x00, 0x0d, 0x59, 0xf8, 0x00, 0x12, 0xb1, 0x28, 0x00, 0x16, 0xe3, 0x60,
			0x00, 0x26, 0x25, 0xa0, 0x34, 0x3e, 0xfc, 0xea, 0x34, 0x40, 0xae, 0xf0,
		})
	assertOptionParses(t, tcpOption{kind: tcpOptionTimestamp, timestamp: TCPTimestamp{Value: 5000000, EchoReply: 7000000}},
		[]byte{
			0x08,                   // kind
			0x0a,                   // length
			0x00, 0x4c, 0x4b, 0x40, // TSval
			0x00, 0x6a, 0xcf, 0xc0, // TSecr
		})
	assertOptionParses(t, tcpOption{kind: tcpOptionUnknown, unknownKind: 12, data: []byte{1, 2, 3}},
		[]byte{0x0c, 0x05, 0x01, 0x02, 0x03})
}

// Ported from smoltcp's wire/tcp.rs test_malformed_tcp_options.
func TestMalformedTCPOptions(t *testing.T) {
	t.Parallel()
	malformed := [][]byte{
		{},
		{0xc},
		{0xc, 0x05, 0x01, 0x02},
		{0xc, 0x01},
		{0x2, 0x02},
		{0x3, 0x02},
	}
	for _, encoded := range malformed {
		if option, _, err := parseTCPOption(encoded); !errors.Is(err, ErrMalformed) {
			t.Errorf("parseTCPOption(% x) = %+v, %v; want ErrMalformed", encoded, option, err)
		}
	}
}

func TestTCPOptionsOfAFixedLengthRejectOtherLengths(t *testing.T) {
	t.Parallel()
	malformed := map[string][]byte{
		"a long MSS":           {0x02, 0x05, 0x05, 0xdc, 0x00},
		"a long window scale":  {0x03, 0x04, 0x0c, 0x00},
		"a long SACK-permit":   {0x04, 0x03, 0x00},
		"a SACK without block": {0x05, 0x02},
		"a partial SACK block": {0x05, 0x0e, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3},
	}
	for name, encoded := range malformed {
		if option, _, err := parseTCPOption(encoded); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: parseTCPOption = %+v, %v; want ErrMalformed", name, option, err)
		}
	}
}

func TestTCPTimestampOptionOfAnotherLengthIsUnknown(t *testing.T) {
	t.Parallel()
	encodings := map[string][]byte{
		"6 octets":                {0x08, 0x06, 0x00, 0x00, 0x00, 0x01},
		"9 octets, one too few":   {0x08, 0x09, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00},
		"11 octets, one too many": {0x08, 0x0b, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x02, 0x00},
	}
	for name, encoded := range encodings {
		option, rest, err := parseTCPOption(encoded)

		want := tcpOption{kind: tcpOptionUnknown, unknownKind: tcpOptionKindTimestamp, data: encoded[2:]}
		if err != nil || len(rest) != 0 || !reflect.DeepEqual(option, want) {
			t.Errorf("%s: parseTCPOption = %+v, % x, %v; want %+v", name, option, rest, err, want)
		}
	}
}

func TestTCPOptionParseReturnsTheOctetsAfterTheOption(t *testing.T) {
	t.Parallel()
	encoded := []byte{0x01, 0x03, 0x03, 0x07, 0x00, 0xff}

	_, afterNOP, errNOP := parseTCPOption(encoded)
	_, afterScale, errScale := parseTCPOption(afterNOP)

	if errNOP != nil || errScale != nil || !bytes.Equal(afterScale, []byte{0x00, 0xff}) {
		t.Errorf("after a NOP and a window scale: % x (%v, %v), want 00 ff", afterScale, errNOP, errScale)
	}
}

func TestSACKOptionWithFourBlocksKeepsThreeAndLogs(t *testing.T) {
	logs := recordLogs(t)
	encoded := []byte{0x05, 0x22}
	for block := range uint32(4) {
		encoded = append(encoded, 0, 0, 0, byte(2*block), 0, 0, 0, byte(2*block+1))
	}

	option, rest, err := parseTCPOption(encoded)

	want := sackOption(SACKRange{0, 1}, SACKRange{2, 3}, SACKRange{4, 5})
	if err != nil || len(rest) != 0 || !reflect.DeepEqual(option, want) {
		t.Errorf("parseTCPOption = %+v, % x, %v; want %+v", option, rest, err, want)
	}
	if got := logs.messages(); len(got) != 1 || got[0] != "sACK with >3 blocks, truncating to 3 pkg=ipstack" {
		t.Errorf("logged %q", got)
	}
}

func TestSACKOptionEmitsOnlyThePresentBlocks(t *testing.T) {
	t.Parallel()
	option := tcpOption{kind: tcpOptionSACKRange}
	option.sackRanges[1] = Some(SACKRange{Left: 500, Right: 1500})
	buffer := make([]byte, option.bufferLen())

	option.emit(buffer)

	want := []byte{0x05, 0x0a, 0x00, 0x00, 0x01, 0xf4, 0x00, 0x00, 0x05, 0xdc}
	if !bytes.Equal(buffer, want) {
		t.Errorf("emit = % x, want % x", buffer, want)
	}
}

func TestTCPEndOfListOptionPadsTheWholeBuffer(t *testing.T) {
	t.Parallel()
	buffer := filled(3, 0xa5)

	rest := tcpOption{kind: tcpOptionEndOfList}.emit(buffer)

	if !bytes.Equal(buffer, []byte{0, 0, 0}) || len(rest) != 2 {
		t.Errorf("buffer % x with % x left, want 00 00 00 with two octets left", buffer, rest)
	}
}
