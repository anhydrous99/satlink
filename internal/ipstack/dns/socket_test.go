package dns

import (
	"bytes"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestStartQueryValidatesTheName(t *testing.T) {
	t.Parallel()
	label63 := strings.Repeat("a", maxLabelLen)
	tests := []struct {
		name    string
		lookup  string
		wantErr error
	}{
		{"an empty name", "", ErrInvalidName},
		{"a lone dot", ".", ErrInvalidName},
		{"an empty label", "example..com", ErrInvalidName},
		{"an empty first label", ".example.com", ErrInvalidName},
		{"two trailing dots", "example.com..", ErrInvalidName},
		{"a 64-octet label", strings.Repeat("a", maxLabelLen+1) + ".com", ErrInvalidName},
		{"a 63-octet label", label63 + ".com", nil},
		{"a trailing dot", "example.com.", nil},
		// Three 63-octet labels and one of 61 take 4 × 1 + 250 octets, and
		// the root label one more.
		{"a name of 255 octets", strings.Join([]string{label63, label63, label63, label63[:61]}, "."), nil},
		{"a name of 256 octets", strings.Join([]string{label63, label63, label63, label63[:62]}, "."), ErrNameTooLong},
		{"a name too long before an empty label", strings.Join([]string{label63, label63, label63, label63, ""}, "."), ErrNameTooLong},
		{"an empty label before a name grows too long", strings.Join([]string{"", label63, label63, label63, label63}, "."), ErrInvalidName},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s := NewSocket(testServers)

			_, err := s.StartQuery(testContext(t), test.lookup, wire.DNSTypeA)

			if !errors.Is(err, test.wantErr) {
				t.Errorf("StartQuery(%q) = %v, want %v", test.lookup, err, test.wantErr)
			}
		})
	}
}

func TestRefusedNameStartsNoQuery(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(testServers)

	if _, err := s.StartQuery(cx, "", wire.DNSTypeA); err == nil {
		t.Fatal("StartQuery of an empty name succeeded")
	}

	if sent := dispatchAt(t, s, cx, 0); len(sent) != 0 {
		t.Errorf("Dispatch emitted %d queries, want none", len(sent))
	}
	if !s.PollAt().IsOnIngress() {
		t.Errorf("PollAt = %v, want on ingress", s.PollAt())
	}
}

func TestTrailingDotIsTheSameName(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(testServers)
	mustStartQuery(t, s, cx, "example.com.")

	query := mustDispatchOne(t, s, cx, 0)

	if question := query.message[headerLen:]; !bytes.Equal(question, questionSection(exampleName, wire.DNSTypeA)) {
		t.Errorf("question = %q, want example.com", question)
	}
}

func TestFirstQueryAsksTheFirstServerWithRecursionDesired(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(testServers)
	mustStartQuery(t, s, cx, "example.com")

	query := mustDispatchOne(t, s, cx, 0)

	wantMessage := []byte("\x00\x03\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01")
	wantIP := wire.IPv4Repr{
		SrcAddr:    localAddr,
		DstAddr:    firstServer,
		NextHeader: wire.IPProtocolUDP,
		PayloadLen: wire.UDPHeaderLen + len(wantMessage),
		HopLimit:   wire.DefaultHopLimit,
	}
	if query.ip != wantIP {
		t.Errorf("IP header = %v, want %v", query.ip, wantIP)
	}
	if wantHeader := (wire.UDPRepr{SrcPort: firstQueryPort, DstPort: dnsPort}); query.header != wantHeader {
		t.Errorf("UDP header = %v, want %v", query.header, wantHeader)
	}
	if !bytes.Equal(query.message, wantMessage) {
		t.Errorf("message = %q, want %q (id %#x, recursion desired)", query.message, wantMessage, firstQueryID)
	}
}

