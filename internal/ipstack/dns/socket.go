package dns

import (
	"net/netip"
	"slices"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// Limits of the resolver, set as the workspace configures smoltcp
// (dns-max-server-count-2, dns-max-result-count-4) and at smoltcp's default
// name size.
const (
	// MaxServerCount is how many servers a socket keeps: IPCP hands out
	// two. Further servers are ignored.
	MaxServerCount = 2
	// MaxResultCount is how many addresses a query keeps from a response,
	// a few per name; later ones are ignored.
	MaxResultCount = 4
	// MaxNameSize is the longest name a query may ask for, in octets of its
	// wire form (RFC 1035 § 2.3.4).
	MaxNameSize = 255
)

// QueryHandle identifies a query of a Socket; it is an opaque, comparable
// value. It is valid from StartQuery until TakeQueryResult returns something
// other than ErrPending, or until CancelQuery; the socket then reuses the
// query's slot, and with it the handle, for a later query. The zero
// QueryHandle identifies no query.
type QueryHandle struct {
	// slot is the query's index in the socket plus one, so that the zero
	// handle is invalid.
	slot int
}

// Socket is a DNS resolver. The zero Socket is ready to use: like
// NewSocket(nil), it is a resolver without servers, whose queries fail at
// their first Dispatch unless UpdateServers has given it some by then. A
// Socket is not safe for concurrent use.
type Socket struct {
	// servers are the servers to ask, in turn: at most MaxServerCount.
	servers []netip.Addr
	// queries holds the queries by slot, a free slot being nil. It grows as
	// needed, as smoltcp's growable query storage does.
	queries []*query
}

// NewSocket returns a resolver that asks servers, of which it keeps the first
// MaxServerCount. A server must be an IPv4 address (Is4): a query fails when
// it reaches a server that is unspecified or not IPv4, as smoltcp fails one
// that reaches an unspecified server.
func NewSocket(servers []netip.Addr) *Socket {
	return &Socket{servers: slices.Clone(servers[:min(len(servers), MaxServerCount)])}
}

// UpdateServers replaces the servers, keeping the first MaxServerCount, with
// the same rules as NewSocket; queries in progress continue with the new
// list.
func (s *Socket) UpdateServers(servers []netip.Addr) {
	if len(servers) > MaxServerCount {
		netlog.Trace("Max DNS Servers exceeded. Increase MaxServerCount")
		servers = servers[:MaxServerCount]
	}
	s.servers = slices.Clone(servers)
}

// StartQuery starts looking up name, given as "example.com" with or without
// a trailing dot (there is no search path), for records of queryType; the
// first query leaves on the next poll. It draws the query's transaction id
// (cx.Rand().Uint16()) and then its source port (cx.Rand().SourcePort()), in
// that order. It returns ErrInvalidName for an empty name or an empty or
// over-long label, and ErrNameTooLong when the name exceeds MaxNameSize
// octets on the wire.
func (s *Socket) StartQuery(cx *socket.Context, name string, queryType wire.DNSType) (QueryHandle, error) {
	encoded, err := encodeName(name)
	if err != nil {
		return QueryHandle{}, err
	}
	return s.add(newQuery(cx, encoded, queryType)), nil
}

// TakeQueryResult returns the query's addresses (at most MaxResultCount),
// ErrPending while it has no answer yet, or ErrFailed. Unless it returns
// ErrPending, it also ends the query: the handle is spent, and the query's
// slot is free for a later one. It panics if the handle identifies no query
// (the zero handle, or a spent one).
func (s *Socket) TakeQueryResult(handle QueryHandle) ([]netip.Addr, error) {
	q, ok := s.queryAt(handle)
	if !ok {
		panic("Getting the result of a query in a free slot.")
	}
	switch q.state {
	case queryPending:
		return nil, ErrPending
	case queryCompleted:
		s.free(handle)
		return q.addresses, nil
	default:
		s.free(handle)
		return nil, ErrFailed
	}
}

// CancelQuery abandons a query and spends its handle. It panics if the
// handle identifies no query (the zero handle, or a spent one).
func (s *Socket) CancelQuery(handle QueryHandle) {
	if _, ok := s.queryAt(handle); !ok {
		panic("Canceling query in a free slot.")
	}
	s.free(handle)
}

// add puts q in the lowest free query slot, adding a slot when none is free,
// and returns its handle.
func (s *Socket) add(q *query) QueryHandle {
	index := slices.Index(s.queries, nil)
	if index < 0 {
		s.queries = append(s.queries, nil)
		index = len(s.queries) - 1
	}
	s.queries[index] = q
	return QueryHandle{slot: index + 1}
}

// queryAt returns the query handle identifies; false when it identifies no
// query of the socket.
func (s *Socket) queryAt(handle QueryHandle) (*query, bool) {
	index := handle.slot - 1
	if index < 0 || index >= len(s.queries) || s.queries[index] == nil {
		return nil, false
	}
	return s.queries[index], true
}

// free frees the slot of the query handle identifies, for a later query to
// take: the handle is spent.
func (s *Socket) free(handle QueryHandle) {
	s.queries[handle.slot-1] = nil
}
