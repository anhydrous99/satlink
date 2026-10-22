package dns

import (
	"net/netip"
	"slices"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The methods in this file are the interface's side of the socket: it hands
// in received responses and asks for queries to send. Applications do not
// call them.

const (
	// dnsPort is the port DNS servers answer on (RFC 1035 § 4.2.1).
	dnsPort = 53
	// mdnsPort is the port of multicast DNS (RFC 6762), whose responses the
	// socket accepts from anyone.
	mdnsPort = 5353
)

// EmitFunc sends one query datagram for Dispatch. ip.PayloadLen covers the
// UDP header and payload, and payload is only valid until EmitFunc returns.
//
// An error means the query was not sent, typically because the device has no
// room for it, and Dispatch returns it unchanged. The query keeps its
// retransmission time and delay, so it is sent at a later poll. What
// Dispatch decided before emitting stands: the server's 10 s timeout runs from
// the query's first attempt to that server, refused or not; a move to the
// next server, or a failure, is not undone.
type EmitFunc func(ip wire.IPv4Repr, header wire.UDPRepr, payload []byte) error

// Accepts reports whether a UDP datagram is for the resolver: it comes from
// port 53 of one of the servers, or from port 5353 (multicast DNS) of anyone,
// as smoltcp accepts them. The interface offers a
// datagram to the DNS sockets only when no UDP socket accepted it.
//
// Accepting port 5353 has a cost: any host that can send the interface a
// datagram can answer a pending query from its own address, without forging
// a server's. Only the query's port and transaction id stand in its way, and
// both are predictable (socket.Rand).
func (s *Socket) Accepts(ip wire.IPv4Repr, header wire.UDPRepr) bool {
	fromServer := header.SrcPort == dnsPort && slices.Contains(s.servers, ip.SrcAddr)
	return fromServer || header.SrcPort == mdnsPort
}

// Process reads a response Accepts accepted and settles the pending query it
// answers, matched by port and transaction id: an NXDOMAIN answer or one
// without addresses fails the query, otherwise it completes with the
// addresses of the records for its name, following CNAME records in order.
// Malformed or unmatched responses are ignored.
func (s *Socket) Process(ip wire.IPv4Repr, header wire.UDPRepr, payload []byte) {
	netlog.Trace("receiving octets", "octets", len(payload), "from", ip.SrcAddr, "port", header.DstPort)
	response, ok := parseResponse(payload)
	if !ok {
		return
	}
	for _, q := range s.queries {
		if !q.isPendingFor(header.DstPort, response.TransactionID()) {
			continue
		}
		if response.Rcode() == wire.DNSRcodeNXDomain {
			netlog.Trace("rcode NXDomain")
			q.fail()
			continue
		}
		q.settle(response)
		// The response matched this query: stop processing.
		return
	}
	netlog.Trace("no query matched")
}

// Dispatch emits at most one query that is due. A server gets a query at once
// and retransmissions after delays that double from 1 s (capped at 10 s), so
// at 0, 1, 3 and 7 s after its first attempt. At the first Dispatch more than
// 10 s after that attempt, the query moves on to the next server, which gets
// it at once with the delay back at 1 s; after the last server, the query
// fails (at once, for a socket without servers). A query also fails when it
// reaches a server that is unspecified or not IPv4, or when the interface has
// no address to send from. Dispatch returns emit's error unchanged, or nil.
func (s *Socket) Dispatch(cx *socket.Context, emit EmitFunc) error {
	for _, q := range s.queries {
		if !q.isPending() {
			continue
		}
		sent, err := s.dispatchQuery(cx, q, emit)
		if sent || err != nil {
			return err
		}
	}
	return nil
}

// PollAt returns the earliest retransmission time of the pending queries, or
// on ingress when there are none. It does not include the 10 s server
// timeout, which Dispatch checks when the next retransmission is due.
func (s *Socket) PollAt() socket.PollAt {
	earliest := socket.PollOnIngress()
	for _, q := range s.queries {
		if q.isPending() {
			earliest = socket.Earliest(earliest, socket.PollAtTime(q.pending.retransmitAt))
		}
	}
	return earliest
}

// dispatchQuery sends the pending query q if it is due, moving it on to the
// next server first when the current one has had its time. It fails the
// query instead when no usable server is left or the interface has no
// address to send from. It reports whether it sent the query, and returns
// emit's error.
func (s *Socket) dispatchQuery(cx *socket.Context, q *query, emit EmitFunc) (bool, error) {
	now := cx.Now()
	pending := &q.pending
	pending.moveOnAfterTimeout(now)
	server, ok := s.serverFor(pending)
	if !ok {
		q.fail()
		return false, nil
	}
	if pending.isWaitingToRetransmit(now) {
		return false, nil
	}
	source, ok := cx.SourceAddress(server)
	if !ok {
		netlog.Trace("no source address for destination", "dst", server)
		q.fail()
		return false, nil
	}
	ip, header, payload := pending.request(source, server)
	netlog.Trace("sending octets", "octets", len(payload), "to", server, "port", header.SrcPort)
	if err := emit(ip, header, payload); err != nil {
		return false, err
	}
	pending.scheduleRetransmission(now)
	return true, nil
}

// serverFor returns the server to send pending to; false, when it has tried
// every server or the server is unusable: unspecified, or not IPv4, which
// the stack cannot reach.
func (s *Socket) serverFor(pending *pendingQuery) (netip.Addr, bool) {
	if pending.serverIndex >= len(s.servers) {
		netlog.Trace("already tried all servers.")
		return netip.Addr{}, false
	}
	server := s.servers[pending.serverIndex]
	if !server.Is4() || server.IsUnspecified() {
		netlog.Trace("invalid unspecified DNS server addr.")
		return netip.Addr{}, false
	}
	return server, true
}