func TestQueryIsRetransmittedWithBackoffThenMovesToTheNextServer(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(testServers)
	handle := mustStartQuery(t, s, cx, "example.com")
	// Each server gets the query at 0, 1, 3 and 7 s after its first attempt;
	// the first Dispatch more than 10 s after that attempt moves the query
	// on, and after the last server it fails.
	steps := []struct {
		elapsed    time.Duration
		wantServer netip.Addr // the zero Addr when nothing is sent
	}{
		{0, firstServer},
		{999 * time.Millisecond, netip.Addr{}},
		{1 * time.Second, firstServer},
		{3 * time.Second, firstServer},
		{7 * time.Second, firstServer},
		{10 * time.Second, netip.Addr{}},
		{10*time.Second + time.Millisecond, secondServer},
		{11*time.Second + time.Millisecond, secondServer},
		{13*time.Second + time.Millisecond, secondServer},
		{17*time.Second + time.Millisecond, secondServer},
		{20*time.Second + time.Millisecond, netip.Addr{}},
	}
	for _, step := range steps {
		if got := destinationOf(t, dispatchAt(t, s, cx, step.elapsed)); got != step.wantServer {
			t.Fatalf("at %v: sent a query to %v, want %v", step.elapsed, got, step.wantServer)
		}
	}
	assertPending(t, s, handle)

	if sent := dispatchAt(t, s, cx, 20*time.Second+2*time.Millisecond); len(sent) != 0 {
		t.Fatalf("after the last server: sent %d queries, want none", len(sent))
	}
	assertFailed(t, s, handle)
}

func TestPollAtIsTheNextRetransmission(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(testServers)

	if !s.PollAt().IsOnIngress() {
		t.Errorf("PollAt without queries = %v, want on ingress", s.PollAt())
	}
	mustStartQuery(t, s, cx, "example.com")
	if want := socket.PollAtTime(time.Time{}); !s.PollAt().Equal(want) {
		t.Errorf("PollAt of a new query = %v, want the epoch, a time already past", s.PollAt())
	}
	for _, step := range []struct{ sentAt, wantNext time.Duration }{
		{0, 1 * time.Second},
		{1 * time.Second, 3 * time.Second},
		{3 * time.Second, 7 * time.Second},
		{7 * time.Second, 15 * time.Second},
	} {
		mustDispatchOne(t, s, cx, step.sentAt)
		if want := socket.PollAtTime(at(step.wantNext)); !s.PollAt().Equal(want) {
			t.Errorf("PollAt after the query sent at %v = %v, want %v", step.sentAt, s.PollAt(), want)
		}
	}
}

func TestPollAtIsTheEarliestPendingQuery(t *testing.T) {
	t.Parallel()
	s, cx, first, query := sentExampleQuery(t)
	mustStartQuery(t, s, cx, "example.org")
	mustDispatchOne(t, s, cx, 500*time.Millisecond)

	if want := socket.PollAtTime(at(time.Second)); !s.PollAt().Equal(want) {
		t.Errorf("PollAt = %v, want %v, the first query's retransmission", s.PollAt(), want)
	}

	answer(t, s, query, responseTo(query, aRecord(questionPointer, exampleAddress)).encode())

	if want := socket.PollAtTime(at(1500 * time.Millisecond)); !s.PollAt().Equal(want) {
		t.Errorf("PollAt after the first query completed = %v, want %v, the second one's", s.PollAt(), want)
	}
	assertCompleted(t, s, first, exampleAddress)
}

func TestTimeoutRunsFromTheFirstAttemptEvenWhenRefused(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(testServers)
	mustStartQuery(t, s, cx, "example.com")

	var refused []sentQuery
	if err := s.Dispatch(cx, recordingEmit(&refused, errRefused)); !errors.Is(err, errRefused) {
		t.Fatalf("Dispatch with emit refused = %v, want %v", err, errRefused)
	}
	if want := socket.PollAtTime(time.Time{}); !s.PollAt().Equal(want) {
		t.Errorf("PollAt after the refusal = %v, want the query still due", s.PollAt())
	}

	mustDispatchOne(t, s, cx, 500*time.Millisecond)
	for _, sentAt := range []time.Duration{1500 * time.Millisecond, 3500 * time.Millisecond, 7500 * time.Millisecond} {
		if query := mustDispatchOne(t, s, cx, sentAt); query.ip.DstAddr != firstServer {
			t.Fatalf("at %v the query went to %v, want %v", sentAt, query.ip.DstAddr, firstServer)
		}
	}

	// The first server's 10 s started with the refused attempt, at 0, not
	// with the first one sent, at 0.5 s.
	query := mustDispatchOne(t, s, cx, 10*time.Second+time.Millisecond)
	if query.ip.DstAddr != secondServer {
		t.Errorf("at 10.001 s the query went to %v, want %v", query.ip.DstAddr, secondServer)
	}
}

