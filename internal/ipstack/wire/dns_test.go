package wire

import (
	"bytes"
	"errors"
	"net/netip"
	"slices"
	"testing"
)

// dnsCNAMEResponseBytes is the message of smoltcp's test_parse_name and
// test_parse_response_cname: www.facebook.com is a CNAME for
// star-mini.c10r.facebook.com, which has one A record.
func dnsCNAMEResponseBytes() []byte {
	return []byte{
		0x78, 0x6c, 0x81, 0x80, 0x00, 0x01, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x03, 0x77,
		0x77, 0x77, 0x08, 0x66, 0x61, 0x63, 0x65, 0x62, 0x6f, 0x6f, 0x6b, 0x03, 0x63, 0x6f,
		0x6d, 0x00, 0x00, 0x01, 0x00, 0x01, 0xc0, 0x0c, 0x00, 0x05, 0x00, 0x01, 0x00, 0x00,
		0x05, 0xf3, 0x00, 0x11, 0x09, 0x73, 0x74, 0x61, 0x72, 0x2d, 0x6d, 0x69, 0x6e, 0x69,
		0x04, 0x63, 0x31, 0x30, 0x72, 0xc0, 0x10, 0xc0, 0x2e, 0x00, 0x01, 0x00, 0x01, 0x00,
		0x00, 0x00, 0x05, 0x00, 0x04, 0x1f, 0x0d, 0x53, 0x24,
	}
}

// encodedName encodes labels as a name on the wire, ending with the root.
func encodedName(labels ...string) []byte {
	var name []byte
	for _, label := range labels {
		name = append(name, byte(len(label)))
		name = append(name, label...)
	}
	return append(name, dnsNameEnd)
}

// nameLabels collects the labels NameLabels yields for name, up to the
// first error.
func nameLabels(packet DNSPacket, name []byte) ([]string, error) {
	var labels []string
	for label, err := range packet.NameLabels(name) {
		if err != nil {
			return labels, err
		}
		labels = append(labels, string(label))
	}
	return labels, nil
}

// Ported from smoltcp's wire/dns.rs test_parse_name.
func TestDNSPacketParseName(t *testing.T) {
	t.Parallel()
	message := dnsCNAMEResponseBytes()
	packet := DNSPacket(message)
	cases := map[int][]string{
		0x0c: {"www", "facebook", "com"},
		0x22: {"www", "facebook", "com"},
		0x2e: {"star-mini", "c10r", "facebook", "com"},
		0x3f: {"star-mini", "c10r", "facebook", "com"},
	}
	for offset, want := range cases {
		labels, err := nameLabels(packet, message[offset:])
		if err != nil || !slices.Equal(labels, want) {
			t.Errorf("labels at %#x = %q, %v; want %q", offset, labels, err, want)
		}
	}
}

// dnsMessage is a message split into its sections, as smoltcp's tests'
// Parsed holds it.
type dnsMessage struct {
	packet      DNSPacket
	questions   []DNSQuestion
	answers     []DNSRecord
	authorities []DNSRecord
	additionals []DNSRecord
}

// parseDNSMessage parses every section of message, which must leave no octet
// over (smoltcp's Parsed::parse).
func parseDNSMessage(t *testing.T, message []byte) dnsMessage {
	t.Helper()
	packet, err := NewDNSPacket(message)
	if err != nil {
		t.Fatalf("NewDNSPacket = %v", err)
	}
	parsed := dnsMessage{packet: packet}
	payload := packet.Payload()
	for range packet.QuestionCount() {
		question, rest, err := ParseDNSQuestion(payload)
		if err != nil {
			t.Fatalf("ParseDNSQuestion(% x) = %v", payload, err)
		}
		parsed.questions = append(parsed.questions, question)
		payload = rest
	}
	parsed.answers = parseDNSRecords(t, &payload, packet.AnswerRecordCount())
	parsed.authorities = parseDNSRecords(t, &payload, packet.AuthorityRecordCount())
	parsed.additionals = parseDNSRecords(t, &payload, packet.AdditionalRecordCount())
	if len(payload) != 0 {
		t.Fatalf("% x left over", payload)
	}
	return parsed
}

