# satlink

A cross-platform client for Iridium satellite handsets (9555, 9555A, 9575/P/E, ROC
service port) over USB, for macOS, Linux and Android: the handset itself (status,
SMS, calls) and an **IP link that only your app can use**.

satlink is written in Go. [docs/DESIGN.md](docs/DESIGN.md) is the maintainers' guide: the
package layout, the conventions the code follows, and the contracts between packages.

## Why no driver is needed

Iridium's Windows installer (`Iridium Handset USB Driver Installer 6.3.9600.9520.exe`)
only binds a USB virtual COM port to vendor `0x1EDD`. On Windows 7/8 its INF
reuses Microsoft's stock `usbser.sys` (CDC-ACM). That means the handset is a
standard CDC-ACM device:

| Platform | How the handset appears | Transport |
|---|---|---|
| macOS | `/dev/cu.usbmodem*` (built-in `AppleUSBCDCACM`) | package `serial` |
| Linux | `/dev/ttyACM*` (built-in `cdc_acm`) | package `serial` |
| Android | USB host device, no tty | `AndroidUsbTransport.kt` → `mobile.ByteTransport` |
| Windows | COM port (with Iridium's driver) | package `serial` |

Everything above the byte stream is implemented once, in Go.

## Layout

```
.                   package satlink: AT protocol, typed commands, URCs, data calls (no I/O dependencies)
ppp/                PPP endpoint: framing, LCP, PAP/CHAP-MD5, IPCP (sans-I/O)
satnet/             app-private IP link: PPP + TCP/IP in-process, policy, limits, TLS
sim/                simulated handset + Iridium gateway + tiny internet, for tests and demos
serial/             serial transport + discovery by USB VID/PID
cmd/satlink/        the satlink command-line tool
mobile/             gomobile bindings: Kotlin and Java (Android), Swift and Objective-C (macOS)
internal/ipstack/   TCP/IP, modelled on smoltcp's design and covering what the link uses
internal/…          shared internals: log rendering, Debug-style texts, TLS cipher suites, lossy UTF-8, ASCII case, OS error texts, test support
android/            Kotlin USB-host transport, examples, USB device filter
linux/              udev rule
scripts/            gomobile build scripts
docs/               the maintainers' guides: DESIGN.md and IPSTACK.md
```

The module is `github.com/anhydrous99/satlink` and needs Go 1.27. Its direct
dependencies beyond the standard library are `go.bug.st/serial` (serial ports),
`github.com/spf13/cobra` (the CLI), `golang.org/x/crypto/x509roots/fallback` (TLS
public roots) and `golang.org/x/mobile` (the gomobile tools, pinned in `go.mod`).

## The app-private satellite link

Dial-up data normally goes through the operating system's PPP daemon, which creates
a network interface (`ppp0`) and routes the whole machine through it. Here the app
dials, then runs PPP and a TCP/IP stack **inside its own process**:

```
 your app ─ TLS ─ TCP/UDP/DNS ─ PPP ─ data call ─ USB ─ handset ─ satellite ─ Iridium gateway ─ internet
          crypto/tls  ipstack   ppp    satlink
 └──────────────────── all in-process: no interface, route or DNS entry exists for the OS ───────┘
```

Nothing else on the computer or phone can use the link, and the app decides exactly
what it may do:

- **Policy** — a default-deny allowlist (`api.example.com:443`, `*.example.com:443`,
  `203.0.113.0/24:8000-8099`, `mail.example.com:*`). Names are checked *before* they
  are looked up, so DNS cannot be used to reach or leak anywhere else either.
- **Limits** — hang up after an idle period (default 5 min), a maximum call duration
  (default 60 min, hang-up included) and/or a data volume. Airtime is billed per
  minute whether data flows or not. Zero means no limit. A limit that would be
  reached before the link is any use — a call time shorter than bringing PPP up
  and hanging up take (24 s by default), an idle time under 25 s (a DNS lookup
  may wait 10 s for each of two servers), a data volume under 2 KiB (PPP alone
  takes up to 500 bytes) — is refused before dialling, as are negative ones;
  `satnet.Config.Validate` checks sooner.
- **Satellite tuning** — about 300 bytes/s with 1.5–3 s round trips, so: 576-byte
  packets (smaller if the gateway asks), small TCP windows, packets released to the
  handset only as fast as the line carries them (so a backlog waits in the app
  rather than in the handset's buffer), the line shared in turns between
  connections, a segment each (so a bulk upload cannot hold up a chat),
  acknowledgements, connection set-up and DNS queries sent at once rather than
  behind anyone's data, TCP segments and DNS queries dropped when resent before an
  answer could possibly have arrived — or answered while they waited — no ICMP
  errors, a DNS cache, and static hosts that skip DNS altogether.
- **TLS** — Go's crypto/tls with no post-quantum key share (it would add 2.3 KB to
  every handshake), session resumption and optional public-key pinning. Reuse one
  `satnet.TLSConnector`: a full handshake costs ~3–5 KB (15–25 s), a resumed one
  ~1 KB. **No RFC 8879 certificate compression**: Go's crypto/tls does not implement
  it, so certificate chains arrive uncompressed, and full handshakes with servers
  that compress them (Cloudflare among them) cost a quarter to a third more airtime
  than they would with compression. A server that closes without the TLS goodbye gives
  `satnet.ErrTLSTruncated` rather than `io.EOF`: trust what arrived only if its own
  framing (a `Content-Length`, say) shows it is complete. Once `CloseWrite` has
  queued our goodbye, writes fail with `satnet.ErrConnectionReset` — the server
  would ignore them — even if it timed out; calling it again finishes the shutdown.

A lookup fails with `satnet.ErrNameNotFound` only when a DNS server said the name
has no IPv4 address; with no answer in time (each of up to two servers gets 10 s), or
only a server failure, it fails with `satnet.ErrTimedOut`, which is worth retrying.

Defaults target Iridium's own internet gateway: `008816000025` with bearer
`AT+CBST=71,0,1`, as Iridium's data manuals and ModemManager use. No PPP
credentials are needed. Some providers use other numbers (Telstra: `66666`); set
`satnet.Config.Number`. Your airtime plan must include circuit-switched data.

```go
handset, err := serial.Connect("", serial.DefaultBaud) // the only handset on USB
if err != nil {
	return err
}
defer handset.Close()

policy, err := satnet.ParsePolicy("relay.example.com:443")
if err != nil {
	return err
}
link, err := satnet.Establish(handset, satnet.NewConfig(policy)) // about 30 s
if err != nil {
	return err
}
tls, err := satnet.NewTLSConnector(satnet.TLSOptions{}) // one for the whole app
if err == nil {
	var conn *satnet.TLSConn
	conn, err = link.DialTLS("relay.example.com", 443, tls, 2*time.Minute)
	if err == nil {
		_, err = conn.Write([]byte("GET / HTTP/1.0\r\nHost: relay.example.com\r\n\r\n"))
	}
}
// Close delivers what was written, then hangs up, even after a failure:
// airtime is billed by the minute. The handset is then free for SMS.
return errors.Join(err, link.Close())
```

The connections are `net.Conn`s with deadlines, and `link.DialContext` dials for
`net/http`: give an `http.Transport` `DialContext: link.DialContext` and
`TLSClientConfig: tls.Config()`, so that the connector's session cache and trust
settings apply.

Setting the link up blocks for about half a minute. To stop it from another
goroutine — a cancel button, a signal handler — call
`config.Call.Cancellation.Cancel()` (a `*satlink.Cancellation`), or let a context
do it with `stop := context.AfterFunc(ctx, config.Call.Cancellation.Cancel)`: a dial
still in progress is aborted, a call that came up is hung up, and `Establish` fails
with an error for which `errors.Is(err, satlink.ErrCancelled)` holds. Once the link
is up, close it instead.

A write returns once the data is queued. Closing lets queued data arrive first — for
at most `Tuning.CloseGrace` (30 s) — and nothing new is accepted meanwhile, while
reads and waits already under way carry on; to know that a particular message
arrived, call `WaitAcknowledged` on its connection. A `Write` that timed out may
have queued none, some or all of its data, and what it queued is still sent —
`WaitAcknowledged` makes sure it is before closing — so the peer may get only the
start of a message. `Write` returns how much it queued, so exactly the rest can be
sent again, and `WriteSome` returns as soon as part of the data is queued; otherwise,
unless the protocol can tell, reset the connection with `Abort` (closing it would
end the stream cleanly after the part). Over TLS, what a write queued but could not
hand to TCP before its deadline stays in the session, and closing the link loses it:
call `WaitAcknowledged` first.

`link.Watcher()` follows the link's state from another goroutine (`LinkStateUp`,
`LinkStateClosing`, `LinkStateClosed`, each with its `CloseReason`); a dropped call
is reported as `CloseReasonCarrierLost`. While the link is up, the handset's own
methods fail with `satlink.ErrBusy`; once it has hung up, the handset is released.
A `Link` garbage-collected without `Close` still hangs up, in the background. If
the hang-up cannot be confirmed — neither `ATH` after the escape nor a DTR drop
was — `Status().CallMayStillBeUp` says so, and `Close` (or `Establish`,
for a link that never came up) fails with an error for which
`errors.Is(err, satlink.ErrCallMayStillBeUp)` holds: the call may still be billing.
`Handset.EndDataCall` retries once the handset answers; power-cycling it is the
last resort.

