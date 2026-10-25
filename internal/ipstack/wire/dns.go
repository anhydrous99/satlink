package wire

import (
	"encoding/binary"
	"net/netip"
	"strconv"
)

// DNSOpcode is the kind of query a DNS message makes. Values without a
// constant here are valid on the wire.
type DNSOpcode uint8

// The DNS opcodes (RFC 1035 § 4.1.1).
const (
	// DNSOpcodeQuery is a standard query, the only kind the stack sends
	// and accepts answers to.
	DNSOpcodeQuery DNSOpcode = 0
	// DNSOpcodeStatus is a server status request.
	DNSOpcodeStatus DNSOpcode = 1
)

// String returns the variant name ("Query", "Unknown(4)").
func (o DNSOpcode) String() string {
	switch o {
	case DNSOpcodeQuery:
		return "Query"
	case DNSOpcodeStatus:
		return "Status"
	default:
		return unknownVariant(int(o))
	}
}

// DNSRcode is the response code of a DNS message. Values without a constant
// here are valid on the wire.
type DNSRcode uint8

// The response codes (RFC 1035 § 4.1.1, RFC 2136 § 2.2).
const (
	// DNSRcodeNoError reports success.
	DNSRcodeNoError DNSRcode = 0x00
	// DNSRcodeFormErr reports a query the server could not interpret.
	DNSRcodeFormErr DNSRcode = 0x01
	// DNSRcodeServFail reports a server that failed to process the query.
	DNSRcodeServFail DNSRcode = 0x02
	// DNSRcodeNXDomain reports a name that does not exist; it fails the
	// query.
	DNSRcodeNXDomain DNSRcode = 0x03
	// DNSRcodeNotImp reports a kind of query the server does not support.
	DNSRcodeNotImp DNSRcode = 0x04
	// DNSRcodeRefused reports a server that refuses to answer.
	DNSRcodeRefused DNSRcode = 0x05
	// DNSRcodeYXDomain reports a name that exists when it should not
	// (dynamic update).
	DNSRcodeYXDomain DNSRcode = 0x06
	// DNSRcodeYXRRSet reports a record set that exists when it should not
	// (dynamic update).
	DNSRcodeYXRRSet DNSRcode = 0x07
	// DNSRcodeNXRRSet reports a record set that does not exist when it
	// should (dynamic update).
	DNSRcodeNXRRSet DNSRcode = 0x08
	// DNSRcodeNotAuth reports a server that is not authoritative for the
	// zone (dynamic update).
	DNSRcodeNotAuth DNSRcode = 0x09
	// DNSRcodeNotZone reports a name outside the zone (dynamic update).
	DNSRcodeNotZone DNSRcode = 0x0a
)

var dnsRcodeNames = map[DNSRcode]string{
	DNSRcodeNoError:  "NoError",
	DNSRcodeFormErr:  "FormErr",
	DNSRcodeServFail: "ServFail",
	DNSRcodeNXDomain: "NXDomain",
	DNSRcodeNotImp:   "NotImp",
	DNSRcodeRefused:  "Refused",
	DNSRcodeYXDomain: "YXDomain",
	DNSRcodeYXRRSet:  "YXRRSet",
	DNSRcodeNXRRSet:  "NXRRSet",
	DNSRcodeNotAuth:  "NotAuth",
	DNSRcodeNotZone:  "NotZone",
}

// String returns the variant name ("NXDomain", "Unknown(12)").
func (r DNSRcode) String() string {
	if name, ok := dnsRcodeNames[r]; ok {
		return name
	}
	return unknownVariant(int(r))
}

// DNSType is the type of a DNS question or record. Values without a constant here are valid on the wire.
type DNSType uint16

// The record types (RFC 1035 § 3.2.2, RFC 3596).
const (
	// DNSTypeA is an IPv4 address, the only type the stack asks for.
	DNSTypeA DNSType = 0x0001
	// DNSTypeNS is an authoritative name server.
	DNSTypeNS DNSType = 0x0002
	// DNSTypeCNAME is the canonical name of an alias, which the resolver
	// follows.
	DNSTypeCNAME DNSType = 0x0005
	// DNSTypeSOA marks the start of a zone of authority.
	DNSTypeSOA DNSType = 0x0006
	// DNSTypeAAAA is an IPv6 address.
	DNSTypeAAAA DNSType = 0x001c
)

var dnsTypeNames = map[DNSType]string{
	DNSTypeA:     "A",
	DNSTypeNS:    "Ns",
	DNSTypeCNAME: "Cname",
	DNSTypeSOA:   "Soa",
	DNSTypeAAAA:  "Aaaa",
}