func TestRefusedRetransmissionGoesAtTheNextDispatchWithTheSameDelay(t *testing.T) {
	t.Parallel()
	s, cx, _, _ := sentExampleQuery(t)

	cx.SetNow(at(time.Second))
	var refused []sentQuery
	if err := s.Dispatch(cx, recordingEmit(&refused, errRefused)); !errors.Is(err, errRefused) {
		t.Fatalf("Dispatch with emit refused = %v, want %v", err, errRefused)
	}
	if want := socket.PollAtTime(at(time.Second)); !s.PollAt().Equal(want) {
		t.Errorf("PollAt after the refusal = %v, want %v, unchanged", s.PollAt(), want)
	}

	retransmission := mustDispatchOne(t, s, cx, 1100*time.Millisecond)

	if !bytes.Equal(retransmission.message, refused[0].message) {
		t.Errorf("retransmission %q differs from the refused one %q", retransmission.message, refused[0].message)
	}
	// The delay after the second attempt is still 2 s: the refusal did not
	// double it.
	if want := socket.PollAtTime(at(3100 * time.Millisecond)); !s.PollAt().Equal(want) {
		t.Errorf("PollAt after the retransmission = %v, want %v", s.PollAt(), want)
	}
}

func TestDispatchSendsOneQueryAtATime(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(testServers)
	mustStartQuery(t, s, cx, "example.com")
	mustStartQuery(t, s, cx, "example.org")

	first := mustDispatchOne(t, s, cx, 0)
	second := mustDispatchOne(t, s, cx, 0)
	rest := dispatchAt(t, s, cx, 0)

	if first.header.SrcPort == second.header.SrcPort {
		t.Errorf("both dispatches sent the query from port %d", first.header.SrcPort)
	}
	if len(rest) != 0 {
		t.Errorf("a third Dispatch sent %d queries, want none", len(rest))
	}
}

func TestQueryFailingInDispatchDoesNotHoldUpTheNext(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket([]netip.Addr{firstServer})
	exhausted := mustStartQuery(t, s, cx, "example.com")
	mustDispatchOne(t, s, cx, 0)
	fresh := mustStartQuery(t, s, cx, "example.org")

	query := mustDispatchOne(t, s, cx, 15*time.Second)

	if question := query.message[headerLen:]; !bytes.Equal(question, questionSection([]byte("\x07example\x03org\x00"), wire.DNSTypeA)) {
		t.Errorf("sent the question %q, want example.org", question)
	}
	assertFailed(t, s, exhausted)
	assertPending(t, s, fresh)
}

func TestAnswerCompletesTheQuery(t *testing.T) {
	t.Parallel()
	s, _, handle, query := sentExampleQuery(t)

	answer(t, s, query, responseTo(query, aRecord(questionPointer, exampleAddress)).encode())

	assertCompleted(t, s, handle, exampleAddress)
}

func TestNXDomainFailsTheQuery(t *testing.T) {
	t.Parallel()
	s, _, handle, query := sentExampleQuery(t)
	response := responseTo(query)
	response.rcode = wire.DNSRcodeNXDomain

	answer(t, s, query, response.encode())

	assertFailed(t, s, handle)
}

