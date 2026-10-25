package satnet

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The lookup tests' stack is at lookupLocal, and asks lookupServer, or
// lookupOtherServer too.
var (
	lookupLocal       = ipv4(10, 64, 0, 2)
	lookupServer      = ipv4(10, 64, 0, 1)
	lookupOtherServer = ipv4(10, 64, 0, 3)
)

// The response codes the tests' servers answer with (RFC 1035 §4.1.1).
const (
	noError       = byte(wire.DNSRcodeNoError)
	serverFailure = byte(wire.DNSRcodeServFail)
	nameError     = byte(wire.DNSRcodeNXDomain)
)

// The fields of a DNS header that a test's answer changes (RFC 1035
// §4.1.1): the QR bit and the opcode in the first flags octet, the
// response code in the low bits of the second, and the low octets of the
// question and answer counts.
const (
	dnsResponseOctet      = 2
	dnsResponseBit        = 0x80
	dnsRcodeOctet         = 3
	dnsRcodeBits          = 0x0F
	dnsQuestionCountOctet = 5
	dnsAnswerCountOctet   = 7
	// dnsStatusResponse is the first flags octet of a response to a
	// message other than a standard query: a status request, as the stack
	// names opcode 1. The opcode sits above the octet's three low bits.
	dnsStatusResponse = dnsResponseBit | byte(wire.DNSOpcodeStatus)<<3
	// questionTypeFromEnd is where the low octet of the question's type is,
	// counted from the end of a message with one question and no records:
	// before the two octets of its class.
	questionTypeFromEnd = 3
)

// answerForTheQuestion is an answer record for the A records of the name a
// message's question asks about: a compression pointer to that name, right
// after the header (RFC 1035 §4.1.4), type A, class IN, a TTL of 60 s, and
// the address 192.0.2.7.
var answerForTheQuestion = []byte{0xC0, dnsHeaderLen, 0, aRecordType, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 7}

// lookupStep is how often outcome polls a lookup, and lookupSteps how often
// at most: 30 s in all, more than both servers' time to answer.
const (
	lookupStep  = 500 * time.Millisecond
	lookupSteps = 60
)

// stackWith returns a stack whose DNS servers are servers.
func stackWith(t *testing.T, servers []netip.Addr, now time.Time) *stack {
	t.Helper()
	s := newStack(DefaultTuning(), now)
	if err := s.configure(IPv4Settings{LocalAddress: lookupLocal, DNSServers: servers}, engineMTU, now); err != nil {
		t.Fatal(err)
	}
	return s
}

// lookUp looks name up, and returns the lookup and the query that went.
func lookUp(t *testing.T, s *stack, name string, now time.Time) (lookupID, []byte) {
	t.Helper()
	id, err := s.startLookup(name)
	if err != nil {
		t.Fatal(err)
	}
	s.poll(now)
	query, ok := s.nextOutgoing()
	if !ok {
		t.Fatal("the query does not go")
	}
	return id, query
}

// answerFrom returns from's answer to query: its question back, with rcode
// and no records.
func answerFrom(t *testing.T, from netip.Addr, query []byte, rcode byte) []byte {
	t.Helper()
	header, message := udpOf(t, query)
	message = append([]byte(nil), message...)
	message[dnsResponseOctet] |= dnsResponseBit
	message[dnsRcodeOctet] = message[dnsRcodeOctet]&^dnsRcodeBits | rcode
	return datagramTo(header.SrcPort(), from, message)
}

// udpOf returns the UDP header and payload of datagram.
func udpOf(t *testing.T, datagram []byte) (wire.UDPPacket, []byte) {
	t.Helper()
	packet, err := wire.NewIPv4Packet(datagram)
	if err != nil {
		t.Fatal(err)
	}
	header, err := wire.NewUDPPacket(packet.Payload())
	if err != nil {
		t.Fatal(err)
	}
	return header, header.Payload()
}

// datagramTo returns a UDP datagram from port 53 of from to our port.
func datagramTo(port uint16, from netip.Addr, payload []byte) []byte {
	return udpDatagram(netip.AddrPortFrom(from, dnsPort), port, payload)
}

// udpDatagram returns a UDP datagram from source to our port, at
// lookupLocal, carrying payload, with its checksums.
func udpDatagram(source netip.AddrPort, port uint16, payload []byte) []byte {
	header := wire.UDPRepr{SrcPort: source.Port(), DstPort: port}
	ip := wire.IPv4Repr{
		SrcAddr:    source.Addr(),
		DstAddr:    lookupLocal,
		NextHeader: wire.IPProtocolUDP,
		PayloadLen: header.HeaderLen() + len(payload),
		HopLimit:   wire.DefaultHopLimit,
	}
	datagram := make([]byte, ip.BufferLen())
	packet := wire.IPv4Packet(datagram)
	ip.Emit(packet, wire.ChecksumCapabilities{})
	header.Emit(wire.UDPPacket(packet.Payload()), source.Addr(), lookupLocal, payload, wire.ChecksumCapabilities{})
	return datagram
}

