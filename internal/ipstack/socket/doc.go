// Package socket holds what the TCP, UDP and DNS sockets share without
// depending on the interface that drives them: the Context the interface
// hands them (the current time, the random source, the IP MTU and the
// interface's addresses), PollAt, the sockets' answer to "when do you next
// need attention?", and Rand, a small PRNG.
//
// It is the half of the interface that the sockets see: the Context, PollAt
// and Rand.
//
// Derived from smoltcp (https://github.com/smoltcp-rs/smoltcp), 0BSD licence;
// see ../LICENSE-smoltcp.
package socket
