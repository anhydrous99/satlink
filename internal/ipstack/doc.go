// Package ipstack is a userspace TCP/IP stack: a Go port of the parts of
// smoltcp 0.14 (https://github.com/smoltcp-rs/smoltcp) that the Rust
// workspace this module ports compiles in. satnet runs it inside the app, fed
// by the PPP link, and the simulator runs it on the gateway's side, answering
// for every address.
//
// # Why a port of smoltcp
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
// As in smoltcp, the stack is built in layers, each a package:
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
// The workspace enables smoltcp's std, log, medium-ip, proto-ipv4,
// socket-tcp, socket-udp, socket-dns, dns-max-server-count-2 and
// dns-max-result-count-4 features, and this port has exactly what they
// compile: devices that carry bare IPv4 datagrams (smoltcp's Medium::Ip, as
// on PPP or a TUN device), IPv4 only, TCP, UDP and DNS sockets. There is no
// Ethernet, IPv6, fragmentation or reassembly, multicast, DHCP, raw or ICMP
// sockets, TCP congestion control, or async wakers, and, since the
// auto-icmp-echo-reply feature is not enabled either, the interface does not
// answer pings.
//
// Derived from smoltcp, 0BSD licence; see LICENSE-smoltcp in this directory.
package ipstack
