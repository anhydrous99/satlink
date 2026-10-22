// Package dns is the stack's DNS resolver socket, modelled on smoltcp's
// socket::dns, which sends queries over UDP to the configured servers,
// retransmits them, moves on to the next server after a timeout, follows
// CNAME records within a response, and keeps the addresses a response gives.
//
// Compared with smoltcp, this socket has no multicast DNS of its own (responses
// from port 5353 are still accepted, as smoltcp accepts them), always sends
// with the default hop limit, has no async wakers, and keeps any number of
// queries at once in growable storage.
//
// Derived from smoltcp (https://github.com/smoltcp-rs/smoltcp), 0BSD licence;
// see ../LICENSE-smoltcp.
package dns
