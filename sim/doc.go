// Package sim is a simulated Iridium 9555/9575 handset, for developing and
// testing without hardware or airtime.
//
// [Handset] implements iridium.Transport, so whatever runs over a real
// handset's serial port runs over it unchanged:
//
//   - AT commands: identity, signal, battery, registration and clock
//     queries, with echo and verbose result codes as on the real handset.
//   - SMS in text mode: sending with AT+CMGS, and a SIM inbox that
//     [Control.ReceiveSMS] fills, announced with +CMTI.
//   - Calls: voice calls are accepted, without audio. Data calls connect
//     after the [LinkProfile]'s setup time. +++ with guard times switches to
//     online-command mode, and ATO back. AT&D2, DTR and AT+CPAS behave as on
//     the handset.
//   - Iridium's gateway behind each data call: a PPP server, with optional
//     PAP or CHAP, that assigns addresses and DNS servers.
//     [Control.RestartPPP] makes it start over with a new address.
//   - A tiny internet. DNS knows echo.test, web.test and secure.test. Every
//     IPv4 address but the configured silent ones answers echo on TCP and
//     UDP port 7, discard on TCP port 9, a web page on TCP port 80, and DNS
//     on UDP port 53. On TCP port 443 it answers echo inside TLS as
//     secure.test, so that TLS clients can be tested end to end.
//     [NetworkConfig] sets the addresses, the DNS servers and the gateway's
//     MRU.
//   - The satellite link: latency and a throughput cap in each direction,
//     from [InstantLinkProfile], the default, for fast tests, to
//     [IridiumLinkProfile]'s real figures.
//
// [Handset.Control] plays the network's part: [Control.ReceiveSMS],
// [Control.DropCall], [Control.RestartPPP] and [Control.Stats].
//
//	simulator := sim.New(sim.DefaultConfig())
//	control := simulator.Control()
//	handset := iridium.NewHandset(simulator)
//	if err := handset.Initialize(); err != nil {
//		return err
//	}
//	bars, err := handset.SignalQuality() // 4
//	...
//	control.ReceiveSMS("+881631234567", "Hello from orbit")
//	inbox, err := handset.ListSMS(iridium.SMSFilterUnread)
//	// inbox[0].Text == "Hello from orbit"
//
// # Timing
//
// Time is real time. The simulation advances whenever the handset is read
// or written, and a read waits for simulated traffic as it would for a real
// handset. Over [IridiumLinkProfile] with satnet's default tuning:
//
//	Bringing the link up         about 27 s (20 s of call setup, then PPP)
//	A DNS lookup                 2 s
//	A TCP connect                2 s
//	An HTTP/1.0 GET of web.test  2.6 s
//	A full TLS handshake         about 2.2 KB, 8 s
//	A resumed TLS handshake      about 1.1 KB
//	Hanging up                   1.5 s
//
// # TLS
//
// The certificate for secure.test comes from a test CA, [SecureTestCAPEM],
// which no client trusts by default: trust it in tests, never in production.
// The test certificates are valid from 2026-10-19 03:17 UTC, so on a
// machine whose clock is behind that, TLS tests fail. testdata/README.md
// says how to replace them.
//
// # Releasing the handset
//
// Close the handset as a serial port is closed. It drops DTR, which ends a
// call once the app has asked for that with AT&D2. A handset nobody refers
// to any more drops DTR too, once the garbage collector notices. Each
// session of the TLS echo runs crypto/tls in a goroutine of its own. It ends
// with the session, with its call, or once nothing refers to the handset or
// any of its Controls any more.
package sim
