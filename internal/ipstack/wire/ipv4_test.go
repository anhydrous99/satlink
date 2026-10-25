package wire

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"
)

// ipv4PacketBytes is a fragment of an ICMP datagram, with flags and fields
// that are all non-zero.
func ipv4PacketBytes() []byte {
	return []byte{
		0x45, 0x00, 0x00, 0x1e, 0x01, 0x02, 0x62, 0x03, 0x1a, 0x01, 0xd5, 0x6e, 0x11, 0x12, 0x13,
		0x14, 0x21, 0x22, 0x23, 0x24, 0xaa, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff,
	}
}

// ipv4PayloadBytes is the payload of ipv4PacketBytes.
func ipv4PayloadBytes() []byte {
	return []byte{0xaa, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff}
}

// ipv4ReprPacketBytes is the datagram
// ipv4PacketRepr describes, with ipv4ReprPayloadBytes as its payload.
func ipv4ReprPacketBytes() []byte {
	return []byte{
		0x45, 0x00, 0x00, 0x18, 0x00, 0x00, 0x40, 0x00, 0x40, 0x01, 0xd2, 0x79, 0x11, 0x12, 0x13,
		0x14, 0x21, 0x22, 0x23, 0x24, 0xaa, 0x00, 0x00, 0xff,
	}
}

func ipv4ReprPayloadBytes() []byte {
	return []byte{0xaa, 0x00, 0x00, 0xff}
}

func ipv4PacketRepr() IPv4Repr {
	return IPv4Repr{
		SrcAddr:    netip.AddrFrom4([4]byte{0x11, 0x12, 0x13, 0x14}),
		DstAddr:    netip.AddrFrom4([4]byte{0x21, 0x22, 0x23, 0x24}),
		NextHeader: IPProtocolICMP,
		PayloadLen: 4,
		HopLimit:   64,
	}
}

// ipv4RouterAlertPacketBytes is the datagram of ipv4ReprPacketBytes with a
// Router Alert option (RFC 2113), 94 04 00 00, in a header of 24 octets. The
// checksum was worked out by hand: the header's words, its checksum field
// taken as zero, add up to 0x1c28d; the carry folds in to give 0xc28e, whose
// complement is the checksum, 0x3d71. Without the option's words, 0x9404
// 0x0000, it would be 0xd175.
func ipv4RouterAlertPacketBytes() []byte {
	return []byte{
		0x46, 0x00, 0x00, 0x1c, 0x00, 0x00, 0x40, 0x00, 0x40, 0x01, 0x3d, 0x71, 0x11, 0x12, 0x13,
		0x14, 0x21, 0x22, 0x23, 0x24, 0x94, 0x04, 0x00, 0x00, 0xaa, 0x00, 0x00, 0xff,
	}
}

func TestIPv4PacketDeconstruct(t *testing.T) {
	t.Parallel()
	packet := IPv4Packet(ipv4PacketBytes())

	checkFields(t, []field{
		{"Version", packet.Version(), uint8(4)},
		{"HeaderLen", packet.HeaderLen(), 20},
		{"TotalLen", packet.TotalLen(), uint16(30)},
		{"MoreFragments", packet.MoreFragments(), true},
		{"FragmentOffset", packet.FragmentOffset(), uint16(0x203 * 8)},
		{"HopLimit", packet.HopLimit(), uint8(0x1a)},
		{"NextHeader", packet.NextHeader(), IPProtocolICMP},
		{"Checksum", packet.Checksum(), uint16(0xd56e)},
		{"SrcAddr", packet.SrcAddr(), netip.AddrFrom4([4]byte{0x11, 0x12, 0x13, 0x14})},
		{"DstAddr", packet.DstAddr(), netip.AddrFrom4([4]byte{0x21, 0x22, 0x23, 0x24})},
		{"VerifyChecksum", packet.VerifyChecksum(), true},
	})
	if !bytes.Equal(packet.Payload(), ipv4PayloadBytes()) {
		t.Errorf("Payload = % x, want % x", packet.Payload(), ipv4PayloadBytes())
	}
}