// parseDNSRecords parses count records from the start of payload and moves
// payload past them.
func parseDNSRecords(t *testing.T, payload *[]byte, count uint16) []DNSRecord {
	t.Helper()
	var records []DNSRecord
	for range count {
		record, rest, err := ParseDNSRecord(*payload)
		if err != nil {
			t.Fatalf("ParseDNSRecord(% x) = %v", *payload, err)
		}
		records = append(records, record)
		*payload = rest
	}
	return records
}

// checkDNSHeader compares the header of a parsed message with the wanted
// identifier, flags, response code and section counts.
func checkDNSHeader(t *testing.T, packet DNSPacket, want dnsHeader) {
	t.Helper()
	checkFields(t, []field{
		{"TransactionID", packet.TransactionID(), want.id},
		{"Flags", packet.Flags(), want.flags},
		{"Opcode", packet.Opcode(), DNSOpcodeQuery},
		{"Rcode", packet.Rcode(), want.rcode},
		{"QuestionCount", packet.QuestionCount(), want.counts[0]},
		{"AnswerRecordCount", packet.AnswerRecordCount(), want.counts[1]},
		{"AuthorityRecordCount", packet.AuthorityRecordCount(), want.counts[2]},
		{"AdditionalRecordCount", packet.AdditionalRecordCount(), want.counts[3]},
	})
}

type dnsHeader struct {
	id     uint16
	flags  DNSFlags
	rcode  DNSRcode
	counts [4]uint16
}

// aRecord is what an A record a test parses should hold: the owner's name,
// as encoded in the message, the TTL and the address.
type aRecord struct {
	name    []byte
	ttl     uint32
	address string
}

// checkARecord checks that record is the A record want.
func checkARecord(t *testing.T, record DNSRecord, want aRecord) {
	t.Helper()
	wantData := DNSRecordData{Type: DNSTypeA, Address: netip.MustParseAddr(want.address)}
	if !bytes.Equal(record.Name, want.name) || record.TTL != want.ttl || record.Data.Type != wantData.Type ||
		record.Data.Address != wantData.Address || record.Data.Raw != nil {
		t.Errorf("record = %+v, want name % x, TTL %d and %+v", record, want.name, want.ttl, wantData)
	}
}

const responseFlags = DNSFlagResponse | DNSFlagRecursionDesired | DNSFlagRecursionAvailable

// Ported from smoltcp's wire/dns.rs test_parse_request.
func TestDNSParseRequest(t *testing.T) {
	t.Parallel()
	parsed := parseDNSMessage(t, []byte{
		0x51, 0x84, 0x01, 0x20, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x06, 0x67,
		0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x03, 0x63, 0x6f, 0x6d, 0x00, 0x00, 0x01, 0x00, 0x01,
	})

	checkDNSHeader(t, parsed.packet, dnsHeader{
		id: 0x5184, flags: DNSFlagRecursionDesired | DNSFlagAuthenticData, counts: [4]uint16{1, 0, 0, 0},
	})
	if len(parsed.questions) != 1 || !bytes.Equal(parsed.questions[0].Name, encodedName("google", "com")) ||
		parsed.questions[0].Type != DNSTypeA {
		t.Errorf("questions = %+v, want one A question for google.com", parsed.questions)
	}
}

// Ported from smoltcp's wire/dns.rs test_parse_response.
func TestDNSParseResponse(t *testing.T) {
	t.Parallel()
	parsed := parseDNSMessage(t, []byte{
		0x51, 0x84, 0x81, 0x80, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x06, 0x67,
		0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x03, 0x63, 0x6f, 0x6d, 0x00, 0x00, 0x01, 0x00, 0x01,
		0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0xca, 0x00, 0x04, 0xac, 0xd9,
		0xa8, 0xae,
	})

	checkDNSHeader(t, parsed.packet, dnsHeader{id: 0x5184, flags: responseFlags, counts: [4]uint16{1, 1, 0, 0}})
	if !bytes.Equal(parsed.questions[0].Name, encodedName("google", "com")) || parsed.questions[0].Type != DNSTypeA {
		t.Errorf("question = %+v, want an A question for google.com", parsed.questions[0])
	}
	checkARecord(t, parsed.answers[0], aRecord{name: []byte{0xc0, 0x0c}, ttl: 202, address: "172.217.168.174"})
}

