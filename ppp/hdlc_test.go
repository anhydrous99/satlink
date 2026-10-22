package ppp

import (
	"bytes"
	"slices"
	"testing"
)

// The reference vectors below are bytes a reference script (Python,
// cross-checked against the CRC-16/X-25 check value) computed,
// independently of this package.
const (
	// lcpRequestVector is an LCP Configure-Request: ACCM 0 and magic
	// number 0x7E7D1A2B.
	lcpRequestVector = "01 01 00 10 02 06 00 00 00 00 05 06 7E 7D 1A 2B"
	// ipcpRequestVector is an IPCP Configure-Request for address and DNS
	// servers, all 0.0.0.0.
	ipcpRequestVector = "01 01 00 16 03 06 00 00 00 00 81 06 00 00 00 00 83 06 00 00 00 00"
)

// assertFrames fails the test unless got holds want's frames, in order.
func assertFrames(t *testing.T, got, want []frame) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("decoded %d frames, want %d: %+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index].protocol != want[index].protocol || !bytes.Equal(got[index].information, want[index].information) {
			t.Fatalf("frame %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestRoundTripsEveryByteValue(t *testing.T) {
	information := make([]byte, 256)
	for value := range information {
		information[value] = byte(value)
	}

	frames := decodeAll(frameBytes(protocolIPv4, information, conservativeEncoding()))

	assertFrames(t, frames, []frame{{protocol: protocolIPv4, information: information}})
}

func TestEscapesFlagsEscapesAndMappedControlCharactersOnly(t *testing.T) {
	encoding := frameEncoding{accm: 1 << 0x11}

	encoded := frameBytes(protocolIPv4, []byte{0x7E, 0x7D, 0x11, 0x13}, encoding)

	stuffed := encoded[5 : len(encoded)-3] // skip flag, header, FCS and flag
	if want := []byte{0x7D, 0x5E, 0x7D, 0x5D, 0x7D, 0x31, 0x13}; !bytes.Equal(stuffed, want) {
		t.Errorf("stuffed information = % X, want % X", stuffed, want)
	}
}

func TestTheDefaultACCMEscapesEveryControlCharacterAndNoMore(t *testing.T) {
	// The encoder and the decoder share isMapped, so a round trip cannot show
	// where the control characters end; the line does. 0x1F, the last (RFC
	// 1662 §7.1), goes out as 7D 3F, and 0x20 as it is. The reference script
	// computed the frame.
	got := frameBytes(protocolIPv4, []byte{0x1F, 0x20}, conservativeEncoding())

	if want := hexBytes(t, "7E FF 7D 23 7D 20 21 7D 3F 20 D5 79 7E"); !bytes.Equal(got, want) {
		t.Errorf("frame = % X, want % X", got, want)
	}
}

func TestCompressesHeadersWhenThePeerAllows(t *testing.T) {
	encoded := frameBytes(protocolIPv4, []byte("x"), negotiatedEncoding())

	if header := encoded[1:3]; !bytes.Equal(header, []byte{0x21, 'x'}) {
		t.Errorf("frame starts % X, want 21 78", header)
	}
	if frames := decodeAll(encoded); len(frames) != 1 || frames[0].protocol != protocolIPv4 {
		t.Errorf("decoded %+v, want one IPv4 frame", frames)
	}
}

func TestNeverCompressesATwoOctetProtocol(t *testing.T) {
	encoded := frameBytes(protocolLCP, []byte("x"), negotiatedEncoding())

	if header := encoded[1:3]; !bytes.Equal(header, []byte{0xC0, 0x21}) {
		t.Errorf("frame starts % X, want C0 21", header)
	}
}

func TestDecodesFramesSplitAcrossReadsAndSurroundedByNoise(t *testing.T) {
	stream := []byte("\r\nNO CARRIER\r\n")
	stream = append(stream, frameBytes(protocolIPv4, []byte("first"), conservativeEncoding())...)
	stream = append(stream, "garbage"...)
	stream = append(stream, frameBytes(protocolLCP, []byte("second"), conservativeEncoding())...)
	decoder := newFrameDecoder(int(DefaultMRU))

	var frames []frame
	for chunk := range slices.Chunk(stream, 3) {
		frames = append(frames, decoder.decode(chunk)...)
	}

	assertFrames(t, frames, []frame{
		{protocol: protocolIPv4, information: []byte("first")},
		{protocol: protocolLCP, information: []byte("second")},
	})
	if decoder.invalidFrames != 2 {
		t.Errorf("invalid frames = %d, want 2", decoder.invalidFrames)
	}
}

func TestRejectsCorruptedAbortedAndOversizedFrames(t *testing.T) {
	corrupted := frameBytes(protocolIPv4, []byte("payload"), conservativeEncoding())
	corrupted[6] ^= 0x01
	aborted := frameBytes(protocolIPv4, []byte("payload"), conservativeEncoding())
	aborted = slices.Insert(aborted, len(aborted)-1, controlEscape)
	oversized := frameBytes(protocolIPv4, bytes.Repeat([]byte{0x55}, 100), conservativeEncoding())
	decoder := newFrameDecoder(50)

	for _, received := range [][]byte{corrupted, aborted, oversized} {
		if frames := decoder.decode(received); len(frames) != 0 {
			t.Errorf("decoded %+v from % X, want nothing", frames, received)
		}
	}

	if decoder.invalidFrames != 3 {
		t.Errorf("invalid frames = %d, want 3", decoder.invalidFrames)
	}
}

func TestTheDecoderTakesFramesUpToItsLimitOnly(t *testing.T) {
	const maxInformationLen = 50
	information := bytes.Repeat([]byte{0x55}, maxInformationLen)
	longest := frameBytes(protocolIPv4, information, conservativeEncoding())
	tests := []struct {
		name    string
		stream  []byte
		want    []frame
		invalid uint64
	}{
		{"the longest frame it takes", longest, []frame{{protocol: protocolIPv4, information: information}}, 0},
		{"a frame one octet longer, its FCS intact",
			frameBytes(protocolIPv4, bytes.Repeat([]byte{0x55}, maxInformationLen+1), conservativeEncoding()), nil, 1},
		// What the decoder keeps of it is a well-formed frame.
		{"the longest frame with one more octet before its closing flag",
			slices.Insert(slices.Clone(longest), len(longest)-1, 0x55), nil, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoder := newFrameDecoder(maxInformationLen)

			frames := decoder.decode(test.stream)

			assertFrames(t, frames, test.want)
			if decoder.invalidFrames != test.invalid {
				t.Errorf("invalid frames = %d, want %d", decoder.invalidFrames, test.invalid)
			}
		})
	}
}