// String returns the variant name ("A", "Cname", "Unknown(16)").
func (t DNSType) String() string {
	if name, ok := dnsTypeNames[t]; ok {
		return name
	}
	return unknownVariant(int(t))
}

// DNSFlags are the single-bit flags of a DNS header.
type DNSFlags uint16

// The flags of a DNS header (RFC 1035 § 4.1.1, RFC 4035 § 3.2).
const (
	// DNSFlagResponse (QR) marks a response; the resolver ignores messages
	// without it.
	DNSFlagResponse DNSFlags = 0x8000
	// DNSFlagAuthoritative (AA) marks an answer from a server that is an
	// authority for the name.
	DNSFlagAuthoritative DNSFlags = 0x0400
	// DNSFlagTruncated (TC) marks a message cut short to fit the transport.
	DNSFlagTruncated DNSFlags = 0x0200
	// DNSFlagRecursionDesired (RD) asks the server to resolve the name
	// fully; every query the stack sends has it.
	DNSFlagRecursionDesired DNSFlags = 0x0100
	// DNSFlagRecursionAvailable (RA) says the server resolves recursively.
	DNSFlagRecursionAvailable DNSFlags = 0x0080
	// DNSFlagAuthenticData (AD) says the server validated the answer with
	// DNSSEC.
	DNSFlagAuthenticData DNSFlags = 0x0020
	// DNSFlagCheckDisabled (CD) asks the server not to validate with
	// DNSSEC.
	DNSFlagCheckDisabled DNSFlags = 0x0010
)

// allDNSFlags are the bits of the flags field that are flags. Flags and
// SetFlags leave the others, the opcode, the response code and the reserved
// Z bit, alone.
const allDNSFlags = DNSFlagResponse | DNSFlagAuthoritative | DNSFlagTruncated |
	DNSFlagRecursionDesired | DNSFlagRecursionAvailable | DNSFlagAuthenticData |
	DNSFlagCheckDisabled

// dnsHeaderLen is the length of the header of every DNS message (RFC 1035
// § 4.1.1).
const dnsHeaderLen = 12

// Offsets of the DNS header fields (RFC 1035 § 4.1.1).
const (
	dnsIDOffset              = 0
	dnsFlagsOffset           = 2 // the flags, the opcode and the response code
	dnsQuestionCountOffset   = 4
	dnsAnswerCountOffset     = 6
	dnsAuthorityCountOffset  = 8
	dnsAdditionalCountOffset = 10
)

// The opcode and the response code inside the flags field.
const (
	dnsOpcodeShift        = 11
	dnsOpcodeMask  uint16 = 0xf // after the shift
	// dnsOpcodeSetMask is the part of the field SetOpcode replaces: the
	// opcode's low three bits, which hold every opcode the stack sends.
	dnsOpcodeSetMask uint16 = 0x3800
	dnsRcodeMask     uint16 = 0xf
)

// dnsClassIN is the class of every question and record the stack handles:
// the Internet (RFC 1035 § 3.2.4).
const dnsClassIN = 1

// The fields that follow the name of a question (RFC 1035 § 4.1.2).
const (
	dnsQuestionTypeOffset  = 0
	dnsQuestionClassOffset = 2
	dnsQuestionFieldsLen   = 4
)

// The fields that follow the name of a resource record (RFC 1035 § 4.1.3).
const (
	dnsRecordTypeOffset    = 0
	dnsRecordClassOffset   = 2
	dnsRecordTTLOffset     = 4
	dnsRecordDataLenOffset = 8
	dnsRecordFieldsLen     = 10
)

// dnsQueryQuestionCount is how many questions a query the stack sends asks.
const dnsQueryQuestionCount = 1

// DNSPacket is a read/write view of a DNS message. Converting a buffer,
// DNSPacket(buffer), does not check it; NewDNSPacket does.
type DNSPacket []byte

// NewDNSPacket returns buffer as a DNSPacket after checking, as CheckLen
// does, that no header accessor will panic.
func NewDNSPacket(buffer []byte) (DNSPacket, error) {
	packet := DNSPacket(buffer)
	if err := packet.CheckLen(); err != nil {
		return nil, err
	}
	return packet, nil
}

// CheckLen returns ErrMalformed unless the buffer holds the 12-octet header.
func (p DNSPacket) CheckLen() error {
	if len(p) < dnsHeaderLen {
		return ErrMalformed
	}
	return nil
}

// Payload returns the octets after the header: the question and record
// sections.
func (p DNSPacket) Payload() []byte {
	return p[dnsHeaderLen:]
}

// TransactionID returns the identifier that matches a response to its query.
func (p DNSPacket) TransactionID() uint16 {
	return binary.BigEndian.Uint16(p[dnsIDOffset:])
}

