package sim

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"

	"github.com/anhydrous99/satlink/internal/asciicase"
	"github.com/anhydrous99/satlink/internal/debugtext"
	"github.com/anhydrous99/satlink/internal/utf8lossy"
)

// The simulated internet's DNS server (RFC 1035): A records for the
// configured hosts and NXDOMAIN for every other name.

const (
	dnsHeaderLen = 12
	// questionNameOffset is where the question's name starts. Answers
	// point back to it with a compression pointer rather than repeating it.
	questionNameOffset = dnsHeaderLen
	// namePointer is the top two bits that mark a compression pointer.
	namePointer = 0xC000
	// maxLabelLen is the longest label, 63 bytes; larger length octets are
	// pointers or reserved forms, which a question has no use for.
	maxLabelLen = 63
	maxNameLen  = 255
	// questionFixedLen is the type and class after the question's name.
	questionFixedLen = 4
	// maxUDPMessageLen is the largest message plain DNS carries over UDP
	// (RFC 1035 §4.2.1).
	maxUDPMessageLen = 512
	// answerLen is a name pointer, type, class, TTL, data length and an
	// IPv4 address.
	answerLen      = 16
	ipv4AddressLen = 4
	dnsTypeA       = 1
	dnsClassIN     = 1
	// ttlSeconds is long enough to save the airtime of repeated lookups,
	// short enough for a test that changes the hosts between calls.
	ttlSeconds = 60
)

// Bits of the header's flags field.
const (
	flagResponse = 0x8000
	// flagOpcode is the kind of query, which the response repeats (RFC
	// 1035 §4.1.1). Only opcode 0, a standard query, is implemented.
	flagOpcode             = 0x7800
	flagAuthoritative      = 0x0400
	flagTruncated          = 0x0200
	flagRecursionDesired   = 0x0100
	flagRecursionAvailable = 0x0080
)

// Response codes, in the low four bits of the flags.
const (
	rcodeNoError     = 0
	rcodeFormatError = 1
	// rcodeNameError is NXDOMAIN: the name does not exist.
	rcodeNameError      = 3
	rcodeNotImplemented = 4
	rcodeRefused        = 5
)

// zone is the names the server answers for.
type zone struct {
	records []zoneRecord
}

// zoneRecord is an A record, its name in lower case without a trailing
// dot.
type zoneRecord struct {
	name    string
	address netip.Addr
}

// newZone returns a zone of A records for hosts, whose addresses must be
// IPv4 (see [checkHosts]).
func newZone(hosts []HostAddress) zone {
	records := make([]zoneRecord, 0, len(hosts))
	for _, host := range hosts {
		records = append(records, zoneRecord{name: normalizeName(host.Name), address: host.Address})
	}
	return zone{records: records}
}

// checkHosts refuses a host whose address is not IPv4, as a DNS A record
// holds four octets.
func checkHosts(hosts []HostAddress) error {
	for _, host := range hosts {
		if !host.Address.Is4() {
			return errors.New(debugtext.Quote(host.Name) + " in hosts needs an IPv4 address")
		}
	}
	return nil
}

func (z zone) addressesOf(name string) []netip.Addr {
	name = normalizeName(name)
	var addresses []netip.Addr
	for _, record := range z.records {
		if record.name == name {
			addresses = append(addresses, record.address)
		}
	}
	return addresses
}

// normalizeName returns name as names compare: in lower case, with or
// without the root's trailing dot.
func normalizeName(name string) string {
	return asciicase.Lower(strings.TrimRight(name, "."))
}

// answerDNS returns the response to query, and false for a message that
// must not be answered: one too short for a header, or a response itself
// (answering those could bounce messages between two servers forever).
func answerDNS(query []byte, names zone) ([]byte, bool) {
	header, ok := parseDNSHeader(query)
	if !ok || header.flags&flagResponse != 0 {
		return nil, false
	}
	if header.flags&flagOpcode != 0 {
		return dnsResponse(header, rcodeNotImplemented, nil, nil), true
	}
	question, ok := parseQuestion(query)
	if !ok || header.questionCount != 1 {
		return dnsResponse(header, rcodeFormatError, nil, nil), true
	}
	if question.class != dnsClassIN {
		return dnsResponse(header, rcodeRefused, &question, nil), true
	}
	addresses := names.addressesOf(question.name)
	switch {
	case len(addresses) == 0:
		return dnsResponse(header, rcodeNameError, &question, nil), true
	case question.kind == dnsTypeA:
		return dnsResponse(header, rcodeNoError, &question, addresses), true
	default:
		// The name exists, but has no records of the type asked for.
		return dnsResponse(header, rcodeNoError, &question, nil), true
	}
}

