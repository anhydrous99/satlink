# satlink design guide

This is the maintainers' guide to satlink's code: the packages and the files in
them, the conventions every package follows, the contracts the packages keep
with one another, implementation notes for each package, and the design choices
that differ from what a reader might expect. It is written for people changing
the code. It describes the code as it is now.

The TCP/IP stack, `internal/ipstack`, is modelled on smoltcp's design. Its
notes, including where it departs from smoltcp 0.14, are in
[IPSTACK.md](IPSTACK.md).

The doc comments on exported API are the contract between packages. Change an
exported signature only when the behaviour cannot be kept otherwise, and say so
prominently.

Contents:

1. [File map](#1-file-map)
2. [Conventions](#2-conventions)
3. [Deliberate design choices](#3-deliberate-design-choices)
4. [Implementation notes per package](#4-implementation-notes-per-package)
5. [Messages](#5-messages)

---

## 1. File map

Legend: **API** marks a file that declares exported API; **internal** marks a file
with unexported code only.

Unit tests are internal tests: `<file>_test.go` in the package itself. Integration
tests are external: package `<name>_test`, named `<name>_integration_test.go`, and
they use only the exported API (the sim and satnet tests may also import
`internal/ipstack`, which the module allows). The test strategy is in section 2,
Tests.

### Module and tooling

| File | Role |
|---|---|
| `go.mod`, `go.sum` | Module definition. Never edit them by hand, never run `go get` or `go mod tidy`, and never use `-mod=mod` builds: the default read-only mode builds every approved import as is. `go.mod` also pins the `gobind` and `gomobile` tools. |
| `.gitignore` | Ignores the `satlink` binary left at the top by `go build ./cmd/satlink`, the output of the scripts (`android/build/`, `build/`, `*.aar`, `*.xcframework`), and coverage profiles. |
| `README.md` | Installation with `go install ./cmd/satlink`, the CLI with `--simulate` examples, the library in brief, gomobile builds, the differences listed in section 3, and testing. |
| `android/src/main/java/io/github/anhydrous99/satlink/AndroidUsbTransport.kt` | Package `io.github.anhydrous99.satlink`, importing `io.github.anhydrous99.satlink.mobile.*` (gomobile appends the Go package name to `-javapkg`). Implements `mobile.ByteTransport`: `write`, `read(maxLen: Long, timeoutMillis: Long)` (the timeout fits the `Int` of `bulkTransfer`), `raiseDTR` (DTR and RTS), and `dropDTR` (RTS only). It is still `AutoCloseable` and drops all lines on close. It throws plain `Exception`, because gomobile has no exception subclasses. |
| `android/src/main/java/io/github/anhydrous99/satlink/Example.kt` | Example use of the gomobile API, importing `io.github.anhydrous99.satlink.mobile.*`. Details below the table. |
| `android/src/main/res/xml/iridium_device_filter.xml` | USB device filter. Iridium's product IDs are a hardware fact, so the file keeps its Iridium name. |
| `linux/99-satlink.rules` | udev rules. The `/dev/iridium%n` symlink names the hardware and stays. |
| `scripts/build-android.sh` | Builds the Android archive `satlink.aar`. Details below the table. |
| `scripts/build-bindings.sh` | Builds the Apple binding framework `Satlink.xcframework`. Details below the table. |
| `scripts/mark-unusable-inits.sh` | Marks NSObject's `init` and `new` unavailable in the gobind classes that declare no argument-less `init`: those whose Go constructor takes arguments, for which gobind writes only an `init:` that takes them, and those that stand for a Go implementation of an interface (section 4, mobile, Zero values). It makes one awk pass per header, keeps the file's permissions, is idempotent, and fails, leaving the header unchanged, on a header of another shape. |

`AndroidUsbTransport.kt` holds the USB transport for Android. `open` claims the
interfaces, sets the line coding (`setLineCoding`, failures ignored) and pulses DTR
inside one `try`. Its `catch (e: Throwable)` releases every claimed interface, closes
the connection and rethrows, so the device never stays claimed. The docs of `open` and
`close` say that a DTR drop should end a data call (`AT&D2`) and, with the default
`+CVHU=2`, a voice call; this is not yet verified on hardware over USB. They also say
that initialising the handset, as `IridiumHandset` and `Mobile.establishLink` do, ends
a data call that is still up (online, or held in online-command mode, which `AT+CPAS`
reveals), or throws an exception whose `Mobile.asSatlinkError(e)?.kind` is
`Mobile.SatlinkErrorHangUpFailed`. The handset's `hangUp` ends a call that its firmware
cannot report.

`Example.kt` shows the intended use of the gomobile API. `LinkSettings()` is followed by
`allow(…)`. `fetchOverSatellite(context, cancellation: Cancellation = Cancellation())`
calls `Mobile.establishLink(transport, settings, null, cancellation)`. `post`, the link
of `postThenText`, calls `Mobile.establishLinkWithHandset(handset, settings, null, null)`.
Each wraps its link in `try { … } catch (e: Exception)`. `mayHaveLeftTheCallUp(e)`
(`when (Mobile.asSatlinkError(e)?.kind) { Mobile.SatlinkErrorHangUpFailed,
Mobile.SatlinkErrorInternal -> true; else -> false }`) decides whether a call may still
be up: after either kind, whether a call is up is unknown. The example then tries to end
the call. `post` does so with `handset.reinitialize()`; `fetchOverSatellite` does so with
`hangUpThrough(transport)`, a new `IridiumHandset` on the transport, still open, whose
`hangUp()` ends the call. That attempt runs inside `runCatching`, adds what it threw with
`addSuppressed`, and rethrows.

The example uses one connector for the whole app:
`private val tlsConnector: TLSConnector by lazy { TLSConnector(TLSSettings()) }`. Session
resumption costs about 1 KB of airtime instead of 3 to 5 KB. Every gomobile object except
a link is closed with `try { … } finally { x.close() }` rather than `.use {}`. Around each
link's work the example runs `try { … } finally { hangUp(link) }`, where
`private fun hangUp(link: SatelliteLink) { link.hangUp(120_000) }` ends the call even
after an exception, while the transport is still open. The 120 s allows for the 30 s close
grace and the hang-up itself; `hangUp` throws when the call may still be up. Links are not
closed after `hangUp`, because `SatelliteLink.Close` only requests the close that `HangUp`
has already requested. `readResponse` loops on `connection.receive(4096, 120_000)` until
`received.finished`, writing `received.data`, and checks
`Mobile.asSatlinkError(e)?.kind == Mobile.SatlinkErrorTLSTruncated` to recognise a
truncated TLS session. The file header warns that gomobile objects do not compare by
identity (section 3): compare them with `===`, and keep them in identity-based
collections, never in a `HashSet`, as map keys, or, without a wrapper, in a `StateFlow`.

`scripts/build-android.sh` runs gomobile for the Android targets. gomobile looks `gobind`
up on `PATH`, `go tool` puts no tool there, and `gomobile init` would fetch an unpinned
copy. The script therefore builds the `go.mod`-pinned `gobind` first and then runs
`PATH="$tools:$PATH" go tool gomobile bind -target=android/arm,android/arm64,android/amd64
-androidapi 21 -javapkg io.github.anhydrous99.satlink -o "$OUT/satlink.aar" ./mobile`.
The ABIs are armeabi-v7a, arm64-v8a and x86_64, not gomobile's default four. API level 21
is needed because gomobile's default, 16, is below what current NDKs ship. Prerequisites
are the Android SDK (`ANDROID_HOME`, for `android.jar`), the NDK (`ANDROID_NDK_HOME`) and a
JDK. `$OUT` is the first argument and defaults to `android/build/generated`. Desktop serial
is excluded by a build constraint.

`scripts/build-bindings.sh` builds the same pinned `gobind`, then runs
`PATH="$tools:$PATH" go tool gomobile bind -target=macos -prefix Satlink
-o "$OUT/Satlink.xcframework" ./mobile`, with `$OUT` as the first argument (default
`build/bindings`). It then runs `scripts/mark-unusable-inits.sh` on every
`SatlinkMobile.objc.h` that `find -type f` finds under `$OUT/Satlink.xcframework`, and it
fails if none is found. The script needs a full Xcode: gomobile refuses the Command Line
Tools alone. gomobile has no Kotlin/JVM desktop target (section 3).

### satlink (module root)

| File | Role |
|---|---|
| `doc.go` | Package documentation. |
| `transport.go`, `transport_test.go` | API: `Transport`, `DTRController`. The `Transport` doc states what `Handset.Close` relies on: the `Close` of an `io.Closer` transport must be safe while `ReadTimeout` or `WriteAll` runs in another goroutine, and it must make them fail rather than act on what it closed. sim's `Handset` and serial's `Transport` keep this; mobile's adapter is not a closer. Internal: the handset's own reads, writes and DTR changes (`write`, `read`, `raiseDTR`, `dropDTR`, `dtrLine`, `noDTRLine`), which a data call uses without the lock. |
| `cancellation.go`, `cancellation_test.go` | API: `Cancellation` (`NewCancellation`, `Cancel`, `Cancelled`), held by `DataCallSettings.Cancellation` (section 2, Cancellation). The test checks that copies of `DataCallSettings` share one request and that another call's settings are not cancelled. The zero value is ready to use, and a nil `Cancellation` is never cancelled. |
| `error.go`, `error_test.go` | API: `Error` (with `CallMayStillBeUp`) and `ErrorKind` with its sentinels, among them `ErrCallMayStillBeUp` (which also matches the flag) and `ErrCancelled`. Internal: `noCarrierError` and `isNoCarrier`. The tests cover the labels as a table, the texts, `errors.Is` for the kinds (for `ErrCallMayStillBeUp` with the flag set and not set, and the kind with and without the flag), matching through `fmt.Errorf("…: %w")`, and `Unwrap`. |
| `at.go`, `at_test.go` | API: `FinalResult`, `FinalResultKind`, `SplitCSV`. Internal: final-result classification, `responsePrefix`, URC detection, the line buffer (text through `internal/utf8lossy`; ASCII-only case folding through `internal/asciicase` for the echo, `responsePrefix` and `mayGoOnline`), the numeric-field parser, and `debugList` (section 4). |
| `deadline.go`, `deadline_test.go` | Internal: `deadlineAfter`, `timeToWait`, `longestRead` (section 4). |
| `handset.go` | API: `Handset` and its records, with the unexported `hangUpTimeout`. |
| `busy.go`, `busy_test.go` | Internal: the lock and the claim (`exchange`, `perform`, `lock`, `claim`, `release`; section 4, Handset internals). The test for a method that waited for the lock while a dial claimed the handset checks that it fails with `ErrBusy` and writes nothing. It holds `h.mu`, waits until `runtime.Stack` shows the test goroutine parked in `(*Handset).lock`, then claims and unlocks the handset, which is the state a connected dial leaves behind. |
| `exchange.go`, `exchange_test.go` | Internal: command execution, `commandWithTimeout` and the cancellable wait (`request.cancellation`, `commandRun.sentAt`, `cancellableWait`, `completion.endsWith`). The handset's reads and writes are in `transport.go`. The test for text entry that times out checks that the entry is left with ESC; it runs through `exec` with a short timeout, since `SendSMS` always waits two minutes. |
| `synchronize.go` | Internal: `Initialize`, through `synchronizeEndingACallLeftUp`, `endCallHeldInCommandMode` and `configure`. These handle AT synchronisation, resynchronising and draining. |
| `voicecall.go` | Internal: voice calls (`dial`, `answer` with `abortAnswer` and `endAnsweredDataCall`, and `hangUp`). |
| `callstate.go` | Internal: which call the handset is in, through `isInDataCall` (`+CPAS`) and `isInVoiceCall` (`+CLCC`). |
| `sms.go` | Internal: SMS, including the +CMGL and +CMGR parsing. |
| `handset_test.go` | Internal tests: the records' names, `mayGoOnline`, `validateNumber` and the quoting of a number it refuses, the +CREG codes, and the cap on the hang-up waits, which is the command timeout but at most ten seconds (`TestAHangUpWaitsForTheCommandTimeoutButAtMostTenSeconds`). |
| `datacall.go`, `datacall_test.go` | API: `DefaultEscapeGuard`, `MaxEscapeGuard`, `BearerService`, `DataCallSettings`, `DataCall`, and `Handset.DialData`, `AnswerData` and `EndDataCall`. Internal: setting a call up (`setUpCall`, `prepareAndStart` with `preparationFailed`), `dialData`, `answerData` with `hangUpVoiceCall`, `callRequest`, `isUnfinished`, `abortAttempt`, `abortCallAttempt` and `endAbandonedCall`. A failed call has no type of its own: it is an `*Error` whose `CallMayStillBeUp` flag says whether the call may still be up (section 2, Errors). The tests cover the escape guard, including refusal of a guard longer than any handset's. `debugDuration`'s cases include 0 (`0ns`) and 1.5 µs. |
| `hangup.go` | Internal: the escape, `ATH`, the DTR fallback, the DTR pulse that a transport's panic sets off (`pulseDTR`, `pulseDTRUnless`; section 4, Hanging up), `awaitOwnResult` and `discardUntil`. |
| `device.go`, `device_test.go` | API: `IridiumUSBVendorID`, `Product`, `KnownProducts`, `ModelForProductID`. The test checks that `KnownProducts` hands out a copy. |
| `support_integration_test.go` | The scripted transport used by the external tests. It implements `Transport` and `DTRController`, records every write with its time (`writtenAt(data) (time.Time, bool)`: when `data` was first written in one piece), records each DTR change with its time (`dtrChange{at, raised}`, returned by `dtrChangesSoFar`), and tracks the steps not yet played. All of this is guarded by a mutex, because `Handset.Close` runs while a read is in progress. Its replies come at once unless a test makes them late. `answersLate(write, delay)` delays the reply to the first unplayed step that writes `write`. `unsolicitedAfter(delay, data)` sends data unprompted, and `unsolicited(data)` is `unsolicitedAfter(0, data)`. No reply arrives before one sent ahead of it. `ReadTimeout` decides at its start whether the next reply arrives within its timeout. If it does, `ReadTimeout` sleeps until the reply arrives and delivers it; if not, it waits out the timeout and returns 0. A slow machine can therefore make a reply arrive sooner than the handset expects, never later. With no reply on its way, it naps `min(timeout, 1 ms)`, which spares the CPU in long waits such as the ten-second abort grace. A separate blocking transport that implements `io.Closer` serves the tests of `Close` and `SetTimeout` during a dial. |
| `handset_integration_test.go` | Tests of `Initialize` over the scripted transport. The `configuration()` steps end `Initialize` (`ATE0`, `ATV1`, `AT+CMEE=2`, `AT+CMGF=1`), and the `AT+CPAS` replies 000 and 004 report a ready or data-call status. The file covers: a call left up whose far end never pauses, which the escape ends (about 11 s, run in parallel); text entry left by ESC CR before the first AT; a line-only Unicode string that folds to the command not being taken as an echo (the echo is matched ASCII-only); a line left after an answer not being the next command's answer; late and partial answers that must not hide a call held in online-command mode (a late OK within the first drain's 300 ms of quiet, and a partial answer left before `Initialize`, since the drain clears the line buffer); earlier output being dropped; a late answer to each AT still synchronising; a zero timeout failing a command without waiting for its answer; firmware refusing the optional settings, which is tolerated, and refusing a required setting (ATE0 or ATV1 answered with ERROR), which fails `Initialize` with `ErrCommandFailed` for that command and sends nothing after it (section 4, Stale input). |
| `datacall_integration_test.go` | Tests of dialling, answering and hanging up over the scripted transport: escape timing and the settings' guard of silence (a guard counts from the last write), the handset's own answers that confirm an escape or a raw ATH, and the answer to a DTR drop, which must not pass for an ATH answer. It also covers late and partial answers that must not pass for the next command's answer or decide a dial; the DTR fallback, which `assertHungUpByDroppingDTR` checks (DTR stays down at least 150 ms and comes up again); and `EndDataCall` failing with `CallMayStillBeUp` set when the handset answers nothing (`TestEndDataCallThatFailsSaysTheCallMayStillBeUp`). The tests of a wait between two writes assert it with `assertWaitedBetween(t, transport, earlier, later, atLeast)`. |
| `cancellation_integration_test.go` | The cancellation tests. `cancelsOn` is a scripted transport that calls a cancel function the moment a command is written, so that a context can be used too. `readTimeouts` records the timeout of every read while a command waits for its answer; `TestADialLooksAtItsCancellationEveryFifthOfASecond` checks that no read of a cancellable dial waits longer than 200 ms. `TestACancelledAnswerIsAborted` checks that an `AnswerData` cancelled as ATA goes out either ends the attempt or ends a call that connects anyway, and returns `ErrCancelled` with `CallMayStillBeUp` false. |
| `busy_integration_test.go` | `ErrBusy` and the release of the handset on every path out of a call, including a transport that panics during `HangUp` or the dial. `TakeURCs` returns nothing while busy, and `SetTimeout` and `Close` are not blocked by a dial. Concurrent callers take turns (`TestConcurrentCallsTakeTurns`: eight goroutines, five `SignalQuality` and `Battery` calls each, over a transport that answers one byte per read, under `-race`). A panic out of a dial or a hang-up pulses DTR before it goes on; the test checks DTR is down for at least 150 ms, then up, and that the panic value reaches the caller. The pulse happens under the lock, before the handset is released, and each site has its own test, over a `panicsOnWrite` whose `DropDTR` has another goroutine send `Command("AT")`. That command must fail with `ErrBusy` and write nothing while DTR is down. A pulse that panics too leaves the first panic to go on, and so does a transport without DTR. |
| `portability_test.go` | For the whole module, every package and its tests type-check for linux/386 and linux/arm, where `int` has 32 bits, and for freebsd/amd64, openbsd/amd64, windows/amd64 and android/arm64. Those platforms have files of their own (terminals, interruptions, close-on-exec) that the host never compiles. The check runs `go vet ./...` with `GOOS`, `GOARCH` and `CGO_ENABLED=0`, and is skipped under `-short`. |

### ppp

| File | Role |
|---|---|
| `doc.go`, `example_test.go` | Package documentation. The example is `ExampleEndpoint`. Its output is a deterministic property, one flag-delimited LCP frame, and not the bytes, which carry a random LCP magic number. |
| `config.go`, `config_test.go` | API: `DefaultMRU`, `MinMRU`, `Timers`, `Keepalive`, `Credentials`, `AuthProtocol`, `Authentication`, `ServerConfig`, `Role`, `Config`, `ConfigError`. The redaction test is a table (section 4, Redaction). `%+v` names the fields of `Credentials`, `ServerConfig` and `Config` (`TestPlusVNamesTheFieldsOfWhatHasFieldsToName`). |
| `endpoint.go`, `endpoint_test.go` | API: `Phase`, `IPv4Settings`, `Event`, `DownReason`, `SendError`, `Stats`, `Endpoint` (`endpoint.go`). Events compare with `Event.Equal`. |
| `keepalive.go`, `keepalive_test.go` | Internal: the keepalive prober and the endpoint's LCP echo handling (probes, echo replies, and loopback detection from returned probes). The tests cover probes, echo replies and their MRU, loopback from our own probes after the third, probes without a magic number once ours is refused, a busy link not being probed, no probe while LCP renegotiates, traffic reaching a dead link starting no probes, and abandoned links: each sends one Terminate-Request as it goes (a table of the ways a link is abandoned) and stays dead until opened again. |
| `deadline.go`, `deadline_test.go` | Internal: the timer arithmetic `isDue`, `earliest`, and the saturating `scaled`. |
| `endpoint_api_test.go`, `endpoint_lifecycle_test.go`, `endpoint_framing_test.go`, `endpoint_rejects_test.go`, `endpoint_lcp_test.go`, `endpoint_ipcp_test.go`, `endpoint_auth_test.go` | Endpoint tests, split by concept. `endpoint_api_test.go`: names and texts, comparisons, copies, the IPv4 settings, datagrams, output, statistics, and the 64-datagram queue. `endpoint_lifecycle_test.go`: `Open` in every phase, `Close`, reopening, every way a link goes down for good and the contract a dead link keeps, `NextTimeout` with IPCP's timer and an owner that follows it, and a close during LCP negotiation that a crossing Nak does not undo. `endpoint_framing_test.go`: the MRU limiting each end, with a 1500-byte receive floor; each end escaping what the other asked for; header compression; framing as LCP agreed; and LCP always framed conservatively. `endpoint_rejects_test.go`: discarded packets, which draw no answer and pass nothing to the IP layer; Protocol-Rejects sent and received; and IPv4 arriving while IPCP negotiates, which is discarded even once the link opens (RFC 1661 §3.6). `endpoint_lcp_test.go`: the LCP codes on the wire against RFC 1661's numbering; answers to unusual and hostile requests (a hostile request at MRU 65535; a request with an option to reject draws only the Reject; a Nak and a Reject keep the request's options in order, including every instance of a rejected option); loops; a stray Terminate-Ack; whichever end restarts first, including crossing restarts and a retransmission arriving first; and the fresh identifiers the endpoint numbers its Echo-Requests, Code-Rejects and Protocol-Rejects with. `endpoint_ipcp_test.go`: IPCP at the endpoint and its Nak tolerance: 20 Naks are accepted, and the 21st ends the link. `endpoint_auth_test.go`: the authentication phase at the endpoint: an LCP restart ends it; a Protocol-Reject of PAP or CHAP ends the link; a packet of the other authentication protocol is discarded unseen by the running exchange; an authenticated link runs no timer once its exchange has decided; and a lost CHAP challenge is sent again after one Restart. |
| `support_test.go` | Every helper that more than one test file uses (section 4, Internal design). |
| `fsm.go`, `fsm_test.go` | Internal: the option-negotiation automaton shared by the PPP protocols (RFC 1661). `fsm_test.go` also pins RFC 1661's rules, including the differences of section 3. `TestAnAnswerToOurRequestRestartsTheRetryCount` is a table over Configure-Ack, -Nak and -Reject: an answer that comes with one retry left is followed by Max-Configure requests before the timeout. The Reject case uses `yieldingNegotiator`, a `simpleNegotiator` that accepts every Reject. `TestAnAnswerToAnAcknowledgedRequestIsIgnored` checks that an Ack, Nak or Reject carrying the acknowledged request's identifier changes nothing and sends nothing, in Ack-Received and in Opened. That guard is what `requestAgain` relies on. `TestAnUnknownCodeDrawsOnlyACodeReject` checks that an RFC 2153 Vendor-Specific packet, whose magic number begins with the code of a Terminate-Request, draws a Code-Reject on the automaton's own protocol. `TestACloseIsNotUndoneByAnAnswerCrossingIt`, `TestARepeatedOptionDrawsOneSuggestion`, and `TestATerminateAckAfterOurRequestWasAcknowledgedStartsOver` cover the restart timeout finding Request-Sent, or Ack-Sent, with helpers `afterAStrayTerminateAck` and `assertAFreshRequestAtTheRestart`. `TestIgnoresDuplicateAndMismatchedAcks` also sends an Ack of the request's length with another value. |
| `lcp.go`, `lcp_test.go` | Internal: the Link Control Protocol. |
| `ipcp.go`, `ipcp_test.go` | Internal: the IP Control Protocol. |
| `auth.go`, `auth_test.go` | Internal: PAP and CHAP. The peer's messages are read through `internal/utf8lossy`. The PAP tables include fields that their length octets overrun (a Nak's message, a request's password); these give an empty message and a refusal. `auth_test.go` includes `TestACHAPAuthenticatorSendsMaxConfigureChallenges` and `TestAnAuthenticatorGivesUpWhenThePeerNeverAuthenticates`, which check exactly Restart times Max-Configure after the start, for a PAP authenticator waiting for the peer's request and for a CHAP one. |
| `hdlc.go`, `hdlc_test.go` | API: `DefaultACCM`. Internal: `frameEncoding`, `frameDecoder` and `frame`. The table `TestDropsControlCharactersThePeerShouldHaveEscaped` checks that control characters the peer should have escaped are dropped. Its second case puts XON inside an escape sequence: a control octet that the receiving ACCM flags is removed before the Control Escape applies (RFC 1662 §4.2). |
| `packet.go`, `packet_test.go` | Internal: PPP packet parsing and encoding. |
| `fcs.go`, `fcs_test.go` | Internal: the frame check sequence. |
| `random.go`, `random_test.go` | Internal: random values, from `crypto/rand`. |

The fuzz targets are `FuzzParsePacket` and `FuzzParseOptionList` (`packet_test.go`),
`FuzzFrameDecoder` and `FuzzFrameRoundTrip` (`hdlc_test.go`), and
`FuzzEndpointHandleInput` (`endpoint_test.go`).

`FuzzEndpointHandleInput` fuzzes these inputs: whether the server demands CHAP-MD5 or PAP
(an endpoint hands a PAP or CHAP packet only to an exchange of that protocol, so over one
link the other protocol's packets meet only the discard); the steps before the attack; an
index into `hostileProtocols`, the protocols that `TestSurvivesArbitraryInputInEveryPhase`
sends (LCP, PAP, CHAP, IPCP, IPv4 and an unknown one); and the packet's information. The
target frames the packet, so every mutation passes the FCS and reaches the protocols. Raw
line bytes are `FuzzFrameDecoder`'s input. Its PAP seeds run over a PAP link: a Nak at step
1, where the client's exchange waits for the verdict, and a request at step 2, where the
server's exchange takes it.

The target takes no second `[]byte` for line noise. Go's fuzzer makes two `[]byte`
arguments share one backing array within a chain of mutations, so the noise would
overwrite the packet. `go test` runs the seed corpora. To run one target, use
`go test -run='^$' -fuzz='^FuzzFrameDecoder$' -fuzztime=30s ./ppp`.

### satnet

| File | Role |
|---|---|
| `doc.go`, `example_test.go` | Package documentation. The example is an `Example` without an output comment, so it is compiled but not run. |
| `config.go`, `config_test.go` | API: `IridiumInternetNumber`, `IridiumInternetBearer`, `Credentials`, `Config` with `Validate` (value receivers, so that `satnet.NewConfig(policy).Validate()` works), `NewConfig`, `Limits`, `Tuning`, and their defaults. Tests: the PPP client's configuration, including the receive ACCM spelled out bit by bit (`TestPPPAsksTheGatewayToEscapeCRAndTheFlowControlCharacters`) and an MRU equal to the MTU, which is 576 bytes by default (`TestPPPTakesPacketsOfUpToTheMTU`). |
| `error.go`, `error_test.go` | API: `Error` (with `CallMayStillBeUp`) and `ErrorKind`. A link failure has no type of its own: its `CallMayStillBeUp` flag is `Error.CallMayStillBeUp` (section 2). The tests cover the texts, the standard-library equivalents of section 2, and `satlink.ErrCallMayStillBeUp` matched by the flag and through a wrapped `*satlink.Error`. |
| `status.go`, `status_test.go` | API: `LinkState`, `CloseReason`, `Usage`, `Status`, and the aliases `DownReason` and `IPv4Settings`. |
| `policy.go`, `policy_test.go` | API: `Policy`, `AllowRule`, `PortRange`, `ParsePortRange`, `ParseRuleError`. |
| `link.go`, `dial.go`, `dial_test.go` | API: `Link`, `Establish`, `Watcher` (`link.go`). Internal: the insides of `Resolve`, `Dial`, `DialUDP` and `DialContext` (`dial.go`). The tests check that `DialContext` returns the context's error only once its deadline has passed. A timeout or an ended context ends a dial's attempts at its first address (`endsAttempts`). Nothing new (a lookup, a TCP connection, a UDP socket) starts while the link drains. |
| `tcp.go`, `connection.go`, `tcp_test.go` | API: `TCPConn`, with `Abort`, `WriteSome` (which queues what fits without waiting for room for the rest) and `Write` (which waits for room for all of its data). Internal, in `connection.go`: what `TCPConn` and `UDPConn` share. These are the socket handle, the ends, the closed flag under the engine lock, the atomic deadlines, and the close that the runtime cleanups share. The tests run over an engine without a driver. `WaitAcknowledged` fails for data that never left (cut off by a new address, dropped by an abort or the hang-up, as it starts, once it is over, or when the driver abandons the link) and returns nil for data acknowledged earlier. A `CloseWrite` stops a waiting write, but the data already queued is still sent, then the FIN. A wait for room ends once the connection no longer sends (after an abort, the peer's reset, or a new address). The "TCP flush" timeout is tested. `Close` sends what was written, then the FIN (`TestCloseSendsWhatWasWrittenThenTheFIN`). An aborted connection keeps its socket, and so its handle, until `Close`. Calls after its reset has left fail as documented rather than panic, and `Close` then releases the socket (`TestAnAbortedConnectionKeepsItsSocketUntilClosed`), including when `Close` comes before the reset has left (`TestAnAbortedConnectionClosedBeforeItsResetLeftLetsItsSocketGoOnce`). |
| `udp.go`, `udp_test.go` | API: `UDPConn`. Internal tests: only the peer's datagrams are read; a write that waits for room and times out reports "UDP send"; a write is refused while the link drains; a closed socket goes once its queue is sent; a datagram counts as undelivered while it is queued in the socket and while it waits in the device for the line (`TestADatagramWaitingForTheLineIsUndelivered`); a datagram of `MaxDatagramLen` bytes leaves as one packet of the MTU (`TestTheLargestDatagramFillsOnePacket`); and `LocalAddr` is the address the datagrams leave from (section 4, Stack). |
| `tls.go`, `tlskeys.go`, `tls_test.go`, `tlskeys_test.go` | API: `TLSOptions`, `TLSConnector`, `Link.DialTLS` (`tls.go`). Internal: the pool of public roots (`rootPoolOf`) and the session cache (`caseBlindSessionCache`), both in `tls.go`. `tlskeys.go` checks the keys a server's chain may hold: `verifyChainKeys`, which is the connector's `VerifyPeerCertificate`, accepts a chain if one verified chain holds no refused key. It checks no keys when verification is skipped. `holdsNoRefusedKey`, `isRefusedKey` and `errRefusedKey` support it (section 3). `tls_test.go` covers the server-name rule, the public roots (which hold exactly the certificates of Mozilla's program; `CertPool.Equal` compares certificates, not their constraints), and a root's distrust date (`TestAPublicRootAnchorsNoChainIssuedAfterItsDistrustDate`). It also checks that a certificate for another name is refused with or without a pin, that TLS 1.2 is offered with the suites of `internal/ringprovider` only, and that a server offering only CBC suites is refused. `tlskeys_test.go` checks, over the fake transport, which key types and sizes a chain may hold, including the refused RSA sizes. It checks that a server sending the certificate crypto/tls asked for is trusted, and that such a certificate must be pinned too (`TestAServerSendingTheCertificateCryptoTLSAskedForIsTrusted`, `TestTheCertificateCryptoTLSAskedForMustBePinnedToo`). It also checks that one verified chain without a refused key is enough, and that a configuration which skips verification checks no key. |
| `tlsstream.go`, `tlsrecords.go`, `tlsstream_test.go`, `faketransport_test.go` | API: `TLSConn`, with `Abort` and `WriteSome` (`tlsstream.go`). Internal: the `tlsTransport` interface and the record buffer (`tlsrecords.go`; section 4, TLS connection). `tlsstream_test.go` checks that a failed session still says why once the connection has ended: a forged record, then the server's FIN. The other tests run over an in-memory fake of `tlsTransport` whose server end is a `net.Conn` for `tls.Server`. `faketransport_test.go` holds the fake. Its `Abort` makes `WaitAcknowledged` fail with `ErrConnectionReset` when it dropped bytes the server had yet to read, as a `TCPConn` does. It also checks `checkSendable`, `announce`, and the waits' `changed` function, and refuses sends after its `CloseWrite` or reset (section 4, TLS tests). The fixture (`newTLSFixture`, or `newTLSFixtureWithKeys` for keys of any `crypto.Signer`: `generateKey`, `generateECDSAKey`, `generateRSAKey`, `generateEd25519Key`, `generateMLDSAKey`) keeps the pin of its server's key (`publicKeySHA256`, the SHA-256 of a key's DER SubjectPublicKeyInfo) and its CA (`tlsFixture.ca`), and makes further server certificates (`serverCertificate`). `servePeerOver` serves a peer over a given fake connection, `servePeerStoppingAfterItsFlight` stops a TLS 1.3 server after it has sent its flight, and `secureFakeFor` secures a connection for a given server name. |
| `tlspinning.go` | Internal: certificate pinning. `tlsstream_test.go` checks that a pin of a CA the server sends but does not own is refused, and that a pinned key does not stand in for the chain. |

RFC 8879 certificate compression is not implemented (section 3).

| File | Role |
|---|---|
| `engine.go`, `engine_test.go` | Internal: the engine, with `isNegotiating`. A link counts as negotiating only while its PPP has not come up. The tests cover `waitUntil`, which checks the closed flag first, then the attempt, and honours a deadline moved while it waits. They check prompt wake-ups and changes that close the channel the waiters hold. `beginClosing` still records why the link closes when aborting its connections panics, and `abandon` still reports the link closed, with the reason and with the call perhaps up, in that case (`TestAnAbandonedLinkWhoseConnectionsCannotBeAbortedStillReportsItselfClosed`). They also check that app traffic restarts the idle clock at all six sites, and that an abandoned link reports itself closed with the call perhaps up. |
| `driver.go`, `driver_test.go`, `drivercall_test.go` | Internal. `driver_test.go` checks panic messages, including for a value that cannot describe itself; a datagram PPP cannot send; a datagram too large for the peer, which is dropped with a warning; data handed to PPP only when the paced line can take it; the "IPv4 up" log line; an address the stack cannot use, which fails IPCP; a link that comes up, which stops its wait for PPP; the limits and the order of checking them, with the call time limit starting the hang-up in time to end the call; the drain (`isDrained`: everything delivered and the line free), with a requested close draining for at most `CloseGrace`, and for none while PPP negotiates; a cancellation that stops only a link still coming up; and hang-up errors that carry the handset's failure. `drivercall_test.go` runs the driver over a real data call to the simulator, behind `faultyLine`. It checks that a lost LCP Configure-Request is sent again, that the keepalive notices a silent gateway, that a line refusing the first frame and a later one is handled, that PPP output after NO CARRIER is dropped without failing the link, and that the hang-up's info and warning lines are logged (`TestAHangUpThatCannotBeConfirmedIsLoggedAsAWarning` waits for the link to close for its transport before calling `Close`, so that it does not depend on PPP's pacing backlog). |
| `stack.go`, `stackconnections.go`, `lookup.go`, `stack_test.go`, `stackconnections_test.go`, `lookup_test.go` | Internal, split by concept (section 4, Stack). `stack.go` holds the interface, polling, UDP sockets, ports, `localEndpoint`, and `configure`, which keeps the DNS servers and notices an address change. `stackconnections.go` holds the TCP connections: `outbox`, `sendTCP`, `tcpMaySend`, `tcpCanSend`, `tcpUnacknowledged`, `shutdownTCP`, `resetTCP`, `dropOutbox`, `closeTCP`, `abortTCP`, `feedConnections`, `cutOffUndelivered`, `recordCutOff`, `sendBacklogs`, `passOnAcknowledgements` and `removeTCP`. `lookup.go` holds name lookups: `dnsPort`, `lookupID`, `nameLookup`, `startLookup`, `lookupResult`, `abandonLookup`, `dnsAnswer`, `parseDNSAnswer`, `dnsServerTimeout` and `longestLookup`. The stack's own tests check that the stack sends no ICMP errors, and that the app's own UDP datagram to a DNS server waits for the line, since `poll` hands the device the app's UDP ports (`TestTheAppsOwnDatagramToADNSServerWaitsForTheLine`). The lookup tests cover a resolved lookup, a lookup that needs a server and a name, a spent lookup, and an abandoned lookup, which is cancelled and sends no more queries. They also cover an answer about another name, an answer that spells the name in another case, and which datagrams count as an answer about the name. A table damages what only each checksum can show (a bit of the IPv4 identification, a letter of the question's name), and refuses every error rcode (FORMERR, SERVFAIL, NOTIMP, REFUSED) and any other question class. The connection tests cover the send buffer bounding the outbox and the socket together (`TestTheSendBufferBoundsTheOutboxAndTheSocketTogether`), a finished connection taking nothing, data cut off by an address change (and forgotten with its socket), data the stack takes just before a reset staying cut off, data the stack holds when the address changes, the same address cutting nothing off, a connection that is let go sending no keepalives, `abortAll` and `resetTCP` dropping what the app wrote and recording it as cut off, and only the connections whose outbox held data being cut off (`TestAResetOrTheHangUpCutsOffTheConnectionsWhoseOutboxItEmptied`). Data the stack holds on a connection that the peer reset no longer counts as undelivered, so a requested close does not wait for it (`TestDataOnAConnectionThePeerResetIsNoLongerUndelivered`). A small write leaves while the one before is unacknowledged, since Nagle is off (`TestASmallWriteDoesNotWaitForTheLastOnesAcknowledgement`). A bare acknowledgement waits the 100 ms ACK delay for a reply to ride on (`TestABareAcknowledgementWaitsAWhileForAReplyToRideOn`). `stackconnections_test.go` has a `sentBeforeTheFIN` helper that several tests share. |
| `device.go`, `device_test.go` | Internal: `ipstack.Device`. Urgent datagrams go at once, the rest go per flow in turns, stale retransmissions are dropped, and waiting segments are brought up to date (section 4). Segments are built with `testSegment`, over `wire.TCPRepr` and `wire.IPv4Repr`; the bare datagram to port 53 is built with the packet's setters. The tests also check that a retransmission carrying the FIN still goes, and that a copy without the FIN does not replace the segment carrying it. Datagrams without a flow are dropped. A TCP datagram without a segment waits. Datagrams that are not segments never replace each other, and acknowledgements of a connection not seen opening change nothing. What is news for the peer is tested with a table. Received datagrams come in order, and clearing forgets the connections too. |
| `pacing.go`, `pacing_test.go` | Internal: the pacing of the line. |
| `names.go`, `names_test.go` | Internal: the name cache. |
| `log.go`, `log_test.go` | Internal: the logging helper (section 2, Logging). |
| `link_integration_test.go` | Link tests over the simulator and a faulty transport (`simtest.FaultyTransport`, `simtest.NewHandset` with `simtest.SimulatorConfig()`, and `simtest.CallEscapeGuard`). The cancellation tests set the link up with `establishInBackground` and `awaitSetup`. Coverage includes: lookups with no server answering time out; a context cancels the setup through `context.AfterFunc`; a bare address is not dialled once the link is closed; an invalid name is never looked up; a refused connection fails with its address; when every address refuses, the error reports the last one tried; a dial goes on to the next address; the link keeps the hosts it was given; the close grace bounds a requested close; the call time limit ends the call; data that holds the handset's NO CARRIER does not end the link (the CR escape keeps the handset's result codes out of frames); an answer is reused rather than asked for again; the usage is current while the link is up; data that the hang-up dropped is not reported delivered (`unpluggingLine`, a line that fails the next PPP frame after running the write it was given); the duration of a call counts its hang-up (`Usage.ConnectedFor`); closing delivers the datagrams already written (`TestClosingDeliversTheDatagramsAlreadyWritten`); and a refused destination sends nothing on the line (`TestARefusedDestinationGetsNothingOnTheLine`). `TestAPanicWhileHangingUpStillReportsTheLinkClosed` also checks that the core's DTR pulse ended the call (section 3). |
| `tls_integration_test.go` | TLS tests over the link. `tlsLinkOver(t, profile, linkRate)` builds the link; it also allows `echo.test:7` for a probe connection. Helpers: `lineOf`, `readExactlyBy`, and `readInBackground`. Coverage includes a close_notify that waits longer than crypto/tls's five seconds (section 4, TLS tests); a TLS write that waits for room goes out whole before another; an abort wakes a TLS write or a close-write waiting for room; and a write refused while the link drains leaves delivery confirmed. The last one uses a 16 KiB upload at 3000 B/s, which keeps the link draining through a 3 s close grace. The TLS write is refused with nothing taken, and `WaitAcknowledged` stays nil. |
| `conn_integration_test.go` | `net.Conn` behaviour: a deadline moved while a call waits applies to it; `Close` wakes blocked calls and later calls return `net.ErrClosed`; `Abort` wakes a waiting `Read`; a `Write` after `CloseWrite` is refused; an empty `Read` or `Write` returns at once. `Write` counts what it queued when it times out, and a write that waits for room goes out whole before another. `WriteSome` queues what fits without waiting for the rest, times out only when nothing fits, and handles an empty write. Also: `DialContext` with cancellation; the timeout of a lookup that nobody answered; a `net/http` GET of `http://web.test/`; runtime cleanups; and a collected Link (section 4). |
| `support_integration_test.go` | Helpers that the four external test files share (link, TLS, connection and panic tests). They include `billingGuard`, the 500 ms escape guard of the billing tests, and `expectTheHangUpBilled`. |
| `connection_test.go` | Internal: the cleanups of a collected `TCPConn`, which closes gracefully (what was written, then the FIN), and of a collected `UDPConn`. |
| `panic_integration_test.go` | External tests of panics. A panic carrying a nil error pointer, on a read or on the escape, over `brokenPanicTransport`, whose DTR line reaches nothing; the call then stays up after `Close` until the app's `EndDataCall` ends it through the released handset (`TestAPanicThatCannotDescribeItselfWhileHangingUpSparesTheProcess`). A log handler that panics as the hang-up aborts the connections: the link is reported closed only once the call has been hung up and the handset released (`TestAPanicWhileTheHangUpAbortsTheConnectionsStillHangsUp`, and `TestTheFirstOfTwoPanicsIsWhyTheLinkClosed`). A handler that stays broken panics every time the connections are aborted, both at the hang-up and when the driver abandons the link (`TestAPanicEveryTimeTheConnectionsAreAbortedStillClosesTheLink`; this pins `driver.abandon`'s own `recover`). A hang-up that panics on the escape after an earlier panic is ended by the core's DTR pulse (`TestAHangUpThatPanicsAfterAnEarlierPanicSparesTheProcess`; `endCallLeftUp`'s own `recover`). A hang-up run after a panic is counted in `Usage.ConnectedFor` (`TestTheCallsDurationCountsTheHangUpThatFollowsAPanic`). |
| `export_test.go` | Test-only exports for the external tests. `AwaitWaitingCall(t, link, budget)` and `(*shared).awaitWaitingCall` read the engine's count of waiting calls, so that `conn_integration_test.go` and `engine_test.go` act only once a call is waiting (section 2, Tests). `PendingLookups(link)` and `HangUpTime(config)` are also here. |

### sim

| File | Role |
|---|---|
| `doc.go`, `example_test.go` | Package documentation. The `Example` has an output comment and runs. The `ppp` package's `ConfigError` is re-exported as the alias `sim.ConfigError` (`config.go`). |
| `config.go`, `config_test.go` | API: `Config`, `LinkProfile`, `NetworkConfig` (with `DNSServers` and `SilentAddresses`), `HostAddress`, their defaults (built from named constants and `defaultDNSServers()`), the aliases `AuthProtocol`, `Credentials` and `Authentication`, and `ConfigError` (what `Control.RestartPPP` reports). `Config` has `String`, `GoString` and `Format`. `%#v` prints `GoString`, and `%+v` prints the fields with their names, as fmt prints a struct. Every other verb prints what `%v` prints. `String` alone would not be enough: `%s` would print the same, but fmt would print `%+v` without the names, quote the text for `%q`, hex-encode it for `%x` and `%X`, and print each field with any other verb, such as `%d` or `%t`, whether or not it fits the field (`%!t(int=4)` for `SignalBars`). Because these methods format `*Authentication` only for `%v`, `%+v` and `%#v`, the gateway's password stays redacted for every verb, as `ppp`'s `(*Authentication).Format` does too. The tests are a redaction table over the verbs `%v %+v %#v %s %q %x %X %d %t` for `Config`, `*Config`, and structs and slices holding them. They also check that `%s %q %x %X %d %t` print what `%v` prints. |
| `handset.go`, `handset_test.go` | API: `Handset`, `New`, `Control` (`DropCall`, `RestartPPP`, `ReceiveSMS`, `Stats`). The tests cover `Close`, I/O and DTR after `Close`, a `Control` that keeps working, and `New` keeping a copy of the configuration, `DNSServers` and `SilentAddresses` included. |
| `stats.go` | API: `Stats`, `SentSMS`. |
| `secureecho.go`, `secureecho_test.go` | API: `SecureTestCAPEM`, `SecureTestKeySHA256`. Internal: the TLS echo session over `internal/memconn`. Its tests run with `-race` (section 4). |
| `testdata/` | Fixtures: test certificates and keys, and a README, which is adapted for satlink. |
| `calendar.go`, `calendar_test.go` | Internal: the simulated calendar. |
| `circuit.go` | Internal: the simulated circuit. |
| `command.go`, `command_test.go` | Internal: AT command handling. |
| `connectionrequest.go`, `connectionrequest_test.go` | Internal: connection requests. |
| `device.go` | Internal: `ipstack.Device`. |
| `dns.go`, `dns_test.go` | Internal: the simulated DNS server. Names fold ASCII letters only (`internal/asciicase`). The test `TestOnlyASCIILettersFoldInNames` checks that a query with the Kelvin sign U+212A does not find kelvin.test. |
| `escape.go`, `escape_test.go` | Internal: the escape sequence. |
| `gateway.go`, `gateway_test.go` | Internal: the gateway, which `startServer` runs. `gateway_test.go` runs a PPP client and a gateway that talk directly to each other (`pppPair`). It covers the internet's lifetime; the "IPv4 up" and "PPP down" log lines; a renegotiation that changes the MTU, which builds a new internet for it and closes the old one's TLS session; a restart of PPP, which builds a new internet for the new address; and a restart that cannot start, which changes nothing. It also checks the "dropped a %d-byte datagram" debug line for an answer the internet sends while the client starts PPP over. PPP is not open for IPv4 until the new negotiation ends, but the internet stays. |
| `http.go`, `http_test.go` | Internal: the simulated web server. |
| `internet.go`, `internet_test.go` | Internal: the simulated internet. The tests check that an address the stack cannot use is left out with a warning, and the debug lines of the UDP echo and the DNS server for an answer they cannot send (to a datagram from port 0, "unaddressable"). |
| `line.go`, `line_test.go` | Internal: the serial line. Text passes through `internal/utf8lossy`. |
| `link.go`, `link_test.go` | Internal: the delay line. |
| `modem.go`, `modem_test.go` | Internal: the modem. `modem_test.go` also covers rules that the other tests leave out. An SMS announcement waits until the app has read the earlier output (`TestAnSMSAnnouncementWaitsUntilTheEarlierOutputIsRead`). Online-command mode holds the far end's data until ATO (`TestOnlineCommandModeHoldsTheFarEndsDataUntilATO`: the PPP server's repeated Configure-Request, after `ppp.DefaultTimers().Restart`). ATZ restores the default bearer and `&D0` as well as echo (`TestATZRestoresTheFactorySettings`). A restart of PPP during call setup changes nothing (`TestARestartOfPPPDuringCallSetupChangesNothing`; a dialling call has no circuit), and the call then connects with its gateway assigning the configured client address. The guard of silence before `+++` counts from CONNECT and from ATO, so pluses sent sooner are data (`TestPlusesSoonerThanAGuardAfterCONNECTAreData`, `TestPlusesSoonerThanAGuardAfterATOAreData`). The modem moves the simulation to the time of what the app writes before taking it, so an escape whose closing guard has passed ends online-data mode before the bytes that follow, even with no read in between (`TestAnATHWrittenOnceTheEscapesGuardHasPassedHangsUp`). Closing the port after AT&D2 ends a call that is being set up, with no CONNECT and no NO CARRIER, and `+CEER: Normal call clearing` (`TestClosingThePortAfterATAndD2EndsACallBeingSetUp`). The modem's log lines, with their levels, are pinned: a data call dialled, a voice call, an SMS, the app's hang-up, a network drop, a DTR drop, and a gateway that cannot start (warn). The helpers `dialDataCall` and `connectDataCall` dial the way these tests do. |
| `modemcommands.go` | Internal: the modem's AT commands. |
| `reply.go`, `reply_test.go` | Internal: replies. |
| `services.go` | Internal: the simulated network services. |
| `sms.go`, `sms_test.go` | Internal: SMS. |
| `time.go`, `time_test.go` | Internal: simulated time. |
| `log.go`, `log_test.go` | Internal: the logging helper, and the recorder the tests share. |
| `tlsclient_test.go`, `goroutines_test.go`, `random_test.go`, `datagram_test.go` | Test helpers of package `sim`. `TLSClient` is a crypto/tls client, pumped over an `internal/memconn.Conn`, with `ServerSaidGoodbye`. Its `awaitEnd`, `finalError` and `extractAll` are unexported, since only internal tests use them. So are `secureTestClientConfig(t, cache)`, the settings of a client that trusts only the test CA, and `newTLSClient(t, local, remote, config)`, which starts a client with the test's own settings. `secureecho_test.go` has `connectClient(t, config, clientConfig)` beside `connectSession`, `connectTLS12ClientOffering(t, suite)` (a client that speaks TLS 1.2 at most and offers one suite), and `sessionPair.handshake`, which returns the handshake's error; `completeHandshake` is built on it. `goroutines_test.go` provides `AwaitGoroutines`, which polls with `runtime.GC`. `datagram_test.go` provides `UDPDatagram`, a UDP datagram builder on `internal/ipstack/wire`'s Reprs, which the external tests use too. `random_test.go` has a xorshift64 generator for the internal tests. |
| `support_integration_test.go` | Helpers for the external tests: a PPP client over a `DataCall`, datagrams built with `sim.UDPDatagram`, and a client stack on `internal/ipstack` (`clientNetwork`). `connectNetworkThrough(t, call, control)` takes the data call, made with `dial` or with `dialWith(t, handset, settings)`, so that a test can dial with settings of its own. The TLS client is `sim.TLSClient`, over an `internal/memconn` connection that the client network pumps on every exchange (section 4). The simulator's tests keep their own set-up rather than using `internal/simtest`, which imports `sim`. |
| `commands_integration_test.go` | `an_sms_from_another_thread_reaches_a_waiting_app` runs in a `testing/synctest` bubble. It sends the message once `PollURCs` waits (section 2, Tests). |
| `datacall_integration_test.go` | Tests of data calls, including `a_call_escaped_from_but_never_hung_up_is_ended_by_the_next_session`, which builds the next session's handset over the earlier one's `Transport`. Also: the DNS servers IPCP offers (two, and none); a silent address, where the echo answers in order, so that the first answer shows the silent one was lost; and `RestartPPP` (a new address with the call kept, an address that is not IPv4, and no call). |
| `internet_integration_test.go` | Tests of the simulated internet. Goroutine counts are checked after `DropCall`, after PPP goes down, after `RestartPPP`, and after the client resets a TLS connection. A simulator that nobody holds, whose call is dialled without AT&D2, ends its TLS session (section 4, TLS session lifetime). A client that hangs up as soon as its records are on the connection still gets the rest of its echo, then close_notify (`TestAClientThatHangsUpStillGetsTheRestOfItsEchoBeforeTheGoodbye`). Data sent after the web server has closed is answered with a reset. |

### serial

| File | Role |
|---|---|
| `doc.go` | Package documentation. |
| `discover.go` | API: `Port`, `Discover`. |
| `transport.go` | API: `DefaultBaud`, `Transport`, `Open`. Internal: the select(2) limit (`selectSetSize` and `isBeyondSelect`). |
| `connect.go` | API: `ChoosePort`, `Connect`. `ChoosePortFrom` chooses among ports already discovered, which the CLI does with its own discovery. |
| `closeonexec_unix.go` | Internal: `openCloseOnExec` and `descriptorsOf`. Build constraint `linux \|\| darwin \|\| freebsd \|\| openbsd`, the Unix platforms the library supports. |
| `closeonexec_other.go` | Internal: `Open` on the other platforms, and a `descriptorsOf` that finds none. |
| `discover_test.go`, `transport_test.go`, `transport_unix_test.go`, `connect_test.go`, `closeonexec_test.go`, `pty_test.go`, `pty_darwin_test.go`, `pty_linux_test.go` | Internal tests. Every exported function reaches the operating system, so the tests go through unexported seams (section 4). `transport_unix_test.go` checks the select(2) limit over `libraryShapedPort`, a fake port shaped as the library's, which `transport_test.go` defines. `closeonexec_test.go` checks that a port of another shape gives no descriptors and does not panic. The pty tests run on macOS and Linux over the slave end of a pseudo-terminal. They check that the port's three descriptors are close-on-exec, and that a child process started while the port is open does not keep the line up once the port closes. An empty read of a port with input waiting reads nothing and leaves the input for the next read (`TestAnEmptyReadOfAPortWithInputWaitingReadsNothing`). A port beyond select(2)'s 1024 descriptors fails its reads with `ErrTransport` instead of panicking; this test is skipped when `RLIMIT_NOFILE` is too low. |

### cmd/satlink

The command tree and its support are split across these files.

| File | Role |
|---|---|
| `main.go`, `doc.go`, `run.go`, `root.go`, `environment.go` | `main.go` starts the program. `run.go` runs a command. `environment.go` holds `environmentOf`, which tests call, and `processEnvironment`, which reads the process's own environment. |
| `list.go`, `info.go`, `status.go`, `sms.go`, `call.go`, `at.go`, `monitor.go` | The commands. `call.go` holds `dial`, `answer` and `hangup`. |
| `handset.go` | The global options, and `withHandset`, which opens the handset and closes it on every path out, a panic included. Opening goes through the environment's discovery and `serial.ChoosePortFrom` when no port is given. It also writes the trailing "(event)" lines. |
| `simulate.go`, `output.go`, `numbers.go` | `simulate.go` is the `--simulate` option. `numbers.go` parses numbers with saturating conversions. |
| `brokenpipe_unix.go`, `brokenpipe_windows.go` | `ignoreBrokenPipes` calls `signal.Ignore(syscall.SIGPIPE)`, and `main` calls it first. The Windows version does nothing. |
| `processstreams.go`, `processstreams_unix.go`, `processstreams_windows.go` | `processStream`, `isBadDescriptor`, `withOSErrorText` and `osError` handle the process's standard streams (section 4, Structure for tests). A bad descriptor is EBADF on Unix and ERROR_INVALID_HANDLE on Windows. Other failures that carry an OS error number are reported with the OS text, for example "Broken pipe (os error 32)", through `internal/errnotext`. `main` also clears `cobra.MousetrapHelpText`. |
| `command.go`, `arguments.go`, `values.go`, `commandline.go` | The command line. `command.go` holds the command tree. A `command` keeps its cobra command in the field `runner` and has `name()`, `about()` and `argumentGroup()`, the arguments the command declares. `arguments.go` and `values.go` hold the arguments as declared and the values they hold. `commandline.go` reads the command line. |
| `help.go`, `usage.go`, `usageline.go`, `suggestions.go` | Help, usage errors, usage lines, and suggestions for mistyped names. |
| `palette.go`, `strip.go` | `palette.go` holds the styles and `showsStyles`, which chooses for each stream whether to show styles. `strip.go` holds `stripStyles`, which removes styles for a stream that shows none. Its rules are copied from the state table of anstyle-parse 1.0.0 (anstream 1.0.0's `StripBytes`). |
| `interrupt.go`, `interrupt_unix.go`, `interrupt_windows.go` | Interruptions. `interrupt.go` has `notifyInterruptions` for the process and `reactionTo`, which says what each interruption does. `interruptionHandler`, with `newInterruptionHandler`, `handle`, `requestStop` and `askedToStop`, carries out the reaction. `interruptions`, with `catch`, `react`, `stop` and `askedToStop`, relays each interruption to the run's handler. `interrupt_unix.go` handles SIGINT, SIGTERM and SIGHUP. `dieInterrupted` runs `signal.Reset(SIGINT)`, then `kill(getpid(), SIGINT)`, and exits with status 130 after `sigintDeliveryGrace` (1 s) if the signal did not end the process. `interrupt_windows.go` handles `os.Interrupt` and SIGTERM, and exits with status 130. |
| `terminal_ioctl.go`, `terminal_linux.go`, `terminal_bsd.go`, `terminal_windows.go` | `isTerminal`, in `terminal_ioctl.go`, for macOS, Linux, Android, FreeBSD and OpenBSD. `terminal_linux.go` uses `TCGETS`, and `terminal_bsd.go` uses `TIOCGETA`. `terminal_windows.go` uses `GetConsoleMode`. |
| `logger.go` | `stderrLog`, the standard-error handler, built on `internal/logtext`. It hands each formatted line to a backlog of `logBacklog` (1000) lines, which one goroutine writes. The goroutine starts with the first line and is ended by `close` at the end of `run`. Lines that do not fit are dropped and counted, and `droppedLinesFormat` is written before the next line. `flush` waits for the lines handed over before it, for at most `flushLimit` (1 s), checking every `flushCheck` (10 ms). `writerBehind` is the CLI's own standard error, whose writes wait for the log lines handed over before them (section 4). |
| `run.go` (finish) | When a run finishes, it flushes the log, writes "error: …", stops catching interruptions, and returns `exitInterrupted` for an interrupted run. `main` then ends the process by SIGINT. A panic that reaches `run` is reported by `panicked` with `reportPanic`, and ends the run with `exitPanicked` (101). |
| `net.go` | The net subcommands' options: `netArguments.linkConfig` and `netArguments.policy`. `netTarget` (with `rule`, `check` and `isReachable`), `ipv4Literal` and `needsACall`. The `netAction` interface (`target`, `literalAddress`, `execute`), where `execute` takes a `netLink`, the link as the net subcommands use it (`Resolve`, `Dial`, `DialTLS` and `RequestClose`; `*satnet.Link` is one). `status`, `resolve` and `newNetCommand`. |
| `netcall.go` | The call of the net subcommands: `establish`, which announces the dial, applies the setup's cancellation, and warns after a failed setup; then recovery, close, summary, result, and the interrupted outcome. |
| `netget.go` | `net get`. `fetch` connects over `dialer`, hands the connection to `exchange`, and closes it afterwards. `exchange` sends the GET and reads the whole response within one deadline, `transferTimeout` from its start, over `exchangeStream` (`io.ReadWriter` and `SetDeadline`). Also `connect`, `newHTTPSConnector` (the public web PKI, with no pin and no ALPN), `readResponse`, `endingOf`, `responseEnding` and `printHTTPResponse`. |
| `httpresponse.go` | `finalResponse`, `header`, and `announcedEnd`. Header field names compare as ASCII through `internal/asciicase`. |
| `httpurl.go` | URL parsing for `net get`. |
| `netrelay.go` | `net tcp`. `sharedConnection` closes the connection once both the relay and the sender of standard input are done with it. `outgoingStream` is the connection as the sender uses it. `afterRemoteClosed` takes the connection's `WaitAcknowledged` as a function, `waitAcknowledged`, the way `readResponse` takes `receive`. |
| `logger_test.go` | The logger tests: lines waiting for a blocked standard error do not hold up the logger; lines beyond the backlog are dropped and counted; `flush` gives up after a second; the CLI's own lines follow the log lines made before them (`TestTheCommandsOwnLinesFollowTheLogLinesMadeBeforeThem`, which pins `newCLI`'s wiring of `writerBehind` too); there is no wait behind a closed log; and a closed log neither waits nor panics. |
| `commands_test.go` | Each command, run in-process against `testdata/commands`: `monitor`, `-v`, the port, a command without `--port` that chooses among the discovered handsets, and a handset command whose output cannot be written. The last covers every command that prints (list, info, status, `sms send`, `sms list` and `read`, dial, at, and monitor) and the "(event)" line. |
| `status_test.go` | A status whose output fails asks the handset nothing more (`TestAStatusAsksTheHandsetNothingMoreOnceALineCannotBeWritten`). |
| `interface_test.go` | Help, usage errors and styles, against `testdata/help`, `testdata/usage` and `testdata/style`. Help is styled when standard output is a terminal, and errors are styled when standard error is one, whatever the other stream is. The style goldens, which are forced with CLICOLOR_FORCE, cannot show this, so `TestHelpAndErrorsAreStyledOnTheTerminalTheyGoTo` runs with TERM=xterm, over the in-process world's `stdoutIsTerminal` and `stderrIsTerminal`. Text that is not UTF-8 is refused or shown lossily, as the parser does. Control characters are taken out of errors as the parser's streams take them out, and they are quoted in the debug form. The outputs are written out byte for byte. |
| `strip_test.go` | `TestStrippingKeepsWhatAnstreamKept` runs anstream's own test cases, then sequences and control characters that a command line can bring into the styles, finished or not. `TestTheParsersRulesAreAnstyleParsesAsStrippingSeesThem` compares the rules with anstyle-parse 1.0.0 for every state and byte. The table that `anstyleParseRules` writes out is compared as stripping sees it: where a byte moves the parser (with Utf8 read as the ground state), and whether the byte was dropped, kept, or kept with the character it starts. `TestStrippingClapsStylesLeavesWhatTheZeroPaletteWrites` runs over every help, usage, environment and style golden command line. |
| `golden_test.go` | Golden files. `go test ./cmd/satlink -update` rewrites them with the commands' output. |
| `environment_test.go`, `inprocess_test.go` | `environment_test.go` checks that the process's arguments, streams and variables reach the run (`TestTheProcessesArgumentsStreamsAndVariablesReachTheRun`). `inprocess_test.go` is the in-process harness. |
| `run_test.go` | The last log lines before the end. `TestAPanicEndsTheRunWithStatus101OnceTheHandsetIsClosed` checks a panic while the handset is readied, in a handset command and while a data call is set up, each checked through the stack reported. `TestAPanicOutranksAnInterruption` checks the precedence of the two. |
| `processstreams_test.go`, `processstreams_windows_test.go` | The standard-stream tests: `processstreams_test.go` on Unix, and `processstreams_windows_test.go` on Windows, which the cross-vet checks type-check. |
| `interrupt_test.go`, `interrupt_unix_test.go` | `interrupt_test.go` covers the first interruption asking the run to stop before saying so (`TestTheFirstInterruptionAsksTheRunToStopBeforeSayingSo`). `interrupt_unix_test.go` relays a real SIGTERM through `notifyInterruptions`. |
| `terminal_test.go`, `terminal_pty_test.go`, `terminal_pty_darwin_test.go`, `terminal_pty_linux_test.go` | A pseudo-terminal is a terminal, and each standard stream is reported as a terminal only when it is one (`TestEachStandardStreamIsSaidToBeATerminalOnlyWhenItIsOne`). |
| `netinprocess_test.go` | In-process tests of the net commands, through `run`, with standard input as a reader. They cover: a request that the remote end answered and closed is no error; an upload that the remote end cut short is an error; an upload whose end the remote end reset is an error; a fetched page is hung up on before a slow reader takes it; standard input still open when the remote end closed is an error, but not at a terminal; a relay whose output cannot be written; an interrupted relay ends with 130; an interruption while the call is being placed; an interruption that hangs up while standard error is blocked (`TestAnInterruptionHangsUpWhileStandardErrorIsBlocked`); and a panic in the subcommand's work (`TestAPanicDuringTheCallHangsUpAndFailsTheCommand`). In the last, `net resolve` runs with a standard output whose writes panic. The panic is reported, then the call summary, then "error: internal error: the command panicked" with status 1. A handset that records its stats as the command closes it shows the call already hung up, not left to the DTR drop of closing the port. |
| `net_integration_test.go` | Tests that run the built binary, Unix only. `TestMain` builds `satlink` once with `go build`, into a directory it removes. The tests run it in `satlinkEnvironment`: the test process's environment, without `SATLINK_*` and without the variables that decide styles (`NO_COLOR`, `CLICOLOR_FORCE`, `CLICOLOR`, `TERM`, `CI`), so that the tests run alike on every machine. They skip where no `go` command is found. Coverage: an interrupted run hangs up and then ends by SIGINT (and by SIGTERM and SIGHUP); a second interruption right after the first still lets the hang-up finish; a double interruption while standard error is blocked still hangs up; an interruption during call setup keeps the call from connecting; a hang-up signal that the process was started to ignore stays ignored (and SIGINT in a background job, through an `sh` trap); a blocked standard error does not hold the link up (`TestAStandardOutputNobodyReadsFailsTheCommandOnceHungUp`, `TestAStandardErrorNobodyReadsStopsNeitherTheRelayNorTheHangUp`, `TestARunInterruptedWhileSIGINTIsIgnoredExitsWithItsStatus`); a second interruption a few seconds after the first quits at once (`TestASecondInterruptionSecondsAfterTheFirstQuitsAtOnce`); and the process's variables standing in for their options (`TestTheProcessesVariablesStandInForTheirOptions`). Slow pipes are FIFOs, opened apart with raw descriptors (`O_CLOEXEC`), and read and written without Go's poller, which on macOS misses a FIFO's end. |
| `netrelay_test.go`, `netcall_test.go` | `netrelay_test.go` checks that a sender resets the stream unless all of standard input was sent. A reset follows a failed read, write or write deadline; a successful send ends the stream, and the outcome is reported first (`TestTheSenderResetsTheStreamUnlessAllOfStandardInputWasSent`). Each chunk's write deadline is two minutes (`TestEachChunkOfStandardInputGetsSendTimeoutToBeQueued`). The end of standard input must be acknowledged within thirty seconds (`TestTheEndOfStandardInputMustBeAcknowledgedWithinThirtySeconds`); a reset instead fails with "not all of standard input was sent: the connection was reset". Over the simulator, a sender whose standard input fails resets the connection, and a sender whose standard input ends finishes the stream (`TestASenderWhoseStandardInputFailsResetsTheConnection`, `TestASenderWhoseStandardInputEndsFinishesTheStream`). The relay's connection closes once the relay and the sender are both done. It closes at once for a relay that returns before the sender starts, "the relay alone", over `brokenWriter`, whose writes fail with `errBrokenOutput` (`TestARelaysConnectionClosesOnceTheRelayAndTheSenderAreDone`). `dialRecorder` is a link that keeps the connection it dials. `netcall_test.go` covers the net call (`establish`, recovery and the summary). |
| `netget_test.go` | `TestAnHTTPSFetchTrustsThePublicWebPKI` runs `fetch` over `linkRecorder`, and checks the connector that reaches `DialTLS`. Its `Config().RootCAs` equals a pool of `bundle.Roots()` from `golang.org/x/crypto/x509roots/fallback/bundle`, the certificates of Mozilla's program and nothing else (`CertPool.Equal` compares certificates). It has no pin (`VerifyConnection` is nil) and no ALPN. The golden `net-get-https-untrusted` cannot show this, because a fetch that trusts anything else fails there with the same error. `TestNetGetAllowsTenMinutesForTheRequestAndItsWholeResponse`, over `transferRecorder` (a recording `exchangeStream`), checks that the exchange sets exactly one deadline, ten minutes from its start, in force as the request is written. `TestAnExchangeEndsAtADeadlineOrRequestThatFails` checks that a deadline that cannot be set sends nothing, and that a request that cannot be written reads nothing. |
| `net_test.go` | Target and limit checks, and argument parsing. Targets that are rules, not hosts, are refused before dialling. Limits too small to be of use are refused before dialling. An address is resolved without a call, and `net …` arguments are parsed with the command tree. `TestALookupAConnectionOrAHandshakeIsAllowedTwoMinutes` checks that a lookup, a relay's connection, and an `http://` and an `https://` fetch each get a two-minute timeout, over `linkRecorder`, which records each lookup's and dial's timeout and fails them. |
| `httpurl_test.go`, `httpresponse_test.go` | `httpurl_test.go` covers URL parsing: a URL that would break the request line is refused before dialling; the fragment stays behind, and a bare query gets a path; the Host header names the port unless it is the default. `httpresponse_test.go` covers framing: responses that never carry a body need no length; framing the client cannot check is an error; an interim response is skipped and never taken for the final one; the status code is read; and the head is split from the body. The reading rules that `netget_test.go` covers are these: reading stops at a close, a truncation or the deadline, and also once the response is as long as announced; a body is complete when its length says so or when the session ended properly; a response whose head never ended cannot be used. |

The golden files are in `cmd/satlink/testdata/commands`, `testdata/help`, `testdata/usage` and
`testdata/style`.

### mobile

| File | Role |
|---|---|
| `doc.go`, `error.go`, `transport.go`, `handset.go`, `lists.go`, `serial.go` | API. |
| `link.go`, `connection.go`, `tls.go`, `cancellation.go` | API. `cancellation.go` holds the `Cancellation` object. |
| `logging.go`, `logging_test.go` | API. The internal tests build the unexported bridge, one per test, without touching `slog.Default` (section 4). One test logs through a logger made with `With("pkg", …)`, and another through a record carrying a `"pkg"` attribute. Both check the target and the text of an attribute. `TestASinkThatFallsBehindLosesRecordsRatherThanHoldingUpTheLogger` holds the queue to the 256 records that `SetLogSink` documents. |
| `boundary.go`, `convert.go`, `errormapping.go`, `goroutine.go`, `handsetslot.go`, `linkconfig.go`, `linkstate.go`, `listener.go`, `log.go`, `portcloser.go` | Internal. `boundary.go`: what every exported function shares as the boundary with the host app. It recovers panics and refuses nil and zero values. `convert.go`: the saturating conversions of milliseconds, seconds and counts. `errormapping.go`: the error mapping. `goroutine.go`: named goroutines that recover their panics. `handsetslot.go`: where an `IridiumHandset`'s handset is, whether lent or stranded. `linkconfig.go`: `LinkSettings` converted to satnet's configuration. `linkstate.go`: the app's copies of the link's state and status. `listener.go`: the listener goroutine. `log.go`: this package's own logging. `portcloser.go`: the goroutine that closes a port once its call has ended. |
| `link_integration_test.go` | The link tests, over `simulatedUSB`, a `simtest.ByteTransport` over `simtest.NewSimulator(t, sim.DefaultConfig())`, and `stubbornUSB`, a `simtest.FaultyTransport` with `DeafToEscape` and `DeafToDTR` set. Its `DeafToDTR.Store(false)` makes the handset honour DTR, and `PanicsOnNextEscape` makes the next escape panic, then turns itself off. `noDTRUSB` is a `simtest.ByteTransport` over a `simtest.FaultyTransport` over a `satlink.Transport` that hides the DTR line, so DTR changes fail with "this transport has no DTR line". The failing-hang-up and no-CPAS variants use `FailsNextHangUp` and `WithoutCPAS`. `dataCallThatWillNotEnd` is a `mobile.ByteTransport` of its own, whose reads with nothing to give nap for the lesser of the timeout and 10 ms. The test transports are in `support_integration_test.go`. The link-level cases cover: a setup cancelled before it dials gives a lent handset back; cancelling a link that is up changes nothing; limits too small to be of use; `Reinitialize` strands a ready handset whose call cannot be ended (`TestReinitializeStrandsAReadyHandsetWhoseCallCannotBeEnded`); collected links, including one whose listener hears it close and a lent one that gives its handset back; a lent link's listener hears every change; and two goroutines taking turns setting links up with one handset leave it usable (`TestSetupsTakingTurnsWithAHandsetLeaveItUsable`). |
| `logging_integration_test.go` | `TestRecordsAtOrAboveTheLevelReachTheSinkOnceSet` runs in a child process. It re-executes `os.Args[0]` with `-test.run=^TestLogSinkChild$` and an environment marker. The child, which is skipped without the marker, checks the exact records, and checks that a second `SetLogSink` fails. Because of this, `-count=N` and other tests cannot interfere. |
| `export_test.go` | Test-only exports for package `mobile`, shared with the external tests. `ListenerPatience` (10 times `listenerPoll`). `SlowCallSetup` (5 s, the call setup of `TestALinkBeingSetUpCanBeCancelled` and `TestClosingAHandsetWhileItsLinkIsSetUpClosesItOnceTheCallEnds`). `CollectGarbageUntil` runs `runtime.GC` every 50 ms until a condition holds, within 30 s. `HangUpAtCleanup`. `StateRecorder`, a `LinkListener` that records the texts of `DescribeState`. `AssertHeardEveryChange` waits for a close with any reason, then expects Up, Closing(Requested) and Closed(Requested). `DescribeState` gives texts such as "Closed(Requested)". |
| `support_integration_test.go`, `connection_integration_test.go`, `handset_integration_test.go` | External tests, and the helpers that only they use. Helpers shared with the internal tests are in `export_test.go`. `connection_integration_test.go` covers TCP, UDP and TLS connections, over helpers that close the connection they open at the test's end (`openEcho` in `link_integration_test.go`, `openUDPEcho` and `openTLS`, through `closeAtCleanup`, which reports a failing `Close` as `closeConnection` does; `TestTheConnectionHelpersCloseTheirConnectionsAtTheTestsEnd`). It also covers `SendSome` and `Abort`: `SendSome` over TLS, a `SendSome` that queues part at once and then times out with the send buffer full, and `Abort` of a TLS connection, after which `ShutdownWrite` fails because no close_notify can be sent. An aborted TCP connection keeps what it had settled, so `WaitAcknowledged` and `ShutdownWrite` succeed and `Receive` returns what had arrived (`TestAnAbortedConnectionKeepsWhatWasSettled`). `SendSome` and `Abort` after `Close` are covered, as is TLS `Close` while a `Send` waits for room (serial, and skipped under `-short`). A closed UDP connection is covered, and `UDPConnection.Receive(0, …)` takes a datagram and keeps none of it (`TestUDPDatagramsAreEchoed`). `TestReceiveRefusesALengthItCannotHonour` is also here. `handset_integration_test.go` covers `IridiumHandset` over the simulator: status, SMS, `SatlinkErrorHandsetBusy`, and a closed `IridiumHandset` refusing every method, including after a link that had it has ended. `TestClosingAHandsetLeavesTheAppsTransportAlone` checks the transport through a new `IridiumHandset`. |
| `error_test.go`, `transport_test.go`, `convert_test.go`, `zerovalue_test.go`, `lists_test.go`, `nilargs_test.go`, `boundary_test.go`, `cancellation_test.go`, `connection_test.go`, `handset_test.go`, `handsetslot_test.go`, `closingport_test.go`, `linkconfig_test.go`, `linkstate_test.go`, `listener_test.go`, `log_test.go`, `portcloser_test.go`, `tls_test.go`, `serial_test.go` | Internal tests. `error_test.go` holds the error-mapping table: every satlink and satnet kind, `net.ErrClosed`, `*ParseRuleError`, a failed hang-up, and the kinds, fields and texts for each. `ErrCallMayStillBeUp` maps to `SatlinkErrorHangUpFailed`, `ErrCancelled` (also inside `ErrHandset`) maps to `SatlinkErrorCancelled`, and a cancelled setup whose hang-up failed maps to `SatlinkErrorHangUpFailed`. An establishing error with `CallMayStillBeUp` maps to `SatlinkErrorHangUpFailed` with the error's text, while the generic mapping keeps such an error's own kind. An `ErrLinkDown` from a connection keeps its reason. `cancellation_test.go`: a `Cancellation`, made or zero, cancels; the link's call settings share it; without one, a link has its own. `handset_test.go`: `endStrandedCall`, whose failing `Initialize` or `ATH` becomes `ErrCallMayStillBeUp`, and `asCallMayStillBeUp`, which keeps an error of that kind and rewords any other, flag included. `handsetslot_test.go`: `strand` takes only a ready handset; a settled setup leaves the next setup's handset alone, whether its link did not come up or came up and ended (`TestASettledSetupLeavesTheNextSetupAlone`); the slots' names. `transport_test.go`: the transport adapter, which clamps milliseconds to [1, 2147483647], copies at most `len(p)`, maps a host error to `ErrTransport` with its message, and turns a panicking `ByteTransport` into `SatlinkErrorInternal`. A panic once the call is up pulses DTR, which ends the call over a transport whose DTR line reaches the handset (`TestAPanicOnceTheCallIsUpPulsesDTRWhichEndsTheCall`). A call that a panic left up is ended by a new `IridiumHandset` on the app's transport, over a handset deaf to DTR (`TestACallAPanicLeftUpIsEndedByANewHandsetOnTheAppsTransport`). `convert_test.go`: the saturation of timeouts and seconds. `zerovalue_test.go`: every exported method on a zero receiver, through `reflect`, must not panic and must report "the %s was not made by this library". `nilargs_test.go`: every function with nil arguments. `lists_test.go`: `Get` out of range. `connection_test.go`: `receiveStream` over a fake stream (data, data with `io.EOF` or a truncation, the end of the stream, and the failures: a TLS session cut short gives `SatlinkErrorTLSTruncated`, a timeout, a closed connection, and a deadline refused by a closed connection). It also covers `receiveBuffer`'s 64 KiB cap, which the test writes out itself rather than taking it from `maxReceiveLen`, a stream buffer that needs room for a byte, and a datagram buffer that may be empty but not negative. `handset_test.go` also covers `Close` of a handset that is lent or dialling, during a real `EstablishLinkWithHandset`, which does not wait for a `PollEvents`. A fault in `reclaim` leaves the lock free. `closingport_test.go` holds the port double, which carries its simulator's control (`newClosingPortWith(config)`, `connectPanickingPort`, `closePanickingPort`). `listener_test.go`: the listener goroutine stops once it has delivered `LinkStateClosed`, and a link that cannot be followed (a nil watcher) logs "the link listener panicked: …". `log_test.go`: `recordErrors`, a process-wide logger built on `internal/logtext` that keeps this package's error records, for tests that never call `t.Parallel`; `TestRecordingErrorsLeavesTheLogPackageAsItWas` checks that it leaves the log package as it was. `portcloser_test.go`: a port closer whose port panics as it closes logs "the port closer panicked: a port bug while closing" and the app carries on. `boundary_test.go`: `survivePanic` logs "recovered a panic in %s: %v". `linkconfig_test.go` and `tls_test.go`: the settings' conversion. A host address that is not IPv4 is refused with the reason "invalid IPv4 address syntax" (`TestHostAddressesMustBeIPv4`), and a name added twice keeps its last address (`TestANameAddedTwiceKeepsTheLastAddress`). Adders race a link or a connector under `-race`. The TLS settings are checked in a fixed order. The internal listener tests also assert the close reasons, through `AssertHeardEveryChange`. `serial_test.go`, with `serial.go`'s build constraint: panics close the port, a collected serial link closes its port, and a serial link has a listener. |
| `bindings_test.go`, `unusableinits_test.go` | Skipped under `-short` (section 4, Bindings check). `bindings_test.go` builds the pinned `gobind` into `t.TempDir()` and runs it for Android, iOS, Mac Catalyst (where the serial API is absent) and macOS (where it is present). It fails on any skipped symbol. It compiles the package for each target. It checks that the Java declarations the Kotlin sources use exist: the establish functions with their `Cancellation`, `Cancellation()`, `IridiumHandset`'s `hangUp` and `reinitialize`, `SatlinkErrorHangUpFailed` and `SatlinkErrorInternal` among them. It checks that `ByteTransport.java` declares exactly the four methods that `AndroidUsbTransport.kt` implements. `TestTheJavaBindingsCompareExportedFieldsOnly` checks which fields each generated `equals` compares, so that the package doc's Equality section is revisited if gobind changes. When a JDK is found through `JAVA_HOME` or `PATH`, the test compiles the generated Java with `javac --release 8` against a stub `android.content.Context`. `unusableinits_test.go` runs `scripts/mark-unusable-inits.sh` on the macOS header that gobind writes, and type-checks the Objective-C against it with clang (section 4, Bindings check). |

### Support packages

Module-internal packages shared by several of the packages above. Their doc comments are the
contract.

| Package | Users | Contents |
|---|---|---|
| `internal/asciicase` | satlink, satnet, sim, `cmd/satlink` | `EqualFold(a, b string) bool`, `Lower(text string) string` and `Upper(text string) string` fold ASCII letters only: 'A'–'Z' and 'a'–'z' change, and nothing else does. Go's `strings.EqualFold`, `ToLower` and `ToUpper` follow Unicode, which maps 'ſ', 'ı' and the Kelvin sign onto ASCII letters. The core matches the echo, `responsePrefix` and `mayGoOnline` with it; satnet uses it for host names and DNS answers; sim uses it for AT commands and DNS names; the CLI uses it for HTTP header names. Its tests cover those three letters, the bytes just outside the ASCII letter ranges ('@', '[', '`'), letters beyond ASCII, and bytes that are not UTF-8. Each user keeps a parity test of its own: the core's echo, satnet's and sim's Kelvin sign, sim's `ATſ12=50`, and the CLI's `Tranſfer-Encoding`. |
| `internal/errnotext` | serial, `cmd/satlink` | `Text(errno syscall.Errno) string` gives an error number's text as C's strerror(3) writes it, which is Go's text with a capital first letter ("No such file or directory"). The CLI reports such errors in this form. Used by serial's `systemError` and the CLI's `osError`, which adds " (os error N)". |
| `internal/logtext` | mobile, `cmd/satlink`; the record key and trace level for satnet, sim, mobile and ipstack's `netlog` | The one rendering of log records for people (section 2, Logging). `TestAHandlerFromLevelTraceShowsTheRecordsLoggedFourBelowDebug` pins the trace level. `LevelTrace` is `slog.LevelDebug - 4`. `PackageKey` is `"pkg"`. `LevelName(level slog.Level) string` gives the level's name. `NewHandler(minimum slog.Leveler, emit func(level slog.Level, pkg, text string)) slog.Handler` builds a complete handler, with `WithAttrs` and `WithGroup`, that is safe for concurrent use. A nil `minimum` means `LevelInfo`, and a nil `emit` panics. Each record is rendered and passed to `emit` from the logging goroutine. The attributes are rendered by `slog.TextHandler` itself, so its quoting holds exactly. |
| `internal/ringprovider` | satnet, sim | The fixed TLS settings that satnet's TLS client and sim's TLS echo share: `TLS12CipherSuites() []uint16`, the six TLS 1.2 suites (ECDHE-ECDSA and ECDHE-RSA with AES-256-GCM, AES-128-GCM and ChaCha20-Poly1305), and `KeyExchangeGroups() []tls.CurveID`, X25519, P-256 and P-384. Each call returns a fresh list, because a `tls.Config` keeps the list it is given. The lists are those of rustls 0.23.46's ring provider, and the tests hold them as literals. satnet's client offers them and sim's echo serves them (section 4, TLS configuration and sim). |
| `internal/debugtext` | satlink, satnet, sim, mobile, `cmd/satlink` | Debug-style text of the values that messages print, in the form the CLI has always printed them. `Quote(text string) string` quotes a string for every message that quotes one (section 2, Errors). `IPv4Settings(local, peer netip.Addr, dnsServers []netip.Addr) string` gives the debug form of ppp's IPv4 settings for the "IPv4 up" log lines, for example `Ipv4Settings { local_address: 10.64.0.2, peer_address: Some(10.64.0.1), dns_servers: [10.64.0.1] }`. It takes the settings' fields, so the package imports nothing from the module and the core can use it. `Quote` escapes a character as `\u{…}` when it is not printable, extends a grapheme, or is default-ignorable. The default-ignorable check is `isWrittenAsCodePoint`, which tests Other_Default_Ignorable_Code_Point, because Go's tables have no Default_Ignorable_Code_Point. The exception is the halfwidth katakana sound marks U+FF9E and U+FF9F, which are written as they are. The Unicode properties come from Go's tables. Checked against rustc 1.99.1's string escaping, over every Unicode scalar value and 200,000 random strings (both at Unicode 17.0.0), `Quote` writes the same text. `TestQuoteWritesStringsInDebugStyle` holds rustc's texts as literals, including the Hangul fillers and the sound marks. `TestQuoteWritesAByteThatIsNotUTF8AsAnEscape` pins the form for a byte that no `string` can hold: `\xNN` in lower case, as `u8::escape_ascii` writes it, next to a valid U+FFFD written as it is. |
| `internal/utf8lossy` | satlink, ppp, satnet (the labels of DNS answers), sim, `cmd/satlink` | `String(data []byte) string` writes one U+FFFD for each maximal subpart of an ill-formed sequence. `strings.ToValidUTF8` would replace a whole run with one. |
| `internal/memconn` | sim | An in-memory `net.Conn` for crypto/tls. `Conn` is built with `New(local, remote netip.AddrPort)`, and has a pump side for its owner: `Inject`, `CloseInbound`, `Extract`, `Pending`, `AwaitReader(timeout)`, `EOFDelivered` and `Closed` (section 4, sim). The broadcast that it waits on is in `signal.go`. `tls_test.go` pumps a crypto/tls client and server step by step over two `Conn`s. The tests run with `-race`. The tests of what wakes `AwaitReader` (a `Read` that starts waiting, and `Close` after the FIN) run in `testing/synctest` bubbles. There, an `AwaitReader` that was woken returns with no fake time passed. One that gave up after its timeout returns that much later, with the same result, because `AwaitReader` looks once more before it gives up. The two parts of its rule (the bytes, and the FIN, that a woken `Read` has yet to take) are tested by holding that `Read` still, since a test cannot catch the moment from outside (`TestAwaitReaderWaitsUntilTheReaderHasTakenWhatWasInjected`; the internal helpers are `bufferWithoutWaking`, `closeInboundWithoutWaking` and `wakeWaitingReads`). Every receive from a test goroutine goes through `awaitResult`, bounded by `patience`, so that a regression fails by name instead of hanging `go test`. |
| `internal/simtest` | satnet, mobile, `cmd/satlink` (their internal and external tests) | Test support for their tests. `EscapeGuard` (20 ms), `CallEscapeGuard` (60 ms) and `SimulatorConfig()`. `NewSimulator(t, config)` is closed by `t.Cleanup`. `NewHandset(t, config) (*satlink.Handset, *sim.Control)` and `InitializedHandset(t, transport)`. `FaultyTransport` (`NewFaultyTransport(transport)`) has the switches `DeafToEscape`, `DeafToDTR`, `PanicsOnEscape`, `PanicsOnNextEscape`, `PanicsOnNextRead`, `FailsNextHangUp`, `WithoutCPAS` and `Unplugged`, all `atomic.Bool` fields, and `DTRDrops()`. The switches whose names contain `Next` turn themselves off once they have acted. `ByteTransport` (`NewByteTransport(transport)`) is a `mobile.ByteTransport` by its methods alone. `Eventually(t, timeout, what, condition)` polls for a condition. The "this transport has no DTR line" helper that both transports share is in `dtr.go`. Its tests run with `-race`. sim's internal tests cannot import it, because that would be an import cycle. It imports neither satnet nor mobile. |

Tests that hand-build IPv4, TCP or UDP datagrams (satnet's pacing, device and stack, sim's
connection requests, DNS and support) build them with `internal/ipstack/wire`'s Reprs rather
than with builders of their own.

---

## 2. Conventions

These hold in every package. A reviewer should be able to predict a name or a signature
from them.

### Names

- Go initialisms: SMS, URC, IMEI, IMSI, AT, TCP, UDP, DNS, TLS, IP, IPv4, MTU,
  MRU, ACCM, PPP, LCP, IPCP, PAP, CHAP, MD5, USB, ID, DTR, RTS, ALPN, SHA256,
  URL, HTTP, PEM, DER.
- No stutter: the name does not repeat its package. Examples: `sim.Handset`,
  `sim.Config`, `satnet.Config`, `satnet.Link`, `satnet.TCPConn`.
- Product names are `satlink`: the binary, the environment variables `SATLINK_PORT`
  and `SATLINK_DATA_NUMBER`, `linux/99-satlink.rules`, the goroutine and log names
  `satlink-link`, `satlink-link-listener`, `satlink-log` and `satlink-port-closer`, the
  CHAP authenticator name `satlink-ppp`, the CLI's `User-Agent: satlink-cli`, and the
  `satlink hangup` command that its hang-up warning suggests. Hardware facts keep their
  Iridium names (`IridiumUSBVendorID`, `IridiumInternetNumber`, `IridiumHandset` in
  mobile, "Iridium 9575", the simulated network's greeting, and the test CA's name).

### Enumerations

- A fieldless enum is `type X int` with constants named `X<Variant>` (`satlink.SMSFilterUnread`,
  `ppp.PhaseOpen`, `ppp.AuthProtocolCHAPMD5`) and a `String` method. A value without a
  constant is refused where it is an input (`Handset.ListSMS`, `ppp.NewEndpoint`; section 5).
- A data-carrying enum is a struct with a `Kind` field of type `XKind` plus the payload
  fields, comparable with `==`: `satnet.LinkState{Kind: satnet.LinkStateClosed, Reason:
  satnet.CloseReason{Kind: satnet.CloseReasonCarrierLost}}`. Kind constants are named after
  the struct: `ppp.EventUp`, `ppp.DownReasonLoopedBack`, `satnet.LinkStateUp`,
  `satnet.CloseReasonPPP`, `satlink.FinalResultNoCarrier`. Payload fields are named after
  their meaning: `Protocol`, `Detail`, `Reason`, `PPP`.
- The exception: `ppp.Event` carries `ppp.IPv4Settings`, whose `DNSServers` is a slice, so
  neither is comparable with `==`. Compare them with `Event.Equal` and `IPv4Settings.Equal`
  (as ipstack's `TCPRepr.Equal`). Records that hold pointers compare them by pointer with
  `==` (`satnet.Status.IPv4`, `ppp.Config.Keepalive`, and a server role's policy), and their
  docs say so.
- Where a value is a protocol code, the Go value is the code. `satlink.ChargeStatus` holds
  the +CBC code, so `Unknown(n)` needs no field. The `RegistrationStatus` constants are the
  3GPP codes 0 to 5, and every other code becomes `RegistrationStatusUnknown`.
- `String()` returns the text the CLI prints for the type. For `DownReason` and `CloseReason`
  that is their display text. Otherwise it is the constant's name without its prefix, which
  is what the CLI prints with `{:?}`-style output (`OnBattery`, `Unknown(3)`, `Home`), and
  unknown values print as `TypeName(n)`. Where a log line prints a struct, `internal/debugtext`
  writes the text.
- Configuration choices that callers build but never inspect are opaque values made by
  constructors (`ppp.ClientRole`, `ppp.ServerRole`), so no invalid state can be expressed.
- gomobile cannot bind named integer types. In `mobile`, enumerations are untyped `int`
  constants (`mobile.LinkStateUp`, `mobile.SatlinkErrorPolicyDenied`).

### Optional values

One rule, applied everywhere:

| Case | Go form | Examples |
|---|---|---|
| A value whose zero value cannot be a real value | the zero value, documented at the field | SMS `Timestamp ""`, `Registration.CellID ""`, `ppp.IPv4Settings.PeerAddress` (zero `netip.Addr`), `serial.Port.Model ""`, `Limits.MaxBytes 0`, `Tuning.TCPKeepalive 0`, `sim.LinkProfile.BytesPerSecond 0` (unlimited) |
| An optional struct | pointer, `nil` for none | `DataCallSettings.Bearer`, `ppp.Config.Keepalive`, `ServerConfig.Authentication`, `sim.Config.Authentication`, `satnet.Status.IPv4` |
| A lookup or poll that may find nothing | `(T, bool)` | `Endpoint.PollEvent`, `ReceiveIPv4`, `NextTimeout`, `IPv4`, `ModelForProductID`, `Watcher.NextStateChange`, `WaitClosed` |
| An optional value that may legitimately be the zero value | a presence `bool` next to the value | `Response.Connected` with `ConnectMessage` (a bare CONNECT has an empty message) |
| An optional deadline | `time.Time`, zero meaning none | `TCPConn.WaitAcknowledged`, net.Conn deadlines |

Limits have no way to express "set to zero", which is invalid, so they reject negative
values instead, and zero means "no limit". mobile has its own rule for results (section 4):
gomobile turns nil into null, so only fields documented as optional are ever nil.

### Constants and defaults

- Every constant keeps its exact value and its comment, as a named constant, internal
  constants included. No constant becomes an inline literal, and defaults built from
  constants are built from theirs (`sim.DefaultConfig` from `defaultModel`, `defaultIMEI`
  and so on; ppp's `DefaultTimers` and `DefaultKeepalive` from `defaultRestart`,
  `defaultKeepaliveInterval` and so on). A default that is itself a struct literal stays
  one: `satnet.DefaultTuning` and `DefaultLimits` keep their reasons in the field and
  function docs.
- Struct-valued constants are functions that return a fresh value, so no caller can change
  them for everyone: `satnet.IridiumInternetBearer()`, `satnet.AllPorts()`,
  `sim.InstantLinkProfile()`, `sim.IridiumLinkProfile()`, `satlink.KnownProducts()`,
  `sim.SecureTestKeySHA256()`.
- A default-value function is `Default<Type>()`: `DefaultDataCallSettings`,
  `ppp.DefaultTimers`, `ppp.DefaultKeepalive`, `satnet.DefaultLimits`,
  `satnet.DefaultTuning`, `sim.DefaultConfig`, `sim.DefaultNetworkConfig`.
- A constructor that takes the one required input is `New<Type>(input)`:
  `satnet.NewConfig(policy)`, `ppp.NewConfig(role)`. Constructors that validate return an
  error (`ppp.NewEndpoint`, `satnet.NewTLSConnector`). A text parser is `Parse<Type>(text)`:
  `ParseAllowRule`, `ParsePortRange`.

### Zero values and interfaces

- Every type says what its zero value is: useful (`satnet.Policy`, `AllowRule` and
  `PortRange` allow nothing; `DataCallSettings` documents its zero timeouts), or not usable,
  naming what makes one ("The zero TCPConn is not usable; Link.Dial and Link.DialContext make
  one"). A zero `AllowRule`'s target kind is `targetNone`, which matches nothing.
- A constructor given a nil required interface panics (`satlink.NewHandset`). mobile, whose
  callers use other languages, refuses nil arguments with an error instead (section 4).
- A type that promises an interface asserts it at compile time, so that an optional
  interface such as `DTRController` cannot silently stop matching:
  `var _ satlink.DTRController = (*Handset)(nil)` in sim, serial and for `DataCall`;
  `var _ net.Conn = (*TCPConn)(nil)` in satnet; and so on for mobile's internal transport
  adapter.

### Errors

One pattern in every package:

- The error type is a struct with a `Kind` field plus the fields the kinds need:
  `*satlink.Error`, `*satnet.Error`, `*ppp.ConfigError`, `*ppp.SendError`,
  `*satnet.ParseRuleError`, `*mobile.SatlinkError`.
- The kind type implements `error`, so every kind constant is a sentinel:
  `errors.Is(err, satlink.ErrTimeout)`, `errors.Is(err, satnet.ErrLinkDown)`,
  `errors.Is(err, ppp.ErrNotOpen)`. Kind constants are named `Err<Variant>`
  (`satnet.ErrRule<Variant>` for rule parsing). A kind's own text is a short label; the
  error's text has the details. (`mobile` binds no named types, so its kinds are int
  constants, and `AsSatlinkError` recovers the details.)
- `Error()` texts are fixed. The CLI's output and its golden files pin them, so changing one
  means updating the goldens. `Quote` escapes a string the way the CLI's output has always
  escaped one: `"\u{b}"` where `%q` writes `"\v"`, `"\u{1b}"` for `"\x1b"`, `"\0"` for
  `"\x00"`, and `\u{…}` for the marks that extend a grapheme and for the default-ignorable
  letters, such as the Hangul fillers, which `%q` writes as they are. The core's command and
  number texts quote with it, as do satnet's host and rule texts, mobile's host text, the
  CLI's URL, target and header texts (the URL check refuses whitespace and control
  characters, and its reason quotes them), and quoted values in command-line errors. Every
  other message that quotes a string uses it too, so the messages read alike. In the texts this
  guide quotes, `%q` stands for it. A list of strings is written with `debugList`,
  `["a", "b"]` (section 4), which is built on `Quote`.
- `Unwrap` returns the cause: a transport's error inside `satlink.ErrTransport`, the
  `*satlink.Error` inside `satnet.ErrHandset`, the crypto/tls or x509 error inside
  `satnet.ErrTLS`.
- `satnet` errors also match the standard library's equivalents of the `io.ErrorKind`
  conversion (table in the satnet package doc): `os.ErrDeadlineExceeded` for timeouts,
  `syscall.ENOTCONN` for a link that is down, `os.ErrPermission` for policy denials,
  `os.ErrInvalid` for invalid input, `syscall.ECONNRESET`, `syscall.ECONNREFUSED`, and
  `io.ErrUnexpectedEOF` for TLS truncation. Two look-alikes are deliberately not matched,
  because the net package uses them for other meanings: `net.ErrClosed` (this side closed the
  connection; mobile maps it to "the connection is closed") and `context.DeadlineExceeded` (a
  context ended; `Link.DialContext` reports that as `&net.OpError{Op: "dial", Net: network,
  Err: ctx.Err()}`, as `net.Dialer` does). Timeouts are `net.Error`s with `Timeout()` and
  `Temporary()` true.
- A failed hang-up is a flag on the error of whatever failed: the `CallMayStillBeUp` field of
  `*satlink.Error` and `*satnet.Error`. The flag never changes the text.
- The error that `Handset.Initialize` and `Handset.Answer` return when a call may still be up
  has the kind `satlink.ErrCallMayStillBeUp`, with the text in `Message` and the flag set too.
  The kind is also the one sentinel for the whole module:
  `errors.Is(err, satlink.ErrCallMayStillBeUp)` matches an error of that kind or with the flag,
  through any wrapping. `Error.Is` checks the flag before the kinds. An error that is passed on
  keeps its own text and flag; a caller never has to convert it.
- mobile maps the kind to `SatlinkErrorHangUpFailed`, and sets the flag only where the bindings
  check it. An `IridiumHandset` whose handset returns an error of that kind is stranded, as
  the handset's call wrapper does. The check is on the kind (`isCallMayStillBeUp`), not on the
  flag (section 4, mobile).
- `ErrCancelled` ("the call setup was cancelled") is not `context.Canceled`: a context ending is
  not what stopped the call, a `Cancellation` is (below).
- Transports may return any error. The handset wraps it as
  `&satlink.Error{Kind: ErrTransport, Err: err}` and passes a `*satlink.Error` through unchanged.
- Never ignore an error silently. Where an error is deliberately discarded, write `_ = …` with
  a comment saying why that is safe.

### Cancellation

- `DataCallSettings.Cancellation` is a `*satlink.Cancellation` that the app can cancel. Copies
  of the settings share the pointer. `DefaultDataCallSettings`, and so `satnet.NewConfig`,
  makes a fresh one each time, so two defaults are not `==`.
- A nil `Cancellation` means that the setup cannot be cancelled: `Cancelled` on nil is false,
  and `Cancel` on nil panics, so that a cancel that cannot take effect never passes silently.
  The zero value is ready to use, so that mobile's gomobile type can hold one by value and
  pass its address.
- A context drives it: `stop := context.AfterFunc(ctx, settings.Cancellation.Cancel)`, then
  `stop()` once the call is set up.
- satnet's `Establish` and driver read `config.Call.Cancellation`. The CLI's interruption handler
  cancels it. mobile has a `Cancellation` object of its own, which holds one.

### Time

- Sans-I/O code (ppp, ipstack, the simulator's modem, pacing, the name cache) takes
  `now time.Time` and never reads the clock. Only the I/O edges call `time.Now()`. Monotonic
  readings make the `time.Time` values stand for instants.
- Blocking calls take `time.Duration` timeouts. A read with a timeout of zero or less only takes
  what has already arrived: a transport's `ReadTimeout`, or a connection's read with a deadline
  of now. A handset command whose timeout is zero or less is sent and fails with `ErrTimeout` at
  once (`Handset.SetTimeout`). The longest timeout (`math.MaxInt64`) means no deadline:
  `time.Time.Add` drops the monotonic reading instead of overflowing, so `time.Now().Add(timeout)`
  is safe, and `deadlineAfter` returns no deadline for it. A loop that waits for the handset
  reads at most a second at a time and looks at its deadline again (`longestRead` in
  deadline.go), so a transport is never handed an absurd timeout. satnet's engine does the same
  with `maxWait`. Conversions from milliseconds or seconds (mobile, the CLI's options) saturate at
  `math.MaxInt64` instead of overflowing, so that an absurd value meets the "at most a year" check
  rather than turning negative.
- Configured durations are limited to at most a year, and, in Go, are not negative.

### Integers

Counts, levels, indices and sizes are `int`; ports `uint16`; USB IDs `uint16`; the ACCM
`uint32`; the MRU `uint16`; byte and frame counters `uint64`. In `mobile`, gomobile binds no
unsigned types. Its Objective-C headers declare a Go `int` as `long`, but gobind's C glue
(`typedef int nint` in `seq_darwin.h`) narrows it to a 32-bit C `int` on every Apple platform.
On Android it is a `jlong`, and a Go `int` has 32 bits on armeabi-v7a. A larger value wraps
before Go sees it. So timeouts, the link settings' seconds and byte counts are `int64`, and the
remaining `int` values (lengths, ports, indices, levels, enumerations) never need more than 31 bits.

### Ownership: the handset is never moved

A `DataCall` and a `satnet.Link` do not take the handset away from its caller. In Go the caller
keeps its `*satlink.Handset` throughout:

- `Handset.DialData` and `AnswerData` claim the handset. While a `DataCall` (and so a
  `satnet.Link`) holds it, the handset's methods that talk to it fail at once with `ErrBusy`,
  `TakeURCs` returns nothing, and `SetTimeout`, `Transport` and `Close` keep working.
- Hanging up releases it, whatever the outcome: `DataCall.HangUp`, a failed dial, a link that
  fails to come up, and a link that hangs up by itself. A `satnet.Link` releases it as soon as
  the call has ended.
- A failed call or link is a plain error, and its `CallMayStillBeUp` flag is the errors'
  `CallMayStillBeUp` (section 2, Errors). The flag is set in these cases:
  - every error of `DataCall.HangUp` (but "the data call is over" for a call already hung up),
    and every error of `Handset.EndDataCall` except the `ErrInvalidArgument` of a guard over
    `MaxEscapeGuard`, and `ErrBusy`;
  - a failed `DialData` or `AnswerData` whose abandoned attempt might have connected and could
    not be ended: an attempt that timed out, was cancelled, or lost the transport; a call found
    up while preparing; or a voice call answered by mistake that ATH could not end. In that
    last case the error is the hang-up's;
  - the errors of kind `ErrCallMayStillBeUp`: `Initialize`'s "the handset does not answer (%v),
    and ending a data call it may be in failed: %v" and "ATH failed on a data call left up: %v",
    and `Answer`'s "ATA answered a data call that could not be ended: %v";
  - `Link.Close`'s error when the hang-up failed, which wraps the handset's error (`ErrHandset`),
    or when the hang-up itself panicked (`ErrLinkDown`, with `CloseReasonInternal`, "the link's
    driver thread panicked");
  - `Establish`'s error when the link did not come up and its hang-up failed too. The error still
    says why the link did not come up: for a setup cancelled before or after CONNECT, it is
    `ErrHandset` wrapping `satlink.ErrCancelled`.

  `Link.Status().CallMayStillBeUp` says so too. mobile reports `SatlinkErrorHangUpFailed` for
  `HangUp`, for an `Establish…` that failed so, and for the handset's errors of kind
  `ErrCallMayStillBeUp`: from `Initialize` inside `NewIridiumHandset`, `OpenSerialHandset`, the
  establish functions and `Reinitialize`, and from `Answer`. A lent or ready `IridiumHandset` is
  then stranded until `Reinitialize` succeeds (section 4, mobile). The advice in every doc is to
  retry with `Handset.EndDataCall` once the handset answers again; power-cycling it is the last
  resort. Closing the port drops DTR, which a failed hang-up has already tried, either by its DTR
  fallback or, when a transport's panic cut it short, by the pulse that panic sets off (section 4,
  Hanging up).

### Drop becomes Close

| Resource | Release in Go |
|---|---|
| `sim.Handset` drops DTR | `sim.Handset.Close` (idempotent). A `Handset` collected without `Close` drops DTR the same way (`runtime.AddCleanup` on the `Handset`). |
| `serial.Transport` closes the port (DTR drops) | `serial.Transport.Close`, which waits up to a second for a write or DTR change under way. `satlink.Handset.Close` closes any `io.Closer` transport. A `Transport` collected without `Close` closes its port all the same (`runtime.AddCleanup` on the `Transport`, holding the library's port, as for `sim.Handset`; `Close` stops it). `serial.Connect` closes the port it opened on every way out but success, a panic out of `Initialize` included (a deferred close guarded by `initialized`). A port that mobile opened itself (`OpenSerialHandset`, `EstablishSerialLink`) is closed too when a panic is recovered during the setup (`closeUnlessHandedOn`). Go has no destructors, so each close is deferred explicitly. |
| `DataCall` holds the handset and its transport | A `DataCall` dropped without `HangUp` ends nothing and leaves the handset busy. Its doc says to hang up in a deferred function. `Handset.Close`, or the collection of the serial `Transport`, closes the port. A transport's panic out of `HangUp` or out of a call's setup pulses DTR (section 4, Hanging up). |
| `satnet.Link` hangs up in the background | `satnet.Link.Close` waits; `RequestClose` does not. A Link collected without Close still hangs up (`runtime.AddCleanup`). |
| `TCPConn` closes gracefully | `TCPConn.Close` (graceful, never waits). A connection collected without Close is closed the same way. |
| `UDPConn` goes once its queue is sent | `UDPConn.Close`. |
| `TLSConn` closes its TCP stream | `TLSConn.Close` (close_notify within the write deadline, then a graceful TCP close). |
| Destroying a mobile object (`.use {}` in Kotlin) | `Close() error` on every mobile object that holds resources: `IridiumHandset`, `SatelliteLink` and the three connections. It releases without waiting and is idempotent. `TLSConnection.Close` releases the connection. `ShutdownWrite(timeoutMillis)` on `TLSConnection` is the counterpart of the TCP one. A closed `IridiumHandset` refuses every method but `Close` with `SatlinkErrorInvalidArgument` "the handset is closed", as the connections refuse with "the connection is closed". Calls on a destroyed object are refused the same way, also once a link that had its handset has ended. `TakeEvents` returns an empty list. A new `IridiumHandset` on the transport talks to the handset again. gomobile objects are not `AutoCloseable`, so apps call `close()` in `finally`. |

Use after `Close` returns `net.ErrClosed` for connections, and `Close` wakes the calls already
waiting in other goroutines with it, as net.Conn requires. `TLSConn.Close` wakes them when its TCP
connection closes, once the close_notify has gone or the write deadline has passed. With no write
deadline and a stalled peer, that can take as long as the link lives. A write deadline of now
closes at once, as mobile's `TLSConnection.Close` does. A `DataCall` used after `HangUp` fails with
`ErrCall` "the data call is over". A closed `sim.Handset` or `serial.Transport` fails reads, writes
and DTR changes.

### Concurrency

- The engine is a `sync.Mutex` with a broadcast channel that is closed and replaced on every
  notify, so a waiter can `select` on it, a timer, and a context. The periodic re-check
  (`maxWait`, one second) stays. The waiting loop is written once (`waitUntil`, with `waitFor` for
  a `Watcher`) and reused.
- Goroutines carry `runtime/pprof` labels and are named in logs (`satlink-link`). A panic in the
  link driver is recovered into `CloseReasonInternal`. A call that the panic kept from being hung
  up is still hung up. A hang-up that itself panicked (a transport bug) is not run again: the core
  pulsed DTR as the panic left it, which ends the call after AT&D2. The link then reports
  `CallMayStillBeUp`, because nothing confirmed the end.
- Every exported type documents whether it is safe for concurrent use. The net.Conn types are safe
  for concurrent use by multiple goroutines, as net.Conn requires (crypto/tls and net/http rely on
  it, closing a connection to interrupt a handshake or a request). `go test -race` passes.
- Use the typed atomics (`atomic.Int64`, `atomic.Uint64`, `atomic.Bool`, `atomic.Pointer[T]`),
  never `atomic.LoadInt64` and friends on plain fields. gomobile builds android/arm, where a 64-bit
  atomic on a field that is not 8-byte aligned panics. `portability_test.go` type-checks every
  package and its tests for linux/386 and linux/arm. Running the tests on such a platform
  (`GOARCH=386 go test` on the packages without cgo) needs a machine that runs its binaries.

### Logging

Producers use `log/slog` through `slog.Default()`, looked up at log time. Each record's `"pkg"`
attribute names the package, which is the log target that consumers see: satnet, sim, ipstack,
and mobile for the panics it recovers where no caller can be told. The core, ppp and serial do
not log. Every logging package has one helper with the same mechanism: level functions that check
`Enabled`, then call `slog.Default().Log(ctx, level, msg, logtext.PackageKey, <package>, args...)`
(`satnet/log.go`, `sim/log.go`, `mobile/log.go`, `internal/ipstack/internal/netlog`). So `pkg` is
always a record attribute, under the key the consumers read (`internal/logtext`'s `PackageKey`,
`"pkg"`). satnet and sim format their message texts at the call site, so a line costs its
formatting even when its level is disabled; only the record is not built. Struct values in those
messages are written by `internal/debugtext`. ipstack passes values as attributes (IPSTACK.md,
section 7). Levels: trace is `slog.LevelDebug-4` (`logtext.LevelTrace`, which `netlog.LevelTrace`
equals), then debug, info, warn and error.

Consumers render every record the same way. The rendering is implemented once in `internal/logtext`
(section 1) and used by the CLI and `mobile.SetLogSink`:

- pkg: the record's `"pkg"` attribute, or one added through `WithAttrs`
  (`slog.Default().With("pkg", …)`); empty when there is none;
- text: the message, then ` key=value` for every other attribute in order, with
  `slog.TextHandler`'s quoting, and groups as `group.key`;
- level names: `ERROR` at error and above, `WARN`, `INFO`, `DEBUG`, and `TRACE` below debug
  (slog itself would say `DEBUG-4`);
- the CLI prints `fmt.Sprintf("[%-5s %s] %s", name, pkg, text)` to standard error, from warn
  (no option), info (`-v`), debug (`-vv`) or trace (`-vvv`) up;
- mobile maps the same thresholds to `LogLevelError` through `LogLevelTrace` and hands the sink
  (level, pkg, text).

### Tests

Test kinds and where they live:

- Unit tests sit beside the code they cover. Integration tests (`*_integration_test.go`)
  drive the public API against the simulator (`sim`, wrapped by `internal/simtest`) or
  against fake transports.
- The sans-I/O packages (the ppp endpoint, the TCP and UDP state machines) take explicit
  `now` values and never sleep.
- Shared helpers live in `internal/simtest` (`NewSimulator`, `NewHandset`,
  `SimulatorConfig`, `CallEscapeGuard`, `FaultyTransport`, `Eventually`), in
  `internal/memconn` (in-memory connections), and in the `export_test.go` files of
  `satnet` and `mobile`.
- Helpers call `t.Helper` and register their cleanups with `t.Cleanup`. Cleanups run
  last in, first out, so the simulated handset closes after the link that uses it.
  A test returns with no call up and no driver running, except a test that leaves a
  call up on purpose: it relies on the simulator's `Close`, which drops DTR, to end it.
- Table-driven tests are used where cases share one shape. Test names state the
  scenario, for example `TestHangUpEscapesToCommandModeThenHangsUp`.

Waiting and timing:

- Tests wait for conditions with deadlines (`simtest.Eventually`, `satnet.AwaitWaitingCall`)
  instead of sleeping. A sleep stands in only for a condition that cannot be observed.
- A test that checks that something wakes a waiting call (`Close`, a moved deadline, a
  cancelled context, a state change) first waits until the call is waiting:
  `AwaitWaitingCall` for link calls, a fake-side helper for TLS calls, and `synctest.Wait`
  in a `testing/synctest` bubble for `sim` and `internal/memconn`. In a bubble, fake time
  also tells a call that was woken, with no time passed, from one that timed out. A call
  started after the `Close` fails at its entry check and never exercises the wake-up, so
  the wait is required; with `GOMAXPROCS=1` the wake-up is never exercised at all.
- A fixed sleep is allowed only for a wait for something not to happen, with a comment
  saying why. Examples: the ICMP-error check in `satnet`, which leaves 300 ms for an error
  to appear, and `heldLineWait` in `cmd/satlink`, which gives a held write 100 ms.
- Every link whose test ends with data queued that need not arrive uses a one-second close
  grace (`linkClosingBriefly`, `tlsLink`). Otherwise the cleanup's `Close` waits for the
  hang-up, up to the 30 s default. A handset deaf to the escape gets a 500 ms timeout
  (`faultyHandset`), so its hang-up does not wait out the five-second default for `ATH`.

Parallelism:

- `t.Parallel` is used only on instant links and on tests that do not depend on timing.
- Tests that assert real-time behaviour run serially. The slowest of them are skipped
  under `testing.Short()`, for example the data-limit and bulk-upload tests in `satnet`.
- Slow tests that assert no timing run in parallel. Their timings come from the handset
  model (escape guard, abort grace, AT timeouts), and a slow machine can only make a
  reply arrive sooner.
- Tests that call `slog.SetDefault` or `SetLogSink` never call `t.Parallel`.
- Timings for a full `go test -race` run, as of this writing: about 45 s for the core
  package, about two and a half minutes for `satnet` (about one minute with `-short`), and
  about 100 s for `mobile`.

Logging in tests:

- Recording log handlers are guarded by a mutex, since background goroutines log into
  them. They keep the attributes passed to `WithAttrs` and match only their own `pkg`.
- A helper that installs a handset with `slog.SetDefault` also saves `log.Writer()` and
  `log.Flags()` and restores them in `t.Cleanup`. `slog.SetDefault` points the `log`
  package at the handler, and restoring only the previous default does not undo that.
  The packages' log tests check this.

Coverage by file:

- `satlink` (core): the dial, data-call and hang-up paths, including the stranded-call
  and call-may-still-be-up cases, and the `ErrCallMayStillBeUp` contract.
- `ppp`: `endpoint_*_test.go` split by concept (`api`, `lifecycle`, `framing`, `rejects`,
  `lcp`, `ipcp`, `auth`), `fsm_test.go` for the negotiation automaton, `auth_test.go` for
  PAP and CHAP, and `support_test.go` for helpers that more than one file uses.
- `satnet`: `link_integration_test.go` for the public link API, with separate files for
  TLS (`tls_test.go`, `tlskeys_test.go`), panics (`panic_integration_test.go`) and logging
  (`log_test.go`), and tests of
  the close reasons (`CloseReasonCallTimeLimit`, `CloseReasonNegotiationTimedOut` and
  `CloseReasonInternal`) in `link_integration_test.go`.
- `sim`: the simulated handset, the commands, the link profiles, and the TLS echo
  (`secureecho_test.go`).
- `serial`: the transport, its timeouts, and the error mapping.
- `mobile`: the binding surface and its errors, with `bindings_test.go` pinning the
  documented equality behaviour (`mobile/doc.go`, Equality). It also
  holds the stranded-handset tests and the runtime-cleanup tests, which drop a link without
  `Close` and collect garbage (`mobile.CollectGarbageUntil`) until it has hung up.
- `cmd/satlink`: each command in-process against `testdata/commands`, `status_test.go`
  (a status whose output cannot be written asks the handset nothing more), help and usage
  errors with the `testdata/help`, `testdata/usage` and `testdata/style` goldens, text that
  is not UTF-8 or contains control characters, the run's exit codes and panic handling in
  `run_test.go`, the interrupt handlers, and the terminal checks. The terminal checks use a
  pseudo-terminal on macOS and Linux (`terminal_pty_darwin_test.go`,
  `terminal_pty_linux_test.go`). The Windows standard-stream test
  (`processstreams_windows_test.go`) is type-checked by cross-vetting for Windows.
- Behaviour that the public-API tests do not reach: `ErrBusy` and
  release, `net.Conn` deadlines (including deadlines moved while a call waits), `Close`
  waking a pending `Read`, `Write` or `WaitAcknowledged`, use after `Close`, `errors.Is`
  against the standard equivalents, password redaction for every verb `fmt` hands a
  `Format` method, gomobile's zero-value constructors and nil arguments, and a transport's
  panic during a data call.

Golden files:

- `cmd/satlink/testdata` holds the golden output of each command, each help text and each
  style case. `go test ./cmd/satlink -update` rewrites them (the `update` flag in
  `golden_test.go`).
- Golden files hold bytes, so tests that check the exact output of a command use them.
  Behaviour that a golden cannot show, such as styling on a terminal, is tested directly.

Fuzzing:

- The wire and TCP parsers have fuzz targets: `internal/ipstack/wire/fuzz_test.go` and
  `internal/ipstack/tcp/fuzz_test.go`. Run them with `go test -fuzz`.

Race detection and build constraints:

- `go test -race ./...` passes.
- Build-constrained tests cover the platform-specific code: the pseudo-terminal tests
  (darwin and linux), the Unix and Windows standard-stream tests, and the Windows console
  test.

Coverage targets:

- `ppp` at least 90%, `satnet` at least 85%, every other package at least 80%, and
  `internal/ipstack` as `IPSTACK.md` section 6 sets.
## 3. Deliberate design choices

Each choice below is intentional and states its reason. Some choices differ from what the
underlying library would do by default. Those are noted with the library's behaviour.

**Handset ownership**

- The handset is never moved. It stays with its `satlink.Handset` (or the `IridiumHandset`
  that wraps it), is released automatically when the call ends, and a use after release
  fails with `ErrBusy`. Go has no moves, so failing fast replaces a compile-time guarantee.
- A `DataCall` dropped without `HangUp` ends nothing and leaves the handset busy.
- A failure that may leave a call up is a plain error with a `CallMayStillBeUp` field. It
  matches `satlink.ErrCallMayStillBeUp` through `errors.Is`. An error passed on keeps its
  own text and flag. Go has no `?` conversion to turn a failure into that error kind.
- `DataCallSettings.Cancellation` may be nil. The setup then cannot be cancelled, and calling
  `Cancel` on a nil pointer panics. A pointer lets copies of the settings share one request.

**Link setup**

- `satnet.Establish` dials before it starts the driver goroutine. A goroutine cannot fail to
  start, so this order costs no airtime.

**Panics**

- A panic in a link's driver is recovered, not raised again, because a panic that leaves a
  goroutine ends the whole process. The driver's outermost recovery hangs up the data call
  (`endCallLeftUp`) before it abandons the engine. A hang-up that itself panics is not run
  again, since a second `HangUp` only reports that the data call is over. The link then
  reports itself closed with `CallMayStillBeUp` set, and `Link.Close` returns `ErrLinkDown`.
- The flag stays set even when the hang-up after a panic is confirmed. This is conservative,
  and mobile maps it to the call-may-still-be-up error.
- `beginClosing` sets `Closing` before it aborts the connections, and `abandon` sets `Closed`
  first. So the link closes for the reason the first panic gave, even when aborting the
  connections panics too. A log handler can panic while connections are aborted, since TCP
  state traces reach it through ipstack, and that panic must neither hide the reason nor stop
  the link closing. `Usage.ConnectedFor` counts the hang-up that follows a panic.
  Tests: `TestTheFirstOfTwoPanicsIsWhyTheLinkClosed`,
  `TestAPanicEveryTimeTheConnectionsAreAbortedStillClosesTheLink`,
  `TestTheCallsDurationCountsTheHangUpThatFollowsAPanic`.
- A transport's panic out of `DataCall.HangUp`, or out of `DialData` or `AnswerData` while they
  set the call up, pulses DTR before the panic goes on. DTR is dropped and raised again, on a
  best-effort basis, and a second panic during the pulse is dropped. The pulse ends the call
  after AT&D2. It is done by `pulseDTRUnless` in `datacall.go`. The Go handset stays with its
  caller, who may recover the panic and use the handset again, so its transport is not
  closed. Only the core can pulse DTR under the handset's lock, before it releases the handset.
  A pulse on the raw transport would race the app's next command.
  Tests: `TestAPanicThatCannotDescribeItselfWhileHangingUpSparesTheProcess` and
  `TestAHangUpThatPanicsAfterAnEarlierPanicSparesTheProcess` in
  `satnet/panic_integration_test.go`.

**Stranded handsets (mobile)**

- A handset is never lost with its link. If a link fails while hanging up, the handset comes
  back stranded. Its calls fail with `SatlinkErrorHangUpFailed` until `Reinitialize`, which
  reaches the handset through its own transport, succeeds. The
  handset, transport included, stays with its `IridiumHandset` whatever the link does, and it
  is unknown whether a call is up. A panic inside `satnet.Establish` that only the driver
  goroutine recovers strands the handset the same way.
  Test: `TestALinkThatLostTheHandsetWhileHangingUpSaysTheCallMayStillBeUp` in
  `mobile/link_integration_test.go`.
- Links that own a serial handset close the port they opened. A panic recovered while
  `OpenSerialHandset` or `EstablishSerialLink` sets up closes that port. If the panic comes
  after the driver goroutine has taken the call, the port closes at once, and the DTR drop
  ends the call.

**Data delivery (satnet)**

- Data that never left a connection's outbox is reported as cut off, not as delivered. An
  address change cuts off connections with data in their outbox, as well as those with data
  in the stack's send buffer. An abort or the hang-up empties the outbox and records the
  connections it dropped data from. `WaitAcknowledged` on a TCP or TLS connection then fails
  for that data: with `ErrConnectionReset` after an abort, and with `ErrLinkDown` and the close
  reason once the hang-up has started. Data acknowledged before the connection ended still
  counts as delivered.
  An emptied outbox leaves nothing to show that data was lost, and the stack's abort keeps
  only what its send buffer took. Data waits in an outbox after the last poll before a
  hang-up, during the driver's final write to the line, or while a keep-alive probe or a stale
  retransmission holds the flow's turn. So the CLI's `net tcp` reports standard input that the
  hang-up dropped as not all sent.
  Tests: `TestWaitAcknowledgedReportsDataTheConnectionsEndDroppedBeforeItLeft`,
  `TestDataTheHangUpDroppedIsNotReportedDelivered`,
  `TestAResetOrTheHangUpCutsOffTheConnectionsWhoseOutboxItEmptied`,
  `TestWhatWasAcknowledgedBeforeTheConnectionEndedStaysDelivered`,
  `TestDataTheStackTakesJustBeforeTheResetStaysCutOff`.
- Each `Write` and `WriteSome` of a `TCPConn` or `TLSConn` goes out whole relative to the other
  writers, as `net.TCPConn` writes do (`writeMu`). `net.Conn` promises safety for concurrent
  use, and an interleaved message helps no caller.
- TCP and TLS connections use `net.Conn` deadlines. `Write` queues all of its input, and
  `WriteSome` returns once part of it is queued. Data already received is returned after a
  deadline has passed, which `net.TCPConn` does not do. `Close` wakes calls waiting in other
  goroutines. `net.Conn` is the Go contract, and `net/http` and `crypto/tls` work over these
  types.
- A timed-out `TLSConn.Write` returns the count of what the session took, with `ErrTimedOut`,
  as `io.Writer` requires.
- `TLSConn.Close` called during a `Write` in another goroutine skips close_notify. Records that
  are already queued still go out within the write deadline before the TCP connection closes.
  `crypto/tls` takes a `Close` during `Write` as a request to break the write.
- mobile's per-call timeouts are applied as the connection's one read deadline or one write
  deadline, which every waiting call in that direction reads again. While two receives, or two
  sends (`ShutdownWrite` included), wait at once, the later timeout applies to both. So
  `TCPConnection`, `TLSConnection` and `UDPConnection` ask callers to receive from one thread
  at a time and to send from one thread at a time. `satnet` offers `net.Conn` deadlines only,
  and per-call deadlines would need new `satnet` API.
- `Tuning.TCPTimeout` of zero means no timeout. Go gives zero the meaning that its other
  optional durations have, which is none.
- `Link.DialContext` and `TLSConnector.Config` exist only in Go, so `net/http` can use a link,
  with HTTPS through the connector's configuration.

**TLS (satnet)**

- TLS uses `crypto/tls` from the standard library. RFC 8879 certificate compression is not
  used: `crypto/tls` does not implement it, and adding it would mean forking `crypto/tls`. A
  server that compresses its chain sends it uncompressed, so full handshakes with such servers
  cost a quarter to a third more airtime. The `satnet` package doc says so.
- The key exchange groups and TLS 1.2 cipher suites are set explicitly from
  `internal/ringprovider`, because `crypto/tls`'s defaults differ. The groups are X25519,
  P-256 and P-384, with a key share for X25519 only, so no hybrid post-quantum group is
  offered. The TLS 1.2 suites are the ECDHE-ECDSA and ECDHE-RSA suites with AES-128-GCM,
  AES-256-GCM and ChaCha20-Poly1305. The defaults would add four ECDHE CBC-SHA1 suites.
- `verifyChainKeys` refuses a chain whose keys fail the key limits: an RSA key under 2041 bits
  or over 8192 bits, or an ECDSA key on P-224. `crypto/x509` alone would accept those keys. A
  chain is accepted if one verified copy of it has no refused key. When verification is
  skipped, no key is checked.
  Tests: `TestTheRefusedKeys`, `TestTheKeysAServersChainMayHold`,
  `TestOneChainWithoutARefusedKeyIsEnough` and
  `TestAConfigurationThatSkipsVerificationChecksNoKey` in `satnet/tlskeys_test.go`.
- The public roots are the NSS snapshot that `go.mod` pins
  (`golang.org/x/crypto/x509roots/fallback`). They are built into a pool directly, and do not
  come from the operating system. The bundle carries NSS's distrust-after dates for roots that
  Mozilla is phasing out, and `rootPoolOf` refuses a chain whose certificate such a root issued
  after its date (`AddCertWithConstraint`). Izenpe.com carries one. The bundle carries NSS's
  own trust decisions, and dropping a constraint would trust what Mozilla distrusts.
  Tests: `TestAPublicRootAnchorsNoChainIssuedAfterItsDistrustDate` and
  `TestThePublicRootsAreMozillasProgram`.
- A server's chain may hold ML-DSA keys, and ECDSA keys on P-521 with SHA-512 signatures, which
  the ring provider cannot verify. `crypto/tls` always offers those, and it cannot withdraw them
  or reorder its signature schemes. Refusing what `crypto/tls` asks for would fail connections
  that the server could serve. A server holding such a certificate beside a classical one may
  send that one, which costs several kilobytes of airtime per handshake. `crypto/tls` also asks
  for RSA-PSS signatures before ECDSA ones, so a server that follows the client's preference may
  send an RSA leaf. Pin every certificate the server holds, with `TLSOptions.PinnedPublicKeys`
  or mobile's `AddPinnedPublicKeySHA256`.
  Tests: `TestAServerSendingTheCertificateCryptoTLSAskedForIsTrusted` and
  `TestTheCertificateCryptoTLSAskedForMustBePinnedToo`.
- A TLS 1.2 session resumes by session ticket only. `crypto/tls` has no client-side session-ID
  resumption, so a TLS 1.2 server that issues no tickets costs a full handshake every time.
  TLS 1.3 servers, and TLS 1.2 servers that issue tickets, resume normally. The `satnet` package
  doc and the `TLSConnector` doc say so.
- The session cache is keyed by server name, ignoring ASCII case (`caseBlindSessionCache`).
  Hostname verification ignores case, so a cache keyed by the name as written would miss on a
  case difference alone. Pins are checked against the leaf after the chain is verified, and
  the server name is checked before anything is sent.
- TLS errors and log lines carry `crypto/tls`'s texts, not those of another TLS library.

**Simulator (sim)**

- The simulator's TLS echo is always on. It runs `crypto/tls` over an in-memory connection, with
  one goroutine per session, and it resumes sessions with stateless tickets keyed per simulated
  internet. So `satlink --simulate net get https://secure.test/` reaches the echo, and fails
  certificate verification against the public roots. `crypto/tls` has no sans-I/O server API,
  and the standard library needs no feature flag.
- The echo keeps `crypto/tls`'s cipher-suite order, at TLS 1.2 as well as TLS 1.3. A client that
  prefers AES-256-GCM agrees `TLS_AES_128_GCM_SHA256` with the echo, not the AES-256 suite it
  prefers. At TLS 1.3 `crypto/tls` lets a server neither choose nor order its
  suites, since `Config.CipherSuites` covers TLS 1.0 to 1.2 only. At TLS 1.2 the echo could
  follow the client, but one rule for both versions was kept. Following the client would save
  no bytes and would matter only to a client of another TLS library that speaks TLS 1.2 at
  most. The comment on `newSecureEchoConfig` gives the same reason.
- A client that hangs up before its handshake completes gets no close_notify, only FIN.
  `crypto/tls` refuses `CloseWrite` before the handshake completes and offers no way to write an
  alert.
- `net get` of an `https://` URL ends its TLS connection with close_notify before TCP's FIN.
  This spends one TLS alert record of airtime more than dropping the TCP connection would. The
  `TLSConn` cannot let a connection go without close_notify, and an API for that would serve
  only this caller.
- A failed session's log line says why in `crypto/tls`'s words, as the echo's log does.

**Serial (serial)**

- Serial writes have no timeout. `Close` does not wake a write blocked in the tty driver, so a
  write guard built on `Close` cannot work. Exclusive access on Unix is the library's `TIOCEXCL`
  ioctl alone, and its failure is ignored. The reason is that `go.bug.st/serial` v1.8.0 has no
  write timeout.
- An OS error number reads as `strerror(3)` writes it, through `internal/errnotext`, because
  Go's error table lower-cases the texts. The serial library's own errors keep its wording.
- On Unix the library waits for input with `select(2)`, whose descriptor sets hold only numbers
  below 1024. A port opened on a descriptor at or above that number fails its reads with
  `ErrTransport`. The library's `Read` builds a 1024-bit set and panics beyond it, and Go raises
  `RLIMIT_NOFILE` at start-up, so a process with about a thousand files and sockets reaches the
  limit. `Transport.ReadTimeout` recovers that panic, and only for such a port: `Initialize`
  fails, and `Connect` and mobile close the port. Any other panic passes on. Upstream,
  `poll(2)` would remove the limit (bugst/go-serial#128).
- `Transport.ReadTimeout` with an empty buffer returns (0, nil) at once, as is conventional for
  an empty `Read`. The library would otherwise read the zero from `read(2)` as a hang-up and fail
  the open port with "Port has been closed".
  Test: `TestAnEmptyReadOfAPortWithInputWaitingReadsNothing`.

**Negotiation (ppp)**

- After a Terminate-Ack, a restart that finds Request-Sent or Ack-Sent sends a fresh
  Configure-Request with a new identifier. The Terminate-Ack itself sends nothing (RFC 1661,
  RTA in Ack-Rcvd). RFC 1661 requires the identifier to change once a valid reply has been
  received, so a peer that negotiates again after its Terminate-Ack must see a new identifier.
  Tests: `TestATerminateAckAfterOurRequestWasAcknowledgedStartsOver` and
  `TestALinkRecoversFromAStrayTerminateAckAtTheNextRestart`.
- A Configure-Nak suggests one value per option kind, the first, however often the peer's
  request repeats the option (`suggestOnce`). Otherwise a request that repeats an option draws a
  Nak that grows with the repeats. A Nak large enough would overflow the packet encoder, and a
  Go panic there would end the process. A hostile peer reaches this only where the configured
  MRU lets a large frame in. A direct ppp user may set an MRU up to 65535, while satnet's MRU is
  its MTU, at most 1500.
  Tests: `TestARepeatedOptionDrawsOneSuggestion` and `TestAHostileRequestCannotInflateTheNak`.
- The authentication wait, Restart times Max-Configure, saturates at the longest `Duration`
  instead of overflowing, for absurd timer settings. ppp's validation does not cap durations.
- `Credentials`, `Authentication`, `Role`, `Config`, `ServerConfig` and `Endpoint` implement
  `fmt.Formatter`, so every verb except `%p` and `%w` prints them redacted. `fmt` handles `%p`
  and `%w` itself, and for those verbs a value that holds credentials can print its fields,
  password included. The `ppp.Credentials` doc lists the cases. `go vet` reports every misplaced
  `%w`. The credentials are held by value, so a client `Role` or `Config` compares by value. Holding
  them behind a pointer would hide them from `fmt`, but would make those types compare by
  pointer.
- The role is an option: `ppp.NewConfig(ppp.ClientRole(...))` or `ppp.ServerRole(...)`. A
  `NewServerConfig` beside the `ServerConfig` type would mislead, so the role stays opaque.

**API shape**

- Go has no option types. A zero limit means no limit, and negative values are refused. Limits
  that are too small to be of use are refused too: a call time shorter than bringing PPP up and
  hanging up takes, an idle time under 25 s, and a data limit under 2048 bytes.
- `sim.LinkProfile.BytesPerSecond` zero means unlimited. A negative value means one byte per
  second.
- `satlink.Handset` is safe for concurrent use, and its calls are serialised. Go callers in
  `mobile` and `satnet` share it across goroutines.
- DTR is an optional interface with `RaiseDTR` and `DropDTR`, which `mobile.ByteTransport` also
  has. An optional interface replaces a default method, and the API takes no boolean flag
  parameters.
- `ConnectMessage` is empty when a response has no message. Go has no option type to tell an
  absent message from an empty one.
- `TakeURCs` returns nothing while a data call holds the handset. This is the semantics of the
  mobile API: no unsolicited results while a link has the handset.
- `ParsePolicy(rules ...string)` and `ParsePortRange` are the parsers. `Policy.Allow` returns a
  new policy, so policies are values. `NewTLSConnector` reports PEM errors. `TLSOptions` is a
  plain struct, not a builder, so its errors appear when the connector is built.

**Logging and the CLI (cmd/satlink)**

- Log lines name the Go package in a `pkg` attribute, as in
  `[INFO  satnet] link closed: closed by the app`. Go has no module path at run time. See
  section 2, Logging.
- The CLI's own lines on standard error wait for the log lines handed over before them, so the
  order is the causal one every time. Go's scheduler otherwise ran the command's goroutine
  before the log's, which put the summary ahead of the hang-up's log lines in four of eight runs.
  Test: `TestTheLastLogLinesOfARunReachStandardErrorBeforeItEnds`.
- A panic on the command's goroutine is recovered and reported with its value and
  `runtime/debug.Stack`. The command fails with `internal error: the command panicked`. The
  handset is closed first, and the run exits with status 101 (`exitPanicked`). A panic on
  another goroutine still ends the process with Go's status 2. Go has no panic hook, so the
  recovered value stands in for one. Go's runtime ends an unrecovered panic with status 2, which
  the CLI, like clap, keeps for a command line that does not parse.
  Tests: `TestAPanicEndsTheRunWithStatus101OnceTheHandsetIsClosed` and
  `TestAPanicDuringTheCallHangsUpAndFailsTheCommand`.
- A command whose standard output cannot be written, for example through a pipe to a reader
  that has gone, fails with `error: <the write's error>` and exit status 1. This includes the
  handset commands that print. An `(event)` line that standard error cannot take fails its
  command too. A reader that has gone is a failure, not a bug, so the CLI reports it rather than
  panicking. A write that fails with `EBADF` on Unix, or `ERROR_INVALID_HANDLE` on Windows, to a
  standard stream that the process was not given counts as no failure, and that stream reads as
  the end of input (`badDescriptor` in `processstreams.go`).
  Tests: `TestAHandsetCommandWhoseOutputCannotBeWrittenFails` and
  `TestAnEventThatCannotBeReportedFailsTheCommand`.
- Interruptions: a SIGTERM that the CLI was started with ignored is caught like any other
  signal. With SIGINT ignored at start, an interrupted run ends with status 130
  (`exitInterrupted`), `sigintDeliveryGrace` (1 s) after the interruption, rather than by
  SIGINT. Go's runtime replaces an inherited ignore of SIGTERM with its own handler, so
  `signal.Ignored` cannot report it, and it offers no way to restore the default for an
  ignored SIGINT. On Windows, closing the console, logging off or shutting down starts the
  hang-up, and Windows ends the process a few seconds later. Ctrl-C and Ctrl-Break hang up and
  exit with status 130.
- Styles follow the environment: `NO_COLOR`, `CLICOLOR_FORCE`, `CLICOLOR`, `TERM` (set, and not
  `dumb`) and `CI` decide whether output shows styles (`showsStyles`). satlink does not enable
  ANSI escape sequences on Windows consoles, so a console with none of those variables set gets
  no styles. When a stream shows no styles, `stripStyles` removes the escape sequences that
  anstream's `StripStream` would, under the same parser rules.
  Tests: `TestHelpAndErrorsAreStyledOnTheTerminalTheyGoTo` and
  `TestStrippingKeepsWhatAnstreamKept`.
- A stream is a terminal when `isatty(3)` says so: `TCGETS` on Linux and Android, `TIOCGETA`
  on macOS, FreeBSD and OpenBSD, and `GetConsoleMode` on Windows. MSYS and Cygwin terminals are
  not recognised on Windows. `net tcp`'s check of standard input uses the same test.
  Test: `TestEachStandardStreamIsSaidToBeATerminalOnlyWhenItIsOne`.
- The CLI builds on macOS (with cgo), Linux, Android, FreeBSD, OpenBSD and Windows, the
  platforms `go.bug.st/serial` supports. It does not build on NetBSD, DragonFly, Solaris,
  illumos, AIX or Plan 9, which the serial library lacks, nor on js/wasm or wasip1, which have no
  signals to catch. On 32-bit platforms an SMS index or a baud rate above 2147483647 wraps when
  it is converted to `int`, because `satlink.Handset` and `serial.Open` take `int`.

**mobile (Android and Apple bindings)**

- The binding shape follows gomobile's type rules: no unsigned, list or optional types. Lists
  are `Len` and `Get` types, optional values are `""`, 0 or nil, enums are int constants, and
  `NewX` functions become Java constructors. Unsigned values are signed, and durations and byte
  counts are `int64`. An `int` crosses as a 32-bit C `int` on armeabi-v7a and Apple platforms.
- Errors carry only their message across the boundary. `AsSatlinkError` recovers the kind and
  details. `SatlinkErrorInternal` reports recovered panics, because a panic that crosses the
  gomobile boundary ends the process. Every satnet error is mapped, including TLS truncation.
- `Receive` returns a `Received` (with `Finished` at the end of the stream) or a `Datagram`,
  not a byte array. A stream's `Receive` (TCP and TLS) requires `maxLen` of at least 1, since
  with 0 it could return neither data nor the end of the stream. `UDPConnection.Receive` accepts
  0 and keeps none of the datagram it waits for.
- `LinkSettings` and `TLSSettings` are objects, not value records. Each call that takes one
  reads it once, as the call starts. Their `Allow`, `AddHost` and `Add…` methods are safe from
  any thread. The exported fields that gomobile's setters write directly must not be set while
  another thread hands the object to a call.
- The establish functions take nil-able listener and cancellation parameters.
- In Java and Kotlin, gobind's generated `equals` and `hashCode` compare a class's exported
  fields only. Distinct handsets, links, connections and connectors therefore compare equal.
  Compare these objects by identity (`===` in Kotlin, `==` in Java), and keep them in
  identity-based collections, never in a `HashSet`, as map keys, or in a `StateFlow` without a
  wrapper. In Objective-C and Swift every object compares by identity, since gobind writes no
  `isEqual:`. An exported identity field would give Java a public setter and Swift a settable
  property, so an app could change an object's identity and hash. Binding a Java superclass
  to skip the generated methods needs gobind-only `Java/...` imports, which break `go build` and
  `go test`. So `mobile/doc.go` documents the behaviour (its Equality section), and
  `bindings_test.go` pins it.
- There are no Kotlin or JVM desktop bindings. gomobile targets Android and Apple platforms
  only. `scripts/build-android.sh` and `scripts/build-bindings.sh` build the bindings for
  those platforms.

**Stack (ipstack)**

- ipstack does not reproduce four defects of smoltcp 0.14, documented in `IPSTACK.md`,
  section 9. smoltcp mishandles these segments, either by panicking or by giving the
  application octets the peer never sent. The four are: a SYN's window is read with the scale of
  later segments, so data beyond a large receive buffer is accepted; a SYN that reaches a
  connecting socket before the socket's own, or a SYN|ACK to a socket with no receive buffer,
  makes `poll_at` underflow; a receive window that ends before it starts takes a segment the
  buffer cannot hold; and the FIN of a segment whose data the receive window cut short is taken,
  ending the stream early. ipstack instead cuts a SYN's window back to the receive buffer's free
  space, takes no segment when the window ends before it starts, ignores such a FIN, and keeps
  `PollAt` from panicking on any input. A panic in the stack's poll ends a link in satnet. The
  `poll_at` defect is not reached by satnet or sim, which do not call it. The others can be
  reached by a hostile peer.

## 4. Implementation notes per package

### satlink (core)

- **Handset internals.** One `sync.Mutex` serialises exchanges (the line buffer, the URC
  queue, transport I/O). A separate claim makes the handset busy. `DialData` and
  `AnswerData` set it with a compare-and-swap before taking the mutex, and it is cleared
  when the call ends. Public methods check the claim first and fail at once with
  `ErrBusy`. They then take the mutex and check again, because a dial may have started in
  the meantime. The lock and the claim are managed in `busy.go` (`exchange`, `perform`,
  `lock`, `claim`, `release`).

  A dial takes its claim before the mutex, so methods called while it waits for an
  exchange in progress (`PollURCs`, say) also fail with `ErrBusy`. Its `Cancellation` is
  looked at only once it holds the lock. The docs of `DialData`, `AnswerData`, `claim` and
  `ErrBusy` say so.

  Argument checks that need no handset (`DialData`'s number) run before the claim. Every
  other check, including the escape guard of `EndDataCall` and of `DialData`'s
  preparation, runs once the handset is held, so a busy handset reports `ErrBusy` first.
  A `DataCall` uses the transport without the mutex while it holds the claim, and takes
  the mutex for its hang-up. `SetTimeout` does not wait for a 90-second dial: the timeout
  is an atomic. `Close` does not take the mutex either. It closes the transport once
  (`sync.Once`), which makes a read in progress fail, and it reports a closer's failure as
  an `ErrTransport` `*Error` that wraps it.

- **Transport errors.** One helper wraps them. A `*Error` passes through unchanged, and
  anything else becomes `ErrTransport` with `Err` set. A transport without
  `DTRController` gives `ErrTransport` "this transport has no DTR line".

- **Exact texts.** These are the messages callers see, grouped by kind.
  - Argument checks, all made before anything is sent, are `ErrInvalidArgument`:
    `%q is not a dialable number`, "SMS text must not contain Ctrl-Z or ESC", "command must
    not contain CR or LF", `%d is not an SMS filter`, and "an escape guard of %s is longer
    than %s".
  - `ErrCall`: "unexpected CONNECT after %s: the handset is online", "ATA answered a voice
    call, not a data call", "the handset keeps sending data: it is still online", and
    "ATA answered a data call, which was ended again".
  - `ErrCallMayStillBeUp` (each error text is written "the call may still be up: …"):
    "the handset does not answer (%v), and ending a data call it may be in failed: %v",
    "ATH failed on a data call left up: %v", and "ATA answered a data call that could not
    be ended: %v".
  - `ErrCancelled`: "the call setup was cancelled".
  - `ErrTimeout` for the commands "AT", "ATH" and "+++" (see below), and for AT&D2 and
    AT+CBST.
  - `ErrParse`: "%s returned no data", "no %s line in %s", "%s: bad field %d in %s", and
    "no +CMGR header in %s".

  The lists in the `ErrParse` texts are written by `debugList`: "[", each item quoted by
  `debugtext.Quote`, the items joined by ", ", then "]".

  The escape guard text writes its durations with `debugDuration`, not `time.Duration.String`,
  so that they keep one form. `debugDuration` writes a duration of a second or more as whole
  seconds, then up to nine fractional digits without trailing zeros, then "s" ("10s",
  "10.5s", "90s", and "9223372036.854775807s" for `math.MaxInt64`). It writes zero as "0ns",
  where `Duration.String` writes "0s". `Duration.String` would also write 90 seconds as
  "1m30s".

- **Lines.** The line buffer decodes each line's bytes as `internal/utf8lossy` does (one
  U+FFFD per maximal subpart of an ill-formed sequence), then trims CR, LF and NUL. The echo
  is compared with the command ignoring the case of ASCII letters only
  (`asciicase.EqualFold`). `mayGoOnline` and `responsePrefix` also fold ASCII letters only:
  Unicode folding would take "ſ" for "s".

- **Numeric fields.** One helper parses them all with
  `strconv.ParseUint(strings.TrimPrefix(field, "+"), 10, bits)`. It uses 8 bits for the
  +CSQ and +CBC (status and level) fields and for the +CREG stat, and 32 bits for the +CMGL
  index and the +CMGS reference. A leading '+' is accepted. A '-', any other sign, and an
  out-of-range value are refused, and any failure is ErrParse "%s: bad field %d in %s".
  `strconv.Atoi` would accept "-1" and "300", and `ParseUint` alone would refuse "+4". On
  32-bit platforms a 32-bit field above `math.MaxInt` is refused the same way, since an
  `int` cannot hold it. `at_test.go` covers "+4", "-1" and "300".

- **Deadlines** (`deadline.go`). `deadlineAfter(timeout)` returns `time.Now().Add(timeout)`.
  For `math.MaxInt64` that is about 292 years ahead, which is in effect no deadline.
  `timeToWait(deadline)` returns the time left, at most `longestRead` (one second), and
  false once the deadline has passed. Every loop that waits on the transport uses them:
  command execution, the wait for a final result (the abort grace), `PollURCs`, and the
  escape's `discardUntil` and `awaitOwnResult`.

  `hangUpTimeout()` is the handset's timeout, capped at `maxHangUpTimeout` (ten seconds).
  What follows an unanswered ATH (dropping DTR, or reporting that the call may still be up)
  must not be put off for good, even when the timeout is `math.MaxInt64`. The cap applies to
  `HangUp`'s ATH (and `leaveDataMode`'s), to `hangUpWhileMaybeOnline`, and to `prepare`'s
  AT&D2 and AT+CBST, whose timeout is what ends a call left up.
  `TestAHangUpWaitsForTheCommandTimeoutButAtMostTenSeconds` checks the cap's value against
  a literal. The integration tests that reach each capped wait without a command deadline
  bound it only by 30 s (`unendingWait`).

- **The handset's own answers** (`awaitOwnResult`). It reads, sending nothing, until a
  `resultCodeWatch` sees "\r\nOK\r" or "\r\nNO CARRIER\r". Call data never holds these,
  because PPP escapes CR. It reports whether one arrived by the deadline. It confirms the
  escape, the raw ATH sent while the handset may still be online, and `synchronize`. Two
  tests pin that NO CARRIER confirms the escape and the ATH sent after an unconfirmed
  escape: `TestNoCarrierConfirmsTheEscape` and
  `TestNoCarrierConfirmsTheATHAfterAnUnconfirmedEscape`.

- **Synchronising** (`synchronize`). The handset gets "AT\r" written raw. It is confirmed
  by `awaitOwnResult` within two seconds (`syncTimeout`), three times, and then the call
  fails with ErrTimeout "AT". The confirmation is not a plain read because a call left up
  relays the far end, whose data may hold an `OK` line. `Initialize`'s first drain ends
  after 300 ms of quiet (`initialQuiet`), and gives up after `resyncLimit` (two seconds),
  even while data keeps coming.

- **Resynchronising** (`resynchronize`). It reads until 200 ms of quiet (`resyncQuiet`),
  keeping URCs. Still receiving after `resyncLimit` means a call's data is arriving. The
  line buffer is then cleared, and the call fails with ErrCall "the handset keeps sending
  data: it is still online". It runs before a dial (`prepare`), before `Answer`'s ATA, in
  `isInVoiceCall`, and after the DTR drop.

- **Stale input.** Some steps keep stale input from deciding a call. Scripted tests pin
  them, and no other test covers them.
  - The line buffer is cleared once resynchronising has kept the URCs, and when it gives up
    on a handset that is still online (`TestAPartialAnswerLeftBeforeAnswerDoesNotPassForATAs`,
    `TestAPartialAnswerLeftBeforeADialDoesNotDecideIt`,
    `TestTheDataOfACallLeftUpAreNoPartOfTheNextAnswer`).
  - `isInVoiceCall` resynchronises before its AT (`TestAVoiceCallWhoseAbortWasAnsweredLateIsKept`).
  - The resynchronisation after the DTR drop discards the handset's answer to the drop
    (`TestTheAnswerToADTRDropDoesNotPassForATHs`). Two seconds of data after the drop end the
    hang-up before any ATH (`TestAHandsetStillRelayingDataAfterDTRDropsIsReportedPromptly`).
    The simulator's `dropDTR` answers nothing to a DTR drop, so these need scripted tests.
  - The abort's wait only queues URCs (`TestURCsDuringTheAbortAreQueuedAndOtherLinesDropped`).
  - `resyncLimit`'s lower bound: a late listing that keeps coming for a second is not a call
    left up, for both the resynchronisation and `Initialize`'s first drain
    (`TestALateAnswerThatKeepsComingIsNoCallLeftUp`,
    `TestALateAnswerThatKeepsComingDoesNotHideAHeldCall`).
  - The quiets the handset waits for, against a late answer inside them
    (`TestALateAnswerArrivingDuringInitializeDoesNotHideAHeldCall`,
    `TestALateAnswerArrivingDuringAnswerDoesNotPassForATAs`,
    `TestALateAnswerArrivingWhileADialIsPreparedDoesNotDecideIt`).
  - An escape and an abort confirmed late (`TestAnEscapeConfirmedLateButWithinThreeGuardsIsNotRepeated`,
    `TestAnAbortsResultArrivingSecondsLaterStillEndsTheAttempt`).
  - The settings' escape guard, for `DataCall.HangUp` and for an abandoned attempt
    (`TestTheEscapeKeepsTheSettingsGuardOfSilence`,
    `TestACallThatConnectsAfterTheTimeoutIsEndedWithTheSettingsGuard`). The quiet is counted
    from CONNECT (`TestTheEscapeKeepsAGuardOfSilenceAfterConnect`) and from the call's last
    write (`TestTheEscapeCountsItsGuardOfSilenceFromTheLastWrite`).
  - `PollURCs` returns as soon as a URC arrives (`TestPollURCsReturnsAsSoonAsOneArrives`).
  - `Initialize` ignores a refused AT+CMEE=2 or AT+CMGF=1
    (`TestFirmwareRefusingTheOptionalSettingsIsStillInitialised`), but not a refused ATE0 or
    ATV1 (`TestFirmwareRefusingARequiredSettingIsNotInitialised`).
  - A cancellation is looked at every fifth of a second
    (`TestADialLooksAtItsCancellationEveryFifthOfASecond`).
  - Reads last at most a second (`TestACommandWithoutADeadlineReadsASecondAtATime`).

  These tests set lower bounds by design: `resyncQuiet` above 150 ms, `initialQuiet` above
  250 ms, the escape's confirmation window above 2.5 guards, the silence before `+++` above a
  guard after the last write, `syncTimeout` above 1 s, `abortGrace` above 3 s, and
  `cancellationCheck` at most 200 ms. A machine stalled longer than the wait under test could
  let a shortened constant go unnoticed, but it can never fail the real code.

- **Initialize** (`synchronize.go`). ESC and CR are sent, then the first drain, then
  `synchronizeEndingACallLeftUp`. A handset that does not answer may be relaying a data call
  that a previous session left up. `endDataCall(DefaultEscapeGuard)` ends such a call before
  synchronising once more. A handset that still does not answer after a failed attempt to end
  a call gets ErrCallMayStillBeUp "the handset does not answer (…), and ending a data call it
  may be in failed: …". Otherwise the error of the second synchronisation is returned.

  Then `endCallHeldInCommandMode`. A handset that answers may still hold a call in
  online-command mode, escaped from but never hung up. `isInDataCall` reveals it (`AT+CPAS`
  reports 4, `dataCallInProgress`), and the call is hung up with `hangUp`. Its failure is
  ErrCallMayStillBeUp "ATH failed on a data call left up: …". Firmware without +CPAS cannot
  tell (any error, or any other state, means "no data call"), and such a call is left as it
  is. Then `configure` sends ATE0, ATV1, and the optional AT+CMEE=2 and AT+CMGF=1.

- **Data calls** (`datacall.go`). `resultCodeWatch` reads result codes that are split across
  reads, and counts only bare-CR codes. `prepareAndStart` runs the setup. `prepare` first
  refuses an escape guard over `MaxEscapeGuard`, before anything is sent. It then
  resynchronises, and sends AT&D2 and AT+CBST, each within `hangUpTimeout()`.
  `preparationFailed` maps an ErrCall or an ErrTimeout (a call's result, or data without
  end, or silence where an answer was due) to `perhapsConnected`, because the handset may
  already be in a call that must be ended. Anything else maps to `failed`. A cancellation
  that is already set fails the setup with ErrCancelled, and nothing is dialled.

  `dialData` waits for CONNECT, and an early OK does not end the wait. `answerData` waits for
  the final result. Both send their command with `callRequest`, which carries the connect
  timeout, the completion and the cancellation.

  A wait that stopped before its final result (`isUnfinished`: ErrTimeout, ErrCancelled or
  ErrTransport) goes to `abortAttempt`, which runs `abortCallAttempt(untilConnect)`. That sends
  the abort byte and waits ten seconds (`abortGrace`) for a final result, ignoring OK. An OK
  may still be unread when the transport failed: a dial's early OK, or a voice call's OK. Only
  a final result other than CONNECT proves that the attempt ended, and then the outcome is
  that error with no call. A CONNECT, no final result, or a failing abort gives
  `perhapsConnected`. `endAbandonedCall` then ends the call with a fresh escape, quiet since
  now. On success the original error is returned without `CallMayStillBeUp`. On failure the
  hang-up's error is returned, with it.

  `AnswerData`'s OK means a voice call. `hangUpVoiceCall` sends ATH and fails with ErrCall
  "ATA answered a voice call, not a data call". If ATH fails, the call is still up. It is
  `perhapsConnected` with ATH's error, and is ended as a data call is.

- **The cancellable wait** (`exchange.go`). While a request has a cancellation, its wait
  reads at most `cancellationCheck` (200 ms) at a time. `cancellableWait` fails it with
  ErrCancelled once the request is cancelled and `abortIgnoredFor` (200 ms) has passed since
  the command was written (`commandRun.sentAt`). A handset may ignore what it receives in the
  first 125 ms after a command (V.250 §5.6.1), and the abort that follows must not be lost.
  A request without a cancellation, which is every command except a data call's dial and
  answer, reads for all the time left. The tests cancel dials, and
  `TestACancelledAnswerIsAborted` pins `AnswerData`'s cancellation.

- **Hanging up** (`hangup.go`). An unexported escape value (guard, quiet since, attempts)
  carries the escape's parameters. There are two attempts, but only one after NO CARRIER.
  `endCall` is `leaveDataMode`, or else `endCallByDroppingDTR`. When both fail it reports the
  first one's error.
  - `escapeToCommandMode`: each attempt discards input until a guard has passed since the last
    write, writes "+++" in one `WriteAll`, and waits for three guards in `awaitOwnResult`. The
    fault-injecting transport in `internal/simtest` (`faulty_test.go`) swallows exactly that
    write. Once the escape is confirmed, input is discarded until a guard has passed since the
    "+++". An early OK may come from a far end that ignores the ACCM, and a byte sent by us
    inside the guard would still cancel the escape.
  - `leaveDataMode`: escape, discard partial input, then, if the escape was confirmed, `hangUp`.
    `HangUp`'s own ATH counts NO CARRIER as success, since the call had just ended. Otherwise
    it runs `hangUpWhileMaybeOnline`, which writes "ATH\r" raw and confirms it only by
    `awaitOwnResult` within `hangUpTimeout()` (ErrTimeout "ATH"). Its failure is reported as
    ErrTimeout "+++".
  - `endCallByDroppingDTR`: `DropDTR`, then 150 ms of `time.Sleep` (`dtrDrop`, the only sleep in
    the package), then `RaiseDTR`, then resynchronise. Data still flowing shows the drop changed
    nothing. What the handset says in response to the drop (NO CARRIER, or OK after AT&D1) is
    discarded too, so that it cannot confirm the raw ATH. Then `hangUpWhileMaybeOnline`.

  `CallMayStillBeUp` is set as section 2 (Ownership) lists. The claim is released on every path
  out of `HangUp` and out of a failed dial, using `defer`. A caller may recover a transport's
  panic during the hang-up, and the handset must not stay busy after one. Before the release, a
  deferred `pulseDTRUnless(&returned)` pulses DTR when `endCall` (in `HangUp`), or the setup's
  `connect` (in `setUpCall`), did not return. That is the case when a transport's panic unwinds
  through it. `pulseDTR` drops DTR for `dtrDrop` and raises it again, as
  `endCallByDroppingDTR` does. After AT&D2 the call therefore ends as it does when a transport
  is dropped (section 3).

  The pulse is best effort. An error is dropped, since there is no caller to report it to. So
  is a second panic, which its own deferred `recover` takes. The panic being unwound is beyond
  that `recover`'s reach, and it carries on with its value and stack. The pulse runs under the
  lock, before the release, so that no other caller's command reaches the handset while DTR is
  down (`TestADialThatPanicsPulsesDTRBeforeReleasingTheHandset`,
  `TestAHangUpThatPanicsPulsesDTRBeforeReleasingTheHandset`).

- **Answer** (`voicecall.go`). `Answer` resynchronises first, since a late answer to a command
  that timed out would pass for ATA's. A CONNECT means a data call was answered by mistake.
  Nothing else would end that call. `endAnsweredDataCall` ends it as `EndDataCall(DefaultEscapeGuard)`
  does, through the unexported path, which involves no claim. It then fails with ErrCall "ATA
  answered a data call, which was ended again". If ending the call failed, it fails with
  ErrCallMayStillBeUp "ATA answered a data call that could not be ended: " and the hang-up's
  error.

  An ErrTimeout or ErrTransport means the handset may still be answering, perhaps with a data
  call that would come up unseen. `abortAnswer` then aborts with `abortCallAttempt(untilFinalResult)`.
  An OK keeps the voice call. CONNECT ends the data call (`endAnsweredDataCall`, with ATA's
  error). Any other result returns ATA's error. With no result, or with a failing abort, nothing
  says which state the handset is in, because ATA's OK may have been lost. `isInVoiceCall`
  (`callstate.go`) then resynchronises and synchronises, since a handset that answers commands
  is not online. A voice call is one where `+CPAS` is not 4 and `+CLCC` lists a call in state
  "000" (`activeCall`; state 006 is idle). If `isInVoiceCall` reports a voice call, the call is
  kept. Otherwise it is ended as a data call.

- **HangUp** (`handset.go`). ATH is sent within `hangUpTimeout()`. An OK means no call is up,
  and NO CARRIER means the call had just ended. Both mean that no call is left.

- `NewHandset` panics on a nil transport.

### ppp

- **Internal design.** The packet types are `packet`, `configOption` and `outbox`, and `code`,
  whose `String` gives the packet names used in "the peer does not understand %s". A generic
  `automaton[N negotiator]` drives the negotiation. The LCP policy is `lcpNegotiator`, and
  IPCP's `ipcpNegotiator` interface is implemented by `clientIPCP` and `serverIPCP`. The
  authentication exchanges sit behind the `authExchange` interface (`authenticatee`,
  `authenticator`). The HDLC types are `frameEncoding`, `frameDecoder` and `frame`. The
  encoder and decoder have these names so that the `Endpoint`'s `encoding` and `decoder`
  fields do not share names with their types. The FCS table is computed once
  (`var fcsTable = buildFCSTable()`). A Configure-Nak's options go through `suggestOnce`
  (one per kind, section 3), and the automaton's failures go through `fail(reason)`.

- **Test helpers.** `support_test.go` holds every helper that more than one test file uses.
  These include the `String` methods of internal kinds, transitions, outcomes, verdicts and
  packets, which only test failures print. `controlPackets` reads frames as a peer that asked
  for ACCM 0 (`framesSent`), so that negotiated frames parse. `answerOf` makes a peer's
  Configure-Ack, -Nak or -Reject that repeats the request's options, and `nakOf` builds on it.
  `assertDeadUntilOpenedAgain` and `withoutFramesOf` (which drops one protocol's frames) are
  also there. `testLink.advanceAsTheOwnersDo` runs timers only once `NextTimeout` is due, as
  satnet's driver and sim's gateway do.

- **Wire numbers against literals.** The tests check numbers on the wire against literals, not
  against the package's own constants. Both ends of every other test share those constants,
  and sim's gateway is itself a ppp endpoint. `TestAcknowledgesATypicalPPPDConfigureRequest`
  writes the literal option types 1, 2, 3, 5, 7 and 8. PAP and CHAP conversations are compared
  byte for byte with the codes of RFC 1334 and RFC 1994. The code of every LCP packet is
  checked against the numbering of RFC 1661. The client's first IPCP request is compared with
  a reference vector. The option lists of a Configure-Nak and of a Configure-Reject are
  compared with literals. RFC 1661 §5.3–5.4 forbids reordering those options, and pppd's
  `lcp_nakci` and `lcp_rejci` discard an answer whose options are out of order. Neither sim's
  gateway nor a test between two satlink endpoints would notice this, because `applyNak` and
  `applyReject` accept any order. `TestARetransmittedRequestAfterOpeningIsAnsweredWithoutRenegotiating`
  uses the literal 3 for the repeat count, which is the value of `maxRepeatedRequests`.

  A reference frame pins the default-ACCM boundary: 0x1F is escaped as 7D 3F, and 0x20 is sent
  raw, giving `7E FF 7D 23 7D 20 21 7D 3F 20 D5 79 7E`. It was computed by an independent
  reference script, like the other vectors. The encoder and decoder share `isMapped`, so a
  round trip alone cannot catch that boundary.

- **Roles and configuration.** `Role` is opaque. The endpoint reads `role.server == nil` to tell
  a client. `ServerRole` copies the `ServerConfig`, including its DNS server list and its
  authentication. `IPv4()` and `Event.IPv4` return copies, with `DNSServers` cloned.
  `TakeOutput` returns nil when there is nothing to take.

  Validation checks MRU, timers, keepalive and credentials, in that order. It also rejects
  negative counts and durations, which Go's integer types can express (section 2). For a
  server, the further checks of section 5 follow. Every address (`LocalAddress`,
  `PeerAddress`, and each DNS server) must be IPv4 (`Is4()`). The zero `Addr` and IPv6 are
  refused, because `As4` would panic on them, and the error is `ErrInvalidAddress`. The
  authentication protocol is checked after that, giving `ErrInvalidAuthProtocol`.

- **Redaction.** Six types implement `fmt.Formatter`. `Credentials`, `Role`, `ServerConfig`
  and `Config` have value receivers. `Authentication` has a pointer receiver. `Endpoint` also
  implements it, as described below.

  For the four value-receiver types, every verb that fmt passes to `Format` prints the
  redacted `String`. `GoString` serves `%#v`. For `%+v`, a type whose fields are its API
  (through the `fieldNamer` interface) prints those fields with their names, as fmt prints a
  struct. The opaque `Role` prints `String` instead. Without this `Format`, `%d` or `%t`
  would print the fields, password included.

  `*Authentication` has a pointer-receiver `Format`. It prints as fmt prints a pointer to a
  struct: "&", then the struct with the same verb and flags, or `<nil>`, or
  `(*ppp.Authentication)(nil)` for `%#v`. Its texts, and those of sim that print it, stay the
  same. An `Authentication` held in a slice, map or struct no longer leaks under `%s`, `%q` or
  `%t`, where fmt's report of a bad verb would print the pointer's target with method calls
  disabled. This is what protects a held pointer.

  `ServerConfig.Format`, `Config.Format` and sim's `Config.Format` print their `String`, their
  `GoString`, or, for `%+v`, their fields with names. Each of these formats the pointer with
  `%v`, `%+v` or `%#v`. `Config.Format` is needed as well: a `*Config` held in a slice, map or struct and
  printed with a verb that does not fit a pointer would otherwise print its role's unexported
  credentials.

  `Endpoint` implements `fmt.Formatter` too. Its `String`, `GoString` and `Format` take the
  receiver by value, which is the one deliberate exception to uniform receivers. A copy, which
  must not be made, or an endpoint held by value in an exported field, therefore prints its
  role and phase ("client endpoint in phase Open"), never its fields. A value receiver cannot
  tell a copy from the pointer, so `GoString` always writes the pointer form,
  `&ppp.Endpoint{role:…, phase:ppp.PhaseOpen}`, as `NewEndpoint` returns it.

  fmt hands `%p` and `%w` to no method. It also calls no method while it reports a verb that
  does not fit its operand (`%!verb(type=value)`), nor on what it reaches through an unexported
  field of the caller's own types. So `%p` of a value that is not a pointer, and `%w`, print the
  password (section 3). The `Credentials` doc lists these cases, and the `Role`, `Config` and
  `Endpoint` docs refer to it. `ServerConfig`, the server `Role` and the server `Config` keep
  their credentials behind pointers, and stay redacted under every verb.

  `go vet` reports every misplaced `%w`, and `%p` of an `Authentication` value. It does not
  report `%p` of `Credentials`, a `Role`, a `Config` or an `Endpoint`, because their `Format`
  methods make vet assume that they accept every verb. The redaction tests are
  `TestRolesAndCredentialsPrintRedacted`, `TestServerConfigsPrintTheirCredentialsRedacted` and
  `TestConfigsPrintTheirFieldsWithCredentialsRedacted` in `config_test.go`. They run a table of
  verbs (`%v %+v %#v %s %q %x %X %d %t`) over values that hold credentials, both directly and
  behind pointers, in slices, maps and structs, and over copies of endpoints. `%p` is tried
  too, for the values that keep their credentials only behind pointers. The password must
  never appear.

- **Random values and readable messages.** Magic numbers and CHAP challenges come from
  `crypto/rand`. The authenticator name is `satlink-ppp` (`authenticatorName`). A peer's PAP and
  CHAP messages are made readable by `readable`: `internal/utf8lossy` decoding, control
  characters dropped, and white space trimmed.

- **Decoder capacity.** The decoder's capacity is the larger of the configured MRU and
  `DefaultMRU`, plus the header and FCS, so that a frame of up to 1500 bytes fits whatever the
  configured MRU is.

- **Timer arithmetic** is in `deadline.go`: `isDue`, `earliest`, and `scaled`, which saturates
  (section 3).

### satnet

- **Engine.** A `sync.Mutex` and a broadcast channel are swapped atomically, so that `notify`
  may run with the engine locked or unlocked. Every state change is still made under the lock,
  so no wake-up is lost. Every blocking operation goes through `waitUntil(terms, attempt)`.
  Its terms are one struct: the operation's name, a deadline function that is looked up again
  whenever the wait wakes, the context (or nil), and the connection's closed flag. `waitUntil`
  checks the closed flag first (`net.ErrClosed`), then the attempt, then whether the link is
  still alive, then the deadline. Data already received therefore beats an expired deadline.
  A `Watcher`'s waits use a sibling, `waitFor`, which skips the alive check.

  "Alive" means usable (negotiating or up), or draining after a requested close (`Closing` with
  `CloseReasonRequested`). So reads and waits under way carry on while written data is
  delivered. Starting anything new (a lookup, a connection, a TCP write, a UDP send) still
  needs the link to be usable (`checkUsable`). The hang-up aborts every socket, which ends
  those waits.

  `Close` sets the closed flag under the engine lock and notifies the engine, which wakes every
  call waiting on that connection. Deadlines are `time.Time`, with the zero value meaning none.
  Changing a connection's deadline notifies the engine, so that a waiting call re-evaluates.
  `DialContext` also waits on `ctx.Done()`. `linkUp` restarts the idle clock only while
  negotiating, since renegotiating later is not traffic. `isNegotiating` reports that PPP has
  yet to come up for the first time. The engine counts its waiting calls in the unexported
  `waiting` field. Tests read it through `waitingCalls` in `export_test.go`.

- **Configuration** (`config.go`). `Config.Validate` is exported, so that an app (the CLI, before
  it announces a dial) can check sooner. `Establish` calls it before dialling, on a
  configuration whose `Hosts` it has cloned first, so that a caller who changes its map during
  the dial changes nothing and races nothing.

  Validation checks, in order: the MTU, the link rate, the TCP buffers, the host names (and, in
  Go, the IPv4 host addresses), and the durations. Durations must not be negative and must be at
  most a year (`maxDuration`). Then comes the escape guard, which must be at most 10 s
  ("the escape guard must be at most 10s", using `satlink.MaxEscapeGuard` with `%v`). Last are
  the limits, which refuse values too small to be of use:
  - a call time limit that leaves time to bring PPP up (`linkSetupTime`, 20 s: 7 to 10 s of
    round trips for LCP, authentication and IPCP, with room to use the link afterwards) and to
    hang up. The message is "the call time limit must leave time to bring PPP up and to hang
    up: at least %v", which is 24 s with the default escape guard;
  - an idle limit of at least `minIdleTimeout`, which is `longestLookup` (10 s for each of
    `dns.MaxServerCount` servers) plus 5 s. The idle clock counts only what the app does, not
    what it waits for. The message is "the idle limit must be at least 25s, or the link hangs
    up while waiting for an answer";
  - a data limit of at least `minMaxBytes` (2048): "the data limit must be at least 2048 bytes,
    as PPP alone takes up to 500".

  Credentials that PPP refuses (a name or password over 255 bytes) are also reported by
  `Establish` as `ErrInvalidConfig`. The error carries ppp's `*ConfigError` in `Err`, with an
  empty `Message`. Its text is "invalid configuration: the username is longer than 255 bytes",
  and `errors.Is(err, ppp.ErrCredentialTooLong)` holds (`TestPPPRefusesWhatItCannotCarry`). The
  docs of `ErrInvalidConfig` and `Error.Err` say so, and mobile's error mapping reads both fields.

  The hang-up time is `Config.hangUpTime()`: twice the escape guard plus `athAllowance` (one
  second). The guard is at most ten seconds and not negative by then, so the hang-up time is
  between 1 s and 21 s. The shortest call is therefore between 21 s and 41 s.

- **Driver goroutine** (`driver.go`). The driver polls every 20 ms (`pollInterval`) and reads in
  1024-byte chunks (`readChunk`). It counts 8 bytes of framing overhead per datagram
  (`framingOverhead`). It uses the hang-up time from the configuration and the receive ACCM
  `1<<'\r' | 1<<XON | 1<<XOFF` (`receiveACCM`). It checks the limits in order, and on a
  requested close it drains within `CloseGrace`, or not at all while still negotiating. It sends
  no PPP goodbye before hanging up.

  A panic in the run is recovered into `CloseReasonInternal`, and the call is still hung up. The
  panic value may be a string, an error, or a value whose `Error` method panics; `panicMessage`
  recovers all of these. The driver hangs up first, before even a log line of its own, because a
  log handler can fail too. It then logs at info "link closed: …", or, if the hang-up failed, at
  warn "link closed (…), but the hang-up was not confirmed, so the call may still be up: …". It
  then records `CallMayStillBeUp`, the final usage, and signals completion. `Establish` waits for
  that completion when the link fails to come up, so the handset is released when `Establish`
  returns. It sets `CallMayStillBeUp` on its error when the status says so.

  The configuration's `Call.Cancellation` (nil-safe) also stops a link that is still negotiating,
  with `CloseReasonRequested` (`setupCancelled`). Nothing written could still need delivering.
  When the cancellation is set and the link did not come up, `Establish`'s `setupFailure` reports
  `ErrHandset` wrapping `&satlink.Error{Kind: satlink.ErrCancelled}`. That is the same error as a
  dial cancelled before CONNECT. It keeps `CallMayStillBeUp` from the hang-up.

  A panic can also get past the run. It may come from the connections aborted as the hang-up
  starts (a log handler can cause one through ipstack's TCP state traces), or from the hang-up
  itself, which means a transport bug. The goroutine's outermost `defer` recovers such a panic.
  - First, `endCallLeftUp` hangs up a call that the panic kept from being hung up. A second
    `HangUp` of a call already hung up only says "the data call is over". A hang-up that itself
    panicked has had DTR pulsed by the core (section 3). This runs under a `recover` of its own.
  - Then the usage up to that point is recorded, so that `Usage.ConnectedFor` counts the hang-up
    that `endCallLeftUp` ran.
  - Then the engine is abandoned. It is set to `Closed`, with the reason it was closing for, or
    to `CloseReasonInternal` with the panic's message if it was not closing yet. `CallMayStillBeUp`
    is set and every socket is aborted (`engine.go`). This also runs under a `recover` of its own.
  - Then it notifies, and completes with `&Error{Kind: ErrLinkDown, Reason: Internal "the link's
    driver thread panicked", CallMayStillBeUp: true}`.

  It never panics again, since that would end the process. It hangs up before it abandons the
  engine, so that a link reported closed has ended its call and released the handset. That is
  what `Watcher.WaitClosed` promises, and what mobile's port closer and reclaim rely on.
  `TestAPanicWhileTheHangUpAbortsTheConnectionsStillHangsUp` and
  `TestTheFirstOfTwoPanicsIsWhyTheLinkClosed` check this as `WaitClosed` returns.
  `engine.beginClosing` sets `Closing` before it aborts the connections. `abandon` sets `Closed`
  first, so a link whose aborts panic still closes for the reason it was closing for (section 3).

  The other log texts, written with `logInfo`, `logWarn` and `logDebug`, are: "IPv4 up: " followed
  by the settings, formatted with `internal/debugtext`; "datagram dropped: …"; and "dropped an
  early repeat of Dns { source_port: …, destination: …, message_len: … }" (see Pacing).

- **Close.** `Close` is `RequestClose` followed by waiting for completion. The first result is
  kept and returned to every later call. `runtime.AddCleanup` on the `Link`, with the engine
  (never the Link itself) as its argument, makes a collected Link still request the hang-up.
  Collected `TCPConn` and `UDPConn` sockets are closed in the same way. `Watcher` and every
  connection hold the engine and never the `Link`. A `*Link` field would keep a dropped link
  reachable, and its call billed, for as long as the app held a connection or a watcher. Mobile's
  listener holds one until the link closes.

- **Device** over `internal/ipstack` (`device.go`). The stack may hand over a datagram whenever
  it asks. `Transmit` grants a slot every time, and a received datagram's reply slot sorts what
  goes into it, in the same way (`sort`).
  - Urgent datagrams are sent at once, without waiting for the line. These are TCP segments with
    no data and no FIN (acknowledgements, window updates, SYNs, RSTs), and DNS queries from a
    port that is not an app UDP socket's (`setAppUDPPorts`, called before each poll). The app's
    own datagrams to port 53 wait for the line like other data (`isUrgent`).
  - The rest waits in a queue per flow. A flow is identified by protocol, destination address and
    both ports, the last read from the payload's first four octets with `wire.NewIPv4Packet`. A
    datagram without a flow is dropped. The flows take turns, one datagram per line slot
    (`lineReady`), so that a busy connection cannot hold up the others. A resent TCP segment
    replaces its original while that one still waits, if the sequence number, length, SYN and FIN
    are the same (`isSameSegment`).

  `popOutgoing` returns the urgent datagrams first, then the next flow's oldest datagram while the
  line is ready. It also records what each TCP segment tells its peer. Each connection has an
  `observedConnection`, which holds what the peer has acknowledged since our SYN. It advances only
  through `peerAcknowledged`, and only once the stack has taken the data off its send buffer
  after checking the acknowledgement. It never advances from the segment that carried the
  acknowledgement, which may be corrupt, forged or ignored. It also holds what we last told the
  peer, as an acknowledgement and a window.

  A waiting segment is dropped as stale (`wentStale`) when the peer has acknowledged its data
  meanwhile, and when it tells the peer nothing new (`isNews`: no later acknowledgement and no
  other window). Two kinds of segment are kept: one no longer than the one-byte keep-alive probe,
  and one carrying SYN, FIN or RST. A dropped segment is logged at debug as "dropped a
  retransmission acknowledged while it waited". A segment that is sent instead is brought up to
  date with the newest acknowledgement and window we sent, and its checksum is filled in again
  (`bringUpToDate`). This matters because some stacks drop a segment, data and all, whose
  acknowledgement is older than one they have already seen. `clear` also forgets the connections.

- **Stack** over `internal/ipstack`, in `stack.go`, `stackconnections.go` and `lookup.go`.
  `nextOutgoing` drops ICMP datagrams. The interface's port- and protocol-unreachable errors
  quote up to 548 bytes of what arrived, and they may be addressed outside the policy
  (`stack.go`). The stack sets these values:
  - ACK delay of 100 ms (`ackDelay`), and Nagle off;
  - the configured timeout and keepalive, with no keepalive for sockets the app has closed;
  - four UDP slots (`udpSlots`);
  - ephemeral ports from 49152 (`firstEphemeralPort`), starting at a random point;
  - a single /32 address, and DNS servers from IPCP, of which it keeps `dns.MaxServerCount`.

  Every TCP socket has an outbox, from `openTCP` until `removeTCP`. The outbox holds what the app
  has written and the stack has yet to take (`sendTCP`). The send buffer bounds the outbox and the
  unacknowledged data together (`tcpUnacknowledged`). A connection the app has finished
  (`shutdownTCP`) takes no more (`tcpMaySend`). Each poll first feeds the connections whose flow
  does not wait for the line, one segment's worth at a time (the MTU less 40 bytes of headers).
  It closes our half of a finished connection once its outbox is empty, so that the FIN follows
  everything written (`feedConnections`). The stack sends what it holds as soon as it may, and it
  may whenever it asks. After the poll, the device hears what each peer acknowledged during it,
  from the send queues before and after. A reset empties a send queue too, but it forgets the
  endpoints, and it counts for nothing (`passOnAcknowledgements`).

  `configure` records the connections with undelivered data in the outbox or the send buffer,
  which are cut off when the local address changes (`cutOffUndelivered`). The stack resets every
  connection on the old address without a word, and empties its send buffer as if the peer had
  acknowledged everything. The next poll may first hand it what waits in the outbox. So
  `WaitAcknowledged` reports a cut-off connection as `ErrConnectionReset`. `cutOff` holds the
  connections cut off by an address change, a reset or the hang-up, each recorded once
  (`recordCutOff`).

  `resetTCP` resets a connection and drops its outbox, but keeps the socket, so that its handle
  stays valid (`TCPConn.Abort`). `abortTCP` resets one that the app has let go of. `abortAll`,
  as the hang-up starts, aborts every socket and drops every outbox. Both drop an outbox through
  `dropOutbox`. That records a connection whose outbox held data as cut off, because an emptied
  outbox shows nothing of what never left. What the stack had taken stays in the socket's send
  buffer through the abort, and needs no record. After an abort, `WaitAcknowledged` fails with
  `ErrConnectionReset`. After the hang-up it fails with `ErrLinkDown` (section 3).

  A closing socket is removed once it is CLOSED and has no peer. An aborted socket forgets its
  peer only once its RST has gone. Removal also drops its outbox and its cut-off entry
  (`removeFinishedSockets`, `removeTCP`). When a `TCPConn` is collected without `Close`, the
  cleanup is `closeTCPGracefully`. A `UDPConn` gets `releaseUDP`, and the stack methods these
  reach are `closeTCP` and `closeUDP`. `sendBacklogs` is what `passOnAcknowledgements` compares.
  `connection.unlessClosed` runs a change unless the connection is closed.

  `UDPConn.LocalAddr` reports the link's address at the time of the call, with the socket's port
  (`localEndpoint`, which `localUDPEndpoint` also uses). The socket is bound to a port only, so
  after PPP renegotiates a new address, its datagrams leave from that new address. TCP
  connections are reset by an address change, so `TCPConn.LocalAddr` keeps the address the
  connection opened from.

  Lookups have an id of their own (`lookupID`). The resolver fails a lookup in the same way
  whether a server said that the name has no address, or none answered in time. `deliver` checks
  each arriving datagram for a DNS server's answer about a name (`parseDNSAnswer`). The answer
  must be a response to a standard query with one A question, NOERROR or NXDOMAIN. It must come
  from port 53 of a kept server, have valid checksums, and not be addressed to an app socket's
  port. The name is read in lower case, and its labels are decoded as `internal/utf8lossy`
  decodes them. A failed lookup is `lookupNoAddress` (`ErrNameNotFound`), and only after such an
  answer about its name. Otherwise it is `lookupUnanswered` (`ErrTimedOut`, "DNS lookup"). That
  covers silence, a server failure, and a server that went away.

- **Pacing.** The repeat key of a DNS query holds the source port, the destination, and the whole
  message (`pacing.go`). A retransmission repeats the message byte for byte, while another message
  to port 53 may share its id and length. The key keeps the message as a string field. That keeps
  the key comparable and exact, at a cost of a few hundred bytes per remembered query.

- **Policy** (`policy.go`). Host names are lower case, have no trailing dot, are at most 253
  bytes long, and have labels of 1 to 63 letters, digits, `-` (not at either end) and `_`. A
  target made only of digits, dots and `/` is an address or a network (`netip.Prefix`, with the
  address kept as written). Port 0 never matches. The zero target kind, `targetNone`, matches
  nothing. `ParseAllowRule` and `ParsePortRange` share one parser for the ports part.

- **DialContext** (`link.go`). When `ctx` is done before the dial completes, it returns
  `&net.OpError{Op: "dial", Net: network, Err: ctx.Err()}`, as `net.Dialer` does. An
  `ErrTimedOut` becomes `&net.OpError{Err: context.DeadlineExceeded}` only once `ctx`'s deadline
  has passed. The operation's own deadline, taken from `ctx`, may fire before the context
  notices. Any other timeout, such as a DNS lookup that no server answered within 10 s, stays
  satnet's `*Error`, as `net.Dialer` keeps a resolver's error. Another network gives
  `net.UnknownNetworkError`, and a malformed address gives `*net.AddrError`.

- **TLS configuration** (`tls.go`). The `tls.Config` has `MinVersion` set to TLS 1.2,
  `CurvePreferences` from `ringprovider.KeyExchangeGroups()`, `CipherSuites` from
  `ringprovider.TLS12CipherSuites()`, a `ClientSessionCache` of
  `caseBlindSessionCache{sessions: tls.NewLRUClientSessionCache(0)}`, `RootCAs`,
  `VerifyPeerCertificate: verifyChainKeys`, and `NextProtos`. The key exchange groups (X25519,
  P-256, P-384) and the TLS 1.2 suites (ECDHE with AES-GCM or ChaCha20-Poly1305) are the ones
  of the ring provider in `internal/ringprovider`. The TLS echo in sim uses the same lists, so
  both ends offer the same set. crypto/tls ignores the order of the curve list and sends a key
  share for the first group in its own fixed order, X25519. P-384 only lengthens the
  ClientHello by 2 bytes. crypto/tls's TLS 1.3 suites, which a client cannot choose, stay as
  crypto/tls sets them. A TLS 1.2 session resumes by ticket only (section 3).

  The session cache keys its LRU cache by the server name in ASCII lower case
  (`asciicase.Lower`), so names that differ only in ASCII case share a session.
  `config.ServerName`, and so the SNI, stays as the caller wrote it. crypto/tls checks a
  resumed session's certificate with `VerifyHostname`, which ignores case.

  `verifyChainKeys` (`tlskeys.go`, section 3) requires one verified chain without a refused key.
  crypto/tls runs it after verifying the chains and before the handshake signature, and only on
  full handshakes. It checks nothing when no chain was verified (`InsecureSkipVerify` on a
  `Config()` copy), and it refuses with `ErrTLS` (section 5). `isRefusedKey` compares the RSA
  minimum of the ring provider with `rsa.PublicKey.Size()*8`, the modulus rounded up to whole
  bytes, and its maximum with `N.BitLen()`.

  The check is `VerifyPeerCertificate` and not `VerifyConnection`, because `VerifyConnection`
  belongs to the pins alone. `cmd/satlink`'s `TestAnHTTPSFetchTrustsThePublicWebPKI` relies on
  `VerifyConnection == nil` meaning no pins. An app that sets its own `VerifyPeerCertificate` on
  a `Config()` copy replaces the check.

  Public roots come from `bundle.Roots()` of `golang.org/x/crypto/x509roots/fallback/bundle`.
  `rootPoolOf` builds the pool once. Where a root has a constraint, it is added with
  `AddCertWithConstraint`, for NSS's distrust-after date (section 3). Pins are checked in
  `VerifyConnection` against the SHA-256 of `PeerCertificates[0].RawSubjectPublicKeyInfo`.

  PEM input counts only `CERTIFICATE` blocks. Its errors are "invalid PEM: …" and "the PEM
  data holds no certificate". With no roots at all, the error is "no root trust anchors were
  provided". `pem.Decode` silently skips a section that does not decode, so `NewTLSConnector`
  compares the number of sections decoded with the number of "-----BEGIN " markers at the start
  of the data or of a line (`sectionStarts`). A marker inside a comment line begins nothing, as
  for `pem.Decode`. A mismatch is reported as `ErrTLS` "invalid PEM: a section is malformed". A
  `CERTIFICATE` block or `ExtraRootsDER` entry whose DER does not parse is `ErrTLS`, carrying
  crypto/x509's error ("TLS: x509: …").

  `DynamicRecordSizingDisabled: true` keeps records at the full 16 KiB of plaintext. Without it,
  crypto/tls would send its first 128 KB in records of about 1.2 KB, each with 22 bytes of TLS 1.3
  overhead.

- **TLS server names** are checked before anything is sent. A name is valid if `netip.ParseAddr`
  accepts it without a zone, or if the policy's host-name normalisation accepts it and its last
  label is not all digits. Anything else is `ErrInvalidHost`. crypto/tls takes any name, and
  fails only at certificate verification, after the airtime of a full handshake has been spent.
  `tls_test.go` covers "bad name" and "1.2.3".

- **TLS connection** (`tlsstream.go`, `tlsrecords.go`). `TLSConn` wraps a `*tls.Conn` over an
  unexported record buffer that sits between crypto/tls and the TCP connection. crypto/tls on its
  own cannot give the semantics this package needs. It makes every write error permanent
  (`halfConn.setErrorLocked`), so a write that timed out would break the session. It also
  processes post-handshake messages, such as TLS 1.3 session tickets, only while reading.

  The buffer is written against a consumer-defined interface, `tlsTransport` (`tlsrecords.go`),
  which `*TCPConn` satisfies. It has a net.Conn's `Close`, addresses and deadline setters, but not
  its `Read` and `Write`, which wait. It also has `CloseWrite`, `Abort`, `WaitAcknowledged` and
  `PeerAddr`, and the unexported primitives of the same package:
  - `trySend(p) (int, error)`, which sends what fits now;
  - `tryReceive(p) (int, bool, error)`, where the bool reports the end of the stream;
  - `checkSendable`, which checks that the link takes new data and that our half is open;
  - `waitWritable`, `waitReadable` and `waitReadableOrWritable`, which wait on the link's traffic
    (operations "TCP write" and "TCP read");
  - `announce(change)`, which makes a change that a wait's `changed` function looks at, under the
    link's lock, and wakes the waits.

  The waits take a deadline function, which is looked up again whenever the wait wakes. So a
  deadline moved while the TLS layer waits applies at once. `readUntil()` and `writeUntil()` are
  the connection's own deadlines. `Secure` keeps its exported `*TCPConn` signature, and calls an
  unexported constructor that takes the interface, so that tests can use a fake.

  1. crypto/tls's writes never wait and never fail, except for a dead TCP connection. The buffer
     appends each record and pushes what TCP takes at once.
  2. Back-pressure comes from `TLSConn.Write`. It hands crypto/tls chunks of at most
     min(16 KiB, the room left under the 64 KiB limit, `sessionBufferLimit`). Once the session is
     full, with 64 KiB of records pending, it sends the pending records until the write deadline,
     before it takes more. So a full session makes a write wait rather than fail. After the last
     chunk, it pushes the pending records until the deadline. At the deadline it returns the count
     crypto/tls took, with `ErrTimedOut` "TCP write". Those records stay pending, and the session
     stays usable.

     `TLSConn.WriteSome` hands crypto/tls only what the session has room for, waiting until the
     write deadline while it has none. It then pushes the records until the deadline. A timeout
     there is no failure: `ErrTimedOut` means that nothing was taken.
  3. Every other call sends pending records first, so that what a timed-out write left goes with
     it. The buffer's `Read`, which crypto/tls calls, pushes before reading. While records remain,
     it waits for readable or writable, not for readable alone. `WaitAcknowledged` pushes until its
     deadline, then waits for TCP's acknowledgement. `CloseWrite` and `Close` push before and after
     the close_notify. Records that TCP no longer takes (our side has shut down, or the link is
     closing) count as gone for a `Read` (`pushBeforeReading`). They must not keep it from what is
     still to be read, and a read reports a connection that has ended.

     A write whose push times out with records left wakes a waiting `Read`, so that the `Read` can
     send them on. This works through the record buffer's change counter, announced under the
     engine lock (`TCPConn.announce`, the waits' `changed` function). The count covers only the
     records a write left unsent. No other call takes records in while a `Read` waits (item 4).
  4. `WaitAcknowledged` and `CloseWrite` then take in whatever TCP already holds, without
     waiting. They do this with a `tls.Conn.Read` on the buffer in non-blocking mode (`tryReceive`).
     Nothing left is a `Temporary` timeout, which crypto/tls survives. This lets crypto/tls process
     the session tickets, so the next connection can resume even after one that never reads.

     Application data that a read returns is kept in `TLSConn`'s read-ahead buffer, which `Read`
     serves first. The take-in stops once 16 KiB of read-ahead data is waiting (`readAheadLimit`).
     So a connection that keeps calling `WaitAcknowledged` without reading cannot grow the buffer
     without bound. The take-in is skipped while another goroutine reads (a `TryLock` of
     `TLSConn`'s read lock). crypto/tls holds its input lock for the whole of a blocking read, and
     that reader takes the tickets in anyway.

     The take-in ignores `errNothingArrived`, the transport's errors, and the end of the stream
     (`isEndOfStream`). The end of the stream is crypto/tls's `io.EOF` after the server's
     close_notify, or after a FIN between records, and `io.ErrUnexpectedEOF` for a FIN inside a
     record. A `Read` or a `Write` reports these instead. So `WaitAcknowledged`, `CloseWrite` and
     `Close` succeed after the server's goodbye or end, and `Close` still sends its own
     close_notify.
  5. The first `ErrTLS` (a failed session: an alert, a bad record, a pin) is kept. Every later
     `Read`, `Write`, `WaitAcknowledged` and `CloseWrite` returns it. Once the session has failed,
     every call reports why.
  6. The buffer records whether the transport reported its end. When crypto/tls returns `io.EOF`
     after the TCP end was seen, the server never sent close_notify, so `ErrTLSTruncated` is
     reported. crypto/tls's `io.ErrUnexpectedEOF` in the middle of a record is truncation too. A
     close_notify makes crypto/tls return `io.EOF` before the TCP end is ever read.
  7. It returns the transport's errors unchanged, never wrapped. crypto/tls keeps a session after a
     read error only when `err.(net.Error)`, a plain type assertion and not `errors.As`, reports
     `Temporary`.
  8. `Write` and `WriteSome` check before each chunk they hand to crypto/tls (`tlsstream.go`), so that
     what could never reach the server is refused rather than taken. Once `CloseWrite` has set
     `goodbyeQueued`, a write fails with `ErrConnectionReset`, as TCP refuses a write after its own
     shutdown. The flag is set before crypto/tls queues the close_notify, so a write goes before
     the goodbye or not at all. Once the TCP connection or the link no longer sends, the error of
     `checkSendable` is returned. Nothing is taken in either case, and no records are left to fail
     every later `WaitAcknowledged`. `writeFailure` reports crypto/tls's own refusals as the
     session's failure. It reports `ErrConnectionReset` when another goroutine's `Read` or
     `CloseWrite` got there first. A `CloseWrite` called again after a timeout finishes the
     shutdown. `Abort` belongs to the TCP connection.
  9. The buffer ignores `SetDeadline`, `SetReadDeadline` and `SetWriteDeadline`. `TLSConn`'s own
     deadline setters set the transport's deadlines directly. crypto/tls calls the buffer's
     setters only in `closeNotify`, where it sets a 5-second write deadline and then the current
     time. The close_notify instead waits for room until the caller's deadline, or forever when
     there is none. A full 4096-byte send buffer takes about 14 s to drain at 300 B/s.

- **TLS handshake.** `Secure` validates the name. It sets the transport's deadlines to now plus the
  timeout, around `tls.Conn.Handshake()`. During that call the buffer's `Read` pushes each flight
  while it waits for the server's. Then `Secure` pushes the pending records, the client's Finished
  among them, until the same deadline. A deadline error fails `Secure`
  (`TestAHandshakeWhoseFinishedCannotLeaveInTimeFails`). The deadlines are then cleared.
  `HandshakeContext` is not used. On cancellation it would close the connection and return the
  context's error, instead of `ErrTimedOut` "TCP read" or "TCP write". A connection closed during
  the handshake is `ErrTLS` "the server closed the connection during the handshake". On any
  failure, the transport is closed.

- **TLS close.** `CloseWrite` takes in what has arrived, reporting a failed session. It then sends
  the close_notify (`tls.Conn.CloseWrite`), pushes the pending records within the write deadline,
  and calls the transport's `CloseWrite`. crypto/tls's own `CloseWrite` does not close the TCP
  half. `Close` first takes in what has arrived, so a connection closed without
  `WaitAcknowledged` still keeps its session tickets. It then calls `tls.Conn.Close`: the
  close_notify goes into the buffer, whose `Close` pushes the pending records within the write
  deadline and then closes the transport gracefully.

  A session that has failed gets no close_notify. Its TCP connection is just closed. When no
  close_notify can be sent at all, because the connection was reset (by `Abort` or by the server)
  or the link is closing, `Close` returns the TCP connection's `ErrConnectionReset` or `ErrLinkDown`
  and still closes everything. `CloseWrite` fails in the same way. `TCPConn.CloseWrite` after
  `Abort` returns nil, since it sends nothing and so cannot fail. A second `Close` returns nil, as
  it does for `TCPConn`. The link's own close drops records that are still pending, so call
  `WaitAcknowledged` first.

- **TLS tests.** `tlsstream_test.go` (internal) runs over the fake of the transport interface in
  `faketransport_test.go`. The fake is an in-memory pair whose far end is an in-process
  `tls.Server`, with a certificate generated in the test by `crypto/x509`. The fake peer closes
  `handshook` after its handshake, and `secureFake` waits for that. Without the wait, the server's
  last handshake read can take in records that the test sent afterwards. With `GOMAXPROCS=1`,
  `TestAFullRecordBufferMakesAWriteWait` failed on every run before this was in place. The tests
  cover the following.
  - Closing: a bare close, or a close in the middle of a record, gives `ErrTLSTruncated`. The
    first also matches `io.ErrUnexpectedEOF`. A close during the handshake gives the exact `ErrTLS`
    text. A close_notify gives `io.EOF`. After `CloseWrite`, the peer reads the close_notify and
    then EOF. Resumption works after a bare `Close`.
  - Timeouts: a read that timed out leaves the session usable. A write that timed out leaves it
    usable, and its records leave with the next call. `WriteSome` takes what the session has room
    for and no more. That is at most one TLS 1.3 record's 22-byte overhead beyond the 64 KiB, which
    `expectSessionFull` checks for `Write` too. It reports how much it took when the connection is
    reset while its records wait, is refused as `Write` is, and takes nothing of an empty slice. A
    `CloseWrite` that timed out finishes when it is called again.
  - Failures: a failed session reports its `ErrTLS` on every later call. A pin mismatch gives
    "invalid peer certificate: the public key is not pinned". crypto/tls's refusals say why.
    Data taken in before the session failed is read first
    (`TestDataTakenInBeforeTheSessionFailedIsReadFirst`). `Close` and `CloseWrite` report a failure
    that they take in. The read-ahead bound holds.
  - The server's end: when the server's end is taken in before `WaitAcknowledged`, nothing fails.
    That covers a close_notify, a FIN after a record, and a FIN inside a record.
    `WaitAcknowledged` returns nil, `Read` returns the data and then `io.EOF` or `ErrTLSTruncated`,
    and `Close` returns nil, sending its own close_notify (`TestTakingInTheServersEndFailsNothing`).
  - Resets: a write after `CloseWrite` is refused with `ErrConnectionReset`, without failing the
    session. A `WaitAcknowledged` that waits, or is called later, reports what an `Abort`
    dropped (`TestWaitAcknowledgedReportsWhatAnAbortDropped`). A `Close` after an `Abort` says the
    connection was reset and lets it go (`TestACloseAfterAnAbortSaysTheConnectionWasResetAndLetsItGo`).
    `CloseWrite` after `Abort` is covered too. Records that TCP no longer takes do not keep a `Read`
    from what arrived. A `CloseWrite` that cannot wait for the goodbye still wakes a waiting read
    (`TestACloseThatCannotWaitForTheGoodbyeStillWakesAWaitingRead`).
  - Waits and deadlines: reads and writes wake when another call takes in or leaves unsent records,
    including a polling read. TLS read and write deadlines moved while a call waits take effect at
    once. No handshake deadlines are left over. `SetDeadline` sets both deadlines. The push and
    acknowledgement timeouts of `WaitAcknowledged` are tested.
  - Handshake and resumption: the handshake returns only once the client's last flight, its
    Finished among it, has left over a TCP connection that takes 16 bytes at a time
    (`TestTheHandshakeIsDoneOnceTheClientsFinishedHasLeft`, over `servePeerOver`). When that flight
    cannot leave before the deadline, `Secure` fails with `ErrTimedOut` "TCP write" and closes the
    transport (`TestAHandshakeWhoseFinishedCannotLeaveInTimeFails`). That test uses a TLS 1.3 server
    stopped in its `VerifyConnection`, after its flight and before it reads the client's
    (`servePeerStoppingAfterItsFlight`), and then moves the fake's write deadline to now. A session
    resumes for its server name in any ASCII case (`TestASessionResumesWhateverTheCaseOfItsServerName`,
    over `secureFakeFor`).

  `tls_integration_test.go` runs over the simulator's unthrottled line at 50 000 B/s
  (`unthrottledRate`). Tests in it cover full sessions, a reader that keeps sending what a timed-out
  write left, and a connection that only sends and still lets the next one resume
  (`TestAConnectionThatOnlySendsStillLetsTheNextResume`). One further test in that file,
  which is slow, runs over the default unthrottled simulator with the default 300 B/s pacing. The
  uplink is what the goodbye waits for. It writes 6000 bytes with a 1 s write deadline, so about
  2 KB of records stay in the session. Then `CloseWrite` with a 30 s deadline must succeed and take
  more than 5 s. `ReadAll` must then return the echo and `io.EOF`, the echo answering the
  close_notify with its own, and not `ErrTLSTruncated`. It takes about 26 s and is skipped under
  `-short`.

- **Exact operation names** in `ErrTimedOut`: "bringing the link up", "DNS lookup", "TCP connect",
  "TCP read", "TCP write", "TCP flush", "UDP send", "UDP receive".

### sim

- **Clock.** The modem, the gateway and the internet take `now` and do no I/O
  of their own. Only `handset.go` reads the clock, while it holds the modem
  mutex. `ReadTimeout` re-polls every 2 ms (`pollInterval`). A timeout longer
  than a year counts as a year.
- **Close.** `Close` drops DTR, which ends a call dialled with `AT&D2`. It then
  marks the handset closed: reads, writes, `RaiseDTR` and `DropDTR` fail with
  "the simulated handset is closed". `Control` keeps working.
- **Text.** Commands are matched with ASCII-only case mapping
  (`internal/asciicase`), and DNS names compare the same way. Unicode case
  mapping would turn `ſ` into `S` and the Kelvin sign into `k`, so the simulator
  would accept commands and names that the handset does not. The bytes of
  command lines, SMS text and DNS labels become text through
  `internal/utf8lossy`, so SMS text that is not valid UTF-8 appears in
  `Stats().SMSSent` with replacement characters.
- **Status.** `AT+CPAS` (`queryActivityStatus`, in `command.go`) reports
  "+CPAS: 004" while a data call is connected, online-command mode included,
  and "+CPAS: 000" otherwise. The reference is the Iridium ISU AT Command
  Reference.
- **The network.** IPCP offers `NetworkConfig.DNSServers`, which
  `ppp.ServerRole` copies, in order. `internet.deliver` drops a datagram whose
  destination, read with `wire.NewIPv4Packet`, is one of the `SilentAddresses`:
  nothing answers there. `Control.RestartPPP` goes to the modem (only while a
  call is connected), the circuit and `gateway.restartPPP`, which starts a new
  server with the new client address (`startServer`). On success it replaces
  the endpoint and the network, and closes the internet, which is rebuilt on
  the next Up. On failure it changes nothing: the new server is built before
  anything is assigned. The web service answers data that arrives after its
  socket has closed with a reset (`Abort`), as a real server's closed socket
  does, so the RST leaves on the same poll.
- **Network validity.** `startGateway` checks the network before the gateway starts.
  `ppp.NewEndpoint` refuses non-IPv4 addresses (`ErrInvalidAddress`), the DNS
  servers among them, and an MRU that is too small. A `Hosts` entry whose
  address is not IPv4 is refused with `%q in hosts needs an IPv4 address`, since
  a DNS A record holds four octets. `TestHostsMustHaveIPv4Addresses` pins the
  quoting: `debugtext.Quote` writes U+007F as `\u{7f}`, where Go's `%q` writes
  `\x7f`. `startServer` creates and opens the endpoint before the hosts check,
  so the errors keep that order. Either way the call fails with NO CARRIER, and
  the reason is logged at warn ("simulated handset: the PPP gateway cannot
  start: ..."). `sim.New` never refuses a configuration.
- **TLS echo.** It cannot be sans-I/O with crypto/tls: `tls.Server` runs over an
  `internal/memconn` connection, with a goroutine per session. On each poll the
  service moves records between the ipstack socket and that connection. It
  takes no more records while echo output is still waiting, so a client that
  never reads stalls the echo. It answers a client's close_notify with its own
  close_notify and then FIN. When the client sends FIN without a close_notify,
  even in the middle of a record, the echo says goodbye with close_notify.
  crypto/tls then fails its `Read` with `io.ErrUnexpectedEOF` rather than
  `io.EOF`, and `EOFDelivered` tells that FIN from a failure. A session is
  finished once its connection is closed and no output is pending
  (`isFinished`).
- **TLS configuration.** One `tls.Config` serves each simulated internet:
  - `MinVersion` is TLS 1.2. `CipherSuites` are the TLS 1.2 suites of
    `internal/ringprovider`: ECDHE with an AEAD cipher, as satnet's client
    offers them. crypto/tls's defaults would also serve a client that offers
    only ECDHE suites with CBC and HMAC-SHA1, which this echo refuses. The list
    is narrower on purpose, so that the echo agrees only suites that satnet's
    own client offers.
  - crypto/tls picks among the suites a client offers in its own order, not the
    client's: at TLS 1.2 by choice, at TLS 1.3 because it must (section 3).
  - `CurvePreferences` are X25519, P-256 and P-384. There is no post-quantum
    group.
  - `DynamicRecordSizingDisabled` is set.
  - Session ticket keys are automatic (there is no `SetSessionTicketKeys`
    call), so later connections to the same internet can resume.

  `secureecho_test.go` pins these settings against clients with settings of
  their own:
  - a TLS 1.2 client is served (`TestTheEchoServesATLS12Client`);
  - a client that offers one ECDSA AEAD suite (AES-128-GCM, AES-256-GCM or
    ChaCha20-Poly1305) is served with that suite
    (`TestTheEchoServesATLS12ClientThatOffersOnlyAnAEADSuite`);
  - a client that offers only AES-128-CBC-SHA, AES-256-CBC-SHA or
    AES-128-CBC-SHA256 is refused with a handshake_failure alert
    (`TestTheEchoRefusesATLS12ClientThatOffersOnlyCBC`);
  - a client that prefers X25519MLKEM768 agrees X25519
    (`TestTheEchoAgreesNoPostQuantumKeyExchange`);
  - the echo of a whole 16 KiB record is one record of the same size, which
    covers `echoChunkLen` (`TestTheEchoOfAWholeRecordIsOneRecord`).

  The RSA suites cannot be agreed, because the certificate of `secure.test` has
  a P-256 key. The certificate and key are embedded from `testdata/` and parsed
  once per process (`sync.OnceValue`).
- **Session lifetime.** A goroutine blocked in `tls.Conn.Read` on an in-memory
  connection is a garbage-collection root, so a session must end when its
  `Internet` does. The internet is dropped when PPP goes down, when the network
  restarts PPP (`restartPPP`), and when a renegotiation changes the MTU, which
  replaces it. It is dropped with the circuit when the call ends, and freed with
  the modem when the last handle to the modem goes away. A connection that the
  services forget once its socket has closed (a client's reset) drops its
  session in `removeClosedConnections`. `Internet` has an unexported `close()`
  that closes every session's connection and so ends its goroutine. It is called
  at each of those sites, and `removeClosedConnections` closes a forgotten
  connection's session.

  For a `Handset` that is dropped with a call still up, `runtime.AddCleanup` on
  the shared modem state shuts the modem down, and a cleanup on the `Handset`
  itself drops DTR. A collected handset therefore ends its call after AT&D2,
  even while a `Control` is held. Session goroutines do not reference that
  state.

  `secureecho_test.go` (run with `-race`) covers the session itself: echo, no
  records taken while echo output waits, a close_notify and a FIN each answered
  with close_notify (a FIN in the middle of a record included), a session
  finished only once its last records have gone, a session that never got a
  hello, a failed session and its debug line, resumption on a later connection,
  and the goroutines ending when a session closes or the services stop.

  The goroutine counts (`runtime.NumGoroutine` back to its baseline, polled with
  a deadline; the tests run serially) need the full client stack, so they live
  in `internet_integration_test.go`. They cover: after `DropCall`
  (`TestATLSSessionEndsWhenTheNetworkDropsTheCall`); PPP going down
  (`TestATLSSessionEndsWhenPPPGoesDown`); `Control.RestartPPP`
  (`TestATLSSessionEndsWhenTheNetworkRestartsPPP`); a client that resets its
  connection while the call stays up
  (`TestATLSSessionEndsWhenItsClientResetsTheConnection`); and a simulator
  nobody holds, whose call was dialled without AT&D2, so only the cleanup of the
  shared modem state can end its session (`TestATLSSessionEndsWithASimulatorNobodyHolds`).
  The same file has the end-to-end check that a client which hangs up still gets
  the rest of its echo before the goodbye.

  `gateway_test.go`'s `TestARenegotiationThatChangesTheMTUBuildsANewInternet`
  checks that a renegotiation to MRU 1200 builds a new internet with that MTU and
  closes the old one's TLS session. The `Handset`'s own cleanup, which drops DTR,
  is `TestAHandsetCollectedWithoutCloseDropsDTR` in `handset_test.go`.
- **`internal/memconn`.** One type, `Conn`, made with `New(local, remote
  netip.AddrPort)`, with two sides. Its doc comments are the contract.
  - The `net.Conn` side, used by crypto/tls: `Read` blocks and honours the read
    deadline (`os.ErrDeadlineExceeded`, a temporary `net.Error`); `Write` never
    blocks; `Close` wakes a waiting `Read` with `net.ErrClosed`; and the deadline
    setters.
  - The pump side, for a sans-I/O owner:
    - `Inject(p []byte) int` takes bytes from the peer, and none once closed.
    - `CloseInbound()` records the peer's FIN; `Read` then returns `io.EOF`.
    - `Extract(p []byte) int` returns the bytes crypto/tls wrote, also after
      `Close`, so that a last close_notify or alert still leaves.
    - `Pending() int` reports output still waiting.
    - `AwaitReader(timeout) bool` waits until a `Read` is waiting with nothing
      buffered and no FIN pending, or until the conn is closed. After
      `CloseInbound` only `Close` satisfies it. The owner calls it between
      injecting and extracting, so the answer to what it injected is there in
      the same step.
    - `EOFDelivered() bool` reports whether `Read` has handed `io.EOF` to
      crypto/tls. crypto/tls returns `io.EOF` both for the peer's close_notify
      and for a bare FIN at a record boundary, and only this tells them apart.
    - `Closed() bool`.

  The TLS echo uses the pump side on the server side. The tests use it on the
  client side: `clientNetwork.startTLS` (`support_integration_test.go`) runs a
  `sim.TLSClient` (`tlsclient_test.go`) for `sim.TLSServerName`, trusting
  `SecureTestCAPEM`. It is pumped to and from the ipstack `tcp.Socket` on every
  exchange, with the helpers `echoInsideTLS`, `sayGoodbye` and
  `TLSClient.ServerSaidGoodbye`. satnet cannot be the client, because its
  `CloseWrite` always sends the FIN together with the close_notify.
- **Random seeds.** TCP initial sequence numbers come from `crypto/rand`, so they
  differ between runs.
- **Logs.** Log lines carry `pkg=sim` and are formatted at the call site:
  "simulated handset: ...", "simulated gateway: ...", "simulated internet: ...".
  The gateway's "IPv4 up, " line writes the settings with `internal/debugtext`.
  Each line has a test of its text and level: `modem_test.go` for the modem,
  `gateway_test.go` for the gateway, `internet_test.go` for the internet's
  warning and the debug lines of the UDP echo and the DNS server, and
  `secureecho_test.go` for the TLS echo (its text, then crypto/tls's reason).
- **Defaults** are named constants, each documented where it is declared.

### serial

- **Files.** `doc.go`; `discover.go` (`Port`, `Discover`, `discover`,
  `parseUSBID`); `transport.go` (`DefaultBaud`, `Transport`, `Open`, the select(2)
  limit: `selectSetSize` and `isBeyondSelect`); `connect.go` (`ChoosePort`,
  `Connect`, `ChoosePortFrom`); and `closeonexec_unix.go` and
  `closeonexec_other.go` (`openCloseOnExec`, `descriptorsOf`).
- **Open.** The port is opened with `serial.Open(path, &serial.Mode{BaudRate:
  baud, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit})`,
  without `InitialStatusBits`: with them, a firmware that refuses control
  requests would fail the whole open. Windows then enables DTR and RTS in the
  DCB, and on Unix open(2) raises both. `SetDTR(true)` and `SetRTS(true)` are
  then called as well, and their results are ignored. Some CDC firmware only
  transmits once RTS is asserted, which is why RTS is raised too.
  go.bug.st/serial opens Unix ports exclusively (`TIOCEXCL`) on its own
  (section 3).
- **Close-on-exec.** `Open` and `Connect` open through `openCloseOnExec`. On Unix
  it holds `syscall.ForkLock` for reading across the library's `Open`, and marks
  the port's descriptor and both ends of its close pipe close-on-exec, since the
  library opens without `O_CLOEXEC`. Without the marking, a child process started
  while the port is open keeps the descriptor open, and DTR up, past `Close` and
  past a crash. The library's `Open` must not take `ForkLock` itself; v1.8.0's
  `x/sys/unix` `Open` and `Pipe` do not. On other platforms the library's `Open`
  is used, because Windows handles are not inheritable.

  `descriptorsOf` reads v1.8.0's unexported `unixPort.handle` and
  `closeSignal.rd`/`wr` by guarded reflection, and never panics. The pty tests of
  close-on-exec and of the select(2) limit fail if an upgrade renames those
  fields. Suggesting `O_CLOEXEC`, and poll(2) instead of select(2), upstream to
  go.bug.st/serial would let the reflection and the `recover` in `libraryRead`
  go.
- **Closed flag.** `Transport` keeps an `atomic.Bool` closed flag and an
  `RWMutex`, `inUse`. Every library call that acts on the descriptor (each
  `Write`, `Drain`, `SetDTR`, `SetReadTimeout`) holds `inUse` for reading while
  it checks the flag and runs (`usePort`). After `Close`, `WriteAll`,
  `ReadTimeout`, `RaiseDTR` and `DropDTR` return `ErrTransport` "the serial port
  is closed" without calling the library.

  `Close` sets the flag, stops the runtime cleanup, and waits up to `closeGrace`
  (1 s, trying `TryLock` every millisecond) for the calls under way. Then it
  closes the port and unlocks. So no call acts on a descriptor number after
  `Close`, by which time the process may have reused it. A call stalled past the
  grace, on a device that has stopped taking data, finds the flag set when it
  returns, and its `WriteAll` then neither writes nor drains.

  A read's wait holds no lock: the library's close pipe ends it. Two limits
  remain. The library closes the descriptor before it wakes readers, so a read
  that select(2) has just let through may read once from the number after the
  close. And on macOS the library's own close blocks behind a write stuck in the
  kernel, which the one-second grace cannot shorten.

  A `Transport` collected without `Close` closes its port all the same
  (`closeAbandonedPort`, registered with `runtime.AddCleanup` by `open`).
- **ReadTimeout.** An empty `p` returns (0, nil) at once, without calling the
  library (section 3). After `Close` it fails as every read does. Otherwise a
  deadline is set, and each pass calls `SetReadTimeout(min(max(remaining,
  time.Millisecond), longestLibraryWait))` and then `Read`. The library returns 0
  and no error on timeout.

  `longestLibraryWait` is a day. macOS's select(2) refuses a wait of more than
  10^8 s. The library's Windows port refuses a wait above 0xFFFFFFFE ms and cuts
  one above 0x7FFFFFFE ms (some 24.8 days) down to that, which would report
  silence early. A longer timeout is therefore waited out in turns.

  A read whose capped wait ended in silence, or that a signal interrupted
  (`errors.Is(err, syscall.EINTR)`, Ctrl-C for example), is retried with what is
  left of the timeout. It gives (0, nil) once the deadline has passed.
  go.bug.st/serial's Unix `Read` already retries EINTR against its own deadline;
  the loop keeps the promise for any port of the library's `Port` interface.
- **Panics from the library's Read.** `libraryRead` turns a runtime panic into
  the `ErrTransport` of section 5 only for a port that select(2) cannot watch:
  one whose descriptor, read by `descriptorsOf`, is `selectSetSize` (FD_SETSIZE,
  1024) or more (`isBeyondSelect`). Any other panic is raised again. A runtime
  panic on a port that select(2) can watch is a bug. Off Unix, `descriptorsOf`
  finds no descriptor, so there every panic passes on.
- **WriteAll.** It writes until everything is taken, then calls `Drain`. A
  library `Write` that takes nothing and reports no error fails with
  `ErrTransport` wrapping `io.ErrShortWrite`, so the write loop cannot spin.
- **Discover.** It calls `enumerator.GetDetailedPortsList()` and keeps the USB
  ports whose VID parses (hex, any case) to `satlink.IridiumUSBVendorID`. There
  are no active-probe filters, so no device is probed. A port whose PID does not
  parse is still reported, with `ProductID` 0 and `Model` "" (`parseUSBID`
  returns 0 explicitly). On macOS the enumerator reports only each port's callout
  node (`IOCalloutDevice`, `/dev/cu.*`), so no filter for `/dev/tty.*` is needed.
  On Linux it reads sysfs, so libudev is not needed.
- **Errors.** Errors are `&satlink.Error{Kind: satlink.ErrTransport, Err: err}`,
  or carry a `Message` for "no Iridium handset found on USB" and "several
  handsets found, choose one: a, b". An error number from the operating system
  (`syscall.Errno`) is wrapped so that it reads as the C library writes it, with
  a capital first letter ("No such file or directory"). It still matches
  `errors.Is` with the number and with the `io/fs` errors.
- **Seams for tests**, without mutable globals. `Transport` holds
  go.bug.st/serial's `Port` interface, so internal tests build
  `&Transport{port: fake}`. `Open(path, baud)` calls `open(path, baud,
  openCloseOnExec)`, which is the seam that `ForkLock` and the reflection need.
  `Connect` calls `connect(path, baud, openCloseOnExec, Discover)`. `Discover()`
  calls `discover(enumerator.GetDetailedPortsList)`, whose filtering is a pure
  function. `ChoosePort(path)` calls `choosePort(path, Discover)`, which chooses
  among the ports found with the exported `ChoosePortFrom(ports)`, as the CLI
  does over its own discovery.
- **Tests.**
  - `discover_test.go`: a table of `PortDetails` (the VIDs "1edd" and "1EDD", a
    missing VID, a port that is not USB, known and unknown products, a PID that
    does not parse), the enumerator's order, and no device probed.
  - `transport_test.go`: `ReadTimeout`'s `max(timeout, 1 ms)` and the library's
    (0, nil); long waits read in turns; no read asks the library to wait longer
    than its Windows port or macOS's select(2) allows
    (`TestNoReadAsksTheLibraryToWaitLongerThanItCan`, which writes both limits as
    literals that cite the library's source); an interrupted read retried with the
    rest of its timeout, and one whose time is up reporting silence; the panics of
    the library's `Read` passed on (`panicOf`), including a runtime panic of a port
    at descriptor 1023, which select(2) can watch
    (`TestARuntimePanicOfAPortSelectCanWatchIsPassedOn`, on every platform);
    `WriteAll` over partial writes, then draining, and stopping at a write that
    takes nothing; DTR calls reaching the port; `Close` being idempotent and ending
    a blocked read; a `Close` that lets a write or DTR change under way finish
    first (`TestCloseLetsACallUnderWayFinishFirst`, over a fake port whose stalled
    calls note whether they reached the port once it was closed), and that gives
    up after the second it promises (`TestCloseGivesAWriteUnderWayAWhileButNoMore`,
    held to a literal second, not to `closeGrace`); a closed `Transport` leaving
    the port alone (`TestAClosedTransportLeavesThePortAlone`); a `Transport`
    collected without `Close` closing its port, and a closed one not being closed
    again; and errors wrapped as `ErrTransport`, with error numbers written as the
    C library writes them.
  - `transport_unix_test.go`: a read on a port at descriptor 1024 or more, which
    select(2) cannot watch, fails with the text of section 5 instead of panicking
    (`TestAReadThatTheLibraryCannotWaitForFailsInsteadOfPanicking`).
  - `connect_test.go`: both exact messages from `ChoosePortFrom`; a missing device
    ("transport error: No such file or directory" on macOS and Linux); `Connect`
    without a path opening the only handset discovered, and opening nothing when
    there is none; `Connect` closing the port when `Initialize` panics, with the
    panic passing on unchanged (`TestConnectClosesThePortWhenInitializePanics`, a
    fake port whose reads panic with a string, which `libraryRead` raises again);
    and `Connect` failing over a fake port that fails every call. A port that never
    answers would take `Initialize` about 33 s to fail: three 2 s ATs, two escapes
    with the default guard, a raw ATH, the DTR drop and ATH, then three more ATs.
  - `closeonexec_test.go` tests `openCloseOnExec`. The pty tests, in `pty_test.go`
    with `pty_darwin_test.go` and `pty_linux_test.go`, run against a real
    pseudo-terminal and check close-on-exec and the select(2) limit.
- **macOS builds need cgo.** The enumerator's IOKit code is cgo. Without cgo the
  build fails with `undefined: nativeGetDetailedPortsList`. This holds for
  `serial`, `cmd/satlink` and the desktop build of `mobile` alike.

### mobile

- **No panic crosses the boundary.** gomobile would end the process, so every
  exported function and method that returns an error recovers panics into
  `SatlinkErrorInternal` (`recoverPanic`). The methods without an error result
  that reach the libraries recover too: `SatelliteLink.Status`,
  `IridiumHandset.TakeEvents`, `Cancellation.Cancel` and `Cancelled`, the
  connections' `PeerAddress`, `Resumed`, `ALPNProtocol` and `MaxDatagramLen`.
  They log "recovered a panic in %s: %v" at error level (`survivePanic`) and
  return the empty result. The trivial accessors and constructors, which reach
  nothing that could panic, recover nothing: the lists' `Len` and `Get`, the
  settings' adders, `NewCancellation`, `NewLinkSettings`, `NewTLSSettings`,
  `KnownProducts`, `IridiumUSBVendorID`, `AsSatlinkError` and
  `SatlinkError.Error`.

  Every goroutine that mobile starts recovers its panics. The link listener and
  the serial port closer log "%s panicked: %v" at error level. The log-delivery
  goroutine recovers a sink's panic for each record, but does not log it, since
  logging it would hand it to the same sink again.

  `initializedHandset` and `establishOwnLink` defer
  `closeUnlessHandedOn(handset, &flag)`. The flag is set only on success, so a
  serial port that the library opened is closed on every other way out, a
  recovered panic included. A panic recovered by an establish function after the
  dial has started can leave the call up, because nothing in the transport
  lowers DTR. A transport's panic inside the core's dial or hang-up does pulse
  DTR first (section 3), which ends the call where the line reaches the handset.

  Over a `ByteTransport` the handset's `Close` does nothing. The docs of
  `EstablishLink` and `SatlinkErrorInternal` therefore tell the app to end the
  call with an `IridiumHandset` made on the still-open transport. Its
  initialisation ends a data call left up, and its `HangUp` ends one held in
  online-command mode. `EstablishLinkWithHandset` strands the lent handset, and
  `Reinitialize` ends the call. `EstablishSerialLink` closes its port, which
  drops DTR.
- **Zero values.** gomobile gives every struct without a constructor of its own
  a public no-argument constructor that makes a zero value (Java `new
  SatelliteLink()`, Swift `SatlinkMobileSatelliteLink()`). Every method copes
  with a zero receiver. Methods that return an error fail with
  `SatlinkErrorInvalidArgument` ("the SatelliteLink was not made by this
  library"), and the others return empty values. `NewLinkSettings` and
  `NewTLSSettings` become the Java constructors, so their zero values never
  appear in Java.

  `IridiumHandset` and `TLSConnector` have constructors that take arguments, so
  they get no zero-value constructor. Java has no `new IridiumHandset()`. In
  Objective-C and Swift, gobind gives them only an `init:` that takes the
  constructor's arguments, and they inherit NSObject's `init` and `new`. The
  same holds for the proxy classes `SatlinkMobileByteTransport`,
  `SatlinkMobileLinkListener` and `SatlinkMobileLogSink` (Swift names the
  protocols `...Protocol`). Such an object has a nil `_ref`. gobind's glue raises
  `NSInternalInconsistencyException` ("go_seq_go_to_refnum on objective-c objects
  is not permitted") on its first use, or when it is passed to a function. That
  happens before any Go code runs, so no `recoverPanic` can help.

  `scripts/build-bindings.sh` therefore runs `scripts/mark-unusable-inits.sh` on
  every `SatlinkMobile.objc.h` in the xcframework. The script declares `init` and
  `new` `NS_UNAVAILABLE` after `initWithRef:` in each class that declares no
  argument-less `init`, so clang and Swift refuse them at compile time. Those
  five classes are `SatlinkMobileByteTransport`, `SatlinkMobileIridiumHandset`,
  `SatlinkMobileLinkListener`, `SatlinkMobileLogSink` and
  `SatlinkMobileTLSConnector`. Their doc comments reach the header, and the
  package doc's Objects section says the same.
- **Nil arguments.** A nil settings, transport, handset, connector or sink fails
  with `SatlinkErrorInvalidArgument` "%s must not be nil", naming the parameter.
  A nil listener is allowed.
- **Never nil.** No exported function returns a nil pointer, or an empty
  `[]byte`, together with a nil error. Swift imports such a method as
  `throws -> T` and throws a generic `nilError` for nil, and Java receives null.
  So:
  - `TCPConnection.Receive` and `TLSConnection.Receive` return a `Received`. Its
    `Data` holds at least one byte unless `Finished` is set, which marks the end
    of the stream.
  - `UDPConnection.Receive` returns a `Datagram`. Its `Data` may be empty (null)
    for an empty datagram.
  - `SatelliteLink.HangUp` returns the `LinkState`. It is `LinkStateClosed`, with
    its `Reason`, once the call has ended. Otherwise it is the state at the time
    (normally `LinkStateClosing`, and for a moment still `LinkStateUp`, until the
    driver's next round sees the request).
  - The lists (`PollEvents`, `ListSMS`, `Resolve`, `DiscoverSerialHandsets`) are
    non-nil, possibly empty.
  - Methods without an error result never return nil either. `Status` and
    `TakeEvents` return empty objects for a zero receiver
    (`&LinkStatus{State: &LinkState{}, DNSServers: &StringList{}}` and
    `&StringList{}`). Fields of those types are never nil, except the ones
    documented as optional (`LinkState.Reason`).
  - `AsSatlinkError` and the lists' `Get` return nil by design, and Swift sees
    an optional.
- **Error mapping.** Errors are mapped in this order:
  1. `errors.As` a `*SatlinkError` passes through unchanged.
  2. `*satnet.Error` is mapped by its kind (below).
  3. `*satlink.Error` is mapped by its kind.
  4. `*satnet.ParseRuleError` maps to InvalidArgument.
  5. `errors.Is(err, net.ErrClosed)` maps to InvalidArgument "the connection is
     closed".
  6. Any other error, which the libraries never return, becomes
     `SatlinkErrorInternal` with its text.

  The satnet checks come before the `net.ErrClosed` check, because satnet's
  errors also match the standard errors. `satlink` kinds map one to one: `ErrBusy`
  becomes `SatlinkErrorHandsetBusy`, and the Reason or Result is the core error's
  `Message`, followed by ": " and its cause when there is one. `satnet` kinds map
  as follows:
  - `ErrInvalidConfig` becomes InvalidArgument;
  - `ErrTimedOut` becomes Timeout, with `Command` set to the operation;
  - `ErrConnectionFailed` sets `Address` to `Addr.String()`;
  - `ErrLinkDown` sets `Reason` to `Reason.String()`;
  - `ErrHandset` becomes the wrapped core error.

  A rule that does not parse reaches the app through the same table, because
  `config` maps `ParsePolicy`'s `*satnet.ParseRuleError` with `fromError`. A host
  address that is not IPv4 fails with the reason `AddrParseError`'s "invalid IPv4
  address syntax", which names no address. `hostAddresses` therefore checks the
  hosts in map order, so which bad host is reported is not fixed. A `HangUp`
  whose call may still be up becomes `SatlinkErrorHangUpFailed`, with the close
  reason's text.

  The establishing functions check `errors.Is(err, satlink.ErrCallMayStillBeUp)`
  before the mapping. Such a failure is `SatlinkErrorHangUpFailed`, with the
  error's text as its Reason. `satlink.ErrCallMayStillBeUp` maps to
  `SatlinkErrorHangUpFailed` with the core error's `Message`.
  `satlink.ErrCancelled`, also when it sits inside satnet's `ErrHandset`, maps to
  `SatlinkErrorCancelled`, "the call setup was cancelled".
  `SatlinkErrorCancelled` is declared after `SatlinkErrorInternal`, so that no
  existing constant changes value. The general mapping ignores the flag on errors
  of other kinds.

  `ListSMS` passes its filter to the core, whose "%d is not an SMS filter" maps
  through unchanged.
- **Durations.** Every `timeoutMillis` and every settings field in seconds is an
  `int64`. These convert to `time.Duration` with saturation (`convert.go`), and a
  timeout of zero is a deadline of now.
- **Ports.** `OpenTCP`, `OpenTLS` and `OpenUDP` refuse only ports outside 0 to
  65535, with "port must be between 0 and 65535". Port 0 passes to satnet, whose
  policy refuses it as `PolicyDenied`: "echo.test:0 is not allowed by the link
  settings".
- **Foreign transport adapter.** Timeouts in milliseconds are clamped to [1,
  2147483647]. At most `len(p)` bytes are copied from what `Read` returns, and
  `Write` is never called with empty data. A host error that is a `SatlinkError`
  of kind Transport gives its Reason. A `*satlink.Error` of kind `ErrTransport`
  from a Go transport passes unchanged. Anything else becomes `ErrTransport` with
  `err.Error()`. The adapter asserts `satlink.Transport` and
  `satlink.DTRController` at compile time.
- **TLS settings.** `NewTLSConnector` reports problems in this order: a PEM bundle
  that does not parse, then a pin that is not 32 bytes ("a SHA-256 pin is 32
  bytes"), then settings that trust nothing. satnet checks the first and the last
  in one call, so the pins are checked in between.
- **Lending** (`EstablishLinkWithHandset`). The IridiumHandset's `*satlink.Handset`
  goes to `satnet.Establish`. Busy and the release come from the core.

  `lend` returns the setup's `handsetLoan`. Through it, `EstablishLinkWithHandset`
  settles where the handset goes, exactly once: with the loan's `returnedAfter`
  when the link did not come up, or with its `lentTo` once it is up. Each of these
  marks the loan settled. The deferred `loan.abandonUnlessSettled()` runs
  `abandonDialling` only when neither ran, which only a panic causes. It then
  strands the handset, and closes it if the app closed the IridiumHandset.

  A setup that has settled leaves the slot alone, because by then the handset may
  belong to another thread's setup. An unconditional deferred `abandonDialling`
  once stranded that setup's handset, with a false `SatlinkErrorHangUpFailed`, and
  closed its port under it when the app had closed the IridiumHandset in the
  meantime. `handBackFromDialling` still takes back only a handset that is being
  lent.

  `Close` records `closed` under the slot lock (`recordClose`). The IridiumHandset
  then refuses every method but `Close` with "the handset is closed", wherever its
  handset is. A `Close` while a link has the handset starts the port closer. While
  the link is being set up, `lentTo` starts it, or `endDialling` closes the handset.
  The methods take `h.mu`, defer the unlock, and then call `reclaim`, so that a
  fault in `reclaim` leaves the lock free.

  Closing a lent link does not close the handset. Closing a lent IridiumHandset, or
  one whose link is being set up, does not close its port either. The link hangs up
  through the port, so the port closes once the call has ended, or at once if the
  link does not come up. `Close` takes only a short-held slot lock and never waits
  for a command.

  Only serial links need their own handset closed, because closing a handset over
  a `ByteTransport` does nothing. `EstablishSerialLink` starts a goroutine
  (`satlink-port-closer`) that polls `Watcher.WaitClosed` every `listenerPoll` and
  closes the port once the call has ended, however it ends. It holds neither the
  `Link` nor the `SatelliteLink`, so a dropped link still hangs up and has its port
  closed.

  `NewLinkSettings` takes its values from `satnet.NewConfig(satnet.DenyAll())`,
  number and credentials included.
- **The stranded handset.** A lent link that fails to come up with
  `ErrCallMayStillBeUp`, or that closes with `Status().CallMayStillBeUp`, strands
  its IridiumHandset. So does a panic inside `satnet.Establish`. Only the driver
  goroutine recovers that panic, and it reaches `EstablishLinkWithHandset` as
  `SatlinkErrorInternal`, with no knowledge of whether a call is up. An
  IridiumHandset method whose handset error is of kind `ErrCallMayStillBeUp`
  also strands it (`perform`). Examples are `Answer` taking a data call that it
  cannot end. The check is on the kind (`isCallMayStillBeUp`), and `strand` takes
  only a ready handset.

  A stranded handset fails every method except `Reinitialize` and `Close` with
  `SatlinkErrorHangUpFailed`, whose Reason is "the last data call could not be hung
  up; reinitialize ends it". `TakeEvents` returns an empty list.

  `Reinitialize` on a ready handset runs `Initialize`, and an error of that kind
  strands it. On a stranded handset it runs `endStrandedCall`, which runs
  `Initialize` and then `HangUp`. A call held in online-command mode shows itself
  only to firmware with +CPAS, and ATH answers OK when there is no call. Every
  failure of `endStrandedCall` is of kind `ErrCallMayStillBeUp`
  (`asCallMayStillBeUp`). An `Initialize` error of that kind stays as it is, any
  other error keeps its text as `Message`, and an ATH failure becomes "ATH failed:
  %v". The app hears `SatlinkErrorHangUpFailed`, and the handset stays stranded
  until a `Reinitialize` succeeds and makes it ready. While a link has the
  handset, `Reinitialize` also fails with `SatlinkErrorHandsetBusy`.

  The IridiumHandset learns how a lent link ended lazily, at its next method call,
  through the link's `Watcher().Status()` (state `LinkStateClosed`, then
  `CallMayStillBeUp`).
- **Cancellation.** `mobile.Cancellation` holds a `satlink.Cancellation` by value,
  so its zero value, which gomobile's constructor makes, is usable too.
  `NewCancellation` is the constructor (Java `new Cancellation()`). `Cancel` and
  `Cancelled` recover panics with `survivePanic`. The establish functions take the
  cancellation as a last parameter, which may be nil, because gomobile has no
  default arguments. `LinkSettings.config(cancellation)` points
  `config.Call.Cancellation` at it. Without one, the configuration keeps the fresh
  cancellation that `satnet.NewConfig` made, which nobody else can reach.

  satnet aborts the dial, or hangs up a call that came up while PPP negotiates.
  The establish function then fails with `SatlinkErrorCancelled`, or with
  `SatlinkErrorHangUpFailed` if that hang-up failed.
- **Listener.** A goroutine (`satlink-link-listener`) polls
  `Watcher.NextStateChange(listenerPoll)` every second, and stops after it delivers
  `LinkStateClosed`. An error that the listener returns is ignored on purpose: the
  link carries on, and so do the notifications. A panic is logged. The goroutine
  holds the watcher alone, never the `Link` or the `SatelliteLink`, so a dropped
  link still hangs up. The goroutine starts once the link is up. Starting a
  goroutine cannot fail, so there is no failure to undo after dialling.
- **SendSome and Abort.** `SendSome` sets the write deadline and calls satnet's
  `WriteSome` once. It queues what fits, waiting for room until the deadline but
  no longer than it takes some to fit, and returns how much it queued as an
  `int64`. `SatlinkErrorTimeout` means that nothing was queued. Any other failure
  is returned as the error. `Abort` calls `TCPConn.Abort` or `TLSConn.Abort`. After
  `Close` it fails with InvalidArgument "the connection is closed".
- **Receive.** The buffer is `min(maxLen, 64 KiB)`. At the end of the stream the
  result is `&Received{Finished: true}`. A stream refuses a `maxLen` below 1
  (`streamBuffer`). UDP refuses only a negative one (`datagramBuffer`), and there
  0 takes a datagram and keeps none of it. The three `Receive` docs state the cap,
  and that a timeout of zero takes only what has already arrived. The stream docs
  add that `maxLen` must be at least 1. The docs reach the generated Java and
  Objective-C.
- **Settings.** `LinkSettings` and `TLSSettings` guard their lists with a mutex.
  `config()` reads `entries()` copies. `NewTLSConnector` works on one `copied()`
  snapshot, so its ordered checks (PEM, then pins, then trusting nothing) all see
  the same settings. Their exported fields are written by gomobile's setters with
  no copy at the boundary, so they must not be set while another thread hands them
  to a call (section 3).
- **Equality.** gobind's `equals` and `hashCode` compare exported fields only
  (section 3). The package doc's Equality section tells apps to compare objects
  with `===` and to keep them in identity-based collections. `bindings_test.go`
  pins which fields each `equals` compares.
- **TLS connection.** `ShutdownWrite(timeoutMillis)` sets the write deadline to
  now plus the timeout, then calls satnet's `TLSConn.CloseWrite`. `Close` releases
  the connection without waiting. It sets the write deadline to now and calls
  `TLSConn.Close`. That sends the records still pending, and the close_notify as
  far as the send buffer has room at once, then closes the TCP connection
  gracefully. A close_notify that does not fit is ignored, which is expected.

  `Send` documents what a timed-out send leaves. A timeout does not say how much
  was taken, and what was taken is left for the next call on the connection. Call
  `WaitAcknowledged` before `HangUp`, or `Abort`, or `SendSome`. `ShutdownWrite`
  documents that sending fails from then on, even after a timeout, and that calling
  it again finishes the shutdown.
- **SetLogSink.** An unexported bridge does the forwarding. It holds `logtext`'s
  handler with the level filter. Its `emit` does a non-blocking send of
  (level, pkg, text) into a channel of 256, dropping records when it is full. A
  goroutine (`satlink-log`) delivers them to the sink. Internal tests build one
  bridge per test, without touching `slog.Default`. `SetLogSink` validates its
  arguments, then installs one bridge with `slog.SetDefault`. This is guarded so
  that it runs once per process, and a second call fails with "a log sink is
  already set".
- **Desktop-only code** stays in `serial.go`, behind the build constraint
  `//go:build !android && (!ios || (macos && !maccatalyst))`. For gomobile's macOS
  target, gobind reads the package with `GOOS=ios` and the `macos` tag, and
  gomobile compiles it with `GOOS=darwin` and the `macos` tag, against the macOS
  SDK, where the serial library compiles with cgo. iOS and Mac Catalyst (`macos`
  and `maccatalyst` tags) leave the file out.
- **Bindings check.** `go tool gobind` cannot take another `GOOS`, because it would
  then cross-compile gobind itself. So `bindings_test.go` builds the pinned gobind
  binary, as the scripts do, and runs it for each target:
  - `GOOS=android "$tools/gobind" -lang=go,java -javapkg=io.github.anhydrous99.satlink
    -outdir <dir> ./mobile`;
  - `GOOS=ios "$tools/gobind" -lang=go,objc -prefix=Satlink -tags=ios -outdir <dir>
    ./mobile`, where the serial API is absent;
  - the same with `-tags=macos,maccatalyst`, where it is absent;
  - the same with `-tags=macos`, where it is present (with cgo enabled and the
    macOS SDK's clang as `CC`).

  None of these may report a skipped symbol. gobind type-checks without running
  cgo: `GOOS=ios -tags=macos` succeeds even with `CGO_ENABLED=0`. So that does not
  prove the serial library builds. The test therefore also compiles the package for
  each target: `GOOS=android GOARCH=arm CGO_ENABLED=0`; `GOOS=ios GOARCH=arm64
  CGO_ENABLED=0 -tags ios`; and `GOOS=ios GOARCH=arm64 CGO_ENABLED=1
  CC=$(xcrun --sdk macosx --find clang)` with `CGO_CFLAGS` and `CGO_LDFLAGS` set to
  `-isysroot <SDK>` and `-tags macos`. The last one fails without cgo, with
  `undefined: nativeGetDetailedPortsList`.

  The test also checks the Java declarations that the Kotlin sources use. It
  compiles the generated Java at Java 8, gomobile's level, when a JDK is found.
  Homebrew's OpenJDK 21 and 27 still accept Java 8; macOS's `/usr/bin/javac` is
  only a stub.

  `unusableinits_test.go` runs `scripts/mark-unusable-inits.sh` on the macOS header
  that gobind writes. It requires `init` to be unavailable in exactly
  `SatlinkMobileByteTransport`, `SatlinkMobileIridiumHandset`,
  `SatlinkMobileLinkListener`, `SatlinkMobileLogSink` and
  `SatlinkMobileTLSConnector`. It type-checks Objective-C against the header with
  the macOS SDK's clang (`-fsyntax-only -fobjc-arc -fmodules`). The documented
  construction must compile, and `init` and `new` of those classes must be refused
  as unavailable. The test also checks that the script leaves a header that it has
  already marked unchanged. It refuses a header of another shape (no gobind class,
  or a class without `initWithRef:`), and leaves such a header as it was.

### cmd/satlink

The command is built on these packages: `serial.Discover`, `ChoosePort`, `Open` and
`DefaultBaud`; `sim.New`, `DefaultConfig`, `IridiumLinkProfile` and
`InstantLinkProfile`; the `Handset` API (`CommandTimeout` for `at`, `PollURCs` for
`monitor`, `TakeURCs` for the trailing "(event)" lines); `satnet.NewConfig`,
`Config.Validate`, `ParsePolicy`, `Policy.AllowsLookup` and `AllowsHost`, `Establish`
with `config.Call.Cancellation`, `Status`, `Watcher().Status()` (`Usage` and
`CallMayStillBeUp`), `Resolve`, `Dial`, `DialTLS`, `NewTLSConnector(satnet.TLSOptions{})`,
the `TCPConn` and `TLSConn` methods, `Link.RequestClose` and `Link.Close`; and
`errors.Is` with `satlink.ErrCallMayStillBeUp`, `satnet.ErrTLSTruncated` and
`satnet.ErrTimedOut`. The battery and registration statuses print through their
`String()` methods.

- **Structure for tests.** `func run(ctx context.Context, env environment) int`.
  `environment` groups:
  - the arguments, standard input, output and error;
  - whether each of standard input, output and error is a terminal
    (`stdinIsTerminal`, `stdoutIsTerminal`, `stderrIsTerminal`);
  - `lookupEnv func(string) (string, bool)`, where a variable set to "" counts as
    set;
  - the function that installs the log handler (`slog.SetDefault` in `main`, a
    no-op or a recorder in tests);
  - the functions that discover handsets and make simulated ones;
  - `notifyInterruptions` and `quitInterrupted`, which tests replace to relay
    their own interruptions.

  `run` builds a fresh command tree on each call. `main` calls `os.Exit(run(...))`,
  except for an interrupted run (`exitInterrupted`), which ends by SIGINT (below).
  Tests never swap `slog.Default`, never read the real environment, and run
  commands in-process. A command given no `--port` chooses among the handsets that
  the environment discovers, as `serial.ChoosePort` would (`portPath`, through
  `serial.ChoosePortFrom`). Tests can therefore give it none, one, or several.
- **Process setup.** `main` first calls `ignoreBrokenPipes`. On Unix that is
  `signal.Ignore(syscall.SIGPIPE)`, so that a write to a pipe that nobody reads
  fails with EPIPE, and the command fails once it has hung up. Without it, Go's
  runtime would end the process by SIGPIPE in the middle of the call and skip the
  hang-up. Go's runtime replaces a SIGPIPE that was ignored on entry, so the CLI
  must ask for the ignore itself. satlink starts no programs that would inherit it.

  `processEnvironment` calls `environmentOf` with the process's arguments, its
  standard files and `os.LookupEnv`. `environmentOf` hands the streams over as
  `processStream` and says which of them are terminals. Tests can therefore give
  it a pseudo-terminal, the null device, and a lookup of their own.

  The errors of a `processStream` that carry an OS error number are written as
  the bare OS message with the number, such as "Broken pipe (os error 32)" or "Is a
  directory (os error 21)". The path that Go's error adds, as in "write
  /dev/stdout: broken pipe", is left out.

  A standard stream whose descriptor is not open is treated as an empty input or a
  sink that takes every write, so the command exits 0 with its output dropped.
  `badDescriptor` names the error that marks such a stream:
  - On Unix it is EBADF, for a standard output opened read-only or a standard input
    opened write-only (`processstreams_unix.go`;
    `TestAStreamNotOpenForTheDirectionIsAnEmptyInputAndASink` in
    `processstreams_test.go`).
  - On Windows it is ERROR_INVALID_HANDLE, for a NULL standard handle. A process
    without a console, or one started by such a process, has NULL for a stream that
    it was not handed. Go keeps that handle from `GetStdHandle`
    (`processstreams_windows.go`; `TestAStreamWithoutAHandleIsAnEmptyInputAndASink`,
    run only on Windows). Package `syscall` does not name ERROR_INVALID_HANDLE, and
    its `syscall.EBADF` is a number that Go made up and no call returns. The Windows
    file therefore declares `badDescriptor` as `syscall.Errno(6)`. It does not import
    `golang.org/x/sys/windows`, because this module uses no x/sys directly; x/sys
    is in `go.mod` only as a dependency of the serial library.

  ERROR_ACCESS_DENIED, a Windows standard output opened read-only, stays a failure.

  `main` also clears `cobra.MousetrapHelpText`. Otherwise cobra on Windows refuses a
  command started from Explorer, such as a shortcut with arguments.
- **The command line.** The command line is read by `parseCommandLine`
  (`commandline.go`) against the command tree (`command.go`; the arguments are
  declared in `arguments.go` and `values.go`). Help is rendered by `help.go`.
  Problems are reported by `usage.go`, with `usageline.go` and `suggestions.go`,
  and styles come from `palette.go`. cobra runs the named command (its `RunE`, and
  the root's `PersistentPreRun`, which installs the logger and the context).

  The parser is the CLI's own, rather than cobra's or pflag's, so that help text,
  error messages and option precedence stay under this package's control. Those
  are the parts users see.

  `parseCommandLine` returns the command named, or a `*commandLineExit`. The exit
  is either help or the version (status 0, standard output), or a problem (status 2,
  standard error). `commandLineExit` is not an error type. Every parser and
  explainer function returns one or nil, so the compiler shows that nothing else
  comes out of reading a command line. `run` hands the exit to `endBeforeRunning`,
  which needs no fallback.

  The rules are:
  - each command takes the tokens up to its subcommand's name;
  - global options (`-p`, `-b`, `--simulate`, `-v`) may come at any level. The
    deepest level that gives one wins outright; counts add up only within a level;
  - a token that starts with "-" is an option, so a value that starts with "-" must
    follow "=" or come after "--". An option that is waiting for a value takes the
    next token even if it names a subcommand. `--simulate` takes a value only after
    "=", so `--simulate instant` makes "instant" an unknown command;
  - an option may be given once per level, except `--allow` and `-v`;
  - `-h` shows the summary, and `--help` the long help. `-V` exists on the root
    only. All three act as soon as the value pending at their own level is checked,
    so `-b x -h` refuses "x". Every group has the `help` subcommand;
  - a group given nothing shows its summary on standard error, with status 2. A
    group given options but no subcommand fails with "requires a subcommand";
  - an environment variable set to "" is set, so `SATLINK_PORT=` opens the empty
    path. Environment variables count as given in the checks made after reading;
  - a value attached to its option (`--x=v`, `-xv`, `--simulate`) is checked at
    once. A value in a token of its own, whether an option's or a positional
    argument's, is checked when the command line moves on: at the command's next
    argument, at the end of its tokens, or once its subcommand has been read in
    full. So help (`-b x info -h`, `-b x help`) and the subcommand's own problems
    come first. The value is still stored before the subcommand's, so the deepest
    command to give a global option wins: `--simulate=instant -b x info -b y`
    reports "y". A token that is itself a problem (an unknown option or command, a
    value too many, a value given to a flag) is reported instead of the value, as in
    `--baud x --frob`. A value still counts as given in the usage line, except for an
    option given twice, which is taken out together with its earlier occurrence. The
    `help` subcommand never checks it;
  - values are read as follows. Decimal digits after an optional "+" are numbers, so
    "010" is ten and "0x10" is not a number. Limits are `u64` with `range(1..)`.
    `u16` and `u32` values are read as `i64` and then range-checked ("-1 is not in
    0..=65535"). Possible values get suggestions above a similarity of 0.7;
  - text that is not valid UTF-8 is refused, with "invalid UTF-8 was detected in one
    or more arguments" and the command's usage line. For possible values the message
    is "invalid value". Tokens and variables are shown lossily, with U+FFFD, in
    errors and help;
  - an environment variable is checked by every command that takes its option
    without being given it, deepest first, before that command's own checks;
  - the usage line of an error about a flag's value (`--help=x`) names the command's
    argument group, `<a|b|...>`, once one of its arguments was given. A counted flag
    goes last among the arguments given, at every occurrence.

  Help and errors are styled whichever stream they go to. The palette is styled
  (`palette{styled: true}`); the zero palette is used in tests. `endBeforeRunning`
  writes them unchanged to a stream that shows styles, as decided by `showsStyles`
  from the NO_COLOR, CLICOLOR_FORCE, CLICOLOR, TERM and CI environment variables. A
  stream that shows no styles gets them stripped (`stripStyles`, `strip.go`). The
  stripping removes:
  - the sequences that ESC starts, whether they are the CLI's own styles or ones that
    a token or variable brought in (CSI, OSC, DCS, SOS, PM and APC alike);
  - the C0 control characters and DEL, other than tab, line feed, form feed and
    carriage return.

  A C1 control, which is two bytes in UTF-8 (U+009B, CSI, and U+0085, NEL, among
  them), stays with what follows it, since every UTF-8 character is kept whole.
  An unfinished sequence in a token ends at the CLI's own style, so `$'\e['` gives
  ''. Each kept run is judged from the ground state, so `$'\e[\n1m'` keeps '\n1m',
  where a terminal would take the 1 and the m into the CSI. A kept byte leaves the
  parser where it was, so `$'\e[\n\x01mz'` gives '\nz', with the m ending the CSI.
  Only a kept character of more than one byte returns the parser to the ground:
  `$'\e[\né\x01mz'` gives '\némz'. The rule table leaves out what stripping cannot
  tell apart. `TestTheParsersRulesAreAnstyleParsesAsStrippingSeesThem`
  (`strip_test.go`) checks every state and byte against the parser table that the
  test holds (`anstyleParseRules`).

  In a summary, a line break in a variable's value starts the next line at the
  help column. Long help indents that line by ten spaces.
  - Exit status is 0 on success, 1 for a failed command, and 2 for a command line
    that does not parse. An interrupted run exits with 130 (`exitInterrupted`, below).
    A panic that reaches `run` exits with 101 (`exitPanicked`).
- **net, before dialling** (`net.go`). `resolve` of an IPv4 literal (`netip.ParseAddr`
  and `Is4`) prints the address and dials nothing. Building the link configuration
  refuses a target that the link would refuse anyway. This covers a rule rather than
  a host, such as `*.example.com` or `10.0.0.0/8`, which is refused with `%q is not
  a host name or IPv4 address`. `resolve` needs `Policy.AllowsLookup(host)` or an
  IPv4 literal. `get` and `tcp` need `Policy.AllowsHost(host, port)`.

  `netArguments.linkConfig` ends with `config.Validate()`, so limits too small to be
  of use are refused, with satnet's texts, before "Dialling" is announced. A URL that
  contains a character for which `unicode.IsSpace` or `unicode.IsControl` holds would
  break the request line. Such a URL is refused as the command line is read, with
  `%q is not a valid http:// or https:// URL`. The argument parsing and the link
  configuration are functions of their own, so the tests of `net` arguments can call
  them without dialling.
- **net, interruptions** (`interrupt.go`, `interrupt_unix.go`, `interrupt_windows.go`).
  From the dial onwards (after `linkConfig`, before "Dialling"), a net call catches
  Ctrl-C, SIGTERM and SIGHUP through `os/signal`. On Windows the signals are
  `os.Interrupt` and SIGTERM. They are relayed through a channel of `signalBacklog`
  (1) to the goroutine that reacts. A SIGHUP or SIGINT that was ignored at start
  (`signal.Ignored`) stays ignored, as it is under `nohup` or in a background job,
  because `signal.Notify` would stop the ignore.

  The first interruption sets the stop request and ends a context. On that context,
  `context.AfterFunc` cancels `config.Call.Cancellation` during `establish`, and calls
  `link.RequestClose` during the subcommand. The CLI then writes "Interrupted: hanging
  up once the call allows (again to quit at once)..." on a best-effort basis. The time
  taken after that message starts `repeatsIgnoredFor` (2 s). A repeat within that
  window is part of the same request. A later repeat warns "warning: the hang-up was
  not confirmed (quitting at once), so the call may still be up and billing. End it
  with `satlink hangup`, or power-cycle the handset.", and calls `quitInterrupted`,
  which leaves the call to the DTR drop.

  An interruption during setup makes `Establish` fail with satnet's error for the
  cancellation, "handset: the call setup was cancelled". Once the link is up, an
  `AfterFunc` that has run makes the outcome `errInterrupted` ("interrupted").

  `run`'s finish flushes the log, writes "error: ..." and stops catching interruptions.
  For a run that an interruption asked to stop, it returns `exitInterrupted` (130),
  whatever the outcome. `main` then calls `dieInterrupted`. On Unix that calls
  `signal.Reset(SIGINT)` and `kill(getpid(), SIGINT)`, and exits with status 130 after
  `sigintDeliveryGrace` (1 s) if the signal did nothing, which happens when SIGINT is
  blocked or was ignored at start. This way a shell loop around the CLI stops too. On
  Windows the exit status is 130.
- **net, the call** (`netcall.go`). `establish` writes "Dialling ..." to standard
  error, and a failed write fails the command. A failed `Establish` that matches
  `satlink.ErrCallMayStillBeUp` prints the hang-up warning, without traffic details,
  before the error.

  Once the link is up, the command runs inside a function that recovers a panic and
  reports it as "internal error: the command panicked". Nothing can then skip
  `link.Close()`. The command's own work, the link's details and then the
  subcommand, runs inside that function.

  Then the call summary is written, from `Watcher().Status()`: "Call lasted %d s:
  %d bytes sent, %d received.". With `CallMayStillBeUp` it is instead "warning: the
  hang-up was not confirmed (%d bytes sent, %d received), so the call may still be up
  and billing. End it with `satlink hangup`, or power-cycle the handset."

  The result is the command's outcome when the hang-up succeeded. Otherwise it is the
  hang-up's failure, with "error: <outcome>" first when the command failed too.
- **Writes.** Writes to standard output and standard error fail the command, with exit
  status 1. The exceptions are best effort, and ignore their write errors: the call
  summary, the second "error: ...", the "Interrupted..." message, the warnings, `run`'s
  final "error: ...", and log lines. A log line must never stop the link's driver
  before it has hung up.

  A command stops at its first failed write. `status` asks the handset for the
  battery and the registration only once the lines before them are written.
  `printStatus` therefore writes each line with `writef`, so that a failed write
  stops the queries
  (`TestAStatusAsksTheHandsetNothingMoreOnceALineCannotBeWritten`, `status_test.go`).
  With SIGPIPE ignored, this holds for the binary too: a log line to a standard error
  that nobody reads is dropped, and the relay and the hang-up go on. Each such write
  is `_, _ = fmt.Fprintf(...)` with a comment that says why.

  The relay's sender does not write its errors itself. It hands them to the relay
  through a channel of one, and closes the channel. If the sender panicked, it closes
  the channel with no outcome at all. The sender ignores the errors of its
  `CloseWrite` and `Abort`, with a comment: they can fail only after `Close`, which
  comes only once the sender too is done with the connection (`sharedConnection`).
- **Panics.** `reportPanic` (`run.go`) writes "panic: <value>" and the goroutine's stack
  to standard error. It is called for three recoveries: the subcommand's work
  (`outcomeOf`), the relay's sender, and `run`. `run` recovers any other panic on its
  goroutine, once the deferred closes have run. `withHandset` defers the handset's
  close from the moment the handset exists, so a panic inside `Initialize` closes it
  too. A panic then makes `panicked` stop catching interruptions and return
  `exitPanicked`, whatever an interruption asked for.

  The relay's sender goroutine recovers its own panic. It then ends alone, instead of
  ending the process before the hang-up. The sender writes to an `outgoingStream`
  (`Write`, `SetWriteDeadline`, `CloseWrite`, `Abort`), which the tests replace with a
  recording fake.

  Interruptions are handled by an `interruptionHandler`. It is built by
  `newInterruptionHandler`, the one constructor that `catch` and the tests share, and
  it does what each interruption calls for.
- **net get** (`netget.go`). One deadline covers the request and its whole response.
  It is set once the connection is made (`exchange`):
  `conn.SetDeadline(time.Now().Add(transferTimeout))`, for TCP and TLS alike.
  `transferTimeout` is 600 s: the time allowed for an HTTP request and its whole
  response, once connected. The request is `GET <path> HTTP/1.0`, with `Host`,
  `User-Agent: satlink-cli` and `Connection: close` headers. The authority names the
  port unless it is the scheme's default (RFC 9112 §3.2).

  `readResponse` keeps what arrives. It ends at the announced end (`endingComplete`),
  because a server that keeps the connection open regardless would hold the call until
  the idle limit. It also ends at `io.EOF` (`endingClosed`), at `ErrTLSTruncated`
  (`endingTruncated`), at `ErrTimedOut` (`endingTimedOut`), and at any other error,
  which it keeps (`endingFailed`: a reset, a dropped call, or a limit reached).

  Then `link.RequestClose` is called, so that a slow reader such as a pager does not
  keep the call up. `printHTTPResponse` prints the final response, past any interim
  (1xx) responses (`finalResponse`, RFC 9110 §15.2). Its head, decoded lossily
  (`internal/utf8lossy`), goes to standard error. Its body goes to standard output.
  The body is written even when it is incomplete, or when the call failed, so that a
  partial reply can be inspected.

  A head that never ended fails with "the server closed the connection without
  replying", with "the response ended after %d bytes, before its headers did", with
  the timeout's text, or with the failure itself. A status line that is not `HTTP/`
  followed by a version, a space and three digits fails with "the reply is not an HTTP
  response".

  The expected body length is 0 for 204 and 304. Framing that the client cannot check
  is an error before the body is printed. That covers any Transfer-Encoding ("the reply
  uses Transfer-Encoding %q, which HTTP/1.0 does not allow"), and a Content-Length that
  is not just digits ("the Content-Length %q is not a length"). Field lines with the
  same name are joined with ", " (RFC 9110 §5.3) and parsed with `strconv.ParseUint`
  into a `uint64`. Field names are compared as ASCII (`asciicase.EqualFold`), past the
  status line (`header`). `announcedEnd` is the head's length plus the body's, unless
  that overflows.

  Then the following errors can be reported: "the body has %d bytes, more than the %d
  the response announced"; "the response did not arrive in full within 600 s"; the
  failure itself; "the response ended after %d of %d bytes"; or, without a length,
  "the server closed the connection without ending the TLS session, so the response
  may be cut short".
- **URLs** (`httpurl.go`). The fragment is dropped (RFC 3986 §3.5). The authority ends
  at the first '/' or '?', and the port follows the last ':'. An empty host is refused.
  A path without a leading '/' gets one (origin form, RFC 9112 §3.2.1), so
  "http://example.com?q=1#top" requests "/?q=1".
- **net tcp** (`netrelay.go`). Standard input is sent from a goroutine of its own, since
  reading it blocks. Each chunk is written with `SetWriteDeadline(time.Now().Add(sendTimeout))`,
  where `sendTimeout` is 120 s. The sender then reports its outcome. On success it
  closes our half of the connection (`CloseWrite`). On a failure to read or send
  standard input it resets the connection (`Abort`), so that the far end sees a reset,
  not a clean end of the stream, when the input was not all sent.

  The relay reads with `SetReadDeadline(now + relayCheckInterval)`, where the interval
  is 1 s. When a read times out, the relay checks the sender's outcome. A sending
  failure says why the stream ended. Any other error ends the relay.

  Once the remote end has closed, the relay waits `stdinEndGrace` (2 s) for standard
  input to end. If all of it was sent, the relay waits for `WaitAcknowledged` within
  `deliveryGrace` (30 s). If sending failed, that error is reported. If standard input
  is still being sent, the relay reports nothing at a terminal (`stdinIsTerminal`).
  Otherwise it reports `errRemoteClosedFirst`. Failures read "not all of standard input
  was sent: %w", with exit status 1.

  The connection closes gracefully once the relay and the sender are both done with
  it (`sharedConnection`). The relay's use ends when it returns, and the sender's ends
  once standard input has ended and the stream with it. Neither closes the connection
  under the other, since that would make the other's calls fail with `net.ErrClosed`.

  A relay that cannot write "Connected to ..." returns before the sender starts, and
  closes the connection at once. This is the subtest "the relay alone" in
  `netrelay_test.go`.
- **Saturating conversions.** `--idle-secs`, `--max-minutes` and `at --timeout` convert
  to `time.Duration` and saturate at `math.MaxInt64`. `--max-kb` multiplies by 1024,
  also saturating. An absurd value then fails with "limits must be at most a year"
  instead of overflowing into "limits must not be negative".
- **Logging** (`logger.go`). Log lines go through `internal/logtext`'s handler to
  standard error. Each line is printed best effort, as
  `fmt.Sprintf("[%-5s %s] %s\n", logtext.LevelName(level), pkg, text)`, from a goroutine
  of its own. The `stderrLog` backlog holds up to `logBacklog` (1000) lines, and one
  goroutine writes them. A standard error that blocks, such as a pipe to a pager nobody
  reads, therefore cannot stop the link's driver while it hangs up and enforces the
  limits. `flush` gives the lines handed over at most `flushLimit` (1 s) to be written,
  before `run`'s final "error: ...". The CLI's own lines on standard error (`writerBehind`)
  wait for the log lines handed over before them, so that the order is the causal one
  (section 3). That wait has no time limit, since standard error would block those
  writes anyway. It ends once the log is closed.
- **Tests.**
  - `commands_test.go` runs each command in-process, over the simulator
    (`--simulate=instant`, and the Iridium profile where timing matters), with fake
    handsets and fake USB ports. Its output is compared with the golden files of
    `testdata/commands` by `golden_test.go`. The hang-up warning (`net-hang-up-unconfirmed`)
    is produced at once by a `simtest.FaultyTransport` with `PanicsOnEscape`. A
    transport that is deaf to the escape and to DTR would take about 20 s with the
    CLI's default 1.5 s escape guard. The text of the call summary itself is unit-tested
    in `netcall_test.go`.
  - Golden files are updated with `go test -update` (the flag is declared in
    `golden_test.go`). The command rewrites each file in `testdata` from the command's
    output. Review the changed files before committing them. The golden directories
    are `testdata/commands`, `testdata/help`, `testdata/usage` and `testdata/style`.
  - `interface_test.go` compares help, usage errors and styles with `testdata/help`,
    `testdata/usage` and `testdata/style`. It also compares the orders of pending values
    and the grouped usage lines, with `testdata/usage` and
    `testdata/style/unneeded-value-after-a-global-option`.
  - `logger_test.go` covers the line format, the `-v` thresholds and the backlog. The
    rendering itself is covered by logtext's own tests.
  - `netinprocess_test.go` runs `net` in-process. It covers the outcomes of relay and
    get, panics during the call, interruptions, and standard error that blocks.
  - `net_integration_test.go` covers the process-level behaviour: environment variables
    that stand in for options, interruptions and their exit statuses, and a standard
    output that nobody reads.
  - Each file's own unit tests cover the rest.
  - A successful `https://` fetch runs in no test. The simulator offers its TLS echo only
    on TCP port 443, and a CLI made to trust the simulator's test CA would not test the
    public roots. Past the handshake, the fetch takes the HTTP path that the goldens run,
    over a `TLSConn`. satnet's TLS tests cover its deadline, its reads up to close_notify
    or truncation, and `Close`. `endingOf(ErrTLSTruncated)` is unit-tested, and
    `TestAnHTTPSFetchTrustsThePublicWebPKI` (`netget_test.go`) covers the trust. Only the
    `return conn, nil` in `connect` after `DialTLS` runs in no test.

## 5. Messages

These are the messages that the code produces in situations a maintainer may not
expect, with the text as the code writes it. For each one the table gives the
package, the situation, the error kind (where there is one) and the text. A dash
means the message is not wrapped in a `satlink.Error` kind. The messages that
ipstack produces are listed in IPSTACK.md, section 9.

| Package | Situation | Kind | Text |
|---|---|---|---|
| satlink | negative SMS index | `ErrInvalidArgument` | `%d is not a message index` |
| satlink | an `SMSFilter` without a constant | `ErrInvalidArgument` | `%d is not an SMS filter` |
| satlink | a `DataCall` used after `HangUp` | `ErrCall` | `the data call is over` |
| satlink | a method of a handset that a data call holds | `ErrBusy` | `the handset is in a data call` |
| ppp | a server address that is not IPv4 (the zero `Addr` included) | `ErrInvalidAddress` | `the server's addresses must be IPv4, not %v` |
| ppp | a server authentication protocol without a constant | `ErrInvalidAuthProtocol` | `%d is not an authentication protocol` |
| satnet | a negative duration in the configuration | `ErrInvalidConfig` | `durations must not be negative` |
| satnet | a negative limit | `ErrInvalidConfig` | `limits must not be negative; use 0 for no limit` |
| satnet | a host whose address is not IPv4 | `ErrInvalidConfig` | `%q in hosts needs an IPv4 address` |
| satnet | the server's key matches no pin | `ErrTLS` | `invalid peer certificate: the public key is not pinned` |
| satnet | every verified chain to a trusted root holds an RSA key under 2041 bits or an ECDSA key on P-224, or a root's own RSA key over 8192 bits. crypto/tls would refuse such a key in a server certificate, with its own text | `ErrTLS` | `invalid peer certificate: a key in the chain is RSA of under 2041 or over 8192 bits, or ECDSA on P-224` |
| satnet | a PEM section that does not decode | `ErrTLS` | `invalid PEM: a section is malformed` |
| satnet | use of a closed connection, or a call that it woke | - | `net.ErrClosed` |
| satnet | `DialContext` with another network, a malformed address, or a context that ended first | - | `net.UnknownNetworkError`, `*net.AddrError`, and `&net.OpError{Op: "dial", Net: network, Err: ctx.Err()}`, respectively |
| sim | I/O or DTR after `Close` | `satlink.ErrTransport` | `the simulated handset is closed` |
| sim | a `Hosts` entry whose address is not IPv4 (logged; the call fails with NO CARRIER) | - | `%q in hosts needs an IPv4 address` |
| sim | `Control.RestartPPP` with an address that is not IPv4 (nothing changes). The address is a `netip.Addr`, which can hold other families | `*sim.ConfigError`, wrapping ppp's `ErrInvalidAddress` | `the server's addresses must be IPv4, not %v` |
| serial | I/O or DTR after `Close` | `satlink.ErrTransport` | `the serial port is closed` |
| serial | a library write that takes nothing and reports no error | `satlink.ErrTransport` wrapping `io.ErrShortWrite` | `transport error: short write` |
| serial | a library read that panics with a runtime error on a port with a descriptor of 1024 or more | `satlink.ErrTransport` wrapping the runtime error | `transport error: the serial library cannot wait for this port (select(2) watches no descriptor from 1024 up): runtime error: ...` |
| mobile | a stream's (TCP or TLS) `maxLen` below 1 | InvalidArgument | `maxLen must be at least 1` |
| mobile | a negative `maxLen` for `UDPConnection.Receive` | InvalidArgument | `maxLen must not be negative` |
| mobile | a closed `IridiumHandset` used for any method but `Close` (`EstablishLinkWithHandset` included) | InvalidArgument | `the handset is closed` |
| mobile | a port outside 0 to 65535 | InvalidArgument | `port must be between 0 and 65535` |
| mobile | a nil settings, transport, handset, connector or sink | InvalidArgument | `%s must not be nil` |
| mobile | an unknown log level | InvalidArgument | `%d is not a log level` |
| mobile | a zero-value object from gomobile's constructor | InvalidArgument | `the %s was not made by this library` |
| mobile | a closed connection | InvalidArgument | `the connection is closed` |
| mobile | a recovered panic in a method that returns an error | Internal | `internal error: <panic>` |
| mobile | a panic recovered in an exported method without an error result (logged at error level) | - | `recovered a panic in %s: %v` |
| mobile | a panic in the link listener or in a serial port closer (logged at error level) | - | `%s panicked: %v` |
| cmd/satlink | a recovered panic on the command's goroutine, which then exits with status 101 (the message is written to standard error) | - | `panic: <value>`, then the goroutine's stack |

Notes on the wording:

- A name in a message is quoted with `internal/debugtext`, so control characters
  show as escapes, such as `\u{7f}`.
- The mobile message `%s must not be nil` names the parameter that was nil.
- The hosts message is the same in `satnet` and in `sim`.