// outcome polls every half second until the lookup has an outcome, and
// returns it.
func outcome(t *testing.T, s *stack, id lookupID, now time.Time) lookup {
	t.Helper()
	for range lookupSteps {
		s.poll(now)
		for _, ok := s.nextOutgoing(); ok; _, ok = s.nextOutgoing() {
		}
		if result := s.lookupResult(id); result.kind != lookupPending {
			return result
		}
		now = now.Add(lookupStep)
	}
	t.Fatal("the lookup never ended")
	return lookup{}
}

// lookupKindNames name where a lookup stands, for the tests' messages.
var lookupKindNames = map[lookupKind]string{
	lookupPending:    "pending",
	lookupResolved:   "resolved",
	lookupNoAddress:  "without an address",
	lookupUnanswered: "unanswered",
}

// expectEnd checks that a lookup ended as want says.
func expectEnd(t *testing.T, got lookup, want lookupKind) {
	t.Helper()
	if got.kind != want {
		t.Errorf("the lookup ended %s, want %s", lookupKindNames[got.kind], lookupKindNames[want])
	}
}

func TestLookupsStartedTogetherAllGoThoughNoOtherSocketAsks(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupServer}, now)
	// The line is busy: nothing but new queries may go now.
	s.setLineReady(false)
	for _, name := range []string{"a.example.com", "b.example.com"} {
		if _, err := s.startLookup(name); err != nil {
			t.Fatal(err)
		}
	}

	s.poll(now)

	queries := 0
	for _, ok := s.nextOutgoing(); ok; _, ok = s.nextOutgoing() {
		queries++
	}
	if queries != 2 {
		t.Errorf("%d queries went, want 2", queries)
	}
}

func TestANameAServerSaysDoesNotExistHasNoAddress(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupServer}, now)
	id, query := lookUp(t, s, "absent.example.com", now)

	s.deliver(answerFrom(t, lookupServer, query, nameError))

	expectEnd(t, outcome(t, s, id, now), lookupNoAddress)
}

func TestANameWithoutAnIPv4AddressHasNoAddress(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupServer}, now)
	id, query := lookUp(t, s, "v6.example.com", now)

	s.deliver(answerFrom(t, lookupServer, query, noError))

	expectEnd(t, outcome(t, s, id, now), lookupNoAddress)
}

func TestAServerFailureSaysNothingAboutTheName(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupServer}, now)
	id, query := lookUp(t, s, "present.example.com", now)

	s.deliver(answerFrom(t, lookupServer, query, serverFailure))

	expectEnd(t, outcome(t, s, id, now), lookupUnanswered)
}

func TestALookupWhoseServerWentAwayWasUnanswered(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupOtherServer, lookupServer}, now)
	id, _ := lookUp(t, s, "present.example.com", now)
	// The first server never answered; the second is asked, and then PPP
	// renegotiates without it.
	now = now.Add(10_500 * time.Millisecond)
	s.poll(now)
	settings := IPv4Settings{LocalAddress: lookupLocal, DNSServers: []netip.Addr{lookupOtherServer}}
	if err := s.configure(settings, engineMTU, now); err != nil {
		t.Fatal(err)
	}

	expectEnd(t, outcome(t, s, id, now), lookupUnanswered)
}

func TestOnlyOurDNSServersAnswersToOurQueriesTell(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupServer}, now)
	app, err := s.openUDP()
	if err != nil {
		t.Fatal(err)
	}
	appPort := s.udp(app).Endpoint().Port
	id, query := lookUp(t, s, "present.example.com", now)
	stranger := ipv4(192, 0, 2, 53)

	// Another host's say, and the answer to the app's own question.
	s.deliver(answerFrom(t, stranger, query, nameError))
	_, message := udpOf(t, answerFrom(t, lookupServer, query, nameError))
	s.deliver(datagramTo(appPort, lookupServer, message))

	expectEnd(t, outcome(t, s, id, now), lookupUnanswered)
}

