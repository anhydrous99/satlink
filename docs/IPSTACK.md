# internal/ipstack design notes

`internal/ipstack` is satlink's TCP/IP stack. It carries IPv4 datagrams between a
device and a set of sockets: TCP, UDP and DNS. The consumers are satnet, which
carries an app's connections over the satellite link, and sim, the simulated
internet at the far end of that link. This document is for the maintainers of
the stack and for anyone who changes it, including changes that the consumers
will feel.

The stack is split into these packages:

- `wire`: packet formats. Parsing, emitting, lengths and checksums for IPv4,
  TCP, UDP, ICMPv4 and DNS.
- `storage`: the buffers the sockets use. A ring buffer, a packet buffer and an
  assembler of out-of-order data.
- `socket`: what the sockets share with the interface: the poll context, the
  random source, the poll times.
- `tcp`, `udp`, `dns`: the sockets.
- `ipstack` (the module root): the interface, the socket set, the device
  interface, and the ingress and egress that connect the sockets to a device.
- `internal/netlog`: the logging every package shares.

The design follows smoltcp 0.14, an open-source TCP/IP stack. Where the stack differs,
section 9 says so and why. The package includes the smoltcp licence, 0BSD, in
`LICENSE-smoltcp`, and every package doc credits smoltcp.

The stack is pull-driven. It never calls the device. The caller polls the
interface with `Poll`, and the device hands over received datagrams and
transmit slots when the interface asks for them. Section 5 describes the
contracts this imposes.

## 1. What the package compiles

The stack implements smoltcp built with these features:

    std, log, medium-ip, proto-ipv4, socket-tcp, socket-udp, socket-dns,
    dns-max-server-count-2, dns-max-result-count-4

That set implies `alloc`, `proto-dns` and `socket`. Nothing else is enabled. In
particular there is no Ethernet, no IPv6 and no fragmentation (section 3).

These build-time constants apply:

| smoltcp constant | value | Go |
|---|---|---|
| `ASSEMBLER_MAX_SEGMENT_COUNT` | 4 | `storage.AssemblerMaxSegmentCount` |
| `DNS_MAX_SERVER_COUNT` | 2 | `dns.MaxServerCount` |
| `DNS_MAX_RESULT_COUNT` | 4 | `dns.MaxResultCount` |
| `DNS_MAX_NAME_SIZE` | 255 | `dns.MaxNameSize` |
| `IFACE_MAX_ADDR_COUNT` | 2 | none: a Go slice has no fixed size |
| `IFACE_MAX_ROUTE_COUNT` | 2 | none: routes are omitted (section 3) |

**The stack does not answer pings.** The `auto-icmp-echo-reply` feature is not in
the feature set, so an incoming echo request gets no reply. The stack parses
incoming ICMP and drops it. Destination unreachable replies are sent (port and
protocol unreachable, section 8).

## 2. File map

Paths are relative to `internal/ipstack`. Each package has a `doc.go` that
credits smoltcp.

| Package | File | Contents |
|---|---|---|
| `ipstack` | `interface.go` | `Interface`, `Config`, `NewInterface`, `Poll`, `PollIngressSingle`, `PollAt`, `PollDelay`, `SetIPAddrs`, `SetAnyIP` |
| `ipstack` | `device.go` | `Device`, `TransmitSlot`, `DeviceCapabilities`, `MinMTU` |
| `ipstack` | `ingress.go` | Received datagrams: the checks, then the sockets in turn (`socketIngress`, `processIP`) |
| `ipstack` | `egress.go` | Egress rounds: each socket's `Dispatch` in slot order |
| `ipstack` | `dispatch.go` | `dispatchIP`: a datagram the interface sends, and the MTU check |
| `ipstack` | `ipv4.go`, `tcp.go`, `udp.go` | IPv4 and ICMPv4 handling, the interface side of TCP and UDP |
| `ipstack` | `packet.go` | `ipPacket`, the datagram representation the interface sends (unexported) |
| `ipstack` | `any_socket.go` | `anySocket`, the interface the socket set drives when sending |
| `ipstack` | `socket_set.go` | `SocketSet`, `SocketHandle`, the typed accessors and iterators |
| `internal/netlog` | `netlog.go` | `Trace` and `Debug` (section 7) |
| `wire` | `ip.go` | `IPProtocol`, `ListenEndpoint`, the address helpers |
| `wire` | `checksum.go` | RFC 1071 arithmetic and the pseudo-header (unexported) |
| `wire` | `ipv4.go`, `icmpv4.go`, `udp.go` | IPv4, ICMPv4 and UDP packets and representations |
| `wire` | `tcp.go`, `tcp_seq_number.go`, `tcp_option.go` | TCP packets and representations, `TCPSeqNumber`, TCP options (unexported) |
| `wire` | `dns.go`, `dns_name.go` | DNS packets, questions and records; `NameLabels` (names, labels, compression pointers) |
| `wire` | `optional.go`, `errors.go` | `Optional[T]`, `Some`; `ErrMalformed` |
| `storage` | `ring_buffer.go`, `packet_buffer.go`, `assembler.go` | `RingBuffer`, `PacketBuffer`, `Assembler` |
| `storage` | `errors.go` | Sentinel errors |
| `socket` | `context.go` | `Context`: the time, the random source, the IP MTU, the addresses, any-IP |
| `socket` | `poll_at.go` | `PollAt`, `PollNow`, `PollAtTime`, `PollOnIngress`, `Earliest` |
| `socket` | `rand.go` | `Rand`, the sPCG32 generator |
| `tcp` | `socket.go` | `Socket`, its options, endpoints and predicates; `SocketBuffer` |
| `tcp` | `state.go`, `errors.go` | `State` and its names; sentinel errors |
| `tcp` | `connection.go` | `connection`, the record that `reset` replaces; `fourTuple`; `Listen`, `Connect`, `Close`, `Abort` |
| `tcp` | `stream.go` | `Send`, `Receive`, `Peek` and their slice forms |
| `tcp` | `segments.go` | `Accepts`, `Process`, `Dispatch`, `PollAt`, `RSTReply`: what the interface calls |
| `tcp` | `process.go`, `transitions.go` | `process`; `transitions.go` has one function per state |
| `tcp` | `reassembly.go` | The receive side of `process`: data, the assembler and the window |
| `tcp` | `reply.go` | RST and ACK replies, and the challenge ACK |
| `tcp` | `transmit.go` | `seqToTransmit`, `ackToTransmit` and the other predicates `dispatch` and `PollAt` share; `localMSS`; `timeoutDeadline` |
| `tcp` | `dispatch.go` | `dispatch`; `emitDueSegment` builds, emits and records the due segment |
| `tcp` | `timer.go`, `rtt_estimator.go`, `delayed_ack.go` | The retransmission, keep-alive and close timers; the RTT estimator; the ACK-delay timer |
| `tcp` | `window.go` | Window scaling and `windowToUpdate` |
| `udp` | `socket.go` | `Socket`, `Metadata`, `Bind`, `Close`, the capacities |
| `udp` | `send.go`, `receive.go` | Queueing outgoing and incoming datagrams |
| `udp` | `datagrams.go` | `Accepts`, `Process`, `Dispatch`, `PollAt`, `sourceAddress` |
| `dns` | `socket.go` | `Socket`, `NewSocket`, `StartQuery`, `TakeQueryResult`, `CancelQuery`, `UpdateServers` |
| `dns` | `query.go` | The query record, its retransmission schedule and the handle |
| `dns` | `response.go` | The response half of `Process` |
| `dns` | `datagrams.go` | `Accepts`, `Process`, `Dispatch`, `PollAt` |
| `dns` | `name.go` | Name validation and encoding |

### Test files

Tests sit beside the code they test, in a `_test.go` file named after it.

- `wire`: one file per packet type (`ipv4_test.go`, `tcp_test.go`,
  `tcp_seq_number_test.go`, `tcp_option_test.go`, `udp_test.go`,
  `icmpv4_test.go`, `dns_test.go`, `ip_test.go`, `optional_test.go`,
  `checksum_test.go`), `helpers_test.go` (shared builders), `example_test.go`
  (the package example) and `fuzz_test.go` (the fuzz targets, section 6).
- `storage`: `ring_buffer_test.go`, `packet_buffer_test.go`, `assembler_test.go`.
- `socket`: `context_test.go`, `poll_at_test.go`, `rand_test.go`.
- `tcp`: the shared harness is `harness_test.go`. Its helpers `send`, `receive`,
  `receiveNothing` and `sanity` take the place of the test macros of the
  smoltcp tests. Scenario tests are grouped by the state machine, one file for
  each state or transition (`closed_test.go`, `listen_test.go`,
  `syn_received_test.go`, `syn_sent_test.go`, `established_test.go`,
  `fin_wait_test.go`, `closing_test.go`, `time_wait_test.go`,
  `close_wait_test.go`, `last_ack_test.go`, `transitions_test.go`), and by
  feature (`retransmission_test.go`, `window_test.go`, `flow_control_test.go`,
  `zero_window_probe_test.go`, `timeout_test.go`, `keep_alive_test.go`,
  `reassembly_test.go`, `receive_close_test.go`, `delayed_ack_test.go`,
  `nagle_test.go`, `filtering_test.go`, `timestamp_test.go`,
  `source_address_test.go`). Tests of internals sit beside them (`timer_test.go`,
  `rtt_estimator_test.go`). The Go-specific tests are `socket_test.go`,
  `stream_test.go`, `connection_test.go`, `process_test.go`, `dispatch_test.go`
  and `pull_model_test.go`. `fuzz_test.go` holds the socket's fuzz target.
- `udp`: `socket_test.go`.
- `dns`: `socket_test.go`, `name_test.go` and `helpers_test.go` (a test context, a
  recording `EmitFunc` and a builder of responses).