func TestFramesMatchTheReferenceVectors(t *testing.T) {
	tests := []struct {
		name        string
		protocol    uint16
		information string
		encoding    frameEncoding
		want        string
	}{
		{
			name:        "an LCP request, conservatively framed",
			protocol:    protocolLCP,
			information: lcpRequestVector,
			encoding:    conservativeEncoding(),
			want: "7E FF 7D 23 C0 21 7D 21 7D 21 7D 20 7D 30 7D 22 7D 26 7D 20 7D 20 7D 20 7D 20 " +
				"7D 25 7D 26 7D 5E 7D 5D 7D 3A 2B C4 EB 7E",
		},
		{
			name:        "an IPCP request, framed as negotiated",
			protocol:    protocolIPCP,
			information: ipcpRequestVector,
			encoding:    negotiatedEncoding(),
			want:        "7E 80 21 01 01 00 16 03 06 00 00 00 00 81 06 00 00 00 00 83 06 00 00 00 00 0E 4B 7E",
		},
		{
			name:        "a Protocol-Reject of CCP to a peer whose ACCM asks for XON/XOFF escaping",
			protocol:    protocolLCP,
			information: "08 02 00 15 80 FD 01 01 00 0F 11 05 00 01 03 12 06 00 00 00 01",
			encoding:    frameEncoding{accm: 0x000A_0000},
			want: "7E FF 03 C0 21 08 02 00 15 80 FD 01 01 00 0F 7D 31 05 00 01 03 12 06 00 00 00 01 " +
				"D3 99 7E",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := frameBytes(test.protocol, hexBytes(t, test.information), test.encoding)

			if want := hexBytes(t, test.want); !bytes.Equal(got, want) {
				t.Errorf("frame = % X, want % X", got, want)
			}
		})
	}
}

