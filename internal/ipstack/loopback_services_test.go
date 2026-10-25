package ipstack_test

// This file holds the applications of the loopback tests, which run on the
// hosts between polls as satnet's connections and the simulator's services
// do: a TCP bulk exchange, the simulator's UDP echo, and a DNS server built
// by hand on a UDP socket, with what the client needs to use them.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The sockets' sizes and settings, as satnet and the simulator configure
// theirs.
const (
	// clientReceiveBuffer and clientSendBuffer are satnet's default TCP
	// buffers.
	clientReceiveBuffer = 2048
	clientSendBuffer    = 4096
	// serviceBufferLen is the size of each TCP buffer of the simulator's
	// services: several seconds of Iridium traffic.
	serviceBufferLen = 4096
	// clientACKDelay is how long satnet's bare acknowledgements wait,
	// hoping to ride on a reply.
	clientACKDelay = 100 * time.Millisecond
	// clientTimeout and clientKeepAlive are satnet's default TCP timeout
	// and keep-alive interval.
	clientTimeout   = 120 * time.Second
	clientKeepAlive = 60 * time.Second
	// datagramSlots is how many datagrams a UDP socket holds each way, as
	// satnet's do, each up to a datagram of the line.
	datagramSlots = 4
	// receiveChunkLen is how much an application reads at a time.
	receiveChunkLen = 1024
)

// The ports of the loopback network.
const (
	// clientPort is the client's local port: the first of the ephemeral
	// ports (RFC 6335) satnet hands out.
	clientPort = 49152
	// echoPort is the simulator's UDP echo service.
	echoPort = 7
	// unservedPort is a port the internet has no socket on, for TCP or UDP.
	unservedPort = 9
	// bulkPort is the internet's bulk TCP service.
	bulkPort = 5001
	// dnsPort is where DNS servers answer (RFC 1035 § 4.2.1).
	dnsPort = 53
)

// The bulk exchange's data: several windows' worth each way.
const (
	clientBulkLen   = 20_000
	internetBulkLen = 30_000
)

// connectClient returns a TCP socket of the client, tuned as satnet tunes
// its sockets, connecting from clientPort to remote; its SYN leaves at the
// next poll.
func connectClient(t *testing.T, n *network, remote netip.AddrPort) *tcp.Socket {
	t.Helper()
	s := tcp.NewSocket(tcp.NewSocketBuffer(clientReceiveBuffer), tcp.NewSocketBuffer(clientSendBuffer))
	s.SetTimeout(clientTimeout)
	s.SetKeepAlive(clientKeepAlive)
	// Every write should leave at once; satnet's pacing already merges
	// small ones.
	s.SetNagleEnabled(false)
	s.SetAckDelay(clientACKDelay)
	if err := s.Connect(n.client.iface.Context(), remote, wire.ListenEndpoint{Port: clientPort}); err != nil {
		t.Fatalf("Connect(%v): %v", remote, err)
	}
	n.client.sockets.AddTCP(s)
	return s
}

// listenOnInternet returns a TCP socket of the internet listening on port of
// every address, sized as the simulator's services are and with the stack's
// default settings.
func listenOnInternet(t *testing.T, n *network, port uint16) *tcp.Socket {
	t.Helper()
	s := tcp.NewSocket(tcp.NewSocketBuffer(serviceBufferLen), tcp.NewSocketBuffer(serviceBufferLen))
	if err := s.Listen(wire.ListenEndpoint{Port: port}); err != nil {
		t.Fatalf("Listen(%d): %v", port, err)
	}
	n.internet.sockets.AddTCP(s)
	return s
}

// closePolicy says when a streamPeer closes its half of the connection.
type closePolicy int

const (
	// closeWhenSent closes as soon as all the data is queued, as the client
	// does.
	closeWhenSent closePolicy = iota
	// closeAfterThePeer also waits for the other end to finish, as the
	// simulator's services do.
	closeAfterThePeer
)