- `ipstack`, internal tests: `testing_device_test.go` (a test device),
  `helpers_test.go` (the test network's addresses and datagram builders),
  `interface_test.go`, `ipv4_test.go`, `socket_set_test.go` and
  `pull_model_test.go`.
- `ipstack`, external tests (package `ipstack_test`): `example_test.go` (the
  package example) and the end-to-end loopback tests (`loopback_test.go`,
  `loopback_network_test.go`, `loopback_services_test.go`, section 6).

Helpers that the `tcp`, `udp` or `dns` tests share must not import package
`ipstack`. It imports those packages, so a test helper that imported it back
would form an import cycle.

## 3. What is left out, and why

The stack follows smoltcp's IPv4, TCP, UDP and DNS. These parts of smoltcp
are not in it. Each entry says why it is not needed, and what would have to
change to add it.

- **Media other than IP.** Ethernet, ARP, IEEE 802.15.4 and 6LoWPAN are out, and
  with them the hardware address, `Medium` and the neighbour cache. The satellite
  link is a bare IP device. On `Medium::Ip`, dispatch never waits for a neighbour.
- **IPv6**, SLAAC, MLD, NDISC, RPL and IPsec.
- **IPv4 fragmentation and reassembly.** The IPv4 parser rejects fragments, and
  `dispatch` drops a datagram larger than the IP MTU and logs it at debug level
  (`fragmentation is not supported`). The interface still draws the fragment
  identifier from its random source when it is created, and throws it away.
  This keeps the random sequence the sockets draw the same as smoltcp's for a
  given seed (section 8).
- **Multicast and IGMP.** The interface treats 224.0.0.1 as its one group, the
  all-hosts group, and does not support joining others.
- **DHCP, raw sockets, ICMP sockets, mDNS.** The DNS socket still accepts
  responses from port 5353, as smoltcp does (section 9). This has a cost: any
  host that can reach the interface can answer a pending query from its own
  port 5353, without forging a server's address. Only the query's source port
  and transaction id stand in its way, and both follow from the `socket.Rand`
  state, which a few observed TCP initial sequence numbers reveal. The mDNS
  feature is not enabled, so the branch serves nothing. Whether to drop the
  5353 branch is an open decision. If it is dropped, record the change in
  section 9 and in [DESIGN.md](DESIGN.md), section 3.
- **TCP congestion control.** Reno and Cubic are out. Without congestion
  control the congestion window is unlimited, and the congestion module does
  not exist. The only limits on data in flight are the peer's window and the
  MSS.
- **Async wakers** and `socket-tcp-pause-synack`.
- **Segmentation offload, `PacketMeta`** and `DeviceCapabilities.max_burst_size`.
  No device sets them.
- **Routes.** On `Medium::Ip` they have no effect. A datagram for an address that
  is not the interface's own is dropped whatever a route lookup would say. With
  any-IP on, the interface reports every address as its own, and the route table
  would not matter.
- **Maintenance polls.** There is no `poll_maintenance`. Without fragmentation or
  SLAAC maintenance has nothing to do, and egress is an internal step of `Poll`.
- **Pretty printing.** `PrettyPrint` and the `Display` impls of the packet views
  and the DNS types are out, since the stack logs nothing that needs them. The
  packet representations `IPv4Repr`, `TCPRepr` and `UDPRepr` have `String`
  methods for logs instead.
- **`auto-icmp-echo-reply`** (section 1), and with it the echo reply to a
  broadcast destination. An ICMP error answers only a datagram whose source and
  destination are both unicast.

Some public options are left out because the consumers never set them. Their
code paths would be dead.

- **phy middleware**: `Loopback`, `Tracer`, `FaultInjector`, `FuzzInjector` and
  `PcapWriter`. The consumers have their own devices.
- **TCP timestamps generator** (`set_tsval_generator`, `timestamp_enabled`).
  Without a generator the stack never sends a timestamps option. The wire layer
  still parses and emits the option, and the sockets do not echo a peer's
  timestamps.
- **Hop limit** (`set_hop_limit` on TCP, UDP and DNS sockets). The stack always
  sends `wire.DefaultHopLimit`, 64.
- **TCP congestion control setters** (`set_congestion_control`), `fmt::Write` for
  the TCP socket, `RingBuffer::reset` (the Go name is `Clear`),
  `TcpPacket::options_summary`, the selective-ACK accessors (no caller),
  `verify_partial_checksum` (offload only), `IpAddress::prefix_len`, and the
  `Ipv4Cidr` helpers, which `net/netip` replaces.
- **Raw DNS query start** (`start_query_raw`, internal to smoltcp).
- **`StartQueryError::NoFreeSlot`.** The query storage grows, so there is no
  such error.

Some internals are left out because nothing in the stack calls them:

- the dual-stack `IpRepr`, `IpVersion` and `ip::Packet`. The sockets and the
  interface use `wire.IPv4Repr`, and the interface reads the version nibble
  itself;
- `Ipv4Packet::get_key`, which only reassembly uses;
- `ChecksumCapabilities::ignored()`. A `wire.ChecksumCapabilities` whose fields
  are `wire.ChecksumNone` says the same;
- the checks smoltcp makes only in debug builds (`debug_assert!`). Release builds
  compile them out. The storage containers still panic on misuse, and smoltcp's
  `unreachable!()` for an ACK that reaches `process` in LISTEN is still a panic
  (section 9 lists its message);
- the TCP traces that smoltcp compiles only under test or its `verbose` feature.

Adding any of these needs a decision first. Raise it before writing the code.

## 4. API conventions

The Go API follows these conventions. Code that adds to the API should keep them.

- **Names** spell out the words smoltcp abbreviates and keep the Go initialisms
  (TCP, UDP, IP, DNS, SYN, ACK, MSS, RTT, RTO, SACK, ISN). `recv` is `Receive`,
  `can_recv` is `CanReceive`, and `rx_buffer` and `tx_buffer` are the unexported
  `receiveBuffer` and `transmitBuffer`. Go drops `get`: `get_allocated` is
  `Allocated`. Unexported names follow the same rules, so `remote_win_len` is
  `remoteWindowLen`. The wire package's own terms stay (`SeqNumber`,
  `AckNumber`).
- **A name that hides a side effect says so.** `TakeQueryResult` ends the
  query, and that is why it has the `Take` prefix.
- **`cx` is the `*socket.Context` parameter** everywhere. It is not `ctx`, which
  is a `context.Context`.
- **Protocol types are named by what they type.** The DNS query type is
  `wire.DNSType`, because the same type also types the records.
- **Packet views.** `wire.XPacket(buf)` wraps a buffer without checking it.
  `wire.NewXPacket(buf)` checks it first. The `Parse…` functions read a
  representation from a buffer, and `Emit` writes one. A view's `Payload()`
  aliases the buffer, so writing through it writes the packet.
- **Lengths.** `HeaderLen()` is the header's length. `BufferLen()` is the
  header plus the payload, as `TCPRepr` has it. So a segment's full datagram
  is `ip.HeaderLen() + segment.BufferLen()`.
- **Addresses** are `netip.Addr`, and endpoints are `netip.AddrPort`. A
  `wire.ListenEndpoint` with a zero `Addr` listens on any address. Interface
  addresses are `netip.Prefix`, which keeps the unmasked address.
- **Only IPv4 addresses enter the stack.** A `netip.Addr` can also be IPv6, an
  IPv4-mapped IPv6 address, or the zero `Addr`. Every place where an address
  comes in from outside checks `Is4`, and a caller that holds an IPv4-mapped
  address unmaps it first (`netip.Addr.Unmap`).
  - `tcp.Socket.Listen`, `tcp.Socket.Connect`, `udp.Socket.Bind` and the UDP
    send methods return `ErrUnaddressable` for an address that is not IPv4.
    A `ListenEndpoint` may leave its address open (the zero `Addr`), and
    `udp.Metadata.LocalAddress` may be the zero `Addr`.
  - A DNS server that is not IPv4 fails the query when the query reaches it.
  - `socket.Context.SetIPAddrs` returns an error, and `SourceAddress` returns
    false for a destination that is not IPv4.
  - The wire functions that write or checksum an address panic. The stack
    builds nothing that reaches them with anything but IPv4.
- **Time.** `time.Time` and `time.Duration`. The zero `time.Time` is the epoch.
  It never means "no deadline". Where there may be no deadline, the API returns
  a `bool` alongside the time, as `Interface.PollAt` does. A duration option
  such as a timeout, keep-alive or ACK delay treats zero, and any negative
  value, as "none". Tests derive every time from one epoch, with
  `time.Time{}.Add(...)`.
- **Optional wire fields.** Wire-level fields whose zero value is valid
  (sequence numbers, window scale, MSS, timestamps, SACK blocks) use
  `wire.Optional[T]`. `wire.Some(x)` makes one present, and the zero `Optional`
  is absent. Elsewhere the zero value means absent: a zero `netip.Addr` or
  `netip.AddrPort`, a zero `wire.ListenEndpoint`, or a zero handle.
- **Results.** Where smoltcp returns an `Option`, the Go API returns the value
  with a `bool`. Where smoltcp returns a per-operation error enum, each package
  has sentinel errors, used with `errors.Is`. The error texts are smoltcp's
  `Display` texts, except that DNS's start in lower case, which is the Go
  convention (staticcheck ST1005).
- **Callbacks** that fill or consume a buffer take `func([]byte) int`, which
  returns how many octets it used. The callback does not return a generic
  result: the caller captures what it needs.
- **Sizes** are `int`, and a negative size panics (section 9).
- **Devices.** The device is the `Device` interface: `Receive(now)` returns a
  datagram and a reply slot, and `Transmit(now)` returns a transmit slot. A
  `TransmitSlot` is `func(datagram []byte)`. The device's MTU is passed to
  `NewInterface` in `DeviceCapabilities`, which is the only place the interface
  reads it.
- **Zero values are usable.** The zero `SocketSet` is an empty set, ready for
  use. The zero `dns.Socket` is a resolver without servers. The zero
  `SocketHandle` and `dns.QueryHandle` identify nothing.
- **String methods** return smoltcp's `Display` text where smoltcp has one, and
  otherwise the name of the variant. Section 9 lists the texts that exist only
  in Go.

## 5. The layers and the contracts between them

The layers are, from the bottom up: `wire`, `storage` and `socket`; then the
sockets `tcp`, `udp` and `dns`; then the interface in `ipstack`. Each layer uses
only the ones below it. `tcp`, `udp` and `dns` never import `ipstack`. This
section records what each layer is built to, and the contracts between the
layers that the code depends on.

### `wire`, `storage`, `socket`

- Every packet view, `Parse…` function, `Emit` method, length and checksum for
  the features in section 1 is implemented. Parsing keeps three quirks:
  - a timestamps option of the wrong length is kept as an unknown option;
  - a SACK option keeps at most three blocks, and logs that it truncates;
  - a window scale above 14 is capped at 14, and logs that it does.
  DNS names follow compression pointers with loop protection (`NameLabels`).
  `ParseDNSQuestion` and `ParseDNSRecord` read a name without following
  pointers.
- `socket.Context.SetIPAddrs` checks each address. It must be IPv4, have a
  valid prefix length, and be unicast or unspecified. Otherwise it returns an
  error that wraps `ErrInvalidIPAddr` and names the address, and changes
  nothing. It does not panic.
- `socket.Rand` is the sPCG32 generator. It is not cryptographically secure:
  a few observed initial sequence numbers reveal its state. Its tests
  (`rand_test.go`) pin the first draws. For seed 0, `Uint32` gives 0x3,
  0xfdecf8a2, 0x6130bec3 and 0x77368c50. For seed 0x5EED it gives 0xaf7268a4,
  0x9ee15bb3, 0x55558f4e and 0xd42f480d. `SourcePort` draws until it has a value
  above 1024.
- The checksum arithmetic in `wire/checksum.go` stays unexported. One helper
  turns an address into its four octets, and panics unless the address is
  `Is4`. Every function that writes or checksums an address uses it.

### `tcp`

- `Socket`, `RSTReply`, the timer, the RTT estimator, the ACK-delay timer and
  the four-tuple are all implemented, and the connection record is kept
  separate. `reset` replaces the connection record, which holds the state, the
  timers, the assembler, the sequence numbers, the tuple and the listen
  endpoint, and it empties both buffers. Fields on the socket itself survive a
  reset: `remoteHasSACK`, `localReceiveLastSeq`, `localReceiveLastAck`,
  `localReceiveDuplicateACKs` and `pendingFastRetransmit`. A reused socket
  therefore starts with the previous connection's values for those.