func TestIPv4PacketConstruct(t *testing.T) {
	t.Parallel()
	packet := IPv4Packet(filled(30, 0xa5))

	packet.SetVersion(4)
	packet.SetHeaderLen(20)
	packet.ClearFlags()
	packet.SetDSCP(0)
	packet.SetECN(0)
	packet.SetTotalLen(30)
	packet.SetID(0x102)
	packet.SetMoreFragments(true)
	packet.SetDontFragment(true)
	packet.SetFragmentOffset(0x203 * 8)
	packet.SetHopLimit(0x1a)
	packet.SetNextHeader(IPProtocolICMP)
	packet.SetSrcAddr(netip.AddrFrom4([4]byte{0x11, 0x12, 0x13, 0x14}))
	packet.SetDstAddr(netip.AddrFrom4([4]byte{0x21, 0x22, 0x23, 0x24}))
	packet.FillChecksum()
	copy(packet.Payload(), ipv4PayloadBytes())

	if !bytes.Equal(packet, ipv4PacketBytes()) {
		t.Errorf("packet = % x, want % x", []byte(packet), ipv4PacketBytes())
	}
}

func TestIPv4PacketOverlong(t *testing.T) {
	t.Parallel()
	overlong := append(ipv4PacketBytes(), 0)

	if got := len(IPv4Packet(overlong).Payload()); got != len(ipv4PayloadBytes()) {
		t.Errorf("len(Payload) = %d, want %d", got, len(ipv4PayloadBytes()))
	}
}

func TestIPv4PacketTotalLenOverflow(t *testing.T) {
	t.Parallel()
	buffer := ipv4PacketBytes()
	IPv4Packet(buffer).SetTotalLen(128)

	if _, err := NewIPv4Packet(buffer); !errors.Is(err, ErrMalformed) {
		t.Errorf("NewIPv4Packet = %v, want ErrMalformed", err)
	}
}

func TestIPv4ReprParse(t *testing.T) {
	t.Parallel()
	repr, err := ParseIPv4Repr(IPv4Packet(ipv4ReprPacketBytes()), ChecksumCapabilities{})

	if err != nil || repr != ipv4PacketRepr() {
		t.Errorf("ParseIPv4Repr = %v, %v; want %v", repr, err, ipv4PacketRepr())
	}
}

func TestIPv4ReprParseBadVersion(t *testing.T) {
	t.Parallel()
	packet := IPv4Packet(ipv4ReprPacketBytes())
	packet.SetVersion(6)
	packet.FillChecksum()

	if _, err := ParseIPv4Repr(packet, ChecksumCapabilities{}); !errors.Is(err, ErrMalformed) {
		t.Errorf("ParseIPv4Repr = %v, want ErrMalformed", err)
	}
}

func TestIPv4PacketParseTotalLenLessThanHeaderLen(t *testing.T) {
	t.Parallel()
	buffer := make([]byte, 40)
	buffer[0] = 0x09

	if _, err := NewIPv4Packet(buffer); !errors.Is(err, ErrMalformed) {
		t.Errorf("NewIPv4Packet = %v, want ErrMalformed", err)
	}
}

func TestIPv4PacketParseSmallIHL(t *testing.T) {
	t.Parallel()
	buffer := ipv4ReprPacketBytes()
	IPv4Packet(buffer).SetHeaderLen(16)

	if _, err := NewIPv4Packet(buffer); !errors.Is(err, ErrMalformed) {
		t.Errorf("NewIPv4Packet = %v, want ErrMalformed", err)
	}
}

func TestIPv4ReprEmit(t *testing.T) {
	t.Parallel()
	repr := ipv4PacketRepr()
	packet := IPv4Packet(filled(repr.BufferLen(), 0xa5))

	repr.Emit(packet, ChecksumCapabilities{})
	copy(packet.Payload(), ipv4ReprPayloadBytes())

	if !bytes.Equal(packet, ipv4ReprPacketBytes()) {
		t.Errorf("packet = % x, want % x", []byte(packet), ipv4ReprPacketBytes())
	}
}

