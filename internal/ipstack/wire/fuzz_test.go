package wire

import (
	"bytes"
	"testing"
)

// The fuzz tests feed arbitrary octets to every function that checks or
// parses a packet. None may panic, whatever it gets: the stack, satnet's
// pacing and the simulator parse whatever arrives over the line. Each test
// also checks what a successful check or parse promises: a packet that
// passed its New… constructor keeps every octet when each field it reads is
// written back, and a parsed Repr survives Emit and a second parse unchanged.

// addSeeds adds every seed to the corpus.
func addSeeds(f *testing.F, seeds ...[]byte) {
	f.Helper()
	for _, seed := range seeds {
		f.Add(seed)
	}
}

// checkRewrite checks that rewrite, which writes every field it reads from
// packet back into a copy of it, leaves every octet as it was.
func checkRewrite(t *testing.T, packet []byte, rewrite func(rewritten []byte)) {
	t.Helper()
	rewritten := bytes.Clone(packet)
	rewrite(rewritten)
	if !bytes.Equal(rewritten, packet) {
		t.Fatalf("writing back the fields of % x gives % x", packet, rewritten)
	}
}

func FuzzNewIPv4Packet(f *testing.F) {
	addSeeds(f, ipv4PacketBytes(), ipv4ReprPacketBytes(), make([]byte, IPv4HeaderLen), nil)
	f.Fuzz(func(t *testing.T, data []byte) {
		packet, err := NewIPv4Packet(data)
		if err != nil {
			return
		}
		headerLen, totalLen := packet.HeaderLen(), int(packet.TotalLen())
		if headerLen < IPv4HeaderLen || headerLen > totalLen || totalLen > len(data) ||
			len(packet.Payload()) != totalLen-headerLen {
			t.Fatalf("header length %d, total length %d, payload %d octets, in %d octets",
				headerLen, totalLen, len(packet.Payload()), len(data))
		}
		checkRewrite(t, packet, func(rewritten []byte) { rewriteIPv4Header(IPv4Packet(rewritten), packet) })
		filledIn := IPv4Packet(bytes.Clone(data))
		if filledIn.FillChecksum(); !filledIn.VerifyChecksum() {
			t.Fatalf("% x fails verification after FillChecksum", []byte(filledIn))
		}
	})
}

// rewriteIPv4Header writes every header field the package reads from packet
// into rewritten.
func rewriteIPv4Header(rewritten, packet IPv4Packet) {
	rewritten.SetVersion(packet.Version())
	rewritten.SetHeaderLen(packet.HeaderLen())
	rewritten.SetTotalLen(packet.TotalLen())
	rewritten.SetMoreFragments(packet.MoreFragments())
	rewritten.SetFragmentOffset(packet.FragmentOffset())
	rewritten.SetHopLimit(packet.HopLimit())
	rewritten.SetNextHeader(packet.NextHeader())
	rewritten.SetChecksum(packet.Checksum())
	rewritten.SetSrcAddr(packet.SrcAddr())
	rewritten.SetDstAddr(packet.DstAddr())
}

func FuzzParseIPv4Repr(f *testing.F) {
	addSeeds(f, ipv4PacketBytes(), ipv4ReprPacketBytes(), icmpv4PortUnreachableBytes()[icmpv4HeaderLen:])
	f.Fuzz(func(t *testing.T, data []byte) {
		packet := IPv4Packet(data)
		verified, verifiedErr := ParseIPv4Repr(packet, ChecksumCapabilities{})
		repr, err := ParseIPv4Repr(packet, ChecksumCapabilities{IPv4: ChecksumNone})
		if verifiedErr == nil && (err != nil || verified != repr) {
			t.Fatalf("verified %v but unverified %v, %v", verified, repr, err)
		}
		if err != nil {
			return
		}
		datagram := IPv4Packet(make([]byte, repr.BufferLen()))
		repr.Emit(datagram, ChecksumCapabilities{})
		copy(datagram.Payload(), packet.Payload())
		reparsed, err := ParseIPv4Repr(datagram, ChecksumCapabilities{})
		if err != nil || reparsed != repr || !bytes.Equal(datagram.Payload(), packet.Payload()) {
			t.Fatalf("emitted %v as % x, which parses as %v, %v", repr, []byte(datagram), reparsed, err)
		}
	})
}

