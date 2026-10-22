// Package udp is the stack's UDP socket: a port of smoltcp's socket::udp.
//
// A Socket is bound to a local port (and optionally address) and keeps two
// packet buffers: datagrams received for it, and datagrams queued to send,
// each with its Metadata. The interface fills and drains them on its polls.
//
// Compared with smoltcp, this port always sends with the default hop limit
// and has no async wakers.
//
// Derived from smoltcp (https://github.com/smoltcp-rs/smoltcp), 0BSD licence;
// see ../LICENSE-smoltcp.
package udp
