// Package dns is the stack's DNS resolver socket, modelled on smoltcp's DNS
// socket. It sends queries over UDP to the configured servers, retransmits
// them, moves on to the next server after a timeout, follows CNAME records
// within a response, and keeps the addresses a response gives.
//
// A server gets a query at 0, 1, 3 and 7 seconds after the first attempt to
// it: the delay starts at a second and doubles. The first dispatch more than
// 10 seconds after that attempt sends the query to the next server at once,
// with the delay back at a second. After the last server, the query fails.
// A retransmission keeps the query's transaction id.
//
// Compared with smoltcp, this socket compares names without regard to the
// case of ASCII letters, as RFC 4343 asks, has no multicast DNS of its own
// (responses from port 5353 are still accepted, as smoltcp accepts them),
// always sends with the default hop limit, has no async wakers, and keeps any
// number of queries at once in growable storage.
//
// Derived from smoltcp (https://github.com/smoltcp-rs/smoltcp), 0BSD licence;
// see ../LICENSE-smoltcp.
package dns
