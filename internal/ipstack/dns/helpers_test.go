package dns

// This file holds what the DNS socket's tests share: a test context, a
// recording EmitFunc, and a builder for the responses a server sends.
// smoltcp has no tests of its DNS socket, so every test here is the port's
// own.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

const (
	// testIPMTU is the IP MTU of the test context, smoltcp's TestingDevice's.
	testIPMTU = 1500
	// firstQueryID and firstQueryPort are the transaction id and source
	// port of the first query on a context seeded with 0: the first Uint16
	// and the first SourcePort after it of smoltcp's sPCG32.
	firstQueryID   uint16 = 0x3
	firstQueryPort uint16 = 1358
	// dnsOpcodeShift is where the opcode sits in a DNS header's flags field
	// (RFC 1035 § 4.1.1); the response code takes its four lowest bits.
	dnsOpcodeShift = 11
	// classIN is the Internet class of every record the tests build.
	classIN = 1
	// testTTL is the time-to-live of every record the tests build.
	testTTL = 300
	// headerLen is the length of a DNS header, where the question starts.
	headerLen = 12
	// takeQueryResultPanic and cancelQueryPanic are what TakeQueryResult and
	// CancelQuery panic with for a handle that identifies no query.
	takeQueryResultPanic = "Getting the result of a query in a free slot."
	cancelQueryPanic     = "Canceling query in a free slot."
)

var (
	localAddr    = netip.AddrFrom4([4]byte{192, 168, 1, 1})
	firstServer  = netip.MustParseAddr("192.0.2.53")
	secondServer = netip.MustParseAddr("198.51.100.53")
	testServers  = []netip.Addr{firstServer, secondServer}

	// exampleName is "example.com" in its wire form.
	exampleName = []byte("\x07example\x03com\x00")
	// targetName is "www.example.net" in its wire form, the target of the
	// tests' CNAME records.
	targetName = []byte("\x03www\x07example\x03net\x00")
	// questionPointer is a compression pointer to the name of a message's
	// question, which follows the header.
	questionPointer = []byte{0xc0, headerLen}
	// forwardPointer points past the end of every test message.
	forwardPointer = []byte{0xc0, 0xff}

	exampleAddress = netip.MustParseAddr("203.0.113.80")
	targetAddress  = netip.MustParseAddr("203.0.113.81")

	errRefused = errors.New("refused")
)

// testContext returns a context at the epoch with seed 0, the test IP MTU and
// the address 192.168.1.1/24.
func testContext(t *testing.T) *socket.Context {
	t.Helper()
	cx := socket.NewContext(time.Time{}, 0, testIPMTU)
	if err := cx.SetIPAddrs([]netip.Prefix{netip.PrefixFrom(localAddr, 24)}); err != nil {
		t.Fatalf("SetIPAddrs: %v", err)
	}
	return cx
}

// at returns the time elapsed after the tests' epoch, the zero time.Time
// (smoltcp's Instant::ZERO).
func at(elapsed time.Duration) time.Time {
	return time.Time{}.Add(elapsed)
}

// sentQuery is a query datagram the socket emitted.
type sentQuery struct {
	ip      wire.IPv4Repr
	header  wire.UDPRepr
	message []byte
}

// recordingEmit returns an EmitFunc that records every datagram, copying
// its payload, and returns err.
func recordingEmit(sent *[]sentQuery, err error) EmitFunc {
	return func(ip wire.IPv4Repr, header wire.UDPRepr, payload []byte) error {
		*sent = append(*sent, sentQuery{ip: ip, header: header, message: bytes.Clone(payload)})
		return err
	}
}

// dispatchAt runs Dispatch at the time elapsed after the epoch, with emit
// granted, and returns what it emitted.
func dispatchAt(t *testing.T, s *Socket, cx *socket.Context, elapsed time.Duration) []sentQuery {
	t.Helper()
	cx.SetNow(at(elapsed))
	var sent []sentQuery
	if err := s.Dispatch(cx, recordingEmit(&sent, nil)); err != nil {
		t.Fatalf("Dispatch at %v: %v", elapsed, err)
	}
	return sent
}

// destinationOf returns the server the one query in sent went to, or the
// zero Addr when sent is empty; it fails the test if more than one was sent.
func destinationOf(t *testing.T, sent []sentQuery) netip.Addr {
	t.Helper()
	switch len(sent) {
	case 0:
		return netip.Addr{}
	case 1:
		return sent[0].ip.DstAddr
	default:
		t.Fatalf("sent %d queries at once, want at most one", len(sent))
		return netip.Addr{}
	}
}

// mustDispatchOne runs Dispatch at the time elapsed after the epoch and
// returns the query it must have emitted.
func mustDispatchOne(t *testing.T, s *Socket, cx *socket.Context, elapsed time.Duration) sentQuery {
	t.Helper()
	sent := dispatchAt(t, s, cx, elapsed)
	if len(sent) != 1 {
		t.Fatalf("Dispatch at %v emitted %d queries, want 1", elapsed, len(sent))
	}
	return sent[0]
}

// mustStartQuery starts looking up the A records of name.
func mustStartQuery(t *testing.T, s *Socket, cx *socket.Context, name string) QueryHandle {
	t.Helper()
	handle, err := s.StartQuery(cx, name, wire.DNSTypeA)
	if err != nil {
		t.Fatalf("StartQuery(%q): %v", name, err)
	}
	return handle
}

// sentExampleQuery starts looking up example.com on a socket with the test
// servers, sends the query at the epoch and returns it.
func sentExampleQuery(t *testing.T) (*Socket, *socket.Context, QueryHandle, sentQuery) {
	t.Helper()
	cx := testContext(t)
	s := NewSocket(testServers)
	handle := mustStartQuery(t, s, cx, "example.com")
	return s, cx, handle, mustDispatchOne(t, s, cx, 0)
}