func TestAResolvedLookupGivesItsAddresses(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupServer}, now)
	id, query := lookUp(t, s, "echo.test", now)
	_, message := udpOf(t, answerFrom(t, lookupServer, query, noError))
	message[dnsAnswerCountOctet] = 1
	message = append(message, answerForTheQuestion...)
	header, _ := udpOf(t, query)

	s.deliver(datagramTo(header.SrcPort(), lookupServer, message))

	got := outcome(t, s, id, now)
	if got.kind != lookupResolved || len(got.addresses) != 1 || got.addresses[0] != ipv4(192, 0, 2, 7) {
		t.Errorf("the lookup ended %s with %v", lookupKindNames[got.kind], got.addresses)
	}
}

func TestALookupNeedsADNSServerAndAName(t *testing.T) {
	now := time.Now()

	_, err := stackWith(t, nil, now).startLookup("echo.test")
	if !errors.Is(err, ErrNoDNSServer) {
		t.Errorf("startLookup without a server = %v, want ErrNoDNSServer", err)
	}
	_, err = stackWith(t, []netip.Addr{lookupServer}, now).startLookup("")
	if !errors.Is(err, ErrInvalidHost) {
		t.Errorf("startLookup of no name = %v, want ErrInvalidHost", err)
	}
}

func TestALookupThatIsOverIsSpent(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupServer}, now)
	id, query := lookUp(t, s, "absent.example.com", now)
	s.deliver(answerFrom(t, lookupServer, query, nameError))
	expectEnd(t, outcome(t, s, id, now), lookupNoAddress)

	expectEnd(t, s.lookupResult(id), lookupUnanswered)
	// Abandoning it is harmless.
	s.abandonLookup(id)
}

func TestAnAbandonedLookupIsCancelled(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupServer}, now)
	pending, _ := lookUp(t, s, "slow.example.com", now)
	settled, query := lookUp(t, s, "absent.example.com", now)
	s.deliver(answerFrom(t, lookupServer, query, nameError))
	s.poll(now)

	s.abandonLookup(pending)
	s.abandonLookup(settled)

	if len(s.lookups) != 0 {
		t.Errorf("%d lookups are left", len(s.lookups))
	}
	// The resolver no longer asks for them: no query goes in all the time
	// the servers would have had.
	for range longestLookup / lookupStep {
		now = now.Add(lookupStep)
		s.poll(now)
		if sent := drain(s); len(sent) > 0 {
			t.Fatalf("%d datagrams went after the lookups were abandoned", len(sent))
		}
	}
	if _, err := s.startLookup("next.example.com"); err != nil {
		t.Errorf("a later lookup fails: %v", err)
	}
}

func TestAnAnswerAboutAnotherNameSaysNothingAboutALookup(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupServer}, now)
	present, _ := lookUp(t, s, "present.example.com", now)
	absent, absentQuery := lookUp(t, s, "absent.example.com", now)

	s.deliver(answerFrom(t, lookupServer, absentQuery, nameError))

	expectEnd(t, outcome(t, s, absent, now), lookupNoAddress)
	expectEnd(t, outcome(t, s, present, now), lookupUnanswered)
}

func TestAnAnswerSpellingTheNameInAnotherCaseStillTells(t *testing.T) {
	now := time.Now()
	s := stackWith(t, []netip.Addr{lookupServer}, now)
	id, query := lookUp(t, s, "absent.example.com", now)
	header, message := udpOf(t, answerFrom(t, lookupServer, query, nameError))
	// The server spells the name its own way, as DNS allows (RFC 4343): its
	// labels, between the header and the type and class, in capitals.
	question := message[dnsHeaderLen : len(message)-questionTypeAndClassLen]
	copy(question, bytes.ToUpper(question))

	s.deliver(datagramTo(header.DstPort(), lookupServer, message))

	expectEnd(t, outcome(t, s, id, now), lookupNoAddress)
}

// questionTypeAndClassLen is the length of a question's type and class,
// which follow its name (RFC 1035 §4.1.2).
const questionTypeAndClassLen = 4