func TestNewIPv4PacketChecksTheLengthFields(t *testing.T) {
	t.Parallel()
	cases := map[string]func(packet IPv4Packet) IPv4Packet{
		"shorter than a header": func(packet IPv4Packet) IPv4Packet { return packet[:IPv4HeaderLen-1] },
		"shorter than its header length": func(packet IPv4Packet) IPv4Packet {
			packet.SetHeaderLen(28)
			return packet
		},
		"shorter than its total length": func(packet IPv4Packet) IPv4Packet { return packet[:len(packet)-1] },
		"a total length below the header length": func(packet IPv4Packet) IPv4Packet {
			packet.SetTotalLen(IPv4HeaderLen - 1)
			return packet
		},
	}
	for name, corrupt := range cases {
		if _, err := NewIPv4Packet(corrupt(IPv4Packet(ipv4ReprPacketBytes()))); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: NewIPv4Packet = %v, want ErrMalformed", name, err)
		}
	}
	if _, err := NewIPv4Packet(ipv4ReprPacketBytes()); err != nil {
		t.Errorf("NewIPv4Packet of a valid datagram = %v", err)
	}
}

func TestParseIPv4ReprRejectsFragments(t *testing.T) {
	t.Parallel()
	cases := map[string]func(packet IPv4Packet){
		"the first fragment": func(packet IPv4Packet) { packet.SetMoreFragments(true) },
		"a middle fragment": func(packet IPv4Packet) {
			packet.SetMoreFragments(true)
			packet.SetFragmentOffset(1480)
		},
		"the last fragment": func(packet IPv4Packet) { packet.SetFragmentOffset(1480) },
	}
	for name, fragment := range cases {
		packet := IPv4Packet(ipv4ReprPacketBytes())
		fragment(packet)
		packet.FillChecksum()

		if _, err := ParseIPv4Repr(packet, ChecksumCapabilities{}); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: ParseIPv4Repr = %v, want ErrMalformed", name, err)
		}
	}
}

func TestParseIPv4ReprChecksTheLength(t *testing.T) {
	t.Parallel()
	truncated := IPv4Packet(ipv4ReprPacketBytes()[:23])

	if _, err := ParseIPv4Repr(truncated, ChecksumCapabilities{}); !errors.Is(err, ErrMalformed) {
		t.Errorf("ParseIPv4Repr of a truncated datagram = %v, want ErrMalformed", err)
	}
}

func TestParseIPv4ReprVerifiesTheChecksumOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	packet := IPv4Packet(ipv4ReprPacketBytes())
	packet.SetChecksum(packet.Checksum() + 1)

	if _, err := ParseIPv4Repr(packet, ChecksumCapabilities{}); !errors.Is(err, ErrMalformed) {
		t.Errorf("ParseIPv4Repr with a bad checksum = %v, want ErrMalformed", err)
	}
	for _, ignored := range []Checksum{ChecksumOnSend, ChecksumNone} {
		repr, err := ParseIPv4Repr(packet, ChecksumCapabilities{IPv4: ignored})
		if err != nil || repr != ipv4PacketRepr() {
			t.Errorf("ParseIPv4Repr not verifying = %v, %v; want %v", repr, err, ipv4PacketRepr())
		}
	}
}

func TestParseIPv4ReprAcceptsOptionsAndAnyDSCPECNAndTTL(t *testing.T) {
	t.Parallel()
	packet := IPv4Packet(ipv4RouterAlertPacketBytes())
	packet.SetDSCP(0x2e)
	packet.SetECN(0x3)
	packet.SetHopLimit(1)
	packet.FillChecksum()

	repr, err := ParseIPv4Repr(packet, ChecksumCapabilities{})

	want := ipv4PacketRepr()
	want.HopLimit = 1
	if err != nil || repr != want {
		t.Errorf("ParseIPv4Repr = %v, %v; want %v", repr, err, want)
	}
	if !bytes.Equal(packet.Payload(), ipv4ReprPayloadBytes()) {
		t.Errorf("Payload = % x, want the octets after the options, % x", packet.Payload(), ipv4ReprPayloadBytes())
	}
}

