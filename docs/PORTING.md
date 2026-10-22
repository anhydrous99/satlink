# Porting iridium-handset to Go

This is the maintainers' guide to satlink, the Go port of the Rust workspace
`iridium-handset`. The Rust code is the specification of behaviour; this
document records how it maps to Go, the conventions every package follows,
what deliberately differs and why, and the contracts the packages keep with
one another.

The TCP/IP stack, `internal/ipstack` (the smoltcp subset), has its own notes
in [PORTING-ipstack.md](PORTING-ipstack.md).

This guide follows the Rust workspace as of its snapshot 4 (October 2026).
Snapshot 2 reworked hanging up, gave satnet's TLS stream writes that
survive a timeout, and rewrote much of the CLI's `net` command. Snapshot 3
added:

- in the core and serial packages: call-setup cancellation
  (`satlink.Cancellation`), the error kinds `ErrCallMayStillBeUp` and
  `ErrCancelled`, `+CPAS` in `Initialize`, a voice `Answer` that ends a
  data call that may have come up unseen, the 10 s cap on hang-up waits,
  and a serial read that waits out EINTR;
- in satnet: a device that sends small, awaited datagrams at once and lets
  the flows take turns at the line, a segment each, dropping retransmissions
  answered while they waited; connection outboxes, connections cut off by an
  address change, and acknowledgements passed on to the device; lookups
  that tell a server's answer from silence; cancellation while PPP comes up;
  `Config.Validate` and larger minimum limits; `Abort` on TCP and TLS
  connections, a TLS `send_until` (Go's `WriteSome`, which TCP connections
  now have too), and TLS writes refused after the goodbye or once TCP no
  longer sends;
- in sim and `internal/simtest`: the network's DNS servers and silent
  addresses, PPP restarted by the network (`Control.RestartPPP`), `+CPAS`,
  a web server that resets what arrives after it closed, and new transport
  faults;
- in `cmd/satlink`: Ctrl-C, SIGTERM and SIGHUP cancel the call setup or hang
  up, and the CLI then ends by SIGINT; the log writes from a goroutine of
  its own; limits and URLs are checked before dialling; `net get` reads to
  the announced end and hangs up before writing out; `net tcp` fails when
  standard input could not all be delivered;
- in mobile and the Kotlin sources: the `Cancellation` object and
  `SatlinkErrorCancelled`, the handset's `ErrCallMayStillBeUp` as
  `SatlinkErrorHangUpFailed`, which strands an `IridiumHandset`,
  `Reinitialize` ending a call held in online-command mode, and `SendSome`
  and `Abort` on TCP and TLS connections.

Snapshot 4 added a stack test, which acknowledgements counted in a poll
that also hands data over, and narrowed which connections an address change
cuts off to those with data in smoltcp's send buffer. The port keeps
counting the outbox too, as snapshot 3 did: section 3 says why.

Rust line references such as `data_call.rs:181-187` are to snapshot 4.

The port is complete. Every package is implemented and every Rust test is
ported; section 2 lists the few whose shape had to change. Go-only
behaviour has tests of its own. The exported API was first written as a
skeleton, whose doc comments were the contract between the people who
implemented the packages in parallel. It held, and the doc comments remain
the contract: change an exported signature only when the behaviour cannot
be kept otherwise, and say so prominently.

Contents:

1. [File map](#1-file-map)
2. [Conventions](#2-conventions)
3. [Known differences from the Rust version](#3-known-differences-from-the-rust-version)
4. [Implementation notes per package](#4-implementation-notes-per-package)
5. [Messages that exist only in Go](#5-messages-that-exist-only-in-go)

---

## 1. File map

Legend: **API** — the file declares exported API; **internal** — unexported
code only; **Go only** — no Rust counterpart. Rust unit tests (`mod tests`,
`endpoint/tests.rs`, `modem/tests.rs`) are internal tests, `<file>_test.go`
in the package itself. Rust integration tests (`tests/*.rs`) and their
`support` modules are external tests, package `<name>_test`, named
`<name>_integration_test.go`, using only the exported API (the sim and
satnet tests may also import `internal/ipstack`, which the module allows).
The Go-only tests that section 2 requires are listed here too.

### Workspace

| Rust | Go | Notes |
|---|---|---|
| `Cargo.toml`, `Cargo.lock` | `go.mod`, `go.sum` | Never edit them by hand, never run `go get`, `go mod tidy` or `-mod=mod` builds: the default read-only mode builds every approved import as is. `go.mod` also pins the `gobind` and `gomobile` tools. |
| `.gitignore` | `.gitignore` | The `satlink` binary that `go build ./cmd/satlink` leaves at the top, what the scripts write (`android/build/`, `build/`, `*.aar`, `*.xcframework`), coverage profiles. |
| `README.md` | `README.md` | Rewritten for Go: `go install ./cmd/satlink` and the CLI, with `--simulate` examples; the library in brief; gomobile builds; the differences of section 3, RFC 8879 first; testing. |
| `crates/uniffi-bindgen/*` | — | Replaced by the `gobind` and `gomobile` tool dependencies in `go.mod`. |
| `android/src/main/java/iridium/handset/AndroidUsbTransport.kt` | `android/src/main/java/io/github/anhydrous99/satlink/AndroidUsbTransport.kt` | Package `io.github.anhydrous99.satlink`, importing `io.github.anhydrous99.satlink.mobile.*` (gomobile appends the Go package name to `-javapkg`). Implements `mobile.ByteTransport`: `write`, `read(maxLen: Long, timeoutMillis: Long)` (the timeout fits the `Int` of `bulkTransfer`), `raiseDTR` (DTR+RTS), `dropDTR` (RTS only); still `AutoCloseable`, dropping all lines on close. Throws plain `Exception` (gomobile has no exception subclasses). As in the Rust snapshot: `open` claims the interfaces, sets the line coding (`setLineCoding`, failures ignored) and pulses DTR inside one `try`, whose `catch (e: Throwable)` releases every claimed interface, closes the connection and rethrows — the device never stays claimed; the docs of `open` and `close` say that a DTR drop should end a data call (`AT&D2`) and, with the default `+CVHU=2`, a voice call, not yet verified on hardware over USB, and, as in snapshot 3, that initialising the handset, as `IridiumHandset` and `Mobile.establishLink` do, ends a data call still up — online, or held in online-command mode, which `AT+CPAS` reveals — or throws an exception whose `Mobile.asSatlinkError(e)?.kind` is `Mobile.SatlinkErrorHangUpFailed`, and that the handset's `hangUp` ends one its firmware cannot report. |
| `android/src/main/java/iridium/handset/Example.kt` | `android/src/main/java/io/github/anhydrous99/satlink/Example.kt` | The gomobile API, importing `io.github.anhydrous99.satlink.mobile.*`: `LinkSettings()` then `allow(…)`; `fetchOverSatellite(context, cancellation: Cancellation = Cancellation())` calls `Mobile.establishLink(transport, settings, null, cancellation)`, and `post` (the link of `postThenText`) `Mobile.establishLinkWithHandset(handset, settings, null, null)`; each wraps its link in `try { … } catch (e: Exception)`, where `mayHaveLeftTheCallUp(e)` (`when (Mobile.asSatlinkError(e)?.kind) { Mobile.SatlinkErrorHangUpFailed, Mobile.SatlinkErrorInternal -> true; else -> false }`, Rust's `catch (e: IridiumException.HangUpFailed)` extended to `SatlinkErrorInternal`, after which whether a call is up is unknown; Rust's Example lets UniFFI's `InternalException` pass, which leaves the call up just the same) tries to end the call — `post` with `handset.reinitialize()`, `fetchOverSatellite` with `hangUpThrough(transport)`, a new `IridiumHandset` on the transport, still open, whose `hangUp()` ends it — inside `runCatching`, adds what that threw with `addSuppressed`, and rethrows; one connector for the whole app, `private val tlsConnector: TLSConnector by lazy { TLSConnector(TLSSettings()) }` (resumption: ~1 KB of airtime instead of 3–5 KB); `try { … } finally { x.close() }` on every gomobile object but links, instead of `.use {}`; around each link's work `try { … } finally { hangUp(link) }`, with `private fun hangUp(link: SatelliteLink) { link.hangUp(120_000) }` documented as the Rust one (end the call even after an exception, while the transport is still open; allow for the 30 s close grace and the hang-up; throws when the call may still be up). Links are not closed after `hangUp`: `SatelliteLink.Close` only requests the close that `HangUp` has already requested. `readResponse` loops on `val received = connection.receive(4096, 120_000)` until `received.finished`, writing `received.data`; `Mobile.asSatlinkError(e)?.kind == Mobile.SatlinkErrorTLSTruncated` instead of catching `IridiumException.TlsTruncated`. The file's header warns that gomobile objects do not compare by identity (section 3): compare them with `===`, and keep them in identity-based collections, never in a `HashSet`, as map keys or, without a wrapper, in a `StateFlow`. |
| `android/src/main/res/xml/iridium_device_filter.xml` | same path and name | A hardware fact (Iridium's product IDs); unchanged. |
| `linux/99-iridium-handset.rules` | `linux/99-satlink.rules` | Same rules; the `/dev/iridium%n` symlink names the hardware and stays. |
| `scripts/build-android.sh` | `scripts/build-android.sh` | gomobile looks `gobind` up on `PATH`, `go tool` puts no tool there, and `gomobile init` would fetch an unpinned one; so the script builds the `go.mod`-pinned binary first: `tools=$(mktemp -d); go build -o "$tools/gobind" golang.org/x/mobile/cmd/gobind; PATH="$tools:$PATH" go tool gomobile bind -target=android/arm,android/arm64,android/amd64 -androidapi 21 -javapkg io.github.anhydrous99.satlink -o "$OUT/satlink.aar" ./mobile`. The ABIs are the Rust build's (armeabi-v7a, arm64-v8a, x86_64), not gomobile's default four; API level 21, cargo-ndk's default for the Rust build, is needed because gomobile's default, 16, is below what current NDKs ship. Prerequisites: the Android SDK (`ANDROID_HOME`, for `android.jar`), the NDK (`ANDROID_NDK_HOME`) and a JDK. `$OUT` is the first argument, `android/build/generated` by default. Desktop serial is excluded by build constraint. |
| `scripts/build-bindings.sh` | `scripts/build-bindings.sh` | The same pinned `gobind`, then `PATH="$tools:$PATH" go tool gomobile bind -target=macos -prefix Satlink -o "$OUT/Satlink.xcframework" ./mobile`, `$OUT` being the first argument, `build/bindings` by default; then `scripts/mark-unusable-inits.sh` on every `SatlinkMobile.objc.h` that `find -type f` finds under `$OUT/Satlink.xcframework`, failing if none is found. The script states its prerequisite, a full Xcode: gomobile refuses the Command Line Tools alone. gomobile has no Kotlin/JVM desktop target (section 3). |
| — | `scripts/mark-unusable-inits.sh` | Go only: marks NSObject's `init` and `new` unavailable in the gobind classes that declare no argument-less `init` — those whose Go constructor takes arguments, for which gobind writes only an `init:` that takes them, and those that stand for a Go implementation of an interface (section 4, mobile, Zero values); one awk pass per header; keeps the file's permissions; idempotent; fails, leaving the header unchanged, on a header of another shape. |

### iridium-core → package `satlink` (module root)

| Rust | Go | |
|---|---|---|
| `Cargo.toml` | — | `thiserror` → hand-written `Error()`. |
| `src/lib.rs` | `doc.go` | API |
| `src/transport.rs` | `transport.go` | API: `Transport`, `DTRController`. `Transport`'s doc states what `Handset.Close` relies on: the `Close` of an `io.Closer` transport must be safe while `ReadTimeout` or `WriteAll` runs in another goroutine, and make them fail rather than act on what it closed (sim's `Handset` and serial's `Transport` keep it; mobile's adapter is no closer). Internal: the handset's own reads, writes and DTR changes — `write`, `read`, `raiseDTR`, `dropDTR`, `dtrLine`, `noDTRLine` — which a data call uses without the lock. |
| `src/cancellation.rs` | `cancellation.go`, `cancellation_test.go` | API: `Cancellation` (`NewCancellation`, `Cancel`, `Cancelled`), which `DataCallSettings.Cancellation` holds (section 2, Cancellation). The Rust test `clones_share_one_request`: copies of `DataCallSettings` share one request, and another call's settings are not cancelled. Go only: the zero value is ready, and a nil one is never cancelled. |
| `src/error.rs` | `error.go`, `error_test.go` | API: `Error` (with `CallMayStillBeUp`) and `ErrorKind` with its sentinels, among them `ErrCallMayStillBeUp` (Rust's `Error::CallMayStillBeUp`, also matching the flag) and `ErrCancelled` (`Error::Cancelled`). Internal: `noCarrierError` and `isNoCarrier` (Rust's `no_carrier`, `is_no_carrier`). Go-only tests: the labels as a table; texts; `errors.Is` for the kinds, for `ErrCallMayStillBeUp` with the flag set and not, its kind with and without the flag, and through `fmt.Errorf("…: %w")`; `Unwrap`. |
| `src/at.rs` | `at.go`, `at_test.go` | API: `FinalResult`, `FinalResultKind`, `SplitCSV`. Internal: final-result classification, `responsePrefix`, URC detection, the line buffer (text through `internal/utf8lossy`; ASCII-only case through `internal/asciicase`: the echo, `responsePrefix`, `mayGoOnline`, as Rust's `eq_ignore_ascii_case` and `to_ascii_uppercase`), the numeric-field parser and `debugList` (section 4). |
| `src/deadline.rs` | `deadline.go`, `deadline_test.go` | Internal: `deadlineAfter`, `timeToWait`, `longestRead` (section 4). The 2 Rust tests. |
| `src/handset.rs` | `handset.go`, `busy.go`, `exchange.go`, `synchronize.go`, `voicecall.go`, `callstate.go`, `sms.go`; `handset_test.go`, `busy_test.go`, `exchange_test.go` | API: `Handset` and its records (`handset.go`, with the unexported `hangUpTimeout`). Internal: the lock and the claim — `exchange`, `perform`, `lock`, `claim`, `release` (`busy.go`, Go only: section 4, Handset internals); command execution, `commandWithTimeout` and the cancellable wait — `request.cancellation`, `commandRun.sentAt`, `cancellableWait`, `completion.endsWith` (`exchange.go`; the handset's reads and writes are `transport.go`'s); `Initialize` — `synchronizeEndingACallLeftUp`, `endCallHeldInCommandMode`, `configure` — AT synchronisation, resynchronising and draining (`synchronize.go`); voice calls: `dial`, `answer` with `abortAnswer` and `endAnsweredDataCall`, and `hangUp` (`voicecall.go`); which call the handset is in, `isInDataCall` (`+CPAS`) and `isInVoiceCall` (`+CLCC`) (`callstate.go`); SMS, with the +CMGL and +CMGR parsing (`sms.go`). Internal tests: the records' names, `mayGoOnline`, `validateNumber` and the quoting of a number it refuses, the +CREG codes, and the cap of the hang-up waits, the command timeout but at most ten seconds, written as literals (`TestAHangUpWaitsForTheCommandTimeoutButAtMostTenSeconds`) (`handset_test.go`); a method that waited for the lock while a dial claimed the handset fails with `ErrBusy`, writing nothing (`TestAMethodThatWaitedForTheLockWhileADialClaimedTheHandsetIsBusy`, which holds `h.mu`, waits until `runtime.Stack` shows the test's goroutine parked in `(*Handset).lock`, then claims and unlocks, as a connected dial leaves the handset; `busy_test.go`); text entry that times out is left with ESC, tested through `exec` with a short timeout, as `SendSMS` always waits two minutes (`exchange_test.go`). |
| `src/data_call.rs` | `datacall.go`, `hangup.go`, `datacall_test.go` | API: `DefaultEscapeGuard`, `MaxEscapeGuard`, `BearerService`, `DataCallSettings`, `DataCall`, `Handset.DialData`/`AnswerData`/`EndDataCall` (`datacall.go`). Internal: the escape, `ATH`, the DTR fallback, the DTR pulse that a transport's panic sets off (`pulseDTR`, `pulseDTRUnless`; section 4, Hanging up), `awaitOwnResult` and `discardUntil` (`hangup.go`). Also internal, in `datacall.go`: setting a call up — `setUpCall`, `prepareAndStart` (Rust's `Setup::run`) with `preparationFailed`, `dialData`, `answerData` with `hangUpVoiceCall`, `callRequest`, `isUnfinished`, `abortAttempt` and `abortCallAttempt`, `endAbandonedCall`. `CallFailure` has no Go type: its `call_may_still_be_up` is `Error.CallMayStillBeUp` (section 2), and Rust's `From<CallFailure> for Error`, which turns a failure whose call may still be up into `Error::CallMayStillBeUp` for `?`, has no Go form either: a Go error passed on keeps its flag and its text. The 4 Rust unit tests, `an_escape_guard_beyond_any_handsets_is_refused` included; `datacall_test.go`'s `debugDuration` cases include 0 → "0ns" and 1.5 µs. |
| `src/device.rs` | `device.go`, `device_test.go` | API: `IridiumUSBVendorID`, `Product`, `KnownProducts`, `ModelForProductID`. Go-only test: `KnownProducts` hands out a copy. |
| `tests/support/mod.rs` | `support_integration_test.go` | The scripted transport: implements `Transport` and `DTRController`, records `history`, every write with its time (`writtenAt(data) (time.Time, bool)`: when `data` was first written in one piece), each DTR change with its time (`dtrChange{at, raised}`, returned by `dtrChangesSoFar`) and unplayed steps, all guarded by a mutex (`Handset.Close` runs while a read is in progress). Its replies come at once, as Rust's do, unless a Go-only test makes them late. `answersLate(write, delay)` makes the reply of the first step still to play that writes `write` come `delay` after that write. `unsolicitedAfter(delay, data)` sends data unprompted, `delay` from now; `unsolicited(data)` is `unsolicitedAfter(0, data)`. No reply arrives before one sent ahead of it. `ReadTimeout` decides at its start whether the next reply arrives within its timeout: it then sleeps until that reply arrives and delivers it, or waits out the timeout and returns 0. A slow machine can therefore make a reply arrive sooner than the handset expects, never later. With no reply on its way, it naps `min(timeout, 1 ms)`, sparing the CPU in long waits such as the ten-second abort grace. A separate blocking transport that implements `io.Closer` serves the tests of `Close` and `SetTimeout` during a dial. |
| `tests/handset.rs` | `handset_integration_test.go` | The `configuration()` steps end `Initialize` (`ATE0`, `ATV1`, `AT+CMEE=2`, `AT+CMGF=1`); `readyStatusReply` and `dataCallStatusReply` answer `AT+CPAS` (000 and 004). Plus, Go only: `TestInitializeEndsACallLeftUpWhoseFarEndNeverPauses`, over `callUntil` (below; the first drain's 2 s cap lets the escape end the call, about 11 s, parallel); `TestInitializeLeavesTextEntryFirst` (ESC CR before the first AT); `TestTheEchoIsNoInformationLine`; `TestALineOnlyUnicodeFoldsToTheCommandIsNoEcho` (the echo is matched ASCII-only); `TestALineLeftAfterAnAnswerIsNotTheNextCommands` (exec queues complete lines already received as URCs; the 7-byte reply puts the stray line in the line buffer); and the waits that keep a late answer from hiding a call: `TestALateAnswerArrivingDuringInitializeDoesNotHideAHeldCall` (an OK 250 ms into `Initialize`, within the first drain's 300 ms of quiet: the call held in online-command mode is still found and hung up), `TestAPartialAnswerLeftBeforeInitializeDoesNotHideAHeldCall` (the drain also clears the line buffer), `TestWhatTheHandsetSaidBeforeInitializeIsDropped`, `TestAnATAnsweredASecondLateStillSynchronises` (each AT is given two seconds), `TestAZeroTimeoutFailsACommandWithoutWaitingForItsAnswer`, `TestALateAnswerThatKeepsComingDoesNotHideAHeldCall`, `TestPollURCsReturnsAsSoonAsOneArrives`, `TestFirmwareRefusingTheOptionalSettingsIsStillInitialised`, `TestFirmwareRefusingARequiredSettingIsNotInitialised` (ATE0 or ATV1 answered with ERROR fails `Initialize` with `ErrCommandFailed` for that command, as Rust's `?` does, and nothing is sent after it) and `TestACommandWithoutADeadlineReadsASecondAtATime` (section 4, Stale input). |
| `tests/data_call.rs` | `datacall_integration_test.go`, `cancellation_integration_test.go` | The four cancellation tests are in `cancellation_integration_test.go`, where Rust's `CancelsOn` is `cancelsOn`, a scripted transport that calls a cancel function the moment a command is written, so that a context's can be used too. Rust's `Hiccup` is `hiccupAfter(trigger, script)`, which fails one read with `errUSBHiccup` each time a write starts with `trigger`; Go only, `andAfter(trigger)` adds a trigger, so that the reads after ATA and after the abort can both fail. The endless-call transport of `a_handset_still_relaying_data_after_dtr_drops_is_reported_promptly` (`endlessCall`) is deaf to the escape and to DTR — `RaiseDTR` a no-op, `DropDTR` only noted — and returns one PPP flag per 10 ms read for 8 s, then nothing; Go only, it notes what is written once DTR has dropped, and the test also asserts that DTR was dropped and that nothing, no ATH, was written after it: two seconds of data end the hang-up first. Go only: `callUntil(stop, script)` (type `callLeftUp`), a call left up whose far end sends without a pause until a write holds `stop`: `+++`, or the next command once the far end has fallen silent; `withoutDTR`, which wraps any `satlink.Transport` and so hides a DTR line; a context that cancels a dial through `context.AfterFunc`, and settings without a `Cancellation`. The tests of a wait between two writes assert it with `assertWaitedBetween(t, transport, earlier, later, atLeast)`: the escape-guard tests, `TestATHWaitsOutTheGuardAfterAnEarlyOK` and `TestACancellationJustAfterTheDialCommandWaitsToAbort`. Go-only tests: `TestAnUnansweredATHAfterAConfirmedEscapeStillReachesTheDTRDrop` (the 10 s cap on command-mode ATH after a confirmed escape; about 10 s, parallel), `TestTheEscapeKeepsTheSettingsGuardOfSilence`, `TestTheEscapeCountsItsGuardOfSilenceFromTheLastWrite` (a call that writes 300 ms after CONNECT keeps a guard of silence after that write before `+++`), `TestACallThatConnectsAfterTheTimeoutIsEndedWithTheSettingsGuard` and `TestTheEscapeKeepsAGuardOfSilenceAfterConnect`; the handset's own answers that confirm an escape or a raw ATH: `TestNoCarrierConfirmsTheEscape` (`+++` answered with NO CARRIER: one escape; its 100 ms guard keeps the three-guard window open on a slow machine) and `TestNoCarrierConfirmsTheATHAfterAnUnconfirmedEscape` (a raw ATH answered with NO CARRIER, DTR untouched), and the one that must not: `TestTheAnswerToADTRDropDoesNotPassForATHs` (a handset deaf to the escape answers the DTR drop NO CARRIER, as after AT&D2, or OK, as after AT&D1; the resynchronisation after the drop drops that answer, so the raw ATH is confirmed by its own OK and AT+CSQ gets its own answer; ATH's OK comes half a command timeout late, so that it is never read together with the drop's answer); `TestEndDataCallThatFailsSaysTheCallMayStillBeUp` (over a handset deaf to everything, `ErrTimeout` "+++" with `CallMayStillBeUp`; not skipped under `-short`); the waits that keep a late answer from passing for the next command's: `TestALateAnswerArrivingDuringAnswerDoesNotPassForATAs` and `TestALateAnswerArrivingWhileADialIsPreparedDoesNotDecideIt` (a late answer 150 ms in, within the 200 ms of quiet that drops late answers), `TestAPartialAnswerLeftBeforeAnswerDoesNotPassForATAs`, `TestAPartialAnswerLeftBeforeADialDoesNotDecideIt`, `TestTheDataOfACallLeftUpAreNoPartOfTheNextAnswer`, `TestALateAnswerThatKeepsComingIsNoCallLeftUp`, `TestAVoiceCallWhoseAbortWasAnsweredLateIsKept`, `TestURCsDuringTheAbortAreQueuedAndOtherLinesDropped`, `TestAnEscapeConfirmedLateButWithinThreeGuardsIsNotRepeated` (an OK two and a half guards after `+++`), `TestAnAbortsResultArrivingSecondsLaterStillEndsTheAttempt` (NO CARRIER three seconds after the abort byte) and `TestTheDataOfACallEndedAgainAreNoURCs`. The DTR-fallback tests assert with `assertHungUpByDroppingDTR` that DTR stayed down at least 150 ms and came up again: `TestAHandsetDeafToTheEscapeIsHungUpByDroppingDTR`, `TestAnOKLineInTheFarEndsDataDoesNotEndTheHangUp`, `TestAHangUpWithoutACommandDeadlineStillReachesTheDTRDrop` and `TestAnUnansweredATHAfterAConfirmedEscapeStillReachesTheDTRDrop`. In `cancellation_integration_test.go`, Go only: `readTimeouts`, a transport that records the timeout of every read while a command waits for its answer, and `TestADialLooksAtItsCancellationEveryFifthOfASecond`: no read of a cancellable dial waits longer than 200 ms; and `TestACancelledAnswerIsAborted`: `AnswerData` cancelled as ATA goes out (`cancelsOn` with trigger "ATA", `patientSettings`) — the abort ends the attempt, or a call that connects anyway is ended; either way `ErrCancelled`, with `CallMayStillBeUp` false. Rust's cancellation tests cancel dials only. |
| — | `busy_integration_test.go` | Go only: `ErrBusy` and the release on every path out of a call — a transport that panics during `HangUp` or the dial included — `TakeURCs` empty while busy, `SetTimeout` and `Close` not blocked by a dial, and concurrent callers taking turns (`TestConcurrentCallsTakeTurns`: eight goroutines × five `SignalQuality` and `Battery` calls over the `answering` transport, which answers a byte per read, under `-race`). A panic out of a dial or a hang-up pulses DTR before it goes on (`TestADialThatPanicsPulsesDTRBeforePassingThePanicOn`, `TestAHangUpThatPanicsPulsesDTRBeforePassingThePanicOn`: DTR down at least 150 ms, then up, and the transport's panic value reaching the caller), under the lock, before the handset is released (`TestADialThatPanicsPulsesDTRBeforeReleasingTheHandset`, `TestAHangUpThatPanicsPulsesDTRBeforeReleasingTheHandset`, one per site, each of which catches its site's regression alone: over `pulseWitness`, made by `newWitnessedHandset`, a `panicsOnWrite` whose `DropDTR` has another goroutine send `Command("AT")`, bounded by `promptly`, which fails with `ErrBusy` and writes nothing while DTR is down); a pulse that panics too leaves the first panic to go on (`TestAPulseThatPanicsTooLeavesTheFirstPanicToGoOn`), and so does a transport without DTR (`TestAHangUpThatPanicsOverATransportWithoutDTRPassesThePanicOn`, over `withoutDTR{panicsOnWrite{…}}`). |
| — | `portability_test.go` | Go only, for the whole module: every package and its tests type-check for linux/386 and linux/arm, where `int` has 32 bits, and for freebsd/amd64, openbsd/amd64, windows/amd64 and android/arm64, whose files of their own (terminals, interruptions, close-on-exec) the host never compiles (`go vet ./...` with `GOOS`, `GOARCH` and `CGO_ENABLED=0`; skipped under `-short`). |

### iridium-ppp → package `ppp`

| Rust | Go | |
|---|---|---|
| `Cargo.toml` | — | `md-5` → `crypto/md5`; `log` unused. |
| `src/lib.rs` | `ppp/doc.go`, `ppp/example_test.go` | The doc example is `ExampleEndpoint`. Its bytes carry a random LCP magic number, so it prints a deterministic property — the output is one flag-delimited LCP frame — never the bytes. |
| `src/config.rs` | `ppp/config.go`, `config_test.go` | API: `DefaultMRU`, `MinMRU`, `Timers`, `Keepalive`, `Credentials`, `AuthProtocol`, `Authentication`, `ServerConfig`, `Role`, `Config`, `ConfigError`. The redaction test is a table (section 4, Redaction); Go only, `%+v` names the fields of `Credentials`, `ServerConfig` and `Config` (`TestPlusVNamesTheFieldsOfWhatHasFieldsToName`). |
| `src/endpoint.rs` | `ppp/endpoint.go`, `ppp/keepalive.go`, `ppp/deadline.go`; `keepalive_test.go` | API: `Phase`, `IPv4Settings`, `Event`, `DownReason`, `SendError`, `Stats`, `Endpoint` (`endpoint.go`). Internal: the keepalive prober and the endpoint's LCP echo handling — probes, echo replies, loopback detection from returned probes (`keepalive.go`); the timer arithmetic `isDue`, `earliest` and the saturating `scaled` (`deadline.go`, with `deadline_test.go`). `keepalive_test.go`, Go only: probes, the endpoint's copy of the keepalive, echo replies and their MRU, loopback from our own probes at the third, probes without a magic number once ours is refused, a busy link not probed, no probe while LCP renegotiates, traffic reaching a dead link starting no probes, and abandoned links: one Terminate-Request as they go (a table of the ways a link is abandoned), dead until opened again. |
| `src/endpoint/tests.rs` | `ppp/endpoint_test.go` | Events compare with `Event.Equal`. Beside it, Go only, split by concept: `endpoint_api_test.go` (names and texts, comparisons, copies, the IPv4 settings, datagrams, output, statistics and the 64-datagram queue); `endpoint_lifecycle_test.go` (`Open` in every phase, `Close`, reopening, every way a link goes down for good and the contract a dead link keeps, `NextTimeout` with IPCP's timer and an owner that follows it, a close during LCP negotiation not undone by a crossing Nak); `endpoint_framing_test.go` (the MRU limiting each end and the 1500-byte receive floor, each end escaping what the other asked for, header compression, framing as LCP agreed, LCP always conservatively framed); `endpoint_rejects_test.go` (discarded packets, which draw no answer and hand the IP layer nothing, Protocol-Rejects sent and received, and IPv4 arriving while IPCP negotiates, dropped even once the link opens: `TestADatagramThatArrivesWhileIPCPNegotiatesIsDiscarded`, RFC 1661 §3.6); `endpoint_lcp_test.go` (the LCP codes on the wire against RFC 1661's numbering, answers to unusual and hostile requests — a hostile request at MRU 65535, a request with an option to reject drawing only the Reject, a Nak and a Reject that keep the request's options in its order, every instance of a rejected option included (`TestANakOrRejectKeepsTheOptionsInTheRequestsOrder`, against literals: an LCP client's Reject and Nak and a server's IPCP Nak, the requests not in ascending order of type) — loops, a stray Terminate-Ack, whichever end restarts first (`TestALinkRecoversFromAStrayTerminateAckAtTheNextRestart`: the two restarts crossing, and the server's retransmission first), and the new identifiers the endpoint numbers its Echo-Requests, Code-Rejects and Protocol-Rejects with (`TestEveryEchoRequestCodeRejectAndProtocolRejectTakesANewIdentifier`)); `endpoint_ipcp_test.go` (IPCP at the endpoint, its Nak tolerance: 20 accepted, the 21st ends the link); `endpoint_auth_test.go` (the authentication phase at the endpoint: a restart of LCP ending it, a Protocol-Reject of PAP or CHAP ending the link (`TestAPeerThatRejectsTheAuthenticationProtocolEndsTheLink`), a packet of the other authentication protocol discarded unseen by the running exchange, though its code would mean something there (`TestAuthenticationPacketsOfTheOtherProtocolAreDiscarded`, over `authenticatingClient`), an authenticated link whose exchange runs no timer once it has decided, a lost CHAP challenge sent again after one Restart); and `support_test.go`, every helper used by more than one test file (section 4, Internal design). The ported files also hold Go-only tests of what the Rust tests leave out; among those that pin RFC 1661's rules and the differences of section 3: in `fsm_test.go`, `TestAnAnswerToOurRequestRestartsTheRetryCount` (a table over Configure-Ack, -Nak and -Reject: an answer that comes with one retry left is followed by Max-Configure requests before the timeout; the Reject case over `yieldingNegotiator`, a `simpleNegotiator` that accepts every Reject), `TestAnAnswerToAnAcknowledgedRequestIsIgnored` (an Ack, Nak or Reject carrying the acknowledged request's identifier, in Ack-Received and in Opened, changes nothing and sends nothing: the `!requestAnswered` guard that `requestAgain` relies on), `TestAnUnknownCodeDrawsOnlyACodeReject` (an RFC 2153 Vendor-Specific packet, whose magic number begins with a Terminate-Request's code, draws a Code-Reject on the automaton's own protocol), `TestACloseIsNotUndoneByAnAnswerCrossingIt`, `TestARepeatedOptionDrawsOneSuggestion` and `TestATerminateAckAfterOurRequestWasAcknowledgedStartsOver` (the restart timeout finding Request-Sent, "the peer asking after our restart", or Ack-Sent, "before our restart"; helpers `afterAStrayTerminateAck` and `assertAFreshRequestAtTheRestart`), the last two for section 3; the ported `TestIgnoresDuplicateAndMismatchedAcks` also sends an Ack of the request's length with another value; and in `auth_test.go`, `TestACHAPAuthenticatorSendsMaxConfigureChallenges` and `TestAnAuthenticatorGivesUpWhenThePeerNeverAuthenticates` (exactly Restart × Max-Configure after the start, a PAP authenticator waiting for the peer's request and a CHAP one). |
| `src/hdlc.rs` | `ppp/hdlc.go`, `hdlc_test.go` | API: `DefaultACCM`. Internal: `frameEncoding`, `frameDecoder` and `frame`. The Rust test `drops_control_characters_the_peer_should_have_escaped` is the first case of the table `TestDropsControlCharactersThePeerShouldHaveEscaped`, whose Go-only second case puts the XON inside an escape sequence: a control octet the receiving ACCM flags is removed before the Control Escape applies (RFC 1662 §4.2). |
| `src/auth.rs` | `ppp/auth.go`, `auth_test.go` | Internal. The peer's messages are read through `internal/utf8lossy`. The PAP tables include fields that their length octets overrun — a Nak's message, a request's password — which give an empty message and a refusal, as Rust's `pap_message` and `accepts_pap_request` do. |
| `src/fcs.rs` | `ppp/fcs.go`, `fcs_test.go` | internal |
| `src/fsm.rs` | `ppp/fsm.go`, `fsm_test.go` | internal |
| `src/ipcp.rs` | `ppp/ipcp.go`, `ipcp_test.go` | internal |
| `src/lcp.rs` | `ppp/lcp.go`, `lcp_test.go` | internal |
| `src/packet.rs` | `ppp/packet.go`, `packet_test.go` | internal |
| `src/random.rs` | `ppp/random.go`, `random_test.go` | internal, `crypto/rand` |

Rust's fuzzed input becomes Go fuzz targets: `FuzzParsePacket` and
`FuzzParseOptionList` (`packet_test.go`), `FuzzFrameDecoder` and
`FuzzFrameRoundTrip` (`hdlc_test.go`), `FuzzEndpointHandleInput`
(`endpoint_test.go`). `FuzzEndpointHandleInput` fuzzes whether the server
demands CHAP-MD5 or PAP (an endpoint hands a PAP or CHAP packet only to an
exchange of that protocol, so over one link the other protocol's packets
meet only the discard), the steps before the attack, an index into
`hostileProtocols` — the protocols `TestSurvivesArbitraryInputInEveryPhase`
sends: LCP, PAP, CHAP, IPCP, IPv4 and an unknown one — and the packet's
information, and frames the packet, so that every mutation passes the FCS
and reaches the protocols; raw line bytes are `FuzzFrameDecoder`'s. Its PAP
seeds run over a PAP link: a Nak at step 1, where the client's exchange
waits for the verdict, and a request at step 2, where the server's takes
it. It takes no second `[]byte` for line noise:
Go's fuzzer makes two `[]byte` arguments share one backing array within a
chain of mutations, so the noise would overwrite the packet. `go test` runs
their seed corpora; run one with
`go test -run='^$' -fuzz='^FuzzFrameDecoder$' -fuzztime=30s ./ppp`.

### iridium-net → package `satnet`

| Rust | Go | |
|---|---|---|
| `Cargo.toml` | — | Feature `tls` is always on (crypto/tls); `smoltcp` → `internal/ipstack`. |
| `src/lib.rs` | `satnet/doc.go`, `satnet/example_test.go` | The `no_run` doc example is an `Example` without an output comment (compiled, not run). |
| `src/config.rs` | `satnet/config.go`, `config_test.go` | API: `IridiumInternetNumber`, `IridiumInternetBearer`, `Credentials`, `Config` with `Validate` (Rust's `validate`, public since snapshot 3; `Config`'s methods have value receivers, so that `satnet.NewConfig(policy).Validate()` works), `NewConfig`, `Limits`, `Tuning` and their defaults. The 4 Rust tests; Go only, the PPP client's configuration: the receive ACCM of `link.rs:413`, spelled out bit by bit (`TestPPPAsksTheGatewayToEscapeCRAndTheFlowControlCharacters`), and an MRU that is the MTU, 576 bytes by default (`TestPPPTakesPacketsOfUpToTheMTU`). |
| `src/error.rs` | `satnet/error.go`, `error_test.go` | API: `Error` (with `CallMayStillBeUp`), `ErrorKind`. `LinkFailure` has no Go type: its `call_may_still_be_up` is `Error.CallMayStillBeUp` (section 2). Go-only tests: texts, the standard equivalents of section 2, and `satlink.ErrCallMayStillBeUp` matched by the flag and through a wrapped `*satlink.Error`. |
| `src/status.rs` | `satnet/status.go`, `status_test.go` | API: `LinkState`, `CloseReason`, `Usage`, `Status`, aliases `DownReason`, `IPv4Settings` |
| `src/policy.rs` | `satnet/policy.go`, `policy_test.go` | API: `Policy`, `AllowRule`, `PortRange`, `ParsePortRange`, `ParseRuleError` |
| `src/link.rs` | `satnet/link.go`, `satnet/dial.go`; `dial_test.go` | API: `Link`, `Establish`, `Watcher` (`link.go`). Internal: the insides of `Resolve`, `Dial`, `DialUDP` and `DialContext` (`dial.go`). `dial_test.go`, Go only, internal: `DialContext`'s error, which is the context's only once its deadline has passed; a timeout or an ended context ending a dial's attempts at its first address (`endsAttempts`, as Rust's `connect_until` returns at once on `TimedOut` and `LinkDown`); nothing new — a lookup, a TCP connection, a UDP socket — starting while the link drains. |
| `src/tcp.rs` | `satnet/tcp.go`, `satnet/connection.go`; `tcp_test.go` | API: `TCPConn`, with `Abort` (Rust's `abort`) and `WriteSome` (Rust's `send_until`; `Write` is `send_all_until`). Internal: what `TCPConn` and `UDPConn` share — the socket handle, the ends, the closed flag under the engine lock, the atomic deadlines, the close that the runtime cleanups share (`connection.go`). `tcp_test.go`, Go only, internal, over an engine without a driver: `WaitAcknowledged` failing for data that never left — cut off by a new address (`moveToANewAddress`), or dropped by an abort or the hang-up, as it starts, once it is over, and when the driver abandons the link — and returning nil for data acknowledged before; a write that a `CloseWrite` stops still sending what it queued, then the FIN; a wait for room ending once the connection no longer sends (an abort, the peer's reset, a new address); nothing new sendable while the link drains; the "TCP flush" timeout; `Close` sending what was written, then the FIN (`TestCloseSendsWhatWasWrittenThenTheFIN`); and an aborted connection keeping its socket, and so its handle, until `Close`: calls after its reset has left fail as documented rather than panic, and `Close` then lets the socket go (`TestAnAbortedConnectionKeepsItsSocketUntilClosed`), once even when it comes before the reset has left (`TestAnAbortedConnectionClosedBeforeItsResetLeftLetsItsSocketGoOnce`). |
| `src/udp.rs` | `satnet/udp.go`; `udp_test.go` | API: `UDPConn`. `udp_test.go`, Go only, internal: only the peer's datagrams read; a write waiting for room, and timing out as "UDP send"; a write refused while the link drains; a closed socket going once its queue is sent; a datagram counting as undelivered while it is queued in the socket and while it waits in the device for the line (`TestADatagramWaitingForTheLineIsUndelivered`); a datagram of `MaxDatagramLen` bytes taken and leaving as one packet of the MTU (`TestTheLargestDatagramFillsOnePacket`); `LocalAddr` being the address the datagrams leave from (section 4, Stack). |
| `src/tls/mod.rs` | `satnet/tls.go`, `satnet/tlskeys.go`; `tls_test.go`, `tlskeys_test.go` | API: `TLSOptions`, `TLSConnector`, `Link.DialTLS` (`tls.go`). Internal: the public roots' pool, `rootPoolOf`, and the session cache, `caseBlindSessionCache` (`tls.go`); the keys a server's chain may not hold (`tlskeys.go`, Go only: `verifyChainKeys`, the connector's `VerifyPeerCertificate`, for which one verified chain without a refused key is enough, and which checks none when verification is skipped; `holdsNoRefusedKey`, `isRefusedKey`, `errRefusedKey`; section 3). Go-only tests: the server-name rule ("bad name", "1.2.3"); the public roots holding exactly the certificates of Mozilla's program (`TestThePublicRootsAreMozillasProgram`; `CertPool.Equal` compares certificates, not their constraints), and a root's distrust date honoured (`TestAPublicRootAnchorsNoChainIssuedAfterItsDistrustDate`, over `rootPoolOf` with a generated root under the bundle's rule); a certificate for another name refused with and without a pin; TLS 1.2 offered with rustls's suites only, and a server offering CBC alone refused; a PEM marker inside a line beginning no section; and in `tlskeys_test.go`, the keys a chain may hold, over the fake transport (`TestTheKeysAServersChainMayHold`: an RSA-2048 server or CA, an RSA-2041 server, a P-384 CA with an Ed25519 server, a P-521 or an ML-DSA server or CA trusted, an RSA-2040 or RSA-1024 server or an RSA-1024 CA refused, with the refusal's text as a literal), a server sending the certificate crypto/tls asked for ([ML-DSA-65, P-256] and [P-521, P-256] under one CA, over `keysOnlyCryptoTLSAsksFor` and `newTLSFixturePreferring`: the preferred leaf received, `TestAServerSendingTheCertificateCryptoTLSAskedForIsTrusted`; the P-256 key pinned alone refused as not pinned and both keys pinned trusted, `TestTheCertificateCryptoTLSAskedForMustBePinnedToo`, over `secureWithPins`), `TestTheRefusedKeys` (2040, 2041, 2047, 2048, 8192 and 8193 bits, 2040 and 8193 refused; P-224 to P-521; Ed25519; ML-DSA), `TestOneChainWithoutARefusedKeyIsEnough` and `TestAConfigurationThatSkipsVerificationChecksNoKey`. |
| `src/tls/stream.rs` | `satnet/tlsstream.go`, `satnet/tlsrecords.go`, `tlsstream_test.go`, `faketransport_test.go` | API: `TLSConn`, with `Abort` and `WriteSome`, Rust's `abort` and `send_until` (`tlsstream.go`). Internal: the `tlsTransport` interface and the record buffer (`tlsrecords.go`; section 4, TLS connection). `tlsstream_test.go` holds the Rust unit test `a_failed_session_still_says_why_once_the_connection_ended`, which Rust checks against rustls directly, rewritten over the fake transport: a forged record (`forgedRecord`), then the server's FIN. The other tests are Go only, over an in-memory fake of `tlsTransport` whose server end is a `net.Conn` for `tls.Server` (`faketransport_test.go`, with `Abort`, which makes its `WaitAcknowledged` fail with `ErrConnectionReset` when it dropped bytes the server had yet to read, as a `TCPConn`'s does, `checkSendable`, `announce` and the waits' `changed` function, refusing sends after its `CloseWrite` or reset; section 4, TLS tests). The fixture, `newTLSFixture` or `newTLSFixtureWithKeys` for keys of any `crypto.Signer` (`generateKey`, `generateECDSAKey`, `generateRSAKey`, `generateEd25519Key`, `generateMLDSAKey`), keeps the pin of its server's key (`publicKeySHA256`, the SHA-256 of a key's DER SubjectPublicKeyInfo) and its CA (`tlsFixture.ca`) and makes further server certificates (`serverCertificate`); `servePeerOver` serves a peer over a given fake connection, `servePeerStoppingAfterItsFlight` stops a TLS 1.3 server after it sent its flight, and `secureFakeFor` secures for a given server name. |
| `src/tls/pinning.rs` | `satnet/tlspinning.go` | Internal. Go-only tests, in `tlsstream_test.go`: a pin of a CA the server sends but does not own is refused, and a pinned key does not stand in for the chain. |
| `src/tls/compression.rs` | — | Not portable: RFC 8879 (section 3). |
| `src/engine.rs` | `satnet/engine.go`, `engine_test.go` | Internal, with `isNegotiating` (snapshot 3). The 2 Rust tests (`negotiating_is_not_idling_and_renegotiating_is_not_traffic`, `waits_under_way_outlive_a_requested_close_but_nothing_new_starts`). Go-only tests: only a link whose PPP never came up is negotiating; of `waitUntil`: the closed flag first, then the attempt, a deadline moved while waiting; prompt wake-ups, and changes closing the channel the waiters hold; `beginClosing` still saying why the link closes when aborting its connections panics, and `abandon` still reporting the link closed, for the reason it was closing for and with the call perhaps up, when aborting them panics (`TestAnAbandonedLinkWhoseConnectionsCannotBeAbortedStillReportsItselfClosed`); the app's traffic restarting the idle clock at all six sites; and an abandoned link reporting itself closed with the call perhaps up. |
| `src/driver.rs` | `satnet/driver.go`, `driver_test.go`, `drivercall_test.go` | Internal. Go-only tests (`driver_test.go`): panic messages, also for a value that cannot describe itself; a datagram PPP cannot send; a datagram too large for the peer, dropped with a warning (over `openEndpoint`, a PPP client brought up against an in-memory server); data handed to PPP only when the paced line can take it; the "IPv4 up" line in Rust's text; an address the stack cannot use failing IPCP; a link that comes up stopping its wait for PPP; the limits' thresholds and Rust's order of checking them, the call time limit starting the hang-up in time to end the call by it; the drain, `isDrained` (everything delivered and the line free), and a requested close draining for at most `CloseGrace`, and for none while PPP negotiates; a cancellation that stops only a link still coming up; hang-up errors that carry the handset's failure. `drivercall_test.go`, Go only: the driver over a real data call to the simulator behind a `faultyLine` — a lost LCP Configure-Request sent again, the keepalive noticing a silent gateway, a line refusing the first and a later frame, PPP output after NO CARRIER dropped without failing the link, and the hang-up's info and warning lines in Rust's texts (`TestAHangUpThatCannotBeConfirmedIsLoggedAsAWarning` waits for the link to close for its transport before calling `Close`, so that it does not depend on PPP's pacing backlog). |
| `src/stack.rs` | `satnet/stack.go`, `satnet/stackconnections.go`, `satnet/lookup.go`; `stack_test.go`, `stackconnections_test.go`, `lookup_test.go` | Internal, split by concept (section 4, Stack): the interface, polling, UDP sockets, ports, `localEndpoint`, and `configure`, which keeps the DNS servers and notices an address change (`stack.go`); the TCP connections — `outbox`, `sendTCP`, `tcpMaySend`, `tcpCanSend`, `tcpUnacknowledged`, `shutdownTCP`, `resetTCP`, `dropOutbox`, `closeTCP`, `abortTCP`, `feedConnections`, `cutOffUndelivered`, `recordCutOff`, `sendBacklogs`, `passOnAcknowledgements`, `removeTCP` (`stackconnections.go`); name lookups — `dnsPort`, `lookupID`, `nameLookup`, `startLookup`, `lookupResult`, `abandonLookup`, `dnsAnswer` and `parseDNSAnswer`, `dnsServerTimeout`, `longestLookup` (`lookup.go`). Tests: the 3 Rust tests of the stack itself and, Go only, the stack's ICMP errors never going, and the app's own datagram to a DNS server waiting for the line, as `poll` hands the device the app's UDP ports (`TestTheAppsOwnDatagramToADNSServerWaitsForTheLine`; `stack_test.go`); the 6 of `mod lookups` and, Go only, a resolved lookup, a lookup that needs a server and a name, a spent lookup, an abandoned one cancelled and sending no more queries, an answer about another name, an answer spelling the name in another case, and which datagrams count as an answer about the name, a table that damages what only each checksum can show (a bit of the IPv4 identification, a letter of the question's name) and refuses every error rcode (FORMERR, SERVFAIL, NOTIMP, REFUSED) and another question class (`lookup_test.go`); the 9 of `mod connections` and, Go only, the send buffer bounding outbox and socket together (`TestTheSendBufferBoundsTheOutboxAndTheSocketTogether`), a finished connection taking nothing, data cut off by an address change (and forgotten with its socket), data the stack takes just before the reset staying cut off, data the stack holds when the address changes (the rule Rust snapshot 4 shares), the same address cutting nothing off, a connection let go of sending no keepalives, `abortAll` and `resetTCP` dropping what the app wrote and recording it as cut off, and cutting off only the connections whose outbox held data, what the stack took staying in the send buffer (`TestAResetOrTheHangUpCutsOffTheConnectionsWhoseOutboxItEmptied`); data the stack holds on a connection the peer reset no longer counting as undelivered, so that a requested close does not wait for it (`TestDataOnAConnectionThePeerResetIsNoLongerUndelivered`); a small write leaving while the one before is unacknowledged, Nagle being off (`TestASmallWriteDoesNotWaitForTheLastOnesAcknowledgement`); and a bare acknowledgement waiting the 100 ms ACK delay for a reply to ride on (`TestABareAcknowledgementWaitsAWhileForAReplyToRideOn`; `stackconnections_test.go`, whose `sentBeforeTheFIN` helper several tests share). |
| `src/device.rs` | `satnet/device.go`, `device_test.go` | Internal (`ipstack.Device`: urgent datagrams at once, the rest per flow in turns, stale retransmissions dropped, waiting segments brought up to date; section 4). The 15 Rust tests: Rust's `Segment` builder is `testSegment`, built with `wire.TCPRepr` and `wire.IPv4Repr`, and the bare datagram to port 53 is built with the packet's setters, as Rust builds it. Go only: a retransmission carrying the FIN still goes, and a copy without the FIN does not replace the segment carrying it; datagrams without a flow are dropped (a table); a TCP datagram without a segment waits; datagrams that are not segments never replace each other; acknowledgements of a connection not seen opening change nothing; what is news for the peer (a table); received datagrams come in order; clearing forgets the connections too. |
| `src/pacing.rs` | `satnet/pacing.go`, `pacing_test.go` | Internal. The 9 Rust tests. |
| `src/names.rs` | `satnet/names.go`, `names_test.go` | internal |
| — | `satnet/log.go`, `log_test.go` | Go only: the logging helper and its tests. |
| `tests/link.rs` | `satnet/link_integration_test.go` | The Rust `Faulty` transport is `simtest.FaultyTransport`; `handset_with` is `simtest.NewHandset` with `simtest.SimulatorConfig()`, and `config.call.escape_guard = SIMULATOR_GUARD * 3` is `simtest.CallEscapeGuard`. The cancellation tests set up the link with `establishInBackground` and `awaitSetup`. Plus, Go only: `TestALookupNoServerAnswersTimesOut`, `TestAContextCancelsTheSetupThroughAfterFunc`, `TestABareAddressIsNotDialledOnceTheLinkIsClosed`, `TestAnInvalidNameIsNeverLookedUp`, `TestARefusedConnectionFailsWithItsAddress`, `TestEveryAddressRefusingReportsTheLastOneTried`, `TestADialGoesOnToTheNextAddress`, `TestTheLinkKeepsTheHostsItWasGiven`, `TestTheCloseGraceBoundsARequestedClose`, `TestTheCallTimeLimitEndsTheCall`, `TestDataHoldingTheHandsetsNoCarrierDoesNotEndTheLink` (echoed "\r\nNO CARRIER\r\n" leaves the link up, as the CR escape keeps the handset's result codes out of frames), `TestAnAnswerIsReusedRatherThanAskedForAgain`, `TestTheUsageIsCurrentWhileTheLinkIsUp`, `TestDataTheHangUpDroppedIsNotReportedDelivered` (over `unpluggingLine`, a line that fails the next PPP frame after running what it was given, here a Write from another goroutine), `TestTheCallsDurationCountsItsHangUp` (`Usage.ConnectedFor` includes the hang-up, the billed airtime), `TestClosingDeliversTheDatagramsAlreadyWritten` (three 200-byte datagrams written just before `Link.Close` all leave before the hang-up: the UDP side of `closing_delivers_what_was_written_but_takes_nothing_new`) and `TestARefusedDestinationGetsNothingOnTheLine` (`Resolve`, `DialContext`, `Dial` and `DialUDP` of a refused name, an allowed name on a refused port, or a refused address send nothing on the line, `DataBytesFromApp` unchanged, and leave no lookup pending, one link per case; `TestAnInvalidNameIsNeverLookedUp` checks the same, through `expectNothingSentSince`). `TestAPanicWhileHangingUpStillReportsTheLinkClosed` also checks, Go only, that the core's DTR pulse ended the call (section 3). |
| `tests/tls.rs` | `satnet/tls_integration_test.go` | Rust's `link_over` and `controlled_link_over` are `tlsLinkOver(t, profile, linkRate)`, which also allows `echo.test:7` for a probe connection; `line_of` is `lineOf`, `receive_exactly_by` is `readExactlyBy`, and a reader in another thread is `readInBackground`. Plus, Go only: the close_notify that waits longer than crypto/tls's five seconds (section 4, TLS tests), `TestATLSWriteWaitingForRoomGoesOutWholeBeforeAnother`, `TestAbortWakesATLSWriteWaitingForRoom`, `TestAbortWakesATLSCloseWriteWaitingBehindRecords`, and `TestAWriteRefusedWhileTheLinkDrainsLeavesDeliveryConfirmed`, the drain version of `a_write_refused_once_the_link_closed_leaves_delivery_confirmed` (a 16 KiB upload at 3000 B/s keeps the link draining through a 3 s close grace; the TLS write is refused with nothing taken, and `WaitAcknowledged` stays nil). |
| — | `satnet/conn_integration_test.go` | Go only: net.Conn behaviour (deadlines moved while a TCP read, a TCP write or a UDP read waits, `TestADeadlineMovedWhileACallWaitsApplies`; `Close` waking blocked calls, `net.ErrClosed` after `Close`, `Abort` waking a waiting `Read`, a `Write` after `CloseWrite` refused, an empty `Read` or `Write` returning at once), `Write` (a timed-out one counting what it queued; one waiting for room going out whole before another), `WriteSome` (what fits queued without waiting for room for the rest, a timeout only when nothing fits, an empty one), `DialContext` with cancellation, keeping the timeout of a lookup nobody answered, and a `net/http` GET of `http://web.test/`, runtime cleanups, a collected Link (section 4). |
| — | `satnet/support_integration_test.go` | The helpers the four external test files share (link, TLS, connection and panic tests), among them `billingGuard`, the 500 ms escape guard of the billing tests, and `expectTheHangUpBilled`. |
| — | `satnet/connection_test.go` | Go only: the cleanups of a collected `TCPConn` — closed gracefully: what was written, then the FIN — and `UDPConn`. |
| — | `satnet/panic_integration_test.go` | Go only, external: a panic carrying a nil error pointer, on a read or on the escape, over `brokenPanicTransport`, whose DTR line reaches nothing, so that in `TestAPanicThatCannotDescribeItselfWhileHangingUpSparesTheProcess` the call stays up after `Close` until the app's `EndDataCall`, through the released handset, ends it; a log handler panicking as the hang-up aborts the connections, the link reported closed only once the call that panic kept from being hung up has been hung up and the handset released (`TestAPanicWhileTheHangUpAbortsTheConnectionsStillHangsUp`, and `TestTheFirstOfTwoPanicsIsWhyTheLinkClosed` with two panics, the first of which says why the link closed; both check with `expectHungUpAndReleased` as `WaitClosed` returns, before `Close`); a handler broken for good, which panics every time the connections are aborted, as the hang-up starts and again as the driver abandons the link (`TestAPanicEveryTimeTheConnectionsAreAbortedStillClosesTheLink`, over `panicOnAClosingConnection.brokenForGood` and `armPanicOnEveryClosingConnection`; it pins `driver.abandon`'s own `recover`); a hang-up that panics on the escape after an earlier panic, whose call the core's DTR pulse ends (`TestAHangUpThatPanicsAfterAnEarlierPanicSparesTheProcess`; `endCallLeftUp`'s own `recover`); and the hang-up run after a panic counted in `Usage.ConnectedFor` (`TestTheCallsDurationCountsTheHangUpThatFollowsAPanic`, a serial table over `armPanicOnAClosingConnection` and `armPanicOnEveryClosingConnection`, whose second case pins the order of `driver.abandon` that section 3's panic row gives). |
| — | `satnet/export_test.go` | Go only, test only: `AwaitWaitingCall(t, link, budget)` and `(*shared).awaitWaitingCall` read the engine's count of waiting calls, so that `conn_integration_test.go` and `engine_test.go` act only once a call waits (section 2, Tests); `PendingLookups(link)` and `HangUpTime(config)`. |

### iridium-sim → package `sim`

| Rust | Go | |
|---|---|---|
| `Cargo.toml` | — | Feature `tls` is always on; `smoltcp` → `internal/ipstack`; `rustls` → `crypto/tls`. |
| `src/lib.rs` | `sim/doc.go`, `sim/example_test.go` | The doc example runs: `Example` with an output comment. Its re-export of `iridium_ppp::ConfigError` (snapshot 3) is the alias `sim.ConfigError` (`config.go`). |
| `src/config.rs` | `sim/config.go`, `config_test.go` | API: `Config`, `LinkProfile`, `NetworkConfig` (with `DNSServers` and `SilentAddresses`), `HostAddress`, their defaults (from named constants and `defaultDNSServers()`), aliases `AuthProtocol`, `Credentials`, `Authentication`, and `ConfigError` (what `Control.RestartPPP` reports). `Config` has `String`, `GoString` and `Format`: `%#v` prints `GoString`, `%+v` the fields with their names (as fmt prints a struct), and every other verb what `%v` prints. With `String` alone, `%s` would print the same, but fmt would print `%+v` without the names, quote the text for `%q`, hex-encode it for `%x` and `%X`, and print each field with any other verb, such as `%d` or `%t`, whether it fits the field or not (`%!t(int=4)` for `SignalBars`). As these texts format the `*Authentication` only with `%v`, `%+v` or `%#v`, they keep the gateway's password redacted for every verb by themselves, as ppp's `(*Authentication).Format` also does (Rust's derived `Debug` printed the credentials' redacting `Debug`). Go-only tests: the redaction table, verbs `%v %+v %#v %s %q %x %X %d %t` × `Config`, `*Config`, and structs and slices holding them; and `%s %q %x %X %d %t` printing what `%v` prints. |
| `src/handset.rs` | `sim/handset.go`, `handset_test.go` | API: `Handset`, `New`, `Control` (`DropCall`, `RestartPPP`, `ReceiveSMS`, `Stats`). Go-only tests: `Close`, I/O and DTR after `Close`, a `Control` that keeps working, and `New` keeping a copy of the configuration, `DNSServers` and `SilentAddresses` included. |
| `src/stats.rs` | `sim/stats.go` | API: `Stats`, `SentSMS` |
| `src/secure_echo.rs` | `sim/secureecho.go`, `secureecho_test.go` | API: `SecureTestCAPEM`, `SecureTestKeySHA256`. Internal: the TLS echo session over `internal/memconn`. Go-only tests, run with `-race` (section 4). |
| `testdata/*` | `sim/testdata/*` | Copied unchanged; README adapted. |
| `src/calendar.rs` | `sim/calendar.go`, `calendar_test.go` | internal |
| `src/circuit.rs` | `sim/circuit.go` | internal |
| `src/command.rs` | `sim/command.go`, `command_test.go` | internal |
| `src/connection_request.rs` | `sim/connectionrequest.go`, `connectionrequest_test.go` | internal |
| `src/device.rs` | `sim/device.go` | internal (`ipstack.Device`) |
| `src/dns.rs` | `sim/dns.go`, `dns_test.go` | Internal; names fold ASCII letters only (`internal/asciicase`). Go-only test: a query with the Kelvin sign U+212A does not find kelvin.test (`TestOnlyASCIILettersFoldInNames`). |
| `src/escape.rs` | `sim/escape.go`, `escape_test.go` | internal |
| `src/gateway.rs` | `sim/gateway.go`, `gateway_test.go` | Internal; Rust's `server()` is `startServer`. `gateway_test.go` is Go only: a PPP client and a gateway talking straight to each other (`pppPair`), the internet's lifetime, the "IPv4 up" and "PPP down" lines in Rust's texts, a renegotiation that changes the MTU, which builds a new internet for it and closes the old one's TLS session, a restart of PPP that builds a new internet for the new address, and one that cannot start, which changes nothing, and the "dropped a %d-byte datagram" debug line, for an answer the internet sends while the client starts PPP over (PPP is not open for IPv4 until the new negotiation ends, but the internet stays). |
| `src/http.rs` | `sim/http.go`, `http_test.go` | internal |
| `src/internet.rs` | `sim/internet.go`, `internet_test.go` | Internal. Go-only tests: an address the stack cannot use is left out with a warning; the UDP echo's and the DNS server's debug lines for an answer they cannot send (to a datagram from port 0, "unaddressable"). |
| `src/line.rs` | `sim/line.go`, `line_test.go` | Internal; text through `internal/utf8lossy`. |
| `src/link.rs` | `sim/link.go`, `link_test.go` | internal (the delay line) |
| `src/modem.rs` | `sim/modem.go` | internal |
| `src/modem/commands.rs` | `sim/modemcommands.go` | internal |
| `src/modem/tests.rs` | `sim/modem_test.go` | Plus, Go only, for rules of Rust's that its tests leave out: an SMS announcement waits until the app has read the earlier output (`TestAnSMSAnnouncementWaitsUntilTheEarlierOutputIsRead`); online-command mode holds the far end's data until ATO (`TestOnlineCommandModeHoldsTheFarEndsDataUntilATO`: the PPP server's repeated Configure-Request, after `ppp.DefaultTimers().Restart`); ATZ restores the default bearer and `&D0` as well as echo (`TestATZRestoresTheFactorySettings`); a restart of PPP during call setup changes nothing (`TestARestartOfPPPDuringCallSetupChangesNothing`, Rust's `Dialing { .. } => Ok(())`; in Go a dialling call has no circuit): the call then connects with its gateway assigning the configured client address; the guard of silence before `+++` counts from CONNECT and from ATO, so pluses sent sooner are data (`TestPlusesSoonerThanAGuardAfterCONNECTAreData`, `TestPlusesSoonerThanAGuardAfterATOAreData`); the modem moves the simulation to the time of what the app writes before taking it, so an escape whose closing guard has passed ends online-data mode before the bytes that follow, even with no read in between (`TestAnATHWrittenOnceTheEscapesGuardHasPassedHangsUp`); closing the port after AT&D2 ends a call being set up — no CONNECT, no NO CARRIER, `+CEER: Normal call clearing` (`TestClosingThePortAfterATAndD2EndsACallBeingSetUp`); and the modem's log lines in Rust's texts and levels: a data call dialled, a voice call, an SMS, the app's hang-up, a network drop, a DTR drop and a gateway that cannot start (warn) (`TestTheModemLogsItsEventsAsTheRustVersionLoggedThem`). The helpers `dialDataCall` and `connectDataCall` dial as these tests and Rust's do. |
| `src/reply.rs` | `sim/reply.go`, `reply_test.go` | internal |
| `src/services.rs` | `sim/services.go` | internal |
| `src/sms.rs` | `sim/sms.go`, `sms_test.go` | internal |
| `src/time.rs` | `sim/time.go`, `time_test.go` | internal |
| — | `sim/log.go`, `log_test.go` | Go only: the logging helper, and the recorder the tests share. |
| — | `sim/tlsclient_test.go`, `sim/goroutines_test.go`, `sim/random_test.go`, `sim/datagram_test.go` | Go only, test helpers of package `sim`: `TLSClient` (a crypto/tls client pumped over an `internal/memconn.Conn`, with `ServerSaidGoodbye`; its `awaitEnd`, `finalError` and `extractAll` are unexported, as only the internal tests use them, and so are `secureTestClientConfig(t, cache)`, the settings of a client that trusts the test CA alone, and `newTLSClient(t, local, remote, config)`, which starts a client with settings of the test's own; `secureecho_test.go` has `connectClient(t, config, clientConfig)` beside `connectSession`, `connectTLS12ClientOffering(t, suite)`, a client that speaks TLS 1.2 at most and offers one suite, and `sessionPair.handshake`, which returns the handshake's error and on which `completeHandshake` is built), `AwaitGoroutines` with the helper that polls with `runtime.GC`, and `UDPDatagram`, the UDP datagram builder on `internal/ipstack/wire`'s Reprs, which the external tests use too, and the Rust tests' xorshift64 for the internal ones. |
| `tests/support/mod.rs` | `sim/support_integration_test.go` | PPP client over a `DataCall`, datagrams built with `sim.UDPDatagram` on `internal/ipstack/wire`'s Reprs, a client stack on `internal/ipstack` (`clientNetwork`). `connectNetworkThrough(t, call, control)` takes the data call, made with `dial` or with `dialWith(t, handset, settings)`, so that a test can dial with settings of its own. The rustls client is `sim.TLSClient` over an `internal/memconn` connection that the client network pumps on every exchange (section 4). The simulator's tests keep their own set-up rather than `internal/simtest`'s, which is built on sim for the packages above it. |
| `tests/commands.rs` | `sim/commands_integration_test.go` | `an_sms_from_another_thread_reaches_a_waiting_app` runs in a `testing/synctest` bubble and sends the message once `PollURCs` waits (section 2, Tests). |
| `tests/data_call.rs` | `sim/datacall_integration_test.go` | `a_call_escaped_from_but_never_hung_up_is_ended_by_the_next_session` builds the next session's handset over the earlier one's `Transport`, where Rust uses `into_transport`. Plus, Go only: the DNS servers IPCP offers (two, and none), a silent address (the echo answers in order, so that the first answer shows the silent one was lost), and `RestartPPP` (the new address with the call kept, an address that is not IPv4, no call). |
| `tests/internet.rs` | `sim/internet_integration_test.go` | Plus, Go only: the goroutine counts after `DropCall`, after PPP goes down, after `RestartPPP` and after the client resets a TLS connection; a simulator nobody holds, its call dialled without AT&D2, ending its TLS session (section 4, TLS session lifetime); a client that hangs up as soon as its records are on the connection still getting the rest of its echo, then close_notify (`TestAClientThatHangsUpStillGetsTheRestOfItsEchoBeforeTheGoodbye`; Rust's `is_finished`); and data sent after the web server closed answered with a reset (which the Rust tests check through the CLI's `net tcp`). |

### iridium-serial → package `serial`

| Rust | Go | |
|---|---|---|
| `Cargo.toml` | — | `serialport` → `go.bug.st/serial` and `go.bug.st/serial/enumerator`. |
| `src/lib.rs` | `serial/doc.go`, `serial/discover.go` (`Port`, `Discover`), `serial/transport.go` (`DefaultBaud`, `Transport`, `Open`; and the select(2) limit, `selectSetSize` and `isBeyondSelect`), `serial/connect.go` (`ChoosePort`, `Connect`, and, Go only, `ChoosePortFrom`, the choice among ports already discovered, which the CLI makes over its own discovery), `serial/closeonexec_unix.go` (`openCloseOnExec` and `descriptorsOf`, build constraint `linux \|\| darwin \|\| freebsd \|\| openbsd`, the library's Unix set) and `serial/closeonexec_other.go` (the library's `Open`, and a `descriptorsOf` that finds none) | API, split by concept; close-on-exec is internal (section 4). |
| — | `discover_test.go`, `transport_test.go`, `transport_unix_test.go`, `connect_test.go`, `closeonexec_test.go`, `pty_test.go` with `pty_darwin_test.go` and `pty_linux_test.go` | Go only, internal: the Rust crate has no tests, and every exported function reaches the operating system, so the tests go through unexported seams (section 4). `transport_unix_test.go` (Unix): the select(2) limit, over `libraryShapedPort`, a fake port shaped as the library's (`transport_test.go`). `closeonexec_test.go` (Unix): a port of another shape gives no descriptors and no panic. The pty tests run on macOS and Linux over a pseudo-terminal's slave: the port's three descriptors are close-on-exec; a child process started while the port is open does not keep the line up once the port closes; an empty read of a port with input waiting reads nothing and leaves the input for the next read (`TestAnEmptyReadOfAPortWithInputWaitingReadsNothing`); a port beyond select(2)'s 1024 descriptors fails its reads with `ErrTransport` instead of panicking (skipped when `RLIMIT_NOFILE` is too low). |

### iridium-cli → `cmd/satlink`

| Rust | Go | |
|---|---|---|
| `Cargo.toml` | — | Binary `iridium` → `satlink`; clap → the command line read as clap read it, with cobra running the command (section 4); `ctrlc` (feature `termination`) and `libc` → `os/signal` and `syscall` (`interrupt*.go`). |
| `src/main.rs` | `cmd/satlink/main.go`, `doc.go`, `run.go`, `root.go`, `environment.go` (`environmentOf`, which tests call, and `processEnvironment`, the process's own); the commands: `list.go`, `info.go`, `status.go`, `sms.go`, `call.go` (`dial`, `answer`, `hangup`), `at.go`, `monitor.go`; their support: `handset.go` (the global options, `withHandset`, which opens the handset — through the environment's discovery and `serial.ChoosePortFrom` when no port is given — and closes it on every path out, a panic included, and the trailing "(event)" lines), `simulate.go`, `output.go`, `numbers.go` (numbers read as clap read them, saturating conversions); the process: `brokenpipe_unix.go` (`ignoreBrokenPipes`: `signal.Ignore(syscall.SIGPIPE)`, called first in `main`) and `brokenpipe_windows.go` (a no-op), `processstreams.go` (`processStream`, `isBadDescriptor`, `withOSErrorText` and `osError`: the process's standard streams, whose `badDescriptor` — EBADF on Unix, `processstreams_unix.go`, and ERROR_INVALID_HANDLE on Windows, `processstreams_windows.go` — reads as Rust's standard library took it (section 4, Structure for tests), and whose other failures with an OS error number read as Rust's `io::Error`, "Broken pipe (os error 32)", through `internal/errnotext`); `main` also clears `cobra.MousetrapHelpText`; the command line as clap's: `command.go` (the command tree; a `command` keeps its cobra command in the field `runner`, with `name()`, `about()` and `argumentGroup()`, clap_derive's group: the arguments the command declares), `arguments.go` and `values.go` (the arguments as clap declared them, and what they hold), `commandline.go` (reading the command line), `help.go`, `usage.go`, `usageline.go`, `suggestions.go`, `palette.go` (clap's styles, and `showsStyles`, anstream's choice for each stream), `strip.go` (`stripStyles`: what anstream's `StripStream` passed on to a stream that shows no styles, anstream 1.0.0's `StripBytes` over anstyle-parse 1.0.0's state table); interruptions: `interrupt.go` (what a net call catches: `notifyInterruptions` for the process, `reactionTo`; `interruptionHandler`, with `newInterruptionHandler`, `handle`, `requestStop` and `askedToStop`: what each interruption does; and `interruptions`, with `catch`, `react`, `stop` and `askedToStop`: the relay that hands each to the run's handler), `interrupt_unix.go` (SIGINT, SIGTERM and SIGHUP; `dieInterrupted`: `signal.Reset(SIGINT)`, `kill(getpid(), SIGINT)`, then exit status 130 after `sigintDeliveryGrace`, 1 s), `interrupt_windows.go` (`os.Interrupt` and SIGTERM; exit status 130); terminals: `isTerminal` in `terminal_ioctl.go` (macOS, Linux and Android, FreeBSD, OpenBSD) with `terminal_linux.go` (`TCGETS`) and `terminal_bsd.go` (`TIOCGETA`), and `terminal_windows.go` (`GetConsoleMode`). `run.go`'s finish flushes the log, writes "error: …", stops catching interruptions and returns `exitInterrupted` for an interrupted run, for which `main` ends by SIGINT; a panic that reaches `run` is reported by `panicked` with `reportPanic` and ends the run with `exitPanicked` (101) | Tests: `commands_test.go` (each command in-process against `testdata/commands`, `monitor`, `-v`, the port, a command without `--port` choosing among the handsets discovered, a handset command whose output cannot be written — every one that prints: list, info, status, sms send, list and read, dial, at and monitor — or whose "(event)" line cannot be written), `status_test.go` (a status whose output fails asks the handset nothing more: `TestAStatusAsksTheHandsetNothingMoreOnceALineCannotBeWritten`, over `commandRecorder` and `breakingWriter`), `interface_test.go` (`testdata/help`, `testdata/usage`, `testdata/style`: help, usage errors and styles; help styled when standard output is a terminal and errors when standard error is, whatever the other stream is, which the style goldens, forced by CLICOLOR_FORCE, cannot show (`TestHelpAndErrorsAreStyledOnTheTerminalTheyGoTo`, with TERM=xterm, over the in-process world's `stdoutIsTerminal` and `stderrIsTerminal`); and, as golden files would hold the bytes, text that is not UTF-8 (`TestTextThatIsNotUTF8IsRefusedOrShownLossilyAsClapDid`, `TestHelpShowsAVariableThatIsNotUTF8Lossily`) and control characters, taken out as clap's streams took them out and quoted as Rust's `{:?}` quotes them (`TestControlCharactersAreTakenOutOfErrorsAsClapsStreamTookThemOut`, `TestHelpShowsVariablesHoldingControlCharactersAsClapDid`), their outputs written out as the Rust binary printed them), `strip_test.go` (`TestStrippingKeepsWhatAnstreamKept`: anstream's own test cases, then sequences and control characters that a command line can bring into clap's styles, finished or not; `TestTheParsersRulesAreAnstyleParsesAsStrippingSeesThem`: the port's rules against anstyle-parse 1.0.0's for every state and byte, which `anstyleParseRules` writes out from `state/codegen.rs`, compared as stripping sees them — where the byte moves the parser, with Utf8 read as the ground, and whether anstream dropped it, kept it or kept the character it starts; `TestStrippingClapsStylesLeavesWhatTheZeroPaletteWrites`, over every help, usage, environment and style golden command line), `environment_test.go` (`TestTheProcessesArgumentsStreamsAndVariablesReachTheRun`), `inprocess_test.go` (the in-process harness), `golden_test.go` (golden files; `go test ./cmd/satlink -update` rewrites them), `run_test.go` (the last log lines before the end; `TestAPanicEndsTheRunWithStatus101OnceTheHandsetIsClosed`: a panic while the handset is readied, in a handset command and while a data call is set up, each checked through the stack reported; `TestAPanicOutranksAnInterruption`), `processstreams_test.go` (Unix) and `processstreams_windows_test.go` (Windows; the cross-vets type-check it), `interrupt_test.go` (with `TestTheFirstInterruptionAsksTheRunToStopBeforeSayingSo`), `interrupt_unix_test.go` (a real SIGTERM relayed by `notifyInterruptions`), `terminal_test.go`, `terminal_pty_test.go` with `terminal_pty_darwin_test.go` and `terminal_pty_linux_test.go` (a pseudo-terminal is a terminal, and each standard stream is said to be one only when it is: `TestEachStandardStreamIsSaidToBeATerminalOnlyWhenItIsOne`), and unit tests beside each file. The 223 golden files were checked against the Rust CLI's output (snapshot 4) with only the product renamed: 207 are byte for byte the same, one differs for a reason of section 3 (`net-get-https-untrusted`, against the parity binary only in the TLS library's text), and 15 use fake handsets, USB ports or discovery the Rust binary cannot reproduce. |
| `src/net.rs` | `cmd/satlink/net.go` (the net options, `netArguments.linkConfig` and `netArguments.policy`, `netTarget` — `rule`, `check`, `isReachable`: Rust's `target_rule` and `check_target` — `ipv4Literal` and `needsACall`, the `netAction` interface for Rust's `NetCommand` — `target`, `literalAddress`, `execute`, which takes `netLink`, the link as the net subcommands use it (`Resolve`, `Dial`, `DialTLS`, `RequestClose`; `*satnet.Link` is one) — `status`, `resolve`, `newNetCommand`), `netcall.go` (the call: `establish` — announcing the dial, the setup's cancellation, the warning after a failed setup — recovery, close, summary, result, the interrupted outcome), `netget.go` (`net get`: `fetch`, which connects over `dialer`, the link as a fetch uses it, hands the connection to `exchange` and closes it afterwards; `exchange`, the GET and its whole response within one deadline, `transferTimeout` from its start, over `exchangeStream`, the connection as an exchange uses it (`io.ReadWriter` and `SetDeadline`); `connect`; `newHTTPSConnector`, Rust's `TlsConnector::new()` in `fetch`: the public web PKI, no pin, no ALPN; `readResponse`, `endingOf`, `responseEnding`, `printHTTPResponse`) with `httpresponse.go` (`finalResponse`, `header`, whose field names compare as ASCII through `internal/asciicase`, `announcedEnd`) and `httpurl.go`, `netrelay.go` (`net tcp`; `sharedConnection`, which closes the connection once the relay and the sender of standard input are both done with it, as Rust's `Arc<TcpStream>` dropped it; `outgoingStream`, the connection as the sender uses it; `afterRemoteClosed`, which takes the connection's `WaitAcknowledged` as a function, `waitAcknowledged`, as `readResponse` takes `receive`) | The 18 Rust tests: `parses_urls`, `a_url_that_would_break_the_request_line_is_refused_before_dialling`, `the_fragment_stays_behind_and_a_bare_query_gets_a_path`, `the_host_header_names_the_port_unless_it_is_the_default` (`httpurl_test.go`); `the_request_names_the_port_but_keeps_the_fragment_back`, `reading_stops_at_a_close_a_truncation_or_the_deadline`, `reading_stops_once_the_response_is_as_long_as_announced`, `a_body_is_complete_when_its_length_says_so_or_the_session_ended_properly`, `a_response_whose_head_never_ended_cannot_be_used` (`netget_test.go`); `responses_that_never_carry_a_body_need_no_length`, `framing_the_client_cannot_check_is_an_error`, `a_length_no_response_could_reach_never_ends_one_early`, `interim_responses_are_skipped_and_never_taken_for_the_final_one`, `reads_the_status_code`, `splits_http_head_from_body` (`httpresponse_test.go`); `targets_that_are_rules_rather_than_hosts_are_refused_before_dialling`, `limits_too_small_to_be_of_use_are_refused_before_dialling` and `an_address_is_resolved_without_a_call` (`net_test.go`, parsing `net …` arguments with the command tree, without dialling). `netcall_test.go` and `netrelay_test.go` are Go only: in `netrelay_test.go`, `TestTheSenderResetsTheStreamUnlessAllOfStandardInputWasSent` (a recording stream: a reset after a failed read, write or write deadline; the end after success; the outcome reported first), `TestEachChunkOfStandardInputGetsSendTimeoutToBeQueued` (each chunk's write deadline two minutes on, a literal, Rust's `SEND_TIMEOUT`), `TestTheEndOfStandardInputMustBeAcknowledgedWithinThirtySeconds` (`WaitAcknowledged` given 30 s, a literal, Rust's `DELIVERY_GRACE`; a reset instead fails with "not all of standard input was sent: the connection was reset") and, over the simulator, `TestASenderWhoseStandardInputFailsResetsTheConnection`, `TestASenderWhoseStandardInputEndsFinishesTheStream` (Rust's tests leave the stream's end unchecked) and `TestARelaysConnectionClosesOnceTheRelayAndTheSenderAreDone` (whichever is done last, and at once for a relay that returns before the sender starts, "the relay alone", over `brokenWriter`, whose writes fail with `errBrokenOutput`; over `dialRecorder`, a link that keeps the connection it dials). Go only too: in `net_test.go`, `TestALookupAConnectionOrAHandshakeIsAllowedTwoMinutes` (a lookup, a relay's connection, and an `http://` and an `https://` fetch each get the literal two minutes of Rust's `CONNECT_TIMEOUT`, over `linkRecorder`, a link that records each lookup's and dial's timeout and fails them); in `netget_test.go`, `TestAnHTTPSFetchTrustsThePublicWebPKI`, which runs `fetch` over `linkRecorder` and checks the connector that reaches `DialTLS`: `Config().RootCAs` equals a pool of `bundle.Roots()` from `golang.org/x/crypto/x509roots/fallback/bundle`, the certificates of Mozilla's program and nothing else (`CertPool.Equal` compares certificates), with no pin (`VerifyConnection` nil) and no ALPN; the golden `net-get-https-untrusted` cannot show this, as a fetch that trusts anything else fails there with the same error. `TestNetGetAllowsTenMinutesForTheRequestAndItsWholeResponse`, over `transferRecorder`, a recording `exchangeStream`: exactly one deadline, ten minutes (a literal, as `doc.go` promises) from the exchange's start, in force as the request is written; `TestAnExchangeEndsAtADeadlineOrRequestThatFails`: a deadline that cannot be set sends nothing, and a request that cannot be written reads nothing. |
| `tests/net.rs` | `cmd/satlink/netinprocess_test.go` (package `main`, in-process), `cmd/satlink/net_integration_test.go` (package `main_test`, Unix only) | In-process, through `run`, with standard input a reader: `a_request_the_remote_end_answered_and_closed_is_no_error`, `an_upload_the_remote_end_cut_short_is_an_error`, `an_upload_whose_end_the_remote_end_reset_is_an_error`, `a_fetched_page_is_hung_up_on_before_a_slow_reader_takes_it`; Go only: standard input still open when the remote end closed (an error, but none at a terminal), a relay whose output cannot be written, an interrupted relay ending with 130, an interruption while the call is placed, an interruption hanging up while standard error is blocked (`TestAnInterruptionHangsUpWhileStandardErrorIsBlocked`), and a panic in the subcommand's work (`TestAPanicDuringTheCallHangsUpAndFailsTheCommand`: `net resolve` with a standard output whose writes panic; the panic is reported, then the call summary, then "error: internal error: the command panicked" with status 1, and a handset that records its stats as the command closes it shows the call already hung up, not left to the DTR drop of closing the port). Running the built binary — `TestMain` builds `satlink` once with `go build` into a directory it removes, the tests run it in `satlinkEnvironment`, the test process's environment without `SATLINK_*` and the variables that decide styles (`NO_COLOR`, `CLICOLOR_FORCE`, `CLICOLOR`, `TERM`, `CI`), so that they run alike on every machine, and they skip where no `go` command is found: `an_interrupted_run_hangs_up_then_ends_by_sigint` (and by SIGTERM and SIGHUP), `a_second_interruption_right_after_the_first_still_lets_it_hang_up`, `a_double_tap_while_standard_error_is_blocked_still_lets_it_hang_up`, `an_interruption_during_call_setup_keeps_the_call_from_connecting`, `a_hang_up_signal_it_was_started_to_ignore_stays_ignored` (and SIGINT in a background job, through an `sh` trap), `a_blocked_standard_error_does_not_hold_the_link_up`; Go only: `TestAStandardOutputNobodyReadsFailsTheCommandOnceHungUp` (`net get` and a relay), `TestAStandardErrorNobodyReadsStopsNeitherTheRelayNorTheHangUp` (`-vvv` with the log's reader gone), `TestARunInterruptedWhileSIGINTIsIgnoredExitsWithItsStatus` (SIGTERM while SIGINT was ignored at start: the hang-up, then exit status 130), `TestASecondInterruptionSecondsAfterTheFirstQuitsAtOnce` (`net get http://web.test/` with standard output a filled FIFO nobody reads, which holds the run in writing the body; SIGINT once the head is out and another `lateRepeatGap`, 3 s, after the interruption's message: the warning with "(quitting at once)", no call summary, death by SIGINT — `quitInterrupted: dieInterrupted`; the Rust binary does the same, its output identical but for the program's name) and `TestTheProcessesVariablesStandInForTheirOptions` (with `SATLINK_DATA_NUMBER=12345`, `--simulate=instant net status` says "Dialling 12345 — "; with `SATLINK_PORT` a missing path, `info` fails with the serial library's open error). Slow pipes are FIFOs opened apart with raw descriptors (`O_CLOEXEC`), read and written without Go's poller, which on macOS misses a FIFO's end. |
| `src/logger.rs` | `cmd/satlink/logger.go`, `logger_test.go` | `stderrLog`: the standard-error handler, built on `internal/logtext`, hands each formatted line to a backlog of `logBacklog` (1000) lines, which one goroutine writes, started with the first line and ended by `close` at the end of `run`; lines that do not fit are dropped and counted, and `droppedLinesFormat` comes before the next line written; `flush` waits for the lines handed over before it, at most `flushLimit` (1 s), looking every `flushCheck` (10 ms); `writerBehind` is the CLI's own standard error, whose writes wait for the log lines handed over before them (section 4). `logger_test.go` adds: lines waiting for a blocked standard error without holding up the logger, lines beyond the backlog dropped and counted, `flush` giving up after a second, the CLI's own lines following earlier log lines (`TestTheCommandsOwnLinesFollowTheLogLinesMadeBeforeThem`, whose log `newCLI` builds and whose lines go through the CLI's `streams.stderr`, so that it pins `newCLI`'s wiring of `writerBehind` too; the test holds only the log line's write, `firstWriteHolder`, so that it fails every time without the wait), no wait behind a closed log, a closed log neither waiting nor panicking. |

### iridium-ffi → package `mobile`

| Rust | Go | |
|---|---|---|
| `Cargo.toml`, `uniffi.toml` | — | Feature `serial` → build constraint `!android && (!ios \|\| (macos && !maccatalyst))`: gobind reads gomobile's macOS target with `GOOS=ios` and the `macos` tag, gomobile compiles it with `GOOS=darwin` and the `macos` tag, and the Rust desktop bindings had the serial API. Package names → gomobile flags (`-javapkg io.github.anhydrous99.satlink`, `-prefix Satlink`). |
| `src/lib.rs` | `mobile/doc.go`, `mobile/error.go`, `mobile/transport.go`, `mobile/handset.go`, `mobile/lists.go`, `mobile/serial.go` | API |
| `src/link.rs` | `mobile/link.go`, `mobile/connection.go`, `mobile/tls.go`, `mobile/cancellation.go` (the `Cancellation` object) | API |
| `src/logging.rs` | `mobile/logging.go`, `logging_test.go` | API. Internal tests build the unexported bridge, one per test, without touching `slog.Default` (section 4). One logs a record through a logger made with `With("pkg", …)` and one carrying a `"pkg"` record attribute, and checks both targets and the text of an attribute; `TestASinkThatFallsBehindLosesRecordsRatherThanHoldingUpTheLogger` holds the queue to the 256 records that `SetLogSink` documents (Rust's `QUEUE_LIMIT`), written out in the test. |
| — | `boundary.go`, `convert.go`, `errormapping.go`, `goroutine.go`, `handsetslot.go`, `linkconfig.go`, `linkstate.go`, `listener.go`, `log.go`, `portcloser.go` | Internal: what every exported function shares as the boundary to the host app — panics recovered, nil and zero values refused (`boundary.go`); the saturating conversions of milliseconds, seconds and counts (`convert.go`); the error mapping (`errormapping.go`); named goroutines that recover their panics (`goroutine.go`); where an `IridiumHandset`'s handset is — here, lent or stranded (`handsetslot.go`); `LinkSettings` to satnet's configuration (`linkconfig.go`); the app's copies of the link's state and status (`linkstate.go`); the listener goroutine (`listener.go`); this package's own logging (`log.go`); the goroutine that closes a port once its call has ended (`portcloser.go`). |
| `tests/link.rs` | `mobile/link_integration_test.go` | `SimulatedUsb` is a `simtest.ByteTransport` over a `simtest.NewSimulator(t, sim.DefaultConfig())`; `StubbornUsb` one over a `simtest.FaultyTransport` with `DeafToEscape` and `DeafToDTR` on, `honour_dtr` being `DeafToDTR.Store(false)` and `panic_on_escape` `PanicsOnNextEscape.Store(true)`, which turns itself off like Rust's `swap(false)`. `NoDtrUsb` is `noDTRUSB`: a `simtest.ByteTransport` over a `simtest.FaultyTransport` over `struct{ satlink.Transport }{simulator}`, whose DTR changes fail "this transport has no DTR line"; `fail_next_hang_up` is `FailsNextHangUp` and `without_cpas` `WithoutCPAS`. `DataCallThatWillNotEnd` is `dataCallThatWillNotEnd`, a `mobile.ByteTransport` of its own, whose reads with nothing to give nap min(timeout, 10 ms), as Rust's do. These are in `support_integration_test.go`. Plus the link-level Go cases: a setup cancelled before it dials gives a lent handset back; cancelling a link that is up changes nothing; limits too small to be of use; `Reinitialize` stranding a ready handset whose call cannot be ended (a raw ATD over a stubborn handset, `TestReinitializeStrandsAReadyHandsetWhoseCallCannotBeEnded`); collected links (garbage collection), one with a listener that hears it close and a lent one that gives its handset back; a lent link's listener hearing every change; and two goroutines taking turns setting links up with one handset, which leave it usable (`TestSetupsTakingTurnsWithAHandsetLeaveItUsable`). |
| `tests/logging.rs` | `mobile/logging_integration_test.go` | Runs in a child process, as the Rust test binary did: the test, `TestRecordsAtOrAboveTheLevelReachTheSinkOnceSet` after the Rust test's name, re-executes `os.Args[0]` with `-test.run=^TestLogSinkChild$` and an environment marker, and the child (skipped without the marker) asserts the exact records and that a second `SetLogSink` fails. `-count=N` and other tests cannot interfere. |
| — | `export_test.go` | Go only, test only, package `mobile`, shared with the external tests: `ListenerPatience` (10 × `listenerPoll`); `SlowCallSetup` (5 s, the call setup of `TestALinkBeingSetUpCanBeCancelled` and `TestClosingAHandsetWhileItsLinkIsSetUpClosesItOnceTheCallEnds`); `CollectGarbageUntil` (`runtime.GC` every 50 ms until a condition holds, within 30 s); `HangUpAtCleanup`; `StateRecorder`, a `LinkListener` that records `DescribeState`'s texts; `AssertHeardEveryChange`, which waits for a close with any reason and then expects Up, Closing(Requested), Closed(Requested); `DescribeState` ("Closed(Requested)"). |
| — | `support_integration_test.go`, `connection_integration_test.go`, `handset_integration_test.go` | Go only, external: the helpers only the external tests use (those they share with the internal tests are in `export_test.go`); the TCP, UDP and TLS connection tests, over helpers that close the connection they open at the test's end (`openEcho` of `link_integration_test.go`, `openUDPEcho` and `openTLS`, through `closeAtCleanup`, which reports a failing `Close` as `closeConnection` does: `TestTheConnectionHelpersCloseTheirConnectionsAtTheTestsEnd`), `SendSome` and `Abort` among them (`SendSome` over TLS; a `SendSome` that queues part at once though its timeout would let room come for the rest, then a timeout with the send buffer full; `Abort` of a TLS connection, after which `ShutdownWrite` fails as no close_notify can go; what an aborted TCP connection had settled staying so: `WaitAcknowledged` and `ShutdownWrite` succeeding and `Receive` returning what had arrived, `TestAnAbortedConnectionKeepsWhatWasSettled`; `SendSome` and `Abort` after `Close`; TLS `Close` while a `Send` waits for room, serial and skipped under `-short`; a closed UDP connection; `UDPConnection.Receive(0, …)` taking a datagram and keeping none of it, in `TestUDPDatagramsAreEchoed`; `TestReceiveRefusesALengthItCannotHonour`); `IridiumHandset` over the simulator (status, SMS, `SatlinkErrorHandsetBusy`; a closed `IridiumHandset` refusing every method, also once a link that had it has ended; `TestClosingAHandsetLeavesTheAppsTransportAlone` checking the transport through a new `IridiumHandset`). |
| — | `error_test.go`, `transport_test.go`, `convert_test.go`, `zerovalue_test.go`, `lists_test.go`, `nilargs_test.go`, `boundary_test.go`, `cancellation_test.go`, `connection_test.go`, `handset_test.go`, `handsetslot_test.go`, `closingport_test.go`, `linkconfig_test.go`, `linkstate_test.go`, `listener_test.go`, `log_test.go`, `portcloser_test.go`, `tls_test.go`, `serial_test.go` | Go only, internal: the error mapping table (every satlink and satnet kind, `net.ErrClosed`, `*ParseRuleError`, a failed hang-up → Kind, fields and text; `ErrCallMayStillBeUp` → `SatlinkErrorHangUpFailed`; `ErrCancelled`, also inside `ErrHandset`, → `SatlinkErrorCancelled`; a cancelled setup whose hang-up failed → `SatlinkErrorHangUpFailed`; an establishing error with `CallMayStillBeUp` → `SatlinkErrorHangUpFailed` with the error's text, while the generic mapping keeps such an error's own kind; an `ErrLinkDown` from a connection keeps its Reason); a `Cancellation`, made or zero, that cancels, the link's call settings sharing it, and without one a link with its own (`cancellation_test.go`); `endStrandedCall`, whose failing `Initialize` or `ATH` becomes `ErrCallMayStillBeUp` with Rust's texts, and `asCallMayStillBeUp`, which keeps an error of that kind and rewords any other, the flag alone included (`handset_test.go`); `strand` taking only a ready handset (`handsetslot_test.go`); the transport adapter (milliseconds clamped to [1, 2147483647], copying at most `len(p)`, a host error becoming `ErrTransport` with its message, a panicking `ByteTransport` giving `SatlinkErrorInternal`; a panic once the call is up pulsing DTR, which ends the call over a transport whose DTR line reaches the handset, `TestAPanicOnceTheCallIsUpPulsesDTRWhichEndsTheCall`, and a call a panic left up, over a handset deaf to DTR, ended by a new `IridiumHandset` on the app's transport, `TestACallAPanicLeftUpIsEndedByANewHandsetOnTheAppsTransport`, the ByteTransport counterpart of `serial_test.go`'s `TestAPanicOnceTheCallIsUpClosesTheSerialPortWhichEndsTheCall`; `transport_test.go`); the saturation of timeouts and seconds (`convert_test.go`); every exported method on a zero receiver through `reflect` (no panic, "the %s was not made by this library"); `Get` out of range; every function with nil arguments; `receiveStream` over a fake stream — data, data with `io.EOF` or a truncation, the end of the stream, and the failures: a TLS session cut short (`SatlinkErrorTLSTruncated`, from a real satnet `ErrTLSTruncated`), a timeout, a closed connection, a deadline refused by a closed connection — `receiveBuffer`'s 64 KiB cap, written out in the test as Rust's `MAX_RECEIVE_LEN` rather than taken from `maxReceiveLen`, a stream buffer needing room for a byte, and a datagram buffer that may be empty but not negative (`connection_test.go`); `Close` of a handset while it is lent or dialling, during a real `EstablishLinkWithHandset`, and not waiting for a `PollEvents`; a fault in `reclaim` leaving the lock free; abandoning a setup taking only a handset being lent; a settled setup leaving the next setup's handset alone, whether its link did not come up or came up and ended (`TestASettledSetupLeavesTheNextSetupAlone`: the loan's own `returnedAfter` or `lentTo`, then the next setup's `lend` and the app's `Close`, all before the first setup's deferred `abandonUnlessSettled`; that `EstablishLinkWithHandset` defers the loan's cleanup only `TestSetupsTakingTurnsWithAHandsetLeaveItUsable` checks); the slots' names (`handsetslot_test.go`, over the port double of `closingport_test.go`, which carries its simulator's control: `newClosingPortWith(config)`, `connectPanickingPort`, `closePanickingPort`); the listener goroutine stopping once it has delivered `LinkStateClosed`, and a link that cannot be followed (a nil watcher) logging "the link listener panicked: …" (`listener_test.go`); `recordErrors`, a process-wide logger built on `internal/logtext` that keeps this package's error records, for tests that never call `t.Parallel`, with `TestRecordingErrorsLeavesTheLogPackageAsItWas` (`log_test.go`); a port closer whose port panics as it closes logging "the port closer panicked: a port bug while closing" while the app carries on (`portcloser_test.go`); `survivePanic` logging "recovered a panic in %s: %v" (`boundary_test.go`); the settings' conversion — a host address that is not IPv4 refused with the Rust bindings' reason, "invalid IPv4 address syntax", written out (`TestHostAddressesMustBeIPv4`), and a name added twice keeping its last address (`TestANameAddedTwiceKeepsTheLastAddress`) — and adders racing a link or a connector under `-race` (`linkconfig_test.go`, `tls_test.go`); the TLS settings, checked in Rust's order; the internal listener tests asserting the close reasons too, through `AssertHeardEveryChange`; `serial_test.go`, with `serial.go`'s build constraint: panics close the port, a collected serial link closes its port, a serial link's listener. |
| — | `bindings_test.go`, `unusableinits_test.go` | Go only, skipped under `-short` (section 4, Bindings check): builds the pinned `gobind` into `t.TempDir()`, runs it for Android, iOS, Mac Catalyst (serial API absent) and macOS (present) and fails on any skipped symbol; compiles the package for each target; checks that the Java declarations the Kotlin sources use exist — the establish functions with their `Cancellation`, `Cancellation()`, `IridiumHandset`'s `hangUp` and `reinitialize`, `SatlinkErrorHangUpFailed` and `SatlinkErrorInternal` among them — and that `ByteTransport.java` declares exactly the four methods `AndroidUsbTransport.kt` implements; checks which fields each generated `equals` compares (`TestTheJavaBindingsCompareExportedFieldsOnly`: none for `IridiumHandset`, `SatelliteLink`, the three connections, `TLSConnector`, `Cancellation` and the lists, `LinkSettings`'s seven exported fields, `TLSSettings`'s `TrustPublicRoots`), so that the package doc's Equality section is revisited if gobind changes; and, when a JDK is found through `JAVA_HOME` or `PATH`, compiles the generated Java with `javac --release 8` against a stub `android.content.Context`. `unusableinits_test.go` runs `scripts/mark-unusable-inits.sh` on the macOS header gobind writes and type-checks Objective-C against it with clang (section 4, Bindings check). |

### Go-only support packages

Module-internal packages shared by several of the packages above; their doc
comments are the contract.

| Package | Users | Contents |
|---|---|---|
| `internal/asciicase` | core, satnet, sim, `cmd/satlink` | `EqualFold(a, b string) bool`, `Lower(text string) string`, `Upper(text string) string`: Rust's `eq_ignore_ascii_case`, `to_ascii_lowercase` and `to_ascii_uppercase`, which change only 'A'–'Z' and 'a'–'z'. Go's `strings.EqualFold`, `ToLower` and `ToUpper` follow Unicode, which maps 'ſ', 'ı' and the Kelvin sign onto ASCII letters. The core matches the echo, `responsePrefix` and `mayGoOnline` with it, satnet host names and DNS answers, sim AT commands and DNS names, the CLI HTTP header names. Its tests cover those three letters, the bytes just outside the ASCII letter ranges ('@', '[', '`'), letters beyond ASCII and bytes that are not UTF-8; each user keeps a parity test of its own (the core's echo, satnet's and sim's Kelvin sign, sim's `ATſ12=50`, the CLI's `Tranſfer-Encoding`). |
| `internal/errnotext` | serial, `cmd/satlink` | `Text(errno syscall.Errno) string`: an error number's text as the C library's strerror(3) writes it, which is Go's with a capital first letter ("No such file or directory"), as the Rust version reported such errors: serial's `systemError` and the CLI's `osError` (which adds Rust's " (os error N)"). |
| `internal/logtext` | mobile, `cmd/satlink`; the record key and trace level for satnet, sim, mobile and ipstack's `netlog` | The one rendering of log records for people (section 2, Logging; `TestAHandlerFromLevelTraceShowsTheRecordsLoggedFourBelowDebug` pins the trace level): `LevelTrace` (`slog.LevelDebug - 4`); `PackageKey` (`"pkg"`); `LevelName(level slog.Level) string`; `NewHandler(minimum slog.Leveler, emit func(level slog.Level, pkg, text string)) slog.Handler`, a complete handler (`WithAttrs` and `WithGroup` included, safe for concurrent use; a nil `minimum` means `LevelInfo`, a nil `emit` panics) that renders each record and passes it to `emit` from the logging goroutine. The attributes are rendered by `slog.TextHandler` itself, so its quoting holds exactly. |
| `internal/ringprovider` | satnet, sim | What the port takes from rustls's ring provider, `rustls::crypto::ring::default_provider()`, which iridium-net's TLS client and iridium-sim's TLS echo both used: `TLS12CipherSuites() []uint16`, its six TLS 1.2 suites (ECDHE-ECDSA and ECDHE-RSA with AES-256-GCM, AES-128-GCM and ChaCha20-Poly1305), and `KeyExchangeGroups() []tls.CurveID`, X25519, P-256 and P-384, each call a list of its own, as a `tls.Config` keeps the list it is given. satnet's client offers them and sim's echo serves them (section 4, TLS configuration and sim); its tests hold rustls 0.23.46's lists as literals. |
| `internal/rustdebug` | core, satnet, sim, mobile, `cmd/satlink` | Rust's `Debug` text of the values that messages print, so that they read as the Rust ones did: `Quote(text string) string`, Rust's `{:?}` of a `str`, for every message that quotes a string (section 2, Errors), and `IPv4Settings(local, peer netip.Addr, dnsServers []netip.Addr) string`, Rust's derived `Debug` of `ppp::Ipv4Settings` for the "IPv4 up" log lines, `Ipv4Settings { local_address: 10.64.0.2, peer_address: Some(10.64.0.1), dns_servers: [10.64.0.1] }`. It takes the settings' fields, so that the package imports nothing of the module's and the core can use it. `Quote` follows the rules of rustc 1.99.1's `char::escape_debug_ext`, with the Unicode properties taken from Go's tables: a character that is not printable, extends a grapheme or is default-ignorable is written `\u{…}`, the default-ignorable ones through `isWrittenAsCodePoint`'s check of `Other_Default_Ignorable_Code_Point` (Go's tables have no Default_Ignorable_Code_Point, and the rest of it is already escaped as format characters or variation selectors), but for the halfwidth katakana sound marks U+FF9E and U+FF9F, which Rust writes as they are. Compared with rustc 1.99.1 over every Unicode scalar value and 200,000 random strings, both at Unicode 17.0.0, it writes the same text. `TestQuoteWritesAStrAsRustsDebugWritesIt` holds rustc 1.99.1's texts as literals, the Hangul fillers and the sound marks among them; `TestQuoteWritesAByteThatIsNotUTF8AsAnEscape` pins `Quote`'s own form for a byte that no `str` can hold: `\xNN` in lower case, as `u8::escape_ascii` writes it, beside a valid U+FFFD written as it is, with the text after it quoted as usual. |
| `internal/utf8lossy` | core, ppp, satnet (the labels of DNS answers), sim, `cmd/satlink` | `String(data []byte) string`, Rust's `String::from_utf8_lossy`: one U+FFFD for each maximal subpart of an ill-formed sequence, where `strings.ToValidUTF8` would replace a whole run with one. |
| `internal/memconn` | sim | An in-memory `net.Conn` for crypto/tls, `Conn` (`New(local, remote netip.AddrPort)`), with a pump side for its owner: `Inject`, `CloseInbound`, `Extract`, `Pending`, `AwaitReader(timeout)`, `EOFDelivered`, `Closed` (section 4, sim); the broadcast it waits on (`signal.go`); and `tls_test.go`, a crypto/tls client and server pumped step by step over two `Conn`s. Its tests run with `-race`. The tests of what wakes `AwaitReader` (a `Read` that starts waiting, and `Close` after the FIN) run in `testing/synctest` bubbles: there an `AwaitReader` that was woken returns with no fake time passed, while one that gave up after its timeout returns that much later with the same result, since `AwaitReader` looks once more before it gives up. The two halves of its rule — the bytes, and the FIN, that a woken `Read` has yet to take — are tested by holding that `Read` still, as a test cannot catch the moment from outside (`TestAwaitReaderWaitsUntilTheReaderHasTakenWhatWasInjected`; the internal helpers `bufferWithoutWaking`, `closeInboundWithoutWaking` and `wakeWaitingReads`); every receive from a test goroutine goes through `awaitResult`, bounded by `patience`, so that a regression fails by name instead of hanging `go test`. |
| `internal/simtest` | satnet, mobile, `cmd/satlink` (their internal and external tests) | Test support for their tests: `EscapeGuard` (20 ms), `CallEscapeGuard` (60 ms) and `SimulatorConfig()`; `NewSimulator(t, config)` (closed by `t.Cleanup`), `NewHandset(t, config) (*satlink.Handset, *sim.Control)`, `InitializedHandset(t, transport)`; `FaultyTransport` (`NewFaultyTransport(transport)`, the switches `DeafToEscape`, `DeafToDTR`, `PanicsOnEscape`, `PanicsOnNextEscape`, `PanicsOnNextRead`, `FailsNextHangUp`, `WithoutCPAS` and `Unplugged` as `atomic.Bool` fields, `DTRDrops()`: the Rust tests' `Faulty`, `StubbornUsb` and `NoDtrUsb` in one, where the switches whose names say `Next` turn themselves off once they have acted); `ByteTransport` (`NewByteTransport(transport)`, a `mobile.ByteTransport` by its methods alone); `Eventually(t, timeout, what, condition)`; the "this transport has no DTR line" helper both transports share (`dtr.go`). Its tests run with `-race`. sim's internal tests cannot import it (import cycle); it imports neither satnet nor mobile. |

Tests that hand-build IPv4, TCP or UDP datagrams (satnet's pacing, device
and stack, sim's connection requests, DNS and support) build them with
`internal/ipstack/wire`'s Reprs rather than with builders of their own.

---

## 2. Conventions

These hold in every package. A reviewer should be able to predict a name or a
signature from them.

### Names

- Go initialisms: SMS, URC, IMEI, IMSI, AT, TCP, UDP, DNS, TLS, IP, IPv4, MTU,
  MRU, ACCM, PPP, LCP, IPCP, PAP, CHAP, MD5, USB, ID, DTR, RTS, ALPN, SHA256,
  URL, HTTP, PEM, DER.
- No stutter: `SimulatedHandset` → `sim.Handset`, `SimulatorConfig` →
  `sim.Config`, `LinkConfig` → `satnet.Config`, `SatelliteLink` →
  `satnet.Link`, `TcpStream` → `satnet.TCPConn`.
- Product renames: binary `satlink`, environment `SATLINK_PORT` and
  `SATLINK_DATA_NUMBER`, `linux/99-satlink.rules`, goroutine and log names
  `satlink-link`, `satlink-link-listener`, `satlink-log` and
  `satlink-port-closer`, the CHAP authenticator name `satlink-ppp` (was
  `iridium-ppp`), the CLI's `User-Agent: satlink-cli` and the `satlink
  hangup` its hang-up warning suggests. Hardware facts keep their Iridium
  names (`IridiumUSBVendorID`, `IridiumInternetNumber`, `IridiumHandset` in
  mobile, "Iridium 9575", the simulated network's greeting, the test CA's
  name).

### Enumerations

- A fieldless Rust enum is `type X int` with constants named `X<Variant>`
  (`satlink.SMSFilterUnread`, `ppp.PhaseOpen`, `ppp.AuthProtocolCHAPMD5`) and a
  `String` method. A value without a constant is refused where it is an input
  (`Handset.ListSMS`, `ppp.NewEndpoint`; section 5).
- A data-carrying Rust enum is a struct with a `Kind` field of type `XKind`
  plus the payload fields, comparable with `==`: `satnet.LinkState{Kind:
  satnet.LinkStateClosed, Reason: satnet.CloseReason{Kind:
  satnet.CloseReasonCarrierLost}}`. Kind constants are named after the
  struct: `ppp.EventUp`, `ppp.DownReasonLoopedBack`, `satnet.LinkStateUp`,
  `satnet.CloseReasonPPP`, `satlink.FinalResultNoCarrier`. Payload fields
  are named after their meaning: `Protocol`, `Detail`, `Reason`, `PPP`.
- The exception: `ppp.Event` carries `ppp.IPv4Settings`, whose `DNSServers`
  is a slice, so neither is comparable with `==`; compare them with
  `Event.Equal` and `IPv4Settings.Equal` (as ipstack's `TCPRepr.Equal`).
  Records that hold pointers compare them by pointer with `==`
  (`satnet.Status.IPv4`, `ppp.Config.Keepalive` and a server role's policy),
  and their docs say so.
- Where the Rust value is a protocol code, the Go value is the code:
  `satlink.ChargeStatus` holds the +CBC code, so `Unknown(n)` needs no field;
  `RegistrationStatus` constants are the 3GPP codes 0–5, and every other code
  becomes `RegistrationStatusUnknown`, as in Rust.
- `String()` returns the Rust `Display` text where the Rust type has one
  (`DownReason`, `CloseReason`), otherwise the constant's name without its
  prefix, which is what the Rust CLI printed with `{:?}` (`OnBattery`,
  `Unknown(3)`, `Home`). Unknown values print as `TypeName(n)`. Where a log
  line printed `{:?}` of a struct, `internal/rustdebug` writes Rust's text.
- Configuration choices that callers build but never inspect are opaque
  values made by constructors (`ppp.ClientRole`, `ppp.ServerRole`), so no
  invalid state can be expressed.
- gomobile cannot bind named integer types: in `mobile`, enumerations are
  untyped `int` constants named after the Rust FFI enum
  (`mobile.LinkStateUp`, `mobile.SatlinkErrorPolicyDenied`).

### Optional values

One rule, applied everywhere:

| Rust | Go | Examples |
|---|---|---|
| `Option<T>` where the zero value cannot be a real value | the zero value, documented at the field | SMS `Timestamp ""`, `Registration.CellID ""`, `ppp.IPv4Settings.PeerAddress` (zero `netip.Addr`), `serial.Port.Model ""`, `Limits.MaxBytes 0`, `Tuning.TCPKeepalive 0`, `sim.LinkProfile.BytesPerSecond 0` (unlimited) |
| `Option<Struct>` | pointer, `nil` for none | `DataCallSettings.Bearer`, `ppp.Config.Keepalive`, `ServerConfig.Authentication`, `sim.Config.Authentication`, `satnet.Status.IPv4` |
| `Option<T>` result of a lookup or poll | `(T, bool)` | `Endpoint.PollEvent`, `ReceiveIPv4`, `NextTimeout`, `IPv4`, `ModelForProductID`, `Watcher.NextStateChange`, `WaitClosed` |
| `Option<T>` that may legitimately be the zero value | a presence `bool` next to the value | `Response.Connected` + `ConnectMessage` (a bare CONNECT has an empty message) |
| `Option<Instant>` deadline | `time.Time`, zero meaning none | `TCPConn.WaitAcknowledged`, net.Conn deadlines |

Where Rust rejects `Some(0)` as invalid (limits), Go has no way to say it and
rejects negative values instead; zero means "no limit". mobile has its own
rule for results (section 4): gomobile turns nil into null, so only fields
documented as optional are ever nil.

### Constants and defaults

- Every Rust constant keeps its exact value and its comment, as a named
  constant, internal constants included: no Rust `const` becomes an inline
  literal, and defaults that Rust builds from constants are built from
  theirs (`sim.DefaultConfig` from `defaultModel`, `defaultIMEI`, …; ppp's
  `DefaultTimers` and `DefaultKeepalive` from `defaultRestart`,
  `defaultKeepaliveInterval`, …). A Rust `Default` impl that is itself a
  struct literal stays one: `satnet.DefaultTuning` and `DefaultLimits` keep
  Rust's reasons in their field and function docs.
- Struct-typed Rust constants are functions returning a fresh value, so no
  caller can change them for everyone: `satnet.IridiumInternetBearer()`,
  `satnet.AllPorts()`, `sim.InstantLinkProfile()`, `sim.IridiumLinkProfile()`,
  `satlink.KnownProducts()`, `sim.SecureTestKeySHA256()`.
- A Rust `Default` impl is `Default<Type>()`: `DefaultDataCallSettings`,
  `ppp.DefaultTimers`, `ppp.DefaultKeepalive`, `satnet.DefaultLimits`,
  `satnet.DefaultTuning`, `sim.DefaultConfig`, `sim.DefaultNetworkConfig`.
- A Rust constructor taking the one required input is `New<Type>(input)`:
  `satnet.NewConfig(policy)`, `ppp.NewConfig(role)`. Constructors that
  validate return an error (`ppp.NewEndpoint`, `satnet.NewTLSConnector`). A
  Rust `FromStr` is `Parse<Type>(text)`: `ParseAllowRule`, `ParsePortRange`.

### Zero values and interfaces

- Every type says what its zero value is: useful (`satnet.Policy`,
  `AllowRule` and `PortRange` allow nothing; `DataCallSettings` documents its
  zero timeouts), or not usable, naming what makes one ("The zero TCPConn is
  not usable; Link.Dial and Link.DialContext make one"). A zero `AllowRule`'s
  target kind is `targetNone`, which matches nothing.
- A constructor given a nil required interface panics (`satlink.NewHandset`);
  mobile, whose callers are other languages, refuses nil arguments with an
  error instead (section 4).
- A type that promises an interface asserts it at compile time, so that an
  optional interface such as `DTRController` cannot silently stop matching:
  `var _ satlink.DTRController = (*Handset)(nil)` in sim, serial and for
  `DataCall`; `var _ net.Conn = (*TCPConn)(nil)` in satnet; and so on for
  mobile's internal transport adapter.

### Errors

One pattern in every package:

- The error type is a struct with a `Kind` field plus the fields the kinds
  need: `*satlink.Error`, `*satnet.Error`, `*ppp.ConfigError`,
  `*ppp.SendError`, `*satnet.ParseRuleError`, `*mobile.SatlinkError`.
- The kind type implements `error`, so every kind constant is a sentinel:
  `errors.Is(err, satlink.ErrTimeout)`, `errors.Is(err, satnet.ErrLinkDown)`,
  `errors.Is(err, ppp.ErrNotOpen)`. Kind constants are named `Err<Variant>`
  (`satnet.ErrRule<Variant>` for rule parsing). A kind's own text is a short
  label; the error's text has the details. (`mobile` binds no named types, so
  its kinds are int constants and `AsSatlinkError` recovers the details.)
- `Error()` texts are the Rust `Display` texts, word for word. Rust's `{:?}`
  of a string is `internal/rustdebug`'s `Quote`, which escapes as Rust
  does: `"\u{b}"` where Go's `%q` writes `"\v"`, `"\u{1b}"` for `"\x1b"`,
  `"\0"` for `"\x00"`, and `\u{…}` for the marks that extend a grapheme
  and for the default-ignorable letters, such as the Hangul fillers, which
  `%q` writes as they are. The core's command and number texts quote with
  it, as do satnet's host and rule texts, mobile's host text, the CLI's URL,
  target and header texts — the URL check refuses whitespace and control
  characters, and its reason quotes them — and clap's quoted values.
  Go-only messages quote with it too, so that they read as the Rust ones
  beside them; in the texts this guide quotes, `%q` stands for it. Rust's
  `{:?}` of a `Vec<String>` becomes satlink's `debugList`, `["a", "b"]`
  (section 4), built on `Quote`.
- `Unwrap` returns the cause: a transport's error inside `satlink.ErrTransport`,
  the `*satlink.Error` inside `satnet.ErrHandset`, the crypto/tls or x509
  error inside `satnet.ErrTLS`.
- `satnet` errors also match the standard library's equivalents of the Rust
  `io::ErrorKind` conversion (table in the satnet package doc):
  `os.ErrDeadlineExceeded` for timeouts, `syscall.ENOTCONN` for a link that
  is down (Rust's `NotConnected`), `os.ErrPermission` for policy denials,
  `os.ErrInvalid` for invalid input, `syscall.ECONNRESET`,
  `syscall.ECONNREFUSED`, `io.ErrUnexpectedEOF` for TLS truncation. Two
  look-alikes are deliberately not matched, as the net package keeps them for
  other meanings: `net.ErrClosed` (this side closed the connection; mobile
  maps it to "the connection is closed") and `context.DeadlineExceeded` (a
  context ended; `Link.DialContext` reports that as `&net.OpError{Op: "dial",
  Net: network, Err: ctx.Err()}`, as `net.Dialer` does). Timeouts are
  `net.Error`s with `Timeout()` and `Temporary()` true.
- A failed hang-up — Rust's `call_may_still_be_up` of `CallFailure` and
  `LinkFailure` — is a flag on the error of whatever failed: the
  `CallMayStillBeUp` field of `*satlink.Error` and `*satnet.Error`. The flag
  never changes the text.
- Rust's `Error::CallMayStillBeUp(String)` ("the call may still be up: …"),
  which `Handset::initialize` and `Handset::answer` return, is the kind
  `satlink.ErrCallMayStillBeUp`, with the text in `Message` and the flag set
  too. The kind is also the one sentinel for the whole module:
  `errors.Is(err, satlink.ErrCallMayStillBeUp)` matches an error of that
  kind or with the flag, through any wrapping; `Error.Is` checks the flag
  before the kinds. Rust's `From<CallFailure> for Error`, which turns a
  failure whose call may still be up into `Error::CallMayStillBeUp` for
  `?`, has no Go form: an error passed on keeps its own text and flag.
- mobile maps the kind to `SatlinkErrorHangUpFailed`, as Rust's
  `From<core::Error>` does, and the flag only where the bindings check it;
  an `IridiumHandset` whose handset returns an error of that kind is
  stranded, as Rust's `with` does — the check is on the kind
  (`isCallMayStillBeUp`), not the flag (section 4, mobile).
- Rust's `Error::Cancelled` is `satlink.ErrCancelled`, "the call setup was
  cancelled", which is not `context.Canceled`: a context ending is not what
  stopped the call, a `Cancellation` is (below).
- Transports may return any error; the handset wraps it as
  `&satlink.Error{Kind: ErrTransport, Err: err}` and passes a `*satlink.Error`
  through unchanged.
- Never ignore an error silently: where Rust writes `let _ = …`, Go writes
  `_ = …` with a comment saying why it is safe.

### Cancellation

- Rust's `Cancellation`, an `Arc<AtomicBool>` in `DataCallSettings` that the
  app clones to cancel with, is a `*satlink.Cancellation` in
  `DataCallSettings.Cancellation`. Copies of the settings share the pointer,
  as Rust's clones share the `Arc`; `DefaultDataCallSettings`, and so
  `satnet.NewConfig`, makes a fresh one each time, so that two defaults are
  not `==`, as Rust's `PartialEq` on a `Cancellation` compares pointers.
- A nil `Cancellation` means that the setup cannot be cancelled: `Cancelled`
  on nil is false, and `Cancel` on nil panics, so that a cancel that cannot
  take effect never passes silently. The zero value is ready to use, so that
  mobile's gomobile type can hold one by value and pass its address.
- A context drives it: `stop := context.AfterFunc(ctx,
  settings.Cancellation.Cancel)`, then `stop()` once the call is set up.
- satnet's `Establish` and driver read `config.Call.Cancellation`; the CLI's
  interruption handler cancels it; mobile has a `Cancellation` object of its
  own, holding one.

### Time

- Sans-I/O code (ppp, ipstack, the simulator's modem, pacing, the name cache)
  takes `now time.Time` and never reads the clock. Only the I/O edges call
  `time.Now()`, exactly where Rust calls `Instant::now()`; monotonic
  readings play `Instant`'s role.
- Blocking calls take `time.Duration` timeouts, as in Rust. A read with a
  timeout of zero or less only takes what has already arrived: a
  transport's `ReadTimeout`, a connection's read with a deadline of now. A
  handset command whose timeout is zero or less is sent and fails with
  `ErrTimeout` at once, as in Rust (`Handset.SetTimeout`). The longest timeout
  (`math.MaxInt64`) means no deadline: `time.Time.Add` drops the monotonic
  reading instead of overflowing, so `time.Now().Add(timeout)` is safe
  (Rust's `deadline_after` returns `None` for it). A loop that waits for
  the handset reads at most a second at a time and looks at its deadline
  again (Rust `deadline.rs`, `LONGEST_READ`), so a transport is never
  handed an absurd timeout; satnet's engine does the same with `MAX_WAIT`.
  Conversions from milliseconds or seconds (mobile, the CLI's options)
  saturate at `math.MaxInt64` instead of overflowing, so that an absurd
  value meets the "at most a year" check rather than turning negative.
- Configured durations keep Rust's guard: at most a year (and, in Go, not
  negative).

### Integers

Counts, levels, indices and sizes are `int`; ports `uint16`; USB IDs `uint16`;
the ACCM `uint32`; the MRU `uint16`; byte and frame counters `uint64`. In
`mobile`, gomobile binds no unsigned types. Its Objective-C headers declare a
Go `int` as `long`, but gobind's C glue (`typedef int nint` in
`seq_darwin.h`) narrows it to a 32-bit C `int` on every Apple platform; on
Android it is a `jlong`, and a Go `int` has 32 bits on armeabi-v7a. A larger
value wraps before Go sees it: so timeouts, the link settings' seconds and
byte counts are `int64`, and the remaining `int` values (lengths, ports,
indices, levels, enumerations) never need more than 31 bits.

### Ownership: the handset is never moved

Rust moves the `Handset` into a `DataCall` or `SatelliteLink` and hands it back
(`CallFailure`, `LinkFailure`). In Go the caller keeps its `*satlink.Handset`:

- `Handset.DialData`/`AnswerData` claim the handset; while a `DataCall` (and
  so a `satnet.Link`) holds it, the handset's methods that talk to it fail at
  once with `ErrBusy`, `TakeURCs` returns nothing, and `SetTimeout`,
  `Transport` and `Close` keep working.
- Hanging up releases it, whatever the outcome: `DataCall.HangUp`, a failed
  dial, a link that fails to come up, a link that hangs up by itself. A
  `satnet.Link` releases it as soon as the call has ended.
- `CallFailure` and `LinkFailure` are plain errors, and their
  `call_may_still_be_up` is the errors' `CallMayStillBeUp` (section 2,
  Errors), set exactly where Rust sets it:
  - every error of `DataCall.HangUp` (but "the data call is over" for a
    call already hung up), and every error of `Handset.EndDataCall` but the
    `ErrInvalidArgument` of a guard over `MaxEscapeGuard` and `ErrBusy`;
  - a failed `DialData` or `AnswerData` whose abandoned attempt might have
    connected and could not be ended — an attempt that timed out, was
    cancelled or lost the transport; a call found up while preparing
    (`data_call.rs:258-276`); a voice call answered by mistake that ATH
    could not end: the error is then the hang-up's, as in Rust
    (`data_call.rs:181-187`);
  - the errors of kind `ErrCallMayStillBeUp`: `Initialize`'s "the handset
    does not answer (%v), and ending a data call it may be in failed: %v"
    and "ATH failed on a data call left up: %v", and `Answer`'s "ATA
    answered a data call that could not be ended: %v"
    (`handset.rs:196-211, 312-322, 499-508`);
  - `Link.Close`'s error when the hang-up failed, wrapping the handset's
    (`ErrHandset`), or after the hang-up itself panicked (`ErrLinkDown`,
    `CloseReasonInternal` "the link's driver thread panicked");
  - `Establish`'s error when the link did not come up and its hang-up
    failed too: the error still says why the link did not come up, as in
    Rust — for a setup cancelled before or after CONNECT, `ErrHandset`
    wrapping `satlink.ErrCancelled` (`link.rs:102-116`).

  `Link.Status().CallMayStillBeUp` says so too. mobile reports
  `SatlinkErrorHangUpFailed` for `HangUp`, for an `Establish…` that failed
  so, and for the handset's errors of kind `ErrCallMayStillBeUp`: from
  `Initialize` inside `NewIridiumHandset`, `OpenSerialHandset`, the
  establish functions and `Reinitialize`, and from `Answer`. A lent or ready
  `IridiumHandset` is then stranded until `Reinitialize` succeeds (section
  4, mobile). The advice in every doc is Rust's: retry with
  `Handset.EndDataCall` once the handset answers again; power-cycling it is
  the last resort. (Closing the port drops DTR, which a failed hang-up has
  already tried: by its DTR fallback, or, when a transport's panic cut it
  short, by the pulse that panic sets off; section 4, Hanging up.)

### Drop becomes Close

| Rust `Drop` | Go |
|---|---|
| `SimulatedHandset` drops DTR | `sim.Handset.Close` (idempotent); a `Handset` collected without `Close` drops DTR the same way (`runtime.AddCleanup` on the `Handset`) |
| `SerialTransport` closes the port (DTR drops) | `serial.Transport.Close`, which waits up to a second for a write or DTR change under way; `satlink.Handset.Close` closes any `io.Closer` transport; a `Transport` collected without `Close` closes its port all the same (`runtime.AddCleanup` on the `Transport`, holding the library's port, as for `sim.Handset`; `Close` stops it). `serial.Connect` closes the port it opened on every way out but success, a panic out of `Initialize` included (a deferred close guarded by `initialized`), and a port that mobile opened itself (`OpenSerialHandset`, `EstablishSerialLink`) is closed too when a panic is recovered during the setup (`closeUnlessHandedOn`), as Rust's unwinding dropped the `SerialTransport` (`iridium-serial` `lib.rs:132-137`; serialport's `TTYPort::drop` closes the descriptor). |
| `DataCall` holds the handset and its transport, so dropping it closes a serial port | A `DataCall` dropped without `HangUp` ends nothing and leaves the handset busy; its doc says to hang up in a deferred function. `Handset.Close`, or the collection of the serial `Transport`, closes the port. A transport's panic out of `HangUp` or out of a call's setup, which in Rust unwound through the moved handset and dropped its transport, pulses DTR (section 4, Hanging up). |
| `SatelliteLink` hangs up in the background | `satnet.Link.Close` waits; `RequestClose` does not; a Link collected without Close still hangs up (`runtime.AddCleanup`) |
| `TcpStream` closes gracefully | `TCPConn.Close` (graceful, never waits); collected without Close: closed the same way |
| `UdpSocket` goes once its queue is sent | `UDPConn.Close` |
| `TlsStream` closes its TCP stream | `TLSConn.Close` (close_notify within the write deadline, then graceful TCP close) |
| UniFFI object destruction (Kotlin `.use {}`) | `Close() error` on every mobile object that holds resources — `IridiumHandset`, `SatelliteLink` and the three connections — releasing it without waiting, idempotent. `TLSConnection.Close` releases the connection; the FFI's `TlsConnection.shutdown_write(timeout_ms)` is `ShutdownWrite(timeoutMillis)`, as for `TCPConnection`. A closed `IridiumHandset` refuses every method but `Close` with `SatlinkErrorInvalidArgument` "the handset is closed", as the connections refuse with "the connection is closed" and UniFFI refused calls on a destroyed object, also once a link that had its handset has ended; `TakeEvents` returns an empty list. A new `IridiumHandset` on the transport talks to the handset again. gomobile objects are not `AutoCloseable`: apps call `close()` in `finally`. |

Use after `Close` returns `net.ErrClosed` for connections, and `Close` wakes
the calls already waiting in other goroutines with it, as net.Conn requires.
`TLSConn.Close` wakes them when its TCP connection closes, once the
close_notify has gone or the write deadline has passed — with no write
deadline and a stalled peer, that can take as long as the link lives, as
Rust's close consumed the stream; a write deadline of now closes at once,
as mobile's `TLSConnection.Close` does. A `DataCall` used after `HangUp`
fails with `ErrCall` "the data call is over"; a closed `sim.Handset` or
`serial.Transport` fails reads, writes and DTR changes.

### Concurrency

- Rust `Mutex<Engine>` + `Condvar` → `sync.Mutex` plus a broadcast channel
  that is closed and replaced on every notify, so a waiter can `select` on it,
  a timer, and a context. Rust's periodic re-check (`MAX_WAIT`, one
  second) stays. The waiting loop is written once (`waitUntil`, with
  `waitFor` for a `Watcher`) and reused.
- Rust threads become goroutines, labelled with `runtime/pprof` labels and
  named in logs (`satlink-link`). A panic in the link driver is recovered into
  `CloseReasonInternal`: a call the panic kept from being hung up is still
  hung up; a hang-up that itself panicked (a transport bug) is not run
  again — the core pulsed DTR as the panic left it, which ends the call
  after AT&D2 — and the link reports `CallMayStillBeUp`, as nothing
  confirmed the end.
- Every exported type documents whether it is safe for concurrent use. The
  net.Conn types are safe for concurrent use by multiple goroutines, as
  net.Conn requires (crypto/tls and net/http rely on it, closing a connection
  to interrupt a handshake or a request). `go test -race` passes.
- The typed atomics (`atomic.Int64`, `atomic.Uint64`, `atomic.Bool`,
  `atomic.Pointer[T]`), never `atomic.LoadInt64` and friends on plain fields:
  gomobile builds android/arm, where a 64-bit atomic on a field that is not
  8-byte aligned panics. `portability_test.go` type-checks every package and
  its tests for linux/386 and linux/arm; running the tests on such a
  platform (`GOARCH=386 go test` on the packages without cgo) needs a
  machine that runs its binaries.

### Logging

Producers: `log/slog` through `slog.Default()`, looked up at log time as
Rust's `log` facade is, with each record's `"pkg"` attribute naming the
package, the Rust log target (`satnet`, `sim`, `ipstack`, and `mobile` for
the panics it recovers where no caller can be told; core, ppp and serial do
not log). Every logging package has one helper with the same mechanism:
level functions that check `Enabled`, then call `slog.Default().Log(ctx,
level, msg, logtext.PackageKey, <package>, args...)` (`satnet/log.go`,
`sim/log.go`, `mobile/log.go`, `internal/ipstack/internal/netlog`), so that
`pkg` is always a record attribute, under the key the consumers read
(`internal/logtext`'s `PackageKey`, `"pkg"`). satnet and sim keep Rust's
texts in the message, formatted at the call site — so a line costs its
formatting even when its level is disabled; only the record is not built —
with Rust's `{:?}` of a struct written by `internal/rustdebug`; ipstack
passes values as attributes (PORTING-ipstack.md, section 7). Levels: trace
= `slog.LevelDebug-4` (`logtext.LevelTrace`, which `netlog.LevelTrace`
equals), debug, info, warn, error.

Consumers render every record the same way, implemented once in
`internal/logtext` (section 1) and used by the CLI and `mobile.SetLogSink`:

- pkg: the record's `"pkg"` attribute, or one added through `WithAttrs`
  (`slog.Default().With("pkg", …)`); empty when there is none;
- text: the message, then ` key=value` for every other attribute in order,
  with `slog.TextHandler`'s quoting and groups as `group.key`;
- level names: `ERROR` at error and above, `WARN`, `INFO`, `DEBUG`, and
  `TRACE` below debug (slog itself would say `DEBUG-4`);
- the CLI prints `fmt.Sprintf("[%-5s %s] %s", name, pkg, text)` to standard
  error, as Rust's `[{:<5} {}] {}`, from warn (no option), info (`-v`), debug
  (`-vv`) or trace (`-vvv`) up;
- mobile maps the same thresholds to `LogLevelError` … `LogLevelTrace` and
  hands the sink (level, pkg, text).

### Tests

- Every Rust test is ported with the same scenario and assertions, under a Go
  name (`TestHangUpEscapesToCommandModeThenHangsUp`), table-driven where cases
  share a shape, with `t.Helper` helpers and `t.Cleanup`.
- Sans-I/O tests use explicit `now` values (`base.Add(…)`), never sleeps.
  Elsewhere, conditions are awaited with deadlines instead of sleeps (for
  example, satnet's `a_connect_cut_short_by_the_call_dropping…` waits until
  `control.Stats().DataBytesFromApp` exceeds its value from before the dial —
  the SYN is on the line — instead of sleeping 300 ms before `DropCall`, and
  `a_bulk_upload_does_not_hold_up_a_later_connection` waits with
  `simtest.Eventually` until 1000 more bytes have left the app, instead of
  Rust's 1 s sleep). Rust's 200 ms sleep in
  `one_thread_reads_while_another_writes` and its 50 ms sleep in
  `a_requested_close_lets_waits_already_under_way_finish` become
  `satnet.AwaitWaitingCall`: the test acts once the reader waits. Where
  snapshot 3's tests sleep before acting, the Go tests wait for what the
  sleep stood for, as the test-shape table below lists.
- A test that checks that something wakes a waiting call — `Close`, a moved
  deadline, a cancelled context, a state change — first waits until the
  call waits: satnet's `AwaitWaitingCall` for link calls, the TLS fake's
  `awaitWaitingCall` for `TLSConn` calls; in sim and `internal/memconn`,
  `synctest.Wait` in a `testing/synctest` bubble (sim's
  `TestCloseEndsAReadInProgress` and `TestAnSMSFromAnotherGoroutineReachesAWaitingApp`,
  memconn's `AwaitReader` tests), where fake time also tells a call that
  was woken (no time passed) from one that timed out. sim's
  `TestCloseEndsAReadInProgress` gives its read a finite timeout
  (`handsetPatience`), so that a regression fails instead of deadlocking
  the bubble, which would end the test binary, and holds the read's end to
  the 2 ms poll interval that `ReadTimeout`'s doc promises
  (`promisedPollInterval`, as the core's `longestPromisedRead`); memconn's
  `TestReadsWaitingSideBySideAllEndWithClose` waits until both reads count
  as waiting (`awaitWaitingReaders`), which `AwaitReader`, satisfied by one,
  cannot tell. Started in a goroutine and closed at once, the call nearly
  always starts after the `Close` and fails at its entry check, so the
  wake-up is never exercised; with `GOMAXPROCS=1` it never is.
- A test returns with no call up and no driver running. The shared helpers
  register their cleanups so that they run in the right order (last in,
  first out): the simulated-handset helper (`simtest.NewSimulator`, also
  behind `simtest.NewHandset`) registers `t.Cleanup(simulator.Close)` first,
  then the link helper `t.Cleanup(func() { _ = link.Close() })`, so that the
  link hangs up cleanly before the port closes, which also stops any driver
  left; mobile helpers register `t.Cleanup(func() { _, _ =
  link.HangUp(10_000) })`; connection helpers close their connections. A
  test that leaves a call up on purpose (a handset deaf to the escape and to
  DTR) relies on the simulator's `Close`, which drops DTR from inside, to
  end it.
- A link whose test ends with data queued that need not arrive uses a
  one-second close grace (satnet's `linkClosingBriefly` and `tlsLink`): the
  cleanup's `link.Close` waits for the hang-up, which would otherwise wait up
  to the default 30 s for that data, where Rust's tests dropped the link and
  let it hang up in the background. A handset made deaf to the escape gets
  `SetTimeout(500 * time.Millisecond)` (satnet's `faultyHandset`, as Rust's
  `faulty_handset`), so that its hang-up does not wait out the default
  five-second timeout for an answer to `ATH`.
- `t.Parallel` only on instant links and timing-insensitive tests. Tests that
  assert real-time behaviour run serially and, when they take seconds, are
  skipped under `testing.Short()`: satnet's `early_retransmissions_cost_no_airtime`,
  `the_data_limit_hangs_up`, `a_bulk_upload_does_not_hold_up_a_later_connection`,
  `a_lookup_does_not_wait_behind_busy_connections` (about 4 s),
  `a_waiting_read_sends_on_what_a_timed_out_write_left` (about 11 s) and the
  Go-only close_notify test; core's
  `a_handset_still_relaying_data_after_dtr_drops_is_reported_promptly`
  (under `-short`, `TestEndDataCallThatFailsSaysTheCallMayStillBeUp` still
  pins `EndDataCall`'s `CallMayStillBeUp`); sim's
  `a_slow_link_paces_delivery`; `cmd/satlink`'s
  `a_blocked_standard_error_does_not_hold_the_link_up` (40 s) and
  `a_double_tap_while_standard_error_is_blocked_still_lets_it_hang_up`
  (about 4 s); mobile's Go-only `TestClosingATLSConnectionDoesNotWaitForASendInProgress`
  (about 6 s). Others that assert real time run serially but are not
  skipped: core's Go-only `TestALateAnswerThatKeepsComingIsNoCallLeftUp`
  and `TestALateAnswerThatKeepsComingDoesNotHideAHeldCall` (about 1 s each)
  and `TestPollURCsReturnsAsSoonAsOneArrives`; satnet's Go-only
  `TestTheCloseGraceBoundsARequestedClose` and
  `TestTheCallsDurationCountsItsHangUp` (about 1.6 s each) and
  `TestTheCallsDurationCountsTheHangUpThatFollowsAPanic` (two cases of
  about 2.8 s; it also installs the process-wide logger): the billing tests escape with a
  500 ms guard (`billingGuard`), so that half a guard leaves `Close` time to
  return.
  mobile's `TestALinkBeingSetUpCanBeCancelled` runs serially, as it asserts
  that the setup took less than the 5 s call setup (`mobile.SlowCallSetup`;
  it takes under a second). Slow tests that assert no timing run in parallel
  instead:
  - core's `a_dial_whose_abort_goes_unanswered_is_ended_anyway` (the
    ten-second abort grace), `an_ok_line_from_a_call_left_up_does_not_pass_for_the_handsets`
    (three two-second synchronisation attempts, then an escape with the
    default guard), `a_call_left_up_that_cannot_be_ended_is_reported`
    (about 23 s), `a_data_call_whose_connect_was_lost_with_a_failed_read_is_ended`
    (about 19 s), `a_handset_that_ended_its_call_but_stays_silent_has_timed_out`
    (15 s), `a_voice_call_whose_ok_was_lost_with_a_failed_read_is_kept`,
    `a_hang_up_without_a_command_deadline_still_reaches_the_dtr_drop` and
    `a_call_left_up_is_ended_before_dialling_without_a_command_deadline`
    (about 10 s each), `a_handset_found_relaying_a_call_it_cannot_end_is_reported`,
    `a_data_call_the_voice_api_answered_unseen_is_ended` and
    `a_late_answer_to_a_timed_out_command_does_not_pass_for_atas`, and,
    Go only, `TestAnUnansweredATHAfterAConfirmedEscapeStillReachesTheDTRDrop`
    (about 10 s), `TestInitializeEndsACallLeftUpWhoseFarEndNeverPauses`
    (about 11 s), `TestTheEscapeKeepsTheSettingsGuardOfSilence`
    and `TestACallThatConnectsAfterTheTimeoutIsEndedWithTheSettingsGuard`
    (two guards of `DefaultEscapeGuard` + 500 ms each, about 4 s),
    `TestAPartialAnswerLeftBeforeAnswerDoesNotPassForATAs` (an escape with
    the default guard) and `TestTheDataOfACallLeftUpAreNoPartOfTheNextAnswer`
    (two seconds of data before `Answer` gives up): they run with real
    handset timings (the 1.5 s escape guard, the 10 s abort grace, three
    2 s ATs, the 10 s hang-up cap). So do the Go-only
    `TestALateAnswerArrivingDuringAnswerDoesNotPassForATAs` and
    `TestAnAbortsResultArrivingSecondsLaterStillEndsTheAttempt` (about 3 s
    each) and `TestAnATAnsweredASecondLateStillSynchronises` (1 s), which
    check that a wait is long enough for a late reply: the scripted
    transport's reads decide at their start whether a reply arrives in time,
    so a slow machine can only make a reply arrive sooner, and these tests
    cannot fail for being slow. serial's `TestCloseGivesAWriteUnderWayAWhileButNoMore`
    (1 s, a lower bound only) runs in parallel too. The core package takes
    about 45 s with `-race`;
  - satnet's `an_idle_link_hangs_up_by_itself` (the shortest idle limit,
    25 s), `a_name_the_second_dns_server_says_does_not_exist_is_not_a_timeout`
    and `sending_again_just_what_was_not_taken_delivers_a_message_once`
    (about 11 s each), `a_waiting_read_gets_the_answer_another_call_took_in`
    (about 10 s), and the Go-only `TestTheCallTimeLimitEndsTheCall`
    (about 21 s; it asserts no timing beyond a lower bound that holds by
    construction, `ConnectedFor >= MaxCallDuration - HangUpTime(config)`,
    while the unit test `TestTheCallTimeLimitStartsTheHangUpInTimeToEndTheCallByIt`
    pins when the hang-up starts) and `TestALookupNoServerAnswersTimesOut`
    (about 11 s); satnet takes about two and a half minutes with `-race`,
    and about a minute with `-short`;
  - sim's `connections_one_after_another_survive_repeated_connection_requests`
    and `several_connections_at_once_survive_repeated_connection_requests`
    (about 15 s each at Iridium's 750 ms one-way delay);
  - mobile's stranded-handset tests (15–57 s each, as in Rust:
    `LinkSettings` has no escape guard, so the default 1.5 s applies; the
    longest, `a_link_that_lost_the_handset_while_hanging_up_says_the_call_may_still_be_up`
    and `a_recovery_that_cannot_end_the_call_fails_with_hang_up_failed_again`,
    reinitialise a handset deaf to the escape: three 2 s ATs and two
    escapes), the Go-only `TestReinitializeStrandsAReadyHandsetWhoseCallCannotBeEnded`
    (about 33 s) and `TestACallAPanicLeftUpIsEndedByANewHandsetOnTheAppsTransport`
    (about 10 s: the new handset's three 2 s ATs, then the escape with the
    default 1.5 s guard and ATH); mobile takes about 100 s with `-race`;
  - `cmd/satlink`'s tests of the built binary, but for the two above.

  Tests that call `slog.SetDefault` or `SetLogSink` never use `t.Parallel`.
- A wait for something not to happen is the one sleep allowed, with a
  comment saying why: satnet's `a_reply_to_a_closed_udp_socket_costs_no_airtime`
  leaves 300 ms for an ICMP error to go out, were there one (a leaked error
  would wait for the paced line, about 1.4 s, so the internal
  `TestTheStacksICMPErrorsNeverGo` is what guards the drop);
  `cmd/satlink`'s tests of an interruption the CLI was started to ignore
  give it a second to do nothing, and its log-order test and
  `TestTheLastLogLinesOfARunReachStandardErrorBeforeItEnds` give a held
  write 100 ms (`heldLineWait`); serial's
  `TestAClosedTransportIsNotClosedAgainOnceCollected` leaves 100 ms after
  the collection for a cleanup that must not run. The double tap's 200 ms
  gap and its 3 s and 40 s of blocked standard error, the 3 s
  (`lateRepeatGap`) after which `TestASecondInterruptionSecondsAfterTheFirstQuitsAtOnce`
  repeats the interruption, and the 300 ms of quiet after CONNECT before
  the call of `TestTheEscapeCountsItsGuardOfSilenceFromTheLastWrite` writes
  are the scenarios' own durations: nothing outside the process could show
  the outcome sooner;
  so is the 50 ms after which serial's `TestCloseLetsACallUnderWayFinishFirst`
  releases the stalled call, as nothing shows `Close` waiting, and long
  enough that a grace of a few milliseconds would close the port under the
  call.
- Recording log handlers are guarded by a mutex (background goroutines log
  into them), keep the attributes passed to `WithAttrs` (sim and satnet's
  `pkg` may come from there), and match only their own `pkg`. A helper that
  installs one with `slog.SetDefault` also saves `log.Writer()` and
  `log.Flags()` and restores them in `t.Cleanup` with the previous default:
  `slog.SetDefault` points the log package at the handler, and setting the
  previous default again does not undo that, so every later line of the
  test binary would go to a handler nobody reads (`satnet/log_test.go`,
  `sim/log_test.go`, `mobile/log_test.go`,
  `internal/ipstack/wire/helpers_test.go`,
  `internal/ipstack/internal/netlog/netlog_test.go`, each with a test that
  the log package is left as it was). mobile's callers of `recordErrors`
  (`TestAPortCloserThatPanicsLogsItAndTheAppCarriesOn`,
  `TestAListenerThatCannotFollowItsLinkLogsItsPanic`,
  `TestAMethodWithoutAnErrorSurvivesAPanicWithItsEmptyResult`) never call
  `t.Parallel`.
- Go-only behaviour has its own tests (files in section 1): `ErrBusy` and
  release; net.Conn deadlines, including deadlines moved while a call waits;
  `Close` waking a pending `Read`, `Write` or `WaitAcknowledged`, and an
  in-progress TLS handshake of crypto/tls over a `TCPConn`; use after Close;
  runtime cleanups, including a dropped `Link` hanging up while a `Watcher`
  and a connection are still held (`runtime.GC()` in a loop until
  `w.WaitClosed(100*time.Millisecond)` reports `CloseReasonRequested`, within
  10 s; then the connection's `Read` fails with `ErrLinkDown` and the handset
  is no longer busy); `errors.Is` against the standard equivalents;
  password redaction for every verb fmt hands a `Format` method (`%p` and
  `%w`, which fmt handles itself, aside: the `ppp.Credentials` doc says
  when); gomobile's zero-value constructors
  and nil arguments; goroutines that end (sim's TLS sessions); a
  transport's panic out of a data call pulsing DTR; Rust's texts
  where Go's formatting would differ (lossy UTF-8, `Debug` of structs, error
  numbers).
- Close reasons without a Rust test have public-API tests in
  `link_integration_test.go`: `CloseReasonCallTimeLimit` (a `MaxCallDuration`
  of 21.5 s, just longer than the shortest allowed, 20 s for bringing PPP up
  plus hanging up — with `simtest.CallEscapeGuard`, 2 × 60 ms + 1 s, so
  21.12 s — which starts the hang-up 20.38 s after CONNECT),
  `CloseReasonNegotiationTimedOut` (a 500 ms one-way delay with
  `NegotiationTimeout` 200 ms), and `CloseReasonInternal` (a
  `simtest.FaultyTransport` with `PanicsOnNextRead` on: the call is still
  hung up, and the status reports `CloseReasonInternal` with
  `CallMayStillBeUp` false). `TestTheCallTimeLimitEndsTheCall` holds
  `ConnectedFor` to at least the limit less the hang-up; Go's own lower
  bounds in `an_idle_link_hangs_up_by_itself` (`ConnectedFor >=
  IdleTimeout`) and `the_data_limit_hangs_up` (bytes `>= MaxBytes`) check
  that a limit did not fire early.
- `go test -race ./...` passes. The statement coverage targets are: ppp at
  least 90%, satnet at least 85%, every other package at least 80%, ipstack
  as PORTING-ipstack.md section 6 sets. `go test -race -cover ./...`
  reports:

  | package | coverage |
  |---|---|
  | `satlink` (core) | 96.0% |
  | `ppp` | 99.7% |
  | `satnet` | 98.9% |
  | `sim` | 98.4% |
  | `serial` | 99.4% |
  | `cmd/satlink` | 98.1% |
  | `mobile` | 99.0% |
  | `internal/ipstack` and its packages | 99.1–100% (PORTING-ipstack.md, section 6) |
  | `internal/logtext` | 97.1% |
  | `internal/asciicase`, `internal/errnotext`, `internal/memconn`, `internal/ringprovider`, `internal/rustdebug`, `internal/simtest`, `internal/utf8lossy` | 100% |

Rust tests that cannot be ported literally change shape as follows; the Go
test says so in a comment.

| Rust test | Go test |
|---|---|
| satnet `config.rs` `rejects_zero_and_absurd_limits` | A zero limit is "no limit" in Go: the invalid cases use -1 for the durations; the `max_bytes: Some(0)` case has no Go form (`MaxBytes` is unsigned and 0 means none) and is dropped, with a comment saying why. The absurd duration, the 23 s call limit (over before PPP is up and the hang-up done), the 24 s idle limit (over while a lookup waits for its second DNS server) and `MaxBytes` 2047 stay. |
| FFI `settings_are_validated_before_dialling` | `IdleTimeoutSeconds: -1` instead of `Some(0)`; the Go-only cases just below the minimums: a 23 s call limit, a 24 s idle limit and a 2047-byte data limit. |
| core `question_mark_keeps_a_call_that_may_still_be_up` | `TestAnErrorPassedOnKeepsACallThatMayStillBeUp`: the hang-up's error, wrapped with `%w`, matches `ErrCallMayStillBeUp` (Go has no `?` conversion). |
| core `clones_share_one_request` | Copies of `DataCallSettings` share one request. |
| core `a_cancelled_dial_is_aborted` | The dial runs in a goroutine, and the test polls until the dial command was written 300 ms ago (`writtenAt`) before cancelling, instead of a thread that sleeps. |
| core tests asserting `Err(Error::CallMayStillBeUp(_))` | The error's kind is `ErrCallMayStillBeUp`, with its exact `Message`. |
| core tests with `thread::spawn` and `recv_timeout(30 s)` | `finishesWithin(t, 30*time.Second, …)`. |
| core Go-only `TestADialIntoAHandsetStillOnlineIsRefused` and `TestAVoiceCallAnsweredInsteadThatCannotBeHungUpSaysWhy` (snapshot 2) | Replaced by the Rust tests that cover them now, plus `TestACallLeftUpThatIsEndedFailsTheDialWithWhatShowedIt` and `TestAVoiceCallAnsweredInsteadThatCannotBeHungUpIsEndedAsADataCall`. |
| satnet `a_link_cancelled_while_it_dials_never_connects`, `a_link_cancelled_while_ppp_comes_up_is_hung_up` | `Establish` runs in a goroutine, and the test cancels once the simulator counts the call placed (connected), instead of a thread sleeping a second; `failure.handset.is_some()` is `expectNotBusy`, `expectCommandMode`. |
| satnet `a_lookup_does_not_wait_behind_busy_connections` | The lookup starts once `DataBytesFromApp` has grown by 3 × `LinkRate`, instead of after a 3 s sleep. |
| TLS `a_waiting_read_gets_the_answer_another_call_took_in`, `a_waiting_read_sends_on_what_a_timed_out_write_left`, `abort_resets_the_connection_and_wakes_a_waiting_read`, `one_thread_reads_while_another_writes`; satnet `a_requested_close_lets_waits_already_under_way_finish` | The test acts once `AwaitWaitingCall` sees the reader wait, instead of after 30 ms, 500 ms, 100 ms, 200 ms and 50 ms sleeps. |
| TLS `sending_again_just_what_was_not_taken_delivers_a_message_once` | `send_until` with a 50 ms deadline is `WriteSome` after `SetWriteDeadline(now + 50 ms)`; the test also checks that a timeout took nothing. |
| TLS `a_waiting_read_gets_what_a_polling_read_took_in` | A net.Conn has one read deadline, not one per read, and Go's reads are serialised, so a `Read` waiting behind another takes what the other took in, and the race the Rust fix closes cannot arise. In each round, the first read takes one byte, started first and seen waiting by `AwaitWaitingCall`; the second, waiting behind it, must get the rest from the session. There is no polling read, the deadline only bounds a read that would wait for good, and the test is deterministic with `GOMAXPROCS=1`. |
| TLS `what_was_acknowledged_before_the_connection_was_reset_was_delivered` | A probe TCP connection is polled until it is reset — every connection on the old address is reset in the same pass — instead of sleeping 200 ms. |
| TLS `a_write_once_the_goodbye_is_queued_is_refused` | `send_until(…).unwrap()` is a `Write` taking all 12 000 bytes, with nil or `ErrTimedOut`; the late write must fail with `ErrConnectionReset`, Rust's own kind, where Rust's test only asks for an error. |
| TLS (rustls) `a_failed_session_still_says_why_once_the_connection_ended` | Over the fake transport: a forged record, then FIN; two `Read`s both return the session's failure. |
| FFI `a_link_being_set_up_can_be_cancelled` | `EstablishLink` runs in a goroutine (`establishInBackground`, `awaitSetup`), and the test cancels once the simulator counts the call placed, instead of a thread sleeping a second; it runs serially. |
| FFI `a_link_that_lost_the_handset_while_hanging_up_says_the_call_may_still_be_up` | Go has no lost handset (section 3): the handset comes back stranded, and the first `Reinitialize`, through `untilNotBusy`, fails with `SatlinkErrorHangUpFailed` as in Rust, without opening a transport again. |
| FFI `reinitialize_ends_a_call_held_in_online_command_mode`, `reinitialize_ends_a_held_call_even_without_cpas` | One test, `TestReinitializeEndsACallHeldInOnlineCommandMode`, whose subtests set `WithoutCPAS`. |
| FFI `a_call_left_up_that_cannot_be_ended_is_reported_when_the_handset_is_opened` | `drop(earlier_session)` is its `Close`, which leaves the app's transport alone; the call is dialled with "ATD" + `satnet.IridiumInternetNumber`. |
| CLI `tests/net.rs` tests that need no process in Go | In-process through `run`, standard input a reader. |
| CLI `a_fetched_page_is_hung_up_on_before_a_slow_reader_takes_it` | A standard output that blocks, instead of a full pipe; the test waits until the simulator shows the call hung up, instead of sleeping 10 s. |
| CLI `a_second_interruption_right_after_the_first_still_lets_it_hang_up` | The second SIGINT is sent once the first's message is printed, instead of 200 ms later. |
| CLI `an_interruption_during_call_setup_keeps_the_call_from_connecting` | `-v`, and the signal goes once the simulator's "simulated handset: dialling" line is logged, instead of after 2 s. |
| CLI `reading_stops_at_a_close_a_truncation_or_the_deadline` | Go's `io.EOF` for Rust's `Ok(0)`; `Failed` is compared with `==` on the same error value. |
| core and satnet tests asserting `failure.call_may_still_be_up` (or its negation) | `errors.Is(err, satlink.ErrCallMayStillBeUp)` (or its negation), plus the field through `errors.As` where the Rust test names the failure's type. |
| satnet `a_panic_while_hanging_up_still_reports_the_link_closed` | `failure.handset.is_none()` ("it went down with the panic") has no Go form, as the handset is never moved: the Go test asserts instead that the handset is released, its methods no longer failing with `ErrBusy`, and that the call ended all the same — DTR was dropped (`DTRDrops`) and the simulator shows no call — as it did in Rust, where the handset went down with its transport (section 3). The faulty transport is `simtest.FaultyTransport` with `PanicsOnEscape`. |
| FFI `a_dropped_link_that_cannot_hang_up_strands_the_handset` | `link.Close()` (no wait); `until_not_busy` polls `handset.SignalQuality()` until it stops failing with `SatlinkErrorHandsetBusy`, as for `a_dropped_link_gives_the_handset_back_once_its_call_ends`, and then expects `SatlinkErrorHangUpFailed`. |
| FFI `a_link_that_fails_and_cannot_hang_up_strands_the_handset_until_it_recovers` | `handset.clone()` is the same `*IridiumHandset`; `transport.honour_dtr()` is `faulty.DeafToDTR.Store(false)`. |
| TLS `a_write_that_times_out_counts_what_the_session_took` | `SetWriteDeadline(now + 1 s)`, then `n, err := conn.Write(…30000 bytes)`: `err` is nil or `ErrTimedOut` (Go's `Write` returns the count with the error, where Rust's `io::Write::write` returned `Ok(taken)` and left the timeout to `flush`), and `n > 0`; then exactly `n` bytes come back, and a `Read` with a 500 ms deadline times out. |
| sim `link.rs` `a_zero_rate_still_delivers_slowly` | `BytesPerSecond: -1` (0 is unlimited in Go). |
| sim `closing_the_port_ends_the_call_when_the_app_asked_for_it`, `drop(call)` elsewhere | `handset.Close()`, which is allowed while the handset is busy. |
| `drop(link)` | `link.Close()`, or, where the Rust test is about dropping, the runtime-cleanup test above. |
| FFI `a_dropped_link_gives_the_handset_back_once_its_call_ends` | `link.Close()` (no wait), then poll `handset.SignalQuality()` until it stops failing with `SatlinkErrorHandsetBusy`. Three Go-only runtime-cleanup tests drop a link without `Close` and collect garbage every 50 ms, for up to 30 s, until it has hung up (`mobile.CollectGarbageUntil`): `TestACollectedLinkStillHangsUpAndItsListenerHearsItClose`, `TestACollectedLentLinkGivesTheHandsetBackOnceItsCallEnds` and `TestACollectedSerialLinkStillHangsUpAndClosesItsPort`. |
| sim `commands.rs` `the_handset_and_its_control_can_go_to_other_threads` (a compile-time Send/Sync check) | A `-race` test that uses the `Handset` from one goroutine while `Control` is used from others. |
| sim `commands.rs` `an_sms_from_another_thread_reaches_a_waiting_app` | In a `testing/synctest` bubble: `PollURCs` runs in a goroutine, and the message is sent once `synctest.Wait` returns, that is once `PollURCs` sleeps between two reads, instead of 50 ms later. |
| core and satnet tests inspecting `CallFailure.handset` / `LinkFailure.handset` | The caller's `*Handset` is not busy and answers `AT`. |
| `into_transport().history()` | The test's own `*scripted` transport. |
| ppp's doc example | `ExampleEndpoint` prints a deterministic property (section 1). |
| satnet `a_handset_deaf_to_the_escape_is_hung_up_by_dropping_dtr` | As in Rust, plus: `simtest.FaultyTransport.DTRDrops()` is at least one after `link.Close`, so that the DTR path is really taken. |
| CLI `targets_that_are_rules_rather_than_hosts_are_refused_before_dialling`, `an_address_is_resolved_without_a_call` (clap's `try_parse_from`) | The arguments go through `parseCommandLine` against a root holding `newNetCommand`; the named command's `RunE` records the `netArguments`, and nothing is dialled. |

---

## 3. Known differences from the Rust version

| Difference | Reason |
|---|---|
| No RFC 8879 certificate compression: certificate chains arrive uncompressed, so full handshakes with servers that compress (Cloudflare among them) cost a quarter to a third more airtime. Documented in the satnet package doc and the README. | Go's crypto/tls does not implement RFC 8879, and faking it is impossible without forking crypto/tls. |
| TLS is crypto/tls with Go's own crypto instead of rustls/ring. TLS error messages are crypto/tls's. crypto/tls ignores the order of the curve list and sends a key share for X25519, the first group of its own order. | Standard library only. Kept: the key exchange groups of rustls's ring defaults (X25519, P-256, P-384) with a key share for X25519 only, so no X25519MLKEM768; TLS 1.2's cipher suites, rustls's ring ones only — ECDHE-ECDSA and ECDHE-RSA with AES-128-GCM, AES-256-GCM and ChaCha20-Poly1305, where crypto/tls's defaults would add four ECDHE CBC-SHA1 suites; the keys ring refuses — a chain holding an RSA key of under 2041 or over 8192 bits, or an ECDSA key on P-224, is refused (`verifyChainKeys`), where crypto/x509 alone would take it, as ring compares its 2048-bit minimum with the modulus's length rounded up to whole bytes and its 8192-bit maximum with the exact length (ring-0.17.14 `src/rsa/public_modulus.rs:56-71`), which keeps the Rust result whenever a server sends both versions the same chain (crypto/tls itself refuses an RSA key of over 8192 bits in a certificate the server sends, with its own text, so only a trust anchor's reaches `verifyChainKeys`); session resumption per connector, whatever the ASCII case of the server name, as rustls's cache compares names (`caseBlindSessionCache`; crypto/tls's would key by the name as written); leaf-only pinning after chain verification; the server-name check before anything is sent. |
| The public roots are the NSS snapshot that `go.mod` pins (`golang.org/x/crypto/x509roots/fallback` v0.0.0-20261016151342, 120 roots, built into a pool directly, whatever the operating system trusts), not webpki-roots 1.0.9's 121 anchors, and they carry the distrust-after dates NSS sets on the roots Mozilla phases out: a chain with a certificate such a root issued after its date is refused (`rootPoolOf`, `AddCertWithConstraint`). Today that is Izenpe.com, whose chains issued after 2026-04-15T23:59:59Z the Rust version still accepts. The snapshots differ too: webpki-roots 1.0.9 also trusts "TUBITAK Kamu SM SSL Kok Sertifikasi - Surum 1", which the Go bundle lacks. | The bundle carries NSS's own trust decisions, constraints included, and dropping a constraint would trust what Mozilla distrusts; webpki-roots' `TrustAnchor` cannot express the dates, and the two crates follow Mozilla's program at different dates (`TestAPublicRootAnchorsNoChainIssuedAfterItsDistrustDate`). |
| A server's certificate chain may hold ML-DSA keys and ECDSA keys on P-521, which rustls's ring backend cannot verify, and certificate signatures ring does not verify, such as ecdsa-with-SHA512, are taken too; crypto/tls also offers and accepts ECDSA with SHA-512 as a TLS 1.2 handshake signature, which rustls never offers. A server holding such a certificate next to a classical one may send satnet that one, where it sends rustls the classical one: an ML-DSA-65 leaf adds about 5 KB to a full handshake (a 1952-byte key and a 3309-byte CertificateVerify), some 17 s at 2.4 kbit/s. A server holding only such a certificate connects in Go and fails in Rust. Among classical certificates too, crypto/tls asks for RSA-PSS signatures before ECDSA ones (MLDSA44, MLDSA65, MLDSA87, PSSWithSHA256, ECDSAWithP256AndSHA256, …), where rustls's ring backend asks for ECDSA (P-384, then P-256) and Ed25519 before RSA-PSS and PKCS#1: a server that follows the client's preference, as OpenSSL does by default, sends satnet its RSA leaf where it sends rustls its ECDSA one — at OpenSSL's SECLEVEL 1 or below (its default before 3.2) perhaps an RSA-1024 one, which satnet refuses where Rust connects with the P-256 one. So a pin set made for the Rust version can fail with "the public key is not pinned": pin the key of every certificate the server holds (the docs of `TLSOptions.PinnedPublicKeys` and of mobile's `AddPinnedPublicKeySHA256`; `TestTheCertificateCryptoTLSAskedForMustBePinnedToo`). | crypto/tls always offers MLDSA44, MLDSA65, MLDSA87 and ECDSAWithP521AndSHA512, and `tls.Config` can neither withdraw them nor reorder its signature schemes, so refusing what it asked for would fail connections that the Rust version makes (Go's `tls.Server` sends the first certificate the offer allows; OpenSSL follows the client's preference). The keys refused are those ring refuses (the second row of this table), short RSA keys being too weak to trust anyway (`TestAServerSendingTheCertificateCryptoTLSAskedForIsTrusted`, `TestTheKeysAServersChainMayHold`). The Baseline Requirements allow ECDSA with SHA-512 only for P-521 keys, so only private PKIs could sign so with P-256 or P-384. |
| crypto/tls resumes a TLS 1.2 session with a ticket only, never by its session ID, which rustls's default also uses: a TLS 1.2 server that issues no tickets costs a full handshake every time. The satnet package doc and the `TLSConnector` doc say so. | crypto/tls has no client-side session-ID resumption. TLS 1.3 servers, and TLS 1.2 servers that issue tickets, resume as with rustls. |
| `TLSConn.Close` while another goroutine is inside crypto/tls's `Write` skips close_notify; the records already waiting still go within the write deadline before the TCP connection closes (crypto/tls's `Close` returns the record buffer's `Close`). A timed-out `TLSConn.Write` returns the count of what the session took together with `ErrTimedOut`, where Rust's `io::Write::write`, which is `send_until`, returns `Ok(taken)` and leaves the timeout to `flush`; `TLSConn.WriteSome`, Rust's `send_until`, returns the count without the timeout too. | crypto/tls takes `Close` during `Write` as a request to break it; the Rust `close` consumed the stream, so nothing could be writing. `io.Writer` returns a count with its error. Everything else of Rust's write semantics is kept by the record buffer of section 4 (TLS connection): crypto/tls itself would fail every write after one that timed out. |
| The handset is never moved (Busy model, automatic release); `CallFailure`/`LinkFailure` are plain errors, their `call_may_still_be_up` a field of the error and the sentinel `satlink.ErrCallMayStillBeUp`; `SatelliteLink::take_handset` is not needed (the handset is released when the call ends, and `Link.Close` on a closed link returns the hang-up's result at once). A handset whose link's driver panicked while hanging up is released like any other, where Rust lost it with the thread, and so is one whose driver panicked before the hang-up, which then still hangs up (the panic rows below). A `DataCall` dropped without `HangUp` ends nothing and leaves the handset busy, where Rust's dropped `DataCall` dropped the handset and so closed its port (section 2, Drop becomes Close). Rust's `From<CallFailure> for Error`, which turns a failure whose call may still be up into `Error::CallMayStillBeUp` for `?`, has no Go form: an error passed on keeps its own text and flag, and matches `satlink.ErrCallMayStillBeUp` all the same. | Go has no moves; failing fast with `ErrBusy` replaces the compile-time guarantee. |
| `DataCallSettings.Cancellation` may be nil — zero settings, say — and the setup then cannot be cancelled, where Rust's settings always hold a `Cancellation`; `Cancel` on a nil one panics. | A pointer keeps the copies of the settings sharing one request, as Rust's clones share the `Arc`. |
| A panic in the link's driver is recovered, not raised again. One that came before the hang-up — while the connections are aborted as the hang-up starts, say, which a log handler can cause through ipstack's TCP state traces — still has the call hung up: the run's outermost recovery calls `DataCall.HangUp` (`endCallLeftUp`) before it abandons the engine, where Rust's unwinding thread dropped the `DataCall` and its transport, whose DTR drop ended the call. A hang-up that itself panicked is not run again (a second `HangUp` only says "the data call is over"), but the core has pulsed DTR as the panic left it (next row). Either way the link reports itself closed with `CallMayStillBeUp`, and `Link.Close` returns `ErrLinkDown` "the link's driver thread panicked" (Rust: `abandon`, then `resume_unwind`, ending the driver thread). `Usage.ConnectedFor` then counts the hang-up `endCallLeftUp` ran, as the normal path's final update does, where Rust's `abandon` kept the last pump's usage; it does so even when abandon's own abort panics, as `driver.abandon` records the usage before `engine.abandon` (`TestTheCallsDurationCountsTheHangUpThatFollowsAPanic`). `beginClosing` sets `Closing` before it aborts the connections, and `abandon` sets `Closed` first, where Rust's `begin_closing` and `abandon` abort first (`engine.rs:202-207, 219-227`): when aborting them panics as the hang-up starts, the link still closes for the reason the hang-up started with, where Rust reported `Internal` with the abort's panic message (`TestTheFirstOfTwoPanicsIsWhyTheLinkClosed`); and when abandon's own abort panics, the link is still reported closed, where Rust's panic escaped the unwind handler, so the driver thread died and the link never closed (`TestAPanicEveryTimeTheConnectionsAreAbortedStillClosesTheLink`). | A panic that leaves a goroutine ends the whole process. `CallMayStillBeUp` stays set even when the hang-up after the panic was confirmed: conservative, as Rust's report is, and the contract mobile maps. A log handler, which ipstack's TCP state traces reach, must not hide why the link closed nor keep it from closing. |
| A transport's panic out of `DataCall.HangUp`, or out of `DialData` or `AnswerData` while they set the call up, pulses DTR before it goes on — DTR dropped for 150 ms and raised again, best effort, a second panic dropped — which ends the call after AT&D2 (`pulseDTRUnless`). Rust's `DataCall::dial(handset, …)` and `DataCall::answer(handset, …)` took the handset by value, and `DataCall::hang_up(self)` owned it, so the unwinding dropped it and its transport, whose drop dropped DTR (`SimulatedHandset::drop` calls `Modem::drop_dtr`; a serial port is closed). So mobile's host transports with a DTR line see the pulse too, ending a call that Rust's FFI `ForeignTransport`, which has no `Drop`, left up (`Slot::Lost`). | The Go handset stays with its caller, who may recover the panic and use the handset again, so its transport is not closed: DTR comes up again, as with AT&D2 a DTR left down would end the next call. Only the core can pulse DTR under the handset's lock, before it releases the handset: by the time the panic reaches satnet's driver, a pulse on the raw transport would race the app's next command. |
| `Establish` dials first and then starts the driver goroutine; Rust starts the driver thread before dialling, so that failing to spawn it ("cannot start the link's thread") costs no airtime. Likewise mobile starts a link's listener goroutine once the link is up, where the Rust bindings start the listener's thread before dialling (snapshot 3's `spawn_listener`). | Starting a goroutine cannot fail. |
| mobile has no lost handset: Rust's `Slot::Lost` reports `HangUpFailed` "the link lost the handset while hanging up; reinitialize ends the call", and Rust's `reinitialize` opens a lost handset's transport again through the `Reopen` that `IridiumHandset::open` keeps ("the link lost the handset, which cannot be reached: %v"). In Go a lent handset whose link died mid-hang-up comes back stranded, failing with `SatlinkErrorHangUpFailed` ("the last data call could not be hung up; reinitialize ends it") until `Reinitialize`, which reaches it through its own transport, succeeds. A panic inside `satnet.Establish`, which only the driver goroutine recovers, reaches `EstablishLinkWithHandset` and strands the handset the same way, since whether a call is up is unknown. The links that have a handset of their own close it: a panic recovered while `OpenSerialHandset` or `EstablishSerialLink` sets up closes the port it opened, as Rust's unwinding dropped the `SerialTransport`; if the panic comes after satnet's driver goroutine has taken the call, the port closes at once and the DTR drop ends the call, where Rust's link `Drop` first hung up through the driver. | The Go handset, transport included, stays with its `IridiumHandset` whatever the link does. |
| An address change cuts off the connections with data in their outbox as well as those with data in the stack's send buffer, as snapshot 3 did; Rust snapshot 4's `cut_off_undelivered` counts smoltcp's `send_queue` alone, on the ground that what waits in an outbox stays there through the reset and says so itself. | It need not stay: the next poll hands a connection's turn of its outbox to smoltcp before smoltcp resets the socket on the old address, and the reset empties the send buffer. With a segment or less waiting, nothing is then left to show the data was lost, and Rust's `wait_acknowledged` reports as delivered what never left; a probe test against snapshot 4 shows it (`unacknowledged=0 cut_off=false open=false`). `TestDataTheStackTakesJustBeforeTheResetStaysCutOff` holds Go to the earlier rule. |
| An `Abort` (`TCPConn.Abort` and `TLSConn.Abort`, which reach `resetTCP`) or the hang-up (`abortAll`, from `beginClosing` and `abandon`) that empties a connection's outbox records the connection as cut off (`dropOutbox`). `WaitAcknowledged`, TCP's and TLS's, then fails for that data: `ErrConnectionReset` after an abort, `ErrLinkDown` with the close reason once the hang-up has started. Rust snapshots 3 and 4 clear the outbox in `abort_all` and `reset_tcp` (`stack.rs:208-218, 297-300`) without a record, and `wait_acknowledged` (`tcp.rs:100-113`) returns `Ok(())` for data that never left. What had been acknowledged still counts as delivered, as in Rust (`a_write_refused_once_the_link_closed_leaves_delivery_confirmed`). So the CLI's `net tcp` reports standard input the hang-up dropped as not all sent, where the Rust CLI reported success. | An emptied outbox leaves nothing to show the data was lost — no unacknowledged bytes, no cut-off entry — and smoltcp's abort keeps only what its send buffer took. Snapshot 2 had no outbox: written data sat in smoltcp's send buffer, which an abort keeps, so `wait_acknowledged` failed; snapshot 3's outbox clear lost that, contrary to snapshot 4's own comment on `cut_off_undelivered` that what waits in an outbox "says so itself". Data waits in an outbox whenever the app wrote after the driver last fed the stack: after the last poll before the hang-up, during the driver's final write to the line, up to one poll interval before an `Abort`, or for seconds while a keep-alive probe or a stale retransmission holds the flow's turn. Probes against snapshot 4 returned `Ok(())` while the discard service counted 0 bytes. `TestWaitAcknowledgedReportsDataTheConnectionsEndDroppedBeforeItLeft`, `TestDataTheHangUpDroppedIsNotReportedDelivered` and `TestAResetOrTheHangUpCutsOffTheConnectionsWhoseOutboxItEmptied` hold Go to the rule; `TestWhatWasAcknowledgedBeforeTheConnectionEndedStaysDelivered` keeps Rust's result where nothing was dropped. |
| Each `Write` and `WriteSome` of a `TCPConn` (its `writeMu`) and of a `TLSConn` goes out whole relative to the others, as `net.TCPConn`'s do; Rust's `send_all_until` and `send_until`, and the FFI's `send`, interleave when called from two threads. | Rust promised one reader and one writer; net.Conn promises safety for concurrent use, and an interleaved message helps no caller. |
| `Tuning.TCPTimeout` of zero means no timeout (ipstack's `SetTimeout`); in Rust, a zero `tcp_timeout`, which smoltcp gets as `Some(ZERO)`, timed every connection out at its first dispatch, before the SYN left. The field doc says so. | Rust's `tcp_timeout` is a required `Duration` (`config.rs:243`, always passed as `Some`, `stack.rs:226`), so its zero was a value no configuration could use; Go gives zero the meaning its other optional durations have, none (section 2), which Rust could not express. |
| `satlink.Handset` is safe for concurrent use (calls serialised). | The Rust handset was `&mut self`; Go callers (mobile, satnet) share it across goroutines. |
| DTR is an optional interface with `RaiseDTR`/`DropDTR` (also on `mobile.ByteTransport`) instead of `set_dtr(bool)` with a default. | No boolean flag parameters; an optional interface replaces the trait's default method. |
| `Response.connect: Option<String>` → `Connected` + `ConnectMessage`; `FinalResult::Connect(None)` and `Some("")` both have an empty `ConnectMessage`. | Go has no `Option`; nothing distinguishes the two in practice. |
| Zero limits mean "no limit" (`Limits`, mobile `LinkSettings`); negative values are refused. The other limits Rust refuses as too small to be of use are refused alike: a call time shorter than bringing PPP up and hanging up take, an idle time under 25 s, a data limit below 2048 bytes. `sim.LinkProfile.BytesPerSecond` 0 is unlimited and a negative value one byte per second (Rust: `None` unlimited, `Some(0)` one byte per second). | Go has no `None`; see the messages in section 5. |
| `TcpStream::receive_until`/`send_all_until`/`set_read_timeout` become net.Conn deadlines: `send_all_until` is `Write`, which queues all of `p`, and `send_until` is `WriteSome`, which returns as soon as part of `p` is queued; the same holds for `TlsStream`. Data already received is still returned after a deadline has passed, as in Rust (unlike `net.TCPConn`). The connections are safe for concurrent use, and `Close` wakes calls waiting in other goroutines. | net.Conn is the Go contract; `net/http` and crypto/tls work over these types. |
| mobile's per-call `timeoutMillis` are applied as the satnet connection's one read or write deadline, which every waiting call in that direction reads again. While two receives, or two sends (TLS `ShutdownWrite` included), wait at once, the timeout of the one that started last applies to both, where Rust passed each call its own deadline. `TCPConnection`, `TLSConnection` and `UDPConnection` say so: receive from one thread at a time, and send from one thread at a time — what the Rust bindings promised ("one thread may receive while another sends"). | satnet offers net.Conn deadlines only; a plain per-direction mutex in mobile would make a zero-timeout poll wait behind another thread's long wait. True per-call deadlines would need new satnet API: deadline-taking variants of the connections' reads and writes, the TLS ones carried through the record buffer. |
| `satnet.Link.DialContext` and `TLSConnector.Config` exist only in Go. | So that `net/http` (with HTTPS through the connector's configuration) can use the link. |
| `TlsConnectorBuilder` → `TLSOptions`; PEM errors are reported by `NewTLSConnector`, not while building. `TlsConnector::connect` → `TLSConnector.Secure`. | A plain struct of options is the Go idiom for builders. |
| `Policy::parse(iter)` → `ParsePolicy(rules ...string)`; `Policy.Allow` returns a new policy; `PortRange`'s `FromStr` → `ParsePortRange`. | Go idiom; policies stay values. |
| `ppp::Config::client/server` → `ppp.NewConfig(ppp.ClientRole(…))`/`ServerRole(…)`. | A `NewServerConfig` returning `Config` next to a `ServerConfig` type would mislead; the role stays opaque. |
| fmt prints a value that holds credentials field by field, password included, for `%p` when the value is not a pointer — `Credentials`, an `Authentication`, a client `Role` or `Config`, a copy of an `Endpoint`, or a struct holding one — and for `%w`, for those and for pointers to them, as it hands no `Format` method a verb that does not fit its operand; every other verb prints them redacted (section 4, Redaction). Rust's `{:p}` does not compile for these types. The `ppp.Credentials` doc lists these cases. | fmt handles `%p` and `%w` itself; holding a client role's credentials behind a pointer would hide them, but would make client `Role`s and `Config`s compare by pointer, where Rust's `PartialEq` compares them by value. go vet reports every misplaced `%w`. |
| ppp's authentication wait, `Restart` × `MaxConfigure`, saturates at the longest `Duration` for absurd timers instead of overflowing. | Rust's `Duration` arithmetic would panic; ppp's validation does not cap the durations, so only absurd timer configurations are concerned. |
| `TakeURCs` returns nothing while a data call holds the handset. | The FFI's semantics ("none while a link has the handset"); the Rust core could not be called then at all. |
| The simulator's TLS echo is always on and runs crypto/tls on an in-memory connection, with a goroutine per session that ends with its internet. So `satlink --simulate net get https://secure.test/` (or any `https://` URL) reaches the echo and fails certificate verification against the public roots, where the Rust CLI, which built iridium-sim without its `tls` feature, reported "could not connect to 192.0.2.43:443". A workspace-wide Rust build (such as the parity binary, where iridium-net's dev-dependency feature `iridium-sim/tls` is unified in) reaches the echo too, and then differs only in the TLS library's text: "TLS: invalid peer certificate: UnknownIssuer" against crypto/tls's "tls: failed to verify certificate: x509: certificate signed by unknown authority". | crypto/tls has no sans-I/O server API (rustls had); Go needs no feature flag for the standard library. |
| The simulator's TLS echo resumes sessions with crypto/tls's stateless tickets, keyed per `tls.Config` (one per simulated internet), where rustls used its stateful session cache; either way a client resumes on a later connection to the same internet. crypto/tls's server appends its 144-byte NewSessionTicket to its first flight (rustls sends tickets after the client's Finished), and its larger ticket makes a resumed ClientHello 441 B, where rustls's ticket makes it 368 B (a full one is 289 B). Over `sim.IridiumLinkProfile()`, a full handshake of satnet's client with `secure.test`, which agrees TLS_AES_128_GCM_SHA256, takes 8.1 s, 578 B up and 1645 B down (Rust: 7.2 s, 553 and 1443 B, a rustls client agreeing TLS13_AES_256_GCM_SHA384, whose Finished messages are 16 B longer; the next row); a resumed one 4.6 s, 643 and 478 B (Rust: 3.8 s, 555 and 325 B, with a PSK binder 16 B longer too); a few bytes vary from run to run with PPP's escaping. satnet's own client bytes are within 25 B of rustls's. | crypto/tls's server behaviour; the costs are the simulator's, not satnet's. |
| The simulator's TLS echo picks among the cipher suites a client offers by crypto/tls's own order, where the Rust echo's rustls server took the first suite in the client's list that its ECDSA certificate could serve (rustls leaves `ignore_client_order` false). crypto/tls's order is AES-128-GCM, AES-256-GCM, ChaCha20-Poly1305, at TLS 1.3 and among the ECDHE-ECDSA suites at TLS 1.2 alike, but puts ChaCha20-Poly1305 first unless the machine has AES hardware and the first suite in the client's list that crypto/tls knows is an ECDHE suite with AES-GCM or a TLS 1.3 AES-GCM suite. So a client that prefers AES-256-GCM, as iridium-net's rustls connector and OpenSSL's defaults do, agrees TLS13_AES_256_GCM_SHA384 with the Rust echo and TLS_AES_128_GCM_SHA256 with the Go one (at TLS 1.2, ECDHE-ECDSA with AES-256-GCM against AES-128-GCM). At TLS 1.3 the SHA-256 suite makes both Finished messages and a resumed ClientHello's PSK binder 16 B shorter; at TLS 1.2 the two cost the same bytes, as verify_data is 12 B under every suite. A crypto/tls client lists its suites in crypto/tls's order, so at TLS 1.2 it agrees the same suite with either echo on the same machine, and at TLS 1.3 so does one whose TLS 1.2 suites include an ECDHE suite with AES-GCM, as satnet's do, or that offers no TLS 1.2 suite. On a machine with AES hardware, one that offers TLS 1.2 suites but no ECDHE one with AES-GCM (ChaCha20-Poly1305, CBC or TLS_RSA_WITH_AES_128_GCM_SHA256 alone, say) agrees TLS_CHACHA20_POLY1305_SHA256 with the Go echo and TLS_AES_128_GCM_SHA256 with the Rust one, as crypto/tls judges the client's preference by the first suite in its list that it knows, a TLS 1.2 one. In the interop runs' `tls` scenario, the rustls client's Finished record is 74 B with the Rust echo and 58 B with the Go one. | TLS 1.3: crypto/tls lets a server neither choose nor order its TLS 1.3 suites (`Config.CipherSuites` covers TLS 1.0–1.2 only and its order is ignored; `PreferServerCipherSuites` has no effect). TLS 1.2: a `GetConfigForClient` that narrows `CipherSuites` to the first of the echo's own ECDHE-ECDSA suites the client offers could follow the client; crypto/tls's order is kept anyway, one rule for both versions, as following the client there would save no bytes and matter only to a client of another TLS library that speaks TLS 1.2 at most, where the project's clients negotiate TLS 1.3 (the comment on `newSecureEchoConfig`'s suites). |
| The simulator's TLS echo says no close_notify to a client that hangs up before its handshake has completed (only FIN), where rustls queued one: in plaintext before any keys, encrypted with the handshake keys after. No Rust test covers it. | crypto/tls refuses `CloseWrite` before the handshake completes and offers no way to write an alert. |
| The simulator's test fixtures keep their Rust contents (the CA is "iridium-sim test CA"). | Renaming would need the CA's private key, deliberately unpublished. |
| Serial writes have no timeout: a device that stops taking data blocks the write. The Rust write used serialport's timeout, which is the last read's (100 ms at open); the Go write drains like Rust's flush. Exclusive access on Unix is the library's `TIOCEXCL` ioctl alone, whose failure is ignored, where Rust added `flock(LOCK_EX)` and failed the open on either. | go.bug.st/serial has no write timeout, and `Close` does not wake a `Write` blocked in the Linux tty driver, so a write guard built on `Close` cannot work either. The design brief's "30 s write timeout" has no counterpart in the Rust code and was not added. |
| Serial errors carry go.bug.st/serial's texts: an error number it passes on from the operating system reads as the C library writes it ("transport error: No such file or directory", as in Rust), but the library's own errors keep its wording ("Serial port busy" where Rust's serialport said "Device or resource busy", "Invalid serial port: …"). | The serial library is another library; only the error numbers' texts could be written as strerror(3) writes them, which Go's lower-cased table hides (`internal/errnotext`). |
| On Unix, go.bug.st/serial waits for input with select(2), whose descriptor sets hold the numbers below 1024, so the reads of a port opened at or above that number fail with `ErrTransport` (section 5), where Rust's serialport waits with poll(2) and reads any descriptor. | The library's `Read` builds an x/sys/unix `FdSet`, which panics beyond its 1024 bits, and Go's runtime raises `RLIMIT_NOFILE` at start-up, so a process holding about a thousand files and sockets reaches that number (upstream bugst/go-serial#128). `Transport.ReadTimeout` recovers that runtime panic, and only for a port with a descriptor from 1024 up, so `Initialize` fails and `Connect` and mobile close the port; any other panic passes on as the bug it is, a runtime error on a port select(2) can watch included, and off Unix every one. poll(2) in the library, upstream, would remove the limit. |
| `serial.Transport.ReadTimeout` with an empty `p` returns (0, nil) at once; Rust's waited for input or the timeout (serialport's poll), then read nothing: `Ok(0)` either way. | go.bug.st/serial's Unix `Read` takes the zero that read(2) returns for an empty buffer to mean that the port hung up, and fails an open port with input waiting with "Port has been closed", so the library is not asked (`TestAnEmptyReadOfAPortWithInputWaitingReadsNothing`). Returning at once is `io.Reader`'s rule for an empty buffer, and what both simulators' reads do. A difference of the serial library, not a Rust bug. |
| ppp's automaton sends a fresh Configure-Request with a new identifier at the restart timeout that follows a Terminate-Ack in Ack-Received, whether the timeout finds Request-Sent or Ack-Sent — the peer's own request acknowledged meanwhile, as when an active peer's retransmission comes first. The Terminate-Ack itself sends nothing, as in Rust (`fsm.rs:426-429`) and RFC 1661 (RTA in Ack-Rcvd). Rust snapshot 4's `on_terminate_ack` (`fsm.rs:420`) moves to Req-Sent with its request still marked answered, `handle_timeout`'s `RequestSent \| AckSent` arm (`fsm.rs:217`) retransmits it under the old identifier, and `is_answer_to_request` (`fsm.rs:511`) ignores every Ack, Nak and Reject of it, so LCP — or IPCP, as the two share the automaton, while the peer has brought IPv4 up — ends with `NegotiationTimedOut` after Max-Configure (about 55 s with the default timers) instead of recovering. A probe against snapshot 4 shows it: an RFC 1661 peer that negotiates again after its Terminate-Ack acknowledges each of the eleven requests, all with identifier 1, and the layer gives up at 55 s; with the change below it is up at 5 s. | A Rust bug: RFC 1661 §5.1 requires the identifier to change once a valid reply has been received, and a peer that negotiates again after its Terminate-Ack (an RFC peer in Stopped, after a Max-Configure timeout of its own or a catastrophic reject) then brings the link up one Restart later (`TestATerminateAckAfterOurRequestWasAcknowledgedStartsOver`, `TestALinkRecoversFromAStrayTerminateAckAtTheNextRestart`). The project's own ends never send such a Terminate-Ack. Worth reporting upstream: the same few-line change in `fsm.rs`'s `handle_timeout`. |
| A ppp Configure-Nak suggests one value per option kind, the first, however often the peer's request repeats the option (`suggestOnce`); Rust snapshot 4 suggests one per instance (`fsm.rs:324`). Some suggestions are longer than the option they answer — an empty Authentication-Protocol option (2 octets) draws the 5-octet CHAP-MD5 value, an empty IP-Address option a 6-octet address — so in Rust a request repeating one draws a Nak that grows with it, and one frame makes `Packet::to_bytes` panic (`packet.rs:101`, "control packets are built far smaller than 64 KiB") at an MRU of 26,218 or more for a client and 21,848 or more for a server, as a probe against snapshot 4 shows. | A Rust bug: every option LCP and IPCP Nak takes one value, so no request that follows the RFC is answered differently, and in Go the panic would end the process (`TestARepeatedOptionDrawsOneSuggestion`, `TestAHostileRequestCannotInflateTheNak`). A hostile peer reaches it only where the configured MRU lets so large a frame in: a direct ppp user may set one up to 65535, while satnet's MRU is its MTU, at most 1500. Worth reporting upstream. |
| ipstack does not reproduce four defects of smoltcp 0.14. Three the Rust workspace inherits: it cuts a SYN's window back to the receive buffer's free space, where smoltcp, reading the unscaled window with its own scale, takes data beyond a buffer of 64 KiB or more and then panics or hands the application octets the peer never sent; it ignores the FIN of a segment whose data the receive window cut short, where smoltcp takes it, ending the stream early and acknowledging an octet it never received; and while its receive window ends before it starts — after a peer's SYN that reaches a connecting socket before the socket's own, a SYN\|ACK to a socket without a receive buffer, or a FIN right at the window's end — it takes no segment at all, where smoltcp's acceptability tests, which compare modulo 2³², take one that starts or ends 2³¹ − 1 or 2³¹ past the window's start and then panic, place data beyond the receive buffer, or end the connection on a RST. The fourth is in smoltcp's `poll_at`: after a peer's SYN that reaches a connecting socket before the socket's own, or a SYN\|ACK to a socket without a receive buffer, the port's `PollAt` counts no window left, where smoltcp's `last_scaled_window` underflows and panics. | The first three are Rust bugs. The first needs a receive buffer of 64 KiB or more (satnet's `Tuning.TCPReceiveBuffer` has no upper bound), and then a hostile peer reaches it at once, and an honest one whose data arrives with its SYN\|ACK; the second, a peer that sends past the window, or window scaling, which rounds the window's end back; the third, a hostile peer, whatever the buffer. smoltcp's `SeqNumber` panics in release builds too, and in satnet a panic in the stack's poll ends the link. The fourth is smoltcp's alone: iridium-net and iridium-sim call only `poll` and `poll_ingress_single`, as satnet and sim call only `Poll` and `PollIngressSingle`, so neither program reaches it, and the port keeps `Interface.PollAt` from panicking on any input, as ipstack's own tests call it, and its fuzz target for the early SYN. Probes against smoltcp 0.14.0 and snapshot 4 show each of them; PORTING-ipstack.md, section 9, has the details, smoltcp's lines and the tests. Worth reporting upstream. |
| Log lines name the Go package (`[INFO  satnet] link closed: closed by the app`), where Rust's logger printed the module path (`[INFO  iridium_net::driver] …`). The texts after it are Rust's. | Section 2, Logging: the `"pkg"` attribute names the package, as Go has no module paths at run time. |
| A panic in a CLI `net` subcommand prints Go's `panic: <value>` and the goroutine's stack, where Rust's panic hook printed "thread 'main' (<thread id>) panicked at <file>:<line>:<column>:" and the message on the next line (recent toolchains print the thread's ID); both then hang up and fail with "internal error: the command panicked" (`TestAPanicDuringTheCallHangsUpAndFailsTheCommand`). Any other panic on the command's goroutine (readying the handset, a handset command, setting up the call) is reported the same way and ends the run with status 101, as the Rust runtime ends a program whose main thread panicked; the report comes once the handset is closed, where Rust's hook printed before unwinding closed it. A panic on another goroutine still ends the process with Go's status 2. | Go's panics carry no hook; the recovered value and `runtime/debug.Stack` stand in for it. Go's runtime ends an unrecovered panic with status 2, which the CLI, like clap, keeps for a command line that does not parse; and Go cannot recover another goroutine's panic. |
| A command whose standard output cannot be written — through a pipe to a `head` that has had enough, say — fails with "error: <the write's error>" and exit status 1, the handset commands that print (list, info, status, sms send, list and read, dial, at, monitor) included, as the net commands do in both versions: for a reader that has gone, "error: Broken pipe (os error 32)". An "(event)" line that standard error cannot take fails the command too; answer, hangup and sms delete print nothing else, and only that line can fail them. The Rust handset commands printed with `println!`/`eprintln!`, which panic when a write fails: "thread 'main' (<thread id>) panicked at …/library/std/src/io/stdio.rs:<line>:<column>:" and, on the next line, "failed printing to stdout: Broken pipe (os error 32)", with exit status 101, as a probe of the snapshot-4 binary shows for each of them. A write that fails with EBADF on Unix, to a standard output opened read-only, or with ERROR_INVALID_HANDLE on Windows, to a NULL standard handle (a stream that a process without a console, or one started by such a process, was not handed), is no failure in either version, and such a read is the end of standard input (section 4, Structure for tests); a Windows standard output opened read-only fails with ERROR_ACCESS_DENIED, in both versions. `--help`, `-V` and usage errors are best effort in both versions (exit 0, 0 and 2). | A Rust bug: a panic is for bugs, not for a reader that has gone, and a crash's status 101 misleads a script that tells crashes from failures; the Rust net commands write with `writeln!(…)?` and report the failure, so the panic comes from `println!`, not from a choice (`TestAHandsetCommandWhoseOutputCannotBeWrittenFails`, `TestAnEventThatCannotBeReportedFailsTheCommand`). |
| `net get` of an `https://` URL ends its TLS connection with a close_notify before TCP's FIN (`TLSConn.Close`), where Rust dropped its `TlsStream`, which has no `Drop`, so only its `TcpStream`'s FIN went: Go spends one TLS alert record more of airtime. | satnet's `TLSConn` cannot let a connection go without the close_notify; an API for that would serve this one caller. |
| The CLI's styles follow anstream's environment rules (`NO_COLOR`, `CLICOLOR_FORCE`, `CLICOLOR`, `TERM`, `CI`), with anstyle-query's Unix rule for `TERM` on every platform. On Windows anstyle-query takes an unset `TERM` for one that shows styles, and anstream then switches the console to ANSI escapes (`enable_ansi_colors`); the port does neither, so a Windows console without `TERM` gets no styles where the Rust CLI styled it. A terminal is what isatty(3) says is one — `TCGETS` on Linux, `TIOCGETA` on macOS, FreeBSD and OpenBSD, a console mode on Windows — so that, unlike Rust's `is_terminal`, MSYS and Cygwin terminals are not recognised on Windows; `net tcp`'s standard-input check uses the same test. The CLI builds where go.bug.st/serial v1.8.0 does: macOS (with cgo), Linux and Android, FreeBSD, OpenBSD and Windows; not NetBSD, DragonFly, Solaris, illumos, AIX or Plan 9, which the serial library lacks, nor js/wasm or wasip1, which have no signals to catch. On 32-bit platforms an SMS index or baud rate above 2147483647 wraps when converted to `int`. | The Rust CLI was documented for macOS and Linux; `satlink.Handset` and `serial.Open` take `int`s. |
| The CLI's interruptions: a SIGTERM the CLI was started with ignored is caught like any other; with SIGINT ignored at start, an interrupted run ends with exit status 130, a second later, rather than by SIGINT, which Rust first resets to `SIG_DFL`; on Windows, closing the console, logging off or shutting down starts the hang-up, and Windows ends the process a few seconds later, where ctrlc's handler returned at once and Windows ended the Rust CLI right away (Ctrl-C and Ctrl-Break hang up and exit with 130, as in Rust). | Go's runtime replaces an inherited `SIG_IGN` for SIGTERM with its own handler (it keeps one only for SIGHUP and SIGINT), so `signal.Ignored` cannot report it, and offers no way to restore `SIG_DFL` for an ignored SIGINT; its Windows console handler blocks so that the program can clean up. |
| The CLI's own lines on standard error wait for the log lines handed over before them, so the order is the causal one every time; Rust's logger thread and main thread raced, nearly always to the same order. At trace level the Rust order of the simulator's lines around "Connected to …" varies from run to run, where Go's does not. | Go's scheduler runs the command's goroutine before the log's, which put the summary ahead of the hang-up's log lines in four runs of eight. |
| mobile: lists are `Len`/`Get` types; optionals are ""/0/nil; enums are int constants; `NewX` functions become Java constructors (Objective-C and Swift call the functions); errors carry only their message across the boundary (`AsSatlinkError` recovers the details); objects have `Close() error`; unsigned types are signed, durations and byte counts `int64`; `default_link_settings(allow)` → `NewLinkSettings()` + `Allow(rule)`, `default_tls_settings()` → `NewTLSSettings()`; `receive` returns a `Received` (with `Finished` at the end of the stream) or a `Datagram` instead of a byte array; `hang_up` returns a `LinkState` instead of an optional reason; the FFI's `TlsConnection.shutdown_write(timeout_ms)` is `ShutdownWrite`, and `Close` releases; the establish functions' listener and cancellation are nil-able parameters, where UniFFI had default arguments, so `EstablishSerialLink` takes five; `abort` returns an error (for a zero receiver, and after `Close`), where Rust's returns nothing. `LinkSettings` and `TLSSettings` are objects, not the Rust value records: each call that takes them reads them once, as it starts; their `Allow`, `AddHost` and `Add…` methods are safe from any thread (a mutex), but their exported fields, which gomobile's setters write directly, must not be set while another thread hands them to a call. A closed `IridiumHandset` refuses further use, as UniFFI's destroyed objects threw. | gomobile's type rules: no unsigned, list or optional types, `int` passed as a 32-bit C `int` on armeabi-v7a and Apple platforms, and empty byte arrays and nil pointers handed to Java as null and to Swift as nil, where a nil result without an error throws. |
| mobile adds the error kind `SatlinkErrorInternal` (recovered panics) and maps every satnet error, including `ErrTLSTruncated`; a stream's `Receive` (TCP, TLS) requires `maxLen` ≥ 1 (with 0 it could return neither data nor the end of the stream); `UDPConnection.Receive` takes 0, as Rust's `receive(0, …)` did, waiting for a datagram and keeping none of it, and refuses only a negative `maxLen`, which Rust's `u32` could not express. | A panic crossing the gomobile boundary ends the process. |
| In Java and Kotlin, gobind's generated `equals` and `hashCode` compare a class's exported fields only. So every `IridiumHandset`, `SatelliteLink`, connection, `TLSConnector`, `Cancellation` and list equals every other of its class, with one hash code; `LinkSettings` and `TLSSettings` compare without their rules, hosts, roots, pins and ALPN protocols; `CommandResponse` and `LinkStatus` compare their lists as the lists do, that is always equal; `Received` and `Datagram` compare `Data` by array reference. In Objective-C and Swift every object, records included, compares by identity, as gobind writes no `isEqual:`. UniFFI 0.32.2 compared Kotlin objects by identity (iridium-ffi exports no `Eq` or `Hash`) and records, which are data classes, by all their fields; its Swift records were `Equatable`, `Hashable` structs. | gobind generates these methods, and the remedies are worse: an exported identity field would give Java a public setter and Swift a settable property, letting apps change an object's identity (and its hash while it sits in a set); reverse-binding a Java superclass, which skips the methods, needs gobind-only `Java/...` imports that break `go build` and `go test`. So mobile documents the behaviour (`doc.go`'s Equality section, `Example.kt`'s header) and `bindings_test.go` pins it. |
| No Kotlin/JVM desktop bindings (`build-bindings.sh` made them with UniFFI). | gomobile targets Android and Apple platforms only. |
| Random values come from `crypto/rand` instead of a randomly keyed SipHash. | Standard library equivalent. |
| The simulator's calendar uses the `time` package instead of Hinnant's days-from-civil algorithm; the Rust tests still pass. | Standard library equivalent. |

---

## 4. Implementation notes per package

### satlink (core)

- **Handset internals.** One `sync.Mutex` serialises exchanges (line buffer,
  URC queue, transport I/O). A separate claim — set by `DialData`/`AnswerData`
  with compare-and-swap before taking the mutex, cleared when the call ends —
  makes the handset busy. Public methods check the claim first (fail fast with
  `ErrBusy`), take the mutex, and check again (a dial may have started
  meanwhile); the lock and the claim are `busy.go`'s (`exchange`, `perform`,
  `lock`, `claim`, `release`). As a dial claims before it takes the mutex,
  the methods called while it waits for an exchange in progress (a
  `PollURCs`, say) also fail with `ErrBusy`, and its `Cancellation` is
  looked at only once it holds the lock; the docs of `DialData`,
  `AnswerData`, `claim` and `ErrBusy` say so. Argument checks that Rust makes before touching the handset
  (`DialData`'s number) come before the claim; every other check, the escape
  guard of `EndDataCall` and of `DialData`'s preparation included, runs once
  the handset is held, so a busy handset reports `ErrBusy` first. The
  `DataCall` uses the transport without the mutex while it holds the claim,
  and takes the mutex for its hang-up. `SetTimeout` does not wait for a
  90-second dial: the timeout is an atomic. `Close` does not take the mutex
  either; it closes the transport once (`sync.Once`), which makes a read in
  progress fail, and reports a closer's failure as an `ErrTransport`
  `*Error` wrapping it.
- **Transport errors.** One helper wraps them: a `*Error` passes through,
  anything else becomes `ErrTransport` with `Err` set. A transport without
  `DTRController` gives `ErrTransport` "this transport has no DTR line".
- **Exact texts.** `%q is not a dialable number`, "SMS text must not contain
  Ctrl-Z or ESC", "command must not contain CR or LF", `%d is not an SMS
  filter`, "an escape guard of %s is longer than %s" (ErrInvalidArgument,
  checked before anything is sent), "unexpected CONNECT after %s: the
  handset is online", "ATA answered a voice call, not a data call", "the
  handset keeps sending data: it is still online", "ATA answered a data
  call, which was ended again" (ErrCall); "the handset does not answer (%v),
  and ending a data call it may be in failed: %v", "ATH failed on a data
  call left up: %v" and "ATA answered a data call that could not be ended:
  %v" (ErrCallMayStillBeUp, each written "the call may still be up: …");
  "the call setup was cancelled" (ErrCancelled); ErrTimeout for the
  commands "AT", "ATH" and "+++" (below), and for AT&D2 and AT+CBST;
  "%s returned no data", "no %s line in %s", "%s: bad field %d in %s",
  "no +CMGR header in %s" (ErrParse). The lists in the ErrParse texts are
  written by `debugList(items []string) string`: "[", each item as `rustdebug.Quote` writes it,
  joined by ", ", then "]" — Rust's `{:?}` of a `Vec<String>`. The escape guard text writes both durations with
  Rust's `{:?}`, which `time.Duration.String` matches only below a minute
  ("1m30s" where Rust writes "90s"): `debugDuration` writes a duration of a
  second or more as Rust's `Debug` does — whole seconds, then up to nine
  fractional digits without trailing zeros, then "s" ("10s", "10.5s", "90s",
  and "9223372036.854775807s" for `math.MaxInt64`) — and zero as "0ns",
  where `Duration.String` writes "0s".
- **Lines.** The line buffer turns each line's bytes into text as Rust's
  `String::from_utf8_lossy` does (`internal/utf8lossy`: one U+FFFD per
  maximal subpart of an ill-formed sequence), then trims CR, LF and NUL.
  The echo is the command but for the case of ASCII letters
  (`asciicase.EqualFold`, Rust's `eq_ignore_ascii_case`), and
  `mayGoOnline` and `responsePrefix` fold ASCII letters only too: Unicode
  folding would take "ſ" for "s".
- **Numeric fields.** One helper parses them all, as Rust's `parse_num` with
  its `u8`/`u32` `FromStr`: `strconv.ParseUint(strings.TrimPrefix(field,
  "+"), 10, bits)`, with 8 bits for +CSQ, +CBC (status and level) and the
  +CREG stat, and 32 bits for the +CMGL index and the +CMGS reference. A
  leading '+' is accepted; '-', other signs and values out of range are
  refused; any failure is ErrParse "%s: bad field %d in %s". (`strconv.Atoi`
  would take "-1" and "300", `ParseUint` alone would refuse "+4".) On 32-bit
  platforms, a `u32` field above `math.MaxInt` is refused the same way, as
  an `int` cannot hold it. `at_test.go` covers "+4", "-1" and "300".
- **Deadlines** (`deadline.rs`): `deadlineAfter(timeout) time.Time` is
  `time.Now().Add(timeout)` — some 292 years ahead for `math.MaxInt64`,
  which is no deadline in effect — and `timeToWait(deadline)
  (time.Duration, bool)` returns the time left, at most `longestRead` (one
  second, Rust `LONGEST_READ`), and false once the deadline has passed.
  Every loop that waits on the transport uses them: command execution, the
  wait for a final result (the abort grace), `PollURCs`, the escape's
  `discardUntil` and `awaitOwnResult` (`handset.rs:243-266, 527-621`,
  `data_call.rs:413-427, 498-505`). `hangUpTimeout()` is the handset's
  timeout, but at most `maxHangUpTimeout`, ten seconds (Rust
  `HANG_UP_TIMEOUT`): what follows an unanswered ATH — dropping DTR, or
  reporting that the call may still be up — must not be put off for good,
  even with a timeout of `math.MaxInt64`. It bounds `HangUp`'s ATH (and so
  `leaveDataMode`'s), `hangUpWhileMaybeOnline`, and `prepare`'s AT&D2 and
  AT+CBST, whose timeout is what has a call left up ended.
  `TestAHangUpWaitsForTheCommandTimeoutButAtMostTenSeconds` pins the cap's
  value with literals; the integration tests that reach each capped wait
  without a command deadline bound it only by 30 s (`unendingWait`).
- **The handset's own answers** (`data_call.rs:413-427`): `awaitOwnResult(deadline)
  (bool, error)` reads, sending nothing, until a `resultCodeWatch` sees
  "\r\nOK\r" or "\r\nNO CARRIER\r", which a call's data never holds (PPP
  escapes CR), and reports whether it did by the deadline. It confirms the
  escape, the raw `ATH` sent while the handset may still be online, and
  `synchronize`; that NO CARRIER confirms the first two is pinned by
  `TestNoCarrierConfirmsTheEscape` and
  `TestNoCarrierConfirmsTheATHAfterAnUnconfirmedEscape`.
- **Synchronising** (`handset.rs:297-310`): "AT\r" written raw, confirmed by
  `awaitOwnResult` within two seconds, three times, then ErrTimeout "AT": a
  call left up relays the far end, whose data may hold an `OK` line.
  `Initialize`'s first drain (300 ms of quiet, a named constant) gives up
  after `RESYNC_LIMIT`, two seconds, even while data keeps coming
  (`handset.rs:630-638`).
- **Resynchronising** (`handset.rs:268-295`): read until 200 ms of quiet,
  keeping URCs; still receiving after `RESYNC_LIMIT` means a call's data:
  clear the line buffer and fail with ErrCall "the handset keeps sending
  data: it is still online". It runs before a dial (`prepare`), before
  `Answer`'s ATA, in `isInVoiceCall`, and after the DTR drop.
- **Stale input.** Go-only tests pin the steps that keep stale input from
  deciding a call, which no Rust test covers: the line buffer cleared once
  resynchronising has kept the URCs (`TestAPartialAnswerLeftBeforeAnswerDoesNotPassForATAs`,
  `TestAPartialAnswerLeftBeforeADialDoesNotDecideIt`) and when it gives up
  on a handset still online (`TestTheDataOfACallLeftUpAreNoPartOfTheNextAnswer`);
  `isInVoiceCall`'s resynchronisation before its AT
  (`TestAVoiceCallWhoseAbortWasAnsweredLateIsKept`); the resynchronisation
  after the DTR drop, which drops the handset's answer to the drop
  (`TestTheAnswerToADTRDropDoesNotPassForATHs`), and two seconds of data
  after the drop ending the hang-up before any ATH (an assertion the port
  adds to `TestAHandsetStillRelayingDataAfterDTRDropsIsReportedPromptly`),
  which only scripted tests can pin, as both simulators, Rust's
  (`iridium-sim` `modem.rs:186-191`) and Go's (`sim`'s `dropDTR`), answer
  nothing to a DTR drop; the abort's wait
  queueing URCs only (`TestURCsDuringTheAbortAreQueuedAndOtherLinesDropped`);
  `RESYNC_LIMIT`'s lower bound, a late listing that keeps coming for a
  second, for both the resynchronisation and `Initialize`'s first drain
  (`TestALateAnswerThatKeepsComingIsNoCallLeftUp`,
  `TestALateAnswerThatKeepsComingDoesNotHideAHeldCall`); the quiets the
  handset waits for, against a late answer inside them
  (`TestALateAnswerArrivingDuringInitializeDoesNotHideAHeldCall`,
  `TestALateAnswerArrivingDuringAnswerDoesNotPassForATAs`,
  `TestALateAnswerArrivingWhileADialIsPreparedDoesNotDecideIt`), an escape
  and an abort confirmed late (`TestAnEscapeConfirmedLateButWithinThreeGuardsIsNotRepeated`,
  `TestAnAbortsResultArrivingSecondsLaterStillEndsTheAttempt`); the
  settings' escape guard for `DataCall.HangUp` and for an abandoned
  attempt (`TestTheEscapeKeepsTheSettingsGuardOfSilence`,
  `TestACallThatConnectsAfterTheTimeoutIsEndedWithTheSettingsGuard`), and
  the quiet counted from CONNECT (`TestTheEscapeKeepsAGuardOfSilenceAfterConnect`)
  and from the call's last write (`TestTheEscapeCountsItsGuardOfSilenceFromTheLastWrite`);
  `PollURCs` returning as soon as a URC arrives
  (`TestPollURCsReturnsAsSoonAsOneArrives`); `Initialize` ignoring a
  refused AT+CMEE=2 and AT+CMGF=1
  (`TestFirmwareRefusingTheOptionalSettingsIsStillInitialised`) but not a
  refused ATE0 or ATV1 (`TestFirmwareRefusingARequiredSettingIsNotInitialised`); a
  cancellation looked at every fifth of a second
  (`TestADialLooksAtItsCancellationEveryFifthOfASecond`); and reads of at
  most a second, written as a literal (`TestACommandWithoutADeadlineReadsASecondAtATime`).
  These tests set lower bounds by design (resyncQuiet above 150 ms,
  initialQuiet above 250 ms, the escape's confirmation window above 2.5
  guards, the silence before `+++` above a guard after the last write,
  syncTimeout above 1 s, abortGrace above 3 s, cancellationCheck at most
  200 ms): a machine stalled longer than the wait under test could let
  a shortened constant go unnoticed, but can never fail the real code.
- **Initialize** (`handset.rs:186-219, 312-329`, `synchronize.go`): ESC and
  CR, the first drain, then `synchronizeEndingACallLeftUp`: a handset that
  does not answer may be relaying a data call a previous session left up,
  which `endDataCall(DefaultEscapeGuard)` ends before synchronising once
  more; a handset that still does not answer once ending a call failed is
  ErrCallMayStillBeUp "the handset does not answer (…), and ending a data
  call it may be in failed: …" (otherwise the second synchronisation's
  error). Then `endCallHeldInCommandMode`: a handset that answers may still
  hold a call in online-command mode — escaped from but never hung up —
  which `isInDataCall` reveals (`AT+CPAS` reports 4, `dataCallInProgress`):
  it is hung up with `hangUp`, whose failure is ErrCallMayStillBeUp "ATH
  failed on a data call left up: …". Firmware without +CPAS cannot tell
  (any error, or another state, is "no data call"), and is left as it is.
  Then `configure`: ATE0, ATV1, and the optional AT+CMEE=2 and AT+CMGF=1.
- **Data calls** (`data_call.rs:171-369`). Rust's `ResultCodeWatch` is ported as `resultCodeWatch`
  (codes split across reads; bare-CR codes only). `prepareAndStart` is
  Rust's `Setup::run` (`data_call.rs:258-276`): `prepare` first refuses an
  escape guard over `MaxEscapeGuard`, before anything is sent, then
  resynchronises, then sends AT&D2 and AT+CBST, each within
  `hangUpTimeout()`; `preparationFailed` turns an ErrCall or an ErrTimeout —
  a call's result or its data without end, or silence where an answer was
  due — into `perhapsConnected`, as the handset may be in a call already,
  which must be ended, and anything else into `failed`. A cancellation
  already set then fails the setup with ErrCancelled, and nothing is
  dialled. `dialData` (until CONNECT: an early OK does not end the wait)
  and `answerData` (until the final result) send their command with
  `callRequest`: the connect timeout, the completion and the cancellation.
  A wait that stopped before its final result — `isUnfinished`: ErrTimeout,
  ErrCancelled or ErrTransport — goes to `abortAttempt`, whose
  `abortCallAttempt(untilConnect)` sends the abort byte and waits ten
  seconds (`ABORT_GRACE`) for a final result, ignoring OK, which may still be
  unread when the transport failed: a dial's early one, or a voice call's
  that was answered. Only a final result other than CONNECT proves that the
  attempt ended (the outcome is then that error, no call). A CONNECT, no
  final result, or a failing abort is `perhapsConnected`
  (`data_call.rs:246-255, 357-369`): `endAbandonedCall` ends the call with a
  fresh escape (quiet since now); success gives the original error without
  `CallMayStillBeUp`, failure the hang-up's error with it. `AnswerData`'s OK
  is a voice call: `hangUpVoiceCall` sends ATH and fails with ErrCall "ATA
  answered a voice call, not a data call"; should ATH fail, the call is
  still up, and it is `perhapsConnected` with ATH's error, so ended as a
  data call is.
- **The cancellable wait** (`handset.rs:541-621`, `exchange.go`): while a
  request has a cancellation, `exec`'s wait reads at most
  `cancellationCheck` (200 ms) at a time, and `cancellableWait` fails it with
  ErrCancelled once it is cancelled and `abortIgnoredFor` (200 ms) has passed
  since the command was written (`commandRun.sentAt`): a handset may ignore
  what it receives in the first 125 ms after a command (V.250 §5.6.1), and
  the abort that follows must not be lost. A request without one — every
  command but a data call's dial and answer — reads for all of the time
  left. Rust's tests cancel dials only; the Go-only
  `TestACancelledAnswerIsAborted` pins `AnswerData`'s cancellation.
- **Hanging up** (`data_call.rs:151-169, 371-505`, `hangup.go`). An
  unexported escape value (guard, quiet since, attempts) carries the
  escape's parameters: two attempts, but one after NO CARRIER. `endCall` is
  `leaveDataMode`, else `endCallByDroppingDTR`, reporting the first's error
  when both fail:
  - `escapeToCommandMode`: per attempt, discard input until a guard has
    passed since the last write, write "+++" in one `WriteAll` (satnet's
    faulty transport swallows exactly that write), and `awaitOwnResult`
    for three guards; once confirmed, discard input until a guard has
    passed since the "+++", as an early OK may come from a far end that
    ignores the ACCM, and a byte from us inside the guard would still
    cancel the escape;
  - `leaveDataMode`: escape, discard partial input, then, when the escape
    was confirmed, `hangUp`, `HangUp`'s own ATH (NO CARRIER counts as
    success: the call had just ended), and otherwise
    `hangUpWhileMaybeOnline` — "ATH\r" written raw and confirmed only by
    `awaitOwnResult` within `hangUpTimeout()` (ErrTimeout "ATH") — whose
    failure is reported as ErrTimeout "+++";
  - `endCallByDroppingDTR`: `DropDTR`, 150 ms of `time.Sleep` (the one sleep
    the specification requires), `RaiseDTR`, resynchronise (data still
    flowing shows the drop changed nothing; what the handset says to the
    drop, NO CARRIER or OK after AT&D1, is dropped too, so that it cannot
    confirm the raw ATH), then `hangUpWhileMaybeOnline`.

  `CallMayStillBeUp` is set as section 2 (Ownership) lists. The claim is
  released in every path out of `HangUp` and out of a failed dial, with
  `defer`: satnet recovers a transport's panic during the hang-up, and the
  handset must not stay busy after it. Before the release, a deferred
  `pulseDTRUnless(&returned)` pulses DTR when the hang-up's `endCall` — or,
  in `setUpCall`, the setup's `connect` — did not return, that is when a
  transport's panic unwinds through it: `pulseDTR` drops DTR for `dtrDrop`
  and raises it again, as `endCallByDroppingDTR` starts, so that after
  AT&D2 the call ends as Rust's dropped transport ended it (section 3). It
  is best effort: an error is dropped, as there is no caller to tell, and
  so is a second panic, which its own deferred `recover` takes — the panic
  being unwound is beyond that `recover`'s reach and goes on with its value
  and stack, no recover-and-repanic involved. The pulse runs under the
  lock, before the release, so that no other caller's command reaches the
  handset while DTR is down (`TestADialThatPanicsPulsesDTRBeforeReleasingTheHandset`,
  `TestAHangUpThatPanicsPulsesDTRBeforeReleasingTheHandset`).
- **Answer** (`handset.rs:464-508`, `voicecall.go`): resynchronise first,
  as a late answer to a command that timed out would pass for ATA's. A
  CONNECT is a data call answered by mistake, which nothing else would end:
  `endAnsweredDataCall` ends it as `EndDataCall(DefaultEscapeGuard)` does,
  through the unexported path (no claim is involved), and fails with ErrCall
  "ATA answered a data call, which was ended again", or, if ending it
  failed, with ErrCallMayStillBeUp "ATA answered a data call that could not
  be ended: " and the hang-up's error. An ErrTimeout or ErrTransport means
  the handset may still be answering, perhaps a data call that would come
  up unseen: `abortAnswer` aborts with `abortCallAttempt(untilFinalResult)`;
  OK keeps the voice call, CONNECT has the data call ended
  (`endAnsweredDataCall` with ATA's error), and any other result returns
  ATA's error. With no result, or a failing abort, nothing says which, as
  ATA's OK may have been lost: `isInVoiceCall` (`callstate.go`) —
  resynchronise and synchronise (a handset that answers commands is not
  online), `+CPAS` not 4, and `+CLCC` listing a call in state "000"
  (`activeCall`; 006 is idle) — keeps the call, and otherwise it is ended
  as a data call.
- **HangUp** (`handset.rs:510-520`): ATH within `hangUpTimeout()`; OK when
  no call is up, and NO CARRIER when it had just ended, both mean that none
  is left.
- `NewHandset` panics on a nil transport.

### ppp

- **Internal design.** `packet` (`packet`, `configOption`, `outbox`, `code`
  whose `String` gives the Rust variant names used in "the peer does not
  understand %s"), a generic `automaton[N negotiator]`, the LCP policy
  `lcpNegotiator`, IPCP's `ipcpNegotiator` interface (implemented by
  `clientIPCP` and `serverIPCP`), the authentication exchanges behind the
  `authExchange` interface (`authenticatee`, `authenticator`), the HDLC
  `frameEncoding`, `frameDecoder` and `frame` — so that the `Endpoint`'s
  `encoding` and `decoder` fields do not share names with their types — and
  the FCS table (computed once, `var fcsTable = buildFCSTable()`). A
  Configure-Nak's options go through `suggestOnce` (one per kind, section
  3), and the automaton's failures through `fail(reason)`. Test helpers:
  `support_test.go` holds every helper used by more than one test file,
  among them `String` methods for the internal kinds, transitions,
  outcomes, verdicts and packets that only test failures print;
  `controlPackets` reads frames as a peer that asked for ACCM 0
  (`framesSent`), so that negotiated frames parse; `answerOf` makes a
  peer's Configure-Ack, -Nak or -Reject that repeats the request's options,
  and `nakOf` builds on it; `assertDeadUntilOpenedAgain`; `withoutFramesOf` drops
  one protocol's frames; and `testLink.advanceAsTheOwnersDo` runs timers
  only once `NextTimeout` is due, as satnet's driver and sim's gateway do.
- **Wire numbers against literals.** ppp's tests check the numbers on the
  wire against literals, not against the package's own constants, which
  both ends of every other test share (sim's gateway is itself a ppp
  endpoint): the ported `TestAcknowledgesATypicalPPPDConfigureRequest`
  writes Rust's literal option types 1, 2, 3, 5, 7 and 8; Go-only tests
  compare a PAP and a CHAP conversation byte for byte with RFC 1334's and
  RFC 1994's codes, check the code of every LCP packet the endpoint sends
  against RFC 1661's numbering, compare the client's first IPCP request
  with the reference vector, and compare the option lists of a
  Configure-Nak and a Configure-Reject with literals: RFC 1661 §5.3–5.4
  forbid reordering those options, and pppd's `lcp_nakci` and `lcp_rejci`
  discard an answer whose options are out of order, which neither sim's
  gateway nor a test between two Go endpoints could notice, as `applyNak`
  and `applyReject` take any order. The ported
  `TestARetransmittedRequestAfterOpeningIsAnsweredWithoutRenegotiating`
  writes Rust's `MAX_REPEATED_REQUESTS`, 3, out. A reference frame pins the default-ACCM
  boundary — 0x1F escaped as 7D 3F, 0x20 sent raw: `7E FF 7D 23 7D 20 21
  7D 3F 20 D5 79 7E` — computed by the same kind of independent reference
  script as the other vectors: the encoder and decoder share `isMapped`, so
  a round trip cannot catch that boundary.
- `Role` is opaque: the endpoint reads `role.server == nil` for the client.
  `ServerRole` deep-copies `ServerConfig`.
- Validation follows Rust's order — MRU, timers, keepalive, credentials —
  rejecting with them what Go can express but Rust could not (negative
  counts and durations, section 2). Then, for a server, the Go-only checks
  (section 5): every address (`LocalAddress`, `PeerAddress`, each DNS server)
  must be IPv4 — `Is4()`; the zero `Addr` and IPv6 are refused, as `As4`
  would panic on them — giving `ErrInvalidAddress`, then the authentication
  protocol, giving `ErrInvalidAuthProtocol`.
- `IPv4()` and `Event.IPv4` hand out copies (clone `DNSServers`).
  `TakeOutput` returns nil when there is nothing.
- **Redaction.** Six types implement `fmt.Formatter`. `Credentials`,
  `Role`, `ServerConfig` and `Config` do so with value receivers: every
  verb fmt hands a `Format` method prints the redacted `String`, `GoString`
  for `%#v`, and, for the
  three whose exported fields are their API, those fields with their names
  for `%+v`, as fmt prints a struct (`writeRedacted`, with the
  `stringWithFieldNames` of the `fieldNamer` interface; the opaque `Role`
  prints `String` then); without it, `%d` or `%t` would print the fields,
  password included. `*Authentication` has a pointer-receiver `Format` that
  prints as fmt prints a pointer to a struct — "&", then the struct with
  the same verb and flags; `<nil>`, or `(*ppp.Authentication)(nil)` for
  `%#v` — so that its texts, and sim's that print it, stay what they were,
  while one held in a slice, map or struct no longer leaks under `%s`, `%q`
  or `%t`, where fmt's report of a bad verb would print the pointer's target
  with method calls disabled: this is what protects a held pointer.
  `ServerConfig.Format`, `Config.Format` and sim's `Config.Format` print
  their `String`, their `GoString` or, for `%+v`, their fields with their
  names, all of which format the pointer with `%v`, `%+v` or `%#v`;
  `Config.Format` is needed besides, as a
  `*Config` held in a slice, map or struct and printed with a verb that
  does not fit a pointer would print its role's unexported credentials.
  `Endpoint` implements `fmt.Formatter` too, and its `String`, `GoString`
  and `Format` take it by value, its only value-receiver methods — the one
  deliberate exception to uniform receivers — so that a copy, which must
  not be made, or one held by value in an exported field, prints its role
  and phase ("client endpoint in phase Open"), never its fields; as a
  value receiver cannot tell a copy from the pointer, `GoString` always
  writes the pointer form, `&ppp.Endpoint{role:…, phase:ppp.PhaseOpen}`, as
  `NewEndpoint` returns it. fmt hands `%p` and `%w` to no method, and calls
  none while it reports a verb that does not fit its operand
  (`%!verb(type=value)`), nor on what it reaches through an unexported field
  of the caller's own types: so `%p` of a value that is not a pointer, and
  `%w`, print the password (section 3). The `Credentials` doc lists these
  cases, and the `Role`, `Config` and `Endpoint` docs point at it;
  `ServerConfig` and the server `Role` and `Config` keep their credentials
  behind pointers and stay redacted under every verb. go vet reports every
  misplaced `%w`, and `%p` of an `Authentication` value, but not `%p` of
  `Credentials`, a `Role`, a `Config` or an `Endpoint`, whose `Format`
  methods make it assume they take every verb. The Rust redaction test is
  a table — verbs `%v %+v %#v %s %q %x %X %d %t` × values `Credentials`,
  `*Credentials`, `Authentication`, `*Authentication`, a
  `[]*Authentication`, a map of `*Authentication`, a struct holding one,
  `ClientRole`, a `ServerRole` with `Authentication`, `ServerConfig`,
  `*ServerConfig`, a client and a server `Config`, a `[]*Config`, a struct
  holding a `*Config`, a client `*Endpoint`, a server `*Endpoint` that
  authenticated its peer, a `[]*Endpoint`, copies of a client and of a
  server `Endpoint`, and a struct holding a copy; and `%p` too for the 14
  values that keep their credentials only behind pointers — asserting that
  the password never appears.
- Magic numbers and CHAP challenges: `crypto/rand`. The authenticator name is
  `satlink-ppp`. A peer's PAP and CHAP messages are made readable as Rust's
  `readable` does: `String::from_utf8_lossy` (`internal/utf8lossy`), control
  characters dropped, white space trimmed.
- Decoder capacity: the larger of the configured MRU and `DefaultMRU`, plus
  header and FCS (every peer must accept 1500 bytes).
- Timer arithmetic is in `deadline.go`: `isDue`, `earliest`, and `scaled`,
  which saturates (section 3).

### satnet

- **Engine.** `sync.Mutex` + broadcast channel, swapped atomically, so that
  `notify` may run with the engine locked or not; every state change is
  still made under the lock, so no wake-up is lost. Every blocking operation
  goes through `waitUntil(terms, attempt)`, whose terms are one struct — the
  operation's name, a deadline function looked up again whenever the wait
  wakes, the context (or nil), the connection's closed flag — and which
  checks the closed flag first (`net.ErrClosed`), then the attempt, then
  whether the link is still alive, then the deadline (so data already
  received beats an expired deadline). A `Watcher`'s waits use a sibling,
  `waitFor`, without the alive check. Alive is Rust's `check_alive`
  (`engine.rs:54-60, 156-164`): usable — negotiating or up — or draining
  after a requested close (`Closing` with `CloseReasonRequested`), so that
  reads and waits under way carry on while written data is delivered;
  starting anything new — a lookup, a connection, a TCP write, a UDP send —
  still needs the link usable (`check_usable`). The hang-up aborts every
  socket, which ends those waits. `Close` sets the closed flag under the
  engine lock and notifies the engine, which wakes every call waiting on
  that connection. Deadlines are `time.Time`, zero meaning none; changing a
  connection's deadline notifies the engine so that a waiting call
  re-evaluates. `DialContext` waits on `ctx.Done()` too. `linkUp` restarts
  the idle clock only while negotiating: renegotiating later is not
  traffic (`engine.rs:176-193`); `isNegotiating` reports that PPP has yet to
  come up for the first time. The engine counts its waiting calls (the
  unexported `waiting`, read only by tests through `export_test.go`).
- **Configuration** (`config.rs:87-216`). `Config.Validate` is exported,
  as Rust's `validate` is since snapshot 3, so that an app — the CLI, before
  it announces a dial — can check sooner; `Establish` calls it before
  dialling, on a configuration whose `Hosts` it has cloned first, so that a
  caller changing its map during the dial changes nothing and races
  nothing. Validation follows Rust's order: MTU, link rate, TCP buffers,
  host names (and, in Go, IPv4 host addresses), durations (in Go first not
  negative, then at most a year), then the escape guard, "the escape guard
  must be at most 10s" (`satlink.MaxEscapeGuard` with `%v`), then the
  limits, refusing those too small to be of use:
  - (in Go) not negative, and at most a year;
  - a call time limit that leaves time to bring PPP up (`linkSetupTime`, 20
    s: 7 to 10 s of round trips for LCP, authentication and IPCP, with room
    to use the link after) and to hang up: "the call time limit must leave
    time to bring PPP up and to hang up: at least %v" (24s by default);
  - an idle limit of at least `minIdleTimeout`, `longestLookup` (10 s for
    each of `dns.MaxServerCount` servers) plus 5 s, as the idle clock counts
    only what the app does, not what it waits for: "the idle limit must be
    at least 25s, or the link hangs up while waiting for an answer";
  - a data limit of at least `minMaxBytes` (2048): "the data limit must be at
    least 2048 bytes, as PPP alone takes up to 500".

  Credentials that PPP refuses (a name or password over 255 bytes) are
  `ErrInvalidConfig` from `Establish` too, with ppp's `*ConfigError` in
  `Err` and `Message` empty: the text reads as Rust's, "invalid
  configuration: the username is longer than 255 bytes", and
  `errors.Is(err, ppp.ErrCredentialTooLong)` holds
  (`TestPPPRefusesWhatItCannotCarry`); the docs of `ErrInvalidConfig` and
  `Error.Err` say so, and mobile's mapping reads both fields.

  Hanging up takes `Config.hangUpTime()`: twice the escape guard plus
  `athAllowance` (one second); as the guard is at most ten seconds and not
  negative by then, it is between 1 s and 21 s, and the shortest call
  between 21 s and 41 s, where `%v` writes what Rust's `{:?}` writes.
- **Driver goroutine.** `Driver` is ported as is: 20 ms poll, 1024-byte
  reads, 8 bytes of framing overhead, the hang-up time from the
  configuration, the receive ACCM `1<<'\r' | 1<<XON | 1<<XOFF`, limits
  checked in Rust's order, drain on requested close within `CloseGrace`
  (none while still negotiating), no PPP goodbye before hanging up. Panics
  of the run (string, error, or "the link driver panicked", also for a
  value whose `Error` method panics: `panicMessage` recovers) are recovered
  into `CloseReasonInternal`, and the call is still hung up. It hangs up
  first of all, before even a log line of its own — a log handler can fail
  too — then
  logs at info "link closed: …" or, when the hang-up failed, at warn "link
  closed (…), but the hang-up was not confirmed, so the call may still be
  up: …" (`driver.rs:277-308`); then records `CallMayStillBeUp` and the
  final usage and signals completion. `Establish` waits for that completion
  when the link fails to come up, so the handset is released when it
  returns, and sets `CallMayStillBeUp` on its error when the status says so
  (`link.rs:102-116`). The configuration's `Call.Cancellation` (nil-safe)
  also stops a link that is still negotiating, with `CloseReasonRequested`
  (`setupCancelled`, Rust's `setup_cancelled`): nothing written could still
  need delivering. Whenever the cancellation is set and the link did not
  come up, `Establish`'s `setupFailure` reports `ErrHandset` wrapping
  `&satlink.Error{Kind: satlink.ErrCancelled}` — the same error as a dial
  cancelled before CONNECT — keeping `CallMayStillBeUp` from the hang-up.
  Should a panic get past the run — from the connections aborted as the
  hang-up starts, which a log handler can cause through ipstack's TCP state
  traces, or from the hang-up itself, a transport bug (`driver.rs:62-74`) —
  the goroutine's outermost `defer` recovers it: first `endCallLeftUp`
  hangs up a call the panic kept from being hung up (a second `HangUp` of a
  call already hung up only says "the data call is over", and a hang-up
  that itself panicked has had DTR pulsed by the core: section 3), under a
  `recover` of its own; then it records the usage until then, so that
  `Usage.ConnectedFor` counts the hang-up `endCallLeftUp` ran, and abandons
  the engine — `Closed`, with the reason it was closing for, or
  `CloseReasonInternal` with the panic's message if it was not closing yet,
  `CallMayStillBeUp` set, every socket aborted (`engine.rs:216-227`) —
  under a `recover` of its own too, notifies, and completes with
  `&Error{Kind: ErrLinkDown, Reason: Internal "the link's driver thread
  panicked", CallMayStillBeUp: true}` (`link.rs:302-316`); it never panics
  again, which would end the process. It hangs up before it abandons the
  engine, so that a link reported closed has ended its call and released
  the handset, as `Watcher.WaitClosed` promises and as mobile's port closer
  and reclaim rely on (`TestAPanicWhileTheHangUpAbortsTheConnectionsStillHangsUp`
  and `TestTheFirstOfTwoPanicsIsWhyTheLinkClosed` check it as `WaitClosed`
  returns). `engine.beginClosing` sets `Closing` before it aborts the
  connections, as `abandon` sets `Closed` first, so a link whose aborts
  panic still closes for the reason it was closing for (section 3).
  Other texts, with `logInfo`/`logWarn`/`logDebug`: "IPv4 up: " and the
  settings in Rust's `Debug` text (`internal/rustdebug`), "datagram dropped:
  …", "dropped an early repeat of …" (the repeat key in Rust's `Debug`
  form, with `message_len` in place of the hash).
- **Close.** `Close` = `RequestClose` + wait for completion; the first result
  is kept and returned to every later call. `runtime.AddCleanup` on the
  `Link`, with the engine (never the Link itself) as the argument, makes a
  collected Link still request the hang-up; collected `TCPConn`/`UDPConn`
  sockets are closed likewise. `Watcher` and every connection hold the
  engine, never the `Link` — the natural `*Link` field would keep a dropped
  link reachable, and its call billing, for as long as the app holds a
  connection or a watcher (mobile's listener holds one until the link
  closes).
- **Device** over `internal/ipstack` (`device.rs`). The stack may hand over a
  datagram whenever it asks: `Transmit` grants a slot every time, and a
  received datagram's reply slot sorts what goes into it the same way
  (`sort`):
  - urgent, sent at once without waiting for the line: TCP segments without
    data and without FIN — acknowledgements, window updates, SYNs, RSTs —
    and DNS queries from a port that is not an app UDP socket's
    (`setAppUDPPorts` before each poll), so that the app's own datagrams to
    port 53 wait for the line as data does (`isUrgent`);
  - the rest waits in a queue per flow — protocol, destination address and
    both ports, the payload's first four octets read with
    `wire.NewIPv4Packet`; a datagram without a flow is dropped — and the
    flows take turns, one datagram per line slot (`lineReady`), so that a
    busy connection cannot hold up the others; a resent TCP segment
    replaces its original while that still waits (the same sequence number,
    length, SYN and FIN: `isSameSegment`).

  `popOutgoing` returns the urgent datagrams first, then the next flow's
  oldest datagram while the line is ready, and records what each TCP
  segment tells its peer (`device.rs:97-195`). Per connection, an
  `observedConnection` holds what the peer acknowledged — from our SYN on,
  advanced only by `peerAcknowledged`, what the stack took off its send
  buffer having checked the acknowledgement, never the segment that carried
  it, which may be corrupt, forged, or ignored — and what we last told it
  (acknowledgement and window). A waiting segment whose data the peer
  acknowledged meanwhile and which tells it nothing new (`isNews`: a later
  acknowledgement or another window) is dropped as stale (`wentStale`),
  unless it is no longer than the one-byte keep-alive probe or carries SYN,
  FIN or RST, and logged at debug as "dropped a retransmission acknowledged
  while it waited"; a segment that goes is brought up to date with the
  newest acknowledgement and window we sent, and its checksum filled again
  (`bringUpToDate`), as some stacks drop a segment, data and all, whose
  acknowledgement is older than one they had. `clear` forgets the
  connections too.
- **Stack** over `internal/ipstack`, in three files (`stack.go`,
  `stackconnections.go`, `lookup.go`): `nextOutgoing` drops ICMP datagrams —
  the interface's port- and protocol-unreachable errors quote up to 548
  bytes of what arrived and may be addressed outside the policy
  (`stack.rs:177-187, 649-651`); the stack sets ACK delay 100 ms, Nagle
  off, the configured timeout and keepalive (none for sockets the app
  closed), four UDP slots, ephemeral ports from 49152 with a random start,
  a single /32 address, DNS servers from IPCP, of which it keeps
  `dns.MaxServerCount`.
  - Every TCP socket has an outbox, from `openTCP` until `removeTCP`: what
    the app wrote and the stack has yet to take (`sendTCP`). The send buffer
    bounds the outbox and the unacknowledged data together
    (`tcpUnacknowledged`), and a connection the app finished
    (`shutdownTCP`) takes no more (`tcpMaySend`). Each poll first feeds the
    connections whose flow does not wait for the line a segment's worth (the
    MTU less 40 bytes of headers), and closes our half of a finished one once
    its outbox is empty, so that the FIN follows everything written
    (`feedConnections`); the stack sends what it holds as soon as it may,
    and it may whenever it asks. After the poll, the device hears what each
    peer acknowledged during it, from the send queues before and after; a
    reset empties a send queue too, but forgets the endpoints, and counts
    for nothing (`passOnAcknowledgements`).
  - `configure` records the connections with undelivered data, in the
    outbox or the send buffer, as cut off when the local address changes
    (`cutOffUndelivered`): the stack resets every connection on the old
    address without a word, emptying its send buffer as if the peer had
    acknowledged everything — and the next poll may first hand it what
    waits in the outbox — so `WaitAcknowledged` reports a cut-off
    connection as `ErrConnectionReset`. Rust snapshot 4 counts the send
    buffer alone (section 3). `cutOff` holds the connections cut off by an
    address change, a reset or the hang-up, each recorded once
    (`recordCutOff`).
  - `resetTCP` resets a connection and drops its outbox, keeping the socket,
    so that its handle stays valid (`TCPConn.Abort`); `abortTCP` resets one
    the app let go of; `abortAll`, as the hang-up starts, aborts every
    socket and drops every outbox. Both drop an outbox through `dropOutbox`,
    which records a connection whose outbox held data as cut off, because an
    emptied outbox shows nothing of what never left; what the stack had
    taken stays in the socket's send buffer through the abort and needs no
    record. `WaitAcknowledged` then fails with `ErrConnectionReset` after an
    abort and `ErrLinkDown` after the hang-up, where Rust reports the data
    delivered (section 3). A closing socket is removed once CLOSED and
    without a peer — an aborted one forgets its peer only once its RST has
    gone — with its outbox and its cut-off entry (`removeFinishedSockets`,
    `removeTCP`). The cleanup that a `TCPConn` collected without `Close`
    runs is `closeTCPGracefully`, a `UDPConn`'s `releaseUDP` (the stack
    methods they reach are `closeTCP` and `closeUDP`); `sendBacklogs` is
    what `passOnAcknowledgements` compares, and `connection.unlessClosed`
    runs a change unless the connection is closed.
  - `UDPConn.LocalAddr` (Go only) reports the link's address at the time
    of the call, with the socket's port (`localEndpoint`, which
    `localUDPEndpoint` uses too): the socket is bound to a
    port only, so after PPP renegotiates a new address its datagrams leave
    from that one. TCP connections are reset by an address change, so
    `TCPConn.LocalAddr` keeps the address the connection opened from.
  - Lookups have an id of their own (`lookupID`), as the resolver fails a
    lookup alike whether a server said that the name has no address or none
    answered in time. `deliver` checks each arriving datagram for a DNS
    server's answer about a name (`parseDNSAnswer`): a response to a
    standard query with one A question, NOERROR or NXDOMAIN, from port 53
    of a kept server, with valid checksums, not addressed to an app socket's
    port; its name is read in lower case, its labels as Rust's
    `String::from_utf8_lossy` reads them. A failed lookup is
    `lookupNoAddress`, `ErrNameNotFound`, only after such an answer about
    its name, and otherwise `lookupUnanswered`, `ErrTimedOut` "DNS lookup":
    silence, a server failure, or a server that went away.
- **Pacing.** The repeat key of a DNS query is the source port, the
  destination and the whole message (`pacing.rs:110-116, 150-162`): a
  retransmission repeats the message byte for byte, while another message
  to port 53 may share its id and length. Rust hashes the message; Go keeps
  it as a string field, which keeps the key comparable, is exact, and costs
  a few hundred bytes per remembered query.
- **Policy.** Host names: lower case, no trailing dot, at most 253 bytes,
  labels of 1–63 letters, digits, `-` (not at either end) and `_`. A target of
  digits, dots and `/` only is an address or network (`netip.Prefix`, address
  kept as written). Port 0 never matches. The zero target kind, `targetNone`,
  matches nothing. `ParseAllowRule` and `ParsePortRange` share one parser of
  the ports part.
- **DialContext.** When `ctx` is done before the dial completes, it
  returns `&net.OpError{Op: "dial", Net: network, Err: ctx.Err()}`, as
  `net.Dialer` does; an `ErrTimedOut` becomes `&net.OpError{Err:
  context.DeadlineExceeded}` only once `ctx`'s deadline has passed (the
  operation's own deadline, taken from `ctx`, may fire before the context
  notices). Any other timeout — a DNS lookup that no server answered
  within 10 s, say — stays satnet's `*Error`, as `net.Dialer` keeps a
  resolver's error. Other networks: `net.UnknownNetworkError`; a malformed
  address: `*net.AddrError`.
- **TLS configuration.** `tls.Config{MinVersion: TLS 1.2, CurvePreferences:
  ringprovider.KeyExchangeGroups(), CipherSuites:
  ringprovider.TLS12CipherSuites(), ClientSessionCache:
  caseBlindSessionCache{sessions: tls.NewLRUClientSessionCache(0)},
  RootCAs, VerifyPeerCertificate: verifyChainKeys, NextProtos}`, with the
  groups (X25519, P-256, P-384) and TLS 1.2 suites (ECDHE with AES-GCM or
  ChaCha20-Poly1305) of rustls's ring provider, which sim's echo serves
  too (section 1, `internal/ringprovider`): crypto/tls ignores the curve
  list's order and sends a key share for the first group of its own fixed
  order, X25519, and P-384 only lengthens the ClientHello by 2 bytes;
  crypto/tls's TLS 1.3 suites, which a client cannot choose, are rustls's
  three. A TLS 1.2 session resumes by ticket only (section 3). The
  session cache keys the LRU cache by the server name in ASCII lower case
  (`asciicase.Lower`), as rustls's `ClientSessionMemoryCache` keys by a
  `ServerName`, whose equality and hash ignore ASCII case;
  `config.ServerName`, and so the SNI, stays as the caller wrote it, as
  rustls sends it, and crypto/tls checks a resumed session's certificate
  with `VerifyHostname`, which ignores case. `verifyChainKeys` (section 3,
  `tlskeys.go`) requires one verified chain without a refused key, as
  webpki needs one path; crypto/tls runs it after verifying the chains and
  before the handshake signature, on full handshakes only, and it checks
  nothing when no chain was verified (`InsecureSkipVerify` on a `Config()`
  copy), and refuses with `ErrTLS` (section 5). `isRefusedKey` compares
  ring's RSA minimum with `rsa.PublicKey.Size()*8`, the modulus rounded up
  to whole bytes, and its maximum with `N.BitLen()`. It is
  `VerifyPeerCertificate` because `VerifyConnection` stays the pins' alone:
  `cmd/satlink`'s `TestAnHTTPSFetchTrustsThePublicWebPKI` reads
  `VerifyConnection == nil` as "no pins". An app that sets its own
  `VerifyPeerCertificate` on a `Config()` copy replaces the check.
  Public roots come from `bundle.Roots()` of
  `golang.org/x/crypto/x509roots/fallback/bundle`, built once by
  `rootPoolOf`, with `AddCertWithConstraint` where a root has a
  constraint: NSS's distrust-after date (section 3); pins
  are checked in `VerifyConnection` against the SHA-256 of
  `PeerCertificates[0].RawSubjectPublicKeyInfo`. PEM: only `CERTIFICATE`
  blocks count ("invalid PEM: …", "the PEM data holds no certificate"); no
  roots at all is "no root trust anchors were provided". `pem.Decode`
  silently skips a section that does not decode, so `NewTLSConnector`
  compares the sections decoded with the number of "-----BEGIN " markers
  at the start of the data or of a line (`sectionStarts`; a marker inside a
  comment line begins nothing, as for `pem.Decode` and rustls-pki-types)
  and reports `ErrTLS` "invalid PEM: a section is malformed"; a
  `CERTIFICATE` block or `ExtraRootsDER` entry whose DER does not parse is
  `ErrTLS` carrying crypto/x509's error ("TLS: x509: …").
  `DynamicRecordSizingDisabled: true` keeps records as large as rustls's (up
  to 16 KiB of plaintext): crypto/tls would otherwise send its first 128 KB
  in records of about 1.2 KB, each with 22 bytes of TLS 1.3 overhead.
- **TLS server names** are checked before anything is sent, as rustls's
  `ServerName::try_from` does: valid if `netip.ParseAddr` accepts the name
  without a zone, or if the policy's host-name normalisation accepts it and
  its last label is not all digits (rustls-pki-types' rule); anything else is
  `ErrInvalidHost`. crypto/tls takes any name and fails only at certificate
  verification, after a full handshake's airtime. `tls_test.go` covers "bad
  name" and "1.2.3".
- **TLS connection** (`tls/stream.rs`). `TLSConn` wraps `*tls.Conn` over an
  unexported record buffer between crypto/tls and the TCP connection: the
  Go counterpart of the rustls session's own buffer, which the Rust stream
  relies on. crypto/tls alone cannot keep the Rust semantics: it makes every
  write error permanent (`halfConn.setErrorLocked`), so a write that timed
  out would break the session, and it processes post-handshake messages —
  TLS 1.3 session tickets — only while reading. The buffer is written
  against a consumer-defined interface, `tlsTransport` (`tlsrecords.go`),
  that `*TCPConn` satisfies: a net.Conn's `Close`, addresses and deadline
  setters, but not its `Read` and `Write`, which wait; `CloseWrite`,
  `Abort`, `WaitAcknowledged`, `PeerAddr`, plus unexported primitives of the
  same package —
  `trySend(p) (int, error)` (what fits now, Rust `try_send`),
  `tryReceive(p) (int, bool, error)` (Rust `try_receive`; the bool reports
  the end of the stream), `checkSendable` (Rust `check_sendable`: the link
  takes new data and our half is open), `waitWritable`, and `waitReadable`
  and `waitReadableOrWritable`, which are Rust's `wait_for_traffic(deadline,
  sending, changed)` without the flag (`tcp.rs:140-187`, operations "TCP
  write" and "TCP read"), and `announce(change)`, which makes a change that
  a wait's `changed` looks at under the link's lock and wakes the waits.
  The waits take a deadline function, looked up again whenever the wait
  wakes, so that a deadline moved while the TLS layer waits applies at
  once; `readUntil()` and `writeUntil()` are the connection's own
  deadlines. `Secure` keeps its exported `*TCPConn`
  signature and calls an unexported constructor that takes the interface,
  so tests can use a fake.
  1. crypto/tls's writes never wait and never fail but for a dead TCP
     connection: the buffer appends each record and pushes what TCP takes
     at once.
  2. Back-pressure is `TLSConn.Write`'s (`stream.rs:136-147, 205-247`): it
     hands crypto/tls chunks of at most min(16 KiB, the room left under the
     64 KiB limit), as rustls's `apply_limit` does; once the session is
     full (64 KiB of records pending, rustls's default buffer limit), it
     sends the pending records, until the write deadline, before taking
     more, as Rust's `take_plaintext` does through `send_records` — a full
     session makes a write wait rather than fail. After the last chunk it
     pushes the pending records until the deadline. At the deadline it
     returns the count crypto/tls took with `ErrTimedOut` "TCP write": those
     records stay pending and the session usable. `TLSConn.WriteSome`, Rust's
     `send_until` (`stream.rs:122-134`), hands crypto/tls only what the
     session has room for — waiting until the write deadline while it has
     none — then pushes the records until the deadline, where a timeout is
     no failure: `ErrTimedOut` means that nothing was taken.
  3. Every other call sends pending records first, so that what a timed-out
     write left leaves with it (`stream.rs:262-300`): the buffer's `Read`,
     which crypto/tls calls, pushes before reading and, while records
     remain, waits for readable or writable rather than readable;
     `WaitAcknowledged` pushes until its deadline, then waits for TCP's
     acknowledgement; `CloseWrite` and `Close` push before and after the
     close_notify. Records that TCP no longer takes — our side has shut
     down, or the link is closing — count as gone for a `Read`
     (`pushBeforeReading`): they must not keep it from what is still to
     read, and reading reports a connection that has ended. A write whose
     push times out with records left wakes a waiting `Read` to send them
     on, through the record buffer's change counter, announced under the
     engine lock (`TCPConn.announce`, the waits' `changed` function). Go
     counts only the records a write left unsent, where Rust also counts
     records another call took in: in Go no other call takes records in
     while a `Read` waits (item 4).
  4. `WaitAcknowledged` and `CloseWrite` then take in what TCP already
     holds, without waiting (`stream.rs:149-158, 302-332`): a
     `tls.Conn.Read` with the buffer in non-blocking mode (`tryReceive`;
     nothing left is a `Temporary` timeout, which crypto/tls survives), so
     that crypto/tls processes the session tickets and the next connection
     can resume even after one that never reads. Application data that read
     returns is kept in `TLSConn`'s read-ahead buffer, which `Read` serves
     first; the take-in stops once 16 KiB of read-ahead data is waiting
     (rustls's received-plaintext limit), so a connection that keeps calling
     `WaitAcknowledged` without reading cannot grow it without bound. The
     take-in is skipped while another goroutine reads (a `TryLock` of
     `TLSConn`'s read lock): crypto/tls holds its input lock for the whole
     of a blocking read, and that reader takes the tickets in anyway; Rust's
     waits leave the session unlocked. The take-in ignores
     `errNothingArrived`, the transport's errors and the end of the stream
     (`isEndOfStream`: crypto/tls's `io.EOF` after the server's close_notify
     or a FIN between records, and `io.ErrUnexpectedEOF` for a FIN inside a
     record), which a `Read` or a `Write` reports, not this. So
     `WaitAcknowledged`, `CloseWrite` and `Close` succeed after the server's
     goodbye or end, as Rust's `take_in_arrived_records`
     (`stream.rs:302-332`) breaks with `Ok` on `read_tls`'s `Ok(0)` and
     errors, and `Close` still sends its own close_notify.
  5. The first `ErrTLS` — a failed session: an alert, a bad record, a pin —
     is kept, and every later `Read`, `Write`, `WaitAcknowledged` and
     `CloseWrite` returns it ("once the session has failed, every call
     reports why").
  6. It records whether the transport reported its end: when crypto/tls
     returns `io.EOF` after the TCP end was seen, the server never sent
     close_notify, so `ErrTLSTruncated` is reported (crypto/tls's
     `io.ErrUnexpectedEOF` mid-record is truncation too); a close_notify
     makes crypto/tls return `io.EOF` before the TCP end is ever read.
  7. It returns the transport's errors unchanged, never wrapped: crypto/tls
     keeps a session after a read error only when `err.(net.Error)` — a
     plain type assertion, not `errors.As` — reports `Temporary`.
  8. `Write` and `WriteSome` check before each chunk they hand crypto/tls
     (`stream.rs:222-235`), so that what could never reach the server is
     refused rather than taken: once `CloseWrite` has set `goodbyeQueued`
     — before crypto/tls queues the close_notify, so that a write goes before
     the goodbye or not at all — a write fails with `ErrConnectionReset`, as
     TCP refuses a write after its own shutdown; and once the TCP connection
     or the link no longer sends, `checkSendable`'s error. Nothing is taken
     then, and no records are left to fail every later `WaitAcknowledged`.
     `writeFailure` reports crypto/tls's own refusals as the session's
     failure, or that `ErrConnectionReset`, when another goroutine's `Read`
     or `CloseWrite` got there first. `CloseWrite` called again after a
     timeout finishes the shutdown, and `Abort` is the TCP connection's.
  9. It ignores `SetDeadline`, `SetReadDeadline` and `SetWriteDeadline`.
     `TLSConn`'s own deadline setters set the transport's deadlines
     directly. crypto/tls calls the buffer's setters only in `closeNotify`,
     setting a 5-second write deadline and then now; the close_notify waits
     for room until the caller's deadline instead (none meaning forever), as
     in Rust. A full 4096-byte send buffer takes about 14 s to drain at
     300 B/s.
- **TLS handshake.** `Secure` validates the name, sets the transport's
  deadlines to now+timeout around `tls.Conn.Handshake()` — the buffer's
  `Read` pushes each flight while waiting for the server's — then pushes
  the pending records, the client's Finished among them, until the same
  deadline (`stream.rs:187-198`), whose error fails `Secure`
  (`TestAHandshakeWhoseFinishedCannotLeaveInTimeFails`), and clears the
  deadlines. Not
  `HandshakeContext`: on cancellation it closes the conn and returns
  context errors instead of Rust's `ErrTimedOut` "TCP read"/"TCP write". A
  connection closed during the handshake is `ErrTLS` "the server closed the
  connection during the handshake". On failure the transport is closed.
- **TLS close.** `CloseWrite` = take in what has arrived (reporting a failed
  session), close_notify (`tls.Conn.CloseWrite`), push the pending records
  within the write deadline, then the transport's `CloseWrite`
  (crypto/tls's own `CloseWrite` does not close the TCP half). `Close` first
  takes in what has arrived, as Rust's close (`shutdown_until`) does, so a
  connection closed without `WaitAcknowledged` still keeps its session
  tickets; then `tls.Conn.Close`: the close_notify goes into the buffer,
  whose `Close` pushes the pending records within the write deadline and
  then closes the transport gracefully. As in Rust, a session that has
  failed gets no close_notify: its TCP connection is just closed. When no
  close_notify can be sent at all — the connection was reset, by `Abort`
  or by the server, or the link is closing — `Close` returns the TCP
  connection's `ErrConnectionReset` or `ErrLinkDown` and closes it all the
  same, and `CloseWrite` fails the same way; `TCPConn.CloseWrite` after
  `Abort` returns nil, as it sends nothing and Rust's `shutdown_write`
  cannot fail. A second `Close` returns nil, as for `TCPConn`. The link's
  own close drops records still pending, as in Rust: `WaitAcknowledged`
  first.
- **TLS tests.** The Rust tests of `tests/tls.rs`, including snapshot 2's
  `a_reader_keeps_sending_what_a_timed_out_write_left`,
  `a_write_that_times_out_counts_what_the_session_took` (section 2),
  `a_connection_that_only_sends_still_lets_the_next_resume` and
  `a_full_session_makes_a_write_wait_rather_than_fail`, which use a link
  paced at 50 000 B/s over the simulator's unthrottled line, as Rust does,
  and snapshot 3's reads woken by what another call took in or left unsent
  (a polling read included), reading after a write that came too late for
  the shutdown, writes refused once the goodbye is queued or the link
  closed, delivery confirmed before a reset, `abort`, and resending exactly
  what a send did not take (section 2).
  `tlsstream_test.go` (internal) runs over `faketransport_test.go`'s fake of
  the transport interface, an in-memory pair whose far end is an in-process
  `tls.Server`, with a certificate generated in the test with
  `crypto/x509`. The fake peer closes `handshook` after its handshake and
  `secureFake` waits for it: otherwise the server's last handshake read can
  take in records the test sent afterwards (with `GOMAXPROCS=1`,
  `TestAFullRecordBufferMakesAWriteWait` failed every run before this). The
  tests: a bare close gives `ErrTLSTruncated`, which also matches
  `io.ErrUnexpectedEOF`; a close mid-record gives `ErrTLSTruncated`; a close
  during the handshake gives the exact `ErrTLS` text; close_notify gives
  `io.EOF`; after `CloseWrite` the peer reads close_notify, then EOF; a read
  that timed out leaves the session usable; a write that timed out leaves it
  usable and its records leave with the next call; a failed session reports
  its `ErrTLS` on every later call; application data taken in by
  `WaitAcknowledged` is read later; a pin mismatch gives "invalid peer
  certificate: the public key is not pinned"; the server's end taken in
  before `WaitAcknowledged` failing nothing — a close_notify, a FIN after a
  record, or a FIN inside a record: `WaitAcknowledged` returns nil, `Read`
  the data, then `io.EOF` or `ErrTLSTruncated`, and `Close` nil, sending its
  own close_notify (`TestTakingInTheServersEndFailsNothing`); the handshake
  returning only once the client's last flight, its Finished among it, has
  left over a TCP connection that takes 16 bytes at a time
  (`TestTheHandshakeIsDoneOnceTheClientsFinishedHasLeft`, over
  `servePeerOver`), and failing with `ErrTimedOut` "TCP write", the
  transport closed, when that flight cannot leave by the deadline
  (`TestAHandshakeWhoseFinishedCannotLeaveInTimeFails`: a TLS 1.3 server
  stopped in its `VerifyConnection`, after its flight and before reading the
  client's, `servePeerStoppingAfterItsFlight`, then the fake's write
  deadline moved to now); a session resumed for its server name in any
  ASCII case (`TestASessionResumesWhateverTheCaseOfItsServerName`, over
  `secureFakeFor`); a close that cannot wait for
  the goodbye still wakes a waiting read
  (`TestACloseThatCannotWaitForTheGoodbyeStillWakesAWaitingRead`); a write
  after `CloseWrite` is refused with `ErrConnectionReset` without failing
  the session; records TCP no longer takes do not keep a `Read` from what
  arrived; a `CloseWrite` that timed out finishes when called again;
  crypto/tls's refusals say why; `WriteSome` takes what the session has room
  for and no more — at most one TLS 1.3 record's 22-byte overhead beyond the
  64 KiB, checked for `Write` too (`expectSessionFull`) — says how much it
  took when the connection is reset while its records wait, is refused as
  `Write` is, and takes nothing of an empty slice; data taken in before the
  session failed is read first (`TestDataTakenInBeforeTheSessionFailedIsReadFirst`,
  as Rust's `receive_until` reads plaintext before anything else); a
  `WaitAcknowledged` waiting or called later reports what an `Abort`
  dropped (`TestWaitAcknowledgedReportsWhatAnAbortDropped`), and a `Close`
  after an `Abort` says the connection was reset and lets it go
  (`TestACloseAfterAnAbortSaysTheConnectionWasResetAndLetsItGo`);
  resumption after a bare `Close`; `Close` and `CloseWrite` reporting a
  failure they take in; the read-ahead bound; no handshake deadlines left;
  `SetDeadline` setting both; `WaitAcknowledged`'s push and acknowledgement
  timeouts; `CloseWrite` after `Abort`; TLS read and write deadlines moved
  while a call waits; a `Read` not spinning on records TCP no longer takes;
  and Rust's unit test `a_failed_session_still_says_why_once_the_connection_ended`.
  `tls_integration_test.go`, Go only: over the default unthrottled
  simulator with the default 300 B/s pacing, since the uplink is what the
  goodbye waits for, it writes 6000 bytes with a 1 s write deadline (about
  2 KB of records stay in the session), then `CloseWrite` with a 30 s
  deadline must succeed and take over 5 s, then `ReadAll` returns the echo
  and `io.EOF` (the echo answers close_notify with its own), not
  `ErrTLSTruncated`. It takes about 26 s and is skipped under `-short`.
- **Exact operation names** in `ErrTimedOut`: "bringing the link up", "DNS
  lookup", "TCP connect", "TCP read", "TCP write", "TCP flush", "UDP send",
  "UDP receive".

### sim

- The modem, the gateway and the internet are sans-I/O and take `now`; only
  `handset.go` reads the clock, while holding the modem mutex. `ReadTimeout`
  re-polls every 2 ms; waits over a year count as a year.
- `Close` drops DTR (ending a call after `AT&D2`), marks the handset closed
  (reads, writes, `RaiseDTR` and `DropDTR` then fail with "the simulated
  handset is closed") and keeps `Control` working.
- Commands are matched with ASCII-only case mapping (`internal/asciicase`),
  as Rust's `to_ascii_uppercase`, and DNS names compare so too: Unicode case
  mapping would turn `ſ` into `S`, and the Kelvin sign into `k`, and accept
  commands and names the handset never saw. The bytes of command lines, SMS
  text and DNS labels
  become text as Rust's `String::from_utf8_lossy` makes it
  (`internal/utf8lossy`), which shows in `Stats().SMSSent`.
- `AT+CPAS` (`queryActivityStatus`) reports "+CPAS: 004" while a data call
  is connected, online-command mode included, and "+CPAS: 000" otherwise
  (`modem/commands.rs`; Iridium ISU AT Command Reference).
- **The network.** IPCP offers `NetworkConfig.DNSServers`, which
  `ppp.ServerRole` copies, in their order. `internet.deliver` drops a
  datagram whose destination, read with `wire.NewIPv4Packet` (Rust's
  `Ipv4Packet::new_checked`), is one of the `SilentAddresses`: nothing
  answers there. `Control.RestartPPP` goes to the modem (only while a call
  is connected), the circuit and `gateway.restartPPP`, which starts a new
  server with the new client address (`startServer`, Rust's `server()`); on
  success it replaces the endpoint and the network and closes the internet,
  which is rebuilt on the next Up; on failure it changes nothing, as Rust's
  `?` comes before its assignments. The web service answers data that
  arrives after it has closed with a reset (`Abort`), as a real server's
  closed socket does, so that the RST leaves on the same poll.
- **Network validity.** The gateway checks its network before it starts, as
  Rust's `Gateway::start` does: `ppp.NewEndpoint` refuses non-IPv4 addresses
  (`ErrInvalidAddress`) — the DNS servers among them — and a small MRU, and
  a `Hosts` entry whose address is not IPv4 is refused with `%q in hosts
  needs an IPv4 address` (a DNS A record holds four octets;
  `TestHostsMustHaveIPv4Addresses`, whose name holds U+007F, which `Quote`
  writes `\u{7f}` and Go's `%q` `\x7f`). `startServer`
  creates and opens the endpoint before the Go-only hosts check, so that
  the errors keep their order. Either way the call fails with NO CARRIER and the reason
  is logged at warn ("simulated handset: the PPP gateway cannot start: …"), as
  in Rust; `sim.New` never refuses a configuration.
- **TLS echo.** It cannot be sans-I/O with crypto/tls: `tls.Server` runs over
  an `internal/memconn` connection, with a goroutine per session. The service
  moves records between the ipstack socket and that connection on each poll,
  takes no more records while echo output is still waiting (as Rust does, so
  a client that never reads stalls), answers a client's close_notify with its
  own and FIN, and says goodbye with close_notify when the client sends FIN
  without one, in the middle of a record too: crypto/tls then fails its
  `Read` with `io.ErrUnexpectedEOF` rather than `io.EOF`, and `EOFDelivered`
  tells that FIN from a failure, as Rust's `hang_up` says goodbye whatever
  the record state. The connection closes only once the rest of the echo
  and the close_notify have gone (`isFinished`, Rust's `is_finished` with
  `!wants_write`). One `tls.Config` per simulated internet: `MinVersion` TLS 1.2,
  `CipherSuites` the TLS 1.2 suites of rustls's ring provider, ECDHE with
  an AEAD cipher, as satnet's client offers them (`internal/ringprovider`;
  crypto/tls's defaults would also serve a client that offers only the
  ECDHE suites with CBC and HMAC-SHA1, which the Rust echo refused),
  `CurvePreferences` X25519, P-256 and P-384 (rustls ring's groups, no
  post-quantum one) and `DynamicRecordSizingDisabled`; its automatic
  session ticket keys (there is no `SetSessionTicketKeys` call) let later
  connections to the same internet resume. crypto/tls picks among the
  suites a client offers by its own order, not by the client's as rustls
  did: at TLS 1.2 by choice, at TLS 1.3 because it must (section 3).
  `secureecho_test.go` pins these settings against clients with settings
  of their own: a TLS 1.2 client is served (`TestTheEchoServesATLS12Client`);
  one that offers only one of rustls's ECDSA AEAD suites (AES-128-GCM,
  AES-256-GCM or ChaCha20-Poly1305) is served with that suite
  (`TestTheEchoServesATLS12ClientThatOffersOnlyAnAEADSuite`); one that
  offers only AES-128-CBC-SHA or AES-256-CBC-SHA, which crypto/tls's
  defaults would serve, or AES-128-CBC-SHA256, is refused with a
  handshake_failure alert (`TestTheEchoRefusesATLS12ClientThatOffersOnlyCBC`),
  as the Rust echo answers each of them; neither echo can agree the RSA
  suites with secure.test's P-256 key; a client that prefers
  X25519MLKEM768 agrees X25519
  (`TestTheEchoAgreesNoPostQuantumKeyExchange`); the echo of a whole
  16 KiB record is one record of the same size, which covers `echoChunkLen`
  too (`TestTheEchoOfAWholeRecordIsOneRecord`). Certificate and key are embedded
  from `testdata/` and parsed once per process (`sync.OnceValue`).
- **TLS session lifetime.** A goroutine blocked in `tls.Conn.Read` on an
  in-memory connection is a garbage-collection root, so a session must end
  when Rust's would: with its `Internet`. Rust drops the `Internet` when PPP
  goes down (`gateway.rs`, `internet = None`) and when the network restarts
  PPP (`restart_ppp`), replaces it on a renegotiation that changes the MTU,
  drops it with the `Circuit` when the call ends, and frees it with the
  modem when the last handle goes away; and a connection that the services
  forget once its socket has closed (a client's reset) drops its `Session`
  with Rust's `retain` in `remove_closed_connections`. `Internet` has an
  unexported `close()` that closes every session's connection, ending its
  goroutine, called at each of those sites, and Go's
  `removeClosedConnections` closes a forgotten connection's session. For a `Handset` dropped with a
  call still up, `runtime.AddCleanup` on the shared modem state shuts the
  modem down, and one on the `Handset` itself drops DTR, as Rust's `Drop`
  did, so that a collected handset ends its call after AT&D2 even while a
  `Control` is held; session goroutines do not reference that state.
  `secureecho_test.go` (internal, `-race`) covers the session itself: echo,
  no records taken while echo output waits, a client's close_notify answered
  with close_notify, a client's FIN answered with close_notify, a FIN in the
  middle of a record answered with close_notify too, a session finished
  only once its last records have gone, a session that never got a hello, a
  failed session and its debug line, resumption on a later connection, and
  closing a session or the services ending the goroutines. The goroutine
  counts (`runtime.NumGoroutine` back to its baseline, polled with a
  deadline; the tests run serially) need the full client stack, so they are
  in `internet_integration_test.go`: after `DropCall`
  (`TestATLSSessionEndsWhenTheNetworkDropsTheCall`), PPP going down
  (`TestATLSSessionEndsWhenPPPGoesDown`), `Control.RestartPPP`
  (`TestATLSSessionEndsWhenTheNetworkRestartsPPP`), a client that resets
  its connection while the call stays up
  (`TestATLSSessionEndsWhenItsClientResetsTheConnection`), and a simulator
  nobody holds, whose call was dialled without AT&D2 so that only the
  cleanup of the shared modem state can end its session
  (`TestATLSSessionEndsWithASimulatorNobodyHolds`). So is the end-to-end
  check that a client that hangs up still gets the rest of its echo before
  the goodbye. `gateway_test.go`'s `TestARenegotiationThatChangesTheMTUBuildsANewInternet`
  checks that a renegotiation to MRU 1200 builds a new internet with that
  MTU and closes the old one's TLS session; the `Handset`'s own cleanup,
  which drops DTR, is `handset_test.go`'s `TestAHandsetCollectedWithoutCloseDropsDTR`.
- **`internal/memconn`.** One type, `Conn` (`New(local, remote
  netip.AddrPort)`), with two sides; its doc comments are the contract. The
  `net.Conn` side, for crypto/tls: `Read` blocks and honours the read
  deadline (`os.ErrDeadlineExceeded`, a temporary `net.Error`), `Write`
  never blocks, `Close` wakes a waiting `Read` with `net.ErrClosed`, and the
  deadline setters. The pump side, for a sans-I/O owner: `Inject(p []byte)
  int` (bytes from the peer; none taken once closed), `CloseInbound()` (the
  peer's FIN; `Read` then returns `io.EOF`), `Extract(p []byte) int` (bytes
  crypto/tls wrote, also after `Close`, so that a last close_notify or
  alert still leaves), `Pending() int` (output still waiting, for "no more
  records while echo output waits"), `AwaitReader(timeout) bool` (until a
  `Read` waits with nothing buffered and no FIN pending, or the conn is
  closed; after `CloseInbound` only `Close` satisfies it, so that an owner
  can await the session's goodbye in the same step: the owner calls it
  between injecting and extracting, so that the answer to what it injected
  is there in the same step, as with rustls's sans-I/O sessions),
  `EOFDelivered() bool` (whether `Read` has handed `io.EOF` to crypto/tls:
  crypto/tls returns `io.EOF` both for the peer's close_notify and for a
  bare FIN at a record boundary, and only this tells them apart), `Closed()
  bool`. The TLS echo uses it server side; the tests use it client side:
  `clientNetwork.startTLS(handle)` (`support_integration_test.go`) runs a
  `sim.TLSClient` (`tlsclient_test.go`) for `sim.TLSServerName` that trusts
  `SecureTestCAPEM`, pumped to and from the ipstack `tcp.Socket` on every
  exchange, with the helpers `echoInsideTLS` and `sayGoodbye` and
  `TLSClient.ServerSaidGoodbye`. satnet cannot stand in as the client: its
  `CloseWrite` always sends the FIN with the close_notify.
- Random seeds (TCP initial sequence numbers) differ between runs:
  `crypto/rand`.
- Logs carry `pkg=sim` and Rust's texts, formatted at the call site
  ("simulated handset: …", "simulated gateway: …", "simulated internet: …";
  the gateway's "IPv4 up, " line writes the settings in Rust's `Debug` text
  with `internal/rustdebug`). Every line has a test of its text and level:
  the modem's (`modem_test.go`), the gateway's (`gateway_test.go`), the
  internet's warning and the UDP echo's and DNS server's debug lines
  (`internet_test.go`), and the TLS echo's (`secureecho_test.go`: Rust's
  text, then crypto/tls's reason).
- Defaults are named constants with Rust's comments.

### serial

- **Open.** `serial.Open(path, &serial.Mode{BaudRate: baud, DataBits: 8,
  Parity: serial.NoParity, StopBits: serial.OneStopBit})`, without
  `InitialStatusBits`: with them, a firmware that refuses control requests
  would fail the whole open. Windows then enables DTR and RTS in the DCB,
  and on Unix open(2) raises both. Then `_ = port.SetDTR(true)` and `_ =
  port.SetRTS(true)`, each with a comment that Rust treats them as best effort
  too (serialport's `dtr_on_open` ignores the result; the RTS result is
  dropped). go.bug.st/serial opens Unix ports exclusively (`TIOCEXCL`) on its
  own (section 3). `Open` and `Connect` open through `openCloseOnExec`: on
  Unix it holds `syscall.ForkLock` for reading across the library's `Open`,
  and marks the port's descriptor and both ends of its close pipe
  close-on-exec, as Rust's serialport opens with `O_CLOEXEC`; without the
  marking, a child process started while the port is open keeps it open,
  and DTR up, past `Close` and a crash. `descriptorsOf` reads v1.8.0's
  unexported `unixPort.handle` and `closeSignal.rd`/`wr` by guarded
  reflection and never panics; the pty tests of close-on-exec and of
  select(2)'s limit fail if an upgrade renames the fields. The library's
  `Open` must not take `ForkLock` itself; v1.8.0's
  x/sys/unix `Open` and `Pipe` do not. On other platforms it is the
  library's `Open`, as Windows handles are not inheritable. Suggesting
  `O_CLOEXEC`, and poll(2) instead of select(2), to go.bug.st/serial
  upstream would let the reflection and the `recover` below go.
- **Closed flag.** `Transport` keeps an `atomic.Bool` closed flag and an
  `RWMutex`, `inUse`. Every library call that acts on the descriptor —
  each `Write`, the `Drain`, `SetDTR`, `SetReadTimeout` — holds `inUse` for
  reading while it checks the flag and runs (`usePort`); after `Close`,
  `WriteAll`, `ReadTimeout`, `RaiseDTR` and `DropDTR` return `ErrTransport`
  "the serial port is closed" without calling the library. `Close` sets the
  flag, stops the runtime cleanup, waits up to `closeGrace` (1 s, a
  `TryLock` every millisecond) for the calls under way, closes the port and
  unlocks. So no call acts on a descriptor number after `Close`, by which
  time the process may have reused it. A call stalled past the grace, on a
  device that stopped taking data, finds the flag set when it returns, and
  its `WriteAll` then neither writes nor drains anything more. A read's
  wait holds no lock: the library's close pipe ends it. What remains: the
  library closes the descriptor before it wakes readers, so a read that
  select(2) has just let through may read once from the number after the
  close; and on macOS the library's own close blocks behind a write stuck
  in the kernel, which the one-second grace cannot shorten. A `Transport`
  collected without `Close` closes its port all the same
  (`closeAbandonedPort`, registered with `runtime.AddCleanup` by `open`).
- `ReadTimeout` (`lib.rs:86-109`): an empty `p` returns (0, nil) at once,
  without calling the library (section 3), and after `Close` fails as
  every read does. Otherwise `deadline := time.Now().Add(timeout)`;
  each pass calls `SetReadTimeout(min(max(remaining, time.Millisecond),
  longestLibraryWait))`, then `Read`; the library returns 0 and no error on
  timeout. `longestLibraryWait` is a day: macOS's select(2) refuses a wait
  of more than 10^8 s, and the library's Windows port refuses one above
  0xFFFFFFFE ms and cuts one above 0x7FFFFFFE ms (some 24.8 days) down to
  that, which would report silence early, where Rust's serialport clamped;
  a longer timeout is waited out in turns. A read whose capped wait ended
  in silence, or that a signal interrupted (`errors.Is(err,
  syscall.EINTR)`; Ctrl-C, say), is retried with what is left of the
  timeout, and gives (0, nil) once the deadline has passed, as silence
  would otherwise seem to come early. go.bug.st/serial's Unix `Read`
  already retries EINTR from select(2) and read(2) against its own
  deadline; the loop keeps the promise for any port of the library's
  `Port` interface. The library's `Read` is called through `libraryRead`,
  which turns a runtime panic into the `ErrTransport` of section 5 only for
  a port select(2) cannot watch (`isBeyondSelect`: a descriptor from
  `descriptorsOf` of `selectSetSize`, FD_SETSIZE 1024, or more; section 3),
  and raises any other panic again: a runtime one on a port select(2) can
  watch is a bug, and off Unix `descriptorsOf` finds none, so there every
  panic passes on.
- `WriteAll`: write until everything is taken, then `Drain`. A library
  `Write` that takes nothing and reports no error fails with `ErrTransport`
  wrapping `io.ErrShortWrite` (Rust's `write_all` fails with `WriteZero`),
  so the write loop cannot spin.
- `Discover`: `enumerator.GetDetailedPortsList()`, USB ports whose VID parses
  (hex, any case) to `satlink.IridiumUSBVendorID`, with no active-probe
  filters, so no device is probed. A port whose PID does not parse is still
  reported, with `ProductID` 0 and `Model` "" (`parseUSBID` returns 0
  explicitly, as strconv's range error would give 65535). On macOS the
  enumerator reports only each port's callout node (`IOCalloutDevice`,
  `/dev/cu.*`), so Rust's filter of `/dev/tty.*` has nothing to do and is
  not ported. On Linux it reads sysfs: no libudev is needed.
- Errors are `&satlink.Error{Kind: satlink.ErrTransport, Err: err}`, or with
  `Message` for "no Iridium handset found on USB" and "several handsets found,
  choose one: a, b". An error number the library passes on from the
  operating system (`syscall.Errno`) is wrapped so that it reads as the C
  library writes it, with a capital first letter ("No such file or
  directory", as Rust's serialport reported it), and still matches
  `errors.Is` with the number and its `io/fs` errors.
- **Seams for tests**, without mutable globals: `Transport` holds
  go.bug.st/serial's `Port` interface, so internal tests build
  `&Transport{port: fake}`; `Open(path, baud)` calls `open(path, baud,
  openCloseOnExec)`, the seam that `ForkLock` and the reflection need, and
  `open` registers the runtime cleanup `closeAbandonedPort`; `Connect` calls
  `connect(path, baud, openCloseOnExec, Discover)`, which chooses the port
  with `choosePort(path, discoverPorts)`; `Discover()` calls
  `discover(enumerator.GetDetailedPortsList)`, whose filtering is a pure
  function; `ChoosePort(path)` calls `choosePort(path, Discover)`, which
  chooses among the ports found with the exported `ChoosePortFrom(ports)`,
  as the CLI does over its own discovery. The tests: `discover_test.go`, a
  table of `PortDetails` ("1edd" and "1EDD", a missing VID, a port that is
  not USB, known and unknown products, a PID that does not parse), the
  enumerator's order, no device probed; `transport_test.go`,
  `ReadTimeout`'s `max(timeout, 1 ms)` and the library's (0, nil), long
  waits read in turns, no read asking the library to wait longer than its
  Windows port or macOS's select(2) takes (both limits written as literals
  that cite the library's source, `TestNoReadAsksTheLibraryToWaitLongerThanItCan`),
  an interrupted read retried with the rest of its timeout and one whose
  time is up reporting silence, the panics of the library's `Read` passed
  on (`panicOf`): one that is no runtime error, and a runtime one of a port
  at descriptor 1023, which select(2) can watch
  (`TestARuntimePanicOfAPortSelectCanWatchIsPassedOn`, every platform) —
  where a port at 1024, FD_SETSIZE written out, fails with the exact text of
  section 5 instead (`TestAReadThatTheLibraryCannotWaitForFailsInsteadOfPanicking`,
  `transport_unix_test.go`) — `WriteAll` looping over
  partial writes then draining and stopping at a write that takes nothing,
  DTR calls reaching the port, `Close` idempotent and ending a blocked read,
  letting a write or DTR change under way finish before it closes the port
  (`TestCloseLetsACallUnderWayFinishFirst`, over a fake port whose stalled
  calls note whether they reached the port once it was closed), and giving
  up after the second it promises (`TestCloseGivesAWriteUnderWayAWhileButNoMore`,
  held to a literal second, not to `closeGrace`), a `Transport` collected
  without `Close` closing its port and a closed one not closed again, the
  closed flag (`TestAClosedTransportLeavesThePortAlone`, an empty read
  among the calls), errors wrapped as `ErrTransport` and error numbers written
  as the C library writes them; `connect_test.go`, both exact messages,
  `ChoosePortFrom`, a missing device ("transport error: No such file or
  directory" on macOS and Linux), `Connect` without a path opening the only
  handset discovered and opening nothing when there is none, and `Connect`
  closing the port when `Initialize` panics, the panic passing on unchanged
  (`TestConnectClosesThePortWhenInitializePanics`, a fake port whose reads
  panic with a string, which `libraryRead` raises again), and when it
  fails, over a fake port that fails every call (a port that never answers
  would take `Initialize` about 33 s
  to fail: three 2 s ATs, two escapes with the default guard, raw ATH, the
  DTR drop and ATH, then three more ATs); and the Unix pty tests of section
  1.
- macOS builds need cgo and the macOS SDK: the enumerator's IOKit code is
  cgo, and without it the build fails (`undefined:
  nativeGetDetailedPortsList`); this holds for `serial`, `cmd/satlink` and
  the desktop `mobile` build alike.

### mobile

- **No panic crosses the boundary**: gomobile would end the process. Every
  exported function and method that returns an error recovers panics into
  `SatlinkErrorInternal` (`recoverPanic`); the methods without an error
  result that reach the libraries — `SatelliteLink.Status`,
  `IridiumHandset.TakeEvents`, `Cancellation.Cancel` and `Cancelled`, the
  connections' `PeerAddress`, `Resumed`, `ALPNProtocol` and
  `MaxDatagramLen` — recover them and log "recovered a panic in %s: %v" at
  error level (`survivePanic`), returning the empty result; the trivial
  accessors and constructors that reach nothing that could panic (the
  lists' `Len` and `Get`, the settings' adders, `NewCancellation`,
  `NewLinkSettings`, `NewTLSSettings`, `KnownProducts`,
  `IridiumUSBVendorID`, `AsSatlinkError`, `SatlinkError.Error`) recover
  nothing. Every goroutine mobile starts recovers its panics: the link
  listener and the serial port closer log "%s panicked: %v" at error
  level; the log-delivery goroutine recovers a sink's panic per record but
  does not log it, since logging it would hand it to the same sink again.
  `initializedHandset` and `establishOwnLink` defer
  `closeUnlessHandedOn(handset, &flag)`, with the flag set only on
  success, so a serial port the library opened is closed on every other
  way out, a recovered panic included. A panic recovered by an establish
  function once the dial is under way may leave the call up, as in Rust,
  where UniFFI dropped the `ForeignTransport` without touching DTR — though
  a transport's panic inside the core's dial or hang-up pulses DTR first
  (section 3), which ends the call where the DTR line reaches the handset.
  Over a `ByteTransport` the handset's `Close` does nothing, so the docs of
  `EstablishLink` and `SatlinkErrorInternal` tell the app to end the call
  with an `IridiumHandset` made on the still-open transport: its
  initialisation ends a data call left up, and its `HangUp` one held in
  online-command mode. `EstablishLinkWithHandset` strands the lent handset,
  and `Reinitialize` ends the call; `EstablishSerialLink` closes its port,
  which drops DTR.
- **Zero values.** gomobile gives every struct without a constructor of
  its own a public no-argument one that makes a zero value (Java `new
  SatelliteLink()`, Swift `SatlinkMobileSatelliteLink()`). Every method
  copes with a zero receiver: error-returning methods fail with
  `SatlinkErrorInvalidArgument` ("the SatelliteLink was not made by this
  library"), others return empty values (below). `NewLinkSettings`/
  `NewTLSSettings` become the Java constructors, so those zero values never
  appear in Java. `IridiumHandset` and `TLSConnector`, whose constructors
  take arguments, get none: Java has no `new IridiumHandset()`. In
  Objective-C and Swift, gobind gives them only an `init:` that takes the
  constructor's arguments, and they inherit NSObject's `init` and `new`,
  as do the proxy classes `SatlinkMobileByteTransport`,
  `SatlinkMobileLinkListener` and `SatlinkMobileLogSink` (Swift names the
  protocols `…Protocol`). Such an object has a nil `_ref`, and gobind's glue
  raises `NSInternalInconsistencyException` ("go_seq_go_to_refnum on
  objective-c objects is not permitted") on its first use, or when it is
  passed to a function, before any Go code runs, so no `recoverPanic` can
  help. `scripts/build-bindings.sh` therefore runs
  `scripts/mark-unusable-inits.sh` on every `SatlinkMobile.objc.h` in the
  xcframework, which declares `init` and `new` `NS_UNAVAILABLE` after
  `initWithRef:` in each class that declares no argument-less `init`, so
  that clang and Swift refuse them at compile time, as UniFFI's Swift
  classes have no `init()`. The doc comments of those five types, which reach the
  header, and the package doc's Objects section say so.
- **Nil arguments.** A nil settings, transport, handset, connector or sink
  fails with `SatlinkErrorInvalidArgument` "%s must not be nil", naming the
  parameter; a nil listener is allowed.
- **Never nil.** No exported function returns a nil pointer or an empty
  `[]byte` with a nil error: Swift imports such a method as `throws -> T` and
  throws a generic `nilError` for nil, and Java gets null. So:
  `TCPConnection.Receive` and `TLSConnection.Receive` return a `Received`,
  whose `Data` holds at least one byte unless `Finished` is set (the end of
  the stream); `UDPConnection.Receive` returns a `Datagram`, whose `Data` may
  be empty (null) for an empty datagram; `SatelliteLink.HangUp` returns the
  `LinkState` — `LinkStateClosed` with its `Reason` once the call has ended,
  otherwise the state meanwhile (normally `LinkStateClosing`; for a moment
  still `LinkStateUp`, until the driver's next round sees the request); lists
  (`PollEvents`, `ListSMS`, `Resolve`, `DiscoverSerialHandsets`) are
  non-nil, possibly empty. Methods without an error result never return nil
  either: `Status` and `TakeEvents` return empty objects for a zero receiver
  (`&LinkStatus{State: &LinkState{}, DNSServers: &StringList{}}`,
  `&StringList{}`), and fields of those types are never nil, except those
  documented as optional (`LinkState.Reason`). `AsSatlinkError` and the lists'
  `Get` return nil by design (Swift sees an optional).
- **Error mapping** (Rust `From` impls), in this order, as satnet's errors
  are also standard ones: `errors.As` a `*SatlinkError` (pass it through);
  then `*satnet.Error`; then `*satlink.Error`; then `*satnet.ParseRuleError`;
  and only then `errors.Is(err, net.ErrClosed)` → InvalidArgument "the
  connection is closed"; any other error, which the libraries never return,
  becomes `SatlinkErrorInternal` with its text. `satlink` kinds map one to
  one (`ErrBusy` → `SatlinkErrorHandsetBusy`; Reason/Result is the core
  error's `Message`, followed by ": " and its cause when it has one);
  `satnet` kinds map to the FFI's (`ErrInvalidConfig` → InvalidArgument,
  `ErrTimedOut` → Timeout with `Command` = operation, `ErrConnectionFailed` →
  `Address` = `Addr.String()`, `ErrLinkDown` → `Reason` = `Reason.String()`,
  `ErrHandset` → the wrapped core error); `*satnet.ParseRuleError` and invalid
  host addresses → InvalidArgument (a rule that does not parse reaches the
  app through the same table, as `config` maps `ParsePolicy`'s
  `*satnet.ParseRuleError` with `fromError`; a host address that is not
  IPv4 has the Rust bindings' reason, `AddrParseError`'s "invalid IPv4
  address syntax", which names no address, so `hostAddresses` checks the
  hosts in map order, as Rust's `into_iter` did); a `HangUp` whose call may
  still be up →
  `SatlinkErrorHangUpFailed` with the close reason's text. The establishing
  functions check `errors.Is(err, satlink.ErrCallMayStillBeUp)` before the
  mapping, as the FFI's `establish_error` does (`link.rs:413-423`): such a
  failure is `SatlinkErrorHangUpFailed` whose Reason is the error's text.
  `satlink.ErrCallMayStillBeUp` maps to `SatlinkErrorHangUpFailed` with the
  core error's `Message`, and `satlink.ErrCancelled`, also inside satnet's
  `ErrHandset`, to `SatlinkErrorCancelled`, "the call setup was cancelled"
  (Rust's `From<core::Error>`, `lib.rs:124-139`); `SatlinkErrorCancelled` is
  appended after `SatlinkErrorInternal`, so that no constant changes value.
  The general mapping ignores the flag on errors of other kinds — Rust's
  `CallFailure`, which those `From` impls never see.
  `ListSMS` passes the filter to the core, whose "%d is not an SMS filter"
  maps through.
- **Durations.** Every `timeoutMillis` and the settings' seconds are `int64`,
  converted to `time.Duration` with saturation (section 2, `convert.go`), a
  timeout of zero being a deadline of now.
- **Ports.** `OpenTCP`, `OpenTLS` and `OpenUDP` refuse only ports outside
  0–65535 ("port must be between 0 and 65535"); port 0 goes on to satnet,
  whose policy refuses it as `PolicyDenied` ("echo.test:0 is not allowed by
  the link settings"), as the Rust `u16` did.
- **Foreign transport adapter**: timeouts in milliseconds clamped to [1,
  2147483647]; at most `len(p)` bytes copied of what `Read` returns, and
  `Write` never called with empty data. A host error that is a
  `SatlinkError` of kind Transport gives its Reason (Rust's `to_core`), a
  `*satlink.Error` of kind `ErrTransport` from a Go transport passes
  unchanged, and anything else becomes `ErrTransport` with `err.Error()`. It
  asserts `satlink.Transport` and `satlink.DTRController` at compile time.
- **TLS settings.** `NewTLSConnector` reports problems in the Rust
  bindings' order: a PEM bundle that does not parse, then a pin that is not
  32 bytes ("a SHA-256 pin is 32 bytes"), then settings that trust nothing.
  satnet checks the first and the last in one call, so the pins are checked
  in between.
- **Lending** (`EstablishLinkWithHandset`): the IridiumHandset's
  `*satlink.Handset` goes to `satnet.Establish`; Busy and the release come
  from the core. `lend` returns the setup's `handsetLoan`, through which
  `EstablishLinkWithHandset` settles where the handset goes exactly once,
  as Rust's `establish_with_handset` does (`link.rs:296-317`): with the
  loan's `returnedAfter` when the link did not come up, or its `lentTo`
  once it is up, each of which marks the loan settled. The deferred
  `loan.abandonUnlessSettled()` runs `abandonDialling` only when neither
  ran, which only a panic leaves so: it strands the handset, and closes it
  if the app closed the IridiumHandset. A setup that settled leaves the
  slot alone, because by then the handset may be another thread's setup's
  (an unconditional deferred `abandonDialling` stranded that setup's
  handset, with a false `SatlinkErrorHangUpFailed`, and closed its port
  under it if the app had closed the IridiumHandset meanwhile);
  `handBackFromDialling` still takes back only a handset being lent.
  `Close` records `closed` under the slot lock (`recordClose`): the
  IridiumHandset then refuses every method but `Close` with "the handset is
  closed", wherever its handset is. A `Close` while a link has the handset
  starts the port closer; while the link is being set up, `lentTo` starts
  it or `endDialling` closes the handset. The methods take `h.mu`, defer
  the unlock, then call `reclaim`, so that a fault in `reclaim` leaves the
  lock free. Closing a lent link does not close the handset. Closing a
  lent `IridiumHandset`, or one whose link is being set up, does not close
  its port either: the link hangs up through it, so the port closes once the
  call has ended, or at once if the link does not come up (Rust: the link
  owned the handset until then); `Close` takes only a short-held slot lock
  and never waits for a command. Only serial links need their own handset
  closed, as closing a handset over a `ByteTransport` does nothing:
  `EstablishSerialLink` starts a goroutine (`satlink-port-closer`, polling
  `Watcher.WaitClosed` every `listenerPoll`) that closes the port once the
  call has ended, however it ends; it holds neither the `Link` nor the
  `SatelliteLink`, so a dropped link still hangs up and has its port closed.
  `NewLinkSettings` takes its values from `satnet.NewConfig(satnet.DenyAll())`,
  number and credentials included, as Rust's `default_link_settings` takes
  them from `LinkConfig::new`.
- **The stranded handset** (FFI `Slot::Stranded`, `lib.rs:297-367, 380-393,
  440-460, 586-594`): a lent link that fails to come up with
  `ErrCallMayStillBeUp`, or that closes with `Status().CallMayStillBeUp`,
  strands its IridiumHandset; so does a panic inside `satnet.Establish`,
  which only the driver goroutine recovers and which reaches
  `EstablishLinkWithHandset` as `SatlinkErrorInternal` (Go only: whether a
  call is up is unknown); and so does an `IridiumHandset` method whose
  handset error is of kind `ErrCallMayStillBeUp` (`perform`, Rust's
  `with`), such as `Answer` taking a data call it cannot end — the check is
  on the kind (`isCallMayStillBeUp`), and `strand` takes only a ready
  handset. Every method but `Reinitialize` and `Close` then fails with
  `SatlinkErrorHangUpFailed`, Reason "the last data call could not be hung
  up; reinitialize ends it", and `TakeEvents` returns an empty list.
  `Reinitialize` on a ready handset runs `Initialize`, and an error of that
  kind strands it. On a stranded one it runs `endStrandedCall`, Rust's
  `recover`: `Initialize`, then `HangUp`, as a call held in online-command
  mode shows itself only to firmware with +CPAS, and ATH answers OK when
  there is none. Every failure of it is of kind `ErrCallMayStillBeUp`
  (`asCallMayStillBeUp`): an `Initialize` error of that kind as it is, any
  other with its text as `Message`, an ATH failure as "ATH failed: %v"; the
  app hears `SatlinkErrorHangUpFailed`, and the handset stays stranded,
  until a `Reinitialize` succeeds and makes it ready. While a link has the
  handset, `Reinitialize` fails with `SatlinkErrorHandsetBusy` too. The
  IridiumHandset learns how a lent link ended as Rust's `reclaim` does,
  lazily at its next method call: through the link's `Watcher().Status()`
  (state `LinkStateClosed`, then `CallMayStillBeUp`). Go has no lost
  handset (`Slot::Lost`; section 3).
- **Cancellation** (`link.rs:105-130`): `mobile.Cancellation` holds a
  `satlink.Cancellation` by value, so that its zero value, which gomobile's
  constructor makes, is usable too; `NewCancellation` is the constructor
  (Java `new Cancellation()`), and `Cancel` and `Cancelled` (Rust's
  `is_cancelled`, named as `Resumed` is for `is_resumed`) recover panics with
  `survivePanic`. The establish functions take it as a last, nil-able
  parameter, as gomobile has no default arguments, and
  `LinkSettings.config(cancellation)` points `config.Call.Cancellation` at
  it; without one, the configuration keeps the fresh one `satnet.NewConfig`
  made, which nobody else can reach. satnet aborts the dial, or hangs up a
  call that came up while PPP negotiates; the establish function then fails
  with `SatlinkErrorCancelled`, or with `SatlinkErrorHangUpFailed` if that
  hang-up failed.
- **Listener**: a goroutine (`satlink-link-listener`) polls
  `Watcher.NextStateChange` every second and stops after delivering
  `LinkStateClosed`; an error the listener returns is ignored on purpose
  (the link carries on, and so do the notifications), a panic logged. The
  goroutine holds the watcher alone, never the `Link` or the
  `SatelliteLink`, so a dropped link still hangs up. The
  Rust bindings start the listener's thread before dialling, so that
  failing to start it costs no airtime (`spawn_listener`,
  `link.rs:431-462`); Go starts the goroutine once the link is up, as
  starting one cannot fail.
- **SendSome and Abort** (`link.rs:473-488, 511-515, 595-603, 630-634`):
  `SendSome` sets the write deadline and calls satnet's `WriteSome`, Rust's
  `send_until`, once: it queues what fits — waiting for room until the
  deadline, but no longer than it takes some to fit — and returns how much
  it queued as an `int64`; `SatlinkErrorTimeout` means that nothing was
  queued, and any other failure is the error. `Abort` is
  `TCPConn.Abort`/`TLSConn.Abort`; after `Close` it fails with
  InvalidArgument "the connection is closed", where Rust's `abort` returns
  nothing.
- **Receive**: buffer `min(maxLen, 64 KiB)`; at the end of the stream
  `&Received{Finished: true}`. A stream refuses a `maxLen` below 1
  (`streamBuffer`), and UDP only a negative one (`datagramBuffer`), where 0
  takes a datagram and keeps none of it. The three `Receive` docs state the
  cap, as Rust's receive docs do (`link.rs:490`, `605`, `665`), and that a
  timeout of zero takes only what has already arrived, as Rust's TCP and
  TLS ones do; the streams' add that `maxLen` must be at least 1. So all of
  it reaches the generated Java and Objective-C.
- **Settings**: `LinkSettings` and `TLSSettings` guard their lists with a
  mutex. `config()` reads `entries()` copies, and `NewTLSConnector` works on
  one `copied()` snapshot, so its Rust-ordered checks (PEM, then pins, then
  trusting nothing) all see the same settings. Their exported fields,
  which gomobile's setters write with no copy at the boundary, must not be
  set while another thread hands them to a call (section 3).
- **Equality**: gobind's `equals` and `hashCode` compare exported fields
  only (section 3); the package doc's Equality section tells apps to
  compare objects with `===` and keep them in identity-based collections,
  and `bindings_test.go` pins which fields each `equals` compares.
- **TLS connection.** `ShutdownWrite(timeoutMillis)` is the FFI's
  `shutdown_write(timeout_ms)`: write deadline now+timeout, then satnet's
  `TLSConn.CloseWrite`. `Close` releases the connection, as UniFFI's destroy
  did, without waiting: write deadline now, then `TLSConn.Close` — the
  records still pending and the close_notify as far as the send buffer has
  room at once, then the graceful TCP close — ignoring a close_notify that
  did not fit, which is expected (Rust's drop sent none). `Send` documents
  what a timed-out send leaves (`link.rs:585-590`): a timeout does not say
  how much was taken, and what was leaves with the next call on the
  connection, so `WaitAcknowledged` before `HangUp`, or `Abort`, or
  `SendSome`. `ShutdownWrite` documents that sending fails from then on,
  even after a timeout, and that calling it again finishes it
  (`link.rs:622-628`).
- **SetLogSink**: an unexported bridge holds the forwarding — `logtext`'s
  handler with the level filter, whose `emit` does a non-blocking send of
  (level, pkg, text) into a channel of 256 (dropping when full), and a
  goroutine (`satlink-log`) that delivers to the sink. Internal tests build
  one bridge per test without touching `slog.Default`. `SetLogSink`
  validates its arguments, then installs one bridge with `slog.SetDefault`,
  guarded so that it runs once per process; another call fails ("a log sink
  is already set").
- Desktop-only code stays in `serial.go`, behind `//go:build !android && (!ios
  || (macos && !maccatalyst))`: for gomobile's macOS target, gobind reads
  the package with `GOOS=ios` and the `macos` tag, and gomobile compiles it
  with `GOOS=darwin` and the `macos` tag, against the macOS SDK, where the
  serial library compiles with cgo; iOS and Mac Catalyst (`macos` and
  `maccatalyst` tags) leave it out.
- **Bindings check.** `go tool gobind` cannot take another `GOOS` (it would
  cross-compile gobind itself), so `bindings_test.go` builds the pinned
  binary as the scripts do and runs it per target: `GOOS=android
  "$tools/gobind" -lang=go,java -javapkg=io.github.anhydrous99.satlink
  -outdir <dir> ./mobile`, `GOOS=ios "$tools/gobind" -lang=go,objc
  -prefix=Satlink -tags=ios -outdir <dir> ./mobile` (the serial API absent),
  the same with `-tags=macos,maccatalyst` (absent) and with `-tags=macos`
  (present; with cgo enabled and the macOS SDK's clang as `CC`). None may
  report a skipped symbol. gobind type-checks without running cgo —
  `GOOS=ios -tags=macos` succeeds even with `CGO_ENABLED=0` — so that does
  not prove that the serial library builds: the test also compiles the
  package per target (`GOOS=android GOARCH=arm CGO_ENABLED=0`; `GOOS=ios
  GOARCH=arm64 CGO_ENABLED=0 -tags ios`; `GOOS=ios GOARCH=arm64
  CGO_ENABLED=1 CC=$(xcrun --sdk macosx --find clang)` with
  `CGO_CFLAGS`/`CGO_LDFLAGS=-isysroot <SDK>` and `-tags macos`, which fails
  without cgo: `undefined: nativeGetDetailedPortsList`). It also checks the
  Java declarations the Kotlin sources use, and compiles the generated Java
  at Java 8, gomobile's level, when a JDK is found (Homebrew's OpenJDK 21 and
  27 still accept it; macOS's `/usr/bin/javac` is only a stub).
  `unusableinits_test.go` runs `scripts/mark-unusable-inits.sh` on the macOS
  header gobind writes. It requires `init` to be unavailable in exactly
  `SatlinkMobileByteTransport`, `SatlinkMobileIridiumHandset`,
  `SatlinkMobileLinkListener`, `SatlinkMobileLogSink` and
  `SatlinkMobileTLSConnector`, and type-checks Objective-C against the
  header with the macOS SDK's clang (`-fsyntax-only -fobjc-arc -fmodules`):
  the documented construction compiles, and `init` and `new` of those
  classes are refused as unavailable. It also checks that the script leaves
  a header it already marked unchanged, and refuses a header of another
  shape (no gobind class, or a class without `initWithRef:`), leaving it as
  it was.

### cmd/satlink

Everything the Rust CLI uses exists: `serial.Discover`/`ChoosePort`/`Open`/
`DefaultBaud`; `sim.New`, `DefaultConfig`, `IridiumLinkProfile`,
`InstantLinkProfile`; the whole `Handset` API (`CommandTimeout` for `at`,
`PollURCs` for `monitor`, `TakeURCs` for the trailing "(event)" lines);
`satnet.NewConfig`, `Config.Validate`, `ParsePolicy`,
`Policy.AllowsLookup`/`AllowsHost`, `Establish` with
`config.Call.Cancellation`, `Status`, `Watcher().Status()` (`Usage`,
`CallMayStillBeUp`), `Resolve`, `Dial`, `DialTLS`,
`NewTLSConnector(satnet.TLSOptions{})`,
`TCPConn.SetDeadline`/`SetReadDeadline`/`SetWriteDeadline`/`CloseWrite`/`Abort`/`WaitAcknowledged`/`PeerAddr`,
`TLSConn.SetDeadline`, `Link.RequestClose` and `Link.Close`, and `errors.Is`
with `satlink.ErrCallMayStillBeUp`, `satnet.ErrTLSTruncated` and
`satnet.ErrTimedOut`.
`{:?}` of the battery and registration statuses is their `String()`.

- **Structure for tests.** `func run(ctx context.Context, env environment)
  int`, where `environment` groups the arguments, standard input, output and
  error, whether standard output and error are terminals
  (`stdoutIsTerminal`, `stderrIsTerminal`), and standard input
  (`stdinIsTerminal`), `lookupEnv func(string) (string, bool)` (a variable
  set to "" is set, as for clap), the function that installs the log
  handler (`slog.SetDefault` in `main`; a no-op or a recorder in tests), the
  functions that discover handsets and make simulated ones, and
  `notifyInterruptions` and `quitInterrupted`, which tests replace to relay
  their own interruptions. `run` builds a fresh command tree on each call;
  `main` calls `os.Exit(run(…))`, but for an interrupted run
  (`exitInterrupted`), which ends by SIGINT (below). Tests never swap `slog.Default`, never read the
  real environment, and run commands in-process. A command given no
  `--port` chooses among the handsets the environment discovers, as
  `serial.ChoosePort` would (`portPath`, through `serial.ChoosePortFrom`),
  so that the tests can give it none, one or several. `main` first calls
  `ignoreBrokenPipes`: on Unix `signal.Ignore(syscall.SIGPIPE)`, as Rust's
  runtime set SIGPIPE to `SIG_IGN` before `main`. A write to a pipe nobody
  reads then fails with EPIPE and the command fails once it has hung up,
  where Go's runtime would end the process by SIGPIPE in the middle of the
  call and skip the hang-up; Go's runtime replaces a SIGPIPE ignored on
  entry, so the CLI must ask, and satlink starts no programs that would
  inherit it. `processEnvironment` calls `environmentOf` with the
  process's arguments, standard files and `os.LookupEnv`; `environmentOf`
  hands the streams over as `processStream` and says which are terminals,
  so that tests can give it a pseudo-terminal, the null device and a
  lookup of their own. A `processStream`'s errors with an OS error number
  read as Rust's `io::Error` ("Broken pipe (os error 32)", "Is a directory
  (os error 21)"), where Go wrote "write /dev/stdout: broken pipe"; and, as
  Rust's standard library does (`handle_ebadf`, through the platform's
  `is_ebadf`), `badDescriptor` reads as the end of the stream and takes
  every write, so that such a command exits 0, its output dropped, as the
  Rust CLI did. On Unix it is EBADF: a standard output opened read-only, or
  a standard input opened write-only (`processstreams_unix.go`,
  `TestAStreamNotOpenForTheDirectionIsAnEmptyInputAndASink`). On Windows it
  is ERROR_INVALID_HANDLE: a NULL standard handle, which a process without
  a console, or one started by such a process, has for a stream it was not
  handed, and which Go keeps from GetStdHandle (`processstreams_windows.go`,
  `TestAStreamWithoutAHandleIsAnEmptyInputAndASink`, run only on Windows).
  There Go's `syscall.EBADF` is a number Go made up, which no call returns,
  and package syscall does not name ERROR_INVALID_HANDLE, so the Windows
  file declares it as `syscall.Errno(6)` rather than import
  `golang.org/x/sys/windows`, which is no approved dependency (it is in
  `go.mod` only for the serial library).
  ERROR_ACCESS_DENIED, a Windows standard output opened read-only, stays a
  failure, as in Rust. `main` also clears
  `cobra.MousetrapHelpText`: otherwise cobra on Windows refuses a command
  started from Explorer, such as a shortcut with arguments, where Rust ran
  it.
- **The command line is clap's.** clap's parsing, help and errors differ
  from cobra's and pflag's in what users see, so the command line is read by
  `parseCommandLine` (`commandline.go`) against the command tree
  (`command.go`; the arguments as clap declared them in `arguments.go` and
  `values.go`). Help is rendered by `help.go`, problems by `usage.go` with
  `usageline.go` and `suggestions.go`, and styles by `palette.go`; cobra
  runs the command named (`RunE`, the root's `PersistentPreRun` that installs
  the logger, the context). `parseCommandLine` returns the command named, or
  a `*commandLineExit`: help or the version (status 0, standard output), or
  the problem (status 2, standard error). `commandLineExit` is not an error
  type: every parser and explainer function returns one or nil, so the
  compiler sees that nothing else comes out of reading a command line, and
  `run` hands the exit to `endBeforeRunning`, which needs no fallback. The
  rules kept:
  - each command takes the tokens up to its subcommand's name;
  - global options (`-p`, `-b`, `--simulate`, `-v`) may come at any level,
    and the deepest level that gives one wins outright (counts add up only
    within a level);
  - a token starting with "-" is an option, so a value that does must follow
    "=" or come after "--"; an option waiting for a value takes the next
    token even if it names a subcommand; `--simulate` takes a value only
    after "=" (`--simulate instant` makes "instant" an unknown command);
  - options may be given once per level, except `--allow` and `-v`;
  - `-h` shows the summary and `--help` the long help; `-V` exists on the
    root only; all three act as soon as the value pending at their own level
    is checked (`-b x -h` refuses "x"); every group has the `help`
    subcommand;
  - a group given nothing shows its summary on standard error with status
    2; given options but no subcommand, it fails with "requires a
    subcommand";
  - an environment variable set to "" is set (`SATLINK_PORT=` opens the
    empty path), and environment variables count as given in the checks
    after reading;
  - a value attached to its option (`--x=v`, `-xv`, `--simulate`) is
    checked at once; a value in a token of its own — an option's that
    follows it, or a positional argument's — is checked once the command
    line moves on: at the command's next argument, at the end of its
    tokens, or once its subcommand has been read in full. So help (`-b x
    info -h`, `-b x help`) and the subcommand's own problems come first.
    The value is still stored before the subcommand's, so the deepest
    command to give a global option wins (`--simulate=instant -b x info -b
    y` reports "y"). A token that is itself a problem — an unknown option
    or command, a value too many, a value given to a flag — is reported
    instead of the value (`--baud x --frob`). The value still counts as
    given in the usage line, except an option given twice, which is taken
    out together with its earlier occurrence; the `help` subcommand never
    checks it;
  - the values themselves: decimal digits after an optional "+", as Rust
    reads an unsigned integer ("010" is ten, "0x10" no number); limits as
    `u64` with `range(1..)`; `u16`/`u32` read as `i64` and then
    range-checked ("-1 is not in 0..=65535"); possible values with Jaro
    suggestions above 0.7;
  - text that is not UTF-8 is refused as clap's value parsers refused it,
    "invalid UTF-8 was detected in one or more arguments" with the
    command's usage line, but "invalid value" for possible values; tokens
    and variables are shown lossily (U+FFFD) in errors and help;
  - an environment variable is checked by every command that takes its
    option without being given it, deepest first, before that command's
    own checks;
  - the usage line of a value given to a flag (`--help=x`) names the
    arguments a command declares as clap's derive group, `<a|b|...>`, once
    one of them was given; a counted flag goes last among the arguments
    given, at every occurrence;
  - help and errors are built in clap's styles whichever stream they go to,
    as clap's `StyledStr` always carried them (`palette{styled: true}`; the
    zero palette, clap's `Styles::plain`, is for tests). `endBeforeRunning`
    writes them unchanged to a stream that shows styles, by anstream's
    rules for `NO_COLOR`, `CLICOLOR_FORCE`, `CLICOLOR`, `TERM` and `CI`
    (`showsStyles`), and to one that shows none as anstream's `StripStream`
    did (`stripStyles`, `strip.go`): without the sequences that ESC starts,
    whether clap's styles or ones a token or variable brought in (CSI, OSC,
    DCS, SOS, PM and APC alike), and without the C0 control characters and
    DEL other than tab, line feed, form feed and carriage return. A C1
    control, two bytes in UTF-8 (U+009B, CSI, and U+0085, NEL, among them),
    stays with what follows it, as anstream kept every UTF-8 character
    whole. Like anstream it strips the styled text: an unfinished sequence
    in a token ends at clap's own style, so `$'\e['` gives ''. It judges
    each kept run from the ground state: `$'\e[\n1m'` keeps '\n1m', where
    a terminal would take the 1 and the m into the CSI. A kept byte leaves
    the parser where it was, so that `$'\e[\n\x01mz'` gives '\nz', the m
    ending the CSI; only a kept character of more than one byte returns it
    to the ground: `$'\e[\né\x01mz'` gives '\némz'. The rule table leaves
    out what stripping cannot tell apart, which
    `TestTheParsersRulesAreAnstyleParsesAsStrippingSeesThem` checks for
    every state and byte against anstyle-parse's `codegen.rs`. In a summary, a line break in a
    variable's value starts the next line at the help's column, as clap
    indented an argument's help; long help indents it by ten spaces;
  - exit status 0 on success, 1 for a failed command, 2 for a command line
    that does not parse, and 101 (`exitPanicked`) for a panic that reaches
    `run`, the Rust runtime's status for a panic that leaves `main`.
- **net, before dialling** (`net.rs:103-218`). `resolve` of an IPv4 literal
  (`netip.ParseAddr` and `Is4`) prints it and dials nothing. Building the
  link configuration refuses a target the link would refuse anyway — a
  rule rather than a host, such as `*.example.com` or `10.0.0.0/8` — with
  `%q is not a host name or IPv4 address`: `resolve` needs
  `Policy.AllowsLookup(host)` or an IPv4 literal, `get` and `tcp`
  `Policy.AllowsHost(host, port)`. `netArguments.linkConfig` ends with
  `config.Validate()` (`net.rs:164-180`), so that limits too small to be of
  use are refused, with satnet's texts, before "Dialling" is announced. A URL
  containing a character for which `unicode.IsSpace` or `unicode.IsControl`
  holds (Rust's `is_whitespace`, `is_control`) would break the request line:
  it is refused as the command line is read, with `%q is not a valid
  http:// or https:// URL`. The argument parsing and the link configuration
  are functions of their own, so that the Rust tests that parse `net …`
  arguments can call them without dialling.
- **net, interruptions** (`net.rs:234-328`, `interrupt*.go`). From the dial
  on — after `linkConfig`, before "Dialling" — a net call catches Ctrl-C,
  SIGTERM and SIGHUP (`os.Interrupt` and SIGTERM on Windows) through
  `os/signal`, relayed to the goroutine that reacts through a channel of
  `signalBacklog` (1); a SIGHUP or SIGINT ignored at start
  (`signal.Ignored`) stays ignored, as under `nohup` or in a background job,
  since `signal.Notify` would stop ignoring it. The first interruption sets
  the stop request and ends a context, on which `context.AfterFunc` cancels
  `config.Call.Cancellation` during `establish` and calls
  `link.RequestClose` during the subcommand; it then writes "Interrupted:
  hanging up once the call allows (again to quit at once)…" best effort,
  and the time taken after that message starts `repeatsIgnoredFor` (2 s):
  a repeat within it is part of the same request, and a later one warns
  "warning: the hang-up was not confirmed (quitting at once), so the call
  may still be up and billing. End it with `satlink hangup`, or power-cycle
  the handset." and calls `quitInterrupted`, which leaves the call to the
  DTR drop. Interrupted during the setup, `Establish` fails with satnet's
  error for the cancellation, "handset: the call setup was cancelled";
  once the link is up, an `AfterFunc` that ran makes the outcome
  `errInterrupted` ("interrupted"). `run`'s
  finish flushes the log, writes "error: …", stops catching interruptions
  and returns `exitInterrupted` (130) for a run an interruption asked to
  stop, whatever its outcome; `main` then calls `dieInterrupted`: on Unix
  `signal.Reset(SIGINT)`, `kill(getpid(), SIGINT)`, then exit status 130
  after `sigintDeliveryGrace` (1 s) if that did nothing (SIGINT blocked, or
  ignored at start), so that a shell loop around the CLI stops too; on
  Windows exit status 130.
- **net, the call** (`net.rs:112-161, 330-357`). `establish` writes
  "Dialling …" to standard error, and a failed write fails the command; a
  failed `Establish` that matches `satlink.ErrCallMayStillBeUp` prints the
  hang-up warning, without traffic details, before the error. Once the link
  is up, the command — the link's details, then the subcommand — runs in a
  function that recovers a panic as the error "internal error: the command
  panicked", so that nothing skips `link.Close()`. Then the call summary,
  from `Watcher().Status()`: "Call lasted %d s: %d bytes sent, %d
  received." or, with `CallMayStillBeUp`, "warning: the hang-up was not
  confirmed (%d bytes sent, %d received), so the call may still be up and
  billing. End it with `satlink hangup`, or power-cycle the handset." (the
  binary's name, section 2). The result is the command's outcome when the
  hang-up succeeded, and otherwise the hang-up's failure, after "error:
  <outcome>" when the command failed too.
- **Best-effort writes.** Writes to standard output and error fail the
  command, with exit status 1 (the Rust handset commands' `println!`
  panicked instead, section 3), except those Rust marks as best effort: the
  call summary, that second "error: …", the "Interrupted…" message and the
  warnings, `run`'s final "error: …", and log lines — a log line must never
  stop the link's driver before it has hung up. A command stops at its
  first failed write, as Rust's `println!` panicked at the first line it
  could not write: `status` asks for the battery and the registration only
  once the lines before them are written, so `printStatus` writes each line
  with `writef`, as a printer still evaluates its arguments after a failure
  (`TestAStatusAsksTheHandsetNothingMoreOnceALineCannotBeWritten`). With SIGPIPE ignored this
  holds for the binary too: a log line to a standard error nobody reads is
  dropped, and the relay and the hang-up go on. Each is `_, _ = fmt.Fprintf(…)` with a
  comment saying why. The relay's sender no longer writes its errors: it
  hands them to the relay through a channel of one, which it closes — with
  no outcome at all if it panicked; its `CloseWrite` and `Abort` errors are
  ignored, with a comment, as they fail only after `Close`, which comes
  only once the sender too is done with the connection
  (`sharedConnection`), and Rust's could not fail. A recovered panic prints "panic: <value>" and the
  goroutine's stack to standard error, the Go counterpart of Rust's panic
  hook: `reportPanic` (in `run.go`) writes it for three recoveries — the
  subcommand's work (`outcomeOf`), the relay's sender, and `run`. `run`
  recovers any other panic on its goroutine once the deferred closes have
  run; `withHandset` defers the handset's close from the moment the handset
  exists, so that a panic inside `Initialize` closes it too, as Rust's
  unwinding dropped it, and `panicked` then stops catching interruptions
  and returns `exitPanicked` whatever an interruption asked for, as Rust's
  panic left `main` before `end_if_interrupted`. The relay's sender
  goroutine recovers its own panic, so that, like the Rust thread, it ends
  alone instead of ending the process before the hang-up; it writes to an
  `outgoingStream` (`Write`, `SetWriteDeadline`, `CloseWrite`, `Abort`),
  which the tests replace with a recording fake. Interruptions: an
  `interruptionHandler`, built by the one constructor that `catch` and the
  tests share, does what each interruption calls for, and `interruptions`
  relays them to it.
- **net get** (`net.rs:384-605`). One deadline for the request and its
  whole response, set once connected (`exchange`):
  `conn.SetDeadline(time.Now().Add(transferTimeout))`,
  for TCP and TLS alike (`transferTimeout`, 600 s: "time allowed for an
  HTTP request and its whole response, once connected"). The request is
  "GET <path> HTTP/1.0\r\nHost: <authority>\r\nUser-Agent:
  satlink-cli\r\nConnection: close\r\n\r\n", the authority naming the port
  unless it is the scheme's default (RFC 9112 §3.2). `readResponse` keeps
  what arrives and ends at the announced end (`endingComplete`, as a server
  that keeps the connection open regardless would hold the call until the
  idle limit), at `io.EOF` (`endingClosed`), at `ErrTLSTruncated`
  (`endingTruncated`), at `ErrTimedOut` (`endingTimedOut`), or at any other
  error, which it keeps (`endingFailed`: reset, call dropped, a limit
  reached). Then `link.RequestClose`, so that a slow reader such as a pager
  does not keep the call up, and `printHTTPResponse`: the final response,
  past any interim (1xx) responses (`finalResponse`, RFC 9110 §15.2), whose
  head, read as Rust's `String::from_utf8_lossy` reads it
  (`internal/utf8lossy`), goes to standard error, and whose body goes to
  standard output, written even when incomplete or when the call failed,
  like curl. A head that never ended fails with "the server closed the
  connection without replying", "the response ended after %d bytes, before
  its headers did", the timeout's text or the failure; a status line other
  than `HTTP/… ` and three digits with "the reply is not an HTTP response".
  The expected body length is 0 for 204 and 304; framing the client cannot
  check is an error before the body is printed — any Transfer-Encoding
  ("the reply uses Transfer-Encoding %q, which HTTP/1.0 does not allow"),
  or a Content-Length that is not just digits ("the Content-Length %q is
  not a length"), whose field lines are joined by ", " (RFC 9110 §5.3) and
  parsed with `strconv.ParseUint` into a `uint64`, Rust's `usize` on 64-bit
  platforms; field names are compared as ASCII (`asciicase.EqualFold`), past
  the status line (`header`). `announcedEnd` is the head's length plus the
  body's, unless that overflows. Then "the body has %d bytes, more than the
  %d the response announced", "the response did not arrive in full within
  600 s", the failure, "the response ended after %d of %d bytes", or,
  without a length, "the server closed the connection without ending the
  TLS session, so the response may be cut short".
- **URLs** (`net.rs:689-777`). The fragment stays behind (RFC 3986 §3.5),
  the authority ends at the first '/' or '?', the port follows the last ':',
  an empty host is refused, and a path without a leading '/' gets one
  (origin form, RFC 9112 §3.2.1): "http://example.com?q=1#top" requests
  "/?q=1".
- **net tcp** (`net.rs:607-689`). Standard input goes out from a goroutine
  of its own, as reading it blocks: each chunk is written with
  `SetWriteDeadline(time.Now().Add(sendTimeout))` (120 s, Rust's
  `SEND_TIMEOUT`); the sender then reports its outcome, and closes our half
  of the connection after success (`CloseWrite`) or resets it after a
  failure to read or send standard input (`Abort`), so that the far end
  cannot take a part for the whole. The relay reads with
  `SetReadDeadline(now + relayCheckInterval)` (1 s); on a timeout it checks
  the sender's outcome — a sending failure says why the stream ended, and
  any other error ends the relay. Once the remote end has closed, it waits
  `stdinEndGrace` (2 s) for standard input to end: all of it sent, it waits
  for `WaitAcknowledged` within `deliveryGrace` (30 s); sending failed, that
  error; still sending, nothing at a terminal (`stdinIsTerminal`), and
  otherwise `errRemoteClosedFirst`. Failures are "not all of standard input
  was sent: %w", with exit status 1. The connection closes, gracefully,
  once the relay and the sender are both done with it, as Rust's
  `TcpStream` closed when the last `Arc` holding it was dropped
  (`sharedConnection`): the relay's as it returns, the sender's once
  standard input has ended and the stream with it; neither closes it under
  the other, whose calls would fail with `net.ErrClosed`. A relay that
  cannot write "Connected to …" returns before the sender starts and
  closes the connection at once, as the Rust relay's `?` on that
  `writeln!` dropped the only `Arc` (the subtest "the relay alone").
- **Saturating conversions.** `--idle-secs`, `--max-minutes` and `at
  --timeout` convert to `time.Duration` saturating at `math.MaxInt64`, as Rust
  saturates `max_minutes.saturating_mul(60)`; `--max-kb` multiplies by 1024
  saturating. An absurd value then fails "limits must be at most a year"
  instead of overflowing into "limits must not be negative".
- **Logging** goes through `internal/logtext`'s handler on standard error
  (section 2), printing `fmt.Sprintf("[%-5s %s] %s\n", logtext.LevelName(level),
  pkg, text)` best effort, from a goroutine of its own (`logger.rs`): the
  `stderrLog` of section 1, whose backlog keeps a standard error that blocks
  — a pipe to a pager nobody reads — from stopping the link's driver while
  it hangs up and enforces the limits. `flush` gives the lines handed over
  at most a second before `run`'s final "error: …". The CLI's own lines on
  standard error (`writerBehind`) wait for the log lines handed over before
  them, so that the order is the causal one (section 3); this wait has no
  time limit, as standard error would block those writes anyway, but it
  ends once the log is closed.
- **Tests.** `commands_test.go` runs each command in-process — over the
  simulator (`--simulate=instant`, and the Iridium profile where timing
  shows), fake handsets and fake USB ports — against the golden files of
  `testdata/commands`, including the hang-up warning
  (`net-hang-up-unconfirmed`), given at once by a `simtest.FaultyTransport`
  with `PanicsOnEscape` (a transport deaf to the escape and to DTR would take
  about 20 s with the CLI's default 1.5 s escape guard; the summary text
  itself is unit-tested in `netcall_test.go`); `interface_test.go` compares
  help, usage errors and styles with `testdata/help`, `testdata/usage` and
  `testdata/style`; `logger_test.go` the line format and the `-v`
  thresholds and the backlog (logtext's own tests cover the rendering);
  `netinprocess_test.go` and `net_integration_test.go` the Rust tests of
  `tests/net.rs` (section 1); the unit tests beside each file the rest. "the
  reply is not an HTTP response" cannot be reached through the simulator and
  is covered by unit tests. `interface_test.go` also compares the
  pending-value orders and the grouped usage lines with `testdata/usage` and
  `testdata/style/unneeded-value-after-a-global-option`. A successful
  `https://` fetch runs in no test, as in Rust: the simulator offers only
  its TLS echo on port 443 (Rust's `SECURE_ECHO_PORT`, which satnet's,
  sim's and mobile's TLS tests use), and a CLI made to trust the
  simulator's test CA would not test the public roots. Past the handshake
  the fetch takes the http path the goldens run, over a `TLSConn`, whose
  deadline, reads up to close_notify or truncation, and `Close` satnet's
  TLS tests cover; `endingOf(ErrTLSTruncated)` is unit-tested, and
  `TestAnHTTPSFetchTrustsThePublicWebPKI` covers the trust. Only
  `connect`'s `return conn, nil` after `DialTLS` runs in no test; a Go-only
  HTTPS service in the simulator would need a port of its own and a
  section 3 entry.

---

## 5. Messages that exist only in Go

New texts, for situations Rust could not express. ipstack's Go-only texts
are listed in PORTING-ipstack.md, section 9: among them the panic
`ipstack: an MTU of %d is below MinMTU, %d`, the debug log
`fragmentation is not supported, dropping a datagram larger than the IP
MTU` (where smoltcp's text tells the reader to enable a Cargo feature), the
trace `ignoring FIN because its data was cut at the receive window's end`
(a deviation from smoltcp, section 3), and the panic `Getting the result of
a query in a free slot.`.

| Package | Situation | Kind | Text |
|---|---|---|---|
| satlink | negative SMS index | `ErrInvalidArgument` | `%d is not a message index` |
| satlink | an `SMSFilter` without a constant | `ErrInvalidArgument` | `%d is not an SMS filter` |
| satlink | a `DataCall` used after `HangUp` | `ErrCall` | `the data call is over` |
| satlink | a method of a handset that a data call holds (the Rust handset was moved into the call) | `ErrBusy` | `the handset is in a data call` |
| ppp | a server address that is not IPv4 (the zero `Addr` included) | `ErrInvalidAddress` | `the server's addresses must be IPv4, not %v` |
| ppp | a server authentication protocol without a constant | `ErrInvalidAuthProtocol` | `%d is not an authentication protocol` |
| satnet | a negative duration in the configuration | `ErrInvalidConfig` | `durations must not be negative` |
| satnet | a negative limit (replaces "limits must be above zero; use None for no limit") | `ErrInvalidConfig` | `limits must not be negative; use 0 for no limit` |
| satnet | a host whose address is not IPv4 | `ErrInvalidConfig` | `%q in hosts needs an IPv4 address` |
| satnet | the server's key matches no pin | `ErrTLS` | `invalid peer certificate: the public key is not pinned` |
| satnet | every verified chain to a trusted root holds an RSA key of under 2041 bits or an ECDSA key on P-224, or a root's own RSA key of over 8192 bits (crypto/tls refuses one in a certificate the server sends, with its own text; rustls reports a `CertificateError` of its own, `BadSignature` for a short RSA key) | `ErrTLS` | `invalid peer certificate: a key in the chain is RSA of under 2041 or over 8192 bits, or ECDSA on P-224` |
| satnet | a PEM section that does not decode | `ErrTLS` | `invalid PEM: a section is malformed` |
| satnet | use of a closed connection, or a call it woke | — | `net.ErrClosed` |
| satnet | `DialContext` with another network / a malformed address / a ctx that ended first | — | `net.UnknownNetworkError` / `*net.AddrError` / `&net.OpError{Op: "dial", Net: network, Err: ctx.Err()}` |
| sim | I/O or DTR after `Close` | `satlink.ErrTransport` | `the simulated handset is closed` |
| sim | a `Hosts` entry whose address is not IPv4 (logged; the call fails with NO CARRIER) | — | `%q in hosts needs an IPv4 address` |
| sim | `Control.RestartPPP` with an address that is not IPv4, which Rust's `Ipv4Addr` cannot express (nothing changes) | `*sim.ConfigError`, ppp's `ErrInvalidAddress` | `the server's addresses must be IPv4, not %v` |
| serial | I/O or DTR after `Close` | `satlink.ErrTransport` | `the serial port is closed` |
| serial | a library write that takes nothing without an error | `satlink.ErrTransport` wrapping `io.ErrShortWrite` | `transport error: short write` |
| serial | a library read that panics with a runtime error on a port with a descriptor of 1024 or more | `satlink.ErrTransport` wrapping the runtime error | `transport error: the serial library cannot wait for this port (select(2) watches no descriptor from 1024 up): runtime error: …` |
| mobile | a stream's (TCP, TLS) `maxLen` below 1 | InvalidArgument | `maxLen must be at least 1` |
| mobile | a negative `maxLen` for `UDPConnection.Receive` | InvalidArgument | `maxLen must not be negative` |
| mobile | a closed `IridiumHandset` used (every method but `Close`, `EstablishLinkWithHandset` included) | InvalidArgument | `the handset is closed` |
| mobile | a port outside 0–65535 | InvalidArgument | `port must be between 0 and 65535` |
| mobile | a nil settings, transport, handset, connector or sink | InvalidArgument | `%s must not be nil` |
| mobile | an unknown log level | InvalidArgument | `%d is not a log level` |
| mobile | a zero-value object from gomobile's constructor | InvalidArgument | `the %s was not made by this library` |
| mobile | a closed connection | InvalidArgument | `the connection is closed` |
| mobile | a recovered panic | Internal | `internal error: <panic>` |
| mobile | a panic recovered in an exported method without an error result (logged, error level) | — | `recovered a panic in %s: %v` |
| mobile | a panic in the link listener or a serial port closer (logged, error level) | — | `%s panicked: %v` |
| cmd/satlink | a recovered panic: in a `net` subcommand, the relay's sender, or anywhere else on the command's goroutine, which then exits with status 101 (standard error) | — | `panic: <value>` and the goroutine's stack |
