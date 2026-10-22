package sim

import (
	"encoding/binary"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

const (
	dnsTypeAAAA = 28
	// dnsOpcodeNotify is NOTIFY (RFC 1996), which the server does not
	// implement.
	dnsOpcodeNotify = 4
	// opcodeShift is where the opcode sits in the flags' first byte.
	opcodeShift = 3
)

func testZone() zone {
	return newZone([]HostAddress{
		{Name: "echo.test", Address: netip.MustParseAddr("192.0.2.7")},
		{Name: "multi.test.", Address: netip.MustParseAddr("192.0.2.1")},
		{Name: "MULTI.test", Address: netip.MustParseAddr("192.0.2.2")},
	})
}

// dnsQuery returns a standard query with one question for name's records
// of type kind, asking for recursion.
func dnsQuery(id uint16, name string, kind uint16) []byte {
	message := binary.BigEndian.AppendUint16(nil, id)
	message = binary.BigEndian.AppendUint16(message, flagRecursionDesired)
	message = append(message, 0, 1, 0, 0, 0, 0, 0, 0)
	for label := range strings.SplitSeq(name, ".") {
		message = append(message, byte(len(label)))
		message = append(message, label...)
	}
	message = append(message, 0)
	message = binary.BigEndian.AppendUint16(message, kind)
	return binary.BigEndian.AppendUint16(message, dnsClassIN)
}

// mustAnswer returns the response to query, failing the test if there is
// none.
func mustAnswer(t *testing.T, query []byte, names zone) wire.DNSPacket {
	t.Helper()
	response, ok := answerDNS(query, names)
	if !ok {
		t.Fatal("the query went unanswered")
	}
	packet, err := wire.NewDNSPacket(response)
	if err != nil {
		t.Fatalf("the response is not a DNS message: %v", err)
	}
	return packet
}

// answeredAddresses returns the A records of a response, read with the
// stack's own DNS parser.
func answeredAddresses(t *testing.T, packet wire.DNSPacket) []netip.Addr {
	t.Helper()
	_, records, err := wire.ParseDNSQuestion(packet.Payload())
	if err != nil {
		t.Fatalf("the response has no question: %v", err)
	}
	var addresses []netip.Addr
	for range packet.AnswerRecordCount() {
		var record wire.DNSRecord
		if record, records, err = wire.ParseDNSRecord(records); err != nil {
			t.Fatalf("a bad answer record: %v", err)
		}
		if record.Data.Type == wire.DNSTypeA {
			addresses = append(addresses, record.Data.Address)
		}
	}
	return addresses
}

func TestAQueryForAKnownNameIsAnsweredInAnyCase(t *testing.T) {
	t.Parallel()

	packet := mustAnswer(t, dnsQuery(0x1234, "Echo.TEST", dnsTypeA), testZone())

	if packet.TransactionID() != 0x1234 || packet.Rcode() != wire.DNSRcodeNoError {
		t.Errorf("id %#x, rcode %v; want 0x1234, NoError", packet.TransactionID(), packet.Rcode())
	}
	if got := answeredAddresses(t, packet); !slices.Equal(got, []netip.Addr{netip.MustParseAddr("192.0.2.7")}) {
		t.Errorf("addresses %v; want [192.0.2.7]", got)
	}
}

// Names fold the case of ASCII letters alone. Unicode would
// fold the Kelvin sign, U+212A, onto "k" and answer for a host that is not
// there.
func TestOnlyASCIILettersFoldInNames(t *testing.T) {
	t.Parallel()
	names := newZone([]HostAddress{{Name: "kelvin.test", Address: netip.MustParseAddr("192.0.2.1")}})

	packet := mustAnswer(t, dnsQuery(1, "\u212Aelvin.test", dnsTypeA), names)

	if packet.Rcode() != wire.DNSRcodeNXDomain || len(answeredAddresses(t, packet)) != 0 {
		t.Errorf("rcode %v with %v; want NXDomain without addresses", packet.Rcode(), answeredAddresses(t, packet))
	}
}

func TestANameListedTwiceHasBothAddresses(t *testing.T) {
	t.Parallel()

	packet := mustAnswer(t, dnsQuery(1, "multi.test", dnsTypeA), testZone())

	want := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")}
	if got := answeredAddresses(t, packet); !slices.Equal(got, want) {
		t.Errorf("addresses %v; want %v", got, want)
	}
}

func TestUnknownNamesDoNotExist(t *testing.T) {
	t.Parallel()

	packet := mustAnswer(t, dnsQuery(7, "nowhere.test", dnsTypeA), testZone())

	if packet.Rcode() != wire.DNSRcodeNXDomain || len(answeredAddresses(t, packet)) != 0 {
		t.Errorf("rcode %v with %v; want NXDomain without addresses", packet.Rcode(), answeredAddresses(t, packet))
	}
}

func TestKnownNamesHaveNoOtherRecords(t *testing.T) {
	t.Parallel()

	packet := mustAnswer(t, dnsQuery(7, "echo.test", dnsTypeAAAA), testZone())

	if packet.Rcode() != wire.DNSRcodeNoError || len(answeredAddresses(t, packet)) != 0 {
		t.Errorf("rcode %v with %v; want NoError without addresses", packet.Rcode(), answeredAddresses(t, packet))
	}
}

func TestMalformedAndUnsupportedQueries(t *testing.T) {
	t.Parallel()
	twoQuestions := dnsQuery(1, "echo.test", dnsTypeA)
	twoQuestions[5] = 2
	truncated := dnsQuery(1, "echo.test", dnsTypeA)[:20]
	inverseQuery := dnsQuery(1, "echo.test", dnsTypeA)
	inverseQuery[2] |= 0x08
	chaosClass := dnsQuery(1, "echo.test", dnsTypeA)
	chaosClass[len(chaosClass)-1] = 3
	cases := []struct {
		name  string
		query []byte
		want  wire.DNSRcode
	}{
		{name: "two questions", query: twoQuestions, want: wire.DNSRcodeFormErr},
		{name: "truncated", query: truncated, want: wire.DNSRcodeFormErr},
		{name: "inverse query", query: inverseQuery, want: wire.DNSRcodeNotImp},
		{name: "chaos class", query: chaosClass, want: wire.DNSRcodeRefused},
	}

	for _, test := range cases {
		if got := mustAnswer(t, test.query, testZone()).Rcode(); got != test.want {
			t.Errorf("%s: rcode %v; want %v", test.name, got, test.want)
		}
	}
}

func TestAResponseRepeatsTheQueryOpcode(t *testing.T) {
	t.Parallel()
	notify := dnsQuery(1, "echo.test", dnsTypeA)
	notify[2] |= dnsOpcodeNotify << opcodeShift

	packet := mustAnswer(t, notify, testZone())

	if packet.Rcode() != wire.DNSRcodeNotImp || packet.Opcode() != wire.DNSOpcode(dnsOpcodeNotify) {
		t.Errorf("rcode %v, opcode %v; want NotImp and Unknown(4)", packet.Rcode(), packet.Opcode())
	}
}

func TestShortMessagesAndResponsesGoUnanswered(t *testing.T) {
	t.Parallel()
	response := dnsQuery(1, "echo.test", dnsTypeA)
	response[2] |= 0x80

	for name, message := range map[string][]byte{"short": make([]byte, dnsHeaderLen-1), "response": response} {
		if answer, ok := answerDNS(message, testZone()); ok {
			t.Errorf("%s: answered with %v", name, answer)
		}
	}
}

func TestManyAddressesAreTruncatedToOneUDPMessage(t *testing.T) {
	t.Parallel()
	var hosts []HostAddress
	for i := range 40 {
		hosts = append(hosts, HostAddress{Name: "big.test", Address: netip.AddrFrom4([4]byte{192, 0, 2, byte(i)})})
	}

	packet := mustAnswer(t, dnsQuery(1, "big.test", dnsTypeA), newZone(hosts))

	if len(packet) > maxUDPMessageLen || packet.Flags()&wire.DNSFlagTruncated == 0 {
		t.Errorf("%d bytes, flags %#x; want at most %d bytes, truncated", len(packet), packet.Flags(), maxUDPMessageLen)
	}
	if got := answeredAddresses(t, packet); len(got) != int(packet.AnswerRecordCount()) {
		t.Errorf("%d addresses for %d answers", len(got), packet.AnswerRecordCount())
	}
}

func TestTheDNSServerNeverPanicsOnArbitraryBytes(t *testing.T) {
	t.Parallel()
	random := newXorShift(0x2545_F491_4F6C_DD1D)
	names := testZone()

	for range 20_000 {
		message := dnsQuery(uint16(random.next()), "echo.test", dnsTypeA)
		for range random.next() % 4 {
			message[random.next()%uint64(len(message))] = byte(random.next())
		}
		message = message[:random.next()%uint64(len(message)+1)]
		answerDNS(message, names)
	}
}

func TestHostsMustHaveIPv4Addresses(t *testing.T) {
	t.Parallel()
	hosts := []HostAddress{
		{Name: "v4.test", Address: netip.MustParseAddr("192.0.2.1")},
		{Name: "v6\x7f.test", Address: netip.MustParseAddr("2001:db8::1")},
	}
	// The name is quoted as debugtext writes it; Go's %q would write "v6\x7f.test".
	const want = `"v6\u{7f}.test" in hosts needs an IPv4 address`

	err := checkHosts(hosts)

	if err == nil || err.Error() != want {
		t.Errorf("checkHosts = %v; want %s", err, want)
	}
	if err := checkHosts(DefaultNetworkConfig().Hosts); err != nil {
		t.Errorf("the default hosts are refused: %v", err)
	}
}

func TestANameLongerThanDNSAllowsIsAFormatError(t *testing.T) {
	t.Parallel()
	label := strings.Repeat("x", maxLabelLen)
	name := strings.Join([]string{label, label, label, label, label}, ".")

	packet := mustAnswer(t, dnsQuery(1, name, dnsTypeA), testZone())

	if packet.Rcode() != wire.DNSRcodeFormErr {
		t.Errorf("rcode %v for a %d-byte name; want FormErr", packet.Rcode(), len(name))
	}
	if _, ok := parseQuestion(make([]byte, dnsHeaderLen-1)); ok {
		t.Error("a message shorter than a header has a question")
	}
}
