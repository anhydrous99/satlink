package satnet

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
)

// Resolving names and opening connections, for the Link's Resolve, Dial,
// DialUDP and DialContext. Every operation finishes by a deadline (the
// zero time: none) and is abandoned once its context is done.

// resolve returns the IPv4 addresses of host by deadline, as Link.Resolve
// does.
func (l *Link) resolve(ctx context.Context, host string, deadline time.Time) ([]netip.Addr, error) {
	if address, ok := parseIPv4(host); ok {
		return []netip.Addr{address}, nil
	}
	name, ok := normalizeHost(host)
	if !ok {
		return nil, &Error{Kind: ErrInvalidHost, Host: host}
	}
	if !l.policy.AllowsLookup(name) {
		return nil, &Error{Kind: ErrPolicyDenied, Destination: name}
	}
	return l.resolveName(ctx, name, deadline)
}

// resolveName returns the addresses of name, which is normalised and
// allowed: from the local hosts or the cache if they are there, and
// otherwise from a DNS lookup, which costs a satellite round trip.
func (l *Link) resolveName(ctx context.Context, name string, deadline time.Time) ([]netip.Addr, error) {
	var (
		addresses []netip.Addr
		cached    bool
		id        lookupID
	)
	err := l.shared.withLock(func(e *engine) error {
		if err := e.checkUsable(); err != nil {
			return err
		}
		now := time.Now()
		if addresses, cached = e.names.lookup(name, now); cached {
			return nil
		}
		e.recordActivity(now)
		var err error
		id, err = e.stack.startLookup(name)
		return err
	})
	if err != nil || cached {
		return addresses, err
	}
	return l.awaitAnswer(ctx, name, id, deadline)
}

// awaitAnswer waits by deadline for the outcome of the lookup id, which
// asks for name, and remembers the addresses it finds.
func (l *Link) awaitAnswer(ctx context.Context, name string, id lookupID, deadline time.Time) ([]netip.Addr, error) {
	// Once the lookup has an outcome its id is spent; only one still
	// pending when we stop waiting must be abandoned.
	settled := false
	var answer []netip.Addr
	err := l.shared.waitUntil(waitTerms{operation: operationDNSLookup, until: fixedDeadline(deadline), ctx: ctx},
		func(e *engine) (bool, error) {
			outcome := e.stack.lookupResult(id)
			if outcome.kind == lookupPending {
				return false, nil
			}
			settled = true
			if err := lookupFailure(name, outcome.kind); err != nil {
				return true, err
			}
			e.names.remember(name, outcome.addresses, time.Now())
			answer = outcome.addresses
			return true, nil
		})
	if !settled {
		l.withStack(func(s *stack) { s.abandonLookup(id) })
	}
	return answer, err
}

// lookupFailure is the error of a lookup of name that ended as kind says:
// ErrNameNotFound when a DNS server said the name has no IPv4 address, and
// ErrTimedOut for operation "DNS lookup" when no answer came in time — or
// only a server failure, which is worth retrying; nil once resolved.
func lookupFailure(name string, kind lookupKind) error {
	switch kind {
	case lookupNoAddress:
		return &Error{Kind: ErrNameNotFound, Host: name}
	case lookupUnanswered:
		return &Error{Kind: ErrTimedOut, Operation: operationDNSLookup}
	default:
		return nil
	}
}