func TestNXDomainFailsEveryQueryItMatches(t *testing.T) {
	t.Parallel()
	s, cx, first, query := sentExampleQuery(t)
	second := mustStartQuery(t, s, cx, "example.org")
	// A second query with the first one's port and transaction id, which
	// the random source could draw.
	other, ok := s.queryAt(second)
	if !ok {
		t.Fatal("the second query has no slot")
	}
	other.pending.port = query.header.SrcPort
	other.pending.transactionID = wire.DNSPacket(query.message).TransactionID()
	response := responseTo(query)
	response.rcode = wire.DNSRcodeNXDomain

	answer(t, s, query, response.encode())

	assertFailed(t, s, first)
	assertFailed(t, s, second)
}

func TestResponseWithoutAddressFailsTheQuery(t *testing.T) {
	t.Parallel()
	tests := map[string][]testRecord{
		"no answer":                  nil,
		"an AAAA record only":        {{name: questionPointer, recordType: wire.DNSTypeAAAA, data: make([]byte, 16)}},
		"an A record for other name": {aRecord(targetName, exampleAddress)},
		"a CNAME without its target": {cnameRecord(questionPointer, targetName)},
	}
	for name, answers := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, handle, query := sentExampleQuery(t)

			answer(t, s, query, responseTo(query, answers...).encode())

			assertFailed(t, s, handle)
		})
	}
}

func TestQueryKeepsAtMostMaxResultCountAddresses(t *testing.T) {
	t.Parallel()
	s, _, handle, query := sentExampleQuery(t)
	var answers []testRecord
	var addresses []netip.Addr
	for host := range byte(MaxResultCount + 2) {
		address := netip.AddrFrom4([4]byte{203, 0, 113, host + 1})
		answers = append(answers, aRecord(questionPointer, address))
		addresses = append(addresses, address)
	}

	answer(t, s, query, responseTo(query, answers...).encode())

	assertCompleted(t, s, handle, addresses[:MaxResultCount]...)
}

func TestCNAMEIsFollowed(t *testing.T) {
	t.Parallel()
	s, _, handle, query := sentExampleQuery(t)
	response := responseTo(query,
		aRecord(questionPointer, exampleAddress),
		cnameRecord(questionPointer, targetName),
		aRecord(targetName, targetAddress),
		// After the CNAME, records for the alias no longer match.
		aRecord(questionPointer, netip.MustParseAddr("203.0.113.82")),
	)

	answer(t, s, query, response.encode())

	assertCompleted(t, s, handle, exampleAddress, targetAddress)
}

func TestCNAMETargetBecomesTheQuerysName(t *testing.T) {
	t.Parallel()
	s, cx, handle, query := sentExampleQuery(t)
	// The record after the CNAME is malformed, so the response settles
	// nothing, but the query asks for the CNAME's target from then on.
	response := responseTo(query, cnameRecord(questionPointer, targetName))
	message := append(response.encode(), forwardPointer...)
	wire.DNSPacket(message).SetAnswerRecordCount(2)
	answer(t, s, query, message)
	assertPending(t, s, handle)

	retransmission := mustDispatchOne(t, s, cx, time.Second)

	if question := retransmission.message[headerLen:]; !bytes.Equal(question, questionSection(targetName, wire.DNSTypeA)) {
		t.Errorf("the retransmission asks %q, want www.example.net", question)
	}
}

func TestResponsesForOtherQueriesAreIgnored(t *testing.T) {
	t.Parallel()
	otherName := []byte("\x07example\x03org\x00")
	tests := map[string]func(response *testResponse){
		"another transaction id":    func(response *testResponse) { response.transactionID++ },
		"not a response":            func(response *testResponse) { response.flags &^= wire.DNSFlagResponse },
		"another opcode":            func(response *testResponse) { response.opcode = wire.DNSOpcodeStatus },
		"two questions":             func(response *testResponse) { response.questionCount = 2 },
		"another question type":     func(response *testResponse) { response.question = questionSection(exampleName, wire.DNSTypeAAAA) },
		"another question name":     func(response *testResponse) { response.question = questionSection(otherName, wire.DNSTypeA) },
		"a truncated question":      func(response *testResponse) { response.question, response.answers = exampleName[:5], nil },
		"a malformed question name": func(response *testResponse) { response.question = questionSection(forwardPointer, wire.DNSTypeA) },
	}
	for name, modify := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, handle, query := sentExampleQuery(t)
			response := responseTo(query, aRecord(questionPointer, exampleAddress))
			modify(&response)

			answer(t, s, query, response.encode())

			assertPending(t, s, handle)
		})
	}
}