// Flags returns the header's flags, without the opcode and response code
// that share their field.
func (p DNSPacket) Flags() DNSFlags {
	return DNSFlags(p.flagsField()) & allDNSFlags
}

// Opcode returns the kind of query.
func (p DNSPacket) Opcode() DNSOpcode {
	return DNSOpcode((p.flagsField() >> dnsOpcodeShift) & dnsOpcodeMask)
}

// Rcode returns the response code.
func (p DNSPacket) Rcode() DNSRcode {
	return DNSRcode(p.flagsField() & dnsRcodeMask)
}

// QuestionCount returns the number of entries in the question section.
func (p DNSPacket) QuestionCount() uint16 {
	return binary.BigEndian.Uint16(p[dnsQuestionCountOffset:])
}

// AnswerRecordCount returns the number of records in the answer section.
func (p DNSPacket) AnswerRecordCount() uint16 {
	return binary.BigEndian.Uint16(p[dnsAnswerCountOffset:])
}

// SetTransactionID sets the identifier that matches a response to its query.
func (p DNSPacket) SetTransactionID(id uint16) {
	binary.BigEndian.PutUint16(p[dnsIDOffset:], id)
}

// SetFlags sets the header's flags, leaving the opcode and response code.
func (p DNSPacket) SetFlags(flags DNSFlags) {
	p.setFlagsField(p.flagsField()&^uint16(allDNSFlags) | uint16(flags&allDNSFlags))
}

// SetOpcode sets the kind of query.
func (p DNSPacket) SetOpcode(opcode DNSOpcode) {
	p.setFlagsField(p.flagsField()&^dnsOpcodeSetMask | uint16(opcode)<<dnsOpcodeShift)
}

// SetQuestionCount sets the number of entries in the question section.
func (p DNSPacket) SetQuestionCount(count uint16) {
	binary.BigEndian.PutUint16(p[dnsQuestionCountOffset:], count)
}

// SetAnswerRecordCount sets the number of records in the answer section.
func (p DNSPacket) SetAnswerRecordCount(count uint16) {
	binary.BigEndian.PutUint16(p[dnsAnswerCountOffset:], count)
}

// SetAuthorityRecordCount sets the number of records in the authority
// section.
func (p DNSPacket) SetAuthorityRecordCount(count uint16) {
	binary.BigEndian.PutUint16(p[dnsAuthorityCountOffset:], count)
}

// SetAdditionalRecordCount sets the number of records in the additional
// section.
func (p DNSPacket) SetAdditionalRecordCount(count uint16) {
	binary.BigEndian.PutUint16(p[dnsAdditionalCountOffset:], count)
}

func (p DNSPacket) flagsField() uint16 {
	return binary.BigEndian.Uint16(p[dnsFlagsOffset:])
}

func (p DNSPacket) setFlagsField(field uint16) {
	binary.BigEndian.PutUint16(p[dnsFlagsOffset:], field)
}

// DNSQuestion is an entry of a message's question section, of class IN.
type DNSQuestion struct {
	// Name is the name as encoded on the wire, possibly ending in a
	// compression pointer; DNSPacket.NameLabels decodes it.
	Name []byte
	// Type is the type of record asked for.
	Type DNSType
}

// ParseDNSQuestion parses the question at the start of buffer and returns it
// with the octets that follow it. It fails with ErrMalformed for a truncated
// question or a class other than IN. The question's name shares the buffer's
// storage.
func ParseDNSQuestion(buffer []byte) (question DNSQuestion, rest []byte, err error) {
	name, rest, err := splitName(buffer)
	if err != nil {
		return DNSQuestion{}, nil, err
	}
	if len(rest) < dnsQuestionFieldsLen {
		return DNSQuestion{}, nil, ErrMalformed
	}
	questionType := DNSType(binary.BigEndian.Uint16(rest[dnsQuestionTypeOffset:]))
	class := binary.BigEndian.Uint16(rest[dnsQuestionClassOffset:])
	if class != dnsClassIN {
		return DNSQuestion{}, nil, ErrMalformed
	}
	return DNSQuestion{Name: name, Type: questionType}, rest[dnsQuestionFieldsLen:], nil
}

// BufferLen returns the length of the question Emit writes.
func (q DNSQuestion) BufferLen() int {
	return len(q.Name) + dnsQuestionFieldsLen
}

// Emit writes the question, with class IN, at the start of buffer, which
// must be at least BufferLen octets long.
func (q DNSQuestion) Emit(buffer []byte) {
	copy(buffer[:len(q.Name)], q.Name)
	fields := buffer[len(q.Name):]
	binary.BigEndian.PutUint16(fields[dnsQuestionTypeOffset:], uint16(q.Type))
	binary.BigEndian.PutUint16(fields[dnsQuestionClassOffset:], dnsClassIN)
}

