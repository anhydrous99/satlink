// Package tcp is the stack's TCP socket, modelled on smoltcp's TCP socket
// (RFC 793 and RFC 9293, with window scaling from RFC 7323, selective
// acknowledgements from RFC 2018, the retransmission timer of RFC 6298,
// delayed ACKs, Nagle's algorithm, keep-alive, and zero window probes).
//
// A Socket either listens for one connection or connects to a peer. There is
// no accept backlog: to accept several connections at once, put several
// listening sockets in the set, or new connection attempts are reset.
//
// The application side of the API (Listen, Connect, SendSlice, ReceiveSlice,
// Close…) never touches the network: the interface moves segments in and out
// on its next poll, through Accepts, Process, Dispatch and PollAt.
//
// Compared with smoltcp, this socket has no congestion control, never sends
// TCP timestamps and ignores the peer's, always sends with the default hop
// limit, and has no async wakers. It also handles four kinds of segment that
// smoltcp mishandles. It never takes more data than the receive buffer can
// hold, even right after its SYN; it copes with a peer's SYN that arrives
// before its own has gone out, or that answers its own while it has no receive
// buffer; and while its receive window ends before it starts, as after such a
// SYN or after a FIN right at the window's end, it takes no segment from half
// the sequence space away. On each of these, smoltcp can panic. And in the
// states that take the peer's FIN (SYN-RECEIVED, ESTABLISHED, FIN-WAIT-1 and
// FIN-WAIT-2) it ignores the FIN of a segment whose data the receive window cut
// short, which smoltcp takes, ending the stream early and acknowledging an
// octet it never received; the other states take no FIN, and handle such a
// segment as smoltcp does.
//
// Derived from smoltcp (https://github.com/smoltcp-rs/smoltcp), 0BSD licence;
// see ../LICENSE-smoltcp.
package tcp
