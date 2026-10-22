# internal/ipstack design notes

These notes are for anyone who changes `internal/ipstack`, satlink's TCP/IP
stack. They record the rules the code follows, the contracts between its layers,
and the behaviour its consumers rely on. The API is documented in the package
docs (`go doc ./internal/ipstack/...`), which these notes do not repeat.

## Overview

The stack carries IPv4 datagrams between a device and a set of TCP, UDP and DNS
sockets. It has two consumers:

- satnet, which runs it inside the app over the PPP link;
- sim, whose simulated internet uses it to answer for every address.

It is built in layers, one package each, which the `ipstack` package doc
describes. From the bottom up: `wire`, `storage` and `socket`; the sockets
`tcp`, `udp` and `dns`; then `ipstack` itself. `internal/netlog` holds the
logging they share. Each layer uses only the layers below it. `tcp`, `udp` and `dns` never import
`ipstack`, and neither do their test helpers: `ipstack` imports those packages,
so the import would form a cycle.

The stack is pull-driven, as the `ipstack` package doc explains: the device
offers a transmit slot only when it can take a datagram. [Layers and their
contracts](#layers-and-their-contracts) spells out what this requires of each
layer.

The design follows smoltcp 0.14. The package keeps smoltcp's 0BSD licence in
`internal/ipstack/LICENSE-smoltcp`, and every package doc credits it. [Where the
stack departs from smoltcp](#where-the-stack-departs-from-smoltcp) lists the
differences in behaviour that matter.

## What is left out

The stack covers IPv4, TCP, UDP and DNS on a device that carries bare IP
datagrams. Everything else is left out on purpose:

| Left out | Why |
|---|---|
| Ethernet, ARP, IEEE 802.15.4, 6LoWPAN, hardware addresses, the neighbour cache | The PPP link is a bare IP device. |
| IPv6, SLAAC, MLD, NDISC, RPL, IPsec | The link carries IPv4 only. |
| IPv4 fragmentation and reassembly | The parser rejects fragments. A datagram to send that exceeds the IP MTU is dropped and logged at debug level. TCP never builds one, and satnet checks UDP sizes first. |
| Multicast and IGMP | 224.0.0.1, the all-hosts group, is the only group. |
| DHCP, raw sockets, ICMP sockets, mDNS | PPP assigns the address, and nothing else needs them. |
| Echo replies (ping) | Nothing needs them. Incoming ICMP is parsed and dropped. Port and protocol unreachable errors are still sent, for unicast datagrams only. |
| TCP congestion control | The pull model and satnet's pacing decide when to send. The peer's window and the MSS are the only limits on data in flight. |
| TCP timestamps | No consumer needs them. The stack never sends the option and ignores the peer's. `wire` still parses and emits it. |
| A settable hop limit | Every datagram goes out with `wire.DefaultHopLimit`, 64. |
| Routes and maintenance polls | Routes have no effect on a bare IP device. With nothing to fragment, nothing needs maintenance, and egress is a step of `Poll`. |
| Async wakers, pause-SYN-ACK | The consumers poll. |
| Segmentation offload, packet metadata, burst sizes | No device uses them. |
| Capture and fault-injection middleware | The consumers bring their own devices. |
| Pretty printing | `IPv4Repr`, `TCPRepr` and `UDPRepr` have `String` methods for logs. |

Adding any of these needs a decision first. Raise it before writing the code.

**One open decision: DNS answers from port 5353.** The DNS socket accepts a
response from port 5353 of any host, as smoltcp does for mDNS. Here that branch
serves nothing, and it has a cost. Any host that can reach the interface can
answer a pending query from its own port 5353, without forging a server's
address. Only the query's source port and transaction id stand in the way. Both
follow from the `socket.Rand` state, which a few observed TCP initial sequence
numbers reveal. If the branch is dropped, remove it from [smoltcp defects
kept](#smoltcp-defects-kept) and record the change in [DESIGN.md](DESIGN.md).

## Conventions

Code that adds to the API keeps these rules.

**Names**
- Spell words out, and keep Go initialisms: TCP, UDP, IP, DNS, SYN, ACK, FIN,
  RST, MSS, RTT, RTO, SACK, ISN.
- A name that hides a side effect says so. `TakeQueryResult` ends the query,
  hence `Take`.
- `cx` is always the `*socket.Context`, never a `context.Context`.

**Addresses**
- Addresses are `netip.Addr`, endpoints `netip.AddrPort`, and interface
  addresses `netip.Prefix`.
- Only IPv4 addresses enter the stack. Every entry point checks `Is4` and
  refuses anything else with an error. Callers unmap IPv4-mapped addresses
  first.
- The `wire` functions that write or checksum an address panic on anything but
  IPv4. Nothing the stack builds reaches them with anything else.

**Time**
- Times are `time.Time` and `time.Duration`. The zero `time.Time` is the epoch,
  never "no deadline". Where there may be no deadline, the API returns a `bool`
  beside the time.
- A duration option (a timeout, keep-alive or ACK delay) treats zero or less as
  "none".
- A timer that should fire at once starts at the epoch: the zero-window probe a
  send starts, the keep-alive `SetKeepAlive` arms, the challenge-ACK timer.
  Where a time may be absent, use `wire.Optional` or explicit state, never the
  zero time.

**Values and results**
- Wire fields whose zero is a valid value use `wire.Optional[T]`. Elsewhere the
  zero value means absent: a zero address, endpoint or handle.
- A lookup that may find nothing returns the value with a `bool`. Errors are
  per-package sentinels for `errors.Is`.
- Sizes are `int`. A negative size panics, and so does a callback that claims
  more octets than it was given.
- Zero values are usable: the zero `SocketSet` is an empty set, and the zero
  `dns.Socket` is a resolver without servers.
- A method does not take a parameter it never reads. So `tcp.Accepts`,
  `udp.Process`, `udp.PollAt`, `dns.Process` and `dns.PollAt` take no `cx`.

## Layers and their contracts

The code depends on these contracts between the layers.

1. **Context.** `NewInterface` creates the `socket.Context` from the time, the
   random seed and the device's MTU. It sets the time at the start of `Poll`,
   `PollIngressSingle` and `PollAt`, so every socket reads the poll's time.
   Sockets only read the context. The IP MTU is the device's MTU, since devices
   carry bare IP datagrams.
2. **Ingress: the interface hands a datagram to the sockets.** The datagram is
   checked and parsed first. Then:
   - **TCP.** A segment from or to an unspecified address is dropped. Otherwise
     the TCP sockets are tried in slot order. The first whose `Accepts` returns
     true gets the segment through `Process`, and its reply goes out through the
     datagram's reply slot. If no socket accepts the segment, the interface
     answers with `tcp.RSTReply`, unless the segment is itself a RST.
   - **UDP.** The UDP sockets are tried in slot order, then the DNS sockets. If
     none accepts the datagram, the interface answers with ICMP port
     unreachable.
   - Payload slices handed to `Process` alias the received datagram. Sockets
     copy what they keep.
3. **Egress: the sockets hand datagrams to the interface.** In slot order, the
   interface calls each socket's `Dispatch(cx, emit)`.
   - `emit` asks the device for a transmit slot first. If the device refuses,
     `emit` returns an unexported sentinel error and the egress round ends.
   - A datagram larger than the MTU is dropped after its slot is granted. `emit`
     returns nil, so the datagram counts as sent: a UDP or DNS socket dequeues
     it, and the round counts as having sent something. Received datagrams are
     never checked against the MTU.
   - A socket returns `emit`'s error unchanged. It skips what comes after the
     `emit` call, and keeps what it decided before:

     | Socket | Skips on refusal | Keeps |
     |---|---|---|
     | TCP | Recording the segment as sent: the sequence, ACK and window sent, RTT sampling, arming the retransmission timer, the keep-alive and zero-window-probe rewinds, the delayed-ACK reset, and forgetting the tuple after an abort's RST | The start of the timeout clock, the abort on timeout, a retransmission timeout's rewind and backoff, and the cleared fast-retransmit flag |
     | DNS | Updating the retransmission time and doubling the delay | The server's timeout, which runs from the first attempt, the move to the next server, and failures decided before emitting |
     | UDP | The dequeue | — |

   - The next `Dispatch` makes the same choice again, unless something changed
     in between. A refused fast retransmission is the exception: it is not
     repeated.
   - The payload of an emitted `TCPRepr`, and the payload slice from UDP and
     DNS, alias socket storage. They are valid only during `emit`.
   - `Poll` repeats egress rounds while a round sends something.
4. **PollAt.** The TCP socket's `PollAt` takes `cx`, because the segment it
   would send depends on the IP MTU. The UDP and DNS sockets' `PollAt` take
   nothing. The interface combines the results with `socket.Earliest`, and turns
   them into a deadline with `PollAt.Deadline(now)`.

Inside the TCP socket:

- `reset` replaces the connection record and empties both buffers. A few fields
  on the socket survive a reset, as `tcp/socket.go` documents, so a reused
  socket starts with the previous connection's SACK, duplicate-ACK and
  fast-retransmit state.
- An absent `wire.Optional` holds sequence number 0, so the handshake is tested
  from initial sequence numbers across the whole sequence space.

## Tests

The tests come in five kinds:

- **Scenario tests** follow smoltcp's test scenarios, for the features the stack
  keeps. A TCP scenario test sits in the file for its state or feature.
- **Behaviour tests** pin the Go API and what the consumers rely on.
- **Pull-model tests** (`pull_model_test.go` in `ipstack` and in `tcp`) check
  what a refused socket skips and keeps.
- **Loopback tests** (`loopback*_test.go`) run the whole stack end to end.
- **Fuzz targets** cover every checked `wire` constructor and parser, and the
  TCP socket against a hostile peer. Each target's comment says what it checks,
  and [CONTRIBUTING.md](../CONTRIBUTING.md) has the command. `go test` runs
  their seeds, which include scripts for the [segments smoltcp
  mishandles](#segments-smoltcp-mishandles).

Rules:

- **Names** use full words and Go initialisms. A scenario test is named after
  its scenario, as in `TestSynSentRSTBadACK`.
- **No test sleeps.** Every time is derived from one epoch,
  `time.Time{}.Add(…)`, and passed in. Most test files call `t.Parallel()`.
- **The TCP harness** (`tcp/harness_test.go`) checks the IP header of every
  reply, including that the payload length equals the segment's `BufferLen`. A
  wrong length would otherwise pass every TCP test and surface only in the
  interface tests.
- **Seeded random values are pinned.** The tests fix the values `socket.Rand`
  gives for a seed, and the ISNs and DNS ids an interface gives. `NewInterface`
  draws a fragment identifier before any socket draws, although nothing
  fragments. Removing that draw, or reordering any draw, shifts every pinned
  value.

**The loopback tests** join two interfaces over an in-memory line that stands in
for the PPP link. The client is set up as satnet sets it up, and polled with
`Poll`. The far end is set up as sim sets it up, with any-IP, and polled one
datagram at a time with `PollIngressSingle`. The line can be made busy or lose
chosen datagrams. The clock is simulated, so the tests run in milliseconds.

**Coverage.** The module's bar is 80% statement coverage per package. The
stack's packages are at 99–100%, and a change should keep them there. Only
defensive code that no test can reach through the API stays uncovered, such as
guards for states that cannot occur.

## Pitfalls

- `TCPRepr` is not comparable, because its payload is a slice. Use `Equal`.
- `time.Time` values compare with `==` only when built the same way. Derive
  every time in a test from one epoch. `PollAt.Equal` uses `time.Time.Equal`.
- Sequence numbers wrap, so compare `TCPSeqNumber`s with `Compare`. Plain `<` on
  the underlying `int32` is a bug.
- `TCPRepr.Emit` and `UDPRepr.Emit` need a buffer exactly `BufferLen` octets
  long.
- A UDP datagram may carry checksum 0 (none) over IPv4. A computed checksum of 0
  is sent as 0xffff.
- `IsUnspecified` is false for the zero `netip.Addr`. Where an address may be
  zero, check `Is4` first.
- **Payload slices alias socket storage.** That covers what
  `storage.PacketBuffer`, `udp.Socket` and `tcp.Socket` hand out, and the slice
  a `SendWith` callback fills. Such a slice is valid only until the buffer or
  socket is next used, and the interface's next poll counts as a use. Fill a
  `Send` payload at once, and copy a received payload before using the socket
  again, even to echo it. Go does not enforce this, so the caller must.

## What the consumers rely on

satnet and sim depend on the behaviour below, beyond what the method docs
promise. The tests pin it, and the loopback tests exercise it end to end.
Changing any of it means changing satnet or sim too.

**Device and polling**
- `Receive` hands over a datagram with a reply slot, and the stack may answer
  through it at once. satnet makes the reply slot usable even when the line is
  busy.
- Egress asks for a transmit slot only when a socket has a datagram ready. A
  refusal ends the round, and the next poll chooses again.
- A refusal does not hold up a timeout: a timed-out connection reports CLOSED at
  once. satnet's connect wait and its clean-up of closing sockets depend on
  that.
- `PollIngressSingle` processes one datagram and sends only its reply, through
  the reply slot. It never calls `Transmit`. The simulator calls it once per
  datagram, so each connection request in a burst finds a listener prepared for
  it.
- The MTU is read once, in `NewInterface`. satnet makes a new interface when the
  link's MTU changes.

**Addresses**
- With any-IP on, the interface accepts datagrams for every address, and replies
  come from the address the datagram was sent to. This is how the simulated
  internet answers for every destination.
- Without any-IP, a datagram for a foreign address is dropped. The exceptions
  are the limited broadcast, the subnet broadcasts and 224.0.0.1. A datagram
  with a non-unicast source is dropped, except one from 0.0.0.0.
- A TCP socket whose local address leaves the interface resets silently. It
  empties both buffers as if the peer had acknowledged everything sent. satnet
  relies on this to report such connections as cut off rather than delivered.

**Socket set**
- `AddTCP`, `AddUDP` and `AddDNS` use the lowest free slot, so a removed handle
  is reused. satnet keeps sockets in the set until their owners let go. The
  simulator forgets closed connections before it adds listeners.
- A TCP segment goes to the first socket in slot order that accepts it, and a
  listener accepts a SYN for its port from anyone. So the simulator drops
  repeated SYNs for existing connections before it polls, or a fresh listener in
  an earlier slot would take the repeat.
- A segment no socket accepts gets a RST, unless it is a RST; a SYN without an
  ACK gets RST|ACK. A UDP datagram no socket accepts gets ICMP port unreachable,
  quoting at most 528 octets. A datagram of another protocol gets ICMP protocol
  unreachable.

**TCP**
- The consumers read the states this way:
  - `StateClosed` means finished, and the socket can be removed. `StateTimeWait`
    is over only at `StateClosed`, so the final ACK still goes out.
  - `StateSynSent` and `StateSynReceived` mean connecting.
  - `StateClosed` or `StateTimeWait` before `StateEstablished` means refused,
    timed out or cut off.
  - The simulator's services look for `StateCloseWait` with nothing left to
    read.
- Unread data is discarded when TIME-WAIT ends, and `ReceiveSlice` then fails
  with `ErrInvalidState`, not `ErrFinished`. satnet reports that as a reset
  connection.
- `SendQueue` drops only for an acknowledgement the stack accepted. satnet adds
  its own outbox to it to count undelivered data, and reports each drop to its
  device as acknowledged.
- The four-tuple survives `Abort`, a timeout, and `Close` in SYN-SENT until the
  RST has gone, and it stays through TIME-WAIT. satnet treats such ports as in
  use, and the simulator matches repeated SYNs against such sockets.
- `Connect` takes the local port explicitly, and chooses the local address with
  `SourceAddress`.
- The defaults are Nagle on, a 10 ms ACK delay, no timeout and no keep-alive.
  satnet turns Nagle off, sets a 100 ms ACK delay, a timeout and a keep-alive,
  and drops the keep-alive on close. Dropping a keep-alive leaves one already
  timed in place, which goes out only if the socket sends nothing before it is
  due.
- The timeout aborts with a RST once the peer has been silent that long,
  whatever the socket is doing, FIN-WAIT-2 included. So a peer that never says
  goodbye is timed out. Silence counts from the last segment received or, after
  a quiet spell such as a connect, from the next dispatch.
- The retransmission timeout starts at 1 s, with a 1 s minimum, a 60 s maximum
  and exponential backoff. A satellite round trip is longer than the initial
  RTO, which is why satnet filters early repeats.

**UDP**
- A queued datagram goes out from its `LocalAddress`, else the bound address,
  else the address `SourceAddress` chooses. The simulator's services send the
  received metadata back, so their answers come from the address the client
  wrote to.
- `SendQueue` counts the payload octets still queued, padding included. satnet
  removes a closing UDP socket once it reaches 0.

**DNS**
- `StartQuery` needs no server: without one, the query fails at its first
  dispatch. satnet refuses to look a name up when PPP gave no server.
- A query fails the same way whether a server says the name has no address,
  answers with a failure such as SERVFAIL, or stays silent. satnet tells these
  apart itself, by watching the datagrams that arrive before the stack sees
  them.
- An NXDOMAIN response fails every pending query it matches, not only the first.
  A CNAME replaces the query's name even when the rest of the response turns out
  to be malformed.
- Retransmission follows the schedule in the `dns` package doc. `PollAt` gives
  only the next retransmission time, not the server timeout. satnet's repeat
  filter recognises a retransmission by its unchanged transaction id.

## Where the stack departs from smoltcp

smoltcp is the design source, not a reference the stack must track. This section
is for readers who know smoltcp, or who compare the stack's behaviour against
it.

Most differences follow from the [conventions](#conventions). Only IPv4
addresses enter the stack, and sizes are signed. `SetIPAddrs` returns an error
where smoltcp panics, and `NewInterface` checks the MTU. `PollAt` returns `now`
for "poll at once". The rest are below.

### Segments smoltcp mishandles

On these four kinds of segment, smoltcp 0.14 can panic, or give the application
a stream other than the one the peer sent. The stack handles each one.

- **The window of a SYN.** A SYN announces its window unscaled, but smoltcp
  reads every window it sends with its own scale, and does not clamp it to the
  receive buffer. With a buffer that needs a scale (64 KiB or more), the window
  of the socket's SYN or SYN|ACK reaches far past the buffer. smoltcp takes data
  up to there, and queuing it panics or corrupts the stream. The stack cuts the
  window back to the buffer's free space (`locateReceiveWindow`). satnet's and
  sim's default buffers need no scale, but satnet allows larger ones.
- **A peer's SYN before the socket's own.** A peer's SYN can reach a connecting
  socket before the socket's own SYN has gone out, so the socket has announced
  no window. Computing the last scaled window then underflows, and smoltcp
  panics in `poll_at`. A SYN|ACK to a socket without a receive buffer leaves the
  same state. The stack's `lastScaledWindow` counts no window left, so the reply
  is due at once. The consumers never poll at that moment, but the stack's tests
  and fuzz target do.
- **A receive window that ends before it starts.** After either SYN case above,
  or a peer's FIN right at the window's end, the receive window runs from the
  next expected sequence number to one before it. Sequence comparisons are
  modulo 2^32, so such a window also accepts some segments half the sequence
  space away. smoltcp panics as it cuts their payload, or overflows its
  assembler. The stack finds every segment outside such a window
  (`windowEndsBeforeItStarts`). It drops a RST and answers anything else with an
  ACK, taking nothing from it. RFC 9293 §3.10.7.4 and RFC 5961 §3.2 agree that
  no sequence number lies inside such a window.
- **A FIN after data the receive window cut short.** smoltcp cuts a segment's
  payload at the window's end but still takes its FIN. It ends the stream early
  and acknowledges an octet it never received, which the peer then never
  resends. In the states that take a FIN (SYN-RECEIVED, ESTABLISHED, FIN-WAIT-1,
  FIN-WAIT-2), the stack ignores such a FIN (`effectiveControl`), so the peer
  sends it again with the rest. This follows RFC 9293 §3.10.7.4, which trims a
  segment to the window "including SYN and FIN". A FIN right at the window's end
  is still taken.

### smoltcp defects kept

The stack keeps these smoltcp 0.14 defects. Fixing one changes the stack's
timers or closing behaviour. So each fix needs its own decision and tests, and
removes its entry here.

- **Close in SYN-RECEIVED** moves to FIN-WAIT-1, which never resends the
  SYN|ACK. If the SYN|ACK was in flight, the peer's ACK of it passes for the ACK
  of the FIN. The socket then reaches FIN-WAIT-2 without its FIN ever going out.
- **A FIN left alone by three duplicate ACKs.** Three duplicate ACKs while only
  the FIN is outstanding replace its retransmission timer with a fast
  retransmit. That sends nothing, because the transmit buffer is empty, so the
  FIN is never resent. Only keep-alive or the timeout ends the connection. A fix
  would start the fast-retransmit timer only while the transmit buffer holds
  data. Dropping the buffer guard instead is not safe, because
  `pendingFastRetransmit` survives `reset`.
- **No probe after a retransmission timeout into a closed window.** After an ACK
  of nothing new that announces window 0 while data is in flight, the
  retransmission timeout idles the timer. A connection whose window update is
  lost is then never probed, against RFC 9293 §3.8.6.1. In satnet the next
  keep-alive (60 s) recovers it.
- **No retransmission once a shrunken window reopens.** If the peer acknowledges
  part of the data in flight and announces window 0, the probe timer replaces
  the retransmission timer. When the window reopens, the timer goes idle, and
  the unacknowledged data is never resent (against RFC 9293 SHLD-16). In satnet,
  keep-alive duplicate ACKs bring a fast retransmission after about three
  minutes; with keep-alive off, only a timeout ends the stall. A fix would start
  the retransmission timer when the probe timer stops with data in flight
  (`hasDataInFlight`).
- **A valid ACK at the start of a window that ends before it starts** gets a
  challenge ACK and is not taken. The data it acknowledged goes again at the
  retransmission timeout.
- **DNS answers from port 5353** of any host are accepted. [What is left
  out](#what-is-left-out) describes the cost.