func TestResponseToAnotherPortIsIgnored(t *testing.T) {
	t.Parallel()
	s, _, handle, query := sentExampleQuery(t)
	message := responseTo(query, aRecord(questionPointer, exampleAddress)).encode()
	ip, header := responseHeaders(query, message)
	header.DstPort++

	s.Process(ip, header, message)

	assertPending(t, s, handle)
}

func TestMalformedMessageIsIgnored(t *testing.T) {
	t.Parallel()
	s, _, handle, query := sentExampleQuery(t)
	message := responseTo(query, aRecord(questionPointer, exampleAddress)).encode()

	answer(t, s, query, message[:headerLen-1])

	assertPending(t, s, handle)
}

func TestMalformedAnswersLeaveTheQueryPending(t *testing.T) {
	t.Parallel()
	tests := map[string]func(query sentQuery) []byte{
		"a truncated record": func(query sentQuery) []byte {
			message := responseTo(query, aRecord(questionPointer, exampleAddress)).encode()
			return message[:len(message)-2]
		},
		"a record with a malformed name": func(query sentQuery) []byte {
			return responseTo(query, aRecord(forwardPointer, exampleAddress)).encode()
		},
		"a CNAME with a malformed target": func(query sentQuery) []byte {
			return responseTo(query, cnameRecord(questionPointer, forwardPointer)).encode()
		},
		"a CNAME with a target over 255 octets": func(query sentQuery) []byte {
			label := "\x3f" + strings.Repeat("a", maxLabelLen)
			target := []byte(strings.Repeat(label, 4) + "\x00")
			return responseTo(query, cnameRecord(questionPointer, target)).encode()
		},
	}
	for name, message := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, handle, query := sentExampleQuery(t)

			answer(t, s, query, message(query))

			assertPending(t, s, handle)
		})
	}
}