// streamPeer is the application at one end of a TCP connection: it sends its
// data, closes its half as its policy says, and reads what the other end
// sends until that end closes.
type streamPeer struct {
	t      *testing.T
	socket *tcp.Socket
	// data is what the peer sends, and unsent the part not queued yet.
	data, unsent []byte
	policy       closePolicy
	// received is what the peer has read.
	received []byte
	// hasClosed says the peer closed its half; hasFinished that it read the
	// other end's FIN, after everything before it.
	hasClosed, hasFinished bool
}

// newStreamPeer returns a peer that sends data over socket and closes as
// policy says.
func newStreamPeer(t *testing.T, socket *tcp.Socket, data []byte, policy closePolicy) *streamPeer {
	return &streamPeer{t: t, socket: socket, data: data, unsent: data, policy: policy}
}

// serve does what the peer can do now: queue data, read, and close.
func (p *streamPeer) serve() {
	p.queueWhatFits()
	p.readWhatArrived()
	p.closeWhenDue()
}

// queueWhatFits queues as much of the unsent data as the socket takes.
func (p *streamPeer) queueWhatFits() {
	if len(p.unsent) == 0 || !p.socket.MaySend() {
		return
	}
	queued, err := p.socket.SendSlice(p.unsent)
	if err != nil {
		p.t.Fatalf("SendSlice in state %v: %v", p.socket.State(), err)
	}
	p.unsent = p.unsent[queued:]
}

// readWhatArrived reads everything the socket has received.
func (p *streamPeer) readWhatArrived() {
	chunk := make([]byte, receiveChunkLen)
	for {
		read, err := p.socket.ReceiveSlice(chunk)
		switch {
		case errors.Is(err, tcp.ErrFinished):
			p.hasFinished = true
			return
		case errors.Is(err, tcp.ErrInvalidState):
			// Not connected yet, or no longer: there is nothing to read.
			return
		case err != nil:
			p.t.Fatalf("ReceiveSlice: %v", err)
		case read == 0:
			return
		}
		p.received = append(p.received, chunk[:read]...)
	}
}

// closeWhenDue closes the peer's half once its data is queued and its
// policy allows.
func (p *streamPeer) closeWhenDue() {
	if p.hasClosed || len(p.unsent) > 0 || (p.policy == closeAfterThePeer && !p.hasFinished) {
		return
	}
	p.socket.Close()
	p.hasClosed = true
}

// bulkExchange is a TCP connection from the client to the internet's bulk
// service at the echo host, over which each end sends data of its own and
// reads the other's. The client closes first, and the service once the
// client has finished.
type bulkExchange struct {
	client, service *streamPeer
}

// startBulkExchange starts a bulk exchange on n: the service listens, and the
// client's SYN leaves at the next poll.
func startBulkExchange(t *testing.T, n *network) *bulkExchange {
	t.Helper()
	listener := listenOnInternet(t, n, bulkPort)
	client := connectClient(t, n, netip.AddrPortFrom(echoHostAddr, bulkPort))
	return &bulkExchange{
		client:  newStreamPeer(t, client, testData(clientBulkLen, 1), closeWhenSent),
		service: newStreamPeer(t, listener, testData(internetBulkLen, 2), closeAfterThePeer),
	}
}

// serve lets both ends do what they can.
func (e *bulkExchange) serve() {
	e.client.serve()
	e.service.serve()
}

// isOver reports whether both ends have read everything and both sockets
// are closed.
func (e *bulkExchange) isOver() bool {
	return e.client.hasFinished && e.service.hasFinished &&
		e.client.socket.State() == tcp.StateClosed && e.service.socket.State() == tcp.StateClosed
}

// assertDelivered checks that each end read exactly what the other sent.
func (e *bulkExchange) assertDelivered(t *testing.T) {
	t.Helper()
	assertSameOctets(t, "the service read", e.service.received, e.client.data)
	assertSameOctets(t, "the client read", e.client.received, e.service.data)
}