For a messaging app, the efficient shape is one long-lived TLS connection from the
app to a relay server you run, which talks to email, chat and social networks on the
app's behalf and sends only compact deltas over the satellite link.

### Keeping the operating system out

- The serial port is opened exclusively (`TIOCEXCL` on macOS and Linux), so no
  other program can open it meanwhile.
- **Linux:** install `linux/99-satlink.rules`
  (`sudo cp linux/99-satlink.rules /etc/udev/rules.d/ && sudo udevadm control --reload`);
  it grants the logged-in user access, adds a `/dev/iridium*` symlink, and stops
  ModemManager (whose Iridium plugin dials `008816000025` by itself) from touching
  the handset.
- **macOS:** don't add the handset as a modem in Network settings.
- Hanging up uses the `+++` escape and `ATH`; if that fails, the library drops DTR,
  which ends the call because `AT&D2` is set before every call.
  While the handset may still be online, only its own result codes count as answers:
  they end in a bare CR, which PPP keeps out of the data, so a server cannot fake a
  successful hang-up.
- If the app dies on macOS or Linux, the OS closes the port and drops DTR, so the
  handset ends the call — also while programs the app started still run, as the
  port is opened close-on-exec and they do not inherit it. A serial transport
  garbage-collected without `Close` closes its port too. **Android sends nothing
  when an app is killed:** the call
  stays up until the app next opens the handset, when `AndroidUsbTransport.open()`
  pulses DTR and `Initialize` ends a data call still up — escaping and hanging up
  one still online, and hanging up one held in online-command mode (escaped from
  but never hung up), which `AT+CPAS` reveals; if it cannot, opening the handset or
  the link throws `SatlinkErrorHangUpFailed`. With the handset's default
  `+CVHU=2`, a DTR drop ends a voice call too. The limits are enforced by the app, so
  they stop with it. That the handsets honour DTR over USB has not been verified on
  hardware.

