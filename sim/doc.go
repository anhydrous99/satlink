// Package sim is a simulated Iridium 9555/9575 handset, for developing and
// testing without hardware or airtime.
//
// [Handset] implements satlink.Transport, so whatever runs over a real
// handset's serial port runs over it unchanged:
//
//   - AT commands: identity, signal, battery, registration and clock
//     queries, with echo and verbose result codes as on the real handset.
//   - SMS in text mode: sending with AT+CMGS, and a SIM inbox that
//     [Control.ReceiveSMS] fills, announced with +CMTI.
//   - Calls: voice calls are accepted (without audio); data calls connect
//     after the [LinkProfile]'s setup time, and +++ with guard times
//     switches to online-command mode and back with ATO.
//   - A network behind each data call: a PPP server (with optional PAP or
//     CHAP) assigns addresses and DNS, and every IPv4 address but the
//     configured silent ones answers echo on TCP and UDP port 7, discard on
//     TCP port 9, a web page on TCP port 80, DNS on UDP port 53, and echo
//     inside TLS on TCP port 443 as secure.test, with a certificate from a
//     test CA ([SecureTestCAPEM]), so that TLS clients can be tested end to
//     end (see [NetworkConfig]).
//   - The satellite link: latency and a throughput cap in each direction,
//     from [InstantLinkProfile] for fast tests to [IridiumLinkProfile]'s real
//     figures.
//
// Time is real time: the simulation advances whenever the handset is read
// or written, and a read waits for simulated traffic as it would for a real
// handset.
//
// Close the handset as a serial port is closed: it drops DTR, which ends a
// call once the app has asked for that with AT&D2; a handset nobody refers
// to any more drops DTR too, once the garbage collector notices. Each
// session of the TLS echo runs crypto/tls in a goroutine of its own, which
// ends with the session, with its call, or once nothing refers to the
// handset or any of its Controls any more.
//
//	simulator := sim.New(sim.DefaultConfig())
//	control := simulator.Control()
//	handset := satlink.NewHandset(simulator)
//	if err := handset.Initialize(); err != nil {
//		return err
//	}
//	bars, err := handset.SignalQuality() // 4
//	...
//	control.ReceiveSMS("+881631234567", "Hello from orbit")
//	inbox, err := handset.ListSMS(satlink.SMSFilterUnread)
//	// inbox[0].Text == "Hello from orbit"
package sim
