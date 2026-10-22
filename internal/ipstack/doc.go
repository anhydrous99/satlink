// Package ipstack is a userspace TCP/IP stack for bare IPv4 links, modelled on
// the design of smoltcp 0.14 (https://github.com/smoltcp-rs/smoltcp). satnet
// runs it inside the app, fed by the PPP link, and the simulator runs it on the
// gateway's side, answering for every address.
//
// # Pull model
//
// The stack is driven in a pull model. A Device offers the stack a transmit
// slot only when it can take a datagram, so the stack decides what to send,
// new data or a retransmission, at the last possible moment, and nothing
// waits in front of a slow satellite line to go stale. Stacks that push
// datagrams into a link endpoint as soon as they exist (gVisor's netstack,
// for one) cannot do that.
//
// # Layers
//
// The stack is built in layers, each a package:
//
//   - wire reads and writes IPv4, TCP, UDP, ICMPv4 and DNS packets, at the
//     level of raw header fields (IPv4Packet…) and of validated
//     representations (IPv4Repr…).
//   - storage holds the ring and packet buffers sockets keep their data in.
//   - socket holds what every socket shares: the Context the interface hands
//     it, PollAt and the random source.
//   - tcp, udp and dns are the sockets. Their buffering, packet construction
//     and state machines do not depend on the interface.
//   - this package is the Interface, which takes datagrams from a Device,
//     hands them to the sockets in a SocketSet, answers what no socket wants
//     (TCP RST, ICMP port and protocol unreachable), and sends what the
//     sockets have to send.
//
// The stack never reads the clock and never blocks: every poll takes the
// current time, and the caller decides when to poll (PollAt and PollDelay
// say when it next has to). It is not safe for concurrent use; satnet guards
// it with a mutex.
//
// # Supported subset
//
// The stack handles devices that carry bare IPv4 datagrams, as on a PPP link or
// a TUN device, with IPv4 only and TCP, UDP and DNS sockets. There is no
// Ethernet, IPv6, fragmentation or reassembly, multicast, DHCP, raw or ICMP
// sockets, TCP congestion control, or async wakers, and the interface does not
// answer pings.
//
// Derived from smoltcp, 0BSD licence; see LICENSE-smoltcp in this directory.
package ipstack