// dnsHeader is the header fields the server needs.
type dnsHeader struct {
	id            uint16
	flags         uint16
	questionCount uint16
}

func parseDNSHeader(message []byte) (dnsHeader, bool) {
	if len(message) < dnsHeaderLen {
		return dnsHeader{}, false
	}
	return dnsHeader{
		id:            binary.BigEndian.Uint16(message[0:]),
		flags:         binary.BigEndian.Uint16(message[2:]),
		questionCount: binary.BigEndian.Uint16(message[4:]),
	}, true
}

// question is the question right after the header.
type question struct {
	// wire is the name, type and class exactly as the query carried them.
	wire  []byte
	name  string
	kind  uint16
	class uint16
}

func parseQuestion(message []byte) (question, bool) {
	if len(message) < dnsHeaderLen {
		return question{}, false
	}
	section := message[dnsHeaderLen:]
	name, nameLen, ok := parseName(section)
	if !ok || len(section) < nameLen+questionFixedLen {
		return question{}, false
	}
	wire := section[:nameLen+questionFixedLen]
	return question{
		wire:  wire,
		name:  name,
		kind:  binary.BigEndian.Uint16(wire[nameLen:]),
		class: binary.BigEndian.Uint16(wire[nameLen+2:]),
	}, true
}

// parseName returns an uncompressed name in dotted form, and its length on
// the wire.
func parseName(section []byte) (string, int, bool) {
	var labels []string
	offset := 0
	for offset < len(section) {
		length := int(section[offset])
		offset++
		if length == 0 {
			return strings.Join(labels, "."), offset, true
		}
		if length > maxLabelLen || offset+length > len(section) {
			return "", 0, false
		}
		labels = append(labels, utf8lossy.String(section[offset:offset+length]))
		offset += length
		if offset > maxNameLen {
			return "", 0, false
		}
	}
	return "", 0, false
}

// dnsResponse returns a response with code that repeats asked, if any,
// and carries an A record for each address that fits into a UDP message.
func dnsResponse(query dnsHeader, code uint16, asked *question, addresses []netip.Addr) []byte {
	var questionWire []byte
	if asked != nil {
		questionWire = asked.wire
	}
	room := max(maxUDPMessageLen-(dnsHeaderLen+len(questionWire)), 0) / answerLen
	answers := addresses[:min(len(addresses), room)]
	flags := flagResponse | query.flags&flagOpcode | flagAuthoritative | flagRecursionAvailable |
		query.flags&flagRecursionDesired | code
	if len(answers) < len(addresses) {
		flags |= flagTruncated
	}

	message := make([]byte, 0, dnsHeaderLen+len(questionWire)+len(answers)*answerLen)
	message = binary.BigEndian.AppendUint16(message, query.id)
	message = binary.BigEndian.AppendUint16(message, flags)
	message = binary.BigEndian.AppendUint16(message, questionCountOf(asked))
	// At most 32 answers fit into 512 bytes.
	message = binary.BigEndian.AppendUint16(message, uint16(len(answers)))
	// No authority or additional records.
	message = append(message, 0, 0, 0, 0)
	message = append(message, questionWire...)
	for _, address := range answers {
		message = appendARecord(message, address)
	}
	return message
}

func questionCountOf(asked *question) uint16 {
	if asked == nil {
		return 0
	}
	return 1
}

func appendARecord(message []byte, address netip.Addr) []byte {
	message = binary.BigEndian.AppendUint16(message, namePointer|questionNameOffset)
	message = binary.BigEndian.AppendUint16(message, dnsTypeA)
	message = binary.BigEndian.AppendUint16(message, dnsClassIN)
	message = binary.BigEndian.AppendUint32(message, ttlSeconds)
	message = binary.BigEndian.AppendUint16(message, ipv4AddressLen)
	octets := address.As4()
	return append(message, octets[:]...)
}
