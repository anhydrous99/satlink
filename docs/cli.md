# The satlink command

`satlink` talks to an Iridium handset over USB. It reads what the handset
reports, sends and reads SMS, places voice calls and sends raw AT commands. It
can also bring up an IP link over a data call that only `satlink` itself can
use. This guide is for people using it from a shell. For every option,
`satlink help COMMAND…` is the reference.

## Install

```sh
go install github.com/anhydrous99/satlink/cmd/satlink@latest
```

From a checkout, `go install ./cmd/satlink` does the same, and
`go run ./cmd/satlink …` runs it without installing.

- **Linux:** install the udev rule:

  ```sh
  sudo cp linux/99-satlink.rules /etc/udev/rules.d/ && sudo udevadm control --reload
  ```

  It grants the logged-in user access and adds a `/dev/iridium*` symlink. It
  also stops ModemManager from touching the handset, as its Iridium plugin
  would otherwise dial by itself. Port discovery reads sysfs, so no libudev is
  needed.
- **macOS:** building needs cgo and the macOS SDK; the Command Line Tools are
  enough. Don't add the handset as a modem in Network settings.
- **Windows:** Iridium's driver makes the handset a COM port.

## Commands

| Command | What it does |
|---|---|
| `satlink list` | List connected handsets |
| `satlink info` | Manufacturer, model, firmware and IMEI |
| `satlink status` | Signal bars, battery and network registration |
| `satlink sms send NUMBER TEXT` | Send a text message |
| `satlink sms list [unread]` | List stored messages |
| `satlink sms read INDEX` / `delete INDEX` | Show or delete one message |
| `satlink dial NUMBER` / `answer` / `hangup` | Voice calls. `hangup` also ends a data call left up |
| `satlink at 'AT+CSQ'` | Send any AT command, raw |
| `satlink monitor` | Print incoming SMS and `RING` events until interrupted |
| `satlink net status` | Dial, show the negotiated addresses, hang up |
| `satlink net resolve HOST` | Look up a host name over the link |
| `satlink net get URL` | Fetch an `http://` or `https://` URL and print its body |
| `satlink net tcp HOST PORT` | Relay standard input and output, like netcat |

`-h` shows a summary of a command's arguments, and `--help` shows more, as does
`satlink help net get`.

## Choosing the handset

`satlink` finds the handset by Iridium's USB vendor ID. When more than one is
attached, name it with `--port /dev/ttyACM0` or the `SATLINK_PORT` variable.
The serial speed is `--baud`, 19200 by default.

## Trying it without a handset

`--simulate` replaces the handset with a simulated one, using Iridium's real
timing. `--simulate=instant` answers at once:

```sh
satlink --simulate net get http://web.test/            # realistic timing: about 40 s
satlink --simulate=instant -v net tcp echo.test 7      # no waiting, with logs
satlink --simulate=instant sms send +881631234567 "hello"
echo hello | satlink --simulate=instant net tcp echo.test 7
```

The simulated internet has three names: `echo.test`, `web.test` and
`secure.test`. Every address answers TCP and UDP echo on port 7, discard on 9,
HTTP on 80 and TLS echo on 443. `net get https://secure.test/` fails
certificate verification on purpose, as the simulator's test CA is not a
public root.

## The network commands

Each `net` command dials, does its work and hangs up.

**What it may reach.** A command only allows its own target. Add more with
`--allow RULE`, which is repeatable. The rule syntax is that of
[satnet's policy](https://pkg.go.dev/github.com/anhydrous99/satlink/satnet#hdr-Policy_rules),
such as `api.example.com:443` or `*.example.com:443`.

**Limits.** The call hangs up when one is reached:

| Option | Limit | Default |
|---|---|---|
| `--idle-secs` | Seconds without traffic | 300 |
| `--max-minutes` | Call duration, hanging up included | 60 |
| `--max-kb` | Kilobytes of traffic, both directions | none |

Zero means no limit.

**Checked before dialling.** `satlink` refuses these before it dials, so they
cost no airtime:

- A negative limit.
- A limit too small to be of use: an idle time under 25 s, or a data volume
  under 2 KiB.
- A wildcard or network as a command's target, such as `*.example.com` or
  `10.0.0.0/8`.
- A URL with spaces.

`net resolve` of an IPv4 address dials nothing.

**Another provider.** `--number` (or `SATLINK_DATA_NUMBER`) sets the data
number. The default is Iridium's gateway, `008816000025`. `--username` and
`--password` set PPP credentials; Iridium's gateway accepts any.

**`net get`** allows 10 minutes for the request and the whole response. It
stops reading once the body is as long as announced, and hangs up as soon as
the response is in. So a slow reader, such as a pager, does not keep the call
up. It prints whatever arrived, even when the call dropped or a limit ended it,
and exits non-zero if the response may be incomplete. Over `https://`, it ends
the session with the TLS goodbye, which costs one alert record of airtime.

**`net tcp`** exits non-zero if standard input could not all be delivered.
That includes the remote end closing first, or resetting data that arrived
after it closed, unless standard input is a terminal. It then resets the
connection, so the far end cannot mistake a part for the whole.

## Interrupting a call

Ctrl-C, `SIGTERM` or `SIGHUP` during call setup cancels the dial, rather than
letting it connect and start billing. Once the call is up, `satlink` hangs up
properly. Either way it then ends by `SIGINT`, so a shell loop around it stops
too.

- **A second signal**, a few seconds after the first, quits at once. The call
  is then left to the DTR drop, and `satlink` says so.
- **Signals ignored at start** stay ignored, as under `nohup` or in a
  background job. The exception is `SIGTERM`, which is always caught.
- **Windows:** Ctrl-C and Ctrl-Break do the same, and `satlink` exits with
  status 130. Closing the console, logging off or shutting down starts the
  hang-up too, but Windows ends `satlink` a few seconds later.
- **Ctrl-Z** suspends the limits along with `satlink`.

> [!CAUTION]
> If a hang-up cannot be confirmed, the call may still be up and billing.
> `satlink` says so. End the call with `satlink hangup`, or power-cycle the
> handset.

## Exit status

| Status | Meaning |
|---|---|
| 0 | Success |
| 1 | The command failed. This includes a standard output that cannot be written, such as a pipe to a `head` that has had enough. It also includes a panic once the link is up: `satlink` hangs up first and reports it on standard error |
| 2 | The command line is wrong |
| 101 | `satlink` panicked while readying the handset, in a handset command or while setting a call up. This is a bug, reported on standard error |
| death by `SIGINT` | Interrupted. On Windows, or when `SIGINT` was ignored at start, the status is 130 instead |

On Unix, output to a standard output opened read-only is dropped without an
error. On Windows, the same fails the command, and output to a standard output
`satlink` was not handed is dropped.

## Logging

`-v` logs what the link is doing, and `-vv` and `-vvv` add detail. Each line
names the Go package, as in `[INFO  satnet] …`. Lines that standard error
cannot take at once wait, or are dropped and counted, so logging never holds
the link up.

Styles are used only on terminals. On Windows, MSYS and Cygwin terminals are
not taken for terminals, and a console without `TERM` gets no styles.
