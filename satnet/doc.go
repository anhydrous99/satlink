// Package satnet is app-private IP networking over an Iridium handset's
// data call.
//
// A [Link] dials the handset, runs PPP and a TCP/IP stack inside this
// process, and hands out [TCPConn], [UDPConn] and [TLSConn] connections.
// The operating system never sees a network interface, route or DNS server,
// so no other program on the computer or phone can use the satellite link.
//
// The app stays in control of what the link costs and where it goes:
//   - A [Policy] lists the only destinations that may be reached. Names are
//     checked before they are even looked up.
//   - [Limits] hang up an idle link, and cap airtime and data volume.
//   - [Tuning] fits the transport to a 2.4 kbit/s link with round trips of
//     1.5 to 3 seconds, as described under Satellite tuning.
//
// For example:
//
//	policy, err := satnet.ParsePolicy("relay.example.com:443")
//	if err != nil {
//		return err
//	}
//	link, err := satnet.Establish(handset, satnet.NewConfig(policy)) // about 30 s
//	if err != nil {
//		return err
//	}
//	conn, err := link.Dial("relay.example.com", 443, time.Minute)
//	if err == nil {
//		_, err = conn.Write([]byte("hello from the sky"))
//	}
//	// Closing delivers what was written, then hangs up, even after a
//	// failure: airtime is billed by the minute. The handset is back in
//	// command mode, ready for SMS.
//	return errors.Join(err, link.Close())
//
// For a messaging app, the efficient shape is one long-lived TLS connection
// to a relay server you run. The relay talks to email, chat and social
// networks on the app's behalf, and sends only compact deltas over the
// satellite link.
//
// # Policy rules
//
// A [Policy] denies everything until rules are added. A rule names a
// target — a host, every subdomain of a domain, or an IPv4 network — and
// the ports allowed on it:
//
//	Rule                         Allows
//	api.example.com:443          that host, port 443
//	*.example.com:443            every subdomain of example.com, port 443
//	mail.example.com:*           every port on that host
//	203.0.113.7:25               that address, port 25
//	203.0.113.0/24:8000-8099     that network, ports 8000 to 8099
//	*:443                        any host reached by name, port 443
//
// Names are authorised by name. Connecting to api.example.com uses its rule
// whatever address DNS returns. Only names that some rule matches are ever
// looked up, so DNS cannot be used to leak data either. Connecting to a
// bare address needs an address or network rule.
//
// # Satellite tuning
//
// The default [Tuning] keeps the airtime for the app's own data:
//   - Packets are small, and are released no faster than the line carries
//     them. A backlog waits in the app, not in the handset's buffer.
//   - Connections share the line in turns, a segment each, so a bulk upload
//     cannot hold up a chat.
//   - Acknowledgements, connection set-up and DNS queries go at once, not
//     behind anyone's data.
//   - Retransmissions are dropped when they come too early, or when an
//     answer arrived while they waited.
//   - No ICMP errors are sent. Each would echo up to 548 bytes of what
//     arrived, perhaps to a destination the policy does not allow.
//   - A DNS cache and [Config].Hosts save lookups.
//
// # The handset
//
// While a link is up, its handset is busy: the handset's own methods fail
// with iridium.ErrBusy. The handset is released, back in command mode, once
// the link has hung up. That is when [Link.Close] returns, or when
// [Watcher.WaitClosed] reports the link closed. A Link that is garbage
// collected without Close still hangs up, in the background.
//
// A hang-up may not be confirmed: neither ATH after the escape nor a DTR
// drop was. Then [Status].CallMayStillBeUp says so, and the call may still
// be billing. Close fails with an error for which
// errors.Is(err, iridium.ErrCallMayStillBeUp) holds, and so does Establish
// for a link that never came up. The handset's EndDataCall retries once the
// handset answers; power-cycling it is the last resort.
//
// # TLS
//
// A full TLS 1.3 handshake moves 3–5 KB, about 15–25 seconds at
// 2.4 kbit/s. So:
//   - Keep one [TLSConnector] for the whole app. Its session cache lets
//     later connections to the same server resume with about 1 KB.
//   - No post-quantum key share is offered. X25519, P-256 and P-384 are
//     offered, with a key share for X25519 only. X25519MLKEM768 would add
//     about 2.3 KB to every handshake, 1.2 KB of it in the ClientHello.
//   - crypto/tls asks servers for post-quantum (ML-DSA) certificates, and
//     for RSA-PSS signatures before ECDSA ones. A server that holds several
//     certificates may therefore send one with another key. An ML-DSA key
//     and signature add about 5 KB to a full handshake. Pin the key of each
//     certificate ([TLSOptions].PinnedPublicKeys).
//   - Certificate compression (RFC 8879) is not available, as crypto/tls
//     does not implement it. With servers that compress certificate chains,
//     a full handshake costs a quarter to a third more.
//   - crypto/tls resumes a TLS 1.2 session with a ticket only, not by its
//     session ID. A TLS 1.2 server that issues no tickets costs a full
//     handshake every time.
//
// # Errors
//
// Errors are [*Error] values, with these exceptions, as in the net package:
//   - a connection's Read returns io.EOF at the end of its stream;
//   - the methods of a connection this side has closed return net.ErrClosed;
//   - [Link.DialContext] reports another network as a
//     net.UnknownNetworkError, a malformed address as a *net.AddrError, and
//     a ctx that is done first as a *net.OpError wrapping ctx.Err().
//
// Each [ErrorKind] constant is a sentinel for errors.Is. errors.Is also
// matches the standard library's equivalents, so code written for the net
// package needs no changes:
//
//	ErrTimedOut                                  os.ErrDeadlineExceeded
//	ErrLinkDown                                  syscall.ENOTCONN
//	ErrPolicyDenied                              os.ErrPermission
//	ErrInvalidConfig, ErrInvalidHost,
//	ErrDatagramTooLarge                          os.ErrInvalid
//	ErrConnectionReset                           syscall.ECONNRESET
//	ErrConnectionFailed                          syscall.ECONNREFUSED
//	ErrTLSTruncated                              io.ErrUnexpectedEOF
//
// A link that went down is not net.ErrClosed, which means that this side
// closed the connection. A deadline is not context.DeadlineExceeded, which
// the net package keeps for contexts.
//
// An Error of kind ErrTimedOut is also a net.Error whose Timeout and
// Temporary methods report true. So crypto/tls and net/http treat it as a
// timeout, and a TLS session survives a read that timed out. An Error of
// kind ErrHandset wraps the *iridium.Error from the handset. An Error whose
// CallMayStillBeUp is set, a failed hang-up, also matches
// iridium.ErrCallMayStillBeUp.
package satnet
