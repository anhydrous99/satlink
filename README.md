# satlink

satlink drives Iridium satellite handsets (9555, 9555A, 9575/P/E and the ROC
service port) over USB, from macOS, Linux and Android. It covers the handset
itself (status, SMS, calls) and gives your app an IP link that **only your app
can use**.

> [!WARNING]
> satlink has not yet been tested against real hardware. See [Status](#status).

## Why satlink

Dial-up data normally goes through the operating system's PPP daemon. That
creates a network interface and routes the whole machine over a link billed by
the minute. satlink dials, then runs PPP and a TCP/IP stack inside your app's
own process:

```
 your app ─ TLS ─ TCP/UDP/DNS ─ PPP ─ data call ─ USB ─ handset ─ satellite ─ Iridium gateway ─ internet
          crypto/tls  ipstack   ppp    satlink
 └──────────────────── all in-process: no interface, route or DNS entry exists for the OS ───────┘
```

Nothing else on the computer or phone can use the link, and your app decides
what it may do:

- **[Policy](https://pkg.go.dev/github.com/anhydrous99/satlink/satnet#Policy):**
  a default-deny allowlist, such as `api.example.com:443`. Names are checked
  before they are looked up, so DNS cannot leak data either.
- **[Limits](https://pkg.go.dev/github.com/anhydrous99/satlink/satnet#Limits):**
  hang up after an idle period, a call duration or a data volume. Airtime is
  billed by the minute whether data flows or not.
- **[Tuning](https://pkg.go.dev/github.com/anhydrous99/satlink/satnet#Tuning):**
  built for about 300 bytes/s and 1.5–3 s round trips. Packets are small,
  connections share the line in turns, and no airtime goes on retransmissions
  sent too early.
- **[TLS](https://pkg.go.dev/github.com/anhydrous99/satlink/satnet#TLSConnector):**
  a resumed handshake costs about 1 KB instead of 3–5 KB. Keep one connector
  for the whole app.

## Quick start

### Without hardware

The simulator plays a handset, Iridium's gateway and a tiny internet:

```sh
go run ./cmd/satlink --simulate=instant net get http://web.test/
```

```
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

Without `=instant`, the simulator uses Iridium's real timing, and the same
request takes about 40 s.

### The CLI, with a handset

```sh
go install github.com/anhydrous99/satlink/cmd/satlink@latest
satlink list                          # find handsets
satlink status                        # signal bars, battery, registration
satlink sms send +881631234567 "hello from the field"
satlink net get https://example.com/  # dial, fetch, hang up
```

On Linux, install the udev rule first. It grants you access without root and
keeps ModemManager away from the handset:

```sh
sudo cp linux/99-satlink.rules /etc/udev/rules.d/ && sudo udevadm control --reload
```

The [CLI guide](docs/cli.md) covers every command, the limits, interrupting a
call and the exit status.

### The Go library

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
		_, err = conn.Write([]byte("hello from the sky"))
	}
}
// Close delivers what was written, then hangs up, even after a failure:
// airtime is billed by the minute. The handset is then free for SMS.
return errors.Join(err, link.Close())
```

The [package documentation](https://pkg.go.dev/github.com/anhydrous99/satlink)
is the API reference. Start with `satnet` for the link, `iridium` for the
handset and `sim` for tests.

### Android and macOS apps

- Kotlin and Java: the [Android guide](docs/android.md).
- Swift and Objective-C: the [macOS guide](docs/macos.md).

## Packages

| Package | What it is |
|---|---|
| [`iridium`](https://pkg.go.dev/github.com/anhydrous99/satlink/iridium) | The handset: AT commands, SMS, voice and data calls, over any byte transport |
| [`satnet`](https://pkg.go.dev/github.com/anhydrous99/satlink/satnet) | The app-private IP link: PPP and TCP/IP in-process, with policy, limits and TLS |
| [`serial`](https://pkg.go.dev/github.com/anhydrous99/satlink/serial) | The desktop transport: finds handsets by USB vendor ID and opens them |
| [`sim`](https://pkg.go.dev/github.com/anhydrous99/satlink/sim) | A simulated handset, gateway and internet, for tests and demos |
| [`ppp`](https://pkg.go.dev/github.com/anhydrous99/satlink/ppp) | A sans-I/O PPP endpoint |
| [`mobile`](https://pkg.go.dev/github.com/anhydrous99/satlink/mobile) | gomobile bindings for Android and Apple platforms |
| [`cmd/satlink`](docs/cli.md) | The command-line tool |
| `internal/…` | The TCP/IP stack (`internal/ipstack`) and shared internals |

## Platforms

The handset is a standard USB CDC-ACM device, so no vendor driver is needed.
Iridium's Windows installer only binds Microsoft's stock `usbser.sys` to it.

| Platform | The handset appears as | Transport |
|---|---|---|
| macOS | `/dev/cu.usbmodem*` | `serial` |
| Linux | `/dev/ttyACM*` | `serial` |
| Windows | a COM port, with Iridium's driver | `serial` |
| Android | a USB host device | `AndroidUsbTransport.kt` and `mobile` |

On macOS, don't add the handset as a modem in Network settings. iOS is not
supported: iPhones don't expose USB CDC devices to apps.

## Requirements

- **Go 1.27.** On macOS, building also needs cgo and the macOS SDK, as port
  discovery uses IOKit. The Command Line Tools are enough.
- **An airtime plan with circuit-switched data.**
- **Your provider's data number.** The default is Iridium's own gateway,
  `008816000025`, which needs no PPP credentials. Some providers use another,
  such as Telstra's `66666`: set `satnet.Config.Number`, or `--number` on the
  CLI.

## Status

- **Not yet tested against real hardware.** satlink uses the standard
  3GPP 27.005/27.007 subset Iridium handsets document. Response formats may need
  small fixes once checked against a real 9555 or 9575.
- **DTR over USB is unverified.** If an app dies, the operating system drops
  DTR, which should end the call. That the handsets honour this over USB has
  not been checked on hardware.
- **The gateway defaults are second-hand.** The number, bearer, lack of
  authentication and DNS over IPCP come from Iridium's manuals and published
  logs. That `008816000025` still answers in 2026 has not been confirmed.
- **The mobile bindings have not been built on full toolchains.** The Java
  bindings compile against a stub of the Android SDK. The macOS archives build
  and were driven from Objective-C.

## Documentation

- [CLI guide](docs/cli.md), [Android guide](docs/android.md) and
  [macOS guide](docs/macos.md)
- [Go API reference](https://pkg.go.dev/github.com/anhydrous99/satlink)
- [Contributing](CONTRIBUTING.md): tests, checks and how the docs are written
- For maintainers: the [design guide](docs/DESIGN.md) and the
  [TCP/IP stack notes](docs/IPSTACK.md)