func FuzzNewTCPPacket(f *testing.F) {
	addSeeds(f, tcpPacketBytes(), tcpSYNPacketBytes(), make([]byte, TCPHeaderLen), nil)
	f.Fuzz(func(t *testing.T, data []byte) {
		packet, err := NewTCPPacket(data)
		if err != nil {
			return
		}
		headerLen := packet.HeaderLen()
		if headerLen < TCPHeaderLen || headerLen > len(data) || headerLen%tcpOptionsAlignment != 0 ||
			len(packet.Options()) != headerLen-TCPHeaderLen || len(packet.Payload()) != len(data)-headerLen {
			t.Fatalf("header length %d, %d octets of options and %d of payload, in %d octets",
				headerLen, len(packet.Options()), len(packet.Payload()), len(data))
		}
		checkRewrite(t, packet, func(rewritten []byte) { rewriteTCPHeader(TCPPacket(rewritten), packet) })
		filledIn := TCPPacket(bytes.Clone(data))
		if filledIn.FillChecksum(testSrcAddr, testDstAddr); !filledIn.VerifyChecksum(testSrcAddr, testDstAddr) {
			t.Fatalf("% x fails verification after FillChecksum", []byte(filledIn))
		}
	})
}

// rewriteTCPHeader writes every field of the fixed header that the package
// reads from packet into rewritten.
func rewriteTCPHeader(rewritten, packet TCPPacket) {
	rewritten.SetSrcPort(packet.SrcPort())
	rewritten.SetDstPort(packet.DstPort())
	rewritten.SetSeqNumber(packet.SeqNumber())
	rewritten.SetAckNumber(packet.AckNumber())
	rewritten.SetHeaderLen(packet.HeaderLen())
	rewritten.SetFIN(packet.FIN())
	rewritten.SetSYN(packet.SYN())
	rewritten.SetRST(packet.RST())
	rewritten.SetPSH(packet.PSH())
	rewritten.SetACK(packet.ACK())
	rewritten.SetWindowLen(packet.WindowLen())
	rewritten.SetChecksum(packet.Checksum())
}

func FuzzParseTCPRepr(f *testing.F) {
	repr := synWithEveryOption()
	withOptions := make([]byte, repr.BufferLen())
	repr.Emit(TCPPacket(withOptions), testSrcAddr, testDstAddr, ChecksumCapabilities{})
	addSeeds(f, tcpPacketBytes(), tcpSYNPacketBytes(), withOptions)
	f.Fuzz(func(t *testing.T, data []byte) {
		packet := TCPPacket(data)
		verified, verifiedErr := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{})
		repr, err := ParseTCPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{TCP: ChecksumNone})
		if verifiedErr == nil && (err != nil || !verified.Equal(repr)) {
			t.Fatalf("verified %v but unverified %v, %v", verified, repr, err)
		}
		if err != nil {
			return
		}
		if repr.HeaderLen() > len(data) {
			t.Fatalf("%v needs a %d-octet header, from %d octets", repr, repr.HeaderLen(), len(data))
		}
		checkTCPReprRoundTrip(t, repr)
	})
}

// checkTCPReprRoundTrip checks that a parsed repr emits as a segment that
// parses as the same repr, but for SACK blocks, which Emit writes only on
// an acknowledgement without SACK-permitted.
func checkTCPReprRoundTrip(t *testing.T, repr TCPRepr) {
	t.Helper()
	if !repr.AckNumber.IsSome() || repr.SACKPermitted {
		repr.SACKRanges = [MaxSACKRanges]Optional[SACKRange]{}
	}
	segment := TCPPacket(make([]byte, repr.BufferLen()))
	repr.Emit(segment, testSrcAddr, testDstAddr, ChecksumCapabilities{})
	reparsed, err := ParseTCPRepr(segment, testSrcAddr, testDstAddr, ChecksumCapabilities{})
	if err != nil || !reparsed.Equal(repr) {
		t.Fatalf("emitted %+v as % x, which parses as %+v, %v", repr, []byte(segment), reparsed, err)
	}
}

