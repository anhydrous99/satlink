# Iridium 9575 hardware tests

This report records the 1 November 2026 tests of a connected 9575 on Linux,
the faults found, their fixes and what still needs satellite service.

## Environment

- USB `1edd:9575`, Linux `cdc_acm`, nominal baud rate 19200.
- Model `9575`; `AT+CGMR` reported `Call Processor Version: DB24002`.
- Follow-up queries identified combined firmware `HL25003` and application
  version `13578033_3`.
- Linux amd64, Go 1.27.3. Signal remained 0/5 and registration Unknown.
- The SIM initially held 21 messages out of 50 slots. All 21 were preserved.

## Fault and recovery

The handset stopped answering AT commands and instead returned a single
ASCII `N` (`0x4e`) for each write, even a single character without a command
terminator. USB captures showed complete outgoing commands and successful
incoming transfers carrying only that byte. The same reply came through
direct libusb transfers with `cdc_acm` detached. Changing baud rate, DTR/RTS
states, flushing buffers and ordinary AT reset commands did not clear it.

Offline inspection of the vendor's
[HL25003 updater package](https://support.satellitephonestore.com/hc/en-us/article_attachments/45905458613652)
found its explicit detection of “Echoing N to all characters”. Its auxiliary
controller protocol uses `P` to ping and `R` to reboot, acknowledging each
with `Y`. The affected handset acknowledged both. Rebooting that controller
caused USB re-enumeration and restored normal AT replies. No firmware was
flashed and the Windows updater was not executed.

The underlying software fault was reproduced under syscall tracing:

```text
write(fd, "AT+CMGF=0\r", 10) = 10
ioctl(fd, TCSBRK, 1) = -1 EINTR
```

Go's `SIGURG` interrupted the drain after the command had already been
written. `serial.Transport.WriteAll` returned the interruption as a transport
failure. During optional initialisation this left the command's answer
unread, allowing it to stand in for the next command's answer. One observed
result was `AT+CGMI returned no data`.

Three deliberately interrupted early sessions in the old build each failed
and closed within about 31 ms. Their rapid open/close transitions produced a
delayed `Y`, followed by the `N`-reply controller mode; `P` and `R` again
acknowledged with `Y`. The vendor's controller-entry routine also uses rapid
DTR transitions. This reproduces the connection between early serial
failures and the controller mode, rather than attributing it to `AT+CNMI?`.

`WriteAll` now retries interrupted writes while retaining their progress,
and retries an interrupted drain without writing the command again. Two
regression tests failed before this fix and pass afterwards.

Hardware verification of the fix:

- The old build failed on the fourth traced open/read/close cycle.
- The fixed build passed all 30 cycles. Three naturally interrupted drains
  were retried successfully during that run.
- The fixed build also passed the same three injected early interruptions;
  the subsequent raw `AT` received `OK`, with no controller mode entry.
- `AT+CNMI?` then returned its settings normally.

## Linux access

The installed `99-satlink.rules` added `uaccess` after systemd's
`73-seat-late.rules` had already checked for it. Renaming the project rule
to `70-satlink.rules` fixes that ordering. The rule also grants direct USB
access for diagnostics, restricted to Iridium's vendor ID.

The corrected rule was installed and applied. Both the serial and USB
device nodes then granted the active user's account read/write access.
Subsequent tests ran without sudo. The [CLI guide](cli.md#install) describes
installation and access for sessions without an active desktop.

## Hardware results

| Check | Result |
|---|---|
| Discovery and identity | Pass |
| Last signal, battery, registration | Pass: 0/5, 99%, Unknown |
| Clock query | Responds, but the value is frozen and unusable as current time; see the follow-up below |
| Error reporting and SMS PDU mode | Pass: `+CMEE:002`, `+CMGF:000` |
| Data bearer query and capability list | Pass |
| SMS list and individual reads | Pass: all 21 messages decoded; reads agreed with listing |
| Unread/read/unsent/sent filters | Pass: 0 / 10 / 0 / 11 after listing |
| Temporary GSM-7 and UCS-2 SMS storage/read/delete | Pass; test messages deleted and all originals compared unchanged |
| Consecutive commands in one session | Pass: 100 model queries |
| Concurrent callers | Pass: 40 battery queries across four goroutines, race detector enabled |
| Unsolicited-event polling while idle | Pass: returned no events; incoming delivery unverified |
| Hang-up while idle | Pass |
| DTR drop/raise while idle | Pass; subsequent AT command answered |
| Recovery from abandoned SMS entry | Pass: reopening aborted the partial PDU and restored identity queries |
| Data-call setup without service | `NO DIALTONE`; AT commands worked afterwards |
| Dial cancellation | Unverified: `NO DIALTONE` arrived before cancellation could take effect |
| Subscriber identity (`AT+CIMI`) | Unavailable through this command on the tested firmware; see the follow-up below |
| SIM SMS centre (`AT+CSCA?`) | SIM has no known centre; the library's explicit PDU fallback was verified on hardware |

Successful SMS transmission/reception, connected voice/data calls, PPP,
DNS, TCP, UDP, HTTP, TLS and hanging up a connected call through DTR remain
unverified on this handset because it had no signal. Those software paths
passed simulator tests. Fresh signal acquisition (`AT+CSQ`) was not tested;
status uses the cached reading (`AT+CSQF`).

The final handset state was responsive, `+CPAS:000` (idle), with 21/50 SMS
slots occupied. No test SMS was submitted to the network.

## Follow-up: subscriber identity, SMS centre and clock

Further checks on the same handset distinguish firmware limitations and
SIM configuration from satlink parsing errors. The relevant reference is
Iridium's [ISU AT Command Reference, MAN0009 version 5](https://www.europasatellite.com/gr-eu/mwdownloads/download/link/id/36).

### Subscriber identity

`AT+CIMI`, `AT+CIMI?` and `AT+CIMI=?` all returned `+CME ERROR: 100` in
numeric error mode. Verbose mode renders this as `unknown`. This is the
handset's response, rather than an error introduced by the library.

The SIM was readable: `AT+CICCID` returned a 19-digit card identifier.
Queries reported SIM PIN locking and fixed dialling disabled (`+CLCK:0`
for both `SC` and `FD`). The `SIM PIN2` response to `AT+CPIN?` is not itself
evidence that PIN1 is missing: section 5.82 of the reference explicitly
describes this response as also covering a ready SIM with no PIN1 required.

The reference defines `+CICCID` (section 5.53), but does not define `+CIMI`
or a generic SIM-file read command. Taken with the hardware responses,
this indicates that the standard IMSI query is unavailable on this
firmware. No documented alternative IMSI query was found. `AT+CICCID` is
usable for identifying the SIM, but its result is an ICCID, not an IMSI.
The library preserves the equipment error, and its API comments now state
the firmware requirement.

### SMS centre

Section 5.66 defines error 330 as an unknown SMS service-centre address.
Section 5.91 states that the configured centre is used in PDU mode only
when the PDU's own centre-address length is zero. Satlink already supplies
an explicit centre address, falling back to `+881662900005` when this SIM
cannot name one. No SIM configuration change is needed for that path.

To verify the actual hardware path, a transport wrapper intercepted the
outgoing PDU before any of its bytes or its submission terminator could
reach USB. The real `SendSMS` method queried the handset, handled error
330 and reached the real SMS-entry prompt. Its captured PDU began with
`0791886126090050`, the explicit address of the fallback centre. Only ESC
was sent after the prompt, cancelling entry. Reinitialisation succeeded,
and storage stayed at 21/50 before and after the check.

This verifies the fallback through PDU construction on the handset. Actual
network submission and delivery still require signal.

### Clock

Repeated reads ten seconds apart returned the identical
`14/05/11,14:23:52`. `AT-MSSTM` reported `no network service`, so the handset
had no valid network time. An attempt to set the clock to current UTC with
the documented `AT+CCLK` setter returned `+CME ERROR: operation not allowed`;
the reading remained unchanged.

Section 5.42 deprecates `+CCLK` and warns that epoch changes can make its
UTC conversion incorrect. Section 5.179 describes the 2014 epoch as
11 May 2014, 14:23:55 UTC, close to the frozen value seen here. This is
consistent with an uninitialised legacy network-time conversion, rather
than a functioning local wall clock. The setter cannot repair it on the
tested firmware in its current state.

The Go and mobile API comments now identify this limitation and no longer
promise a time-zone suffix. Applications should use their host clock for
timestamps. No fixed offset or artificial time is added to the handset's
raw reply.

### Firmware data restriction

The combined version query confirmed that this handset runs `HL25003`.
Section 4.1 of Iridium's
[HL25003 release notes](https://support.satellitephonestore.com/hc/en-us/article_attachments/45905458617876)
states that Direct Internet is no longer available and gives the display
message `Dial-Up Internet Restricted`. The data-network caveat therefore
includes a firmware restriction as well as the current lack of signal.
The effect on provider-specific circuit-switched data numbers remains
untested; the observed `NO DIALTONE` alone cannot establish its cause.

## Software checks

`go test -race -cover ./...` passed all 26 packages: 4,208 passing test and
subtest results, with no failures. Five results were skipped: unavailable
macOS tooling, Java compilation without a JDK, and a child-process-only test.

Formatting, `go vet`, staticcheck, the 32-bit atomic-alignment check,
gocyclo's complexity limit, `git diff --check` and udev rule verification
also passed. The test run used the pinned mobile binding tool after its
dependencies had been downloaded.

These suite results precede concurrent SBD feature edits that appeared in
the checkout during final review; they do not certify those additions.

For this follow-up, `go test -race ./iridium ./mobile ./sim` and `go vet`
for those packages passed. Hardware checks verified the gated SMS-centre
fallback, the rejected clock setter, an idle call state and the unchanged
21/50 SMS count. API comments were corrected to describe the firmware
requirements and legacy clock behaviour; protocol behaviour was preserved.
