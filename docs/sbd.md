# Short Burst Data

satlink sends and receives SBD messages over the handset's USB serial connection.
Use this guide for explicit exchanges or a durable background worker.

SBD must be provisioned by your airtime provider, with an email or DirectIP
receiving destination. Your voice or circuit-switched data plan alone does not
establish SBD service. This project implements the handset side; the receiving
server stays external. SBD messages do not use PPP or the `satnet` IP link.

## Try USB without satellite airtime

```sh
satlink --simulate=instant sbd loopback --text hello
satlink sbd status
satlink sbd loopback --file payload.bin
```

`status` reads local buffer state. `loopback` loads the outgoing buffer, copies
it to incoming, verifies the binary bytes and clears both buffers. It refuses
to overwrite existing data. It does not establish a satellite session.

The default voice-handset limits are 1,960 outbound bytes and 1,890 inbound bytes.
Set `--max-mo` and `--max-mt` below those limits if your provider requires it.
Loopback payloads must fit both limits. A local buffer copy can be tested with
`--max-mt 1960`; that does not establish a 1,960-byte satellite receive limit.
Oversized messages are refused, without fragmentation or added wire headers.

## Explicit exchanges

```sh
satlink sbd send --text 'application-id:123 hello'
satlink sbd send --file payload.bin
satlink sbd receive --output received.bin
```

`--file -` reads binary stdin. Text is sent as UTF-8 bytes. Exactly one payload
source is required. `send` prints the session result as JSON. Gateway acceptance
does not confirm that your destination application has processed the message.
A session can receive a message while sending one; that payload is saved in the
local inbox before the incoming handset buffer is cleared.

`receive` reads an existing incoming buffer first, or starts one receive-only
session. It refuses an occupied outgoing buffer. Received bytes go to stdout
unless `--output` is given; session metadata goes to stderr. The payload is also
retained in the durable inbox. Reading a message does not acknowledge it.

Satellite sessions and mailbox checks may incur charges under your SBD plan.
The default session deadline is 120 seconds. A lost final result means delivery
is uncertain, even if the host times out. No dial-up call is established.

## Durable messaging

```sh
satlink sbd enqueue --file payload.bin
satlink sbd run
```

Start `run` explicitly in the foreground. It opens the handset and processes
queued messages. Other processes may enqueue and inspect the same queue while
the worker runs. Only one worker can own a queue. Stop it before using that
handset for SMS, calls, raw commands or PPP. Do not open its USB port in another
program or use another queue for the same phone concurrently.

The queue defaults to `satlink/sbd` under the operating system's user
configuration directory. `--queue-dir DIR` selects another private directory;
use it consistently across commands. Each queue binds to its first handset's
IMEI and refuses a different handset. Queue files contain message payloads and
identity metadata; keep the directory in application-private storage.

Useful queue operations:

| Command | Behaviour |
|---|---|
| `sbd outbox` | JSON records with state, payload and attempt history |
| `sbd inbox` | JSON received records; payloads are base64 |
| `sbd read ID` | Exact inbox payload bytes |
| `sbd ack ID` | Remove a consumed inbox record |
| `sbd cancel ID` | Prevent future attempts; refuses active/accepted records |
| `sbd retry ID` | Explicitly requeue with a new retry budget |
| `sbd purge ID` | Remove a terminal outbox record |
| `sbd check` | Persist a request for one mailbox collection pass |

The worker reacts to ring alerts, outbound exchanges and explicit `check`
requests. Timed polling is disabled. To enable it, use `sbd run --poll-secs 300`;
nonzero intervals must be at least 60 seconds. Enabling local ring reporting
does not itself register on the network: perform an exchange or explicitly
register through the Go/mobile API before relying on alerts.

Temporary failures retry with jittered exponential backoff from one minute to
fifteen minutes. A message receives at most ten transmission attempts per retry
budget. Reconnect failures do not consume attempts. Provisioning and unknown
network errors pause the worker. Restart `run` after addressing the error.