// assertSameOctets checks that got, what one end read, is want, what the
// other end sent.
func assertSameOctets(t *testing.T, what string, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Errorf("%s %d octets, which differ from the %d sent from octet %d on", what, len(got), len(want), firstDifference(got, want))
	}
}

// firstDifference returns the offset of the first octet where a and b
// differ, or the length of the shorter one when it is the other's prefix.
func firstDifference(a, b []byte) int {
	shorter := min(len(a), len(b))
	for i := range shorter {
		if a[i] != b[i] {
			return i
		}
	}
	return shorter
}

// testData returns length random octets made from seed, the same on every
// run, which a shifted or reordered copy would not match.
func testData(length int, seed uint64) []byte {
	random := rand.New(rand.NewPCG(seed, seed))
	data := make([]byte, length)
	for i := range data {
		data[i] = byte(random.Uint32())
	}
	return data
}

// newDatagramSocket returns a UDP socket bound to local, with room for
// datagramSlots datagrams of the line each way.
func newDatagramSocket(t *testing.T, local wire.ListenEndpoint) *udp.Socket {
	t.Helper()
	newBuffer := func() *udp.PacketBuffer { return udp.NewPacketBuffer(datagramSlots, datagramSlots*lineMTU) }
	s := udp.NewSocket(newBuffer(), newBuffer())
	if err := s.Bind(local); err != nil {
		t.Fatalf("Bind(%v): %v", local, err)
	}
	return s
}

// sendDatagram queues payload on s for destination.
func sendDatagram(t *testing.T, s *udp.Socket, payload string, destination netip.AddrPort) {
	t.Helper()
	if err := s.SendSlice([]byte(payload), udp.Metadata{Endpoint: destination}); err != nil {
		t.Fatalf("SendSlice to %v: %v", destination, err)
	}
}

// receiveEach hands every datagram waiting on s to handle, oldest first.
func receiveEach(t *testing.T, s *udp.Socket, handle func(payload []byte, meta udp.Metadata)) {
	t.Helper()
	for {
		payload, meta, err := s.Receive()
		if errors.Is(err, udp.ErrExhausted) {
			return
		}
		if err != nil {
			t.Fatalf("Receive: %v", err)
		}
		handle(payload, meta)
	}
}

// receivedDatagram is a datagram an application read.
type receivedDatagram struct {
	payload string
	meta    udp.Metadata
}

// udpEcho is the simulator's UDP echo service: it sends every datagram back
// with the metadata it arrived with, so the answer comes from the address
// the client wrote to.
type udpEcho struct {
	t      *testing.T
	socket *udp.Socket
	// received is what the service read, oldest first.
	received []receivedDatagram
}

// serve echoes every datagram that has arrived.
func (e *udpEcho) serve() {
	receiveEach(e.t, e.socket, func(payload []byte, meta udp.Metadata) {
		e.received = append(e.received, receivedDatagram{payload: string(payload), meta: meta})
		if err := e.socket.SendSlice(bytes.Clone(payload), meta); err != nil {
			e.t.Fatalf("echoing to %v: %v", meta, err)
		}
	})
}

// datagramReader is an application that reads every datagram its socket
// receives.
type datagramReader struct {
	t      *testing.T
	socket *udp.Socket
	// received is what the application read, oldest first.
	received []receivedDatagram
}

// serve reads every datagram that has arrived.
func (r *datagramReader) serve() {
	receiveEach(r.t, r.socket, func(payload []byte, meta udp.Metadata) {
		r.received = append(r.received, receivedDatagram{payload: string(payload), meta: meta})
	})
}

// The parts of a DNS message the hand-built responder writes itself
// (RFC 1035 § 4.1).
const (
	// dnsHeaderLen is the length of a message's header.
	dnsHeaderLen = 12
	// dnsRcodeOffset is the octet of the header whose low four bits hold the
	// response code.
	dnsRcodeOffset = 3
	// dnsClassIN is the class of every record: the Internet.
	dnsClassIN = 1
	// dnsRecordTTL is how long the responder's records may be cached, in
	// seconds: five minutes.
	dnsRecordTTL = 300
)