// Ported from smoltcp's wire/dns.rs test_parse_response_multiple_a.
func TestDNSParseResponseMultipleA(t *testing.T) {
	t.Parallel()
	parsed := parseDNSMessage(t, []byte{
		0x4b, 0x9e, 0x81, 0x80, 0x00, 0x01, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00, 0x09, 0x72,
		0x75, 0x73, 0x74, 0x2d, 0x6c, 0x61, 0x6e, 0x67, 0x03, 0x6f, 0x72, 0x67, 0x00, 0x00,
		0x01, 0x00, 0x01, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x09, 0x00,
		0x04, 0x0d, 0xe0, 0x77, 0x35, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00,
		0x09, 0x00, 0x04, 0x0d, 0xe0, 0x77, 0x28, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00,
		0x00, 0x00, 0x09, 0x00, 0x04, 0x0d, 0xe0, 0x77, 0x43, 0xc0, 0x0c, 0x00, 0x01, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x09, 0x00, 0x04, 0x0d, 0xe0, 0x77, 0x62,
	})

	checkDNSHeader(t, parsed.packet, dnsHeader{id: 0x4b9e, flags: responseFlags, counts: [4]uint16{1, 4, 0, 0}})
	if !bytes.Equal(parsed.questions[0].Name, encodedName("rust-lang", "org")) || parsed.questions[0].Type != DNSTypeA {
		t.Errorf("question = %+v, want an A question for rust-lang.org", parsed.questions[0])
	}
	for i, address := range []string{"13.224.119.53", "13.224.119.40", "13.224.119.67", "13.224.119.98"} {
		checkARecord(t, parsed.answers[i], aRecord{name: []byte{0xc0, 0x0c}, ttl: 9, address: address})
	}
}

// Ported from smoltcp's wire/dns.rs test_parse_response_cname.
func TestDNSParseResponseCNAME(t *testing.T) {
	t.Parallel()
	parsed := parseDNSMessage(t, dnsCNAMEResponseBytes())

	checkDNSHeader(t, parsed.packet, dnsHeader{id: 0x786c, flags: responseFlags, counts: [4]uint16{1, 2, 0, 0}})
	if !bytes.Equal(parsed.questions[0].Name, encodedName("www", "facebook", "com")) || parsed.questions[0].Type != DNSTypeA {
		t.Errorf("question = %+v, want an A question for www.facebook.com", parsed.questions[0])
	}
	cname := parsed.answers[0]
	wantTarget := []byte{
		0x09, 0x73, 0x74, 0x61, 0x72, 0x2d, 0x6d, 0x69, 0x6e, 0x69, 0x04, 0x63, 0x31, 0x30,
		0x72, 0xc0, 0x10,
	}
	if !bytes.Equal(cname.Name, []byte{0xc0, 0x0c}) || cname.TTL != 1523 ||
		cname.Data.Type != DNSTypeCNAME || !bytes.Equal(cname.Data.Raw, wantTarget) || cname.Data.Address.IsValid() {
		t.Errorf("CNAME record = %+v, want TTL 1523 and target % x", cname, wantTarget)
	}
	checkARecord(t, parsed.answers[1], aRecord{name: []byte{0xc0, 0x2e}, ttl: 5, address: "31.13.83.36"})
}

