package satnet

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/anhydrous99/satlink/internal/asciicase"
	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
	"github.com/anhydrous99/satlink/internal/utf8lossy"
)

// The stack's name lookups. The resolver fails a lookup alike whether a DNS
// server said the name has no address or none answered in time, so the
// stack watches what arrives: only a server's answer about the name makes
// the lookup's failure a missing name rather than silence.

// dnsPort is where DNS servers listen (RFC 1035 §4.2): where the stack's
// queries go, which the device sends at once and the repeat filter knows,
// and where answers come from.
const dnsPort = 53

// dnsServerTimeout is how long the resolver gives each DNS server to answer
// a query, from the first time it tries to send it there.
const dnsServerTimeout = 10 * time.Second

// longestLookup is the longest the resolver tries a lookup: each server it
// keeps (dns.MaxServerCount), in turn.
const longestLookup = dnsServerTimeout * dns.MaxServerCount

// lookupKind says where a lookup stands.
type lookupKind int

const (
	// lookupPending is a lookup without an outcome yet.
	lookupPending lookupKind = iota
	// lookupResolved is a lookup answered with addresses.
	lookupResolved
	// lookupNoAddress is a lookup a DNS server answered: the name has no
	// IPv4 address.
	lookupNoAddress
	// lookupUnanswered is a lookup no answer came for in time, or whose
	// server went away.
	lookupUnanswered
)

// lookup is where a lookup stands, with its addresses once resolved.
type lookup struct {
	kind      lookupKind
	addresses []netip.Addr
}

// lookupID identifies a lookup started with stack.startLookup.
type lookupID uint64

// nameLookup is a lookup under way.
type nameLookup struct {
	id    lookupID
	query dns.QueryHandle
	// name is the name as DNS answers spell it, in lower case.
	name string
	// answered reports that a DNS server answered for the name.
	answered bool
}

// startLookup starts looking up the IPv4 addresses of name, which must be
// normalised; the query leaves on the next poll.
func (s *stack) startLookup(name string) (lookupID, error) {
	if len(s.dnsServers) == 0 {
		return 0, &Error{Kind: ErrNoDNSServer}
	}
	query, err := s.dnsSocket().StartQuery(s.iface.Context(), name, wire.DNSTypeA)
	if err != nil {
		return 0, &Error{Kind: ErrInvalidHost, Host: name}
	}
	id := s.nextLookup
	s.nextLookup++
	s.lookups = append(s.lookups, nameLookup{id: id, query: query, name: asciicase.Lower(name)})
	return id, nil
}

// lookupResult returns where the lookup stands. Once it is no longer
// pending, id is spent.
func (s *stack) lookupResult(id lookupID) lookup {
	index := s.lookupIndex(id)
	if index < 0 {
		return lookup{kind: lookupUnanswered}
	}
	under := s.lookups[index]
	addresses, err := s.dnsSocket().TakeQueryResult(under.query)
	if errors.Is(err, dns.ErrPending) {
		return lookup{kind: lookupPending}
	}
	s.lookups = slices.Delete(s.lookups, index, index+1)
	switch {
	case err == nil:
		return lookup{kind: lookupResolved, addresses: addresses}
	case under.answered:
		return lookup{kind: lookupNoAddress}
	default:
		return lookup{kind: lookupUnanswered}
	}
}

// abandonLookup gives up on a lookup whose outcome was never collected.
func (s *stack) abandonLookup(id lookupID) {
	index := s.lookupIndex(id)
	if index < 0 {
		return
	}
	query := s.lookups[index].query
	s.lookups = slices.Delete(s.lookups, index, index+1)
	if _, err := s.dnsSocket().TakeQueryResult(query); errors.Is(err, dns.ErrPending) {
		s.dnsSocket().CancelQuery(query)
	}
}

// lookupIndex returns the index of the lookup id in s.lookups, and -1 for a
// lookup that is over.
func (s *stack) lookupIndex(id lookupID) int {
	return slices.IndexFunc(s.lookups, func(under nameLookup) bool { return under.id == id })
}

// noteAnswer records that a DNS server answered for a name, saying what its
// addresses are: a lookup of it that fails now found none. Every lookup of
// the name counts as answered, whichever of their queries the answer was
// for. An answer to one of the app's UDP sockets is
// theirs, and says nothing about our lookups.
func (s *stack) noteAnswer(answer dnsAnswer) {
	if !slices.Contains(s.dnsServers, answer.server) || slices.Contains(s.appUDPPorts(), answer.port) {
		return
	}
	for i := range s.lookups {
		if s.lookups[i].name == answer.name {
			s.lookups[i].answered = true
		}
	}
}

// dnsAnswer is a DNS server's answer to an A question that says what the
// name's addresses are, or that it has none. Other answers, such as a server
// failure, say nothing about the name.
type dnsAnswer struct {
	server netip.Addr
	// port is where it went: the port the question came from.
	port uint16
	// name is the name asked about, in lower case.
	name string
}

// parseDNSAnswer returns the DNS answer datagram carries, and false for any
// other datagram.
func parseDNSAnswer(datagram []byte) (dnsAnswer, bool) {
	server, port, message, ok := dnsResponse(datagram)
	if !ok || !tellsOfTheName(message) {
		return dnsAnswer{}, false
	}
	question, _, err := wire.ParseDNSQuestion(message.Payload())
	if err != nil || question.Type != wire.DNSTypeA {
		return dnsAnswer{}, false
	}
	name, ok := nameText(message, question.Name)
	if !ok {
		return dnsAnswer{}, false
	}
	return dnsAnswer{server: server, port: port, name: name}, true
}

// dnsResponse returns the DNS message of a UDP datagram from port 53, whose
// checksums hold, with the server that sent it and the port it went to;
// false for any other datagram.
func dnsResponse(datagram []byte) (netip.Addr, uint16, wire.DNSPacket, bool) {
	packet, err := wire.NewIPv4Packet(datagram)
	if err != nil || packet.NextHeader() != wire.IPProtocolUDP || !packet.VerifyChecksum() {
		return netip.Addr{}, 0, nil, false
	}
	source, destination := packet.SrcAddr(), packet.DstAddr()
	header, err := wire.NewUDPPacket(packet.Payload())
	if err != nil || header.SrcPort() != dnsPort || !header.VerifyChecksum(source, destination) {
		return netip.Addr{}, 0, nil, false
	}
	message, err := wire.NewDNSPacket(header.Payload())
	if err != nil {
		return netip.Addr{}, 0, nil, false
	}
	return source, header.DstPort(), message, true
}

// tellsOfTheName reports whether message is a response to a standard query
// with one question that says what the name's addresses are, or that it has
// none: no error, or no such name.
func tellsOfTheName(message wire.DNSPacket) bool {
	rcode := message.Rcode()
	return message.Flags()&wire.DNSFlagResponse != 0 &&
		message.Opcode() == wire.DNSOpcodeQuery &&
		message.QuestionCount() == responseQuestionCount &&
		(rcode == wire.DNSRcodeNoError || rcode == wire.DNSRcodeNXDomain)
}

// responseQuestionCount is how many questions a response has: the one of
// the query it answers.
const responseQuestionCount = 1

// nameText returns the name encoded at name, inside message, as text in
// lower case: its labels read as UTF-8, with invalid bytes replaced,
// joined by dots. It returns false for a name that does not decode.
func nameText(message wire.DNSPacket, name []byte) (string, bool) {
	var labels []string
	for label, err := range message.NameLabels(name) {
		if err != nil {
			return "", false
		}
		labels = append(labels, asciicase.Lower(utf8lossy.String(label)))
	}
	return strings.Join(labels, "."), true
}