func TestDecodesTheReferenceReceiveStream(t *testing.T) {
	// Modem text, a frame, an aborted frame, a frame sharing its opening
	// flag, empty frames, and the modem's NO CARRIER without a flag after
	// it.
	stream := hexBytes(t, "0D 0A 43 4F 4E 4E 45 43 54 20 32 34 30 30 0D 0A "+
		"7E FF 7D 23 C0 21 7D 21 7D 21 7D 20 7D 30 7D 22 7D 26 7D 20 7D 20 7D 20 7D 20 "+
		"7D 25 7D 26 7D 5E 7D 5D 7D 3A 2B C4 EB 7E 41 42 7D 7E "+
		"80 21 01 01 00 16 03 06 00 00 00 00 81 06 00 00 00 00 83 06 00 00 00 00 0E 4B 7E "+
		"7E 7E 0D 0A 4E 4F 20 43 41 52 52 49 45 52 0D 0A")
	decoder := newFrameDecoder(int(DefaultMRU))
	decoder.setReceiveACCM(0x0000_2000)

	frames := decoder.decode(stream)

	assertFrames(t, frames, []frame{
		{protocol: protocolLCP, information: hexBytes(t, lcpRequestVector)},
		{protocol: protocolIPCP, information: hexBytes(t, ipcpRequestVector)},
	})
	if decoder.invalidFrames != 2 {
		t.Errorf("invalid frames = %d, want 2: the modem text and the aborted frame", decoder.invalidFrames)
	}
}

func TestDecodesAnEscapedControlEscape(t *testing.T) {
	// Peers may escape any byte; pppd's `escape 5d` sends 0x5D as 7D 7D.
	encoded := frameBytes(protocolIPv4, []byte{0x5D, 0x41}, conservativeEncoding())
	var escaped []byte
	for _, b := range encoded {
		if b == 0x5D {
			escaped = append(escaped, controlEscape, controlEscape)
		} else {
			escaped = append(escaped, b)
		}
	}

	frames := decodeAll(escaped)

	assertFrames(t, frames, []frame{{protocol: protocolIPv4, information: []byte{0x5D, 0x41}}})
}

func TestDropsControlCharactersThePeerShouldHaveEscaped(t *testing.T) {
	// The frame starts 7E FF 7D 23 7D 20 21: the flag, the address, then the
	// control field and the protocol's first octet, each escaped.
	tests := []struct {
		name string
		// at is where flow control inserts an XON into the frame.
		at int
	}{
		{"between two escape sequences", 4},
		// RFC 1662 §4.2: a control character the receiving ACCM flags is
		// removed before a Control Escape applies, so the escape applies to
		// the octet after the XON.
		{"inside an escape sequence", 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded := frameBytes(protocolIPv4, []byte("data"), conservativeEncoding())
			encoded = slices.Insert(encoded, test.at, 0x11) // an XON inserted by flow control
			decoder := newFrameDecoder(int(DefaultMRU))
			decoder.setReceiveACCM(1 << 0x11)

			frames := decoder.decode(encoded)

			assertFrames(t, frames, []frame{{protocol: protocolIPv4, information: []byte("data")}})
		})
	}
}

func TestFramesTooShortOrWithoutAProtocolAreInvalid(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"nothing but the FCS", nil},
		{"address and control alone", []byte{allStationsAddress, unnumberedInformation}},
		{"a protocol field with two even octets", []byte{allStationsAddress, unnumberedInformation, 0x80, 0x20}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fcs := fcsOf(test.body)
			raw := append(slices.Clone(test.body), byte(fcs), byte(fcs>>8))
			onTheLine := append(appendEscaped([]byte{flag}, raw, DefaultACCM), flag)
			decoder := newFrameDecoder(int(DefaultMRU))

			frames := decoder.decode(onTheLine)

			if len(frames) != 0 || decoder.invalidFrames != 1 {
				t.Errorf("decoded %+v with %d invalid frames, want none and 1", frames, decoder.invalidFrames)
			}
		})
	}
}