func TestOnlyADNSServersAnswerAboutANameIsNoticed(t *testing.T) {
	question := dnsMessage(1, aRecordType)
	_, message := udpOf(t, question)
	response := append([]byte(nil), message...)
	response[dnsResponseOctet] |= dnsResponseBit
	answer := datagramTo(queryPort, lookupServer, response)
	answering := func(rcode wire.DNSRcode) func([]byte) []byte {
		return func([]byte) []byte {
			return datagramTo(queryPort, lookupServer, withOctet(response, dnsRcodeOctet, byte(rcode)))
		}
	}
	for _, c := range []struct {
		name   string
		change func(datagram []byte) []byte
	}{
		{"not an IPv4 datagram", func([]byte) []byte { return []byte("nothing") }},
		{"a TCP segment", func([]byte) []byte { return segment(1, wire.TCPControlSYN, "") }},
		// Each damage leaves the rest well formed, so that only the checksum
		// it names can show it.
		{"a damaged IPv4 header", damageTheIPv4Identification},
		{"a damaged UDP datagram", damageTheQuestionsName},
		{"not from port 53", func([]byte) []byte { return udpFrom(5353, response) }},
		{"too short for DNS", func([]byte) []byte { return datagramTo(queryPort, lookupServer, response[:4]) }},
		{"a query", func([]byte) []byte { return datagramTo(queryPort, lookupServer, message) }},
		{"another opcode", func([]byte) []byte {
			return datagramTo(queryPort, lookupServer, withOctet(response, dnsResponseOctet, dnsStatusResponse))
		}},
		{"two questions", func([]byte) []byte {
			return datagramTo(queryPort, lookupServer, withOctet(response, dnsQuestionCountOctet, 2))
		}},
		// Only no error and no such name say what the name's addresses are.
		{"a format error", answering(wire.DNSRcodeFormErr)},
		{"a server failure", answering(wire.DNSRcodeServFail)},
		{"a query the server does not implement", answering(wire.DNSRcodeNotImp)},
		{"a refusal", answering(wire.DNSRcodeRefused)},
		{"another question type", func([]byte) []byte {
			return datagramTo(queryPort, lookupServer, withOctet(response, len(response)-questionTypeFromEnd, txtRecordType))
		}},
		{"another question class", func([]byte) []byte {
			return datagramTo(queryPort, lookupServer, withOctet(response, len(response)-1, chaosClass))
		}},
		{"a name pointing outside the message", func([]byte) []byte {
			return datagramTo(queryPort, lookupServer, append(response[:dnsHeaderLen:dnsHeaderLen], pointerOutside...))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := parseDNSAnswer(c.change(append([]byte(nil), answer...))); ok {
				t.Error("taken for an answer about the name")
			}
		})
	}
	if got, ok := parseDNSAnswer(answer); !ok || got != (dnsAnswer{server: lookupServer, port: queryPort, name: "example.com"}) {
		t.Errorf("parseDNSAnswer = %+v, %t", got, ok)
	}
}

// dnsHeaderLen is the length of a DNS message's header (RFC 1035 §4.1.1).
const dnsHeaderLen = 12

// firstLetterOfTheQuestion is where the first letter of a message's question
// is: after the header, and the length octet of the name's first label (RFC
// 1035 §4.1.2).
const firstLetterOfTheQuestion = dnsHeaderLen + 1

// chaosClass is the CHAOS class (RFC 1035 §3.2.4), which a question about
// a name's IPv4 addresses never has: only class IN does.
const chaosClass = 3

// ipv4IdentificationLowOctet is where the low octet of the identification
// field of an IPv4 header is (RFC 791 §3.1).
const ipv4IdentificationLowOctet = 5

// damageTheIPv4Identification flips a bit of the identification field of
// datagram's IPv4 header, which only the header's checksum covers: the UDP
// checksum leaves it out, and nothing else reads it. (Flipping every bit of
// a field of zeros would not show: in one's complement, all ones is zero
// too.)
func damageTheIPv4Identification(datagram []byte) []byte {
	datagram[ipv4IdentificationLowOctet] ^= 1
	return datagram
}

// damageTheQuestionsName flips a bit of a letter of the name in datagram's
// DNS question, which leaves another name — 'e' becomes 'd' — that only the
// UDP checksum shows.
func damageTheQuestionsName(datagram []byte) []byte {
	message := wire.UDPPacket(wire.IPv4Packet(datagram).Payload()).Payload()
	message[firstLetterOfTheQuestion] ^= 1
	return datagram
}

// pointerOutside is a question for the A records of a name that is a
// compression pointer past the end of its message (RFC 1035 §4.1.4).
var pointerOutside = []byte{0xC0, 0xFF, 0, aRecordType, 0, 1}

// udpFrom returns a UDP datagram from sourcePort of the lookup server to
// the query port, carrying payload.
func udpFrom(sourcePort uint16, payload []byte) []byte {
	datagram := datagramTo(queryPort, lookupServer, payload)
	packet := wire.IPv4Packet(datagram)
	header := wire.UDPPacket(packet.Payload())
	header.SetSrcPort(sourcePort)
	header.FillChecksum(packet.SrcAddr(), packet.DstAddr())
	return datagram
}

// withOctet returns a copy of message whose octet at index is value.
func withOctet(message []byte, index int, value byte) []byte {
	changed := append([]byte(nil), message...)
	changed[index] = value
	return changed
}
