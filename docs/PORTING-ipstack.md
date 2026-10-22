# Porting smoltcp to `internal/ipstack`

`internal/ipstack` is a Go port of the part of smoltcp 0.14 that the Rust
workspace compiles. The port is complete. This document records what is
ported and where, what is left out and why, where the port deviates from
smoltcp, which smoltcp tests are ported, and which smoltcp behaviours the
two packages that use the stack depend on. It began as the contract for
the people who implemented the stack in parallel. It remains the contract
for the stack's consumers, satnet and sim, and for anyone who changes it.

- smoltcp source: `~/.cargo/registry/src/index.crates.io-1949cf8c6b5b557f/smoltcp-0.14.0/`.
  In this document, paths like `src/socket/tcp.rs:1302` are relative to that directory.
- Licence: 0BSD. The text is in `internal/ipstack/LICENSE-smoltcp`. Every
  package doc credits smoltcp.
- Why a port and not gVisor: see the design brief and the `ipstack` package
  doc. In short, the stack has to work in a pull model.

## 1. What the workspace compiles

The workspace depends on smoltcp with `default-features = false` and these
features: `std, log, medium-ip, proto-ipv4, socket-tcp, socket-udp,
socket-dns, dns-max-server-count-2, dns-max-result-count-4`. Cargo's resolved
set, read from `target/debug/.fingerprint/smoltcp-*`, is exactly that set plus
the implied `alloc, proto-dns, socket`. Nothing else is enabled.

These build-time constants apply (smoltcp `build.rs`, plus the two features):

| smoltcp constant | value | Go |
|---|---|---|
| `ASSEMBLER_MAX_SEGMENT_COUNT` | 4 | `storage.AssemblerMaxSegmentCount` |
| `DNS_MAX_SERVER_COUNT` | 2 (feature) | `dns.MaxServerCount` |
| `DNS_MAX_RESULT_COUNT` | 4 (feature) | `dns.MaxResultCount` |
| `DNS_MAX_NAME_SIZE` | 255 | `dns.MaxNameSize` |
| `IFACE_MAX_ADDR_COUNT` | 2 | dropped. smoltcp needs it only to size a `heapless::Vec`; a Go slice has no fixed size. |
| `IFACE_MAX_ROUTE_COUNT` | 2 | dropped together with routes (§3) |

smoltcp's own unit tests do not use these values. They build with a test
configuration in `src/lib.rs:150-168` (one DNS server, one result, eight
addresses). None of the tests ported below depend on the difference. If a
ported test does turn out to depend on it, use the workspace values.

**Important: there is no `auto-icmp-echo-reply`.** That feature is part of
smoltcp's default set, which the workspace disables. So the
`Icmpv4Repr::EchoRequest` arm in `src/iface/interface/ipv4.rs:345-357` is
compiled out and **the stack does not answer pings**. The brief says echo
replies stay, but it also says that where the brief and the Rust code
disagree about behaviour, the Rust code wins. The port therefore parses
incoming ICMP and drops it, as the compiled smoltcp does. Destination
unreachable replies are compiled in and are kept.

## 2. File map

Paths are relative to `internal/ipstack`. Each package has a `doc.go`
that credits smoltcp.