func TestAFrameMayCarryNothingButItsProtocolField(t *testing.T) {
	tests := []struct {
		name     string
		encoding frameEncoding
	}{
		{"of two octets", conservativeEncoding()},
		{"of one octet, compressed", negotiatedEncoding()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frames := decodeAll(frameBytes(protocolIPv4, nil, test.encoding))

			assertFrames(t, frames, []frame{{protocol: protocolIPv4}})
		})
	}
}

// FuzzFrameDecoder checks that no byte stream, however it is split and
// whatever the receive ACCM, makes the decoder panic, and that it never
// returns a frame longer than it accepts.
func FuzzFrameDecoder(f *testing.F) {
	f.Add(frameBytes(protocolLCP, []byte{1, 1, 0, 4}, conservativeEncoding()), 3, uint32(0x0000_2000))
	f.Add([]byte("\r\nNO CARRIER\r\n\x7e\x7d\x7e\x7e"), 1, DefaultACCM)
	f.Add(bytes.Repeat([]byte{flag, controlEscape}, 8), 0, uint32(0))
	f.Fuzz(func(t *testing.T, stream []byte, split int, receiveACCM uint32) {
		const maxInformationLen = 64
		decoder := newFrameDecoder(maxInformationLen)
		decoder.setReceiveACCM(receiveACCM)
		split = min(max(split, 0), len(stream))

		frames := append(decoder.decode(stream[:split]), decoder.decode(stream[split:])...)

		// Of the most the decoder keeps of a frame, the FCS and at least one
		// octet of protocol field are not information.
		longest := maxInformationLen + maxHeaderLen - 1
		for _, received := range frames {
			if len(received.information) > longest {
				t.Fatalf("decoded a %d-byte information field, longer than %d", len(received.information), longest)
			}
		}
		if flags := uint64(bytes.Count(stream, []byte{flag})); decoder.invalidFrames+uint64(len(frames)) > flags {
			t.Fatalf("%d invalid and %d good frames from %d flags", decoder.invalidFrames, len(frames), flags)
		}
	})
}

// FuzzFrameRoundTrip checks that every frame decodes to what was encoded,
// whatever the protocol, information and encoding.
func FuzzFrameRoundTrip(f *testing.F) {
	f.Add(protocolIPCP, hexBytes(f, ipcpRequestVector), uint32(0), true, true)
	f.Add(protocolIPv4, []byte{0x7E, 0x7D, 0x11, 0x13, 0x00, 0xFF}, DefaultACCM, false, true)
	f.Add(uint16(0x00FD), []byte{0x03, 0xFF}, uint32(0x000A_0000), true, true)
	f.Fuzz(func(t *testing.T, protocol uint16, information []byte, accm uint32, compressHeader, compressProtocol bool) {
		protocol = validProtocol(protocol)
		encoding := frameEncoding{accm: accm, compressAddressAndControl: compressHeader, compressProtocol: compressProtocol}
		decoder := newFrameDecoder(len(information))
		decoder.setReceiveACCM(accm)

		frames := decoder.decode(frameBytes(protocol, information, encoding))

		if len(frames) != 1 || frames[0].protocol != protocol || !bytes.Equal(frames[0].information, information) {
			t.Fatalf("protocol %#04x, % X with %+v decoded as %+v", protocol, information, encoding, frames)
		}
	})
}

// validProtocol turns any number into a valid PPP protocol number: the low
// bit of its high octet clear, that of its low octet set (RFC 1661 §2), and
// not 0x00FF, which is reserved because its compressed form reads as an
// address field.
func validProtocol(number uint16) uint16 {
	number = number&^0x0100 | 0x0001
	if number == 0x00FF {
		return protocolIPv4
	}
	return number
}