## Simulator

Package `sim` emulates a handset (AT commands, SMS, voice and data calls, `&D2`,
DTR and `+CPAS`), Iridium's PPP gateway — which `Control.RestartPPP` can make start
over with a new address — and a tiny internet: DNS for `echo.test`, `web.test` and
`secure.test`; TCP/UDP echo on port 7, discard on 9 (counted in its stats), HTTP on
80 and TLS echo on 443 at every address, with a certificate from a test CA
(`sim.SecureTestCAPEM()`). `sim.IridiumLinkProfile()` reproduces the real call setup
time, latency and throughput; `sim.InstantLinkProfile()`, the default, is for tests.
`sim.NetworkConfig` sets the addresses IPCP assigns, the DNS servers it offers
(some may be silent: `SilentAddresses`) and the gateway's MRU. The TLS
test certificates are valid from 2026-10-19 03:17 UTC, so a machine whose clock is
behind that fails the TLS tests.

```go
simulator := sim.New(sim.DefaultConfig()) // a satlink.Transport, like a serial port
defer simulator.Close()
handset := satlink.NewHandset(simulator)
if err := handset.Initialize(); err != nil {
	return err
}
policy, err := satnet.ParsePolicy("echo.test:7")
if err != nil {
	return err
}
link, err := satnet.Establish(handset, satnet.NewConfig(policy))
if err != nil {
	return err
}
conn, err := link.Dial("echo.test", 7, time.Minute)
if err == nil {
	_, err = conn.Write([]byte("hello"))
}
reply := make([]byte, len("hello"))
if err == nil {
	_, err = io.ReadFull(conn, reply) // "hello"
}
return errors.Join(err, link.Close())
```