- **Initial sequence numbers** come from the socket's `chooseISN` field, a
  `func(*socket.Context) wire.TCPSeqNumber`. `NewSocket` sets it to `randomISN`,
  which draws from `cx.Rand()`. `Connect` consults it, and so does `Process`
  when a listener accepts a SYN. The internal tests replace it with one that
  returns `localSeq` (10000). There is no package-level mutable variable.
  Because an absent `wire.Optional` holds sequence number 0, the handshake is
  tested from ISNs across sequence space too (`isnsAcrossSequenceSpace` in
  `connection_test.go`, and the fuzz target's `configUpperHalfISN`).
- **Times that smoltcp uses as "already past"** start at `time.Time{}`, so they
  are due at once. These are the zero-window probe started by a send, the
  keep-alive that `SetKeepAlive` arms, and the challenge-ACK timer
  (`challengeACKTimer`), which starts at the epoch so that the first challenge
  ACK may go at once. Wherever smoltcp has an `Option` of time, the stack uses
  `wire.Optional` or explicit state, never the zero time.
- Congestion control, the timestamp generator and hop limits are left out
  (section 3). Everything else in `process`, `dispatch` and `PollAt` is
  implemented, including SACK ranges in ACK replies, the challenge-ACK rate
  limit (`challengeACKInterval`, one second) and the zero-window probe backoff.
  Section 9 lists where the stack departs from smoltcp.

### `ipstack`, `udp`, `dns`

- The interface keeps an unexported packet type (`ipPacket`) for its replies
  and for egress. It serialises each datagram from the wire representations
  into a fresh buffer, which belongs to the device once it has the datagram.
- The socket set holds its sockets behind an unexported interface (`anySocket`),
  which egress drives through `Dispatch` and `PollAt`. Ingress needs no such
  interface. Each protocol reaches only its own kind of socket, through the
  typed iterators.
- The UDP socket implements the behaviour of smoltcp's UDP socket in full.
- The DNS socket keeps two behaviours of smoltcp's DNS socket. An NXDOMAIN
  response fails every pending query it matches, not only the first. A CNAME
  record replaces the query's name even when the rest of the response turns out
  to be malformed.

### Contracts between the layers

1. **Context.** The interface creates one with
   `socket.NewContext(now, config.RandomSeed, capabilities.MTU)`. It draws the
   fragment identifier from the context's random source before any socket draws
   (section 3). It calls `SetNow(now)` at the start of `Poll`,
   `PollIngressSingle` and `PollAt`, so every socket reads the poll's time.
   Sockets only read the context. `IPMTU` is the device's MTU, since devices
   carry bare IP datagrams.
2. **Ingress (interface to sockets).** A datagram is checked and parsed first.
   Then:
   - **TCP.** A segment whose source or destination address is unspecified is
     dropped before any socket sees it. Otherwise the TCP sockets are tried in
     slot order: the first with `Accepts(ip, seg)` takes the segment through
     `Process(cx, ip, seg)`, and the search stops. Its reply, if any, goes out
     through the received datagram's reply slot. If no socket accepts the
     segment, the interface replies with `tcp.RSTReply(ip, seg)`, unless the
     segment is itself a RST.
   - **UDP.** The UDP sockets are tried in slot order (`Accepts`, then
     `Process`), then the DNS sockets in the same way. If none accepts the
     datagram, the interface replies with ICMP port unreachable.
   - Payload slices handed to `Process` alias the received datagram. Sockets
     copy what they keep.
3. **Egress (sockets to interface).** In slot order, the interface calls each
   socket's `Dispatch(cx, emit)`.
   - `emit` asks the device for a transmit slot first. If the device refuses,
     `emit` returns an unexported sentinel error, and the egress round ends.
   - If the datagram is larger than the MTU, `emit` leaves the slot unused and
     returns nil. The datagram is dropped, but it counts as sent, so a UDP or DNS
     socket dequeues it and the round counts as having sent something. Otherwise
     `emit` serialises the datagram into the slot. Received datagrams are never
     checked against the MTU (section 8).
   - A socket returns `emit`'s error unchanged. It skips exactly what comes after
     the `emit` call, and keeps what it decided before it:
     - TCP skips the keep-alive rewind, the delayed-ACK reset, the zero-window
       probe rewind, the update of the last sequence number, acknowledgement
       and window sent, RTT sampling, arming the retransmission timer, and
       forgetting the four-tuple after an abort's RST. It keeps the start of the
       timeout clock, the abort on timeout, a retransmission timeout's rewind
       and backoff, and the cleared fast-retransmit flag.
     - DNS skips updating its retransmission time and doubling the delay. It
       keeps the server's timeout, which runs from the first attempt, the move to
       the next server, and the failures it decides before emitting.
     - UDP skips the dequeue.
     The next `Dispatch` makes the same choice again, unless something changed in
     between. The exception is a refused fast retransmission, which the next
     `Dispatch` does not repeat.
   - The `Payload` of an emitted `TCPRepr`, and the payload slice from UDP and DNS,
     alias socket storage. They are valid only during `emit`.
   - `Poll` repeats egress rounds while a round sent something (section 8).
4. **PollAt.** The TCP socket's `PollAt` takes `cx`, because `seqToTransmit`
   needs the IP MTU. The UDP and DNS sockets' `PollAt` take nothing. The
   interface combines the results with `socket.Earliest`, and converts the
   combined poll with `PollAt.Deadline(now)`.
5. **Unused parameters are dropped.** Where a method would take `cx` or the
   packet metadata and never read it, the Go method does not take it. This is
   the case for `tcp.Accepts`, `udp.Process`, `udp.PollAt`, `dns.Process` and
   `dns.PollAt`.

## 6. Tests

The tests have four kinds. Scenario tests follow the scenarios of smoltcp's own
tests, with the same setup and assertions, for the features the stack keeps.
Go-specific tests cover behaviour that is specific to the Go API or that the
consumers rely on. Pull-model tests check the device contract (section 5). The
loopback tests run the whole stack end to end. Section 6 lists the tests of each
package, and ends with the coverage numbers.

Rules every test follows:

- **Names.** A scenario test takes the name `Test` + subject + the CamelCase of
  the smoltcp test's name, with Go initialisms (TCP, UDP, IP, DNS, SYN, ACK, FIN,
  RST, MSS, RTT, SACK). For example, the TCP packet deconstruction test is
  `TestTCPPacketDeconstruct`, and the SYN-SENT test for a RST with a bad ACK is
  `TestSynSentRSTBadACK`. Such a test keeps smoltcp's abbreviations as well
  (`TestRxCloseFIN`, `TestEstablishedSendBufGtWin`, `TestDoesntAcceptWrongPort`),
  so that it can be found from either name. Tests of Go-specific behaviour use
  full words.
- **Helpers.** The harness has `send`, `receive`, `receiveNothing` and `sanity`,
  which take the place of smoltcp's test macros. `send` expects the socket to
  reply with nothing. `receive` expects exactly one segment, and ignores PSH
  unless the test asks for an exact match. Every reply the socket sends is
  checked, through `checkOutgoingIPHeader`: the addresses, the protocol, the
  hop limit and the IP payload length, which must equal the segment's
  `BufferLen`. A wrong length would otherwise pass every TCP test and surface
  only as a malformed datagram in the interface tests.
- **Time.** No test sleeps. Every time is derived from one epoch and passed in.
  Most test files call `t.Parallel()`, unless they share state.
- **Helpers shared by `tcp`, `udp` and `dns`** must not import package `ipstack`
  (section 2).

### `wire`

- Packet and representation tests for IPv4, TCP, UDP, ICMPv4, DNS, the IP
  helpers, `Optional`, `TCPSeqNumber` and TCP options cover construction,
  deconstruction, parsing (including truncated and malformed input), emitting,
  and length and checksum behaviour, one file per type.
- **Go-specific**: `Optional`; `TCPSeqNumber` wrap-around and its panics;
  `TCPRepr.Equal`, `HeaderLen` and `String`; `ListenEndpoint`; `ParseIPv4Repr`
  rejecting fragments; every enum's `String`. The DNS name walker
  `NameLabels` stops at its first error, and rejects the reserved label types
  and follows only backward compression pointers
  (`TestDNSPacketNameLabelsStopsAtTheFirstError`). The address setters, `Emit`
  methods and checksum functions panic on an address that is not IPv4. The
  parsers' length and label guards are tested at their exact boundaries.
- **Checksums** are checked against values worked out by hand, and against a
  separate RFC 1071 calculation. They include a pseudo-header whose length has a
  non-zero high octet (`TestPseudoHeaderChecksumAddsAddressesProtocolAndLength`),
  a UDP datagram and a TCP segment of more than 255 octets, whose length has a
  high octet (`TestUDPChecksumOfALongDatagramCoversTheHighOctetOfItsLength`,
  `TestTCPChecksumOfALongSegmentCoversTheHighOctetOfItsLength`), and an IPv4
  header with an option, which `VerifyChecksum` accepts, rejects once the option
  changes, and `FillChecksum` restores (`TestIPv4PacketChecksumCoversTheOptions`).
- `example_test.go` holds the package example.
- **Fuzz targets** (`fuzz_test.go`) exist for every checked constructor and
  parser, and for the TCP option parser and `NameLabels`:
  `FuzzNewIPv4Packet`, `FuzzParseIPv4Repr`, `FuzzNewTCPPacket`,
  `FuzzParseTCPRepr`, `FuzzParseTCPOption`, `FuzzNewUDPPacket`,
  `FuzzParseUDPRepr`, `FuzzNewICMPv4Packet`, `FuzzParseICMPv4Repr`,
  `FuzzNewDNSPacket`, `FuzzParseDNSQuestion`, `FuzzParseDNSRecord` and
  `FuzzDNSPacketNameLabels`. Beyond never panicking (section 8), they check that
  writing back every field read leaves a packet's octets unchanged, that
  `FillChecksum` makes `VerifyChecksum` pass, that a parsed representation
  survives `Emit` and a second parse, and that name parsing always ends. Each has
  a seed corpus in its test file. No crasher has been found, so there is no
  `testdata/fuzz` directory.

### `storage` and `socket`

- **`RingBuffer`**: length changes; enqueue and dequeue of one element, many, and
  a slice, with and without callbacks; access to the unallocated and allocated
  areas; a zero-capacity buffer; writes that fill the buffer wholly.
- **`PacketBuffer`**: header and payload; padding, with and without a large
  payload; full and empty metadata; a window too small for the packet, and the
  contiguous window's wrap-around and priority; a capacity too small; and an
  enqueue that refuses before calling its metadata function.
- **`Assembler`**: adding to an empty and to a partly filled assembler, at the
  front, the back, the middle and across overlaps; removing from the front; joining
  two runs; iteration; a reproduction of an earlier failure. Its table tests
  are `TestAssemblerAddToEmpty` and `TestAssemblerAddToPartial`. Rejected adds
  keep the state. `TestAssemblerRandom` cross-checks the assembler against a bitmap,
  with `math/rand/v2` seeded with a fixed seed, so it is repeatable.
- **`socket.Context`**: local subnet broadcasts (`IsBroadcast`), source address
  selection, and source address on an interface with no addresses.
- **Go-specific, `socket`**: `PollAt` ordering, `Earliest`, `Deadline`, `Equal`
  and `String` (`TestPollAtStringNamesTheVariant`);
  `IsBroadcast` and `IsUnicast`; `SetIPAddrs` refusing multicast, broadcast,
  IPv6, IPv4-mapped addresses and bad prefix lengths; `HasIPAddr` with any-IP;
  `IsUnicast` for subnet broadcasts; `SourceAddress` refusing destinations that
  are not IPv4; the `Rand` golden values (section 5); and the zero `Context` and
  zero `Rand` (`TestZeroContextIsAtTheEpochSeededWithZeroWithoutMTUOrAddresses`,
  `TestZeroRandIsSeededWithZero`).
- **Go-specific, `storage`**: the containers' zero values, their capped slices,
  and their panics. `PacketBuffer` panics on a negative size before it changes
  anything (`TestPacketBufferPanicsOnANegativeSizeBeforeChangingAnything`).
  `EnqueueWith` refuses what cannot fit at all, and a payload one octet larger
  than either free area
  (`TestPacketBufferEnqueueWithRefusesWhatCannotFitAtAll`,
  `TestPacketBufferRefusesAPayloadOneOctetLargerThanEitherFreeArea`). Random
  access just before, at and past the end of an area returns nothing past the
  end (`TestRingBufferRandomAccessGetsNothingPastTheEndOfItsArea`).
  `AddThenRemoveFront` accepts data at offset 0 on a full assembler, on both sides
  of the first hole's end (`TestAssemblerAddThenRemoveFrontNeverRefusesDataAtOffsetZero`).

