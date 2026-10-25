# satlink design guide

## Purpose

This guide is for people changing satlink's code. It records the conventions every
package follows, how the moving parts fit together, and the choices a reader might not
expect, each with its reason. It describes the code as it is.

- The doc comments on exported API are the contract between packages. Change an exported
  signature only when the behaviour cannot be kept otherwise, and say so prominently.
- Unexported code is documented where it is declared. This guide does not repeat it, and
  it does not list files or tests.
- The TCP/IP stack, `internal/ipstack`, has its own guide: [IPSTACK.md](IPSTACK.md).
- Tests, fuzzing and the checks every change passes are in
  [CONTRIBUTING.md](../CONTRIBUTING.md).

## Package map

| Package | Role | Key types |
|---|---|---|
| `iridium` | The handset: the AT protocol over any byte transport, with identity, status, SMS, voice and data calls. No I/O of its own. | `Handset`, `Transport`, `DataCall`, `Cancellation`, `Error` |
| `ppp` | A sans-I/O PPP endpoint: HDLC-like framing, LCP, PAP and CHAP-MD5, IPCP; client and server roles. | `Endpoint`, `Config`, `Role` |
| `satnet` | The app-private IP link: PPP and the TCP/IP stack in-process over a data call, with policy, limits, pacing and TLS. | `Link`, `Config`, `Policy`, `TCPConn`, `TLSConn`, `TLSConnector` |
| `sim` | A simulated handset, Iridium gateway and tiny internet. | `Handset`, `Control`, `Config`, `LinkProfile` |
| `serial` | The desktop transport: serial ports, found by USB vendor ID. | `Transport`, `Open`, `Connect`, `Discover` |
| `mobile` | The gomobile API for Android and Apple platforms. | `IridiumHandset`, `SatelliteLink`, `ByteTransport` |
| `cmd/satlink` | The command-line tool. | — |
| `internal/ipstack` | The TCP/IP stack, in layers: `wire`, `storage`, `socket`, `tcp`, `udp`, `dns`. | see [IPSTACK.md](IPSTACK.md) |
| `internal/logtext` | How the module logs, and the one rendering of log records for people. | `Log`, `NewHandler`, `PackageKey` |
| `internal/debugtext` | The quoting and struct texts that messages use. | `Quote`, `IPv4Settings` |
| `internal/ringprovider` | The TLS 1.2 suites and key exchange groups that satnet offers and sim serves. | — |
| `internal/smspdu` | SMS PDUs: the SMS-SUBMIT iridium sends, and the SMS-DELIVER sim stores, both ways. | `Message`, `Decode`, `EncodeSubmit` |
| `internal/utf8lossy`, `asciicase`, `errnotext` | Lossy UTF-8; ASCII-only case folding; OS error texts as strerror(3) writes them. | — |
| `internal/memconn` | An in-memory `net.Conn` that sim's TLS echo runs crypto/tls over. | `Conn` |
| `internal/simtest` | Shared test support: simulators, faulty transports, waiting helpers. | — |
| `internal/logtest` | Shared test support for logging: the process-wide logger for one test, and what a package logs. | `Install`, `Record`, `Line` |

Dependencies point one way. `iridium` imports only the `internal/` text helpers and
`smspdu`, and `ppp` only `utf8lossy`. `satnet` and `sim` build on `iridium`, `ppp` and `ipstack`; `serial` on
`iridium`. `mobile` and `cmd/satlink` sit on top. The module root holds no package, only
the module files and these directories.

The module files are never edited by hand. Do not run `go get` or `go mod tidy`, and do not
build with `-mod=mod`. `go.mod` also pins the `gobind` and `gomobile` tools.

## Conventions

These hold in every package. A reviewer should be able to predict a name or a signature
from them.

### Names

- Initialisms follow Go: SMS, URC, IMEI, AT, TCP, DNS, TLS, IPv4, MTU, MRU, ACCM, PPP, LCP,
  IPCP, PAP, CHAP, USB, ID, DTR, ALPN, URL, HTTP, PEM, DER.
- A name does not repeat its package: `sim.Handset`, `satnet.Config`, `iridium.USBVendorID`.
- Product names say `satlink`: the binary, `SATLINK_PORT`, `linux/99-satlink.rules`,
  goroutines such as `satlink-link`, the CHAP name `satlink-ppp`, and
  `User-Agent: satlink-cli`.
