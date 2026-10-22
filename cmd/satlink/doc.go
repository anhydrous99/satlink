// Command satlink talks to an Iridium satellite handset over USB. It reads
// what the handset reports about itself, sends and reads SMS, places voice
// calls and sends raw AT commands. It can also bring up an IP link over a
// data call that only this program can use.
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
// The handset is found by Iridium's USB vendor ID. Use --port /dev/ttyACM0
// (or SATLINK_PORT) when more than one is attached. The net commands only
// allow their own target; add more with --allow host:port.
//
// Ctrl-C during call setup cancels the dial, and once the call is up it
// hangs up properly. If a hang-up could not be confirmed, satlink says so:
// the call may still be billing. End it with satlink hangup, or
// power-cycle the handset.
//
// Every command shows a summary of what it takes with -h, and more with
// --help or satlink help, as in satlink help net get. The exit status is 0
// on success, 1 when the command failed, 2 when the command line is wrong,
// 101 when satlink panicked, and death by SIGINT (130 on Windows) once
// interrupted.
//
// docs/cli.md in the repository is the user guide: limits, signals,
// Windows, and what each net command guarantees.
package main