func TestIPv4PacketChecksumCoversTheOptions(t *testing.T) {
	t.Parallel()
	refilled := IPv4Packet(ipv4RouterAlertPacketBytes())
	refilled.SetChecksum(0)
	refilled.FillChecksum()
	anotherAlertValue := IPv4Packet(ipv4RouterAlertPacketBytes())
	anotherAlertValue[IPv4HeaderLen+3] = 0x01 // the option's value, 0, becomes 1

	checkFields(t, []field{
		{"VerifyChecksum", IPv4Packet(ipv4RouterAlertPacketBytes()).VerifyChecksum(), true},
		{"VerifyChecksum with another option value", anotherAlertValue.VerifyChecksum(), false},
		{"FillChecksum", refilled.Checksum(), uint16(0x3d71)},
	})
}

func TestIPv4ReprEmitWithoutChecksumWritesZero(t *testing.T) {
	t.Parallel()
	for _, notComputed := range []Checksum{ChecksumOnReceive, ChecksumNone} {
		packet := IPv4Packet(filled(IPv4HeaderLen, 0xa5))

		ipv4PacketRepr().Emit(packet, ChecksumCapabilities{IPv4: notComputed})

		if packet.Checksum() != 0 {
			t.Errorf("Checksum = %#04x, want 0", packet.Checksum())
		}
	}
}

func TestIPv4PacketDSCPAndECNShareAnOctet(t *testing.T) {
	t.Parallel()
	packet := IPv4Packet(make([]byte, IPv4HeaderLen))

	packet.SetDSCP(0x3f)
	packet.SetECN(0x2)
	packet.SetDSCP(0x15)

	// DSCP 0x15 in the high six bits, ECN 2 in the low two.
	if got := packet[ipv4DSCPAndECNOffset]; got != 0x56 {
		t.Errorf("DSCP and ECN octet = %#02x, want 0x56", got)
	}
}

func TestIPv4PacketFlagsKeepTheFragmentOffset(t *testing.T) {
	t.Parallel()
	packet := IPv4Packet(make([]byte, IPv4HeaderLen))
	packet.SetFragmentOffset(0x1fff * 8)
	packet.SetDontFragment(true)
	packet.SetMoreFragments(true)

	packet.SetDontFragment(false)
	// MF and the offset 0x1fff, without DF.
	if got := packet.flagsAndOffset(); got != 0x3fff {
		t.Errorf("after clearing DF: flags and offset %#04x, want 0x3fff", got)
	}
	packet.ClearFlags()
	if packet.MoreFragments() || packet.FragmentOffset() != 0x1fff*8 {
		t.Errorf("after ClearFlags: MF %v offset %d", packet.MoreFragments(), packet.FragmentOffset())
	}
}

func TestIPv4ReprLengthsAndString(t *testing.T) {
	t.Parallel()
	repr := ipv4PacketRepr()

	checkFields(t, []field{
		{"HeaderLen", repr.HeaderLen(), 20},
		{"BufferLen", repr.BufferLen(), 24},
		{"String", repr.String(), "IPv4 src=17.18.19.20 dst=33.34.35.36 proto=ICMP"},
	})
}

func TestIPv4AddressSettersPanicOnAddressesThatAreNotIPv4(t *testing.T) {
	t.Parallel()
	packet := IPv4Packet(make([]byte, IPv4HeaderLen))
	for name, addr := range addressesThatAreNotIPv4 {
		expectPanic(t, "SetSrcAddr("+name+")", func() { packet.SetSrcAddr(addr) })
		expectPanic(t, "SetDstAddr("+name+")", func() { packet.SetDstAddr(addr) })
	}
}