// questionNamePointer is a compressed name (RFC 1035 § 4.1.4) that points to
// the name of a response's question, which starts right after the header.
var questionNamePointer = []byte{0xc0, dnsHeaderLen}

// zoneEntry is what the responder knows of a name: its addresses, or the
// name it is an alias of.
type zoneEntry struct {
	addresses []netip.Addr
	aliasOf   string
}

// testZone is the responder's zone: a host with one address, one with two,
// and an alias.
var testZone = map[string]zoneEntry{
	"echo.test": {addresses: []netip.Addr{echoHostAddr}},
	"web.test":  {addresses: []netip.Addr{webHostAddr, netip.AddrFrom4([4]byte{192, 0, 2, 81})}},
	"www.test":  {aliasOf: "web.test"},
}

// askedQuery is a query the responder received.
type askedQuery struct {
	from             netip.AddrPort
	to               netip.Addr
	name             string
	recursionDesired bool
}

// dnsResponder is a DNS server built by hand on a UDP socket of the internet:
// it answers queries for names of its zone with their A records, after a
// CNAME record for each alias on the way, and other queries with NXDOMAIN.
type dnsResponder struct {
	t      *testing.T
	socket *udp.Socket
	zone   map[string]zoneEntry
	// asked is what the responder was asked, oldest first.
	asked []askedQuery
}

// newDNSResponder returns a responder for testZone on port 53 of every
// address of n's internet.
func newDNSResponder(t *testing.T, n *network) *dnsResponder {
	t.Helper()
	socket := newDatagramSocket(t, wire.ListenEndpoint{Port: dnsPort})
	n.internet.sockets.AddUDP(socket)
	return &dnsResponder{t: t, socket: socket, zone: testZone}
}

// serve answers every query that has arrived.
func (r *dnsResponder) serve() {
	receiveEach(r.t, r.socket, func(query []byte, meta udp.Metadata) {
		if err := r.socket.SendSlice(r.answer(query, meta), meta); err != nil {
			r.t.Fatalf("answering %v: %v", meta, err)
		}
	})
}

// answer records query, which came with meta, and returns the response to
// it.
func (r *dnsResponder) answer(query []byte, meta udp.Metadata) []byte {
	packet, err := wire.NewDNSPacket(query)
	if err != nil {
		r.t.Fatalf("the responder got a malformed query: %v", err)
	}
	question, _, err := wire.ParseDNSQuestion(packet.Payload())
	if err != nil {
		r.t.Fatalf("the responder got a query without a question: %v", err)
	}
	name := dottedName(r.t, packet, question.Name)
	r.asked = append(r.asked, askedQuery{
		from:             meta.Endpoint,
		to:               meta.LocalAddress,
		name:             name,
		recursionDesired: packet.Flags()&wire.DNSFlagRecursionDesired != 0,
	})
	response := newDNSResponse(packet, question)
	r.appendAnswers(response, questionNamePointer, name)
	return response.message
}

// appendAnswers appends to response the records that answer for name, which
// owner holds in wire form: its A records, or a CNAME record and then the
// answers for the name it is an alias of. An unknown name makes the
// response NXDOMAIN.
func (r *dnsResponder) appendAnswers(response *dnsResponse, owner []byte, name string) {
	entry, ok := r.zone[name]
	switch {
	case !ok:
		response.setRcode(wire.DNSRcodeNXDomain)
	case entry.aliasOf != "":
		target := wireName(entry.aliasOf)
		response.appendRecord(owner, wire.DNSTypeCNAME, target)
		r.appendAnswers(response, target, entry.aliasOf)
	default:
		for _, address := range entry.addresses {
			response.appendRecord(owner, wire.DNSTypeA, address.AsSlice())
		}
	}
}