### `tcp`

- **Scenario tests** cover the state machine, one file for each state or
  transition (section 2), and the features the stack keeps: retransmission,
  windows and window scaling, zero-window probes, timeouts, keep-alive,
  reassembly, the delayed ACK, Nagle, and the source-address change. The
  harness context is `socket.NewContext` at the epoch, with random seed 0, an
  IP MTU of 1500, and the addresses 192.168.1.1/24 and 127.0.0.1/8. The
  socket's buffers default to 64 octets, and the ACK delay is off in the
  harness's socket helpers.
- **Omitted features** have no scenario tests: pause-SYN-ACK, congestion
  control, segmentation offload, hop limits, the congestion-control setters,
  and the timestamp scenarios. The scenarios of the MSS limits that involve
  timestamps are covered without them. `TestEstablishedLocalMSSLimitsSegments`
  (`established_test.go`) sends 4000 queued octets against a peer MSS and window
  of 9999. The octets go out as two local-MSS segments of 1460, the second while
  the first is in flight, and the rest waits.
- **Go-specific**:
  - Zero and negative duration options. `SetKeepAlive` with zero or less arms
    no keep-alive. Turning keep-alive on arms one that is due at once
    (`TestSetKeepAliveOffArmsNoKeepAlive`). Turning it off lets a keep-alive
    already timed go out (`TestTurningKeepAliveOffLetsTheKeepAliveAlreadyTimedGo`).
  - `LocalEndpoint` and `RemoteEndpoint` are the zero `AddrPort` before `Connect`,
    while listening and after a received RST. The four-tuple survives `Abort`, a
    timeout and `Close` in SYN-SENT until the RST has gone, and it stays in
    TIME-WAIT. `TestEndpointsAreZeroWithoutAConnection` pins the rest.
  - `Listen` and `Connect` refuse addresses that are not IPv4 with
    `ErrUnaddressable`. `PeekSlice` works in any state.
  - The exact expiry of the delayed-ACK timer: no ACK at 9 ms of a 10 ms delay,
    and the ACK at 10 ms.
  - Behaviour the scenario tests do not pin. The local MSS follows the IP MTU
    (`TestLocalMSSFollowsTheIPMTU`). The peer's MSS sets the segment size: none or
    0 gives 536-octet segments, and a tiny one gives 48
    (`TestSegmentsFollowThePeersMSS`). The peer's window scale shifts its later
    windows (`TestThePeersWindowScaleAppliesToItsLaterSegments`). A large window
    saturates the field at 65535 for a peer without window scaling
    (`TestALargeWindowSaturatesTheFieldForAPeerWithoutWindowScaling`). A window
    scale of 15 suits a 1 GiB buffer (`TestWindowScaleOfTheLargestReceiveBuffers`).
  - RTT and retransmission. A running retransmission timer is not restarted by
    new data (`TestLaterDataDoesNotRestartTheRetransmissionTimer`). Each round
    trip ends its own sample (`TestEachRoundTripIsTimedFromItsOwnSegment`). Only
    an ACK that reaches the timed segment's end ends the sample
    (`TestAnACKBeyondTheTimedSegmentMeasuresItsRoundTrip`,
    `TestAnACKShortOfTheTimedSegmentMeasuresNothing`). The RTO keeps a 5 ms margin
    (`TestRTTEstimatorKeepsAMarginOfFiveMilliseconds`).
  - The handshake from ISNs in both halves of sequence space, and at its wrap. A
    peer's SYN without an ACK acknowledges nothing, whatever the ISN
    (`TestAPeersSYNWithoutAnACKAcknowledgesNothingWhateverTheISN`). The SYN is
    timed whatever the ISN (`TestTheSYNIsTimedWhateverTheISN`).
  - Closed and listening sockets. A closed socket accepts nothing, though it keeps
    a listen endpoint or a four-tuple (`TestAClosedSocketAcceptsNothing`).
    `Connect` refuses port 0 on either side. `Listen` on another endpoint while
    listening is refused. Listening on the same endpoint again succeeds and
    changes nothing.
  - Segments the state does not take. Data without an ACK is dropped
    (`TestDataWithoutAnACKIsDropped`). A FIN is dropped once the peer's FIN has
    been taken. Segments at the window's start that CLOSE-WAIT, CLOSING, LAST-ACK
    and TIME-WAIT drop are dropped whole
    (`TestSegmentsTheStateDoesNotTakeAreDropped`). CLOSE-WAIT and CLOSING take a
    segment beyond a hole without its FIN
    (`TestCloseWaitAndClosingTakeASegmentBeyondAHoleWithoutItsFIN`). Out-of-window
    data with FIN or PSH is answered every time
    (`TestRetransmittedDataIsAnsweredEveryTime`).
  - Windows. Window growth is announced in FIN-WAIT-1 and FIN-WAIT-2. With window
    scaling, a window that has doubled is announced at once, compared in the
    field's scaled units (`TestScaledWindowGrowthIsAnnouncedAtOnce`). After the
    peer's FIN, window growth is not announced
    (`TestWindowGrowthIsNotAnnouncedAfterThePeersFIN`). While the peer may still
    send, it is announced in FIN-WAIT-1 and FIN-WAIT-2
    (`TestWindowGrowthIsAnnouncedWhileThePeerMaySend`).
  - SACK. A segment that extends a SACK block is the whole of that block
    (`TestSACKBlockOfASegmentThatExtendsABlockIsTheWholeBlock`).
  - Delayed ACK. More data does not restart the ACK delay
    (`TestMoreDataDoesNotRestartTheACKDelay`). Out-of-order data starts none
    (`TestOutOfOrderDataStartsNoACKDelay`). Data that fills a gap starts one, which
    its immediate ACK leaves running
    (`TestDataThatFillsAGapStartsAnACKDelayItsImmediateACKLeavesRunning`).
  - Closing. CLOSING lasts until the socket's FIN is acknowledged, and the FIN is
    retransmitted meanwhile (`TestClosingLastsUntilItsFINIsAcknowledged`).
    TIME-WAIT lasts 10 s (`TestTimeWaitLastsTenSeconds`). Unread data is discarded
    at the end of TIME-WAIT (`TestTheEndOfTimeWaitDiscardsUnreadData`). After
    LAST-ACK's final ACK there are no endpoints and no RST.
  - Timeouts. The timeout aborts an idle connection, in ESTABLISHED and in
    FIN-WAIT-2 (`TestTimeoutAbortsAnIdleConnection`). A send of nothing keeps its
    clock running (`TestSendingNothingKeepsTheTimeoutClockRunning`).
  - Reuse and reset. An address change empties both buffers
    (`TestLosingTheLocalAddressEmptiesBothBuffers`). A reused socket starts with
    empty buffers and an empty assembler (`TestAReusedSocketStartsWithNothingLeftOver`,
    `TestAReusedSocketReceivesOnlyTheNewConnectionsData`). Every reset forgets the
    listen endpoint, but `Close` keeps it (`TestTheListenEndpointIsForgottenWhenTheSocketResets`).
  - Probes and retransmission. Data queued while probing keeps the probe schedule
    (`TestDataQueuedWhileProbingKeepsTheProbeSchedule`). A segment whose challenge
    ACK the rate limit withholds is still dropped
    (`TestASegmentWhoseChallengeACKIsWithheldIsDropped`). The challenge ACK rate
    limit is one per second (`TestChallengeACKsAreLimitedToOnePerSecond`).
    After enough backoffs in a row, the RTT estimator forgets its samples
    (`TestRTTEstimatorStartsAfreshAfterThreeBackoffs`).
  - Keep-alive. It resumes once everything sent is acknowledged
    (`TestKeepAliveResumesOnceEverythingSentIsAcknowledged`), when the probed
    window opens (`TestKeepAliveResumesWhenTheProbedWindowOpens`), and after a
    retransmission timeout that cannot resend
    (`TestKeepAliveResumesAfterARetransmissionTimeoutIntoAClosedWindow`).
  - A RST's data is never queued, in ESTABLISHED or in a SYN-RECEIVED that came
    from LISTEN (`TestTheDataOfARSTIsNotQueued`). SYN-RECEIVED takes no data before
    its SYN|ACK announces a window (`TestNoDataIsTakenBeforeTheSYNACKAnnouncesAWindow`).
    Data that arrives with a SYN is neither taken nor acknowledged, in a passive
    or an active open, and is taken when the peer sends it again
    (`TestDataArrivingWithASYNIsNeitherTakenNorAcknowledged`).
  - Fast retransmission. A fast retransmission of the last data ends it as its
    first transmission did, with PSH or with the FIN once closed
    (`TestAFastRetransmissionOfTheLastDataEndsItAsItsFirstTransmissionDid`).
  - The deviations of section 9 each have a test
    (`TestTheWindowOfASYNReachesNoFurtherThanTheBuffer`,
    `TestAPeersSYNBeforeTheSocketsOwnIsAnsweredAtOnce`,
    `TestASYNACKToASocketWithoutAReceiveBufferIsAcknowledgedAtOnce`,
    `TestAFINAfterDataCutAtTheWindowsEndWaitsForTheRest`,
    `TestAFINAfterDataCutAtTheWindowsEndIsIgnoredInEveryStateThatTakesAFIN`,
    `TestAWindowThatEndsBeforeItStartsTakesNoSegmentHalfTheSequenceSpaceAway`,
    `TestAWindowThatEndsBeforeItStartsAnswersASegmentAtItsStartWithAChallengeACK`).
- **Pull-model tests** (`pull_model_test.go`) check what `Dispatch` leaves undone
  when `emit` refuses a segment, and what it keeps:
  - a refused segment goes out unchanged at the next `Dispatch`, at the same
    time (`TestRefusedSegmentGoesOutUnchangedAtTheNextDispatch`);
  - a retransmission timeout that fires while `emit` is refused backs off once
    (`TestRetransmissionTimeoutWhileRefusedBacksOffOnce`);
  - a refused SYN still starts the timeout clock. With a 10 s timeout, a SYN
    refused at 0 s and granted at 8 s aborts at 10 s, not at 18 s
    (`TestRefusedSYNStartsTheTimeoutClock`);
  - a timeout makes `State()` report CLOSED at once, even while `emit` is
    refused, and the RST goes out at the next `Dispatch`
    (`TestTimeoutClosesAtOnceWhileEmitIsRefused`);
  - a refused fast retransmission is not repeated (`TestRefusedFastRetransmissionIsNotRepeated`);
  - data refused by `emit` is timed from its transmission
    (`TestRefusedDataIsTimedFromItsTransmission`).