func TestIPv4ReprEmitPanicsOnAddressesThatAreNotIPv4(t *testing.T) {
	t.Parallel()
	for name, addr := range addressesThatAreNotIPv4 {
		withBadSource, withBadDestination := ipv4PacketRepr(), ipv4PacketRepr()
		withBadSource.SrcAddr = addr
		withBadDestination.DstAddr = addr
		packet := IPv4Packet(make([]byte, IPv4HeaderLen))

		expectPanic(t, "Emit with a "+name+" source", func() { withBadSource.Emit(packet, ChecksumCapabilities{}) })
		expectPanic(t, "Emit with a "+name+" destination", func() {
			withBadDestination.Emit(packet, ChecksumCapabilities{IPv4: ChecksumNone})
		})
	}
}

// It also checks a /30 case, the smallest network that has a broadcast address.
func TestIPv4CIDRBroadcast(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"0.0.0.0/0":          "255.255.255.255",
		"0.0.0.1/24":         "0.0.0.255",
		"0.0.0.0/32":         "",
		"127.0.0.0/8":        "127.255.255.255",
		"192.168.0.0/16":     "192.168.255.255",
		"192.168.1.1/16":     "192.168.255.255",
		"192.168.1.1/17":     "192.168.127.255",
		"172.16.0.1/12":      "172.31.255.255",
		"255.255.255.1/24":   "255.255.255.255",
		"192.168.1.1/30":     "192.168.1.3",
		"255.255.255.254/31": "",
		"255.255.255.255/32": "",
	}
	for prefix, want := range cases {
		got, ok := SubnetBroadcast(netip.MustParsePrefix(prefix))
		switch {
		case want == "" && ok:
			t.Errorf("SubnetBroadcast(%s) = %v, want none", prefix, got)
		case want != "" && (!ok || got != netip.MustParseAddr(want)):
			t.Errorf("SubnetBroadcast(%s) = %v, %v; want %s", prefix, got, ok, want)
		}
	}
}

func TestSubnetBroadcastRefusesPrefixesThatAreNotIPv4(t *testing.T) {
	t.Parallel()
	prefixes := []netip.Prefix{
		{},
		netip.MustParsePrefix("2001:db8::/64"),
		netip.MustParsePrefix("::ffff:192.168.1.0/120"),
		netip.PrefixFrom(netip.MustParseAddr("192.168.1.1"), 33),
	}
	for _, prefix := range prefixes {
		if got, ok := SubnetBroadcast(prefix); ok {
			t.Errorf("SubnetBroadcast(%v) = %v, want none", prefix, got)
		}
	}
}

func TestIsBroadcast(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"255.255.255.255":        true,
		"255.255.255.254":        false,
		"192.168.1.255":          false, // a subnet broadcast: see socket.Context.IsBroadcast
		"::ffff:255.255.255.255": false,
	}
	for address, want := range cases {
		if got := IsBroadcast(netip.MustParseAddr(address)); got != want {
			t.Errorf("IsBroadcast(%s) = %v, want %v", address, got, want)
		}
	}
	if IsBroadcast(netip.Addr{}) {
		t.Error("IsBroadcast(zero Addr) = true")
	}
}

func TestIsUnicast(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"192.168.1.1":        true,
		"10.0.0.255":         true, // a subnet broadcast only from an interface's point of view
		"127.0.0.1":          true,
		"0.0.0.0":            false,
		"255.255.255.255":    false,
		"224.0.0.1":          false,
		"239.255.255.250":    false,
		"::1":                false,
		"::ffff:192.168.1.1": false,
	}
	for address, want := range cases {
		if got := IsUnicast(netip.MustParseAddr(address)); got != want {
			t.Errorf("IsUnicast(%s) = %v, want %v", address, got, want)
		}
	}
	if IsUnicast(netip.Addr{}) {
		t.Error("IsUnicast(zero Addr) = true")
	}
}