// Ported from smoltcp's wire/dns.rs test_parse_response_nxdomain.
func TestDNSParseResponseNXDomain(t *testing.T) {
	t.Parallel()
	parsed := parseDNSMessage(t, []byte{
		0x63, 0xc4, 0x81, 0x83, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x13, 0x61,
		0x68, 0x61, 0x73, 0x64, 0x67, 0x68, 0x6c, 0x61, 0x6b, 0x73, 0x6a, 0x68, 0x62, 0x61,
		0x61, 0x73, 0x6c, 0x64, 0x03, 0x63, 0x6f, 0x6d, 0x00, 0x00, 0x01, 0x00, 0x01, 0xc0,
		0x20, 0x00, 0x06, 0x00, 0x01, 0x00, 0x00, 0x03, 0x83, 0x00, 0x3d, 0x01, 0x61, 0x0c,
		0x67, 0x74, 0x6c, 0x64, 0x2d, 0x73, 0x65, 0x72, 0x76, 0x65, 0x72, 0x73, 0x03, 0x6e,
		0x65, 0x74, 0x00, 0x05, 0x6e, 0x73, 0x74, 0x6c, 0x64, 0x0c, 0x76, 0x65, 0x72, 0x69,
		0x73, 0x69, 0x67, 0x6e, 0x2d, 0x67, 0x72, 0x73, 0xc0, 0x20, 0x5f, 0xce, 0x8b, 0x85,
		0x00, 0x00, 0x07, 0x08, 0x00, 0x00, 0x03, 0x84, 0x00, 0x09, 0x3a, 0x80, 0x00, 0x01,
		0x51, 0x80,
	})

	checkDNSHeader(t, parsed.packet, dnsHeader{
		id: 0x63c4, flags: responseFlags, rcode: DNSRcodeNXDomain, counts: [4]uint16{1, 0, 1, 0},
	})
	if parsed.questions[0].Type != DNSTypeA {
		t.Errorf("question type = %v, want A", parsed.questions[0].Type)
	}
	soa := parsed.authorities[0]
	if !bytes.Equal(soa.Name, []byte{0xc0, 0x20}) || soa.TTL != 899 || soa.Data.Type != DNSTypeSOA || len(soa.Data.Raw) != 0x3d {
		t.Errorf("SOA authority = %+v, want name c0 20 (com.), TTL 899 and 61 octets of data", soa)
	}
}

// Ported from smoltcp's wire/dns.rs test_emit.
func TestDNSReprEmit(t *testing.T) {
	t.Parallel()
	repr := DNSRepr{
		TransactionID: 0x1234,
		Flags:         DNSFlagRecursionDesired,
		Opcode:        DNSOpcodeQuery,
		Question:      DNSQuestion{Name: encodedName("rust-lang", "org"), Type: DNSTypeA},
	}
	buffer := make([]byte, repr.BufferLen())

	repr.Emit(DNSPacket(buffer))

	want := []byte{
		0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x09, 0x72,
		0x75, 0x73, 0x74, 0x2d, 0x6c, 0x61, 0x6e, 0x67, 0x03, 0x6f, 0x72, 0x67, 0x00, 0x00,
		0x01, 0x00, 0x01,
	}
	if !bytes.Equal(buffer, want) {
		t.Errorf("message = % x, want % x", buffer, want)
	}
}

func TestDNSReprEmitWritesTheSameQueryIntoAReusedBuffer(t *testing.T) {
	t.Parallel()
	repr := DNSRepr{
		TransactionID: 0x0003,
		Flags:         DNSFlagRecursionDesired,
		Question:      DNSQuestion{Name: encodedName("echo", "test"), Type: DNSTypeA},
	}
	zeroed, reused := make([]byte, repr.BufferLen()), filled(repr.BufferLen(), 0xff)

	repr.Emit(DNSPacket(zeroed))
	repr.Emit(DNSPacket(reused))

	if !bytes.Equal(reused, zeroed) {
		t.Errorf("into a reused buffer: % x, want % x", reused, zeroed)
	}
}

func TestNewDNSPacketNeedsTheHeader(t *testing.T) {
	t.Parallel()
	if _, err := NewDNSPacket(make([]byte, 11)); !errors.Is(err, ErrMalformed) {
		t.Errorf("NewDNSPacket(11 octets) = %v, want ErrMalformed", err)
	}
	packet, err := NewDNSPacket(make([]byte, 12))
	if err != nil || len(packet.Payload()) != 0 {
		t.Errorf("NewDNSPacket(12 octets) = % x, %v; want an empty payload", packet.Payload(), err)
	}
}

