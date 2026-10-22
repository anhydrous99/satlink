// Package ppp is a sans-I/O PPP endpoint for dial-up data links.
//
// [Endpoint] speaks PPP in HDLC-like framing (RFC 1661, RFC 1662) with what
// a dial-up client needs — LCP with dead-peer detection, PAP and CHAP-MD5
// authentication (RFC 1334, RFC 1994), and IPCP with DNS server discovery
// (RFC 1332, RFC 1877). It can also play the answering side, which stands in
// for an ISP in tests and serves handset-to-handset links.
//
// The endpoint never does I/O and never reads the clock: its owner passes in
// received bytes and the current time, and writes out what comes back. That
// keeps it portable and makes every timer path testable.
//
//	credentials := ppp.Credentials{Username: "user", Password: "secret"}
//	endpoint, err := ppp.NewEndpoint(ppp.NewConfig(ppp.ClientRole(credentials)))
//	if err != nil {
//		return err
//	}
//	endpoint.Open(time.Now())
//	toModem := endpoint.TakeOutput() // a framed LCP Configure-Request
//
// Errors are [*ConfigError] and [*SendError] values; each of their kind
// constants is also a sentinel for errors.Is.
package ppp