`simulator.Control()` plays the network's part: `ReceiveSMS`, `DropCall`,
`RestartPPP`, `Stats`.
Over `sim.IridiumLinkProfile()` with the default tuning, bringing the link up takes
about 27 s (20 s of call setup, then PPP), a DNS lookup 2 s, a TCP connect 2 s, an
HTTP/1.0 GET of `web.test` 2.6 s and the hang-up 1.5 s; a full TLS handshake with
`secure.test` costs about 2.2 KB in 8 s and a resumed one about 1.1 KB.

## CLI (macOS / Linux)

```sh
go install ./cmd/satlink          # from a checkout, into $GOBIN or $(go env GOPATH)/bin
satlink list                      # find handsets
satlink info                      # model, firmware, IMEI
satlink status                    # signal bars, battery, registration
satlink sms send +881631234567 "hello from the field"
satlink sms list unread
satlink at 'AT+CSQ'               # anything else, raw
satlink monitor                   # print incoming SMS / RING events

satlink net status                # dial, show the negotiated addresses, hang up
satlink net resolve example.com
satlink net get https://example.com/
satlink net tcp example.com 7     # like netcat
```

Without hardware, `--simulate` uses a simulated handset, with Iridium's real timing,
or with instant responses (`--simulate=instant`); `go run ./cmd/satlink …` runs any
of these without installing:

```sh
satlink --simulate net get http://web.test/            # realistic timing: about 40 s
satlink --simulate=instant -v net tcp echo.test 7      # no waiting, with logs
satlink --simulate=instant info
satlink --simulate=instant sms send +881631234567 "hello from the field"
satlink --simulate=instant net resolve echo.test
echo hello | satlink --simulate=instant net tcp echo.test 7
```

```
$ satlink --simulate=instant net get http://web.test/
Dialling 008816000025 — call setup over Iridium takes about half a minute…
Connected (19200).
  address:     10.64.0.2
  gateway:     10.64.0.1
  DNS servers: 10.64.0.1
HTTP/1.0 200 OK
Content-Type: text/plain
Content-Length: 42
Hello from the simulated Iridium network!
Call lasted 4 s: 514 bytes sent, 510 received.
```

`net` commands only allow their own target; add more with `--allow host:port`.
Limits: `--idle-secs`, `--max-minutes`, `--max-kb`. Everything is checked before
anything is dialled — a limit too small to be of use (see **Limits** above), a
wildcard or network such as `*.example.com` or `10.0.0.0/8` as a target, a URL with
spaces — and `net resolve` of an IPv4 address dials nothing.

`net get` allows 10 minutes for the request and its whole response, stops reading
once the body is as long as announced, hangs up as soon as the response is in — a
slow reader such as a pager does not keep the call up — and exits non-zero when the
response may be incomplete, printing what arrived even when the call dropped or a
limit ended it. `net tcp` exits non-zero if standard input could not all be
delivered — the remote end closing first, or resetting what arrived after it closed,
included, unless standard input is a terminal — and then resets the connection, so
the far end cannot take a part for the whole.

Ctrl-C, `SIGTERM` or `SIGHUP` during call setup cancels it: the dial is aborted
rather than left to connect and bill first. Once the call is up, it hangs up
properly. Either way the CLI then ends by `SIGINT`, so that a shell loop or script
around it stops too. Another one, a few seconds after the first, quits at once,
leaving the call to the DTR drop, and says so; a `SIGHUP` or `SIGINT` the CLI was
started with ignored, as under `nohup` or in a background job, stays ignored. On
Windows, Ctrl-C ends with status 130, and closing the console starts the hang-up
before Windows ends `satlink`. Suspending the CLI with Ctrl-Z suspends its limits
too. Log lines (`-v`) that standard error cannot take at once wait, or are dropped
and counted, rather than hold the link up. If a hang-up could not be confirmed —
after the call, or when setting it up failed — the CLI says so: end the call with
`satlink hangup`, or power-cycle the handset.

Every command shows a summary of its arguments with `-h`, and more with `--help` or
`satlink help COMMAND…`, as in `satlink help net get`. The exit status is:

- 0 on success;
- 1 when the command failed: a standard output that cannot be written, a pipe to a
  `head` that has had enough, included, and a panic once the link is up, which
  hangs up first and is reported on standard error;
- 2 when the command line is wrong;
- 101 when `satlink` panicked while readying the handset, in a handset command or
  while setting a call up (a bug, reported on standard error);
- once interrupted, death by `SIGINT` (status 130 on Windows).

Output to a standard output opened read-only on Unix, or to one that `satlink` was
not handed on Windows, is dropped without an error. On
Windows, one opened read-only fails the command.

Use `--port /dev/ttyACM0` (or `SATLINK_PORT`) when more than one handset is
attached, and `satlink net --number` (or `SATLINK_DATA_NUMBER`) for another
provider's data number. On Linux, install `linux/99-satlink.rules` so you don't need root and
ModemManager leaves the port alone; port discovery reads sysfs, so no libudev is
needed. On macOS, building needs cgo and the macOS SDK (the Command Line Tools are
enough), as port discovery uses IOKit.

## Go: the handset

```go
handset, err := serial.Connect("", serial.DefaultBaud) // opens and initialises it
if err != nil {
	return err
}
defer handset.Close()
bars, err := handset.SignalQuality()
if err != nil {
	return err
}
fmt.Println(bars, "bars")
_, err = handset.SendSMS("+881631234567", "hello")
return err
```

`satlink.Handset` works over any `satlink.Transport` (`WriteAll` + `ReadTimeout`
with a timeout, plus `satlink.DTRController` where there is a DTR line), so other
links such as Bluetooth serial or TCP bridges only need that interface. It is safe
for use by several goroutines. `Handset.DialData` places circuit-switched data calls,
and `DataCall.HangUp` ends them (`+++`, `ATH`, and a DTR drop if that fails);
meanwhile the handset's own methods fail with `satlink.ErrBusy`, and the settings'
`Cancellation` stops a call being set up. `Handset.Initialize` also ends a data call
an earlier session left up — still online, or held in online-command mode — or
reports `satlink.ErrCallMayStillBeUp` if it cannot. `Handset.Answer` is for voice
calls: a data call answered by mistake — or one that may come up after `ATA`'s
answer was lost, unless the handset then shows a voice call up — is ended again at
once, or reported as `satlink.ErrCallMayStillBeUp`.

