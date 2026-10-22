// Package dns is the stack's DNS resolver socket: a port of smoltcp's
// socket::dns, which sends queries over UDP to the configured servers,
// retransmits them, moves on to the next server after a timeout, follows
// CNAME records within a response, and keeps the addresses a response gives.
//
// Compared with smoltcp, this port has no multicast DNS (socket-mdns is not
// enabled; responses from port 5353 are still accepted, as smoltcp does),
// always sends with the default hop limit, has no async wakers, and keeps
// any number of queries at once, as smoltcp does with growable storage.
//
// Derived from smoltcp (https://github.com/smoltcp-rs/smoltcp), 0BSD licence;
// see ../LICENSE-smoltcp.
package dns