- **`State.String`** and the behaviour section 8 says the consumers rely on.
- **The fuzz target** (`fuzz_test.go`), `FuzzSocketAgainstAHostilePeer`, plays
  a hostile peer against one socket. The peer sends any segments, of which only
  those `Accepts` takes matter. The application sends, receives, peeks, closes,
  aborts and reopens at any moment. The interface polls over a line that
  refuses segments at times, and whose address leaves and returns. The socket
  is set up as a config octet says: the buffers, one of them a 128 KiB receive
  buffer that needs a window scale; listen or connect; Nagle, the ACK delay,
  the timeout and the keep-alive; and initial sequence numbers in the lower
  half of sequence space (`localSeq`), or in the upper half (`configUpperHalfISN`).
  The peer's segments may also start about half the sequence space past the
  receive window's start (`peerFlagHalfSpaceSeq`). The run keeps those out of
  its account of the peer's stream, which compares modulo 2^32.

  The run checks, besides never panicking and `PollAt` working in every state:
  the application reads only the peer's octets, at their sequence numbers, up to
  the FIN; a FIN is taken only from a segment that carries one, after every
  octet before it, unless the socket already holds octets beyond it; no
  acknowledgement goes beyond the peer's furthest sequence number, except for a
  RST or one after such a FIN; sent data is the application's, at its sequence
  numbers, no longer than the effective MSS, and within the peer's window
  except for zero-window probes and fast retransmissions; a RST is never
  answered; replies carry no data; and the assembler holds nothing beyond the
  receive buffer.

  `go test` runs its 18 seeds, which are 17 distinct scripts. Among them are the
  scripts for the deviations of section 9, such as
  `peerSYNBeforeTheSocketsOwnScript`, which runs in both halves of sequence space,
  and `farSegmentsAfterAFINAtTheWindowsEndScript`. To fuzz it, run:

      go test -run='^$' -fuzz='^FuzzSocketAgainstAHostilePeer$' -fuzztime=60s -parallel=2 ./internal/ipstack/tcp

  The status line's execution count pauses while the engine minimizes new corpus
  entries. `-fuzzminimizetime=0` avoids the pause.

### `udp`

- **Scenario tests** (`socket_test.go`) cover binding (twice, and to an
  unaddressable endpoint), sending with and without a source address, dispatch,
  receiving and peeking, truncated slices, datagrams to the wrong port or
  address, a large datagram, an empty payload, and closing.
- **Go-specific**: `Bind` refuses an address that is not IPv4. `Send`, `SendWith`
  and `SendSlice` refuse a destination that is not IPv4 (an IPv6 address, an
  IPv4-mapped one, or the zero `Addr` with a port), and a `LocalAddress` that is
  set but not IPv4. All of them return `ErrUnaddressable` and queue nothing.
  `Send` and `SendWith` panic on a negative size before any other check, so an
  unbound socket panics too. `SendWith` does not call its callback when the
  datagram does not fit. A datagram with no source address is dropped, after
  it is dequeued. The socket accepts broadcasts and multicasts on an
  address-bound socket. The capacities and queue lengths are tested, padding
  included.

### `dns`

smoltcp has no tests of its DNS socket, so every DNS test is Go-specific. They
cover:

- `StartQuery` validation: an empty name, empty labels, a 64-octet label, names
  of 255 and 256 octets, and a trailing dot.
- The first query. It asks for recursion, to port 53. `StartQuery` draws the
  transaction id (`Uint16`) and then the source port (`SourcePort`). With a
  fresh seed-0 context the first query has id 0x3 and source port 1358.
- Retransmission. A server gets the query at 0, 1, 3 and 7 s after its first
  attempt: the delay starts at 1 s and doubles, capped at 10 s. At the first
  `Dispatch` more than 10 s after that attempt, the next server gets the query at
  once, with the delay back at 1 s. After the last server, the query fails.
- The 10 s server timeout runs from the first attempt even when `emit` refused
  it. A refused retransmission is sent at the next `Dispatch`, with the same
  delay.
- Failure on NXDOMAIN, which fails every query the response matches, and on a
  response with no address.
- Keeping at most `MaxResultCount` addresses.
- CNAME following. The CNAME's target becomes the query's name.
- Ignoring a response with a mismatched id or port, a non-response, another
  opcode, two questions, a different question, or a malformed message or answer.
- `TakeQueryResult` ends the query, and the slot and handle are reused.
- `CancelQuery`, and the panics of `TakeQueryResult` and `CancelQuery` on a spent
  handle and on the zero `QueryHandle`, each checked against its text.
- `NewSocket` and `UpdateServers` keep the first two servers. A query in progress
  continues with the new ones. The zero `Socket` is a resolver without servers
  (`TestZeroSocketIsAResolverWithoutServers`).
- Responses from port 5353 are accepted (section 3).
- An unspecified server, a server that is not IPv4, no server, or no source
  address fails the query.
- `PollAt`.
- The unexported name helpers: `encodeName`, `copyName` and `namesEqual`.

### `ipstack`

- **Interface tests** (`interface_test.go`) check that the interface answers TCP
  and UDP the way smoltcp's interface does, on IPv4 only. `TestTCPNotAccepted`
  checks that an unmatched SYN gets RST|ACK, and a segment to 0.0.0.0 gets no
  reply. The ICMP error tests cover the no-payload and port-unreachable cases,
  and the size of the reply. Tests of the omitted features are not included
  (section 3). The local subnet broadcast and the source-address tests are
  `socket.Context` tests.
- **Socket set** (`socket_set_test.go`): smoltcp has no tests here. The
  Go-specific tests cover reuse of the lowest free slot; `String` (`#0`, and
  `#invalid` for the zero handle); the typed accessors, which panic on a wrong
  kind or an invalid handle; `Remove`; the iteration order; iterators that stop
  when the loop does; and the zero `SocketSet`.
- **Go-specific interface tests.** These are the pull-model guarantees that the
  consumers rely on (section 8):
  - When `Transmit` refuses, egress stops, and the socket's segment goes out
    unchanged at the next poll.
  - The reply slot of a received datagram is used even while `Transmit` refuses.
  - The first accepting TCP socket in slot order gets the segment, and UDP
    sockets come before DNS sockets (`TestUDPSocketsComeBeforeDNSSockets`).
  - Any-IP accepts every destination. Without it, a datagram for a foreign
    address is dropped.
  - An echo request gets no reply. An unknown protocol gets protocol unreachable.
  - An outgoing datagram larger than the MTU is dropped after its transmit slot
    is taken, and counts as sent, so a UDP socket dequeues it. An incoming
    datagram of that size is processed.
  - An empty frame counts as processed (`IngressPacketProcessed`). Every other
    datagram counts as `IngressSocketStateChanged`, even one that is dropped.
  - `PollIngressSingle` processes exactly one datagram and sends only its reply,
    through the reply slot. It never calls `Transmit`. It returns `IngressNone`
    when nothing is waiting.
  - A source-address change resets TCP connections without a RST.
  - A seed gives the sockets the random values that smoltcp gives for the same
    seed. After `NewInterface`, the first `Uint32` is the first ISN for seed 0
    (0xfdecf8a2) and for seed 0x5EED (0x9ee15bb3), and a seed-0 interface's first
    DNS query has id 0x54e and port 57331
    (`TestSeedDeterminesTheSocketsRandomValues`,
    `TestFragmentIdentifierIsTheFirstUint16ThatIsNotZero`). The DNS tests, which
    use `socket.NewContext` alone, see id 0x3 and port 1358.
  - No ICMP error answers a datagram for a subnet broadcast of either of the
    interface's networks, the limited broadcast, 224.0.0.1, or, with any-IP, a
    foreign group. This holds for UDP to a port without a socket and for an unknown
    protocol (`TestNoICMPErrorAnswersADatagramForAGroupOrABroadcast`).
  - `PollAt` and `PollDelay`. A new DNS query's deadline lies in the past, and
    `PollAt` returns it as due. Every poll sets the context's time.
  - `NewInterface` panics below `MinMTU` (`TestNewInterfacePanicsBelowMinMTU`).
  - `SetIPAddrs` returns errors for bad addresses.
  - Malformed datagrams, non-unicast sources and foreign multicast groups are
    dropped. Datagrams from 0.0.0.0, and datagrams for the interface's broadcasts
    and for 224.0.0.1, are accepted.
  - `Example`: a UDP echo server on an interface.

### End to end: the loopback tests

`loopback_test.go` (package `ipstack_test`, exported API only) joins two
interfaces with an in-memory line. The line stands in for the PPP link between the
two ends.

- The client is the app's end, set up as satnet sets it up. It has one /32
  address, 10.64.0.2, and is polled with `Poll`. Its TCP sockets are tuned as
  satnet tunes them: 2048-octet receive and 4096-octet send buffers, Nagle off, a
  100 ms ACK delay, a 120 s timeout and a 60 s keep-alive.
- The other end is the simulated internet, set up as sim sets it up. It has the
  gateway's /32 address, 10.64.0.1, and any-IP, so it answers for every
  destination. It is polled one datagram at a time with `PollIngressSingle`, and
  then with `Poll`. Its services have 4096-octet buffers and the stack's defaults.
- Both devices work in the pull model. Each received datagram comes with a reply
  slot that always takes the answer. Transmit slots are granted only while the
  line is free.
- The line records every datagram, and can be made busy or can lose chosen
  datagrams. The clock is simulated. It jumps to the next socket timer, or moves by
  the driver's poll interval, 20 ms, while a socket waits for a busy line. The
  tests therefore run in milliseconds.
- The MTU is 576 octets, `wire.IPv4MinMTU`.

The tests are:

- **Bulk data both ways, and an orderly close.** 20 000 octets go from the client
  and 30 000 from the internet over one connection. Each end reads exactly what the
  other sent. Every segment from the internet comes from the address the client
  wrote to. There is no RST. The client's FIN goes first, and the client stays in
  TIME-WAIT for 10 s before it closes (`TestLoopbackTCPCarriesBulkDataBothWaysAndClosesInOrder`).
- **Recovery from a lost segment**, for each of: the client's SYN, the internet's
  SYN|ACK, a data segment from each end, each end's FIN, and the client's ACK of
  the internet's FIN. The data still arrives intact, and the sender of the lost
  segment sends it again (`TestLoopbackTCPRecoversFromALostSegment`).
- **A busy transmit line.** The client's line is busy for 3 s, which is longer
  than the initial RTO, in the middle of the bulk exchange. The device refuses
  transmit slots, and the client sends nothing until the line is free, but it still
  answers through reply slots. The exchange completes intact
  (`TestLoopbackBusyLineHoldsBackTheClientsTransmissionsButNotItsReplies`).
- **UDP both ways.** Datagrams to the simulator's UDP echo on two hosts arrive
  with the client's endpoint and the destination as their local address. The
  echoes come back from those destinations
  (`TestLoopbackUDPEchoAnswersFromTheAddressTheClientWroteTo`).
- **DNS.** A resolver asks the gateway, and a DNS responder built by hand on a UDP
  socket of the internet answers. The answers are one A record, two (for a name
  with a trailing dot), an alias through its CNAME record, and NXDOMAIN, which
  fails the query. The server is asked once, with recursion desired
  (`TestLoopbackDNSQueryIsAnsweredByAHandBuiltResponder`).
- **The fallbacks.** A UDP datagram to a port without a socket gets ICMP port
  unreachable through the reply slot, from the destination it was sent to, and
  quoting it. Nothing answers the error
  (`TestLoopbackUDPToAPortWithoutASocketGetsPortUnreachable`). A SYN to a port
  without a listener gets RST|ACK through the reply slot. The client's connection
  is refused, and it is CLOSED with no timer left
  (`TestLoopbackTCPToAPortWithoutAListenerGetsRST`).

### Not covered elsewhere

These tests of smoltcp compile with the stack's feature set, but they test what
the stack replaces or leaves out, so the stack has no equivalent of them:

- The address and prefix parsers: `net/netip`'s `ParseAddr`, `ParsePrefix` and
  `ParseAddrPort` replace them.
- The time tests: package `time` replaces smoltcp's `Instant` and `Duration`.
- The neighbour metadata tests: the metadata is omitted on `Medium::Ip`
  (section 3).