| Method | AT command |
|---|---|
| `Initialize` | `AT+CPAS` (`ATH` for a data call left up), `ATE0`, `ATV1`, `AT+CMEE=2`, `AT+CMGF=1` |
| `Info` | `+CGMI`, `+CGMM`, `+CGMR`, `+CGSN` |
| `IMSI` | `+CIMI` |
| `SignalQuality` (0–5) | `+CSQ` |
| `Battery` | `+CBC` |
| `Registration` | `+CREG?` |
| `Clock` | `+CCLK?` |
| `SendSMS` / `ListSMS` / `ReadSMS` / `DeleteSMS` | `+CMGS` / `+CMGL` / `+CMGR` / `+CMGD` (text mode) |
| `Dial` / `Answer` / `HangUp` | `ATD…;` / `ATA` (a data call is ended again; `AT+CLCC` if `ATA`'s answer was lost) / `ATH` (`NO CARRIER` counts as hung up) |
| `DialData` / `AnswerData` / `DataCall.HangUp` | `AT&D2`, `AT+CBST`, `ATD…` / `ATA` / `+++`, `ATH`, DTR drop |
| `Command`, `CommandTimeout` | anything |
| `TakeURCs` / `PollURCs` | queued `RING`, `+CMTI`, `+CREG`, … |

Errors come back typed, as `*satlink.Error` whose kind is a sentinel for
`errors.Is`: `ErrEquipment` (`+CME ERROR`), `ErrSMS` (`+CMS ERROR`), `ErrCall`
(`NO CARRIER`, `BUSY`), `ErrTimeout`, `ErrTransport`, `ErrBusy`,
`ErrCallMayStillBeUp` ("the call may still be up: …", from `Initialize` and
`Answer`, and matching every failed hang-up), `ErrCancelled` and more.

## Android

```sh
ANDROID_HOME=… ANDROID_NDK_HOME=… ./scripts/build-android.sh
```

The script needs the Android SDK (for `android.jar`), the NDK and a JDK (21 and 27
both accept gomobile's Java 8 target), and builds the `go.mod`-pinned `gobind`
itself. It writes `android/build/generated/satlink.aar`, with the Go code for
armeabi-v7a, arm64-v8a and x86_64 and its Java bindings in package
`io.github.anhydrous99.satlink.mobile`. Add the `.aar` and `android/src/main/` to
your app module, and declare `<uses-feature android:name="android.hardware.usb.host" />`.
Then:

```kotlin
// One for the whole app: later connections resume their TLS session, ~1 KB of
// airtime instead of 3–5 KB.
val tls = TLSConnector(TLSSettings())

AndroidUsbTransport.open(usbManager, device).use { transport ->
    val handset = IridiumHandset(transport)
    try {
        val settings = LinkSettings().apply { allow("relay.example.com:443") }
        try {
            val link = Mobile.establishLinkWithHandset(handset, settings, null, null)
            try {
                val connection = link.openTLS("relay.example.com", 443, tls, 120_000)
                try {
                    connection.send("hello".toByteArray(), 60_000)
                    val reply = connection.receive(4096, 120_000)
                } finally {
                    connection.close()
                }
            } finally {
                link.hangUp(120_000) // while the transport is open: the hang-up goes through it
            }
        } catch (e: Exception) {
            when (Mobile.asSatlinkError(e)?.kind) {
                // The call may still be up: end it, through the transport still open.
                Mobile.SatlinkErrorHangUpFailed, Mobile.SatlinkErrorInternal -> handset.reinitialize()
                else -> throw e
            }
        }
        handset.sendSMS("+881631234567", "back on SMS") // the link gave the handset back
    } finally {
        handset.close()
    }
}
```

gomobile objects are not `AutoCloseable`, so `use {}` does not apply to them: close
each in a `finally` block, and hang up a link in one. One transport has one user at
a time: `establishLinkWithHandset` lends the handset to the link until its call ends,
and meanwhile the handset's methods throw `SatlinkErrorHandsetBusy`; closing a lent
handset leaves the port to its link until the call has ended. (`Mobile.establishLink(transport, …)`
is for apps that only use data.) `hangUp` returns the link's state, `LinkStateClosed`
with why it ended, or the state meanwhile if it is still closing.

A call left up keeps billing. After these errors one may be up, and this ends it:

| The app has | Thrown | End the call with |
|---|---|---|
| an `IridiumHandset`, lent with `establishLinkWithHandset` or not | `SatlinkErrorHangUpFailed` from `hangUp`, `establishLinkWithHandset` or `answer` (a data call answered by mistake); `SatlinkErrorInternal` from `establishLinkWithHandset`, a fault inside the library, after which whether a call is up is unknown | `handset.reinitialize()`. Until it succeeds, the handset's other methods throw `SatlinkErrorHangUpFailed`, and so does `reinitialize` while it cannot end the call. |
| the transport, its link made with `Mobile.establishLink` | `SatlinkErrorHangUpFailed` from `hangUp` or `establishLink`; `SatlinkErrorInternal` from `establishLink` | a new `IridiumHandset` on the transport, still open, and its `hangUp()`: making it ends a data call still up, and `hangUp` one its firmware cannot report |
| the transport, `IridiumHandset(transport)` having failed | `SatlinkErrorHangUpFailed`: the handset does not answer, and a data call it may be relaying could not be ended | a new `IridiumHandset` on the transport, later |

Catch these while the transport is open, before the handset is closed, and hang up
before the transport closes, even after an exception: a link merely dropped as its
transport closes may leave the call up. Power-cycling the handset is the last resort
(then open the transport and the handset anew). `Mobile.asSatlinkError(e)` gives an
exception's details, such as its `kind`. A closed `IridiumHandset` refuses further
use: make a new one on the still-open transport to talk to the handset again.

A `Cancellation` passed as the establish functions' last argument stops the setup
from another thread — a cancel button's — with `cancellation.cancel()`: a dial still
in progress is aborted, a call that came up is hung up, and the establish function
throws `SatlinkErrorCancelled`. Use a new one for each link. A `send` that times out
cannot say how much it queued; `sendSome` returns it, so exactly the rest can be sent
again, and `abort()` resets the connection so that the peer cannot take a part for
the whole. Over TLS, what `sendSome` took may still wait in the session:
`waitAcknowledged` before `hangUp` sends it, as closing the link drops it. On one
connection, receive from one thread and send from one thread at a time: the calls
in one direction share one deadline. Each establish call reads its `LinkSettings`
once, as it starts.
`TLSConnection.shutdownWrite` sends the TLS goodbye — sending fails from then on, even
after a timeout, and calling it again finishes it; `close()` only releases the
object.

All calls block (call setup takes ~30 s, SMS submission can take over a minute), so
call them off the main thread. A `LinkListener` passed to `establishLink` hears every
state change. In Kotlin, the handles — `IridiumHandset`, `SatelliteLink`, the
connections, `TLSConnector`, `Cancellation` and the lists — equal every other of
their class, because gobind's `equals` compares exported fields only: compare them
with `===`, and keep them in identity-based collections. See
`android/src/main/java/io/github/anhydrous99/satlink/Example.kt`.

## macOS apps (Swift)

```sh
./scripts/build-bindings.sh
```

The script needs a full Xcode (gomobile refuses the Command Line Tools alone) and
writes `build/bindings/Satlink.xcframework`, whose names start with `SatlinkMobile`.
Use `SatlinkMobileOpenSerialHandset(path, baud, &error)` or
`SatlinkMobileEstablishSerialLink(…)` with a path from
`SatlinkMobileDiscoverSerialHandsets(&error)`. Make objects with these functions,
`SatlinkMobileNewIridiumHandset` and `SatlinkMobileNewTLSConnector`: the `init` that
`SatlinkMobileIridiumHandset`, `SatlinkMobileTLSConnector` and the interface classes
(`SatlinkMobileByteTransport`, …) inherit from NSObject would make an object with no
Go side, whose first use ends the app, so the script marks it unavailable and the
compiler refuses it. Implement `SatlinkMobileByteTransportProtocol`, and the other
protocols, in a class of your own. A serial link keeps its port, which
only one may open, until its call has ended: after `SatlinkErrorHangUpFailed` from a
serial link's `HangUp`, the link closes the port a moment later, and then
`SatlinkMobileOpenSerialHandset` on the same path can end the call. iOS is not
supported: iPhones don't expose USB CDC devices to apps. There are no Kotlin/JVM desktop
bindings: gomobile targets Android and Apple platforms only.

## Behaviour notes

These are the places where satlink deliberately behaves in a way a reader might not
expect, with the reason for each. [docs/DESIGN.md](docs/DESIGN.md) section 3 lists
them with their reasons in full.

- **No RFC 8879 certificate compression** (above): Go's crypto/tls lacks it.
- **TLS roots and certificates.** TLS uses Go's crypto/tls. The public roots are
  Mozilla's, from `golang.org/x/crypto/x509roots/fallback`, whatever the operating
  system trusts, with the distrust dates NSS sets on the roots Mozilla phases out: a
  chain issued after its root's date is refused (Izenpe.com's, after 2026-04-15).
  crypto/tls asks servers for ML-DSA and P-521 certificates and cannot be told not to,
  so satnet takes chains with such keys. A server that holds one next to a classical
  certificate may send it, and an ML-DSA-65 leaf adds about 5 KB (some 17 s) to a full
  handshake. crypto/tls also asks for RSA-PSS signatures before ECDSA ones, so a server
  that holds several certificates may send satnet another one than it would otherwise:
  pin the key of each (`TLSOptions.PinnedPublicKeys`). Chains with an RSA key under
  2041 bits (a 2048-bit minimum counted in whole bytes) or over 8192 bits, or an ECDSA
  key on P-224, are refused.
- **Handset ownership.** The handset is never moved into a link or a call: while one
  holds it, its methods fail with `satlink.ErrBusy`, and it is released when the call
  ends. A `DataCall` dropped without `HangUp` ends nothing, so hang up in a deferred
  function. An error that may leave a call up matches `satlink.ErrCallMayStillBeUp`
  and keeps its own text.
- **Go API where Go needs it.** net.Conn deadlines, `Link.DialContext` and
  `TLSConnector.Config` for `net/http`, and `Close` methods on values that hold a
  resource.
- **Simulator TLS echo.** The echo is always on, so `satlink --simulate net get https://…`
  fails certificate verification (the test CA is not a public root) instead of failing
  to connect.
- **TLS 1.2 resumption.** crypto/tls resumes a TLS 1.2 session with a ticket only, never
  by session ID: a TLS 1.2 server that issues no tickets costs a full handshake every
  time.
- **Acknowledged data.** `WaitAcknowledged` never reports as delivered what never left:
  data that an address change, an `Abort` or the hang-up dropped makes it fail
  (`ErrConnectionReset`, `ErrLinkDown`).
- **Serial writes and descriptors.** Serial writes have no timeout, and errors other
  than the operating system's own keep the serial library's wording. On Unix, a serial
  port opened while the process holds about a thousand files fails its reads, as the
  serial library waits with select(2).
- **Transport panics during a call.** A transport that panics while a data call is set
  up or hung up has DTR dropped briefly before the panic goes on, which ends the call
  after `AT&D2`.
- **Log lines** name the Go package (`[INFO  satnet] …`).
- **Signals and terminals.** A `SIGTERM` the CLI was started with ignored is caught. A
  CLI started with `SIGINT` ignored ends an interrupted run with status 130 instead of
  by `SIGINT`. On Windows, closing the console starts the hang-up before Windows ends
  the process; MSYS and Cygwin terminals are not taken for terminals; and a console
  without `TERM` gets no styles.
- **`net get https://…`** ends its TLS session with the TLS goodbye, one alert record
  more of airtime.
- **Mobile API.** It keeps to what gomobile binds: lists with `Len`/`Get`, int constants
  for enumerations, `int64` milliseconds. In Java and Kotlin, handles of one class all
  compare equal, and records compare their fields, but the lists they hold always compare
  equal, and the `Data` of `Received` and `Datagram` compares by reference, so compare
  contents. Settings compare without their rules, hosts, roots, pins and ALPN protocols.
- **Defects not reproduced.** satlink does not reproduce these defects: PPP negotiations
  stuck after a stray Terminate-Ack, and a Nak that a hostile request could inflate into a
  panic; the CLI's handset commands panicking with status 101 when their output cannot be
  written, where satlink fails with status 1; and three of smoltcp 0.14's: a SYN's window
  reaching past the receive buffer, a FIN taken after data the window cut short, and
  segments from half the sequence space away taken while the receive window ends before
  it starts. ipstack also keeps its `PollAt` from a panic of smoltcp's `poll_at` after a
  peer's early SYN, or a SYN|ACK to a socket without a receive buffer, which ipstack
  cannot reach.

## Testing

```sh
go test ./...                 # everything, offline: about two minutes
go test -short ./...          # without the real-time tests and the cross-platform builds
go test -race ./...           # as the checks below run it
go test -run='^$' -fuzz='^FuzzFrameDecoder$' -fuzztime=30s ./ppp   # one fuzz target
go test -run='^$' -fuzz='^FuzzSocketAgainstAHostilePeer$' -fuzztime=60s -parallel=2 ./internal/ipstack/tcp
```

The checks every change passes:

```sh
gofmt -l .
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
go run github.com/fzipp/gocyclo/cmd/gocyclo@latest -over 15 .
go test -race -cover ./...
```

The tests are written beside the code they cover. [docs/DESIGN.md](docs/DESIGN.md)
lists them file by file (section 1) and says how they are written (section 2, Tests).

PPP has been checked against RFC test vectors, its option types and packet codes
against the RFCs' numbering, and satlink's own server role (the simulator's gateway),
not yet against `pppd`: that needs a mode that runs PPP on a raw serial line without
AT commands, which does not exist yet.

## Status

- **Not yet tested against real hardware.** The command set is the standard
  3GPP 27.005/27.007 subset Iridium handsets document; response formats may need
  small adjustments once checked against a real 9555/9575.
- The gateway behaviour the defaults rely on (number, bearer, no authentication,
  DNS over IPCP) comes from Iridium's manuals and published logs; that
  `008816000025` still answers in 2026 has not been confirmed first-hand.
- The Android library and the macOS framework have not been built here (no Android
  SDK or NDK, and no full Xcode, on the build machine): the generated Java bindings
  compile against a stub of the Android SDK, the Kotlin was checked against them by
  hand, and gomobile's macOS and Mac Catalyst archives build and were driven from
  Objective-C.