// DNSRecord is a resource record of class IN.
type DNSRecord struct {
	// Name is the owner name as encoded on the wire, possibly ending in a
	// compression pointer; DNSPacket.NameLabels decodes it.
	Name []byte
	// TTL is how long the record may be cached, in seconds.
	TTL uint32
	// Data is the record's type and data.
	Data DNSRecordData
}

// ParseDNSRecord parses the record at the start of buffer and returns it with
// the octets that follow it. It fails with ErrMalformed for a truncated
// record, a class other than IN, or an A record whose data is not four
// octets. The record's name and raw data share the buffer's storage.
func ParseDNSRecord(buffer []byte) (record DNSRecord, rest []byte, err error) {
	name, rest, err := splitName(buffer)
	if err != nil {
		return DNSRecord{}, nil, err
	}
	if len(rest) < dnsRecordFieldsLen {
		return DNSRecord{}, nil, ErrMalformed
	}
	recordType := DNSType(binary.BigEndian.Uint16(rest[dnsRecordTypeOffset:]))
	class := binary.BigEndian.Uint16(rest[dnsRecordClassOffset:])
	ttl := binary.BigEndian.Uint32(rest[dnsRecordTTLOffset:])
	dataLen := int(binary.BigEndian.Uint16(rest[dnsRecordDataLenOffset:]))
	rest = rest[dnsRecordFieldsLen:]
	if class != dnsClassIN || len(rest) < dataLen {
		return DNSRecord{}, nil, ErrMalformed
	}
	data, err := parseDNSRecordData(recordType, rest[:dataLen:dataLen])
	if err != nil {
		return DNSRecord{}, nil, err
	}
	return DNSRecord{Name: name, TTL: ttl, Data: data}, rest[dataLen:], nil
}

// DNSRecordData is the data of a resource record. Type says which kind of
// record it is: an A record has its Address; every other type, CNAME
// included, has its data in Raw. With IPv4 only, AAAA records are "other"
// records.
type DNSRecordData struct {
	// Type is the record's type.
	Type DNSType
	// Address is an A record's IPv4 address; the zero Addr for other types.
	Address netip.Addr
	// Raw is the record's data as on the wire, for every type but A. For a
	// CNAME it is the target name, possibly ending in a compression pointer;
	// DNSPacket.NameLabels decodes it.
	Raw []byte
}

// parseDNSRecordData interprets the data of a record of type recordType.
func parseDNSRecordData(recordType DNSType, data []byte) (DNSRecordData, error) {
	if recordType != DNSTypeA {
		return DNSRecordData{Type: recordType, Raw: data}, nil
	}
	if len(data) != ipv4AddrLen {
		return DNSRecordData{}, ErrMalformed
	}
	return DNSRecordData{Type: DNSTypeA, Address: netip.AddrFrom4([ipv4AddrLen]byte(data))}, nil
}

// DNSRepr is a high-level representation of a DNS query: a header and one
// question, which is all the stack sends.
type DNSRepr struct {
	// TransactionID matches the response to the query.
	TransactionID uint16
	// Opcode is the kind of query, DNSOpcodeQuery for every query the stack
	// sends.
	Opcode DNSOpcode
	// Flags are the header's flags, DNSFlagRecursionDesired for every query
	// the stack sends.
	Flags DNSFlags
	// Question is the message's one question.
	Question DNSQuestion
}

// BufferLen returns the length of the message Emit writes.
func (r DNSRepr) BufferLen() int {
	return dnsHeaderLen + r.Question.BufferLen()
}

// Emit writes the query into packet, which must be at least BufferLen octets
// long: the header with one question and no records, then the question.
func (r DNSRepr) Emit(packet DNSPacket) {
	packet.SetTransactionID(r.TransactionID)
	// Setting the flags and the opcode keeps what the field held; clearing it
	// first gives the response code and reserved bits the zero they have in a
	// fresh buffer, whatever the buffer held.
	packet.setFlagsField(0)
	packet.SetFlags(r.Flags)
	packet.SetOpcode(r.Opcode)
	packet.SetQuestionCount(dnsQueryQuestionCount)
	packet.SetAnswerRecordCount(0)
	packet.SetAuthorityRecordCount(0)
	packet.SetAdditionalRecordCount(0)
	r.Question.Emit(packet.Payload())
}

// unknownVariant formats a value without a name as Unknown(value).
func unknownVariant(value int) string {
	return "Unknown(" + strconv.Itoa(value) + ")"
}
