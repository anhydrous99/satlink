// Command satlink talks to an Iridium satellite handset over USB: what the
// handset reports about itself, SMS, voice calls and raw AT commands, and an
// IP link over a data call that only this program can use.
//
//	satlink list                      # find handsets
//	satlink info                      # model, firmware, IMEI
//	satlink status                    # signal bars, battery, registration
//	satlink sms send +881631234567 "hello from the field"
//	satlink sms list unread
//	satlink at 'AT+CSQ'               # anything else, raw
//	satlink monitor                   # print incoming SMS / RING events
//
//	satlink net status                # dial, show the negotiated addresses, hang up
//	satlink net resolve example.com
//	satlink net get https://example.com/
//	satlink net tcp example.com 7     # like netcat
//
//	satlink --simulate net get http://web.test/          # no hardware: realistic timing
//	satlink --simulate=instant -v net tcp echo.test 7    # no hardware, no waiting, with logs
//
// The handset is found by Iridium's USB vendor ID; use --port /dev/ttyACM0
// (or SATLINK_PORT) when more than one is attached. On Linux, install
// linux/99-satlink.rules, so that no root is needed and ModemManager leaves
// the port alone.
//
// The net commands only allow their own target; add more with --allow
// host:port. Limits: --idle-secs, --max-minutes, --max-kb. Everything is
// checked before anything is dialled — a limit too small to be of use (an
// idle time under 25 s, a data volume under 2 KiB), a wildcard or network
// such as *.example.com or 10.0.0.0/8 as a target, a URL with spaces — and
// net resolve of an IPv4 address dials nothing.
//
// net get allows 10 minutes for the request and its whole response, stops
// reading once the body is as long as announced, hangs up as soon as the
// response is in — a slow reader such as a pager does not keep the call up
// — and exits non-zero when the response may be incomplete, printing what
// arrived even when the call dropped or a limit ended it. net tcp exits
// non-zero if standard input could not all be delivered — the remote end
// closing first, or resetting what arrived after it closed, included,
// unless standard input is a terminal — and resets the connection when
// standard input cannot be read or sent, so the far end cannot take a part
// for the whole.
//
// Ctrl-C, SIGTERM or SIGHUP during call setup cancels it: the dial is
// aborted rather than left to connect and bill first. Once the call is up,
// it hangs up properly. Either way satlink then ends by SIGINT, so that a
// shell loop or script around it stops too. Another one, a few seconds
// after the first, quits at once, leaving the call to the DTR drop, and
// says so; a SIGHUP or SIGINT satlink was started with ignored, as under
// nohup or in a background job, stays ignored. On Windows, Ctrl-C and
// Ctrl-Break do the same, and satlink then exits with status 130, as there
// is no SIGINT to end by; closing the console window, logging off or
// shutting down starts the hang-up too, but Windows ends satlink a few
// seconds later whatever it is doing. Suspending satlink with Ctrl-Z
// suspends its limits too. Log lines (-v) that standard error cannot take at
// once wait, or are dropped and counted, rather than hold the link up. If a
// hang-up could not be confirmed — after the call, or when setting it up
// failed — satlink says so: end the call with satlink hangup, or
// power-cycle the handset.
//
// Every command shows a summary of what it takes with -h, and more with
// --help or satlink help, as in satlink help net get. The command line is
// read as clap reads it, and its errors and help are clap's.
//
// The exit status is 0 on success, 1 when the command failed, 2 when the
// command line is wrong, 101 when satlink panicked, and 130 — or death by
// SIGINT — once interrupted. A panic is a bug, reported on standard error;
// one while the link is up hangs up first and fails the command.
package main