- Hardware facts keep their Iridium names: the `iridium` package, `IridiumInternetNumber`,
  `mobile.IridiumHandset` and `mobile.IridiumUSBVendorID`, `/dev/iridium*`.

### Enumerations

- A plain enumeration is `type X int` with constants `X<Variant>` and a `String` method:
  `iridium.SMSFilterUnread`, `ppp.PhaseOpen`. An input value with no constant is refused.
- An enumeration that carries data is a struct with a `Kind` field of type `XKind` plus
  payload fields, comparable with `==`. Its kinds are named after the struct:
  `satnet.LinkStateUp`, `satnet.CloseReasonCarrierLost`.
- `ppp.Event` and `ppp.IPv4Settings` hold a slice, so they compare with `Equal`. Records
  that hold pointers compare them by pointer, and their docs say so.
- Where a value is a protocol code, the Go value is the code: `iridium.ChargeStatus` holds
  the +CBC code, and `RegistrationStatus` the 3GPP codes.
- `String` returns the text the CLI prints: usually the constant's name without its prefix
  (`OnBattery`, `Home`), and `TypeName(n)` for a value with no constant.
- Choices that callers build but never inspect are opaque values made by constructors
  (`ppp.ClientRole`, `ppp.ServerRole`), so that no invalid state can be expressed.
- `mobile` binds no named integer types, so its enumerations are untyped `int` constants.

### Optional values

| Case | Form | Examples |
|---|---|---|
| The zero value cannot be a real value | the zero value, documented at the field | `Registration.CellID ""`, `Limits.MaxBytes 0` |
| An optional struct | a pointer, nil for none | `DataCallSettings.Bearer`, `satnet.Status.IPv4` |
| A lookup or poll that may find nothing | `(T, bool)` | `Endpoint.PollEvent`, `Watcher.NextStateChange` |
| The zero value is a real value | a presence `bool` beside it | `Battery.Level` with `LevelReported` |
| An optional deadline | `time.Time`, zero for none | `TCPConn.WaitAcknowledged` |

A limit of zero means no limit, and a negative one is refused. In `mobile`, nothing
returned is nil except what is documented as optional.

### Constants and defaults

- Every constant is named and carries its reason in its comment. No value that means
  something appears as a bare literal, and defaults are built from named constants.
- A default that is a struct literal (`satnet.DefaultTuning`, `DefaultLimits`) gives each
  field's reason at the field.
- A struct-valued constant is a function that returns a fresh value, so that no caller can
  change it for everyone: `satnet.IridiumInternetBearer()`, `iridium.KnownProducts()`.
- Defaults are `Default<Type>()`. A constructor with one required input is
  `New<Type>(input)`, and one that validates returns an error. A parser is
  `Parse<Type>(text)`.

### Zero values and interfaces

- Every type says what its zero value is: useful (a zero `satnet.Policy` allows nothing),
  or not usable, naming what makes one.
- A constructor given a nil required interface panics (`iridium.NewHandset`). `mobile`,
  whose callers use other languages, returns an error instead.
- A type that promises an interface asserts it at compile time, so that an optional one
  such as `DTRController` cannot silently stop matching.

### Errors

- Each package's error type is a struct with a `Kind` field plus the fields its kinds need:
  `*iridium.Error`, `*satnet.Error`, `*ppp.ConfigError`, `*mobile.SatlinkError`.
- The kind type implements `error`, so every kind constant (`Err<Variant>`) is a sentinel
  for `errors.Is`. A kind's text is a short label; the error's text has the details.
- `Unwrap` returns the cause: a transport's error, the handset's error inside
  `satnet.ErrHandset`, the crypto/tls error inside `satnet.ErrTLS`.
- satnet's errors also match their standard-library equivalents, so that code written for
  the net package keeps working. The `satnet` package doc has the table.
- Error texts are fixed, and the CLI's golden files pin them. Messages quote strings with
  `debugtext.Quote`, so that every message quotes alike.
- A transport may return any error. The handset wraps it as `ErrTransport`, and passes a
  `*iridium.Error` through unchanged.
- `ErrCancelled` is not `context.Canceled`: a `Cancellation` stopped the call, not a context.
- Never ignore an error silently. Discard one only with `_ = …` and a comment saying why
  that is safe.

### Cancellation

- `DataCallSettings.Cancellation` is a `*iridium.Cancellation`, shared by copies of the
  settings. `DefaultDataCallSettings`, and so `satnet.NewConfig`, makes a fresh one.