// dnsResponse is a DNS response under construction: the header and question
// of the query it answers, then answer records.
type dnsResponse struct {
	message []byte
	answers uint16
}

// newDNSResponse returns a response to query, which asks question, with no
// answers yet: a standard query's response, recursion available.
func newDNSResponse(query wire.DNSPacket, question wire.DNSQuestion) *dnsResponse {
	message := make([]byte, dnsHeaderLen+question.BufferLen())
	packet := wire.DNSPacket(message)
	packet.SetTransactionID(query.TransactionID())
	packet.SetOpcode(wire.DNSOpcodeQuery)
	packet.SetFlags(wire.DNSFlagResponse | query.Flags()&wire.DNSFlagRecursionDesired | wire.DNSFlagRecursionAvailable)
	packet.SetQuestionCount(1)
	question.Emit(packet.Payload())
	return &dnsResponse{message: message}
}

// setRcode sets the response code, which the header's flags leave out.
func (r *dnsResponse) setRcode(rcode wire.DNSRcode) {
	r.message[dnsRcodeOffset] |= byte(rcode)
}

// appendRecord appends an answer record for owner, a name in wire form, of
// recordType with data.
func (r *dnsResponse) appendRecord(owner []byte, recordType wire.DNSType, data []byte) {
	r.message = append(r.message, owner...)
	r.message = binary.BigEndian.AppendUint16(r.message, uint16(recordType))
	r.message = binary.BigEndian.AppendUint16(r.message, dnsClassIN)
	r.message = binary.BigEndian.AppendUint32(r.message, dnsRecordTTL)
	r.message = binary.BigEndian.AppendUint16(r.message, uint16(len(data)))
	r.message = append(r.message, data...)
	r.answers++
	wire.DNSPacket(r.message).SetAnswerRecordCount(r.answers)
}

// dottedName returns name, in wire form in packet, as people write it:
// "echo.test".
func dottedName(t *testing.T, packet wire.DNSPacket, name []byte) string {
	t.Helper()
	var labels []string
	for label, err := range packet.NameLabels(name) {
		if err != nil {
			t.Fatalf("a malformed name in a query: %v", err)
		}
		labels = append(labels, string(label))
	}
	return strings.Join(labels, ".")
}

// wireName returns the wire form of name, written as people write it: each
// label after its length, then the root label.
func wireName(name string) []byte {
	var encoded []byte
	for label := range strings.SplitSeq(name, ".") {
		encoded = append(encoded, byte(len(label)))
		encoded = append(encoded, label...)
	}
	return append(encoded, 0)
}

// pendingLookup is a DNS query of the client whose result the test awaits.
type pendingLookup struct {
	resolver *dns.Socket
	query    dns.QueryHandle
	// addresses and err are the query's result, once settled.
	addresses []netip.Addr
	err       error
	settled   bool
}

// startLookup starts looking up the A records of name on a resolver of n's
// client that asks the gateway; the query leaves at the next poll.
func startLookup(t *testing.T, n *network, name string) *pendingLookup {
	t.Helper()
	resolver := dns.NewSocket([]netip.Addr{gatewayAddr})
	query, err := resolver.StartQuery(n.client.iface.Context(), name, wire.DNSTypeA)
	if err != nil {
		t.Fatalf("StartQuery(%q): %v", name, err)
	}
	n.client.sockets.AddDNS(resolver)
	return &pendingLookup{resolver: resolver, query: query}
}

// collect takes the query's result once it has one, which ends the query.
func (l *pendingLookup) collect() {
	if l.settled {
		return
	}
	l.addresses, l.err = l.resolver.TakeQueryResult(l.query)
	l.settled = !errors.Is(l.err, dns.ErrPending)
}

// isSettled reports whether collect has taken the query's result.
func (l *pendingLookup) isSettled() bool {
	return l.settled
}