func TestDNSPacketHeaderSetters(t *testing.T) {
	t.Parallel()
	packet := DNSPacket(make([]byte, dnsHeaderLen))

	packet.SetTransactionID(0xbeef)
	packet.SetQuestionCount(1)
	packet.SetAnswerRecordCount(2)
	packet.SetAuthorityRecordCount(3)
	packet.SetAdditionalRecordCount(4)

	checkDNSHeader(t, packet, dnsHeader{id: 0xbeef, counts: [4]uint16{1, 2, 3, 4}})
}

func TestDNSPacketFlagsOpcodeAndRcodeShareTheirField(t *testing.T) {
	t.Parallel()
	packet := DNSPacket(make([]byte, dnsHeaderLen))
	packet[dnsFlagsOffset+1] = byte(DNSRcodeRefused) | 0x40 // and the reserved Z bit

	packet.SetFlags(DNSFlagResponse | DNSFlagTruncated | DNSFlags(0x000f))
	packet.SetOpcode(DNSOpcodeStatus)

	checkFields(t, []field{
		{"Flags", packet.Flags(), DNSFlagResponse | DNSFlagTruncated},
		{"Opcode", packet.Opcode(), DNSOpcodeStatus},
		{"Rcode", packet.Rcode(), DNSRcodeRefused},
		{"Z bit", packet[dnsFlagsOffset+1] & 0x40, uint8(0x40)},
	})
}

func TestDNSPacketReadsAnOpcodeWithoutAName(t *testing.T) {
	t.Parallel()
	const notify = 4 // RFC 1996
	packet := DNSPacket(make([]byte, dnsHeaderLen))

	packet.SetOpcode(notify)

	if packet.Opcode() != DNSOpcode(notify) || packet.Opcode().String() != "Unknown(4)" {
		t.Errorf("Opcode = %v, want Unknown(4)", packet.Opcode())
	}
}

func TestDNSPacketSetOpcodeReplacesTheLowThreeBitsAsSmoltcpDoes(t *testing.T) {
	t.Parallel()
	packet := DNSPacket(make([]byte, dnsHeaderLen))
	packet.SetOpcode(9)

	packet.SetOpcode(DNSOpcodeQuery)

	if packet.Opcode() != 8 {
		t.Errorf("Opcode = %v, want Unknown(8): smoltcp leaves the opcode's high bit", packet.Opcode())
	}
}

func TestDNSPacketNameLabelsFollowsAPointerFromANameOutsideThePacket(t *testing.T) {
	t.Parallel()
	outside := append([]byte{0x04, 'm', 'a', 'i', 'l'}, 0xc0, 0x0c)

	labels, err := nameLabels(DNSPacket(dnsCNAMEResponseBytes()), outside)

	if want := []string{"mail", "www", "facebook", "com"}; err != nil || !slices.Equal(labels, want) {
		t.Errorf("labels = %q, %v; want %q", labels, err, want)
	}
}

func TestDNSPacketNameLabelsStopsAtTheFirstError(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		name []byte
		want []string
	}{
		"an empty name":           {nil, nil},
		"no root label":           {[]byte{0x03, 'w', 'w', 'w'}, []string{"www"}},
		"a label one octet short": {[]byte{0x03, 'w', 'w', 'w', 0x02, 'x'}, []string{"www"}},
		// Taken for pointers, the labels of the reserved types 01 and 10
		// would point at www.facebook.com, at offset 12.
		"the reserved label type 01": {[]byte{0x01, 'a', 0x40, 0x0c}, []string{"a"}},
		"the reserved label type 10": {[]byte{0x80, 0x0c}, nil},
		"a truncated pointer":        {[]byte{0x01, 'a', 0xc0}, []string{"a"}},
		"a pointer beyond":           {[]byte{0xc0, 0xff}, nil},
	}
	packet := DNSPacket(dnsCNAMEResponseBytes())
	for name, c := range cases {
		labels, err := nameLabels(packet, c.name)
		if !errors.Is(err, ErrMalformed) || !slices.Equal(labels, c.want) {
			t.Errorf("%s: labels %q, %v; want %q, then ErrMalformed", name, labels, err, c.want)
		}
	}
}