// dial opens a TCP connection to host on port by deadline, as Link.Dial
// does.
func (l *Link) dial(ctx context.Context, host string, port uint16, deadline time.Time) (*TCPConn, error) {
	if err := l.ensureAllowed(host, port); err != nil {
		return nil, err
	}
	addresses, err := l.resolve(ctx, host, deadline)
	if err != nil {
		return nil, err
	}
	var lastErr error = &Error{Kind: ErrNameNotFound, Host: host}
	for _, address := range addresses {
		conn, err := l.connectTo(ctx, netip.AddrPortFrom(address, port), deadline)
		if err == nil {
			return conn, nil
		}
		if endsAttempts(ctx, err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// endsAttempts reports whether err, a failed attempt to connect, ends a
// dial's attempts at once: a timeout, the link going down, or the dial's
// context ending.
func endsAttempts(ctx context.Context, err error) bool {
	return errors.Is(err, ErrTimedOut) || errors.Is(err, ErrLinkDown) || ctx.Err() != nil
}

// connectTo opens a TCP connection to address by deadline.
func (l *Link) connectTo(ctx context.Context, address netip.AddrPort, deadline time.Time) (*TCPConn, error) {
	var handle ipstack.SocketHandle
	err := l.shared.withLock(func(e *engine) error {
		if err := e.checkUsable(); err != nil {
			return err
		}
		e.recordActivity(time.Now())
		var err error
		handle, err = e.stack.openTCP(address)
		return err
	})
	if err != nil {
		return nil, err
	}
	var local netip.AddrPort
	err = l.shared.waitUntil(waitTerms{operation: operationTCPConnect, until: fixedDeadline(deadline), ctx: ctx},
		func(e *engine) (bool, error) {
			socket := e.stack.tcp(handle)
			local = socket.LocalEndpoint()
			return connectOutcome(e, socket.State(), address)
		})
	if err != nil {
		l.withStack(func(s *stack) { s.abortTCP(handle) })
		return nil, err
	}
	return newTCPConn(l.shared, handle, local, address), nil
}

// connectOutcome says where a connection to address in state stands: still
// connecting, established, or failed.
func connectOutcome(e *engine, state tcp.State, address netip.AddrPort) (bool, error) {
	switch state {
	case tcp.StateSynSent, tcp.StateSynReceived:
		return false, nil
	case tcp.StateClosed, tcp.StateTimeWait:
		// Refused or timed out — or cut off by the link going down.
		if err := e.checkUsable(); err != nil {
			return true, err
		}
		return true, &Error{Kind: ErrConnectionFailed, Addr: address}
	default:
		return true, nil
	}
}

// dialUDP opens a UDP socket for host on port, resolved by deadline, as
// Link.DialUDP does.
func (l *Link) dialUDP(ctx context.Context, host string, port uint16, deadline time.Time) (*UDPConn, error) {
	if err := l.ensureAllowed(host, port); err != nil {
		return nil, err
	}
	addresses, err := l.resolve(ctx, host, deadline)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, &Error{Kind: ErrNameNotFound, Host: host}
	}
	var (
		handle ipstack.SocketHandle
		local  netip.AddrPort
	)
	err = l.shared.withLock(func(e *engine) error {
		if err := e.checkUsable(); err != nil {
			return err
		}
		var err error
		if handle, err = e.stack.openUDP(); err != nil {
			return err
		}
		local = localUDPEndpoint(e.stack, handle)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return newUDPConn(l.shared, handle, local, netip.AddrPortFrom(addresses[0], port)), nil
}

// localUDPEndpoint returns the local end of the UDP socket handle names: our
// address on the link — the zero Addr until IPCP has assigned one — and the
// port it is bound to.
func localUDPEndpoint(s *stack, handle ipstack.SocketHandle) netip.AddrPort {
	return s.localEndpoint(s.udp(handle).Endpoint().Port)
}

// ensureAllowed fails with ErrPolicyDenied unless the policy allows host on
// port.
func (l *Link) ensureAllowed(host string, port uint16) error {
	if l.policy.AllowsHost(host, port) {
		return nil
	}
	return &Error{Kind: ErrPolicyDenied, Destination: host + ":" + strconv.Itoa(int(port))}
}

// withStack runs f on the stack, with the engine locked.
func (l *Link) withStack(f func(s *stack)) {
	e := l.shared.lock()
	defer l.shared.unlock()
	f(e.stack)
}

// dialer opens a connection to host on port by deadline, abandoning it once
// ctx is done.
type dialer func(ctx context.Context, host string, port uint16, deadline time.Time) (net.Conn, error)

// dialerFor returns how DialContext dials on network, and false for a
// network the link does not carry.
func (l *Link) dialerFor(network string) (dialer, bool) {
	switch network {
	case "tcp", "tcp4":
		return asDialer(l.dial), true
	case "udp", "udp4":
		return asDialer(l.dialUDP), true
	default:
		return nil, false
	}
}

// asDialer turns dial, which opens connections of type C, into a dialer.
// It returns a nil net.Conn on failure, not a nil C inside one, which would
// compare unequal to nil.
func asDialer[C net.Conn](dial func(context.Context, string, uint16, time.Time) (C, error)) dialer {
	return func(ctx context.Context, host string, port uint16, deadline time.Time) (net.Conn, error) {
		conn, err := dial(ctx, host, port, deadline)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

// splitHostPort splits "host:port", with a decimal port, as net.Dialer
// does, failing with a *net.AddrError.
func splitHostPort(address string) (string, uint16, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return "", 0, &net.AddrError{Err: "invalid port", Addr: address}
	}
	return host, uint16(port), nil
}

// dialError is what DialContext reports for err: as net.Dialer does, the
// context's own error once it is done — also when the dial timed out at its
// deadline, taken from ctx, which the context may not have noticed yet. Any
// other timeout, such as that of a DNS lookup nobody answered, keeps its
// own error, as net.Dialer keeps a resolver's.
func dialError(ctx context.Context, network string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &net.OpError{Op: "dial", Net: network, Err: ctxErr}
	}
	if deadlinePassed(ctx) && errors.Is(err, ErrTimedOut) {
		return &net.OpError{Op: "dial", Net: network, Err: context.DeadlineExceeded}
	}
	return err
}

// deadlinePassed reports whether ctx has a deadline that has passed. A wait
// that timed out at that deadline saw it pass on the same clock, so this
// holds for its timeout.
func deadlinePassed(ctx context.Context) bool {
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
}
