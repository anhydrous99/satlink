package ppp

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestPacketRoundTripsAndIgnoresPadding(t *testing.T) {
	sent := packet{code: codeConfigureRequest, identifier: 7, data: []byte{1, 6, 0, 0, 0, 0}}
	information := append(sent.encode(), 0xAA, 0xBB)

	received, ok := parsePacket(information)

	if !ok {
		t.Fatalf("parsePacket(% x) failed", information)
	}
	assertPacket(t, received, sent)
}

func TestPacketWithInconsistentLengthIsRejected(t *testing.T) {
	tests := []struct {
		name        string
		information []byte
	}{
		{"a length below the header", []byte{1, 1, 0, 3}},
		{"a length past the end", []byte{1, 1, 0, 9, 0}},
		{"a truncated header", []byte{1, 1, 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if received, ok := parsePacket(test.information); ok {
				t.Errorf("parsePacket(% x) = %+v, want a failure", test.information, received)
			}
		})
	}
}

func TestOptionListRoundTrips(t *testing.T) {
	options := []configOption{{kind: 2, value: []byte{0, 0, 0, 0}}, {kind: 7}}

	parsed, ok := parseOptionList(encodeOptionList(options))

	if !ok {
		t.Fatal("parseOptionList failed")
	}
	assertOptions(t, parsed, options)
}

func TestMalformedOptionListsAreRejected(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"a length below the header", []byte{2, 1}},
		{"a value running past the end", []byte{2, 6, 0, 0}},
		{"a truncated header", []byte{2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if options, ok := parseOptionList(test.data); ok {
				t.Errorf("parseOptionList(% x) = %+v, want a failure", test.data, options)
			}
		})
	}
}

func TestCodesAreNamedAsTheRFCNamesThem(t *testing.T) {
	tests := []struct {
		code code
		want string
	}{
		{codeConfigureRequest, "ConfigureRequest"},
		{codeConfigureAck, "ConfigureAck"},
		{codeConfigureNak, "ConfigureNak"},
		{codeConfigureReject, "ConfigureReject"},
		{codeTerminateRequest, "TerminateRequest"},
		{codeTerminateAck, "TerminateAck"},
		{codeCodeReject, "CodeReject"},
		{codeProtocolReject, "ProtocolReject"},
		{codeEchoRequest, "EchoRequest"},
		{codeEchoReply, "EchoReply"},
		{codeDiscardRequest, "DiscardRequest"},
		{codeIdentification, "Identification"},
		{codeTimeRemaining, "TimeRemaining"},
		{0, "Code(0)"},
		{14, "Code(14)"},
		{42, "Code(42)"},
	}
	for _, test := range tests {
		if got := test.code.String(); got != test.want {
			t.Errorf("code(%d).String() = %q, want %q", uint8(test.code), got, test.want)
		}
	}
}

func TestOnlyTheFirstSevenCodesAreNegotiation(t *testing.T) {
	for value := range 256 {
		want := value >= 1 && value <= 7
		if got := code(value).isNegotiation(); got != want {
			t.Errorf("code(%d).isNegotiation() = %t, want %t", value, got, want)
		}
	}
}

func TestOptionValuesMustHaveTheirExactSize(t *testing.T) {
	short, long := configOption{value: []byte{1}}, configOption{value: []byte{1, 2, 3, 4, 5}}
	for _, option := range []configOption{short, long} {
		if _, ok := option.uint16Value(); ok {
			t.Errorf("uint16Value of % x succeeded", option.value)
		}
		if _, ok := option.uint32Value(); ok {
			t.Errorf("uint32Value of % x succeeded", option.value)
		}
		if _, ok := option.ipv4Value(); ok {
			t.Errorf("ipv4Value of % x succeeded", option.value)
		}
	}
}