// testRecord is an answer record of a test response, of class IN.
type testRecord struct {
	// name is the owner name as on the wire, possibly a pointer.
	name       []byte
	recordType wire.DNSType
	data       []byte
}

func aRecord(name []byte, address netip.Addr) testRecord {
	octets := address.As4()
	return testRecord{name: name, recordType: wire.DNSTypeA, data: octets[:]}
}

func cnameRecord(name, target []byte) testRecord {
	return testRecord{name: name, recordType: wire.DNSTypeCNAME, data: target}
}

func (r testRecord) appendTo(message []byte) []byte {
	message = append(message, r.name...)
	message = binary.BigEndian.AppendUint16(message, uint16(r.recordType))
	message = binary.BigEndian.AppendUint16(message, classIN)
	message = binary.BigEndian.AppendUint32(message, testTTL)
	message = binary.BigEndian.AppendUint16(message, uint16(len(r.data)))
	return append(message, r.data...)
}

// testResponse is a DNS message a test server sends.
type testResponse struct {
	transactionID uint16
	flags         wire.DNSFlags
	opcode        wire.DNSOpcode
	rcode         wire.DNSRcode
	questionCount uint16
	// question is the question section as on the wire.
	question []byte
	answers  []testRecord
}

// responseTo returns the successful response to query with answers: its
// transaction id and question, with the response, recursion desired and
// recursion available flags.
func responseTo(query sentQuery, answers ...testRecord) testResponse {
	return testResponse{
		transactionID: wire.DNSPacket(query.message).TransactionID(),
		flags:         wire.DNSFlagResponse | wire.DNSFlagRecursionDesired | wire.DNSFlagRecursionAvailable,
		opcode:        wire.DNSOpcodeQuery,
		rcode:         wire.DNSRcodeNoError,
		questionCount: 1,
		question:      query.message[headerLen:],
		answers:       answers,
	}
}

// encode returns the response's message: the header, the question section
// and the answer records, without authority or additional records.
func (r testResponse) encode() []byte {
	var message []byte
	message = binary.BigEndian.AppendUint16(message, r.transactionID)
	message = binary.BigEndian.AppendUint16(message, uint16(r.flags)|uint16(r.opcode)<<dnsOpcodeShift|uint16(r.rcode))
	message = binary.BigEndian.AppendUint16(message, r.questionCount)
	message = binary.BigEndian.AppendUint16(message, uint16(len(r.answers)))
	message = binary.BigEndian.AppendUint16(message, 0)
	message = binary.BigEndian.AppendUint16(message, 0)
	message = append(message, r.question...)
	for _, record := range r.answers {
		message = record.appendTo(message)
	}
	return message
}

// questionSection returns the wire form of a question for name, which is in
// wire form, and questionType.
func questionSection(name []byte, questionType wire.DNSType) []byte {
	question := wire.DNSQuestion{Name: name, Type: questionType}
	section := make([]byte, question.BufferLen())
	question.Emit(section)
	return section
}

// answer hands s message from the server query went to, from port 53 to the
// query's port, as the interface does once Accepts has taken it.
func answer(t *testing.T, s *Socket, query sentQuery, message []byte) {
	t.Helper()
	ip, header := responseHeaders(query, message)
	if !s.Accepts(ip, header) {
		t.Fatalf("the socket does not accept a response from %v", ip.SrcAddr)
	}
	s.Process(ip, header, message)
}

// responseHeaders returns the IP and UDP headers of a response with message
// to query.
func responseHeaders(query sentQuery, message []byte) (wire.IPv4Repr, wire.UDPRepr) {
	ip := wire.IPv4Repr{
		SrcAddr:    query.ip.DstAddr,
		DstAddr:    query.ip.SrcAddr,
		NextHeader: wire.IPProtocolUDP,
		PayloadLen: wire.UDPHeaderLen + len(message),
		HopLimit:   wire.DefaultHopLimit,
	}
	return ip, wire.UDPRepr{SrcPort: query.header.DstPort, DstPort: query.header.SrcPort}
}

// assertCompleted checks that TakeQueryResult returns the addresses want for
// handle.
func assertCompleted(t *testing.T, s *Socket, handle QueryHandle, want ...netip.Addr) {
	t.Helper()
	addresses, err := s.TakeQueryResult(handle)
	if err != nil || !slices.Equal(addresses, want) {
		t.Errorf("TakeQueryResult = %v, %v; want %v", addresses, err, want)
	}
}

// assertPending checks that the query handle identifies has no answer yet.
func assertPending(t *testing.T, s *Socket, handle QueryHandle) {
	t.Helper()
	assertResultError(t, s, handle, ErrPending)
}

// assertFailed checks that the query handle identifies failed.
func assertFailed(t *testing.T, s *Socket, handle QueryHandle) {
	t.Helper()
	assertResultError(t, s, handle, ErrFailed)
}

func assertResultError(t *testing.T, s *Socket, handle QueryHandle, want error) {
	t.Helper()
	if addresses, err := s.TakeQueryResult(handle); !errors.Is(err, want) || addresses != nil {
		t.Errorf("TakeQueryResult = %v, %v; want %v", addresses, err, want)
	}
}

// assertPanics checks that operation panics with message.
func assertPanics(t *testing.T, what, message string, operation func()) {
	t.Helper()
	defer func() {
		t.Helper()
		if recovered := recover(); recovered != message {
			t.Errorf("%s panicked with %v, want %q", what, recovered, message)
		}
	}()
	operation()
}