| smoltcp | Go |
|---|---|
| `src/lib.rs` (crate docs), `src/iface/mod.rs` | `doc.go` |
| `src/macros.rs` (`net_trace!`, `net_debug!`) | `internal/netlog` |
| `src/wire/mod.rs` (`Error`, module docs) | `wire/doc.go`, `wire/errors.go` |
| `src/wire/ip.rs` (`Protocol`, `ListenEndpoint`, the address helpers) | `wire/ip.go` |
| `src/wire/ip.rs` (`checksum` module), `src/phy/mod.rs` (`Checksum`, `ChecksumCapabilities`) | `wire/checksum.go`; the RFC 1071 arithmetic and the pseudo-header stay unexported |
| `src/wire/ipv4.rs` | `wire/ipv4.go` |
| `src/wire/tcp.rs` | `wire/tcp.go`; `wire/tcp_seq_number.go` (`SeqNumber`); `wire/tcp_option.go` (`TcpOption`, unexported) |
| `src/wire/udp.rs` | `wire/udp.go` |
| `src/wire/icmpv4.rs`, `src/wire/icmp.rs` | `wire/icmpv4.go` |
| `src/wire/dns.rs` | `wire/dns.go`; `wire/dns_name.go` (names, labels and compression pointers) |
| `Option<T>` of wire-level fields | `wire/optional.go` (`Optional`, `Some`) |
| `src/storage/mod.rs` | `storage/doc.go`, `storage/errors.go` |
| `src/storage/ring_buffer.rs` | `storage/ring_buffer.go` |
| `src/storage/packet_buffer.rs` | `storage/packet_buffer.go` |
| `src/storage/assembler.rs` | `storage/assembler.go` |
| `src/rand.rs` | `socket/rand.go` |
| `src/socket/mod.rs` `PollAt` | `socket/poll_at.go` |
| `src/iface/interface/mod.rs` + `ipv4.rs`: the `InterfaceInner` state sockets read (`now`, `rand`, `ip_mtu`, `ip_addrs`, `any_ip`, `has_ip_addr`, `get_source_address*`, `is_broadcast*`, `is_unicast_v4`, `ipv4_addr`) | `socket/context.go` |
| `src/socket/tcp.rs` | `tcp/`, one concept per file: `socket.go` (the socket, its options, endpoints and predicates), `state.go`, `errors.go`, `connection.go` (the connection record that `reset` starts afresh — `connection`, `fourTuple`, `newConnection`, `reset`, `forgetTuple` — and `listen`, `connect`, `close`, `abort`), `stream.go` (`send`, `recv`, `peek` and the queues), `segments.go` (`accepts`, `process`, `dispatch`, `poll_at`, `rst_reply`: what the interface calls), `process.go` and `transitions.go` (`process`; `transitions.go` has one transition function per state), `reassembly.go` (the receive side of `process`), `reply.go` (`rst_reply`, `ack_reply`, the challenge ACK), `transmit.go` (`seq_to_transmit`, `ack_to_transmit` and the other predicates `dispatch` and `poll_at` share, with `localMSS` — the IP MTU less the IPv4 and TCP headers, which the effective MSS and the SYN's MSS option use — and `timeoutDeadline`), `dispatch.go` (`dispatch`; `emitDueSegment` builds, emits and records the due segment), `timer.go` (`Timer`), `rtt_estimator.go` (`RttEstimator`), `delayed_ack.go` (`AckDelayTimer`, with `delayedACKPollAt` and `immediateACKToTransmit`), `window.go` (window scaling and `window_to_update`) |
| `src/socket/udp.rs` | `udp/`: `socket.go` (the socket, `bind`, `close`, capacities), `send.go`, `receive.go`, `datagrams.go` (`accepts`, `process`, `dispatch`, `poll_at`), `errors.go` |
| `src/socket/dns.rs` | `dns/`: `socket.go` (the socket and its queries), `query.go` (`DnsQuery`, `PendingQuery`), `response.go` (the response half of `process`), `datagrams.go` (`accepts`, `process`, `dispatch`, `poll_at`), `name.go` (name validation, `eq_names`, `copy_name`), `errors.go` |
| `src/phy/mod.rs` `Device`, tokens, `DeviceCapabilities` | `device.go` |
| `src/iface/interface/mod.rs` (`Interface`, `Config`, the polls) | `interface.go` |
| `src/iface/interface/mod.rs` `socket_ingress`, `process_ip` | `ingress.go` |
| `src/iface/interface/mod.rs` `socket_egress` | `egress.go`; `any_socket.go` (smoltcp's `Socket` enum, as the interface drives it when sending) |
| `src/iface/interface/mod.rs` `dispatch_ip` | `dispatch.go` |
| `src/iface/interface/ipv4.rs` (`process_ipv4`, `process_icmpv4`, `icmpv4_reply`) | `ipv4.go` |
| `src/iface/interface/tcp.rs`, `udp.rs` | `tcp.go`, `udp.go` |
| `src/iface/packet.rs` (`Packet`, `IpPayload`, `icmp_reply_payload_len`) | `packet.go`, unexported |
| `src/iface/socket_set.rs` | `socket_set.go` |
| `src/time.rs` | `time.Time` and `time.Duration` |

### Test files

Tests sit beside the code they test, in a `_test.go` file named after it.

- `wire`: `ipv4_test.go`, `tcp_test.go`, `tcp_seq_number_test.go`,
  `tcp_option_test.go`, `udp_test.go`, `icmpv4_test.go`, `dns_test.go`,
  `ip_test.go`, `optional_test.go`, `checksum_test.go`, `helpers_test.go`
  (shared builders), `example_test.go` (the `wire` module documentation's
  example) and `fuzz_test.go` (the fuzz targets of §6 and §8).
- `storage`: `ring_buffer_test.go`, `packet_buffer_test.go`,
  `assembler_test.go`.
- `socket`: `context_test.go`, `poll_at_test.go`, `rand_test.go`.
- `tcp`: `harness_test.go` holds the shared test harness: the `TestSocket`
  equivalent, the `send`, `receive`, `receiveNothing` and `sanity`
  helpers, the `SEND_TEMPL`/`RECV_TEMPL` templates and the ISN override,
  with a table that maps each smoltcp macro, builder and field to its Go
  name. The ported tests follow smoltcp's sections, one file each:
  `closed_test.go`, `listen_test.go`, `syn_received_test.go`,
  `syn_sent_test.go`, `established_test.go`, `fin_wait_test.go`,
  `closing_test.go`, `time_wait_test.go`, `close_wait_test.go`,
  `last_ack_test.go`, `transitions_test.go`, `retransmission_test.go`,
  `window_test.go`, `flow_control_test.go`, `zero_window_probe_test.go`,
  `timeout_test.go`, `keep_alive_test.go`, `reassembly_test.go`,
  `receive_close_test.go`, `delayed_ack_test.go`, `nagle_test.go`,
  `filtering_test.go`, `timestamp_test.go` (smoltcp's "Timestamp tests"
  section) and `source_address_test.go`. Tests of internals sit beside
  them (`test_timer_retransmit` in `timer_test.go`, `test_rtt_estimator`
  in `rtt_estimator_test.go`). The Go-specific tests sit beside the code
  they test: `socket_test.go` (construction, settings, states and
  predicates, endpoints), `stream_test.go` (receiving and sending),
  `connection_test.go` (`Listen`, `Connect`, what starts a connection
  afresh, and the handshake from initial sequence numbers across sequence
  space), `process_test.go` (`Accepts`, `Process` and `RSTReply`: the
  segments taken and dropped, the replies, the receive window, the peer's
  window), `dispatch_test.go` (what `Dispatch` sends and when, and
  `PollAt`: the handshake, segment sizes, data and retransmission, timers,
  ACKs and window updates) and `pull_model_test.go` (the `emit` contract);
  `established_test.go` and `rtt_estimator_test.go` hold a few beside
  their ported ones, and `fuzz_test.go` holds the fuzz target of §6.
- `udp`: `socket_test.go`. `dns`: `socket_test.go`, `name_test.go` and
  `helpers_test.go` (a test context, a recording `EmitFunc` and a builder
  of responses).
- `ipstack`, internal tests: `testing_device_test.go` (smoltcp's
  `TestingDevice` from `src/tests.rs`), `helpers_test.go` (the test
  network's addresses and datagram builders), `interface_test.go`,
  `ipv4_test.go`, `socket_set_test.go` and `pull_model_test.go`.
- `ipstack`, external tests (package `ipstack_test`): `example_test.go`
  (the package example), and the end-to-end loopback tests of §6:
  `loopback_test.go` (the tests), `loopback_network_test.go` (two
  interfaces on an in-memory line, the clock, the traffic record) and
  `loopback_services_test.go` (the applications on the two hosts).

Test helpers that `tcp`, `udp` or `dns` tests share must not import
package `ipstack`: it imports those packages, so a test that imports it
back would form an import cycle.

## 3. What is left out, and why

These are not compiled in the workspace, so they are not ported:

- **Media other than IP.** Ethernet, ARP, IEEE 802.15.4 and 6LoWPAN go,
  and with them `HardwareAddress`, `Medium` and the neighbour cache. The
  socket metadata in `src/iface/socket_meta.rs` goes too: on `Medium::Ip` a
  dispatch never fails with `NeighborPending`, so the waiting state never
  happens. `Config.hardware_addr` is dropped because only `Ip` exists.
- **IPv6**, SLAAC, MLD, NDISC, RPL and IPsec.
- **IPv4 fragmentation and reassembly.** `ParseIPv4Repr` rejects fragments
  (`src/wire/ipv4.rs`, `Repr::parse`, the `not(proto-ipv4-fragmentation)`
  branch). `dispatch_ip` silently drops datagrams larger than the IP MTU
  (`src/iface/interface/mod.rs:1297` and `:1384`). smoltcp still draws a
  fragment identifier from the RNG in `Interface::new` whenever
  `proto-ipv4` is on, drawing again while it is 0, and stores it only with
  fragmentation (`:250-259`). `NewInterface` draws it too and throws it
  away (`fragmentIdentifier`), so a seed gives the sockets smoltcp's
  initial sequence numbers and DNS query ids and ports. The reassembly buffer of `src/iface/fragmentation.rs`
  (`PacketAssembler`, `PacketAssemblerSet`) goes too: smoltcp compiles it
  in every configuration, but only reassembly uses it.
- **Multicast and IGMP.** The interface still treats 224.0.0.1 as one of its
  groups (`src/iface/interface/mod.rs:928`).
- **DHCP, raw sockets, ICMP sockets, mDNS** (`socket-mdns`). The DNS socket
  still accepts responses from port 5353, as smoltcp does (§8). This has a
  cost: any host that can reach the interface answers a pending query from
  its own port 5353, without forging a server's address; only the query's
  port and transaction id stand in its way, and both follow from sPCG32's
  state, which a few observed TCP initial sequence numbers reveal
  (`socket.Rand`). The workspace compiles no mDNS, so the path serves
  nothing; Rust snapshot 4 and smoltcp behave the same. Whether to drop the
  5353 branch is the user's decision; if it is dropped, record it in §9 and
  in PORTING.md section 3, since iridium-net then differs.
- **TCP congestion control.** Reno and Cubic are separate features. Only
  `CongestionControl::None` exists, its window is unlimited, and the
  `congestion` module disappears.
- **Async wakers** (`async`) and `socket-tcp-pause-synack`.
- **Segmentation offload, `PacketMeta`** (`packetmeta-id`),
  `DeviceCapabilities.max_burst_size`, and the window-clamping hack in
  `src/iface/packet.rs:155-164` that uses it. No device sets them.
- **Routes** (`src/iface/route.rs`). On `Medium::Ip` they have no effect.
  `process_ipv4` drops a datagram for a foreign address whatever the route
  lookup says (`src/iface/interface/ipv4.rs:185-211`). With any-IP on,
  `has_ip_addr` is true for every address (`src/iface/interface/mod.rs:909`).
  `has_neighbor` only matters for the omitted socket metadata.
- **`Interface::poll_maintenance`, `poll_egress`.** Without fragmentation or
  SLAAC, maintenance only sets the time. Egress is an internal step of `Poll`.
- **Pretty printing** (`src/wire/pretty_print.rs`, `PrettyPrint`), and
  with it the `Display` impls of the packet views (`Ipv4Packet`,
  `TcpPacket`, `UdpPacket`, `Icmpv4Packet`), of `Icmpv4Repr` and of the DNS
  types, which nothing in the stack logs. `IPv4Repr`, `TCPRepr` and
  `UDPRepr` get `String()` for logs instead.
- **`auto-icmp-echo-reply`** (§1), and with it the branch of
  `icmpv4_reply` for broadcast destinations, which builds only echo
  replies: an ICMP error answers only a datagram whose source and
  destination are both unicast.

Some public runtime options are left out because nothing in the workspace
sets them. Their code paths would be dead.

- **phy middleware**: `Loopback`, `Tracer`, `FaultInjector`, `FuzzInjector`
  and `PcapWriter` (`src/phy/`). smoltcp compiles them, but nothing in the
  workspace uses them: the workspace has its own devices, which satnet and
  sim port.
- **TCP timestamps generator** (`set_tsval_generator`, `timestamp_enabled`).
  Without a generator, smoltcp never sends a timestamps option, and its
  option-length arithmetic is always 0. The wire layer still parses and
  emits the option.
- **Hop limit** (`set_hop_limit`/`hop_limit` on TCP, UDP and DNS sockets).
  The stack always sends `wire.DefaultHopLimit` (64).
- **`tcp::Socket::set_congestion_control`/`congestion_control`**,
  `fmt::Write for tcp::Socket`,
  `dns::Socket::start_query_raw` (internal), `StartQueryError::NoFreeSlot`
  (query storage grows, as smoltcp's `Vec` storage does), `RingBuffer::reset`
  (nothing in the subset uses it), `TcpPacket::options_summary`,
  `selective_ack_permitted`/`selective_ack_ranges` (no caller),
  `verify_partial_checksum` (offload only), `IpAddress::prefix_len`,
  `Ipv4Cidr::from_netmask`/`netmask`/`network`/`contains_*` (`netip.Prefix` does these).

Some internals are left out because nothing in the subset calls them:

- the dual-stack `IpRepr`, `IpVersion` and `ip::Packet`: the sockets and
  the interface use `wire.IPv4Repr`, and the interface reads the version
  nibble itself;
- `Ipv4Packet::get_key`, which only reassembly uses;
- `ChecksumCapabilities::ignored()`: a `wire.ChecksumCapabilities` whose
  fields are `wire.ChecksumNone` says the same;
- smoltcp's `debug_assert!`s, which release builds compile out. The
  storage containers still panic on misuse, and smoltcp's
  `unreachable!()` for an ACK that reaches `process` in LISTEN stays a
  panic;
- the TCP traces smoltcp compiles only under `cfg(test)` or its `verbose`
  feature (in `send_impl`, `recv_impl` and `peek`).

If one of these turns out to be needed, raise it. Do not add it on your own
initiative.

## 4. How the Rust API maps to Go

General rules:

- Names follow the brief: Go names spell out the words that smoltcp
  abbreviates, and keep the Go initialisms. So `recv` becomes `Receive`
  (`ReceiveSlice`, `CanReceive`, `MayReceive`, `ReceiveQueue`,
  `ReceiveCapacity`, `PacketReceiveCapacity`, `PayloadReceiveCapacity`),
  `rx_buffer`/`tx_buffer` become `receiveBuffer`/`transmitBuffer`, and the
  test macros `recv!`/`recv_nothing!` become `receive`/`receiveNothing`.
  A name that hides a side effect says so: `get_query_result` becomes
  `TakeQueryResult`, because it ends the query, like the module's other
  `Take…` methods. Go drops `Get`: `get_allocated`/`get_unallocated`
  become `Allocated`/`Unallocated`. `DnsQueryType` becomes `wire.DNSType`,
  because it types records too. Unexported names follow the same rule
  (`remote_win_len` becomes `remoteWindowLen`), keeping the initialisms
  (`MSS`, `RTT`, `RTO`, `SACK`, `ISN`) and the wire package's terms
  (`SeqNumber`, `AckNumber`). One name stays short on purpose: `cx` is the
  `*socket.Context` parameter everywhere, as `ctx` is a `context.Context`.
  smoltcp's own type and error names appear in comments as they are
  (`RecvError`).
- Addresses are `netip.Addr` (plain IPv4, `Is4`). Endpoints are
  `netip.AddrPort`. `IpListenEndpoint` becomes `wire.ListenEndpoint`
  (`Addr` zero = any). `IpCidr` becomes `netip.Prefix`, which keeps the
  unmasked address as `Ipv4Cidr` does. `IpRepr` exists only as its IPv4
  variant, so the sockets and the interface use `wire.IPv4Repr` directly.
- **Only IPv4 addresses enter the stack.** smoltcp's types cannot hold
  anything else; a `netip.Addr` can also be IPv6, IPv4-mapped IPv6
  (`::ffff:a.b.c.d`) or the zero `Addr`. Every place where an address
  comes in from outside therefore checks `Is4`, and callers that hold
  IPv4-mapped addresses unmap them first (`netip.Addr.Unmap`):
  - `tcp.Socket.Listen`, `tcp.Socket.Connect`, `udp.Socket.Bind` and
    `udp.Socket.Send`/`SendWith`/`SendSlice` return `ErrUnaddressable`.
    A `ListenEndpoint` may still leave its address open (the zero `Addr`),
    and `udp.Metadata.LocalAddress` may be the zero `Addr`.
  - A DNS server that is not IPv4 is treated like an unspecified one: the
    query fails when it reaches that server (`src/socket/dns.rs:579-583`).
  - `socket.Context.SetIPAddrs` returns an error, and `SourceAddress`
    returns false for a destination that is not IPv4.
  - The wire functions that write or checksum an address panic. Nothing
    the stack builds reaches them with anything but IPv4.
- `Instant` becomes `time.Time` and `Duration` becomes `time.Duration`.
  smoltcp's `Instant::ZERO` (the stack's epoch) maps to `time.Time{}`. The
  stack never uses the zero time to mean "no deadline": it uses `PollAt` and
  `(time.Time, bool)` instead. Tests can therefore write smoltcp's
  `Instant::from_millis(ms)` as `time.Time{}.Add(ms * time.Millisecond)`.
  Where smoltcp computes `(a - b).total_millis()`, keep the truncation to
  milliseconds. Its `Instant - Instant` is an absolute value
  (`src/time.rs:173-178`).
- An `Option<Duration>` option, such as a timeout, keep-alive or ACK delay,
  becomes a `time.Duration` where zero or negative means "none".
- Wire-level `Option<T>` fields whose zero value is valid (sequence numbers,
  window scale, MSS, timestamps, SACK blocks) become `wire.Optional[T]`: `wire.Some(x)`;
  the zero value is absent. `Option<IpEndpoint>`/`Option<IpAddress>` become the
  zero `netip.AddrPort`/`netip.Addr` (`tcp.Socket.LocalEndpoint`,
  `udp.Metadata.LocalAddress`).
- Packet views: `XPacket::new_unchecked(buf)` becomes `wire.XPacket(buf)` and
  `new_checked` becomes `wire.NewXPacket(buf)`. `payload_mut()` becomes `Payload()`:
  slices alias, so the same accessor writes. `Repr::parse` becomes `wire.ParseXRepr`.
- **`Ipv4Repr::buffer_len()` is the header length in smoltcp.** In Go,
  `IPv4Repr.HeaderLen()` is the header and `BufferLen()` is header plus
  payload, as `TCPRepr` has it. So, for a `TCPRepr` named `segment`,
  `ip.buffer_len() + segment.buffer_len()` ports to
  `ip.HeaderLen() + segment.BufferLen()`, and
  `ip.buffer_len() + ip.payload_len` ports to `ip.BufferLen()`.
- `UdpRepr::emit(packet, src, dst, len, |buf| copy, caps)` becomes
  `UDPRepr.Emit(packet, src, dst, payload, caps)`.
- Errors: smoltcp's per-operation enums become per-package sentinels, used
  with `errors.Is`. The texts are smoltcp's `Display` texts, except that DNS
  starts them in lower case (Go convention, staticcheck ST1005), and
  `wire.ErrMalformed` (`wire::Error`) is worded for Go.
- Closures that borrow a buffer (`recv(|buf| …)`, `send(|buf| …)`,
  `enqueue_many_with`, `dequeue_with`) become `func([]T) int` or
  `func(*H, []byte) error` callbacks. The generic result `R` goes, because
  callers capture what they need.

| Rust (smoltcp) | Go |
|---|---|
| `Config::new(HardwareAddress::Ip)` + `random_seed` | `ipstack.Config{RandomSeed: seed}` |
| `Interface::new(config, &mut device, now)` | `ipstack.NewInterface(config, ipstack.DeviceCapabilities{MTU: mtu}, now)`; the device keeps its own MTU accessor |
| `iface.context()` | `iface.Context()` (`*socket.Context`) |
| `iface.update_ip_addrs(\|a\| { a.clear(); a.push(cidr) })` | `iface.SetIPAddrs([]netip.Prefix{prefix})` (error, not panic) |
| `iface.set_any_ip(true)` | `iface.SetAnyIP(true)` |
| `iface.poll(now, &mut dev, &mut sockets)` | `iface.Poll(now, dev, &sockets)` (`PollResult`) |
| `iface.poll_ingress_single(…) != PollIngressSingleResult::None` | `iface.PollIngressSingle(…) != ipstack.IngressNone` |
| `iface.poll_at(now, &sockets)` / `poll_delay` | `iface.PollAt(now, &sockets) (time.Time, bool)` / `PollDelay` |
| `SocketSet::new(Vec::new())` | `ipstack.SocketSet{}` (the zero value is ready) |
| `sockets.add(socket)` | `sockets.AddTCP(s)` / `AddUDP` / `AddDNS` |
| `sockets.get::<tcp::Socket>(h)`, `get_mut` | `sockets.TCP(h)` / `UDP(h)` / `DNS(h)` |
| `sockets.remove(h)` | `sockets.Remove(h)` |
| `sockets.iter()` + `match Socket::Tcp(s)` | `for h, s := range sockets.TCPSockets()` (and `UDPSockets`, `DNSSockets`) |
| `impl Device` + `RxToken`/`TxToken` | `ipstack.Device` with `Receive(now) ([]byte, TransmitSlot, bool)` and `Transmit(now) (TransmitSlot, bool)`; a `TransmitSlot` is `func(datagram []byte)`. `capabilities()` becomes `NewInterface`'s argument, the only place the interface reads it |
| `DeviceCapabilities { medium: Ip, max_transmission_unit: mtu, .. }` | `ipstack.DeviceCapabilities{MTU: mtu}` |
| `ChecksumCapabilities::default()` | `wire.ChecksumCapabilities{}` |
| `tcp::Socket::new(tcp::SocketBuffer::new(vec![0; n]), …)` | `tcp.NewSocket(tcp.NewSocketBuffer(n), tcp.NewSocketBuffer(m))` |
| `socket.listen(port)` | `s.Listen(wire.ListenEndpoint{Port: port})` |
| `socket.connect(cx, remote, local_port)` | `s.Connect(cx, remote, wire.ListenEndpoint{Port: localPort})` |
| `set_timeout(Some(d))`, `set_keep_alive(None)`, `set_ack_delay(Some(d))`, `set_nagle_enabled(false)` | `SetTimeout(d)`, `SetKeepAlive(0)`, `SetAckDelay(d)`, `SetNagleEnabled(false)` |
| `recv_slice(buf)` → `Err(RecvError::Finished)` | `ReceiveSlice(buf)` → `errors.Is(err, tcp.ErrFinished)` |
| `recv(\|buf\| …)`, `can_recv()`, `may_recv()`, `recv_queue()`, `recv_capacity()` | `Receive(consume)`, `CanReceive()`, `MayReceive()`, `ReceiveQueue()`, `ReceiveCapacity()` |
| `send_slice(data)` → `Err(SendError::InvalidState)` | `SendSlice(data)` → `tcp.ErrInvalidState` |
| `local_endpoint() == Some(ep)` | `LocalEndpoint() == ep` (zero `AddrPort` when unconnected) |
| `tcp::State::CloseWait` | `tcp.StateCloseWait` |
| `udp::PacketBuffer::new(vec![PacketMetadata::EMPTY; n], vec![0; m])` | `udp.NewPacketBuffer(n, m)` |
| `udp::Socket::new(rx, tx)`, `bind(port)` | `udp.NewSocket(rx, tx)`, `Bind(wire.ListenEndpoint{Port: port})` |
| `send_slice(data, endpoint)` / `send_slice(data, metadata)` | `SendSlice(data, udp.Metadata{Endpoint: ep})` / `SendSlice(data, metadata)` |
| `recv()` → `Ok((payload, metadata))`, `Err(RecvError::Exhausted)` | `Receive() ([]byte, udp.Metadata, error)`, `udp.ErrExhausted` |
| udp `recv_slice(buf)`, `can_recv()`, `packet_recv_capacity()`, `payload_recv_capacity()` | `ReceiveSlice(buf)`, `CanReceive()`, `PacketReceiveCapacity()`, `PayloadReceiveCapacity()` |
| `metadata.endpoint`, `metadata.local_address` | `meta.Endpoint`, `meta.LocalAddress` |
| `dns::Socket::new(&servers, Vec::new())` | `dns.NewSocket(servers)` |
| `start_query(cx, name, DnsQueryType::A)` | `StartQuery(cx, name, wire.DNSTypeA)` (`dns.QueryHandle`; the zero handle names no query) |
| `get_query_result(q)` → `Pending` / `Failed` | `TakeQueryResult(q) ([]netip.Addr, error)`; `dns.ErrPending` / `dns.ErrFailed` |
| `cancel_query(q)`, `update_servers(&s)` | `CancelQuery(q)`, `UpdateServers(s)` |
| `Ipv4Repr { src_addr, dst_addr, next_header, payload_len, hop_limit }` | `wire.IPv4Repr{SrcAddr, DstAddr, NextHeader, PayloadLen, HopLimit}` |
| `TcpRepr { …, ack_number: Some(n), max_seg_size: None, sack_ranges: [None; 3], timestamp: None, payload }` | `wire.TCPRepr{…, AckNumber: wire.Some(n), Payload: p}` (zero Optionals are absent) |
| `TcpSeqNumber(i)`, `s + n`, `a - b`, `a < b`, `s.0` | `wire.TCPSeqNumber(i)`, `s.Add(n)`, `a.Sub(b)`, `a.Compare(b) < 0`, `int32(s)` |
| `TcpControl::Syn` / `Psh` / `None` | `wire.TCPControlSYN` / `TCPControlPSH` / `TCPControlNone` |
| `IpProtocol::Tcp`, `Unknown(0x0c)` | `wire.IPProtocolTCP`, `wire.IPProtocol(0x0c)` |
| `tcp.syn()`, `tcp.ack()`, `ip.next_header()`, `ip.dst_addr()` | `t.SYN()`, `t.ACK()`, `ip.NextHeader()`, `ip.DstAddr()` |
| `DnsPacket::new_checked(p)`, `rcode()`, `opcode()`, `flags().contains(DnsFlags::TRUNCATED)` | `wire.NewDNSPacket(p)`, `Rcode()`, `Opcode()`, `Flags()&wire.DNSFlagTruncated != 0` |
| `DnsQuestion::parse(b)` → `(rest, q)`; `DnsRecord::parse(b)` → `(rest, r)` | `wire.ParseDNSQuestion(b)` → `(q, rest, err)`; `wire.ParseDNSRecord(b)` → `(r, rest, err)` |
| `DnsRecordData::A(addr)` | `record.Data.Type == wire.DNSTypeA`, `record.Data.Address` |
| `DnsRecordData::Cname(name)`, `Other(type, data)` | `record.Data.Raw` (with `record.Data.Type`) |
| `ring.get_allocated(o, n)`, `get_unallocated(o, n)`; `PacketBuffer::reset()` | `ring.Allocated(o, n)`, `Unallocated(o, n)`; `PacketBuffer.Clear()` (as `RingBuffer` and `Assembler` name it) |
| `DnsRcode::NXDomain`, `DnsOpcode::Unknown(4)` | `wire.DNSRcodeNXDomain`, `wire.DNSOpcode(4)` |

## 5. The layers and the contracts between them

The stack was built by three implementers in parallel: one for `wire`,
`storage` and `socket`, one for `tcp`, and one for `ipstack`, `udp` and
`dns`. Each layer uses only those below it (`tcp`, `udp` and `dns` never
import `ipstack`), and the exported signatures of the skeleton held
throughout. This section records the notes each layer was built to, and
the contracts between the layers, which still hold.

### `wire`, `storage`, `socket`

- Every packet view, `Parse…` function, `Emit` method, length and checksum
  is implemented, with smoltcp's quirks: a timestamps option of the wrong
  length is kept as an unknown option, a SACK option keeps at most three
  blocks ("sACK with >3 blocks, truncating to 3"), and a window scale over
  14 is capped ("parsed window scaling factor >14, setting to 14"). DNS
  names follow compression pointers with smoltcp's loop protection
  (`NameLabels`); `ParseDNSQuestion` and `ParseDNSRecord` read a name
  without following pointers, as `parse_name_part` does.
- `socket.Context.SetIPAddrs` validates the same way `check_ip_addrs` does
  (`src/iface/interface/mod.rs:896`): each address must be IPv4 unicast or
  unspecified. It also requires `Is4` and a valid prefix length, and returns
  an error wrapping `ErrInvalidIPAddr` whose text names the address
  ("invalid interface address: IP address 224.0.0.1 is not unicast"). It does
  not panic.
- `Rand` is sPCG32 exactly as in `src/rand.rs`, not cryptographically
  secure: its values are predictable from a few observed ISNs (§3). Its
  tests check these golden values, computed with rustc from smoltcp's code:
  - seed 0: `Uint32` = 0x3, 0xfdecf8a2, 0x6130bec3, 0x77368c50; `Uint16` =
    0x3, 0x54e, 0xdff3, 0xfb66; `SourcePort` = 1358, 57331, 64358, 18633.
  - seed 0x5EED: `Uint32` = 0xaf7268a4, 0x9ee15bb3, 0x55558f4e, 0xd42f480d;
    `Uint16` = 0xc7d6, 0xc552, 0xda1b, 0x9c22; `SourcePort` = 51158, 50514,
    55835, 39970.
  - `SourcePort`'s bound, the first draw above 1024: seed 0x1b705 draws 1024
    first, which `SourcePort` skips, returning 45842 (the next `Uint16` is
    6677); seed 0x28689 draws 1025 first, which it returns (the next is
    35777).
- The checksum arithmetic from `src/wire/ip.rs` (`checksum::data`,
  `combine`, `pseudo_header`) stays unexported. One helper turns an
  address into its four octets, panicking unless it is `Is4` (the zero
  `Addr`, IPv6 and IPv4-mapped addresses all panic), and serves every
  function that writes or checksums one (§4).

### `tcp`

- `tcp.Socket`, `RSTReply` and every internal type of `src/socket/tcp.rs`
  are ported: the timer, the RTT estimator, the ACK-delay timer and the
  tuple. The fields smoltcp's `reset` reinitialises are grouped in an
  embedded struct. As in smoltcp, `reset` keeps `remote_has_sack`, the
  last received sequence number and ACK, the duplicate-ACK count and the
  pending fast retransmit, so a reused socket starts with the previous
  connection's values.
- Initial sequence numbers: smoltcp returns 10000 under `cfg(test)`
  (`src/socket/tcp.rs:1070-1077`). The socket has an unexported seam
  instead, its `chooseISN` field, a `func(*socket.Context)
  wire.TCPSeqNumber` that `NewSocket` sets to `randomISN`, which draws
  `cx.Rand().Uint32()`. `Connect` consults it (`:1063`), and so does
  `Process` when a listener accepts a SYN (`:1919`). The internal tests
  replace it with one that returns `LOCAL_SEQ` (10000). There is no
  package-level mutable variable. An absent `wire.Optional` holds sequence
  number 0, which comes before an ISN in the lower half of sequence space
  and after one in the upper half, so the handshake is tested from ISNs
  across sequence space too (`isnsAcrossSequenceSpace`: 10000, 0x7fffffff,
  0x80000000, 0x90000000, 0xfffffffe and 0xffffffff, in `connection_test.go`),
  and the fuzz target's config octet can put the socket's ISNs in the upper
  half (`configUpperHalfISN`).
- smoltcp uses `Instant::ZERO` as "a time already past" in three places:
  the zero-window probe started from `send` (`:1262-1264`), the keep-alive
  armed by `set_keep_alive` (`Timer::set_keep_alive`, `:362-368`), and the
  challenge-ACK timer, which starts and resets at the epoch
  (`challenge_ack_timer: Instant::from_secs(0)`, `:619` and `:928`), so that
  the first challenge ACK may go at once. The port uses `time.Time{}` in
  all three places — its zero `challengeACKTimer` does as smoltcp's does —
  and `wire.Optional` or explicit state wherever smoltcp has an `Option`,
  never the zero time.
- Congestion control, the timestamp generator and hop limits are left out
  (§3). Everything else in `process`, `dispatch` and `poll_at`, including
  SACK ranges in `ack_reply`, the challenge-ACK rate limit and the
  zero-window probe backoff, is ported as written, but for the four kinds
  of segment smoltcp mishandles (§9).

### `ipstack`, `udp`, `dns`

- The interface keeps an unexported packet type for its replies and
  egress, mirroring `src/iface/packet.rs`, and serializes datagrams with
  the wire Reprs into a fresh buffer per datagram, which the device owns
  once it has the datagram.
- The socket set holds its sockets behind an unexported interface, the
  counterpart of smoltcp's `Socket` enum, which egress drives
  (`dispatch`, `poll_at`). Ingress needs none: each protocol reaches only
  its own kinds of socket, through the typed iterators.
- The UDP socket ports every behaviour of `socket::udp`. The DNS socket
  ports `socket::dns` exactly, two smoltcp quirks included: an NXDOMAIN
  response fails every pending query it matches, not only the first, and
  a CNAME record replaces the query's name even when the rest of the
  response turns out to be malformed.

### Contracts between the layers

1. **Context.** The interface creates one with
   `socket.NewContext(now, config.RandomSeed, capabilities.MTU)`, then draws
   smoltcp's fragment identifier from its random source before any socket
   draws (§3). It calls
   `SetNow(now)` at the start of `Poll`, `PollIngressSingle` and `PollAt`,
   as smoltcp's `poll`, `poll_ingress_single` and `poll_at` set
   `inner.now`, although no socket's `PollAt` reads the time. Sockets only
   read the context. `IPMTU` is the device MTU, since devices carry bare IP
   datagrams.
2. **Ingress (interface to sockets).** The datagram is checked and parsed
   first. Then:
   - TCP: a segment whose source or destination address is unspecified is
     dropped before any socket sees it (`src/iface/interface/tcp.rs:14-22`).
     Otherwise, for each TCP socket in slot order, `if s.Accepts(ip, seg)`,
     take `s.Process(cx, ip, seg)` and stop. Its reply, if any, goes out
     through the received datagram's reply slot. If no socket accepts the
     segment, reply with `tcp.RSTReply(ip, seg)`, unless the segment is
     itself a RST.
   - UDP: first the UDP sockets in slot order (`Accepts(cx, ip, hdr)`, then
     `Process(ip, hdr, payload)`), then the DNS sockets
     (`Accepts(ip, hdr)`, then `Process(ip, hdr, payload)`). If nobody
     accepts the datagram, reply with ICMP port unreachable.
   - Payload slices handed to `Process` alias the received datagram, and
     sockets copy them.
3. **Egress (sockets to interface).** In slot order, the interface calls
   each socket's `Dispatch(cx, emit)`.
   - `emit` asks the device for a slot (`Transmit`) first. If it gets none,
     it returns an unexported sentinel error, and the egress round ends. If
     the datagram is larger than the MTU, `emit` leaves the slot unused and
     returns nil: the datagram is dropped, but counts as sent, so a UDP or
     DNS socket dequeues it and the round counts as having sent something
     (`src/iface/interface/mod.rs:734-746`, `:1380-1386`). Otherwise it
     serializes the datagram and sends it through the slot. Received
     datagrams are never checked against the MTU (§8).
   - Sockets return `emit`'s error unchanged. They skip exactly what smoltcp
     skips after its `emit(…)?`, and keep what they decided before it:
     - TCP skips the keep-alive rewind, the delayed-ACK reset, the
       zero-window-probe rewind, the update of `remote_last_seq`,
       `remote_last_ack` and `remote_last_win`, RTT sampling, arming the
       retransmission timer, and forgetting the four-tuple after an abort's
       RST (`src/socket/tcp.rs:2793-2853`). It keeps the start of the
       timeout clock, the abort on timeout, a retransmission timeout's
       rewind and backoff (`:2462-2512`), and the cleared fast-retransmit
       flag (`:2639`).
     - DNS skips setting the retransmission time and doubling the delay
       (`src/socket/dns.rs:643-644`). It keeps the server's 10 s timeout,
       which runs from the first attempt, the move to the next server, and
       the failures decided before emitting (`:552-623`).
     - UDP skips the dequeue (`src/socket/udp.rs:535-592`).
     So the next `Dispatch` makes the same choice again, unless something
     changed in between. The exception is a refused fast retransmission,
     which smoltcp does not repeat.
   - The `Payload` of an emitted `TCPRepr`, and the payload slice from UDP
     and DNS, alias socket storage and are valid only during `emit`.
   - `Poll` repeats egress rounds while a round sent something, as smoltcp
     does (`src/iface/interface/mod.rs:501-507`). A round that ends at a
     refusal after sending something is followed by another, which asks
     the device again.
4. **PollAt.** TCP's takes `cx` because `seq_to_transmit` needs the IP MTU.
   UDP's and DNS's take nothing. The interface combines them with
   `socket.Earliest` and converts the result with `PollAt.Deadline(now)`.
5. **Unused parameters dropped.** Where smoltcp passes `cx` or `PacketMeta`
   and never reads it (`tcp::accepts`, `udp::process`, `udp::poll_at`,
   `dns::process`, `dns::poll_at`), the Go method does not take it.

## 6. Tests

Every smoltcp test that compiles with the workspace's features is ported,
or listed below as not ported with the reason. Go-specific behaviour has
tests of its own, and the loopback tests run the whole stack end to end.

Rules every test follows:

- It ports the scenario and the assertions.
- The Go test is named `Test` + subject + the CamelCase of the smoltcp name
  without `test_`, using Go initialisms (TCP, UDP, IP, DNS, SYN, ACK, FIN,
  RST, MSS, RTT, SACK). For example,
  `wire/tcp.rs test_deconstruct` becomes `TestTCPPacketDeconstruct`, and
  `socket/tcp.rs test_syn_sent_rst_bad_ack` becomes `TestSynSentRSTBadACK`.
  This is the one deliberate exception to the module's rule against
  abbreviations (§4): a ported test keeps smoltcp's words, abbreviations
  included (`TestRxCloseFIN`, `TestEstablishedSendBufGtWin`,
  `TestTsvalDisabledInLocalServer`, udp's `TestDoesntAcceptWrongPort`), so
  that each can be found from its smoltcp name and back, in every package
  alike. Go-only tests are named in full words.
- smoltcp's test helpers are ported along with the tests. smoltcp's macros
  (`send!`, `recv!`, `recv_nothing!`, `sanity!`) become `t.Helper()`
  functions with the same meaning (`send`, `receive`, `receiveNothing`,
  `sanity`); `sanity!`, for example, compares the unexported fields of two
  sockets. `receive` quashes PSH unless asked for an exact match. It also
  checks that exactly one packet was sent, and that the IP header has the
  right addresses and `PayloadLen == BufferLen`. Unlike smoltcp's `send`,
  which discards the reply's IP header, the Go `send` checks it too: the
  addresses swapped, `NextHeader` `wire.IPProtocolTCP`, `HopLimit`
  `wire.DefaultHopLimit` and `PayloadLen` equal to the reply's
  `BufferLen`. A wrong length would otherwise pass every TCP test and only
  show up as a malformed datagram in the interface's tests.
- `t.Parallel()` wherever the test shares no state. No test sleeps: every
  time is derived from one epoch and passed in.

### `wire`

- **`src/wire/ipv4.rs`**: `test_deconstruct`, `test_construct`, `test_overlong`, `test_total_len_overflow`,
  `test_parse`, `test_parse_bad_version`, `test_parse_total_len_less_than_header_len`, `test_parse_small_ihl`,
  `test_emit`, and `test_cidr_broadcast` (against `wire.SubnetBroadcast`,
  with a /30 case added: 192.168.1.1/30 has the broadcast 192.168.1.3, the
  smallest network with a broadcast address).
  - Not ported: `test_unspecified`, `test_broadcast`, `test_cidr`,
    `test_cidr_from_netmask`, `test_cidr_netmask`, `test_cidr_network`.
    They test `core::net` or `Ipv4Cidr` helpers that `netip` replaces.
- **`src/wire/ip.rs`**: none. `to_prefix_len_ipv4` and
  `to_prefix_len_ipv4_error` test the omitted `prefix_len`; the others
  (`to_prefix_len_ipv6*`, `parse_*packet*`) need IPv6 and do not compile.
- **`src/wire/tcp.rs`**: `test_deconstruct`, `test_construct`, `test_truncated`, `test_impossible_len`,
  `test_parse`, `test_emit`, `test_header_len_multiple_of_4`, `test_tcp_options`, `test_malformed_tcp_options`.
  - Not ported: `test_tcp_options_summary`, because `options_summary` is
    omitted.
- **`src/wire/udp.rs`**: `test_deconstruct`, `test_construct`, `test_impossible_len`, `test_zero_checksum`,
  `test_no_checksum`, `test_parse`, `test_emit`, `test_checksum_omitted`.
- **`src/wire/icmpv4.rs`**: `test_echo_deconstruct`, `test_echo_construct`, `test_echo_parse`,
  `test_echo_emit`, `test_check_len`.
- **`src/wire/dns.rs`**: `test_parse_name` (against `NameLabels`), `test_parse_request`,
  `test_parse_response`, `test_parse_response_multiple_a`, `test_parse_response_cname`,
  `test_parse_response_nxdomain`, `test_emit`.
- **Go-specific**: `Optional`; `TCPSeqNumber` wrap-around and its panics;
  `TCPRepr.Equal`, `HeaderLen` and `String`; `ListenEndpoint`;
  `ParseIPv4Repr` rejecting fragments; every enum's `String`; `NameLabels`
  stopping at the first error — labels of the reserved types 01 and 10
  rejected, not followed as pointers — and following only backward
  pointers (`TestDNSPacketNameLabelsStopsAtTheFirstError`); the address
  setters, `Emit` methods and checksum functions panicking on an address
  that is not IPv4; the parsers' length and label-type guards at their
  exact boundary (a 7-octet echo with a valid checksum, a question with
  three octets of fields, a label one octet short, the reserved label type
  10 in questions and records, timestamps options of 9 and 11 octets, kept
  as unknown options as smoltcp keeps them, a TCP header length of 16
  octets, a truncated last TCP option); and checksums computed
  independently, worked out by hand and checked with a separate RFC 1071
  calculation — a port-unreachable message; a pseudo-header whose length
  has a non-zero high octet, 0x0118 and 0x10118, giving 0x8472
  (`TestPseudoHeaderChecksumAddsAddressesProtocolAndLength`); a 308-octet
  UDP datagram (0x49f2) and a 320-octet TCP segment (0x3f73), each checked
  on `Emit` and on `Parse`
  (`TestUDPChecksumOfALongDatagramCoversTheHighOctetOfItsLength`,
  `TestTCPChecksumOfALongSegmentCoversTheHighOctetOfItsLength`); and an
  IPv4 header with a Router Alert option (0x3d71), which `VerifyChecksum`
  accepts, rejects once an option octet changes, and `FillChecksum` writes
  back (`TestIPv4PacketChecksumCoversTheOptions`) — as an error the sender
  and the receiver share cancels out in the loopback, satnet and sim tests;
  `Example`, from smoltcp's `wire` module documentation.
- **Fuzz targets** (`fuzz_test.go`), one per checked constructor and
  parser, plus the TCP option parser and `NameLabels`:
  `FuzzNewIPv4Packet`, `FuzzParseIPv4Repr`, `FuzzNewTCPPacket`,
  `FuzzParseTCPRepr`, `FuzzParseTCPOption`, `FuzzNewUDPPacket`,
  `FuzzParseUDPRepr`, `FuzzNewICMPv4Packet`, `FuzzParseICMPv4Repr`,
  `FuzzNewDNSPacket`, `FuzzParseDNSQuestion`, `FuzzParseDNSRecord` and
  `FuzzDNSPacketNameLabels`. Beyond never panicking (§8), they check that
  writing back every field read leaves a packet's octets unchanged, that
  `FillChecksum` makes `VerifyChecksum` pass, that a parsed Repr survives
  `Emit` and a second parse, and that name parsing always ends. Each has a
  seed corpus in the test; no crasher was found, so there is no
  `testdata/fuzz`.

### `storage` and `socket`

- **`src/storage/ring_buffer.rs`** (all): `test_buffer_length_changes`, `test_buffer_enqueue_dequeue_one_with`,
  `test_buffer_enqueue_dequeue_one`, `test_buffer_enqueue_many_with`, `test_buffer_enqueue_many`,
  `test_buffer_enqueue_slice`, `test_buffer_dequeue_many_with`, `test_buffer_dequeue_many`,
  `test_buffer_dequeue_slice`, `test_buffer_get_unallocated`, `test_buffer_write_unallocated`,
  `test_buffer_get_allocated`, `test_buffer_read_allocated`, `test_buffer_with_no_capacity`,
  `test_buffer_write_wholly`.
- **`src/storage/packet_buffer.rs`** (all): `test_simple`, `test_peek`, `test_padding`,
  `test_padding_with_large_payload`, `test_dequeue_with`, `test_metadata_full_empty`, `test_window_too_small`,
  `test_contiguous_window_too_small`, `test_contiguous_window_wrap`, `test_capacity_too_small`,
  `test_contig_window_prioritized`, `test_enqueue_fallible_full_metadata_fn_not_called`, `clear`.
- **`src/storage/assembler.rs`** (all): `test_new`, `test_empty_add_full`, `test_empty_add_front`,
  `test_empty_add_back`, `test_empty_add_mid`, `test_partial_add_front`, `test_partial_add_back`,
  `test_partial_add_front_overlap`, `test_partial_add_front_overlap_split`, `test_partial_add_back_overlap`,
  `test_partial_add_back_overlap_split`, `test_partial_add_both_overlap`, `test_partial_add_both_overlap_split`,
  `test_rejected_add_keeps_state`, `test_empty_remove_front`, `test_trailing_hole_remove_front`,
  `test_trailing_data_remove_front`, `test_boundary_case_remove_front`, `test_shrink_next_hole`, `test_join_two`,
  `test_join_two_reversed`, `test_join_two_overlong`, `test_iter_empty`, `test_iter_full`, `test_iter_one_front`,
  `test_iter_one_back`, `test_iter_one_mid`, `test_iter_one_trailing_gap`, `test_iter_two_split`,
  `test_iter_three_split`, `test_issue_694`, `test_add_then_remove_front`, `test_add_then_remove_front_at_front`,
  `test_add_then_remove_front_at_front_touch`, `test_add_then_remove_front_at_front_full`,
  `test_add_then_remove_front_at_front_full_offset_0`, `test_random`.
  - The eight `test_empty_add_*` and `test_partial_add_*` cases are tables
    of `TestAssemblerAddToEmpty` and `TestAssemblerAddToPartial`, with the
    smoltcp names as case names.
  - `test_random` is the bitmap cross-check, with `math/rand/v2` seeded
    with a fixed seed so it is repeatable.
- **`src/iface/interface/tests/ipv4.rs`**, as `socket.Context` tests:
  `test_local_subnet_broadcasts` (`IsBroadcast`), `get_source_address`, `get_source_address_empty_interface`.
- **Go-specific**: `PollAt` ordering, `Earliest`, `Deadline`, `Equal` and
  `String` (`TestPollAtStringNamesTheVariantAsSmoltcpsDebugDoes`, §9 Texts);
  `IsBroadcast` and `IsUnicast`; `SetIPAddrs` rejecting multicast,
  broadcast, IPv6, IPv4-mapped addresses and bad prefix lengths;
  `HasIPAddr` with any-IP; `IsUnicast` with subnet broadcasts;
  `SourceAddress` refusing non-IPv4 destinations; the `Rand` golden
  values (§5); the zero `Context` and `Rand`
  (`TestZeroContextIsAtTheEpochSeededWithZeroWithoutMTUOrAddresses`,
  `TestZeroRandIsSeededWithZero`); the storage containers' zero values,
  capped slices and panics, `PacketBuffer`'s panic on a negative size,
  which leaves it unchanged (`TestPacketBufferPanicsOnANegativeSizeBeforeChangingAnything`),
  `EnqueueWith`'s refusal of what cannot fit at all
  (`TestPacketBufferEnqueueWithRefusesWhatCannotFitAtAll`) and of a
  payload one octet larger than either free area
  (`TestPacketBufferRefusesAPayloadOneOctetLargerThanEitherFreeArea`), the
  random-access methods just before, at and past the end of their areas
  (`TestRingBufferRandomAccessGetsNothingPastTheEndOfItsArea`), and
  `AddThenRemoveFront` taking data at offset 0 on a full assembler, on both
  sides of the first hole's end
  (`TestAssemblerAddThenRemoveFrontNeverRefusesDataAtOffsetZero`: sizes 1,
  3, 9 and 10, where 10 reaches the first run and returns 13), beside the
  ported `test_add_then_remove_front_at_front_full_offset_0`, which adds
  only 3 octets.

### `tcp`

- **`src/socket/tcp.rs`**, 180 of its 195 tests: every test from
  `test_closed_reject` to `test_established_close_on_src_ip_change`, except
  the 15 below. That includes `test_timer_retransmit`,
  `test_rtt_estimator`, and `test_tsval_disabled_in_local_server` and
  `test_tsval_disabled_in_local_client`, with their `timestamp_enabled()`
  assertions dropped: they check that a peer's timestamps are not echoed.
- Not ported, because they test an omitted feature:
  - `test_syn_paused_ack` (pause-synack)
  - `test_congestion_window_limits_data_in_flight`,
    `test_congestion_window_doesnt_limit_fast_retransmit`,
    `test_zero_window_probe_not_capped_by_cwnd` (Reno)
  - `test_segmentation_offload`
  - `test_set_hop_limit`, `test_set_hop_limit_zero`
  - `test_set_get_congestion_control`
  - `test_tsval_established_connection`, `test_tsval_disabled_in_remote_client`,
    `test_tsval_disabled_in_remote_server`,
    `test_established_options_reduce_payload_when_local_mss_limited`,
    `test_established_options_reduce_payload_when_remote_mss_limited`,
    `test_established_tiny_mss_with_options_makes_progress`,
    `test_nagle_works_with_reduced_payload_from_options` (these need the
    timestamp generator). The clamp of a tiny peer MSS that the third
    also covers is tested in `listen_test.go` and `syn_sent_test.go`.
- The local MSS limit that `test_established_options_reduce_payload_when_remote_mss_limited`
  also covers is tested, without timestamps, by
  `TestEstablishedLocalMSSLimitsSegments` (`established_test.go`): with the
  peer's MSS and window at 9999, 4000 queued octets go out as two segments
  of the local MSS, 1460 octets each, the second while the first is in
  flight (Nagle's full-segment test), and the rest waits; smoltcp's one
  segment's worth of data would fit whichever MSS applied once timestamps
  are gone.
- The test context: `socket.NewContext(time.Time{}, 0, 1500)` with
  addresses `192.168.1.1/24` and `127.0.0.1/8`. That is smoltcp's
  `tests::setup(Medium::Ip)` (`src/tests.rs:8-56`) with `TestingDevice`'s
  MTU of 1500. Buffers default to 64 octets, and `ack_delay` is turned off,
  as in `socket_with_buffer_sizes`.
- `test_close_wait_no_window_update` and `test_time_wait_no_window_update`
  cannot fail on the state restriction they are named after, in smoltcp
  either: the window grows from 60 to 64 octets, and only a window that
  has doubled is announced in any state. The Go-only
  `TestWindowGrowthIsNotAnnouncedAfterThePeersFIN` (`dispatch_test.go`)
  grows it from 4 to 64 octets after the peer's FIN, in both states; its
  counterpart, `TestWindowGrowthIsAnnouncedWhileThePeerMaySend`, checks
  FIN-WAIT-1 and FIN-WAIT-2.
- **Go-specific**:
  - the zero/negative duration options; `SetKeepAlive` of zero or less
    arming no keep-alive, as only turning keep-alive on arms one, due at
    once (`TestSetKeepAliveOffArmsNoKeepAlive`), and turning keep-alive off
    letting a keep-alive already timed go out, as smoltcp's
    `set_keep_alive` does (`TestTurningKeepAliveOffLetsTheKeepAliveAlreadyTimedGo`);
  - `LocalEndpoint`/`RemoteEndpoint`: the zero `AddrPort` before `Connect`,
    while listening and after a received RST; the four-tuple kept after
    `Abort`, a timeout and `Close` in SYN-SENT until the RST has been sent,
    and in TIME-WAIT;
  - `Listen` and `Connect` refusing addresses that are not IPv4 (IPv6,
    IPv4-mapped) with `ErrUnaddressable`;
  - `PeekSlice` working in any state;
  - the exact expiry of the delayed-ACK timer (nothing at 9 ms of a 10 ms
    delay, the ACK at 10 ms), which no smoltcp test pins;
  - what smoltcp's tests leave unpinned: the local MSS following the IP
    MTU (`TestLocalMSSFollowsTheIPMTU`); the peer's MSS — none or 0 gives
    536-octet segments, a tiny one 48 (`TestSegmentsFollowThePeersMSS`);
    the peer's window scale shifting its later windows
    (`TestThePeersWindowScaleAppliesToItsLaterSegments`); a large window
    saturating the field at 65535 for a peer without window scaling, in
    both opens (`TestALargeWindowSaturatesTheFieldForAPeerWithoutWindowScaling`);
    RFC 6298 (5.1), a running retransmission timer not restarted by new
    data (`TestLaterDataDoesNotRestartTheRetransmissionTimer`); each RTT
    measurement ending its sample (`TestEachRoundTripIsTimedFromItsOwnSegment`,
    with round trips of 100 ms and 900 ms, an RTO of 1.152 s, so that the
    second is seen to be measured at all; `TestRTTEstimatorMeasuresASampleOnce`),
    and only an ACK that reaches the timed segment's end ending it: one
    beyond it measures the segment's round trip, one short of it nothing
    (`TestAnACKBeyondTheTimedSegmentMeasuresItsRoundTrip`,
    `TestAnACKShortOfTheTimedSegmentMeasuresNothing`); the RTO's 5 ms margin
    (`TestRTTEstimatorKeepsAMarginOfFiveMilliseconds`); the handshake from
    ISNs in both halves of sequence space and at its wrap (§5): a peer's SYN
    without an ACK acknowledging nothing
    (`TestAPeersSYNWithoutAnACKAcknowledgesNothingWhateverTheISN`), and the
    SYN timed (`TestTheSYNIsTimedWhateverTheISN`); a closed socket
    accepting nothing, though it keeps a listen endpoint or a four-tuple
    (`TestAClosedSocketAcceptsNothing`); data without an ACK dropped
    (`TestDataWithoutAnACKIsDropped`); `Connect` refusing port 0 on either
    side alone (`TestConnectRefusesPortZeroOnEitherSide`); `Listen` on
    another endpoint while listening refused
    (`TestListenOnAnotherEndpointWhileListeningIsRefused`); a FIN dropped
    once the peer's FIN has been taken, the segments at the window's start
    that CLOSE-WAIT, CLOSING, LAST-ACK and TIME-WAIT drop whole
    (`TestSegmentsTheStateDoesNotTakeAreDropped`), and a segment beyond a
    hole that CLOSE-WAIT and CLOSING take without its FIN
    (`TestCloseWaitAndClosingTakeASegmentBeyondAHoleWithoutItsFIN`); out-of-window data with
    FIN or PSH answered every time (`TestRetransmittedDataIsAnsweredEveryTime`);
    window growth announced in FIN-WAIT-1 and FIN-WAIT-2, and, with a window
    scale, a window that has doubled announced at once, compared in the
    field's scaled units (`TestScaledWindowGrowthIsAnnouncedAtOnce`: a
    128 KiB buffer, the field going from 1024 to 2048); the SACK block of a
    segment that extends a block another block follows being the whole of
    that block (`TestSACKBlockOfASegmentThatExtendsABlockIsTheWholeBlock`);
    the ACK delay not
    restarted by more data (`TestMoreDataDoesNotRestartTheACKDelay`), out of
    order data starting none (`TestOutOfOrderDataStartsNoACKDelay`), and
    data that fills a gap starting one that its immediate ACK leaves
    running, as in smoltcp (`TestDataThatFillsAGapStartsAnACKDelayItsImmediateACKLeavesRunning`);
    no endpoints and no RST after LAST-ACK's final ACK
    (`TestEndpointsAreZeroWithoutAConnection`); CLOSING lasting until the
    socket's FIN is acknowledged, the FIN retransmitted
    (`TestClosingLastsUntilItsFINIsAcknowledged`); TIME-WAIT lasting 10 s
    (`TestTimeWaitLastsTenSeconds`); the timeout aborting an idle
    connection, in ESTABLISHED and FIN-WAIT-2 (`TestTimeoutAbortsAnIdleConnection`),
    and a send of nothing keeping its clock running
    (`TestSendingNothingKeepsTheTimeoutClockRunning`); unread data
    discarded at the end of TIME-WAIT (`TestTheEndOfTimeWaitDiscardsUnreadData`);
    the address-change reset emptying both buffers
    (`TestLosingTheLocalAddressEmptiesBothBuffers`); a reused socket
    starting with empty buffers and an empty assembler
    (`TestAReusedSocketStartsWithNothingLeftOver`,
    `TestAReusedSocketReceivesOnlyTheNewConnectionsData`); the listen
    endpoint forgotten by every reset but kept by `Close`
    (`TestTheListenEndpointIsForgottenWhenTheSocketResets`); the shift of
    15 for a 1 GiB buffer (`TestWindowScaleOfTheLargestReceiveBuffers`);
    data queued while probing keeping the probe schedule
    (`TestDataQueuedWhileProbingKeepsTheProbeSchedule`); a segment whose
    challenge ACK the rate limit withholds dropped all the same
    (`TestASegmentWhoseChallengeACKIsWithheldIsDropped`); a RST's data
    never queued, in ESTABLISHED and in a SYN-RECEIVED that came from
    LISTEN (`TestTheDataOfARSTIsNotQueued`); SYN-RECEIVED taking no data
    before its SYN|ACK announces a window (`TestNoDataIsTakenBeforeTheSYNACKAnnouncesAWindow`);
    data that arrives with a SYN neither taken nor acknowledged, in a
    passive and an active open, and taken when the peer sends it again
    (`TestDataArrivingWithASYNIsNeitherTakenNorAcknowledged`);
    keep-alive resuming after an ACK of everything sent, when the probed
    window opens, and after a retransmission timeout that cannot resend
    (`TestKeepAliveResumesOnceEverythingSentIsAcknowledged`,
    `TestKeepAliveResumesWhenTheProbedWindowOpens`,
    `TestKeepAliveResumesAfterARetransmissionTimeoutIntoAClosedWindow`); a
    fast retransmission of the last data ending it with PSH, or with the
    FIN once closed (`TestAFastRetransmissionOfTheLastDataEndsItAsItsFirstTransmissionDid`);
    and the deviations of §9 (`TestTheWindowOfASYNReachesNoFurtherThanTheBuffer`,
    `TestAPeersSYNBeforeTheSocketsOwnIsAnsweredAtOnce`,
    `TestASYNACKToASocketWithoutAReceiveBufferIsAcknowledgedAtOnce`,
    `TestAFINAfterDataCutAtTheWindowsEndWaitsForTheRest`,
    `TestAFINAfterDataCutAtTheWindowsEndIsIgnoredInEveryStateThatTakesAFIN`,
    `TestAWindowThatEndsBeforeItStartsTakesNoSegmentHalfTheSequenceSpaceAway`,
    `TestAWindowThatEndsBeforeItStartsAnswersASegmentAtItsStartWithAChallengeACK`).
    `TestChallengeACKsAreLimitedToOnePerSecond` and
    `TestRTTEstimatorStartsAfreshAfterThreeBackoffs` state their values as
    literals (999 ms and 1 s; 2 and 3 backoffs);
  - the pull model (`emit` refused), in `pull_model_test.go`:
    - after a refused `emit`, the next `Dispatch` at the same time sends
      the identical segment (sequence and acknowledgement numbers, flags,
      window and payload);
    - a retransmission timeout that fires while `emit` is refused: the
      retransmission goes out at the next `Dispatch`, and the RTO has
      doubled exactly once;
    - a refused SYN still starts the timeout clock: with `SetTimeout(10 s)`,
      `emit` refused at 0 s and granted at 8 s, the socket aborts at 10 s,
      not at 18 s;
    - a timeout makes `State()` report `StateClosed` at once, even while
      `emit` is refused, and the RST goes out at the next `Dispatch`;
  - `State.String`;
  - the behaviour §8 says the consumers rely on.
- **The fuzz target** (`fuzz_test.go`), `FuzzSocketAgainstAHostilePeer`:
  a peer that sends any segments (only those `Accepts` takes), an
  application that sends, receives, peeks, closes, aborts and reopens at
  any moment, and an interface that polls over a line that refuses
  segments at times and whose address leaves and returns, against one
  socket set up as a config octet says (the buffers, one a 128 KiB receive
  buffer that needs a window scale; listen or connect; Nagle, ACK delay,
  timeout and keep-alive; the socket's initial sequence numbers in the
  lower half, from `localSeq`, or in the upper half, `configUpperHalfISN`).
  The peer's segments may also come from about half the sequence space past
  the receive window's start (`peerFlagHalfSpaceSeq` adds 2³¹ − 1 to a
  segment's offset from the sequence number the socket expects next, so
  that with the signed offset octet it reaches 2³¹ too), which the run
  keeps out of its account of the peer's stream, as that account compares
  modulo 2³². Besides never panicking,
  and `PollAt` working
  in every state, it checks that the application reads only the peer's
  octets at their sequence numbers, up to the FIN; that a FIN is taken
  only from a segment that carries one, where the peer sent it, after
  every octet before it, unless the socket held octets beyond it (a peer
  that sends data beyond its own FIN, which the socket, like smoltcp,
  queues in CLOSE-WAIT); that no acknowledgement goes beyond the peer's
  furthest sequence number, but a RST's or one after such a FIN; that sent
  data is the application's, at its sequence numbers, no longer than the
  effective MSS, and within the peer's window but for zero-window probes
  and fast retransmissions; that a RST is never answered; that replies
  carry no data; and that the assembler holds nothing beyond the receive
  buffer. go test runs its 18 seeds, 17 scripts — `peerSYNBeforeTheSocketsOwnScript`
  runs in both halves of sequence space, which catches an absent ACK read
  as sequence number 0 — among them the crashers of §9, of which
  `farSegmentsAfterAFINAtTheWindowsEndScript` is the latest;
  no crasher remains, so there is no `testdata/fuzz`. Run it with
  `go test -run='^$' -fuzz='^FuzzSocketAgainstAHostilePeer$' -fuzztime=60s -parallel=2 ./internal/ipstack/tcp`;
  the status line's exec count pauses while the engine minimizes new
  corpus entries, which `-fuzzminimizetime=0` avoids. Over the review, some
  60 million executions found nothing more.

### `udp`

- **`src/socket/udp.rs`**: `test_bind_unaddressable`, `test_bind_twice`, `test_send_unaddressable`,
  `test_send_with_source`, `test_send_dispatch`, `test_recv_process`, `test_peek_process`, `test_recv_truncated_slice`,
  `test_peek_truncated_slice`, `test_doesnt_accept_wrong_port`, `test_doesnt_accept_wrong_ip`,
  `test_send_large_packet`, `test_process_empty_payload`, `test_closing`,
  each in its `Medium::Ip` case.
  - Not ported: `test_set_hop_limit`, `test_set_hop_limit_zero` (§3).
- **Go-specific**: `Bind` refusing an address that is not IPv4; `Send`,
  `SendWith` and `SendSlice` refusing a destination that is not IPv4 (an
  IPv6 address, an IPv4-mapped one, the zero `Addr` with a port) and a
  `LocalAddress` that is set but not IPv4, all with `ErrUnaddressable`
  and nothing queued; `Send` and `SendWith` panicking on a negative size
  with the texts of §9, before any other check, so that an unbound socket
  panics too;
  `SendWith` not calling its callback when the datagram does not fit;
  the choice of source address; a datagram without one being dropped;
  `PollAt`; accepting broadcasts and multicasts on an address-bound
  socket; the capacities and queue lengths, padding included.

### `dns`

smoltcp has no tests of `src/socket/dns.rs`. The Go-specific ones cover:

- `StartQuery` validation: an empty name, empty labels, a 64-octet label,
  names of 255 and 256 octets, and a trailing dot.
- The first query: recursion desired, to port 53. `StartQuery` draws the
  transaction id (`Uint16`) and then the source port (`SourcePort`), so
  with a fresh seed-0 context the query has id 0x3 and source port 1358.
- Retransmission: a server gets sends at 0, 1, 3 and 7 s after its first
  attempt (delays doubling from 1 s, capped at 10 s). At the first
  `Dispatch` more than 10 s after that attempt, the next server gets the
  query at once, with the delay back at 1 s. After the last server, the
  query fails.
- The 10 s server timeout runs from the first attempt even when `emit`
  refused it, and a refused retransmission is sent at the next `Dispatch`
  with the same delay.
- Failure on NXDOMAIN (of every query the response matches) and on a
  response with no address.
- Keeping at most `MaxResultCount` addresses.
- CNAME following, the CNAME's target becoming the query's name.
- Ignoring a response with a mismatched id or port, a non-response, another
  opcode, two questions, another question, or a malformed message or
  answer.
- `TakeQueryResult` ending the query (the slot and handle are reused).
- `CancelQuery`, and the panics of `TakeQueryResult` and `CancelQuery`
  on a spent handle and on the zero `QueryHandle`, each checked against its
  text.
- `NewSocket` and `UpdateServers` keeping the first two servers, and a
  query in progress continuing with the new ones; the zero `Socket`, a
  resolver without servers like `NewSocket(nil)`
  (`TestZeroSocketIsAResolverWithoutServers`).
- Accepting responses from port 5353.
- An unspecified server, a server that is not IPv4, no server, or no
  source address, failing the query.
- `PollAt`.
- The unexported name helpers: `encodeName`, `copyName` and
  `namesEqual`.

### `ipstack`

- **`src/iface/interface/tests/mod.rs`**: `test_handle_udp_broadcast` (`Medium::Ip`), `tcp_listen_drops_unspecified_src`.
  - `tcp_not_accepted` needs IPv6. It is ported to IPv4 as
    `TestTCPNotAccepted`: an unmatched SYN gets RST|ACK, and a segment to
    0.0.0.0 gets no reply.
  - Not ported: `test_new_panic`, which needs Ethernet.
- **`src/iface/interface/tests/ipv4.rs`** (`Medium::Ip`): `test_no_icmp_no_unicast`, `test_icmp_error_no_payload`,
  `test_icmp_error_port_unreachable`, `test_icmp_reply_size`.
  - Not ported, because they need Ethernet, auto-icmp-echo-reply, the ICMP
    socket, multicast, fragmentation or raw sockets:
    `test_any_ip_accept_arp`, `test_handle_ipv4_broadcast`,
    `test_handle_valid_arp_request`, `test_handle_other_arp_request`,
    `test_arp_flush_after_update_ip`, `test_icmpv4_socket`,
    `test_handle_igmp`, `test_packet_len`, `test_raw_socket_no_reply_udp`,
    `test_raw_socket_no_reply_tcp`, `test_raw_socket_with_udp_socket`,
    `test_raw_socket_tx_fragmentation`, `test_raw_socket_rx_fragmentation`,
    `test_ipv4_fragment_size`.
  - `test_local_subnet_broadcasts`, `get_source_address` and
    `get_source_address_empty_interface` are ported as `socket.Context`
    tests.
- **`src/iface/socket_set.rs`**: smoltcp has no tests here. The Go-specific
  ones cover lowest-free-slot reuse; `String` (`#0`, and `#invalid` for
  the zero handle); the typed accessors panicking on a wrong kind or an
  invalid handle; `Remove`; iteration order, and iterators that stop when
  the loop does; the zero `SocketSet`.
- **Go-specific** interface tests. These are the pull-model guarantees the
  consumers rely on (§8):
  - When `Transmit` refuses, egress stops and the socket's segment goes out
    unchanged at the next poll.
  - The reply slot of a received datagram is used even while `Transmit`
    refuses.
  - The first accepting TCP socket in slot order gets the segment.
  - UDP sockets come before DNS sockets.
  - Any-IP accepts every destination; without it, a datagram for a foreign
    address is dropped.
  - An echo request gets no reply.
  - An unknown protocol gets protocol unreachable.
  - An outgoing datagram larger than the MTU is dropped after taking its
    transmit slot, and counts as sent (a UDP socket dequeues it); an
    incoming one is processed.
  - An empty frame counts as processed (`IngressPacketProcessed`); every
    other datagram, even one that is dropped, as `IngressSocketStateChanged`.
  - `PollIngressSingle` processes exactly one datagram and sends only its
    reply, through the reply slot (it never calls `Transmit`), and returns
    `IngressNone` when nothing is waiting.
  - A source-address change resets TCP connections without a RST.
  - A seed gives the sockets smoltcp's random values: after `NewInterface`,
    the first `Uint32` is smoltcp's first ISN for seeds 0 (0xfdecf8a2),
    0x5EED (0x9ee15bb3) and 0x2e80 (0x4b4ee653; its first `Uint16` is 0 and
    is drawn again), and a seed-0 interface's first DNS query has id 0x54e
    and port 57331 (`TestASeedGivesTheSocketsSmoltcpsRandomValues`,
    `TestFragmentIdentifierIsTheFirstUint16ThatIsNotZero`,
    `TestUDPSocketsComeBeforeDNSSockets`); the dns tests, which use
    `socket.NewContext` alone, see id 0x3 and port 1358 instead.
  - No ICMP error answers a datagram for a subnet broadcast of either of
    the interface's networks, the limited broadcast, 224.0.0.1 or, with
    any-IP, a foreign group, whether UDP to a port without a socket or an
    unknown protocol (`TestNoICMPErrorAnswersADatagramForAGroupOrABroadcast`).
  - `PollAt` and `PollDelay`, a new DNS query's deadline lying in the past.
  - Every poll setting the context's time.
  - `NewInterface` panics below `MinMTU`.
  - `SetIPAddrs` errors.
  - Malformed datagrams, non-unicast sources and foreign multicast groups
    dropped; datagrams from 0.0.0.0 and for the interface's broadcasts and
    224.0.0.1 accepted.
  - `Example`: a UDP echo server on an interface.

### End to end: the loopback tests

`loopback_test.go` (package `ipstack_test`, exported API only) joins two
interfaces with an in-memory line, as PPP joins the two stacks of the
workspace. The client is the app's stack as satnet sets it up: one /32
address (10.64.0.2), polled with `Poll`, its TCP sockets tuned as satnet
tunes them (2048/4096-octet buffers, Nagle off, a 100 ms ACK delay, the
120 s timeout and 60 s keep-alive). The other end is the simulated
internet as sim sets it up: the gateway's /32 address (10.64.0.1) and
any-IP, so it answers for every destination, polled one datagram at a
time with `PollIngressSingle` and then `Poll`, its services with
4096-octet buffers and the stack's defaults. Both devices work in the pull
model: each received datagram comes with a reply slot that always takes
the answer, while transmit slots are granted only while the line is free.
The line records every datagram, and can be busy or lose chosen ones. The
clock is simulated: it jumps to the next socket timer, or by satnet's
driver interval (20 ms) while a socket waits for a busy line, so the tests
run in milliseconds. The MTU is 576.

- **Bulk data both ways and an orderly close**: 20 000 octets from the
  client and 30 000 from the internet over one connection; each end reads
  exactly what the other sent; every segment from the internet comes from
  the address the client wrote to; no RST; the client's FIN first; the
  client in TIME-WAIT for 10 s before closing.
- **Recovery from a lost segment**, for each of: the client's SYN, the
  internet's SYN|ACK, a data segment of each end, each end's FIN, and the
  client's ACK of the internet's FIN. The data still arrives intact, and
  the lost segment's sender sends it again.
- **A refused transmit slot**: the client's line is busy for 3 s, longer
  than the initial RTO, in the middle of the bulk exchange. The device
  refuses slots, the client sends nothing through a transmit slot until
  the line is free but still answers through reply slots, and the exchange
  completes intact.
- **UDP both ways**: datagrams to the simulator's UDP echo on two hosts
  arrive with the client's endpoint and the destination as their local
  address, and the echoes come back from those destinations.
- **DNS**: a resolver asks the gateway, and a DNS server built by hand on a
  UDP socket of the internet answers: one A record, two (for a name with a
  trailing dot), an alias through its CNAME record, and NXDOMAIN, which
  fails the query. The server is asked once, recursion desired.
- **The fallbacks**: a UDP datagram to a port without a socket gets ICMP
  port unreachable through the reply slot, from the destination it was
  sent to and quoting it, and nothing answers the error; a SYN to a port
  without a listener gets RST|ACK through the reply slot, and the client's
  connection is refused (CLOSED, with no timer left).

### Not ported elsewhere

These smoltcp tests compile with the workspace's features but test what
the port replaces or leaves out:

- `src/parsers.rs` (`test_ip_ipv4`, `test_cidr_ipv4`, `test_endpoint_ipv4`):
  `netip.ParseAddr`, `ParsePrefix` and `ParseAddrPort` replace `FromStr`.
- `src/time.rs` (11 tests): package `time` replaces `Instant` and
  `Duration`.
- `src/iface/socket_meta.rs` (6 tests): the neighbour metadata is omitted
  on `Medium::Ip` (§3).
- `src/iface/route.rs` (`test_fill`): routes are omitted (§3).
- `src/iface/fragmentation.rs` (`packet_assembler_overlap`,
  `packet_assembler_assemble`, `packet_assembler_out_of_order_assemble`,
  `packet_assembler_set`, `packet_assembler_set_full`,
  `packet_assembler_set_assembling_many`): the reassembly buffer they test
  is omitted with reassembly (§3).
- `src/phy/tracer.rs` (`test_tracer`, `test_tracer_packet_display_ip`): the
  phy middleware is omitted (§3).
- `src/wire/mod.rs` `parse_hardware_address` has cases for Ethernet and
  IEEE 802.15.4 only, so none is compiled here.

### Coverage

The target is at least 90% statement coverage for `wire`, `storage`,
`socket`, `tcp` and `udp`, and at least 85% for `dns` and `ipstack`.
`go test -race -cover ./internal/ipstack/...` reports:

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

What is left uncovered is defensive code no test can reach through the
API: in `storage`, `enqueuePadding`'s error for a full metadata ring,
which `admits` rules out — kept, as smoltcp has
the same `?` and no error is ignored silently; in `tcp`, smoltcp's guards for
states that cannot occur (a listening socket's transmit, the SYN term of
the ACK range check, an ACK below the transmit buffer, SYN-SENT's
non-SYN transition, a RST in LISTEN, the trace of a flag combination no
segment has), and `NewSocket`'s panic for a receive buffer over 1 GiB,
which only allocating 1 GiB could reach.

## 7. Pitfalls

- `TCPRepr` is not comparable. Use `Equal`.
- `time.Time` values compare with `==` only when they were built the same
  way. Tests should derive every time from one epoch. `PollAt.Equal` uses
  `time.Time.Equal`.
- `TCPSeqNumber` comparisons must go through `Compare` (wrap-around). Plain
  `<` on the int32 is a bug.
- `Optional` is generic over comparable `T`. `Optional[SACKRange]` and
  `Optional[TCPTimestamp]` are comparable because those structs are.
- `UDPRepr.Emit` and `TCPRepr.Emit` need a buffer of exactly the right
  length, as smoltcp's do.
- A UDP datagram may be sent with checksum 0 (none) over IPv4.
  `FillChecksum` writes 0xffff for a computed zero.
- `netip.Addr{}.IsUnspecified()` is false. Where an address may be the zero
  `Addr`, check `Is4` first (§4).
- Slices that `storage` hands out have a capacity equal to their length.
  `append` on one reallocates rather than writing into the storage that
  follows it.
- Logging goes through `internal/ipstack/internal/netlog` (`Trace`,
  `Debug`), where smoltcp has `net_trace!`/`net_debug!`. Pass values as slog
  attributes rather than formatting them. A disabled `Trace` or `Debug`
  call still builds its arguments: an address or a Repr costs an allocation
  per call (two for udp's per-datagram traces), so keep such calls out of
  loops over octets. netlog takes its trace level and record key from
  `internal/logtext` (`LevelTrace`, `PackageKey`), as the module's other
  logging packages do.
- The payload slices that `storage.PacketBuffer`, `udp.Socket` and
  `tcp.Socket` hand out (`Send`, `Receive`, `Peek`, and the `SendWith`
  callback's slice) alias the ring storage. They are valid only until the
  buffer or socket is next used, and the interface's next poll counts as a
  use: `Dispatch` may send a `Send` payload as it then is, and `Process`
  may put a later datagram over a received payload (a TCP reset empties the
  receive buffer, which the next connection then fills). Fill a `Send`
  payload at once, and copy a received payload before using the socket
  again, even to echo it with `SendSlice` (sim's services clone it, as
  iridium-sim's `services.rs` copies with `to_vec()`). This is Rust's
  borrow rule written out; Go cannot enforce it.

## 8. Behaviour the consumers depend on

`satnet` ports `iridium-net` (`stack.rs`, `device.rs`, `pacing.rs`,
`link.rs`, `tcp.rs`, `udp.rs`, `engine.rs`, `driver.rs`). `sim` ports
`iridium-sim` (`internet.rs`, `services.rs`, `device.rs`,
`connection_request.rs`, `gateway.rs`, `dns.rs` tests, `tests/support`,
`tests/internet.rs`). Their code relies on the following behaviour, which
the port must keep and test.

**Device and polling (the pull model)**

- `Receive` hands over a datagram together with a reply slot, and the stack
  may answer at once through it. The satnet device makes that slot always
  usable, even when the line is busy (`src/phy/mod.rs:406-413`,
  `src/iface/interface/mod.rs:643-709`, `src/socket/tcp.rs:2283`).
- Egress asks the device for a transmit slot only when a socket has a packet
  ready (`src/iface/interface/mod.rs:734-746`).
  - A refusal (`transmit` returning `None`) ends the egress round
    (`:811`). `poll` runs another round only if this one sent something,
    and that round asks the device again.
  - The socket that was refused skips what follows its `emit(…)?`
    (`src/socket/tcp.rs:2791`, `src/socket/dns.rs:641`; `dequeue_with` in
    udp `:535-592`). It records nothing as sent, and keeps what it decided
    before emitting: TCP's timeout clock, timeout abort and retransmission
    backoff, and the DNS server timeout (§5, contract 3). On a busy line
    that matters: a timed-out connection reports CLOSED at once, which
    satnet's connect wait and its removal of closing sockets key on.
  - The next poll therefore makes its choice again. The satnet device
    grants a slot whenever asked and sorts what it is handed itself —
    acknowledgements, SYNs, RSTs and the DNS socket's queries at once, the
    rest per flow, the flows taking turns at the line — and satnet feeds
    each connection what the app wrote a segment per turn, so that about a
    segment per connection waits in front of the line, made when its turn
    came.
- `poll` processes every received datagram, then egress repeatedly until a
  round sends nothing (`src/iface/interface/mod.rs:480-509`).
- `poll_ingress_single` processes one datagram and sends only that
  datagram's reply (`:557-570`). The simulator calls it once per datagram,
  so each connection request in a burst finds a listener prepared for it.
- `poll_at` returns `Instant::ZERO` when a socket says `Now`, the earliest
  time when a socket has a timer, and `None` when every socket waits for
  ingress (`:594-626`). The Go `PollAt` returns `now` itself for `Now`,
  through `PollAt.Deadline(now)` (§5, contract 4). Both mean "poll at
  once". `poll_delay` returns `poll_at - now`, or 0 if that time has passed
  (`:635-641`).
- The MTU is read once, in `Interface::new` (`:207-208`). satnet re-creates
  the interface when PPP's MTU changes.
- A datagram larger than the IP MTU is dropped without error
  (`:1297`, `:1384`), after the device granted its slot. It counts as sent:
  the socket dequeues it, and the round counts as having sent something
  (`:745`). TCP never builds one, because its MSS comes from the IP MTU
  (`TestEstablishedLocalMSSLimitsSegments`, `TestLocalMSSFollowsTheIPMTU`). A
  UDP payload larger than the MTU allows is dropped this way, which is why
  satnet checks `max_udp_payload` first.
- Received datagrams are never checked against the MTU; `process_ip` has no
  such check. satnet relies on this: its stack MTU is the smaller of its own
  MTU and the peer's MRU (`iridium-net/src/driver.rs:134`),
  while the peer may send datagrams up to the app's own, larger MRU.

**Addresses**

- `update_ip_addrs` panics on non-unicast addresses (`:896-902`). satnet and
  sim check before calling it; Go returns an error instead.
- Any-IP accepts datagrams for every address (`:907-916`,
  `src/iface/interface/ipv4.rs:185`). The simulated internet answers for
  every destination with it.
  - The interface may have no address at all: the simulator skips a
    broadcast or multicast gateway address.
  - Replies use the destination address of the datagram they answer (the
    TCP tuple, `udp` `local_address`), not the interface's address.
- Without any-IP, a datagram for an address that is not the interface's own
  is dropped (`src/iface/interface/ipv4.rs:185-211`). The exceptions are the
  limited and subnet broadcasts, and 224.0.0.1
  (`src/iface/interface/mod.rs:928`).
- A datagram with a non-unicast source is dropped, except one from 0.0.0.0
  (`src/iface/interface/ipv4.rs:104-108`).
- A TCP socket resets silently when its local address leaves the interface
  (`src/socket/tcp.rs:2448-2452`), emptying its send buffer as if the peer
  had acknowledged everything (both buffers are emptied:
  `TestLosingTheLocalAddressEmptiesBothBuffers`). satnet relies on this:
  when PPP renegotiates a new address, it records the connections that
  still had data on its way as cut off, so that `WaitAcknowledged` reports
  them reset rather than delivered.

**Socket set**

- `add` puts the socket in the lowest free slot (`src/iface/socket_set.rs:76-90`).
  - Removed handles are therefore reused. satnet keeps sockets in the set
    until their owners let go, so that no handle is reused under them.
  - The simulator forgets closed connections before adding listeners.
- `get`/`get_mut` panic on a wrong type or a free slot (`:98-118`).
  `remove` panics on a free slot (`:124-130`).
- A TCP segment goes to the **first** TCP socket in slot order that accepts
  it (`src/iface/interface/tcp.rs:32-41`).
  - A listener accepts a SYN for its port from anyone
    (`src/socket/tcp.rs:1555-1583`).
  - The simulator relies on this order: it drops repeated SYNs for existing
    connections before polling, because a fresh listener placed in an
    earlier slot would accept the repeat.
- A segment with an unspecified source or destination address is dropped
  before any socket sees it (`src/iface/interface/tcp.rs:14-22`). A segment
  no socket accepts gets a RST, unless it is itself a RST (`:43-56`). The
  RST carries RST|ACK for a SYN without ACK (`src/socket/tcp.rs:1459-1473`).
- A UDP datagram goes to the first UDP socket that accepts it, then to the
  DNS sockets. If none accepts it, it gets ICMP port unreachable, but only
  when both its addresses are unicast (`src/iface/interface/udp.rs:27-65`,
  `src/iface/interface/ipv4.rs:373-418`). The ICMP error quotes at most 528
  octets of the datagram (`src/iface/packet.rs:254-264`).
- A datagram of another protocol gets ICMP protocol unreachable
  (`src/iface/interface/ipv4.rs:240-250`). ICMP messages themselves,
  including echo requests, are ignored (§1).

**TCP** (`src/socket/tcp.rs`)

- State values and their names (`:109-137`). satnet tests these:
  - `Closed` means finished; such a socket can be removed. `TimeWait` is
    over only once the socket reaches `Closed`, so the final ACK still goes
    out.
  - `SynSent`/`SynReceived` mean connecting.
  - `Closed` or `TimeWait` before `Established` means refused, timed out
    or cut off.
  - The simulator's services look for `CloseWait` with nothing left to read.
- `recv_slice` (`:1302-1366`):
  - returns `Ok(0)` when nothing is buffered;
  - returns `Finished` once the peer's FIN is reached, with every octet read
    (end of stream);
  - returns `InvalidState` otherwise, when the receive half is closed
    (connection lost). When this side closed first, the end of TIME-WAIT
    resets the socket, which discards data still unread; `recv_slice` then
    returns `InvalidState`, not `Finished` (`:904-930`, `:2538-2542`;
    `TestTheEndOfTimeWaitDiscardsUnreadData`), and satnet's `TCPConn.Read`
    fails with `ErrConnectionReset`, as its doc says.
- `send_slice` returns `InvalidState` unless the transmit half is open
  (`may_send`: Established or CloseWait). It returns `Ok(0)` when the buffer
  is full (`:1238-1300`).
- `send_queue` counts octets queued but not yet acknowledged
  (`:1392-1395`). satnet's "undelivered data" means that its outbox (what
  the app wrote and the socket has yet to take) and `send_queue` together
  are above zero while `is_active`; `wait_acknowledged` waits for both to be
  empty while `is_open`, and fails for a connection cut off by an address
  change. The drop in `send_queue` during a poll is what satnet tells its
  device the peer acknowledged: smoltcp has checked that acknowledgement,
  where the segment that carried it may be corrupt or one it ignores.
- `close` sends a FIN after the queued data (`:1084-1107`). `abort` sends
  one RST and closes (`:1114-1116`).
- The predicates (`:1122-1236`):
  - `is_open`: not Closed or TimeWait.
  - `is_active`: additionally not Listen.
  - `may_recv`: Established, FinWait1, FinWait2, or data still buffered.
  - `can_recv`: data buffered.
  - `can_send`: `may_send` and the buffer is not full.
  - `is_listening`.
- `listen` on the endpoint it is already listening on succeeds and changes
  nothing (`:942-985`).
- `local_endpoint`/`remote_endpoint` return the four-tuple (`:888-897`). It
  survives `abort`, a timeout and `close` in SYN-SENT, in CLOSED, until the
  RST has been sent (`:2846-2848`), and it stays in TIME-WAIT. satnet's
  `free_port` treats such ports as in use, and the simulator's
  `has_connection_for` matches repeated SYNs against such sockets.
- `connect` takes the local port explicitly and chooses the local address
  with `get_source_address` (`:1013-1067`).
- The defaults: Nagle on, ACK delay 10 ms, no timeout, no keep-alive
  (`:300`, `:572-632`). satnet turns Nagle off and sets a 100 ms ACK delay,
  its timeout and its keep-alive, then drops keep-alive on close; dropping
  it leaves a keep-alive already timed in place, as smoltcp's
  `set_keep_alive` does, which goes out only if the socket sends nothing
  before it is due (`TestTurningKeepAliveOffLetsTheKeepAliveAlreadyTimedGo`).
- The timeout aborts with a RST once the peer has been silent for the
  timeout, whatever the socket is doing: waiting for an answer, or idle
  with keep-alive off and nothing queued, in FIN-WAIT-2 too
  (`:2292-2297`, `:2470-2473`, `:2886-2893`); smoltcp's `set_timeout` doc
  (`:788-797`) says otherwise, and is wrong. The silence counts from the
  last segment received or, after a quiet spell (a connect, or data queued
  into an empty buffer), from the next dispatch. satnet relies on this:
  closing drops keep-alive, so that a peer that never says goodbye is timed
  out (`TestTimeoutAbortsAnIdleConnection`).
- Retransmission: the initial RTO is 1 s, the minimum 1 s and the maximum
  60 s, with exponential backoff (`:144-156`, `:253-271`). satnet's repeat
  filter exists because a satellite round trip exceeds that initial RTO.
- Data that arrives with a SYN is neither taken nor acknowledged, so the
  peer sends it again: smoltcp takes no payload in LISTEN or SYN-SENT
  (`:1715-1717`) and drops a SYN once synchronized, and there is
  no TCP Fast Open (`TestDataArrivingWithASYNIsNeitherTakenNorAcknowledged`).
  `recv_slice` fails with `InvalidState` before Established
  (`:1302-1313`).

**UDP** (`src/socket/udp.rs`)

- `bind` fails for port 0 or a bound socket (`:217-236`), and in Go for an
  address that is not IPv4 (§4).
- `send_slice` returns `Unaddressable` when the socket is unbound or the
  destination is unspecified or has port 0 (in Go also when an address is
  not IPv4), and `BufferFull` when the packet does not fit, now or ever
  (`:296-386`).
- `recv` returns `Exhausted` when no datagram is waiting (`:388-400`). Each
  received datagram carries the sender as `endpoint` and the address it was
  sent to as `local_address` (`:492-532`).
- A queued datagram goes from its `local_address` if set, else from the
  bound address, else from `get_source_address`. It is dropped if there is
  no such address (`:543-560`). The simulator's echo and DNS services send
  received metadata back, so their answers come from the address the client
  wrote to.
- `send_queue` counts the payload octets still queued, padding included
  (`:464-466`). satnet removes a closing UDP socket once it reaches 0.

**DNS** (`src/socket/dns.rs`)

- Servers are truncated to `DNS_MAX_SERVER_COUNT` (2) (`:152-176`).
  `start_query` needs no server; with none, the query fails at its first
  dispatch. satnet refuses to start a lookup when PPP gave no server.
- `start_query` strips a trailing dot and rejects empty names and labels or
  labels over 63 octets as `InvalidName`, and names over 255 octets as
  `NameTooLong` (`:227-280`). It draws the transaction id and then the
  source port from the RNG, in that order (`:298-299`).
- `get_query_result` (Go: `TakeQueryResult`) returns `Pending`, or frees
  the slot and returns `Completed` (at most 4 addresses) or `Failed`
  (`:318-337`). The handle is then spent; satnet cancels only a query that
  is still pending. `cancel_query` panics on a freed slot (`:345-351`).
- The DNS socket accepts responses from port 53 of a configured server, or
  from port 5353 of anyone (`:369-376`), a cost §3 describes.
  - NXDOMAIN fails the query (`:425-429`).
  - A response with no matching address fails it.
  - A query fails alike whether a server said the name has no address
    (NXDOMAIN, or no A record), answered with a failure such as SERVFAIL,
    or stayed silent. satnet tells these apart itself: it watches the
    datagrams that arrive for a kept server's answer about the name, before
    the stack sees them, and reports a missing name only after one.
  - A CNAME is followed within the same response (`:497-512`).
- A server gets the query at 0, 1, 3 and 7 s after the first attempt: the
  delay starts at 1 s and doubles, capped at 10 s, which a server never
  reaches. The first dispatch more than 10 s after the first attempt moves
  the query to the next server, at once and with the delay back at 1 s.
  After the last server the query fails (`:17-21`, `:532-652`). The timeout
  is not part of `poll_at`, which returns only the next retransmission
  time. satnet's repeat filter drops DNS retransmissions sent before an
  answer could arrive. It recognises them by their unchanged transaction
  id.

**Wire** (used directly by `pacing.rs`, `device.rs`, the stack's lookups
and `connection_request.rs`)

- `Ipv4Packet::new_checked`, `next_header`, `src_addr`/`dst_addr`, `payload`,
  `verify_checksum` (Go `IPv4Packet.VerifyChecksum`).
- `TcpPacket::new_checked`, `syn`/`fin`/`ack`/`rst`, `src_port`/`dst_port`,
  `seq_number().0`, `ack_number().0`, `window_len`, `payload`, and, for a
  segment brought up to date, `set_ack_number`, `set_window_len` and
  `fill_checksum` (Go `SetAckNumber`, `SetWindowLen`, `FillChecksum`), with
  `verify_checksum` in the tests.
- `UdpPacket::new_checked`, `src_port`/`dst_port`, `verify_checksum`,
  `payload` (Go `NewUDPPacket`, `SrcPort`/`DstPort`, `VerifyChecksum`,
  `Payload`).
- `DnsPacket` header accessors — `flags`, `opcode`, `rcode`,
  `question_count` and the payload — and `parse_name` (Go `NameLabels`),
  `DnsQuestion::parse` (Go `ParseDNSQuestion`),
  `DnsRecord::parse`, `DnsRecordData::A`, `DnsRcode` and
  `DnsOpcode::Unknown`.
- Checked constructors never panic on arbitrary bytes. The simulator's
  `never_panics_on_arbitrary_bytes` tests depend on this, and `wire`'s
  fuzz targets check it for every `New…` constructor and `Parse…` function
  (§6).

The tests named in §6 pin each of these behaviours, and the loopback tests
of §6 exercise them end to end, as the two consumers use the stack.

## 9. Where the port deviates from smoltcp

The behaviour is smoltcp's: the same checks in the same order, the same
random draws in the same order, the same timers and the same quirks.
smoltcp's long functions are split into smaller ones, and its modules into
files of one concept each (§2), without changing what they do. The port
deviates only where Go's types allow what Rust's ruled out, where Go has
no counterpart, where smoltcp's text would mislead in Go, or where smoltcp
mishandles a segment, panicking or giving the application a stream other
than the one the peer sent (the tcp package doc names the same four kinds
of segment). smoltcp's other defects stay, as the last part lists.

**Segments smoltcp mishandles**

These are defects of smoltcp 0.14 (PORTING.md, section 3). The Rust
workspace inherits the first, the third and the fourth; the second is in
`poll_at`, which neither it nor satnet and sim call.

- **The window of a SYN.** A SYN announces its window unscaled
  (`src/socket/tcp.rs:2604`, recorded as `remote_last_win` at `:2828`), yet
  smoltcp reads every window it sent with its own scale (`window_end` at
  `:1706-1711`, never clamped to the buffer). With a receive buffer that
  needs a scale — 64 KiB or more, which satnet's `Tuning.TCPReceiveBuffer`
  allows; satnet's default of 2048 and sim's 4096 need none — the window of
  the socket's SYN or SYN|ACK therefore reaches far past the buffer until
  its next segment. smoltcp takes data up to there; queuing it panics
  (`src/storage/ring_buffer.rs:345`, a `debug_assert` at `:2233` first) or,
  with reads in between, hands the application octets the peer never sent.
  The port cuts the window back to the buffer's free space
  (`locateReceiveWindow`), which binds only in that case
  (`TestTheWindowOfASYNReachesNoFurtherThanTheBuffer`). A probe against
  smoltcp 0.14.0, through its public `Interface` as iridium-net drives it,
  shows it: with a 128 KiB buffer, a 6-octet segment at offset 131072 after
  a passive open, and a server's data that comes with its SYN|ACK in an
  active open, fail the `debug_assert` at `:2233`; two in-order segments of
  60 000 octets into a 64 KiB buffer panic at `ring_buffer.rs:345` in a
  release build.
- **A peer's SYN before the socket's own.** A peer's SYN can reach a
  connecting socket before the socket's own SYN has gone out (not yet
  dispatched, or refused by `emit`). The socket has then announced no
  window, `remote_last_ack` is the peer's sequence number, and
  `last_scaled_window` subtracts past zero (`:777-785`, `:782`, through
  `SeqNumber`'s "attempt to subtract sequence numbers with underflow"),
  from `window_to_update` in `poll_at`: smoltcp panics. The port's
  `lastScaledWindow` counts no window left and returns 0, so the socket's
  SYN|ACK is due at once, which its dispatch did anyway
  (`TestAPeersSYNBeforeTheSocketsOwnIsAnsweredAtOnce`). A SYN|ACK that
  answers a SYN which announced a zero window, from a socket without a
  receive buffer, leaves the same state — `remote_last_ack` the peer's
  sequence number, `remote_last_win` 0 — and `poll_at` panics the same
  way: in a probe against smoltcp 0.14.0, after the SYN|ACK is processed
  with `poll_ingress_single`, `Interface::poll_at` panics at `:782`, reached
  from `:2423` and `:2874`. The port's `PollAt` says the ACK is due at once
  (`TestASYNACKToASocketWithoutAReceiveBufferIsAcknowledgedAtOnce`). A FIN
  right at the window's end (the next bullet) leaves no such panic: it
  leaves a state for which `window_to_update` never calls
  `last_scaled_window`. smoltcp's
  `dispatch` checks for an ACK to send before `window_to_update`
  (`:2524-2529`), whether its `emit` then succeeds or not, so only
  `poll_at` (`:2860-2876`) panics, and only when an application calls it,
  or `poll_delay`, between the peer's SYN and the next successful dispatch.
  iridium-net and iridium-sim call only `poll` and `poll_ingress_single`,
  so the Rust workspace never panics here: a probe through iridium-net's
  `Stack` answers such a SYN with its SYN|ACK in the same poll, the line
  busy or not. satnet and sim never call `Interface.PollAt` either; in
  satlink the deviation keeps `PollAt` from panicking as ipstack's own
  tests call it, and as its fuzz target does for the early SYN
  (every receive buffer in `fuzzBuffers` holds octets).
- **A receive window that ends before it starts.** `process` checks a
  segment against a window that runs from `remote_seq_no +
  rx_buffer.len()` to `remote_last_ack + (remote_last_win <<
  remote_win_shift)` (`:1706-1711`). It ends one sequence number before it
  starts when the peer's SYN or FIN takes the first sequence number beyond
  the window last announced, and stays so until the socket's next segment
  announces a window afresh. A SYN does this when it reaches a connecting
  socket before the socket's own SYN has announced a window
  (`remote_last_ack` becomes the peer's sequence number, `:1970`, with
  `remote_last_win` 0), or when it answers a SYN that announced a zero
  window (a receive buffer of no octets). A FIN does it when it lands right
  at the window's end (`:1945`, `:1993`, `:2009`, `:2023`; a full window,
  or `test_zero_window_fin`), which needs no race: it lasts until the
  socket's ACK of the FIN goes out. The acceptability tests (`:1720-1770`)
  compare modulo 2³², where of two numbers 2³¹ apart each compares as
  before the other. So in such a window they take a segment that starts or
  ends 2³¹ − 1 or 2³¹ after the window's start, which is 2³¹ after the
  window's end or its start, for one inside it. smoltcp then panics as it
  cuts the payload ("attempt to subtract sequence numbers with
  underflow"): on an empty segment at either place, on data that ends
  2³¹ − 1 after the start (`overlap_end - segment_start`, `:1782`), and on
  data that starts 2³¹ after it (`overlap_start - window_start`, `:1783`).
  It places data that starts 2³¹ − 1 after the start, or ends 2³¹ after it,
  that far into the assembler (2³¹ − 1 octets, or 2³¹ less its length),
  beyond the receive buffer, where, if the peer allowed SACK, the next SACK
  block overflows (`:1512`, "attempt to add to sequence number with
  unsigned overflow"). And it takes a RST carrying such data and ends the
  connection; a RST needs no acknowledgement number, so its sender needs
  only the peer's sequence numbers. With smoltcp's debug assertions on, as
  in a debug build, the `debug_assert`s at `:1777` (data that ends 2³¹ − 1
  after the start) and `:2233` (data placed beyond the buffer) fire first.
  Probes against smoltcp 0.14.0 through its `Interface` show each case:
  after "abcdef" and a FIN fill a 6-octet window, the peer allowing SACK,
  and after data and a FIN fill a 64-octet one, without SACK.
  The port finds every segment outside such a window
  (`windowEndsBeforeItStarts`): it drops a RST and answers anything else
  with an ACK, as smoltcp answers any segment outside its window. Only
  segments half the sequence space away fare differently: an empty segment
  at the window's start still gets smoltcp's challenge ACK, and the socket
  takes nothing from it
  (`TestAWindowThatEndsBeforeItStartsTakesNoSegmentHalfTheSequenceSpaceAway`,
  whose twelve segments, six 2³¹ − 1 and six 2³¹ after the window's start,
  meet each of the three ways into such a window,
  `TestAWindowThatEndsBeforeItStartsAnswersASegmentAtItsStartWithAChallengeACK`,
  and the fuzz target's flag `peerFlagHalfSpaceSeq` with its seed
  `farSegmentsAfterAFINAtTheWindowsEndScript`). The RFCs agree: no
  sequence number lies inside such a window, and RFC 9293 (3.10.7.4) and
  RFC 5961 (3.2) drop a RST outside the window.
- **A FIN after data the receive window cut short.** smoltcp 0.14 cuts the
  payload at `window_end.min(segment_end)` (`:1773-1777`) but disregards a
  FIN only when `window_start < segment_start` (`:1867-1876`). So it takes
  the FIN of a segment whose data the window cut short: it ends the stream
  at the window's end and acknowledges one sequence number past the octets
  it holds, so that the peer counts the first cut-off octet as delivered
  and never sends it again; the peer's retransmission of the rest with its
  FIN is then a FIN in CLOSE-WAIT, dropped as unexpected, and the rest is
  lost. RFC 9293 (3.10.7.4) trims a segment to the window "including SYN
  and FIN". (History: smoltcp PR #977, in 0.12, narrowed the FIN check to
  `window_start < segment_start`; PR #1079, in 0.13, moved the cut to the
  advertised window's end without revisiting that check.) In the states
  that take a FIN — SYN-RECEIVED, ESTABLISHED, FIN-WAIT-1, FIN-WAIT-2
  (`takesFIN`) — the port ignores that FIN (`effectiveControl`), and the
  peer sends it again with the rest of its data; the comparison is
  strict, so a FIN right at the window's end (a full or zero window,
  smoltcp's `test_zero_window_fin`) is still taken. In CLOSE-WAIT,
  CLOSING, LAST-ACK and TIME-WAIT, where the peer's FIN was taken already,
  the port does as smoltcp does: it drops the segment whole, takes neither
  its data, its ACK nor its window, and sends nothing — unless a hole
  precedes the segment. Then smoltcp's hole check (`:1867-1876`)
  disregards the FIN, as in every state, and the state handles the segment
  as one without a FIN: CLOSE-WAIT and CLOSING queue its data in the
  assembler, take its ACK and window, and answer with an ACK
  (`TestCloseWaitAndClosingTakeASegmentBeyondAHoleWithoutItsFIN`); LAST-ACK
  answers a segment that acknowledges nothing new with a challenge ACK, and
  closes on one that acknowledges its FIN; TIME-WAIT drops it. The case arises
  with a peer that sends past the window, at any buffer size, and with
  window scaling, where rounding moves the window's end back while the
  peer's last segment is in flight
  (`TestAFINAfterDataCutAtTheWindowsEndWaitsForTheRest`,
  `TestAFINAfterDataCutAtTheWindowsEndIsIgnoredInEveryStateThatTakesAFIN`,
  `TestSegmentsTheStateDoesNotTakeAreDropped`, and the fuzz target's
  FIN-position invariant with its seed `finAfterDataCutAtTheWindowsEndScript`).
  A probe against smoltcp 0.14.0, alone and through iridium-net's `Stack`,
  shows it: with `Tuning::default()` and a server that sends past the
  window, the socket holds 2048 octets, acknowledges 2049 and reports the
  end of the stream, and the 96 octets the server sends again with its FIN
  are dropped in CLOSE-WAIT as unexpected; with a 128 KiB buffer and a peer
  that keeps to every window it was told, 3 octets are lost so.

**Inputs that Rust's types ruled out**

- Only IPv4 addresses enter the stack (§4). `tcp.Socket.Listen` and
  `Connect`, `udp.Socket.Bind` and the UDP `Send` family return
  `ErrUnaddressable` for an address that is not IPv4; a DNS server that is
  not IPv4 fails the query when the query reaches it; `SetIPAddrs`
  returns an error; and the wire functions that write or checksum an
  address panic.
- Sizes are signed in Go. `udp.Socket.Send` and `SendWith` panic on a
  negative size, before the packet buffer is touched, as do
  `storage.PacketBuffer`'s `Enqueue` and `EnqueueWith`, before they change
  the buffer (smoltcp's `usize` rules it out), and the storage containers
  panic on a count outside their range. `tcp.Socket.Send` and
  `Receive` and `udp.Socket.SendWith` panic when their callback returns
  more octets than it was given, where smoltcp's slices would panic.
- `wire.TCPSeqNumber.Add(n)` panics for an `n` below -(2³¹-1), with
  smoltcp's text for its `Sub<usize>` overflow ("attempt to subtract to
  sequence number with unsigned overflow"), where Rust takes the count as
  an unsigned `usize`.
- `NewInterface` panics for an MTU below `MinMTU`, an IPv4 and a TCP
  header (40 octets). smoltcp does not check, and its maximum segment size
  arithmetic would underflow below it.
- The zero `SocketHandle` and the zero `dns.QueryHandle` identify nothing
  (the handle's `String` is `#invalid`), the zero `SocketSet` is an empty
  set ready for use, and the zero `dns.Socket` is a resolver without
  servers.

**Errors instead of panics**

- `SetIPAddrs` returns an error wrapping `socket.ErrInvalidIPAddr` where
  `update_ip_addrs` panics (§5); satnet and sim check before calling it.

**Results**

- `Interface.PollAt` returns `now` itself when a socket wants a poll at
  once, where smoltcp returns `Instant::ZERO`; both mean "poll at once"
  (§8). Like smoltcp's, it may return a time already past: a DNS query not
  yet sent is due at the epoch, the zero `time.Time`.
- Where smoltcp returns an `Option`, Go returns the value with a `bool`
  (`tcp.Socket.Process`, `socket.Context.SourceAddress`,
  `Interface.PollAt`), and where it returns a per-operation error enum,
  per-package sentinels for `errors.Is` (§4).

**Texts**

- Errors keep smoltcp's `Display` texts, except that DNS's start in lower
  case and `wire.ErrMalformed` says "malformed or unsupported packet"
  where smoltcp's `wire::Error` says "wire::Error" (§4).
- `String` methods return smoltcp's `Display` text where smoltcp has one
  (`wire.IPProtocol` names every protocol smoltcp names, `ICMPv4Message`
  every message, `TCPSeqNumber`, `ListenEndpoint`, `tcp.State`), otherwise
  the variant name as Rust's `Debug` prints it (`TCPControl`, `PollResult`,
  `PollIngressSingleResult`, the DNS enums, and `socket.PollAt`'s `Now`,
  `Ingress` and `Time(…)`, whose time is Go's `time.Time` text, as in
  `Time(0001-01-01 00:00:01.5 +0000 UTC)`, where smoltcp's derived `Debug`
  writes `Time(Instant { micros: 1500000 })`). A value without a name prints
  as smoltcp prints it (`Unknown(4)`, and `0x0c` for an `IPProtocol`), or
  as `Type(n)` where smoltcp's type cannot hold it (`State(9)`,
  `PollResult(7)`).
- Log messages are smoltcp's, with the values as slog attributes instead
  of formatted into the text (§7), at `slog.LevelDebug-4` for
  `net_trace!` and `slog.LevelDebug` for `net_debug!`, with `pkg=ipstack`.
- These texts exist only in Go:

| package | situation | kind | text |
|---|---|---|---|
| `ipstack` | `NewInterface` with an MTU below `MinMTU` | panic | `ipstack: an MTU of %d is below MinMTU, %d` |
| `ipstack` | a datagram for 0.0.0.0 reaches `dispatchIP` (smoltcp's `assert!` has no text) | panic | `ipstack: a datagram for the unspecified address` |
| `ipstack` | a datagram larger than the IP MTU is dropped | debug log | `fragmentation is not supported, dropping a datagram larger than the IP MTU` (`len`, `mtu`). smoltcp's text tells the reader to enable its `proto-ipv4-fragmentation` feature, which the port does not have. |
| `ipstack` | a malformed datagram is dropped (smoltcp's `check!`) | trace log | `iface: malformed`, with `what` naming the parser where smoltcp appends the Rust expression |
| `socket` | `SetIPAddrs` of an address an interface cannot have | error wrapping `ErrInvalidIPAddr` | `invalid interface address: IP address %v is not unicast` (smoltcp's panic text), `… is not IPv4`, `… has an invalid prefix length` |
| `wire` | an address that is not IPv4 written or checksummed | panic | `wire: %v is not an IPv4 address` |
| `wire` | an `ICMPv4Repr` of a message it does not represent | panic | `wire: ICMPv4Repr does not represent %v (type %d) messages` |
| `storage` | a count outside a container's range | panic | `storage: %s: count %d is outside 0 to %d` |
| `storage` | `PacketBuffer.Enqueue` or `EnqueueWith` of a negative size | panic | `storage: Enqueue: size %d is negative`, `storage: EnqueueWith: size %d is negative` |
| `tcp` | an ACK reaching `Process` in LISTEN (smoltcp's `unreachable!()`) | panic | `tcp: Process given a segment with an ACK in LISTEN, which Accepts refuses` |
| `tcp` | a FIN that follows data the receive window cut off is ignored | trace log | `ignoring FIN because its data was cut at the receive window's end` (`window_end`, `segment_end`) |
| `udp` | `Send` or `SendWith` of a negative size | panic | `udp: Send: size %d is negative`, `udp: SendWith: size %d is negative` |
| `dns` | `TakeQueryResult` of a spent or zero handle (smoltcp unwraps a `None`) | panic | `Getting the result of a query in a free slot.`, worded as smoltcp's `cancel_query` panic |

**smoltcp's defects the port keeps**

Found during the port's review. They are smoltcp 0.14's, and so the Rust
workspace's; changing them would make the stack's timers or its closing
differ from smoltcp's, which needs a decision of its own, a §9 entry and
tests.

- **Close in SYN-RECEIVED** (`src/socket/tcp.rs:1093`) moves to FIN-WAIT-1,
  which never sends a SYN|ACK again. If the SYN|ACK was already in flight,
  `sent_syn` is false in FIN-WAIT-1 (`:1594`), so the peer's ACK of the
  SYN|ACK passes for the ACK of the FIN (`ack_of_fin`, `:1856`), and the
  socket reaches FIN-WAIT-2 without its FIN ever going out; if the SYN|ACK
  had not gone out yet, the socket sends a bare FIN instead.
- **A FIN left alone by three duplicate ACKs.** If three duplicate ACKs
  arrive while only the socket's FIN is outstanding (FIN-WAIT-1 or
  CLOSING, transmit buffer empty), `countDuplicateACK` replaces the FIN's
  retransmission timer with a fast-retransmit timer; `Dispatch` then idles
  the timer and sets `pendingFastRetransmit`, but `seqToTransmit`'s guard
  that the transmit buffer holds data sends nothing (smoltcp: the guard at
  `:2301`, `set_for_idle` after a fast retransmit). The FIN is never sent
  again: only a keep-alive (satnet drops keep-alive on close) or the
  timeout (satnet's 120 s `TCPTimeout`) ends the connection. Dropping the
  guard is no safe fix, as `pendingFastRetransmit` survives `reset`; one
  would start the fast-retransmit timer only while the transmit buffer
  holds data, so that a FIN alone keeps its RTO.
- **No probe after a retransmission timeout into a closed window.** After
  an ACK of nothing new announcing window 0 while data is in flight, the
  retransmission timeout idles the timer even though data waits, so a
  connection whose window update is lost is never probed (RFC 9293
  3.8.6.1 requires probing): `PollAt` waits for ingress. In satnet the
  next keep-alive (60 s) recovers it; with keep-alive off (the simulator's
  sockets, or satnet after `Close`) only ingress or `TCPTimeout` does. A
  window that closes before the data goes out is probed, as in smoltcp.
- **No retransmission once a shrunken window reopens.** If the peer
  acknowledges part of the data in flight and announces window 0, the
  zero-window-probe timer replaces the retransmission timer, as smoltcp
  starts it on any ACK of new data (`src/socket/tcp.rs:2192-2200`;
  `updateZeroWindowProbeTimer`). Such a peer moves its window's right edge
  back, which RFC 9293 discourages (SHLD-14) but a sender must survive
  (MUST-34), or its scaled window rounds down while data is on its way.
  When everything queued is already in flight, the probes carry no data,
  and when the window reopens the timer goes idle (`:2201-2204`): the data
  beyond the old window stays unacknowledged and nothing sends it again,
  where RFC 9293 has it retransmitted normally (SHLD-16); `PollAt` waits
  for ingress. With more data queued, that data goes out once the window
  reopens and starts the retransmission timer, which resends the rest an
  RTO later. In satnet each keep-alive (60 s) draws a duplicate ACK, whose
  arrival also keeps `TCPTimeout` from firing, and the third brings a fast
  retransmission about three minutes after the window reopened. With
  keep-alive off (the simulator's sockets, or satnet after `Close`),
  nothing the waiting peer sends ends the stall, not even data of its own,
  which is no duplicate ACK; only a timeout, where one is set, ends it, by
  aborting the connection. Reproduced in Go and against smoltcp 0.14.0
  itself, through its `Interface`: after the window reopens, `poll_at` is
  `None` with three octets still queued. A fix would start the
  retransmission timer instead of idling it when the probe timer stops
  with data in flight (`hasDataInFlight`).
- **A valid ACK at the start of a window that ends before it starts** gets
  a challenge ACK and is not taken (`:1740`, the `(true, false)` arm), as
  the port keeps: after a FIN right at the window's end, a peer's ACK that
  arrives before the socket's ACK of the FIN is lost to the socket, and the
  data it acknowledged goes again at the retransmission timeout. smoltcp
  neither panics on the segment nor corrupts a stream
  (`TestAWindowThatEndsBeforeItStartsAnswersASegmentAtItsStartWithAChallengeACK`).
- **DNS answers from port 5353** of any host are accepted (§3).