- The route table test: routes are omitted (section 3).
- The reassembly buffer tests: the reassembly buffer is omitted with fragmentation
  (section 3).
- The tracer tests: the phy middleware is omitted (section 3).
- The hardware address parser: its cases are for Ethernet and IEEE 802.15.4 only,
  so none is compiled.

### Coverage

The statement coverage below comes from `go test -race -count=1 -cover ./internal/ipstack/...`,
run on the current tree:

| package | coverage |
|---|---|
| `ipstack` | 100.0% |
| `wire` | 100.0% |
| `storage` | 99.6% |
| `socket` | 100.0% |
| `tcp` | 99.1% |
| `udp` | 100.0% |
| `dns` | 100.0% |
| `internal/netlog` | 100.0% |

What is left uncovered is defensive code that no test can reach through the API.
In `storage`, the error of `enqueuePadding` for a full metadata ring is kept, so
that no error is ignored silently. `admits` rules it out. In `tcp`, the guards for
states that cannot occur are kept: a listening socket's transmit, the SYN term of
the ACK range check, an ACK below the transmit buffer, a non-SYN transition from
SYN-SENT, a RST in LISTEN, and the trace of a flag combination that no segment has.
The `NewSocket` panic for a receive buffer over 1 GiB is the other case. Only
allocating 1 GiB could reach it.

## 7. Pitfalls

These are the mistakes that are easy to make in this package.

- `TCPRepr` is not comparable, because its payload is a slice. Use `Equal`.
- `time.Time` values compare with `==` only when they were built the same way.
  Tests should derive every time from one epoch. `PollAt.Equal` uses
  `time.Time.Equal`.
- `TCPSeqNumber` comparisons must go through `Compare`, because sequence numbers
  wrap. Plain `<` on the underlying `int32` is a bug.
- `Optional` is generic over comparable `T`. `Optional[SACKRange]` and
  `Optional[TCPTimestamp]` are comparable because those structs are.
- `TCPRepr.Emit` and `UDPRepr.Emit` write into a buffer that must be exactly
  `BufferLen` octets long. A longer or shorter buffer is an error.
- A UDP datagram may be sent with checksum 0 (none) over IPv4. A computed checksum
  of zero is sent as 0xffff, and `FillChecksum` does the same.
- `netip.Addr{}.IsUnspecified()` is false for the zero `Addr`. Where an address may
  be the zero `Addr`, check `Is4` first (section 4).
- Slices that `storage` hands out have a capacity equal to their length.
  `append` on one reallocates rather than writing into the storage that follows
  it.
- Logging goes through `internal/ipstack/internal/netlog` (`Trace` and `Debug`).
  Pass values as slog attributes, not formatted into the message. A disabled
  `Trace` or `Debug` call still builds its arguments. An address or a
  representation costs an allocation per call even when the level is off, so
  keep such calls out of loops over octets. netlog takes its trace level and
  record key from `internal/logtext` (`LevelTrace`, `PackageKey`), as the module's
  other logging packages do. The level is four below `slog.LevelDebug`, and
  `pkg` is `ipstack` on every record.
- The payload slices that `storage.PacketBuffer`, `udp.Socket` and `tcp.Socket`
  hand out (`Send`, `Receive`, `Peek`, and the slice that a `SendWith` callback
  fills) alias the ring storage. They are valid only until the buffer or socket
  is next used, and the interface's next poll counts as a use. `Dispatch` may send
  a `Send` payload as it then is. `Process` may write a later datagram over a
  received payload: a TCP reset empties the receive buffer, and the next
  connection then fills it. Fill a `Send` payload at once, and copy a received
  payload before using the socket again, even to echo it with `SendSlice`. The
  simulator's services clone what they keep. Go does not enforce this rule, so
  the caller must.

## 8. Behaviour the consumers depend on

The consumers are satnet and sim. The stack must keep the behaviour below, and
the tests named in section 6 pin it. The loopback tests exercise it end to end.

**Device and polling (the pull model)**

- `Receive` hands over a datagram together with a reply slot, and the stack may
  answer at once through that slot. The satnet device makes the reply slot always
  usable, even when the line is busy.
- Egress asks the device for a transmit slot only when a socket has a packet
  ready.
  - A refusal (`Transmit` returning false) ends the egress round. `Poll` runs
    another round only if this one sent something, and that round asks the device
    again.
  - The socket that was refused skips what follows its `emit` call. It records
    nothing as sent, and keeps what it decided before emitting: TCP's timeout
    clock, timeout abort and retransmission backoff, and the DNS server timeout
    (section 5, contract 3). On a busy line this matters. A timed-out connection
    reports CLOSED at once, and satnet's connect wait and its removal of closing
    sockets depend on that.
  - The next poll makes its choice again. The satnet device grants a slot
    whenever it is asked, and chooses among the datagrams it is handed.
- `Poll` processes every received datagram, then runs egress rounds until a round
  sends nothing.
- `PollIngressSingle` processes one datagram, and sends only that datagram's
  reply. The simulator calls it once per datagram, so each connection request in
  a burst finds a listener prepared for it.
- `PollAt` returns the current time when a socket wants a poll at once. It
  returns the earliest timer deadline when a socket has a timer, and reports no
  deadline when every socket waits for ingress. `PollDelay` returns the time from
  now until `PollAt`, or zero if that time has passed. For a socket that wants a
  poll at once, `PollAt` returns `now` itself (section 5, contract 4).
- The MTU is read once, in `NewInterface`. A consumer that changes the MTU makes a
  new interface. satnet does this when the link's MTU changes.
- A datagram larger than the IP MTU is dropped without an error, after the device
  has granted its slot. It counts as sent: the socket dequeues it, and the round
  counts as having sent something. TCP never builds one, because its MSS comes
  from the IP MTU (`TestEstablishedLocalMSSLimitsSegments`, `TestLocalMSSFollowsTheIPMTU`).
  A UDP payload larger than the MTU allows is dropped this way, which is why satnet
  checks the largest UDP payload first.
- Received datagrams are never checked against the MTU. A consumer may let the
  peer send datagrams larger than the stack's MTU, and the stack processes them.

**Addresses**

- `SetIPAddrs` returns an error for an address that is not an IPv4 unicast or
  unspecified address, where smoltcp's `update_ip_addrs` panics.