func FuzzParseTCPOption(f *testing.F) {
	addSeeds(f, []byte{0x00}, []byte{0x01}, []byte{0x02, 0x04, 0x05, 0xdc}, []byte{0x03, 0x03, 0x0c},
		[]byte{0x04, 0x02}, []byte{0x08, 0x0a, 0, 0, 0, 1, 0, 0, 0, 2}, []byte{0x0c, 0x05, 0x01, 0x02, 0x03},
		[]byte{0x05, 0x0a, 0x00, 0x00, 0x01, 0xf4, 0x00, 0x00, 0x05, 0xdc})
	f.Fuzz(func(t *testing.T, data []byte) {
		option, rest, err := parseTCPOption(data)
		if err != nil {
			return
		}
		consumed := data[:len(data)-len(rest)]
		if !bytes.Equal(data[len(consumed):], rest) || len(consumed) == 0 {
			t.Fatalf("parsing % x left % x", data, rest)
		}
		emitted := make([]byte, option.bufferLen())
		if left := option.emit(emitted); len(left) != 0 {
			t.Fatalf("emitting %+v left %d octets", option, len(left))
		}
		// Only a SACK option of more than three blocks loses octets.
		if len(emitted) == len(consumed) && !bytes.Equal(emitted, consumed) {
			t.Fatalf("% x parses as %+v, which emits as % x", consumed, option, emitted)
		}
	})
}

func FuzzNewUDPPacket(f *testing.F) {
	addSeeds(f, udpPacketBytes(), udpNoChecksumPacketBytes(), make([]byte, UDPHeaderLen), nil)
	f.Fuzz(func(t *testing.T, data []byte) {
		packet, err := NewUDPPacket(data)
		if err != nil {
			return
		}
		length := int(packet.Length())
		if length < UDPHeaderLen || length > len(data) || len(packet.Payload()) != length-UDPHeaderLen {
			t.Fatalf("length %d, %d octets of payload, in %d octets", length, len(packet.Payload()), len(data))
		}
		checkRewrite(t, packet, func(rewritten []byte) {
			datagram := UDPPacket(rewritten)
			datagram.SetSrcPort(packet.SrcPort())
			datagram.SetDstPort(packet.DstPort())
			datagram.SetLength(packet.Length())
			datagram.SetChecksum(packet.Checksum())
		})
		filledIn := UDPPacket(bytes.Clone(data))
		filledIn.FillChecksum(testSrcAddr, testDstAddr)
		if filledIn.Checksum() == udpNoChecksum || !filledIn.VerifyChecksum(testSrcAddr, testDstAddr) {
			t.Fatalf("% x fails verification after FillChecksum", []byte(filledIn))
		}
	})
}

func FuzzParseUDPRepr(f *testing.F) {
	addSeeds(f, udpPacketBytes(), udpNoChecksumPacketBytes())
	f.Fuzz(func(t *testing.T, data []byte) {
		packet := UDPPacket(data)
		verified, verifiedErr := ParseUDPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{})
		repr, err := ParseUDPRepr(packet, testSrcAddr, testDstAddr, ChecksumCapabilities{UDP: ChecksumNone})
		if verifiedErr == nil && (err != nil || verified != repr) {
			t.Fatalf("verified %v but unverified %v, %v", verified, repr, err)
		}
		if err != nil {
			return
		}
		datagram := UDPPacket(make([]byte, repr.HeaderLen()+len(packet.Payload())))
		repr.Emit(datagram, testSrcAddr, testDstAddr, packet.Payload(), ChecksumCapabilities{})
		reparsed, err := ParseUDPRepr(datagram, testSrcAddr, testDstAddr, ChecksumCapabilities{})
		if err != nil || reparsed != repr || !bytes.Equal(datagram.Payload(), packet.Payload()) {
			t.Fatalf("emitted %v as % x, which parses as %v, %v", repr, []byte(datagram), reparsed, err)
		}
	})
}