func TestAcceptsResponsesFromTheServersAndFromMulticastDNS(t *testing.T) {
	t.Parallel()
	s := NewSocket(testServers)
	stranger := netip.MustParseAddr("203.0.113.9")
	tests := []struct {
		name   string
		source netip.AddrPort
		want   bool
	}{
		{"port 53 of the first server", netip.AddrPortFrom(firstServer, dnsPort), true},
		{"port 53 of the second server", netip.AddrPortFrom(secondServer, dnsPort), true},
		{"another port of a server", netip.AddrPortFrom(firstServer, dnsPort+1), false},
		{"port 53 of another host", netip.AddrPortFrom(stranger, dnsPort), false},
		{"port 5353 of another host", netip.AddrPortFrom(stranger, mdnsPort), true},
	}
	for _, test := range tests {
		ip := wire.IPv4Repr{SrcAddr: test.source.Addr(), DstAddr: localAddr, NextHeader: wire.IPProtocolUDP}
		header := wire.UDPRepr{SrcPort: test.source.Port(), DstPort: firstQueryPort}

		if got := s.Accepts(ip, header); got != test.want {
			t.Errorf("Accepts a datagram from %s = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestMulticastDNSResponseSettlesTheQuery(t *testing.T) {
	t.Parallel()
	s, _, handle, query := sentExampleQuery(t)
	message := responseTo(query, aRecord(questionPointer, exampleAddress)).encode()
	ip, header := responseHeaders(query, message)
	ip.SrcAddr = netip.MustParseAddr("192.168.1.7")
	header.SrcPort = mdnsPort
	if !s.Accepts(ip, header) {
		t.Fatal("the socket does not accept a response from port 5353")
	}

	s.Process(ip, header, message)

	assertCompleted(t, s, handle, exampleAddress)
}

func TestQueryFailsWithoutAUsableServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		servers []netip.Addr
	}{
		{"no server", nil},
		{"an unspecified server", []netip.Addr{netip.IPv4Unspecified()}},
		{"an IPv6 server", []netip.Addr{netip.MustParseAddr("2001:db8::53")}},
		{"an IPv4-mapped server", []netip.Addr{netip.MustParseAddr("::ffff:192.0.2.53")}},
		{"the zero Addr", []netip.Addr{{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cx := testContext(t)
			s := NewSocket(test.servers)
			handle := mustStartQuery(t, s, cx, "example.com")

			sent := dispatchAt(t, s, cx, 0)

			if len(sent) != 0 {
				t.Errorf("Dispatch emitted %d queries, want none", len(sent))
			}
			assertFailed(t, s, handle)
		})
	}
}

func TestQueryFailsWithoutASourceAddress(t *testing.T) {
	t.Parallel()
	cx := socket.NewContext(time.Time{}, 0, testIPMTU)
	s := NewSocket(testServers)
	handle := mustStartQuery(t, s, cx, "example.com")

	sent := dispatchAt(t, s, cx, 0)

	if len(sent) != 0 {
		t.Errorf("Dispatch emitted %d queries, want none", len(sent))
	}
	assertFailed(t, s, handle)
}

func TestZeroSocketIsAResolverWithoutServers(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	var withoutServers, updated Socket
	failing := mustStartQuery(t, &withoutServers, cx, "example.com")
	updated.UpdateServers(testServers)
	handle := mustStartQuery(t, &updated, cx, "example.com")

	if sent := dispatchAt(t, &withoutServers, cx, 0); len(sent) != 0 {
		t.Errorf("a zero socket emitted %d queries, want none", len(sent))
	}
	assertFailed(t, &withoutServers, failing)
	query := mustDispatchOne(t, &updated, cx, 0)
	answer(t, &updated, query, responseTo(query, aRecord(questionPointer, exampleAddress)).encode())
	assertCompleted(t, &updated, handle, exampleAddress)
}

func TestNewSocketKeepsTheFirstTwoServers(t *testing.T) {
	t.Parallel()
	third := netip.MustParseAddr("203.0.113.53")
	servers := []netip.Addr{firstServer, secondServer, third}
	cx := testContext(t)
	s := NewSocket(servers)
	servers[0] = third
	handle := mustStartQuery(t, s, cx, "example.com")

	asked := []netip.Addr{mustDispatchOne(t, s, cx, 0).ip.DstAddr, mustDispatchOne(t, s, cx, 15*time.Second).ip.DstAddr}
	rest := dispatchAt(t, s, cx, 30*time.Second)

	if want := []netip.Addr{firstServer, secondServer}; !slices.Equal(asked, want) || len(rest) != 0 {
		t.Errorf("asked %v, then %d more; want %v only", asked, len(rest), want)
	}
	assertFailed(t, s, handle)
}

func TestUpdateServersKeepsTheFirstTwo(t *testing.T) {
	t.Parallel()
	third := netip.MustParseAddr("203.0.113.53")
	cx := testContext(t)
	s := NewSocket(nil)
	handle := mustStartQuery(t, s, cx, "example.com")

	s.UpdateServers([]netip.Addr{third, secondServer, firstServer})

	asked := []netip.Addr{mustDispatchOne(t, s, cx, 0).ip.DstAddr, mustDispatchOne(t, s, cx, 15*time.Second).ip.DstAddr}
	if want := []netip.Addr{third, secondServer}; !slices.Equal(asked, want) {
		t.Errorf("asked %v, want %v", asked, want)
	}
	if rest := dispatchAt(t, s, cx, 30*time.Second); len(rest) != 0 {
		t.Errorf("asked %d more servers, want none", len(rest))
	}
	assertFailed(t, s, handle)
}

func TestQueryInProgressContinuesWithUpdatedServers(t *testing.T) {
	t.Parallel()
	replacement := netip.MustParseAddr("203.0.113.53")
	s, cx, _, _ := sentExampleQuery(t)

	s.UpdateServers([]netip.Addr{replacement})

	if query := mustDispatchOne(t, s, cx, time.Second); query.ip.DstAddr != replacement {
		t.Errorf("the retransmission went to %v, want %v", query.ip.DstAddr, replacement)
	}
}

func TestTakeQueryResultEndsTheQuery(t *testing.T) {
	t.Parallel()
	s, cx, handle, query := sentExampleQuery(t)
	answer(t, s, query, responseTo(query, aRecord(questionPointer, exampleAddress)).encode())

	assertCompleted(t, s, handle, exampleAddress)

	assertPanics(t, "TakeQueryResult of a spent handle", takeQueryResultPanic, func() {
		_, _ = s.TakeQueryResult(handle)
	})
	if reused := mustStartQuery(t, s, cx, "example.org"); reused != handle {
		t.Errorf("the next query got handle %v, want the freed %v", reused, handle)
	}
}

func TestTakeQueryResultKeepsAPendingQuery(t *testing.T) {
	t.Parallel()
	s, _, handle, query := sentExampleQuery(t)

	assertPending(t, s, handle)
	answer(t, s, query, responseTo(query, aRecord(questionPointer, exampleAddress)).encode())

	assertCompleted(t, s, handle, exampleAddress)
}

func TestFailedQueryIsEndedByTakeQueryResult(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(nil)
	handle := mustStartQuery(t, s, cx, "example.com")
	dispatchAt(t, s, cx, 0)

	assertFailed(t, s, handle)

	assertPanics(t, "TakeQueryResult of a spent handle", takeQueryResultPanic, func() { _, _ = s.TakeQueryResult(handle) })
}

func TestCancelQueryFreesTheSlot(t *testing.T) {
	t.Parallel()
	s, cx, handle, _ := sentExampleQuery(t)

	s.CancelQuery(handle)

	if sent := dispatchAt(t, s, cx, time.Second); len(sent) != 0 {
		t.Errorf("a cancelled query was sent %d times", len(sent))
	}
	assertPanics(t, "CancelQuery of a spent handle", cancelQueryPanic, func() { s.CancelQuery(handle) })
	assertPanics(t, "TakeQueryResult of a cancelled handle", takeQueryResultPanic, func() { _, _ = s.TakeQueryResult(handle) })
	if reused := mustStartQuery(t, s, cx, "example.org"); reused != handle {
		t.Errorf("the next query got handle %v, want the freed %v", reused, handle)
	}
}

func TestZeroAndForeignHandlesIdentifyNoQuery(t *testing.T) {
	t.Parallel()
	s, _, _, _ := sentExampleQuery(t)
	for _, handle := range []QueryHandle{{}, {slot: 2}} {
		assertPanics(t, "TakeQueryResult", takeQueryResultPanic, func() { _, _ = s.TakeQueryResult(handle) })
		assertPanics(t, "CancelQuery", cancelQueryPanic, func() { s.CancelQuery(handle) })
	}
}

func TestFreeSlotsAreReusedLowestFirst(t *testing.T) {
	t.Parallel()
	cx := testContext(t)
	s := NewSocket(testServers)
	handles := []QueryHandle{
		mustStartQuery(t, s, cx, "a.example"),
		mustStartQuery(t, s, cx, "b.example"),
		mustStartQuery(t, s, cx, "c.example"),
	}

	s.CancelQuery(handles[1])
	s.CancelQuery(handles[0])

	if reused := mustStartQuery(t, s, cx, "d.example"); reused != handles[0] {
		t.Errorf("the next query got handle %v, want the lowest free %v", reused, handles[0])
	}
	if reused := mustStartQuery(t, s, cx, "e.example"); reused != handles[1] {
		t.Errorf("the query after got handle %v, want %v", reused, handles[1])
	}
	if added := mustStartQuery(t, s, cx, "f.example"); slices.Contains(handles, added) {
		t.Errorf("with every slot taken, the next query got the used handle %v", added)
	}
}