- Any-IP accepts datagrams for every address. The simulated internet answers for
  every destination with it.
  - Replies use the destination address of the datagram they answer (the TCP
    tuple, or a UDP datagram's `LocalAddress`), not the interface's address.
- Without any-IP, a datagram for an address that is not the interface's own is
  dropped. The exceptions are the limited broadcast, subnet broadcasts, and
  224.0.0.1.
- A datagram with a non-unicast source is dropped, except one from 0.0.0.0.
- A TCP socket resets silently when its local address leaves the interface. It
  empties its send buffer as if the peer had acknowledged everything, and both
  buffers are emptied (`TestLosingTheLocalAddressEmptiesBothBuffers`). satnet
  relies on this. When the PPP link renegotiates an address, satnet records the
  connections that still had data on their way as cut off. Its `WaitAcknowledged`
  then reports them reset, not delivered.

**Socket set**

- `AddTCP`, `AddUDP` and `AddDNS` put the socket in the lowest free slot.
  - A removed handle is therefore reused. satnet keeps sockets in the set until
    their owners let go, so that no handle is reused under them.
  - The simulator forgets closed connections before it adds listeners.
- The typed accessors panic on a wrong kind or a free slot. `Remove` panics on a
  free slot.
- A TCP segment goes to the first TCP socket in slot order that accepts it.
  - A listener accepts a SYN for its port from anyone
    (`TestListenerAcceptsASYNForItsPortFromAnyone`).
  - The simulator relies on this order. It drops repeated SYNs for existing
    connections before it polls, because a fresh listener in an earlier slot would
    accept the repeat.
- A segment with an unspecified source or destination address is dropped before
  any socket sees it. A segment that no socket accepts gets a RST, unless it is
  itself a RST. The RST carries RST|ACK for a SYN without an ACK.
- A UDP datagram goes to the first UDP socket that accepts it, then to the DNS
  sockets. If none accepts it, it gets ICMP port unreachable, but only when both
  its addresses are unicast. The ICMP error quotes at most 528 octets of the
  datagram.
- A datagram of another protocol gets ICMP protocol unreachable. ICMP messages
  themselves, including echo requests, are ignored (section 1).

**TCP**

- The state names are the `State` values, such as `StateCloseWait` and
  `StateTimeWait`. The consumers rely on these meanings:
  - `StateClosed` means finished, and such a socket can be removed.
    `StateTimeWait` is over only once the socket reaches `StateClosed`, so the
    final ACK still goes out.
  - `StateSynSent` and `StateSynReceived` mean connecting.
  - `StateClosed` or `StateTimeWait` before `StateEstablished` means refused, timed
    out or cut off.
  - The simulator's services look for `StateCloseWait` with nothing left to read.
- `ReceiveSlice`:
  - returns 0 and no error when nothing is buffered;
  - returns `ErrFinished` once the peer's FIN is reached and every octet has been
    read: the end of the stream;
  - returns `ErrInvalidState` otherwise, when the receive half is closed. That is
    a lost connection. When this side closed first, the end of TIME-WAIT resets the
    socket, which discards unread data. `ReceiveSlice` then returns
    `ErrInvalidState`, not `ErrFinished`
    (`TestTheEndOfTimeWaitDiscardsUnreadData`). satnet's `TCPConn.Read` fails with
    `ErrConnectionReset` in that case.
  - returns `ErrInvalidState` before the connection is established.
- `SendSlice` returns `ErrInvalidState` unless the transmit half is open, that is,
  in ESTABLISHED or CLOSE-WAIT. It returns 0 when the buffer is full.
- `SendQueue` counts the octets queued and not yet acknowledged. satnet's
  "undelivered data" is its outbox (what the app wrote and the socket has yet to
  take) together with `SendQueue`. While `IsActive`, that total is above zero.
  `WaitAcknowledged` waits for both to be empty while `IsOpen`, and fails for a
  connection cut off by an address change. A drop in `SendQueue` during a poll is
  what satnet reports to its device as acknowledged. The count drops only for an
  acknowledgement the stack has accepted, not for a segment it ignored or found
  corrupt.
- `Close` sends a FIN after the queued data. `Abort` sends one RST and closes.
- The predicates:
  - `IsOpen`: neither CLOSED nor TIME-WAIT.
  - `IsActive`: additionally not LISTEN.
  - `MayReceive`: ESTABLISHED, FIN-WAIT-1, FIN-WAIT-2, or data still buffered.
  - `CanReceive`: data is buffered.
  - `CanSend`: `MaySend` (ESTABLISHED or CLOSE-WAIT), and the buffer is not full.
  - `IsListening`.
- `Listen` on the endpoint the socket is already listening on succeeds and changes
  nothing.
- `LocalEndpoint` and `RemoteEndpoint` return the four-tuple. It survives `Abort`,
  a timeout, and `Close` in SYN-SENT, until the RST has been sent. It also stays
  in TIME-WAIT. satnet's `freePort` treats such ports as in use, and the
  simulator's `hasConnectionFor` matches repeated SYNs against such sockets.
- `Connect` takes the local port explicitly, and chooses the local address with
  `SourceAddress`.
- The defaults are: Nagle on, an ACK delay of 10 ms, no timeout, and no keep-alive.
  satnet turns Nagle off, sets a 100 ms ACK delay, and sets its timeout and its
  keep-alive. It drops the keep-alive on close. Dropping a keep-alive leaves one
  that is already timed in place, and that one goes out only if the socket sends
  nothing before it is due (`TestTurningKeepAliveOffLetsTheKeepAliveAlreadyTimedGo`).
- The timeout aborts with a RST once the peer has been silent for the timeout,
  whatever the socket is doing. That covers waiting for an answer, and sitting
  idle with keep-alive off and nothing queued, in FIN-WAIT-2 too. The silence is
  counted from the last segment received, or after a quiet spell (a connect, or
  data queued into an empty buffer) from the next dispatch. satnet relies on this.
  Closing drops keep-alive, so a peer that never says goodbye is timed out
  (`TestTimeoutAbortsAnIdleConnection`). Section 9 notes where smoltcp's own
  documentation of this says otherwise.
- Retransmission: the initial RTO is 1 s, the minimum is 1 s and the maximum is
  60 s, with exponential backoff. satnet's repeat filter exists because a
  satellite round trip is longer than that initial RTO.
- Data that arrives with a SYN is neither taken nor acknowledged, so the peer sends
  it again. The stack takes no payload in LISTEN or SYN-SENT, and drops a SYN once
  the connection is synchronised. TCP Fast Open is not implemented
  (`TestDataArrivingWithASYNIsNeitherTakenNorAcknowledged`).

**UDP**

- `Bind` fails for port 0 or for a socket that is already bound, and for an address
  that is not IPv4 (section 4).
- `SendSlice` returns `ErrUnaddressable` when the socket is unbound, or the
  destination is unspecified or has port 0, or (in Go) the address is not IPv4. It
  returns `ErrBufferFull` when the datagram does not fit, now or ever.
- `Receive` returns `ErrExhausted` when no datagram is waiting. Each received
  datagram carries the sender as `Metadata.Endpoint`, and the address it was sent to
  as `Metadata.LocalAddress`.
- A queued datagram goes out from its `LocalAddress` if that is set, else from the
  bound address, else from the address `SourceAddress` chooses for the destination.
  A datagram with no such address is dropped. The simulator's echo and DNS services
  send the received metadata back, so their answers come from the address the
  client wrote to.
- `SendQueue` counts the payload octets still queued, padding included. satnet
  removes a closing UDP socket once it reaches 0.

**DNS**

- Servers are truncated to `MaxServerCount` (2). `StartQuery` needs no server. With
  none, the query fails at its first dispatch. satnet refuses to start a lookup
  when PPP gave no server.
- `StartQuery` strips a trailing dot. It rejects an empty name, or an empty label,
  or a label over 63 octets, with `ErrInvalidName`. It rejects a name over 255
  octets with `ErrNameTooLong`. It draws the transaction id, and then the source
  port, from the random source, in that order.
- `TakeQueryResult` returns `ErrPending` while the query has no answer. Otherwise
  it frees the slot, and returns at most `MaxResultCount` addresses, or `ErrFailed`.
  The handle is then spent. satnet cancels only a query that is still pending.
  `CancelQuery` panics on a freed slot.
- The DNS socket accepts responses from port 53 of a configured server, and from
  port 5353 of anyone. Section 3 describes the cost of that.
  - An NXDOMAIN response fails the query, and so does a response with no matching
    address.
  - A query fails the same way whether a server says the name has no address
    (NXDOMAIN, or no A record), answers with a failure such as SERVFAIL, or stays
    silent. satnet tells these apart itself. It watches the datagrams that arrive
    for a kept server's answer about the name, before the stack sees them, and
    reports a missing name only after one.
  - A CNAME is followed within the same response.
- A server gets the query at 0, 1, 3 and 7 s after the first attempt. The delay
  starts at 1 s and doubles, capped at 10 s. The first dispatch more than 10 s after
  the first attempt moves the query to the next server, at once, with the delay back
  at 1 s. After the last server, the query fails. The timeout is not part of
  `PollAt`, which returns only the next retransmission time. satnet's repeat filter
  drops DNS retransmissions sent before an answer could arrive. It recognises them by
  their unchanged transaction id.

**Wire**

The consumers use these wire functions directly: `NewIPv4Packet`, `NextHeader`,
`SrcAddr`, `DstAddr`, `Payload` and `VerifyChecksum` on the IPv4 packet;
`NewTCPPacket`, the `SYN`, `FIN`, `ACK` and `RST` flags, `SrcPort`, `DstPort`,
`SeqNumber`, `AckNumber`, `WindowLen` and `Payload`, and, for a segment brought up
to date, `SetAckNumber`, `SetWindowLen` and `FillChecksum`; `NewUDPPacket`,
`SrcPort`, `DstPort`, `VerifyChecksum` and `Payload`; the DNS header accessors
`Flags`, `Opcode`, `Rcode` and `QuestionCount`; and `NameLabels`,
`ParseDNSQuestion`, `ParseDNSRecord`, the `Address` of an A record, `DNSRcode` and
`DNSOpcode`.

The checked constructors and parsers never panic on arbitrary bytes. The fuzz
targets in section 6 check this for every `New…` constructor and `Parse…` function.

## 9. Where the stack deviates from smoltcp

The stack follows smoltcp's behaviour: the same checks in the same order, the
same random draws in the same order, the same timers and the same quirks. Its
long functions are split into smaller ones, and its modules into files of one
concept each (section 2), without changing what they do. The stack departs from
smoltcp only where Go's types allow what smoltcp's types rule out, where Go has no
counterpart, where smoltcp's text would mislead in Go, or where smoltcp mishandles
a segment. Mishandling means panicking, or giving the application a stream other
than the one the peer sent. The `tcp` package doc names the same four kinds of
segment. smoltcp's other defects stay, and the last subsection lists them.

**Segments smoltcp mishandles**

- **The window of a SYN.** A SYN announces its window unscaled, yet smoltcp reads
  every window it sends with its own scale, and it never clamps that window to the
  receive buffer. With a receive buffer that needs a scale (64 KiB or more, which
  satnet's `TCPReceiveBuffer` allows; satnet's default of 2048 octets and sim's 4096
  need none), the window of the socket's SYN or SYN|ACK reaches far past the buffer
  until the socket's next segment. smoltcp takes data up to there. Queuing that data
  panics, or, with reads in between, the application gets octets the peer never
  sent. The stack cuts the window back to the buffer's free space (`locateReceiveWindow`).
  That binds only in this case (`TestTheWindowOfASYNReachesNoFurtherThanTheBuffer`).
  A probe against smoltcp 0.14.0 through its public `Interface` shows the failure.
  With a 128 KiB buffer, a 6-octet segment at offset 131072 after a passive open
  fails a debug assertion. So does a server's data that comes with its SYN|ACK in an
  active open. Two in-order segments of 60 000 octets into a 64 KiB buffer panic in
  a release build.
- **A peer's SYN before the socket's own.** A peer's SYN can reach a connecting
  socket before the socket's own SYN has gone out, because it has not been
  dispatched yet, or `emit` refused it. The socket has then announced no window, and
  its last acknowledgement number is the peer's sequence number. Computing the last
  scaled window then subtracts past zero, and smoltcp panics in `poll_at` (a
  `SeqNumber` underflow). The stack's `lastScaledWindow` counts no window left and
  returns 0, so the socket's SYN|ACK is due at once, which its dispatch did anyway
  (`TestAPeersSYNBeforeTheSocketsOwnIsAnsweredAtOnce`). A SYN|ACK that answers a SYN
  which announced a zero window, from a socket without a receive buffer, leaves the
  same state, and smoltcp's `poll_at` panics the same way. The stack's `PollAt` says
  the ACK is due at once (`TestASYNACKToASocketWithoutAReceiveBufferIsAcknowledgedAtOnce`).
  A FIN at the window's end (the next item) does not lead to this panic.

  smoltcp's `dispatch` sends an ACK before it computes the window, whether or not
  `emit` succeeds, so the panic is only in `poll_at`, and only when an application
  calls it (or `poll_delay`) between the peer's SYN and the next successful dispatch.
  The consumers call only `Poll` and `PollIngressSingle`, and they never reach the
  panic. The stack keeps its `PollAt` from panicking anyway, because its own tests and
  fuzz target call it.
- **A receive window that ends before it starts.** The check of a segment against
  the receive window runs from the next expected sequence number to the end of the
  last window announced. It can end one sequence number before it starts. That
  happens when the peer's SYN or FIN takes the first sequence number beyond the
  window last announced. The state lasts until the socket's next segment announces a
  window afresh. A SYN does this when it reaches a connecting socket before the
  socket's own SYN has announced a window, or when it answers a SYN that announced a
  zero window (a receive buffer of no octets). A FIN does it when it lands right at
  the window's end (a full window, or a zero window). That needs no race, and it
  lasts until the socket's ACK of the FIN goes out.

  The acceptability comparisons are modulo 2^32, where of two numbers 2^31 apart each
  compares as before the other. So such a window also accepts some segments that
  start or end about half the sequence space away. smoltcp then panics as it cuts
  the payload. That happens on an empty segment at either place, on data that ends
  2^31 − 1 after the start, and on data that starts 2^31 after it. The data that
  smoltcp places too far from the start (2^31 − 1 after it, or ending 2^31 after it)
  overflows the assembler, and a SACK block overflows if the peer allowed SACK. A RST
  carrying such data also ends the connection, because a RST needs no acknowledgement
  number, so its sender needs only the peer's sequence numbers. With smoltcp's debug
  assertions on, as in a debug build, those assertions fire first. Probes against
  smoltcp 0.14.0 show each case. They use a 6-octet window with a FIN and a peer that
  allows SACK, and a 64-octet window with data and a FIN and no SACK.

  The stack finds every segment outside such a window (`windowEndsBeforeItStarts`).
  It drops a RST and answers anything else with an ACK, as smoltcp answers any
  segment outside its window. Only segments half the sequence space away differ. An
  empty segment at the window's start still gets a challenge ACK, and the socket takes
  nothing from it
  (`TestAWindowThatEndsBeforeItStartsTakesNoSegmentHalfTheSequenceSpaceAway`,
  `TestAWindowThatEndsBeforeItStartsAnswersASegmentAtItsStartWithAChallengeACK`).
  The RFCs agree that no sequence number lies inside such a window, and that a RST
  outside the window is dropped (RFC 9293, section 3.10.7.4; RFC 5961, section 3.2).
- **A FIN after data that the receive window cut short.** smoltcp 0.14 cuts the
  payload at the lesser of the window's end and the segment's end. It disregards a
  FIN only when the window starts before the segment. So it takes the FIN of a
  segment whose data the window cut short. It ends the stream at the window's end,
  and acknowledges one sequence number past the octets it holds. The peer then counts
  the first cut-off octet as delivered and never sends it again. The peer's
  retransmission of the rest, with its FIN, is a FIN in CLOSE-WAIT, which is dropped as
  unexpected, and the rest is lost. RFC 9293 (section 3.10.7.4) trims a segment to the
  window "including SYN and FIN". Upstream history: smoltcp PR #977, in 0.12, narrowed
  the FIN check to `window_start < segment_start`. PR #1079, in 0.13, moved the cut to
  the advertised window's end without revisiting that check.

  In the states that take a FIN (SYN-RECEIVED, ESTABLISHED, FIN-WAIT-1 and FIN-WAIT-2,
  `takesFIN`), the stack ignores that FIN (`effectiveControl`), and the peer sends it
  again with the rest of its data. The comparison is strict, so a FIN right at the
  window's end (a full or zero window) is still taken.

  In CLOSE-WAIT, CLOSING, LAST-ACK and TIME-WAIT, the peer's FIN was taken already.
  There the stack does as smoltcp does: it drops the segment whole, takes neither its
  data, its ACK nor its window, and sends nothing, unless a hole precedes the segment.
  If a hole does precede it, smoltcp's hole check disregards the FIN, as in every
  state, and the state handles the segment as one without a FIN. CLOSE-WAIT and
  CLOSING queue its data in the assembler, take its ACK and window, and answer with an
  ACK (`TestCloseWaitAndClosingTakeASegmentBeyondAHoleWithoutItsFIN`). LAST-ACK answers
  a segment that acknowledges nothing new with a challenge ACK, and closes on one that
  acknowledges its FIN. TIME-WAIT drops it.

  The case arises when a peer sends past the window, at any buffer size, and with
  window scaling, where rounding moves the window's end back while the peer's last
  segment is in flight (`TestAFINAfterDataCutAtTheWindowsEndWaitsForTheRest`,
  `TestAFINAfterDataCutAtTheWindowsEndIsIgnoredInEveryStateThatTakesAFIN`,
  `TestSegmentsTheStateDoesNotTakeAreDropped`). A probe against smoltcp 0.14.0, alone
  and through its `Interface`, shows it. With a 2048-octet receive buffer and a server
  that sends past the window, the socket holds 2048 octets, acknowledges 2049 and
  reports the end of the stream. The 96 octets that the server sends again with its
  FIN are dropped in CLOSE-WAIT as unexpected. With a 128 KiB buffer, and a peer that
  keeps to every window it was told, 3 octets are lost in the same way.

**Inputs that smoltcp's types rule out**

- Only IPv4 addresses enter the stack (section 4). `tcp.Socket.Listen` and
  `Connect`, `udp.Socket.Bind` and the UDP send methods return `ErrUnaddressable`
  for an address that is not IPv4. A DNS server that is not IPv4 fails the query
  when the query reaches it. `SetIPAddrs` returns an error. The wire functions that
  write or checksum an address panic.
- Sizes are signed in Go. `udp.Socket.Send` and `SendWith` panic on a negative size
  before the packet buffer is touched. `storage.PacketBuffer`'s `Enqueue` and
  `EnqueueWith` panic before they change the buffer, since smoltcp's `usize` rules a
  negative size out. The storage containers panic on a count outside their range.
  `tcp.Socket.Send` and `Receive`, and `udp.Socket.SendWith`, panic when their
  callback returns more octets than it was given. smoltcp's slices would panic too.
- `wire.TCPSeqNumber.Add(n)` panics for an `n` below −(2^31 − 1), with smoltcp's
  text for its unsigned overflow ("attempt to subtract to sequence number with
  unsigned overflow"). smoltcp takes the count as an unsigned `usize`.
- `NewInterface` panics for an MTU below `MinMTU`, the size of an IPv4 and a TCP
  header together (40 octets). smoltcp does not check, and its maximum segment size
  arithmetic would underflow below that size.
- The zero `SocketHandle` and the zero `dns.QueryHandle` identify nothing. The
  handle's `String` is `#invalid`. The zero `SocketSet` is an empty set, ready for
  use, and the zero `dns.Socket` is a resolver without servers.

**Errors instead of panics**

- `SetIPAddrs` returns an error wrapping `socket.ErrInvalidIPAddr` where smoltcp's
  `update_ip_addrs` panics (section 4).

**Results**

- `Interface.PollAt` returns `now` itself when a socket wants a poll at once, where
  smoltcp returns `Instant::ZERO`. Both mean "poll at once" (section 8). Like
  smoltcp's, `PollAt` may return a time already past. A DNS query not yet sent is due
  at the epoch, the zero `time.Time`.
- Where smoltcp returns an `Option`, Go returns the value with a `bool`:
  `tcp.Socket.Process`, `socket.Context.SourceAddress` and `Interface.PollAt`. Where
  smoltcp returns a per-operation error enum, Go returns per-package sentinel errors
  for `errors.Is` (section 4).

**Texts**

- Errors keep smoltcp's `Display` texts, except that DNS's start in lower case, and
  `wire.ErrMalformed` says "malformed or unsupported packet", where smoltcp's
  `wire::Error` says "wire::Error" (section 4).
- `String` methods return smoltcp's `Display` text where smoltcp has one. That covers
  `wire.IPProtocol` for every protocol smoltcp names, `ICMPv4Message` for every
  message, `TCPSeqNumber`, `ListenEndpoint` and `tcp.State`. Otherwise they return the
  variant's name: `TCPControl`, `PollResult`, `PollIngressSingleResult`, the DNS enums,
  and the cases of `socket.PollAt` (`Now`, `Ingress` and `Time(…)`). A `Time(…)` shows
  Go's `time.Time` text, as in `Time(0001-01-01 00:00:01.5 +0000 UTC)`. smoltcp's
  derived `Debug` writes `Time(Instant { micros: 1500000 })`. A value without a name
  prints as smoltcp prints it (`Unknown(4)`, and `0x0c` for an `IPProtocol`), or as
  `Type(n)` where smoltcp's type cannot hold the value (`State(9)`, `PollResult(7)`).
  `TestPollAtStringNamesTheVariant` pins the `PollAt` names.
- Log messages keep smoltcp's text. The values are slog attributes, not formatted
  into the text (section 7). Trace messages are at `LevelTrace`, and debug messages
  at `slog.LevelDebug`, with `pkg=ipstack`.
- These texts exist only in Go:

| Package | Situation | Kind | Text |
|---|---|---|---|
| `ipstack` | `NewInterface` with an MTU below `MinMTU` | panic | `ipstack: an MTU of %d is below MinMTU, %d` |
| `ipstack` | a datagram for 0.0.0.0 reaches `dispatchIP` (smoltcp's `assert!` has no text) | panic | `ipstack: a datagram for the unspecified address` |
| `ipstack` | a datagram larger than the IP MTU is dropped | debug log | `fragmentation is not supported, dropping a datagram larger than the IP MTU`, with the length and the MTU. smoltcp's text tells the reader to enable its `proto-ipv4-fragmentation` feature, which the stack does not have. |
| `ipstack` | a malformed datagram is dropped (smoltcp's `check!`) | trace log | `iface: malformed`, with `what` naming the parser |
| `socket` | `SetIPAddrs` of an address an interface cannot have | error wrapping `ErrInvalidIPAddr` | `invalid interface address: IP address %v is not unicast` (smoltcp's panic text), `… is not IPv4`, or `… has an invalid prefix length` |
| `wire` | an address that is not IPv4 is written or checksummed | panic | `wire: %v is not an IPv4 address` |
| `wire` | an `ICMPv4Repr` of a message it does not represent | panic | `wire: ICMPv4Repr does not represent %v (type %d) messages` |
| `storage` | a count outside a container's range | panic | `storage: %s: count %d is outside 0 to %d` |
| `storage` | `PacketBuffer.Enqueue` or `EnqueueWith` of a negative size | panic | `storage: %s: size %d is negative` |
| `tcp` | an ACK reaches `process` in LISTEN (smoltcp's `unreachable!()`) | panic | `tcp: Process given a segment with an ACK in LISTEN, which Accepts refuses` |
| `tcp` | a FIN that follows data the receive window cut off is ignored | trace log | `ignoring FIN because its data was cut at the receive window's end` |
| `udp` | `Send` or `SendWith` of a negative size | panic | `udp: %s: size %d is negative` |
| `dns` | `TakeQueryResult` of a spent or zero handle (smoltcp unwraps a `None`) | panic | `Getting the result of a query in a free slot.` |
| `dns` | `CancelQuery` of a spent or zero handle | panic | `Canceling query in a free slot.` |

**smoltcp's defects that the stack keeps**

These are defects of smoltcp 0.14, which the stack keeps. Each keeps the stack's
behaviour the same as smoltcp's. Changing one would change the stack's timers or its
closing behaviour, and so needs a decision of its own, a new entry in this section,
and tests.

- **Close in SYN-RECEIVED** moves the socket to FIN-WAIT-1, which never sends a
  SYN|ACK again. If the SYN|ACK was already in flight, the sent-SYN flag is false in
  FIN-WAIT-1. The peer's ACK of the SYN|ACK then passes for the ACK of the FIN, and
  the socket reaches FIN-WAIT-2 without its FIN ever going out. If the SYN|ACK had not
  gone out yet, the socket sends a bare FIN instead.
- **A FIN left alone by three duplicate ACKs.** If three duplicate ACKs arrive while
  only the socket's FIN is outstanding (in FIN-WAIT-1 or CLOSING, with the transmit
  buffer empty), `countDuplicateACK` replaces the FIN's retransmission timer with a
  fast-retransmit timer. `dispatch` then idles the timer and sets
  `pendingFastRetransmit`. But the guard in `seqToTransmit` that the transmit buffer
  holds data sends nothing, so the FIN is never sent again. Only a keep-alive (satnet
  drops keep-alive on close) or the timeout (satnet's 120 s `TCPTimeout`) ends the
  connection. Dropping the guard is not a safe fix, because `pendingFastRetransmit`
  survives `reset`. A fix would start the fast-retransmit timer only while the
  transmit buffer holds data, so that a FIN alone keeps its RTO.
- **No probe after a retransmission timeout into a closed window.** After an ACK of
  nothing new that announces window 0, while data is in flight, the retransmission
  timeout idles the timer even though data waits. A connection whose window update
  is lost is then never probed, although RFC 9293 (section 3.8.6.1) requires probing.
  `PollAt` waits for ingress. In satnet the next keep-alive (60 s) recovers the
  connection. With keep-alive off (the simulator's sockets, or satnet after `Close`),
  only ingress or the `TCPTimeout` does. A window that closes before the data goes out
  is probed.
- **No retransmission once a shrunken window reopens.** If the peer acknowledges part
  of the data in flight and announces window 0, the zero-window probe timer replaces
  the retransmission timer. smoltcp starts the probe timer on any ACK of new data
  (`updateZeroWindowProbeTimer`). Such a peer moves its window's right edge back, which
  RFC 9293 discourages (SHLD-14) but a sender must survive (MUST-34). The scaled window
  can also round down while data is on its way. When everything queued is already in
  flight, the probes carry no data. When the window reopens, the timer goes idle, so
  the data beyond the old window stays unacknowledged, and nothing sends it again. RFC
  9293 has it retransmitted normally (SHLD-16). `PollAt` waits for ingress. With more
  data queued, that data goes out once the window reopens, and starts the
  retransmission timer, which resends the rest an RTO later. In satnet each keep-alive
  (60 s) draws a duplicate ACK, whose arrival also keeps `TCPTimeout` from firing. The
  third duplicate brings a fast retransmission about three minutes after the window
  reopened. With keep-alive off (the simulator's sockets, or satnet after `Close`),
  nothing the waiting peer sends ends the stall, not even data of its own, since that
  is not a duplicate ACK. Only a timeout, where one is set, ends it, by aborting the
  connection. The stall was reproduced in Go and against smoltcp 0.14.0 itself, through
  its `Interface`: after the window reopens, `poll_at` is `None` with three octets
  still queued. A fix would start the retransmission timer, instead of idling it, when
  the probe timer stops with data in flight (`hasDataInFlight`).
- **A valid ACK at the start of a window that ends before it starts** gets a
  challenge ACK, and is not taken. After a FIN right at the window's end, a peer's
  ACK that arrives before the socket's ACK of the FIN is lost to the socket, and the
  data it acknowledged goes again at the retransmission timeout. smoltcp neither
  panics on the segment nor corrupts a stream
  (`TestAWindowThatEndsBeforeItStartsAnswersASegmentAtItsStartWithAChallengeACK`).
- **DNS answers from port 5353** of any host are accepted. Section 3 describes the
  cost.