- A nil `Cancellation` cannot be cancelled. `Cancel` on nil panics, so that a cancel that
  cannot take effect never passes silently. The zero value is ready to use.
- A context drives one with `stop := context.AfterFunc(ctx, settings.Cancellation.Cancel)`.

### Time

- Sans-I/O code (ppp, ipstack, the simulator's modem, pacing, the name cache) takes
  `now time.Time` and never reads the clock. Only the I/O edges call `time.Now`.
- Blocking calls take `time.Duration` timeouts. Zero or less takes only what has already
  arrived; `math.MaxInt64` means no deadline.
- A wait reads at most a second at a time and looks at its deadline again, so a transport
  is never handed an absurd timeout.
- Conversions from milliseconds or seconds saturate. Configured durations are at most a
  year, and never negative.

### Integers

Counts, levels, indices and sizes are `int`. Ports, USB IDs and the MRU are `uint16`, the
ACCM `uint32`, byte counters `uint64`. `mobile` binds no unsigned types, and a Go `int`
crosses as 32 bits on some targets, so its timeouts, seconds and byte counts are `int64`.

### Handset claims

A data call or a link borrows the handset; it never takes it away.

- `DialData` and `AnswerData` claim the handset. While a `DataCall` or a `satnet.Link`
  holds the claim, the handset's methods that talk to it fail at once with `ErrBusy`.
  `SetTimeout`, `Transport` and `Close` keep working.
- Hanging up releases the claim, whatever the outcome. So do a failed dial and a link that
  fails to come up.
- A failed hang-up is a flag on the error of whatever failed: `CallMayStillBeUp` on
  `*iridium.Error` and `*satnet.Error`. It never changes the text. The field docs list where
  it is set.
- `errors.Is(err, iridium.ErrCallMayStillBeUp)` is the one test for the whole module. It
  matches an error of that kind, or one with the flag, through any wrapping.
- `mobile` reports these as `SatlinkErrorHangUpFailed` and strands the handset (see
  [The mobile boundary](#the-mobile-boundary)).
- Every doc gives the same advice: retry with `Handset.EndDataCall` once the handset
  answers; power-cycling it is the last resort.

### Releasing resources

Each value that holds a resource has a `Close`. Most also clean up after a value dropped
without it, through `runtime.AddCleanup`.

| Value | Released by | Dropped without it |
|---|---|---|
| `serial.Transport` | `Close`: waits up to a second for a write under way, then closes the port | closes its port |
| `sim.Handset` | `Close`, which drops DTR | drops DTR |
| `DataCall` | `HangUp`, in a deferred function | ends nothing; the handset stays busy |
| `satnet.Link` | `Close`, which waits; `RequestClose`, which does not | still hangs up |
| `TCPConn`, `UDPConn` | `Close`: graceful, never waits | closed the same way |
| `TLSConn` | `Close`: close_notify within the write deadline, then a graceful TCP close | its TCP connection closes, without close_notify |
| `mobile` objects | `Close` (`HangUp` for a link), in a `finally` block | the Go value inside cleans up as above |

A connection used after `Close` returns `net.ErrClosed`, and `Close` wakes calls waiting in
other goroutines. A `DataCall` used after `HangUp` fails with `ErrCall`.

### Concurrency

- Every exported type says whether it is safe for concurrent use. The `net.Conn` types are,
  as crypto/tls and net/http rely on. A `Handset` serialises its exchanges.
- Goroutines carry `runtime/pprof` labels and are named in logs.
- Use the typed atomics (`atomic.Int64`, `atomic.Bool`, …), never `atomic.LoadInt64` on
  plain fields: a misaligned 64-bit atomic panics on android/arm. `go vet` does not check
  the alignment. staticcheck's SA1027 does, but only for a 32-bit target, so the
  [checks](../CONTRIBUTING.md#the-checks-every-change-passes) run it for linux/386, which
  aligns fields as android/arm does. `internal/portability` type-checks every package for
  32-bit targets.

### Logging

Producers log with `log/slog` through `slog.Default()`, looked up at log time. The core,
ppp and serial do not log.

- Each record carries a `"pkg"` attribute (`logtext.PackageKey`) naming the package:
  satnet, sim, ipstack or mobile.
- Every logging package logs through `logtext.Log`, which checks `Enabled`, then logs with
  the package attribute. Each wraps it in one-line helpers, one per level it uses; ipstack's
  are in `netlog`.
- satnet and sim format their texts at the call site, with `internal/debugtext` for struct
  values. ipstack passes values as attributes.
- Levels are trace (`logtext.LevelTrace`, debug − 4), debug, info, warn and error.

Consumers render every record the same way, through `internal/logtext`. The CLI prints
`[LEVEL pkg] text` to standard error, from warn up, or from info, debug or trace with `-v`,
`-vv` or `-vvv`. `mobile.SetLogSink` hands its sink the level, the package and the text.

### Tests

[CONTRIBUTING.md](../CONTRIBUTING.md) has the commands. The rules:

- Unit tests sit beside the code they cover. Integration tests (`*_integration_test.go`,
  external packages) drive the public API against the simulator or fake transports.
- Sans-I/O code is tested with explicit `now` values and never sleeps.
- Shared helpers live in `internal/simtest`, `internal/logtest`, `internal/memconn` and the
  `export_test.go` files of `satnet` and `mobile`. They call `t.Helper` and clean up with
  `t.Cleanup`, so that a test ends with no call up and no driver running.
- Test names state the scenario: `TestHangUpEscapesToCommandModeThenHangsUp`.
- Tests wait for conditions with deadlines (`simtest.Eventually`,
  `satnet.AwaitWaitingCall`). A fixed sleep only shows that something does not happen, and
  says why in a comment.
- A test that checks that something wakes a waiting call first waits until the call is
  waiting. Otherwise the wake-up is never exercised.
- `t.Parallel` is for instant links and tests that do not depend on timing. Real-time
  tests run serially, and the slowest are skipped under `-short`. Tests that set the
  default logger never run in parallel.
- ppp's tests check numbers on the wire against literals, not the package's constants.
  Both ends of other tests share those constants, so a wrong one would pass there.
- Coverage: `ppp` at least 90%, `satnet` at least 85%, and at least 80% of statements in
  every other package, `internal/ipstack` included (see its [Tests](IPSTACK.md#tests)).

## Architecture

### The handset's lock and claim

A `Handset` has two guards. A mutex serialises exchanges: the line buffer, the URC queue
and transport I/O. A separate claim marks the handset busy while a data call holds it.

`DialData` and `AnswerData` set the claim with a compare-and-swap before they take the
mutex. Other methods check the claim, take the mutex, and check again, since a dial may
have started meanwhile. So a busy handset reports `ErrBusy` before any other error.

A `DataCall` uses the transport without the mutex, and takes it to hang up. `SetTimeout` and
`Close` never wait for the mutex, so neither waits for a 90-second dial.

### Hanging up

Ending a call is a ladder. Each step runs only if the one before failed:

1. **Escape.** Wait for a guard of silence, send `+++`, and wait for the handset's answer.
2. **ATH.** Hang up from command mode. NO CARRIER counts as success.
3. **DTR.** Drop DTR for 150 ms; after `AT&D2`, which every data call sets, that ends the
   call. Then confirm with ATH.

While the handset may still be online, only its own result codes count as answers. They
end in a bare CR, which PPP always escapes, so the far end cannot fake a hang-up.

Every wait for a hang-up is capped at ten seconds, whatever the command timeout, so that
the next step is never put off for good.

`Initialize` ends a call an earlier session left up. A handset that does not answer may be
relaying one, so it is escaped and hung up. One that answers may still hold a call in
online-command mode, which `AT+CPAS` reveals.

### Setting a call up

A dial or answer that stops before its final result (a timeout, a cancellation, a transport
error) may already have connected. The attempt is aborted, and the handset gets ten seconds
to say how it ended. Only a final result other than CONNECT proves that no call is up;
anything else goes down the hang-up ladder.

A cancellation is looked at every 200 ms. The abort waits until 200 ms after the command
was sent, as a handset may ignore what arrives in the first 125 ms (V.250 §5.6.1).

### satnet's engine and driver

The engine is a mutex with a broadcast channel, closed and replaced on every change, so a
waiter can `select` on it, a timer and a context. Every blocking call goes through one loop,
`waitUntil`. It checks the connection's closed flag, then the operation, then whether the
link is alive, then the deadline. So data already received beats an expired deadline.

"Alive" means usable, or draining after a requested close: reads and waits under way carry
on while written data is delivered. Anything new needs a usable link.

One driver goroutine owns the line. It polls every 20 ms, enforces the limits, and on a
requested close drains for at most `Tuning.CloseGrace`. `Establish` dials before it starts
the goroutine; starting one cannot fail, so nothing after the dial is left to undo.

A panic in the driver is recovered, since one that leaves a goroutine ends the process. The
driver then, each step under a `recover` of its own:

1. hangs up, before even a log line, since a log handler can panic too;
2. records the usage, so that the call's duration counts the hang-up;
3. abandons the engine: marks it closed for the first panic's reason, sets
   `CallMayStillBeUp`, and aborts every socket.

So a link reported closed has ended its call and released the handset, as
`Watcher.WaitClosed` promises and `mobile` relies on.

### satnet's device and pacing

The stack hands datagrams to a device that sorts them before the line:

- **Urgent** datagrams go at once: TCP segments without data or FIN, and the stack's own
  DNS queries.
- **The rest** wait in a queue per flow. Flows take turns, one datagram per line slot, so a
  bulk upload cannot hold up a chat. A resent segment replaces its original while that one
  still waits.
- A waiting segment the peer has since acknowledged, and that tells it nothing new, is
  dropped. One that is sent is first brought up to date with the newest acknowledgement and
  window, since some stacks drop a segment whose acknowledgement looks old.
- ICMP errors are never sent. Each would quote up to 548 bytes of what arrived, perhaps to a
  destination the policy does not allow.

Datagrams leave only as fast as the line carries them, so a backlog waits in the app, not
in the handset's buffer.

### satnet's TLS record buffer

`TLSConn` runs crypto/tls over a record buffer that sits between it and TCP. crypto/tls
alone makes every write error permanent, so a timed-out write would break the session. It
also handles post-handshake messages, such as session tickets, only while reading.

The buffer takes every record crypto/tls writes, at once. `TLSConn.Write` applies the
back-pressure instead, with a 64 KiB limit on pending records. A write that times out
returns what the session took, and the session stays usable. Every other call sends pending
records first. `WaitAcknowledged`, `CloseWrite` and `Close` take in what has arrived without
waiting, so even a connection that never reads keeps its session tickets. The buffer also
records whether TCP ended, so that crypto/tls's `io.EOF` after a bare FIN becomes
`ErrTLSTruncated`.

### The mobile boundary

- **No panic crosses it.** gomobile would end the process, so every exported function that
  reaches the libraries recovers panics into `SatlinkErrorInternal`. So does every
  goroutine `mobile` starts.
- **Nothing returned is nil** unless documented as optional: Swift would throw, and Java
  would get null.
- **Nil arguments are refused** with `SatlinkErrorInvalidArgument`, naming the parameter.
  Only a listener and a cancellation may be nil.
- **Zero-value objects**, which gomobile's constructors make, fail their methods cleanly.
- **Lending.** `EstablishLinkWithHandset` lends the handset to the link. The setup settles
  where it goes exactly once: on to the link, or back.
- **The stranded handset.** A data call that could not be ended, or a panic in the setup,
  strands the `IridiumHandset`. That call is a lent link's, one `Answer` took by mistake or
  found up, or one whose `CONNECT` no command awaited. The handset's methods then fail with
  `SatlinkErrorHangUpFailed` until `Reinitialize` ends the call through the handset's own
  transport. A handset is never lost with its link.
- **The port closer.** A serial link closes its port once its call has ended. It holds only
  the link's watcher, so a dropped link still hangs up.

### The CLI's command line

The CLI reads its command line itself, with no command-line library. Help text, error
messages and option precedence are what users see, and this keeps them under the
package's control. It keeps its own tree of commands, and runs the command the parser
chose directly.

## Deliberate design choices

### The handset stays with its caller

A link or call never takes the handset away. While one holds it, the handset's methods fail
with `ErrBusy`, so a stray command cannot corrupt the call. A `DataCall` dropped without
`HangUp` ends nothing, so hang up in a deferred function. See
[Handset claims](#handset-claims).

### A transport panic pulses DTR

A transport that panics while a call is set up or hung up has DTR dropped and raised again
before the panic goes on; after `AT&D2` that ends the call. The pulse runs under the
handset's lock, so no other command reaches the handset while DTR is down. The transport is
not closed: the caller may recover the panic and use the handset again.

### Data cut off is never reported delivered

`WaitAcknowledged` fails for data that an address change, an `Abort` or the hang-up dropped
before it left: `ErrConnectionReset` after an abort, `ErrLinkDown` after the hang-up. Data
acknowledged before the connection ended still counts as delivered.

### Connections follow net.Conn

TCP and TLS connections have `net.Conn` deadlines, and `Close` wakes waiting calls.
`Link.DialContext` and `TLSConnector.Config` let `net/http` use a link. Data already
received is returned after a deadline has passed. A timed-out `TLSConn.Write` returns the
count the session took, as `io.Writer` requires. Each `Write` goes out whole relative to
other writers, as `net.TCPConn` writes do: an interleaved message helps no caller.

### mobile's timeouts share one deadline per direction

`mobile` applies a call's timeout as the connection's read or write deadline, so two
receives, or two sends, waiting at once share the later timeout. Its docs ask apps to
receive from one thread and send from one thread at a time.

### TLS uses crypto/tls, without certificate compression

crypto/tls does not implement RFC 8879, and adding it would mean forking crypto/tls. Servers
that compress their chains send them uncompressed, so full handshakes with them cost a
quarter to a third more airtime.

### TLS offers a small set of groups and suites

The groups are X25519, P-256 and P-384, with a key share for X25519 only; a post-quantum
share would add about 2.3 KB to every handshake. The TLS 1.2 suites are ECDHE with AES-GCM
or ChaCha20-Poly1305, where crypto/tls's defaults would add four CBC-SHA1 suites. satnet and
sim take both lists from `internal/ringprovider`.

### TLS refuses weak and oversized keys

A chain with an RSA key under 2041 bits (a 2048-bit minimum counted in whole bytes) or over
8192 bits, or an ECDSA key on P-224, is refused; crypto/x509 alone would accept them. One
verified chain without such a key is enough.

### TLS trusts Mozilla's roots, with their distrust dates

The public roots come from `golang.org/x/crypto/x509roots/fallback`, pinned in `go.mod`,
whatever the operating system trusts. Roots Mozilla is phasing out carry NSS's
distrust-after dates, and a chain issued after its root's date is refused.

### TLS accepts the certificates crypto/tls asks for

crypto/tls always asks for ML-DSA and P-521 certificates, and for RSA-PSS signatures before
ECDSA ones, and cannot be told otherwise. Refusing them would fail connections the server
could serve. So a server with several certificates may send an unexpected one, and an ML-DSA
leaf adds about 5 KB to a full handshake. Pin every key the server holds
(`TLSOptions.PinnedPublicKeys`).

### TLS 1.2 sessions resume by ticket only

crypto/tls has no client-side session-ID resumption, so a TLS 1.2 server that issues no
tickets costs a full handshake every time.

### TLS server names are checked before anything is sent

A name that is neither an IP address nor a valid host name fails with `ErrInvalidHost`.
crypto/tls would fail it only at certificate verification, after a full handshake.

### The simulator's TLS echo is always on

So `satlink --simulate net get https://…` reaches the echo, and fails certificate
verification, since the test CA is not a public root, instead of failing to connect.

### `net get https://` ends with close_notify

The CLI ends its TLS session with the TLS goodbye, one alert record more of airtime. A
`TLSConn` able to skip the goodbye would need API that serves only this caller.

### SMS uses PDU mode

Iridium handsets have only PDU mode (ISU AT Command Reference, +CMGF), and refuse text mode
with +CME ERROR. `internal/smspdu` encodes and decodes the PDUs for `iridium` and `sim`, which
refuses text mode too.

- A message is one PDU. Text that does not fit one is refused before anything is sent, and
  the parts of a concatenated message are listed as messages of their own.
- The `<length>` of a listing goes unread: a 9555A's is one more than TS 27.005 says, and the
  PDU holds its own lengths.
- A message names its SMS centre: the SIM's (AT+CSCA?), or Iridium's when it names none. A
  9555A whose SIM names none refuses a PDU that leaves the choice to it.

### Serial writes have no timeout

The serial library offers none, and `Close` does not wake a write blocked in the tty
driver. Exclusive access on Unix is the library's `TIOCEXCL` alone.

### Serial errors read as the C library writes them

An OS error number reads as strerror(3) writes it, with a capital first letter, through
`internal/errnotext`; Go's table lower-cases the texts. The serial library's own errors
keep its wording.

### Ports beyond select(2) fail instead of panicking

On Unix the serial library waits with select(2), which handles descriptors below 1024 only,
and panics beyond them. A process with about a thousand open files reaches that. A read on
such a port fails with `ErrTransport`; any other panic passes on.

### PPP starts over after a Terminate-Ack

A restart after a Terminate-Ack sends a fresh Configure-Request with a new identifier.
RFC 1661 requires a new one once a valid reply has arrived, so that the peer cannot take an
old answer for the new request.

### PPP suggests one value per option

A Configure-Nak suggests one value per option kind, however often the peer's request
repeats it. Otherwise a hostile request could grow the Nak past what the encoder holds.

### PPP credentials print redacted

The types that hold credentials implement `fmt.Formatter`, so every verb prints them
redacted. fmt hands `%p` and `%w` to no method; the `Credentials` doc lists those cases.
Credentials are held by value, so that configurations compare by value.

### Limits refuse values too small to be of use

Besides negative limits, a limit the link would reach before it is any use is refused: a
call time shorter than bringing PPP up and hanging up, an idle time under 25 s, or a data
limit under 2048 bytes. The check runs before dialling, and `satnet.Config.Validate` lets an
app run it sooner.

### Log lines name the package

Every line names the Go package that wrote it, as in
`[INFO  satnet] link closed: closed by the app`. See [Logging](#logging).

### A panic in the CLI exits with status 101

A panic on the command's goroutine is recovered and reported with its stack, after the
handset is closed. Status 2 stays for a command line that does not parse. A panic once the
link is up hangs up first, and fails the command with status 1.

### Output that cannot be written is a failure

A command whose standard output cannot be written, such as a pipe whose reader has gone,
fails with status 1: that is a failure, not a bug. A standard stream the process was never
given counts as no failure.

### Signals and terminals follow the platform

- A SIGTERM the CLI was started with ignored is caught like any other signal.
- With SIGINT ignored at start, an interrupted run exits with status 130 instead of dying by
  SIGINT.
- On Windows, closing the console starts the hang-up before Windows ends the process.
- MSYS and Cygwin terminals are not taken for terminals.
- Styles follow `NO_COLOR`, `CLICOLOR_FORCE`, `CLICOLOR`, `TERM` and `CI`. A Windows console
  with none of them set gets no styles.

### The mobile API keeps to what gomobile binds

Lists are types with `Len` and `Get`, enumerations are int constants, and timeouts are
`int64` milliseconds. Errors carry only their message across the boundary, and
`AsSatlinkError` recovers the details. `Receive` returns a `Received` or a `Datagram`,
because an empty byte array reaches Java as null.

### gobind decides equality

In Java and Kotlin, gobind's `equals` and `hashCode` compare exported fields only, so all
handles of one class compare equal. An exported identity field would give apps a setter for
it, and a Java superclass would need gobind-only imports that break `go build`. The `mobile`
package doc tells apps to compare by identity, and `bindings_test.go` pins the behaviour.
gomobile targets Android and Apple platforms only, so there are no JVM desktop bindings.

### The stack departs from smoltcp where smoltcp mishandles input

`internal/ipstack` is modelled on smoltcp 0.14, but handles four kinds of segment
differently, where smoltcp would panic or hand the app octets the peer never sent. A hostile
peer can reach three of them. Its DNS socket departs from smoltcp too, where a server's
answer would fail a query. It compares names without regard to ASCII case, and keeps a
query's name when a CNAME cannot be copied. IPSTACK.md lists the
[segments smoltcp mishandles](IPSTACK.md#segments-smoltcp-mishandles), the
[DNS names in another case](IPSTACK.md#dns-names-in-another-case),
[a CNAME that cannot be copied](IPSTACK.md#a-cname-that-cannot-be-copied) and the
[smoltcp defects kept](IPSTACK.md#smoltcp-defects-kept).

### DNS answers from port 5353 are accepted

The DNS socket accepts a response from port 5353 of any host, the multicast DNS port, as
smoltcp does. satlink uses no multicast DNS, so the branch serves nothing; it stays until
someone decides to drop it. Its cost: any host that can reach the interface can answer a
pending query without forging a server's address. Only the query's source port and
transaction id stand in the way, and a few observed TCP initial sequence numbers reveal
both. Dropping the branch means updating this entry and IPSTACK.md's
[What is left out](IPSTACK.md#what-is-left-out).