// pointerChainMessage is a header, then names made of pointers: at offset 12
// a pointer to itself; at 14 a pointer to 16, where a pointer to 18 follows;
// at 18 the label "a", then a pointer back to 14.
func pointerChainMessage() []byte {
	return append(make([]byte, dnsHeaderLen), 0xc0, 12, 0xc0, 16, 0xc0, 18, 0x01, 'a', 0xc0, 14)
}

func TestDNSPacketNameLabelsRejectsPointersThatDoNotPointBackwards(t *testing.T) {
	t.Parallel()
	message := pointerChainMessage()
	cases := map[string]struct {
		offset int
		want   []string
	}{
		"a pointer to itself": {12, nil},
		"a pointer at or after the octets the last one led to": {14, nil},
		"a loop through the octets before the name":            {18, []string{"a"}},
	}
	for name, c := range cases {
		labels, err := nameLabels(DNSPacket(message), message[c.offset:])
		if !errors.Is(err, ErrMalformed) || !slices.Equal(labels, c.want) {
			t.Errorf("%s: labels %q, %v; want %q, then ErrMalformed", name, labels, err, c.want)
		}
	}
}

func TestDNSPacketNameLabelsFollowsAFirstPointerForward(t *testing.T) {
	t.Parallel()
	message := append(make([]byte, dnsHeaderLen), 0xc0, 14, 0x01, 'a', dnsNameEnd)

	labels, err := nameLabels(DNSPacket(message), message[12:])

	if err != nil || !slices.Equal(labels, []string{"a"}) {
		t.Errorf("labels = %q, %v; want [a]", labels, err)
	}
}

func TestDNSPacketNameLabelsLetsTheConsumerStopEarly(t *testing.T) {
	t.Parallel()
	message := dnsCNAMEResponseBytes()
	var first []byte

	for label := range DNSPacket(message).NameLabels(message[0x0c:]) {
		first = label
		break
	}

	if string(first) != "www" {
		t.Errorf("first label = %q, want www", first)
	}
}

func TestParseDNSQuestionRejects(t *testing.T) {
	t.Parallel()
	question := func(fields ...byte) []byte { return append(encodedName("echo", "test"), fields...) }
	cases := map[string][]byte{
		"an empty buffer":  nil,
		"a truncated name": {0x04, 'e', 'c'},
		// Read as a pointer or as an empty label, either reserved type would
		// make these octets a well-formed question.
		"the reserved label type 01": {0x40, 0x00, 0x00, 0x01, 0x00, 0x01},
		"the reserved label type 10": {0x80, 0x00, 0x00, 0x01, 0x00, 0x01},
		"no class":                   question(0x00, 0x01),
		"a truncated class":          question(0x00, 0x01, 0x00),
		"the chaos class":            question(0x00, 0x01, 0x00, 0x03),
	}
	for name, buffer := range cases {
		if q, _, err := ParseDNSQuestion(buffer); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: ParseDNSQuestion = %+v, %v; want ErrMalformed", name, q, err)
		}
	}
}

func TestDNSQuestionSurvivesEmitAndParseWithACompressedName(t *testing.T) {
	t.Parallel()
	question := DNSQuestion{Name: []byte{0x03, 'w', 'w', 'w', 0xc0, 0x10}, Type: DNSTypeAAAA}
	buffer := append(make([]byte, question.BufferLen()), 0x99)

	question.Emit(buffer)
	parsed, rest, err := ParseDNSQuestion(buffer)

	if err != nil || !bytes.Equal(parsed.Name, question.Name) || parsed.Type != question.Type || !bytes.Equal(rest, []byte{0x99}) {
		t.Errorf("ParseDNSQuestion = %+v, % x, %v; want %+v and 99 left", parsed, rest, err, question)
	}
}

// dnsRecordBytes encodes a record of class class for the name at offset 12,
// with data.
func dnsRecordBytes(recordType DNSType, class uint16, data []byte) []byte {
	record := []byte{0xc0, 0x0c, byte(recordType >> 8), byte(recordType), byte(class >> 8), byte(class)}
	record = append(record, 0x00, 0x00, 0x0e, 0x10, byte(len(data)>>8), byte(len(data)))
	return append(record, data...)
}