Uncertain attempts retry automatically, including after a crash. **Duplicates
are possible.** Include an application identifier in your own payload if the
receiver needs duplicate suppression; satlink adds no bytes to the payload.
`accepted` means acceptance by the Iridium gateway, not end-to-end delivery.

Ctrl-C stops scheduling and lets an active session finish. A second interrupt,
after two seconds, exits immediately. The persisted active attempt will be
retried as uncertain on restart. Cancellation cannot recall a transmitted
message. The queue retains terminal records until purged and inbox data until
acknowledged; its default capacity is 1,000 records or 16 MiB of payloads.

If the phone contains an unexpected outgoing buffer, the worker copies it into
an `orphaned` outbox record and pauses. Use `retry ID` to adopt it or `cancel ID`
to discard it, then restart the worker. No orphan is transmitted automatically.

## Go and mobile APIs

`iridium.Handset.AcquireSBD` returns an exclusive client for buffer operations,
registration, ring configuration and sessions. Close the client to release
ownership. `Handset.ExchangeSBD` provides an atomic one-shot exchange; incoming
data remains on the handset until explicitly cleared after processing.

Use `SBDProfile` for known voice handsets. For another device, supply explicit
`SBDCapabilities`, including whether its binary read emits a trailing final
result. The 9575 tested here emits `OK`; the reader also supports resultless
binary framing when `ReadFinalResult` is false. A payload never passes through
the AT line parser. Raw commands that enter SBD data modes or start network sessions are refused;
use the typed operations for those commands.

A partial write/read or uncertain session quarantines that `Handset` object.
Subsequent commands are refused, including initialisation and data calls. Close
it; allow the device's SBD operation to finish or reset it before opening a new
connection. Do not use `+++`, ESC or ATH as an assumed SBD session abort.

Package `sbd` exposes a durable `Queue` and a `Manager`. `Run(context)` owns and
closes handsets returned by its connector. The connector must return a fresh,
initialised connection; transport failures reconnect. `Stop` requests graceful
shutdown. Observe `Status` for worker state, and use `Events` or `TakeEvents` for
unrelated unsolicited handset events.

Mobile bindings provide `SBDSettings`, one-shot handset operations,
`SBDQueue`, typed inbox/outbox lists and `SBDManager`. Start blocking work off
the UI thread. Supply application-private storage and an `SBDTransportFactory`
that opens a fresh USB connection and releases it after use. The host controls
USB permission and background execution. The worker does not survive app
suspension by itself. Desktop apps may use `NewSerialSBDManager`.

## Validation status

On 1 November 2026, a USB-connected **9575 on firmware DB24002** passed:

- SBD buffer-status queries.
- A 26-byte text loopback.
- A 1,823-byte binary loopback containing every byte value and strings
  resembling AT results and ring alerts.
- A 1,960-byte local loopback with the explicit local receive-limit override.
- Local buffer cleanup, with both buffers confirmed empty afterwards.

The phone returns a descriptive `SBDTC` success line and a trailing `OK` after
binary reads. Regression tests cover those forms and the documented numeric
loopback/resultless-read forms.

| Capability | Simulator | Real 9575 |
|---|---|---|
| Binary framing, checksum, loopback | Tested | Tested |
| Outbound/inbound satellite messages | Tested | Awaiting provisioned endpoint |
| Ring alerts and multi-session receive | Tested | Unverified |
| Lost-result retry and restart recovery | Tested | Unverified |
| Other handset models | Profiles only | Unverified |

Software checks do not replace satellite acceptance. Complete hardware
acceptance by recording endpoint receipt of an outbound payload, delivering an
inbound payload, testing alerts/queued messages, and exercising loss of signal
and process restart. Record firmware, sequence/status metadata and timestamps;
keep subscriber identifiers and payload contents out of routine logs.

Protocol references: [Iridium AT reference](https://www.latinsatelital.com/mwdownloads/download/link/id/36),
[Extreme USB data services](https://www.europasatellite.com/mwdownloads/download/link/id/111#page=50).

Binding generation and Go target-build checks run in the automated suite.
Full Android AAR and Apple framework packaging requires platform SDKs; those
SDK builds were unavailable on this Linux host.
