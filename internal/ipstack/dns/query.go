package dns

import (
	"net/netip"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The timing of queries (smoltcp's dns.rs:19-21).
const (
	// retransmitDelay is how long a server has to answer a query's first
	// attempt before the query is sent to it again.
	retransmitDelay = 1 * time.Second
	// retransmitBackoff multiplies the delay after every attempt.
	retransmitBackoff = 2
	// maxRetransmitDelay caps the delay, which a server never reaches: the
	// query moves on before.
	maxRetransmitDelay = 10 * time.Second
	// retransmitTimeout is how long a server has to answer, from the
	// query's first attempt to it, before the query moves on to the next
	// one. smoltcp: "Should generally be 2-10 secs".
	retransmitTimeout = 10 * time.Second
)

// queryState is where a query stands.
type queryState int

const (
	// queryPending waits for an answer, and sends the query when due.
	queryPending queryState = iota
	// queryCompleted has the addresses of an answer.
	queryCompleted
	// queryFailed got no usable answer.
	queryFailed
)

// query is one lookup: smoltcp's DnsQuery and its State.
type query struct {
	state queryState
	// pending is the lookup in progress, while the state is queryPending.
	pending pendingQuery
	// addresses are the answer's addresses, once the state is
	// queryCompleted.
	addresses []netip.Addr
}

// pendingQuery is a lookup waiting for its answer.
type pendingQuery struct {
	// name is the name to look up, in its wire form. A CNAME record in a
	// response replaces it, so that the records for the alias's target
	// follow.
	name      []byte
	queryType wire.DNSType
	// port is the query's UDP port: the source of the requests and the
	// destination of the responses.
	port uint16
	// transactionID matches a response to the query.
	transactionID uint16
	// timeoutAt is when the current server's time to answer is over; absent
	// until the query is first dispatched.
	timeoutAt wire.Optional[time.Time]
	// retransmitAt is when the query is next sent to the current server;
	// the epoch, a time already past, sends it at once.
	retransmitAt time.Time
	// delay is how long the server has to answer the next attempt.
	delay time.Duration
	// serverIndex is the index of the server being asked.
	serverIndex int
}

// newQuery returns a pending query for name, in its wire form, and
// queryType. It draws the transaction id and then the source port from the
// interface's random source, as smoltcp does: the order of the draws decides
// both values.
func newQuery(cx *socket.Context, name []byte, queryType wire.DNSType) *query {
	transactionID := cx.Rand().Uint16()
	port := cx.Rand().SourcePort()
	return &query{
		state: queryPending,
		pending: pendingQuery{
			name:          name,
			queryType:     queryType,
			port:          port,
			transactionID: transactionID,
			delay:         retransmitDelay,
		},
	}
}

// isPendingFor reports whether q, which may be a free slot's nil, is a
// pending query that a response to port with transactionID answers.
func (q *query) isPendingFor(port, transactionID uint16) bool {
	return q != nil && q.state == queryPending &&
		q.pending.port == port && q.pending.transactionID == transactionID
}

// isPending reports whether q, which may be a free slot's nil, is a pending
// query.
func (q *query) isPending() bool {
	return q != nil && q.state == queryPending
}

// fail ends the query without an answer.
func (q *query) fail() {
	*q = query{state: queryFailed}
}

// complete ends the query with the addresses of its answer.
func (q *query) complete(addresses []netip.Addr) {
	*q = query{state: queryCompleted, addresses: addresses}
}

// moveOnAfterTimeout starts the current server's time to answer at the
// query's first attempt, and moves the query on to the next server, to be
// sent at once, after that time; the caller checks that there is a next
// server.
func (pending *pendingQuery) moveOnAfterTimeout(now time.Time) {
	timeout, started := pending.timeoutAt.Get()
	if !started {
		timeout = now.Add(retransmitTimeout)
		pending.timeoutAt = wire.Some(timeout)
	}
	if !timeout.Before(now) {
		return
	}
	// DNS timeout: try the next server.
	pending.timeoutAt = wire.Some(now.Add(retransmitTimeout))
	pending.retransmitAt = time.Time{}
	pending.delay = retransmitDelay
	pending.serverIndex++
}

// isWaitingToRetransmit reports whether the query is not due yet at now.
func (pending *pendingQuery) isWaitingToRetransmit(now time.Time) bool {
	return pending.retransmitAt.After(now)
}

// scheduleRetransmission times the next attempt after one sent at now,
// backing off.
func (pending *pendingQuery) scheduleRetransmission(now time.Time) {
	pending.retransmitAt = now.Add(pending.delay)
	pending.delay = min(maxRetransmitDelay, pending.delay*retransmitBackoff)
}

// request returns the datagram that asks server the query, from source: its
// IP and UDP headers and the DNS message, a standard query with recursion
// desired.
func (pending *pendingQuery) request(source, server netip.Addr) (wire.IPv4Repr, wire.UDPRepr, []byte) {
	message := wire.DNSRepr{
		TransactionID: pending.transactionID,
		Flags:         wire.DNSFlagRecursionDesired,
		Opcode:        wire.DNSOpcodeQuery,
		Question:      wire.DNSQuestion{Name: pending.name, Type: pending.queryType},
	}
	payload := make([]byte, message.BufferLen())
	message.Emit(wire.DNSPacket(payload))
	header := wire.UDPRepr{SrcPort: pending.port, DstPort: dnsPort}
	ip := wire.IPv4Repr{
		SrcAddr:    source,
		DstAddr:    server,
		NextHeader: wire.IPProtocolUDP,
		PayloadLen: header.HeaderLen() + len(payload),
		HopLimit:   wire.DefaultHopLimit,
	}
	return ip, header, payload
}