func TestOptionValuesRoundTrip(t *testing.T) {
	address := netip.MustParseAddr("10.64.0.2")

	if value, ok := uint16Option(1, 1500).uint16Value(); !ok || value != 1500 {
		t.Errorf("uint16Value = %d, %t; want 1500, true", value, ok)
	}
	if value, ok := uint32Option(2, 0x000A_0000).uint32Value(); !ok || value != 0x000A_0000 {
		t.Errorf("uint32Value = %#x, %t; want 0xa0000, true", value, ok)
	}
	if value, ok := ipv4Option(3, address).ipv4Value(); !ok || value != address {
		t.Errorf("ipv4Value = %v, %t; want %v, true", value, ok, address)
	}
}

func TestTheOutboxHandsOutItsPacketsOnce(t *testing.T) {
	var out outbox
	first := packet{code: codeEchoRequest, identifier: 1}
	second := packet{code: codeEchoReply, identifier: 2}
	out.push(protocolLCP, first)
	out.push(protocolIPCP, second)

	assertOutgoing(t, out.take(), []outgoingPacket{{protocolLCP, first}, {protocolIPCP, second}})
	if waiting := out.take(); len(waiting) != 0 {
		t.Errorf("a second take returned %+v", waiting)
	}
}

// FuzzParsePacket checks that no information field makes parsePacket
// panic, and that what parses re-encodes to the octets its length covers.
func FuzzParsePacket(f *testing.F) {
	f.Add([]byte{1, 7, 0, 10, 1, 6, 0, 0, 0, 0, 0xAA, 0xBB})
	f.Add([]byte{1, 1, 0, 3})
	f.Add([]byte{1, 1, 0, 9, 0})
	f.Add([]byte{9, 1, 0, 8, 0x12, 0x34, 0x56, 0x78})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, information []byte) {
		received, ok := parsePacket(information)
		if !ok {
			return
		}
		length := binary.BigEndian.Uint16(information[2:headerLen])
		if encoded := received.encode(); !bytes.Equal(encoded, information[:length]) {
			t.Fatalf("% x parsed as %+v, which encodes as % x", information, received, encoded)
		}
	})
}

// FuzzParseOptionList checks that no data makes parseOptionList panic, and
// that a list that parses re-encodes to itself.
func FuzzParseOptionList(f *testing.F) {
	f.Add([]byte{1, 4, 0x05, 0xDC, 2, 6, 0, 0, 0, 0, 7, 2, 8, 2})
	f.Add([]byte{2, 1})
	f.Add([]byte{2, 6, 0, 0})
	f.Add([]byte{2})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		options, ok := parseOptionList(data)
		if !ok {
			return
		}
		if encoded := encodeOptionList(options); !bytes.Equal(encoded, data) {
			t.Fatalf("% x parsed as %+v, which encodes as % x", data, options, encoded)
		}
	})
}

func TestTheLargestPacketsAndOptionsStillEncode(t *testing.T) {
	// The most their length fields can say: 65535 octets for a packet, its
	// header included, and 255 for an option.
	largestPacket := packet{data: make([]byte, 65535-headerLen)}.encode()
	largestOption := configOption{kind: 13, value: make([]byte, 255-optionHeaderLen)}.appendTo(nil)

	if length := binary.BigEndian.Uint16(largestPacket[2:headerLen]); len(largestPacket) != 65535 || length != 65535 {
		t.Errorf("the largest packet encodes as %d octets, its length field %d; want 65535", len(largestPacket), length)
	}
	if len(largestOption) != 255 || largestOption[1] != 255 {
		t.Errorf("the largest option encodes as %d octets, its length field %d; want 255", len(largestOption), largestOption[1])
	}
}

func TestPacketsAndOptionsTooLargeToEncodeAreAProgrammingError(t *testing.T) {
	tests := []struct {
		name   string
		encode func()
	}{
		{"a packet over 64 KiB", func() { packet{data: make([]byte, 0x1_0000)}.encode() }},
		{"an option over 255 octets", func() { configOption{value: make([]byte, 254)}.appendTo(nil) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("it was encoded")
				}
			}()

			test.encode()
		})
	}
}