func FuzzNewDNSPacket(f *testing.F) {
	addSeeds(f, dnsCNAMEResponseBytes(), make([]byte, dnsHeaderLen), nil)
	f.Fuzz(func(t *testing.T, data []byte) {
		packet, err := NewDNSPacket(data)
		if err != nil {
			return
		}
		if len(packet.Payload()) != len(data)-dnsHeaderLen || packet.Flags()&^allDNSFlags != 0 {
			t.Fatalf("%d octets of payload and flags %#04x, in %d octets", len(packet.Payload()), packet.Flags(), len(data))
		}
		checkRewrite(t, packet, func(rewritten []byte) {
			message := DNSPacket(rewritten)
			message.SetTransactionID(packet.TransactionID())
			message.SetFlags(packet.Flags())
			message.SetOpcode(packet.Opcode())
			message.SetQuestionCount(packet.QuestionCount())
			message.SetAnswerRecordCount(packet.AnswerRecordCount())
		})
	})
}

func FuzzParseDNSQuestion(f *testing.F) {
	addSeeds(f, dnsCNAMEResponseBytes()[dnsHeaderLen:], append(encodedName("echo", "test"), 0, 1, 0, 1))
	f.Fuzz(func(t *testing.T, data []byte) {
		question, rest, err := ParseDNSQuestion(data)
		if err != nil {
			return
		}
		if !bytes.HasPrefix(data, question.Name) || !bytes.HasSuffix(data, rest) ||
			question.BufferLen()+len(rest) != len(data) {
			t.Fatalf("% x parses as name % x, with % x left", data, question.Name, rest)
		}
		emitted := make([]byte, question.BufferLen())
		question.Emit(emitted)
		reparsed, left, err := ParseDNSQuestion(emitted)
		if err != nil || !bytes.Equal(reparsed.Name, question.Name) || reparsed.Type != question.Type || len(left) != 0 {
			t.Fatalf("emitted %+v as % x, which parses as %+v, %v", question, emitted, reparsed, err)
		}
	})
}

func FuzzParseDNSRecord(f *testing.F) {
	addSeeds(f, dnsCNAMEResponseBytes()[34:], dnsRecordBytes(DNSTypeA, dnsClassIN, []byte{192, 0, 2, 1}))
	f.Fuzz(func(t *testing.T, data []byte) {
		record, rest, err := ParseDNSRecord(data)
		if err != nil {
			return
		}
		if !bytes.HasPrefix(data, record.Name) || !bytes.HasSuffix(data, rest) ||
			len(record.Name)+dnsRecordFieldsLen+len(record.Data.Raw) > len(data)-len(rest) {
			t.Fatalf("% x parses as %+v, with % x left", data, record, rest)
		}
		isA := record.Data.Type == DNSTypeA
		if isA != record.Data.Address.Is4() || isA != (record.Data.Raw == nil) {
			t.Fatalf("record of type %v has address %v and raw data % x", record.Data.Type, record.Data.Address, record.Data.Raw)
		}
	})
}

func FuzzDNSPacketNameLabels(f *testing.F) {
	f.Add(dnsCNAMEResponseBytes(), uint16(0x3f), []byte{0x04, 'm', 'a', 'i', 'l', 0xc0, 0x0c})
	f.Add(pointerChainMessage(), uint16(18), []byte{0xc0, 0x10})
	f.Fuzz(func(t *testing.T, data []byte, offset uint16, outside []byte) {
		packet := DNSPacket(data)
		for _, name := range [][]byte{data[min(int(offset), len(data)):], outside} {
			// No octet is read twice, so a name has at most one label for
			// each octet there is.
			labels := 0
			for label, err := range packet.NameLabels(name) {
				if err != nil {
					break
				}
				if labels++; labels > len(data)+len(name) || len(label) > dnsLabelLenMask {
					t.Fatalf("label %d (% x) of the name % x in % x", labels, label, name, data)
				}
			}
		}
	})
}