func TestParseDNSRecordRejects(t *testing.T) {
	t.Parallel()
	reservedLabelType := dnsRecordBytes(DNSTypeA, dnsClassIN, []byte{192, 0, 2, 1})
	reservedLabelType[0] = 0x80 // the name's pointer, made the reserved type 10
	cases := map[string][]byte{
		"a truncated name":              {0xc0},
		"the reserved label type 10":    reservedLabelType,
		"truncated fields":              dnsRecordBytes(DNSTypeA, dnsClassIN, nil)[:11],
		"the chaos class":               dnsRecordBytes(DNSTypeA, 3, []byte{192, 0, 2, 1}),
		"data beyond the buffer":        dnsRecordBytes(DNSTypeA, dnsClassIN, []byte{192, 0, 2, 1})[:15],
		"an A record of three octets":   dnsRecordBytes(DNSTypeA, dnsClassIN, []byte{192, 0, 2}),
		"an A record of five octets":    dnsRecordBytes(DNSTypeA, dnsClassIN, []byte{192, 0, 2, 1, 0}),
		"an A record with an IPv6 size": dnsRecordBytes(DNSTypeA, dnsClassIN, make([]byte, 16)),
	}
	for name, buffer := range cases {
		if record, _, err := ParseDNSRecord(buffer); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: ParseDNSRecord = %+v, %v; want ErrMalformed", name, record, err)
		}
	}
}

func TestParseDNSRecordKeepsTheDataOfOtherTypesRaw(t *testing.T) {
	t.Parallel()
	ipv6 := netip.MustParseAddr("2001:db8::1").As16()
	buffer := append(dnsRecordBytes(DNSTypeAAAA, dnsClassIN, ipv6[:]), 0xaa)

	record, rest, err := ParseDNSRecord(buffer)

	if err != nil || record.Data.Type != DNSTypeAAAA || !bytes.Equal(record.Data.Raw, ipv6[:]) ||
		record.Data.Address.IsValid() || record.TTL != 3600 || !bytes.Equal(rest, []byte{0xaa}) {
		t.Errorf("ParseDNSRecord = %+v, % x, %v; want a raw AAAA record and aa left", record, rest, err)
	}
}

func TestDNSEnumStrings(t *testing.T) {
	t.Parallel()
	checkFields(t, []field{
		{"Query", DNSOpcodeQuery.String(), "Query"},
		{"Status", DNSOpcodeStatus.String(), "Status"},
		{"opcode 4", DNSOpcode(4).String(), "Unknown(4)"},
		{"NoError", DNSRcodeNoError.String(), "NoError"},
		{"FormErr", DNSRcodeFormErr.String(), "FormErr"},
		{"ServFail", DNSRcodeServFail.String(), "ServFail"},
		{"NXDomain", DNSRcodeNXDomain.String(), "NXDomain"},
		{"NotImp", DNSRcodeNotImp.String(), "NotImp"},
		{"Refused", DNSRcodeRefused.String(), "Refused"},
		{"YXDomain", DNSRcodeYXDomain.String(), "YXDomain"},
		{"YXRRSet", DNSRcodeYXRRSet.String(), "YXRRSet"},
		{"NXRRSet", DNSRcodeNXRRSet.String(), "NXRRSet"},
		{"NotAuth", DNSRcodeNotAuth.String(), "NotAuth"},
		{"NotZone", DNSRcodeNotZone.String(), "NotZone"},
		{"rcode 12", DNSRcode(12).String(), "Unknown(12)"},
		{"A", DNSTypeA.String(), "A"},
		{"NS", DNSTypeNS.String(), "Ns"},
		{"CNAME", DNSTypeCNAME.String(), "Cname"},
		{"SOA", DNSTypeSOA.String(), "Soa"},
		{"AAAA", DNSTypeAAAA.String(), "Aaaa"},
		{"type 16", DNSType(16).String(), "Unknown(16)"},
	})
}
