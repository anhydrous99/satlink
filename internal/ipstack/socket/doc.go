// Package socket holds what the TCP, UDP and DNS sockets share without
// depending on the interface that drives them: the Context the interface
// hands them (the current time, the random source, the IP MTU and the
// interface's addresses), PollAt, the sockets' answer to "when do you next
// need attention?", and Rand, smoltcp's small PRNG.
//
// It is the socket-facing half of smoltcp's InterfaceInner (iface::Context),
// plus socket::PollAt and the rand module.
//
// Derived from smoltcp (https://github.com/smoltcp-rs/smoltcp), 0BSD licence;
// see ../LICENSE-smoltcp.
package socket
