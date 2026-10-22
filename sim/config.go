package sim

import (
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/anhydrous99/satlink/ppp"
)

// What the simulated handset reports about itself, and how its satellite
// link and the network behind the gateway behave.

// defaultModel is a current Iridium handset, as AT+CGMM names it.
const defaultModel = "9575"

// defaultIMEI is a made-up IMEI shaped like those of Iridium devices.
const defaultIMEI = "300234010000000"

// defaultSignalBars is good reception, though not a perfect view of the
// sky.
const defaultSignalBars = 4

// defaultEscapeGuard is the handsets' factory S12 setting: 50 fiftieths of
// a second.
const defaultEscapeGuard = time.Second

// defaultWebPage is the body the web service serves on TCP port 80.
const defaultWebPage = "Hello from the simulated Iridium network!\n"

var (
	// defaultGatewayAddress is the gateway's address on the PPP link; it
	// is also the DNS server IPCP offers by default.
	defaultGatewayAddress = netip.AddrFrom4([4]byte{10, 64, 0, 1})
	// defaultClientAddress is the address the gateway hands to the app
	// through IPCP.
	defaultClientAddress = netip.AddrFrom4([4]byte{10, 64, 0, 2})
)

// defaultDNSServers returns the DNS servers IPCP offers by default: the
// gateway alone; a fresh slice each time.
func defaultDNSServers() []netip.Addr {
	return []netip.Addr{defaultGatewayAddress}
}

// defaultHosts returns hosts under the reserved .test domain (RFC 2606)
// with addresses from TEST-NET-1 (RFC 5737), so none can be mistaken for a
// real destination; a fresh slice each time.
func defaultHosts() []HostAddress {
	return []HostAddress{
		{Name: "echo.test", Address: netip.AddrFrom4([4]byte{192, 0, 2, 7})},
		{Name: "web.test", Address: netip.AddrFrom4([4]byte{192, 0, 2, 80})},
		// The name on the TLS echo's certificate.
		{Name: "secure.test", Address: netip.AddrFrom4([4]byte{192, 0, 2, 43})},
	}
}

// AuthProtocol is a PPP authentication protocol; its constants are
// ppp.AuthProtocolPAP and ppp.AuthProtocolCHAPMD5.
type AuthProtocol = ppp.AuthProtocol

// Credentials are a PPP name and password.
type Credentials = ppp.Credentials

// Authentication is a PPP protocol and the credentials it checks.
type Authentication = ppp.Authentication

// ConfigError is a configuration the PPP gateway cannot start with, as
// [Control.RestartPPP] reports it.
type ConfigError = ppp.ConfigError

// Config is how the simulated handset presents itself and what its network
// does. Start from [DefaultConfig]. Formatting a Config with fmt never shows
// the password the gateway demands, whatever the verb.
type Config struct {
	// Model is reported by AT+CGMM.
	Model string
	// IMEI is reported by AT+CGSN.
	IMEI string
	// SignalBars is the signal strength, 0 to 5, reported by AT+CSQ and
	// AT+CSQF. Larger values are reported as 5, as no handset shows more;
	// negative ones as 0.
	SignalBars int
	// Link is the delay and throughput of the satellite link during data
	// calls.
	Link LinkProfile
	// Network holds the addresses and names of the network behind the PPP
	// gateway.
	Network NetworkConfig
	// Authentication is what the PPP gateway demands; nil demands nothing.
	// A gateway that cannot start with it — credentials too long for PAP
	// and CHAP, a protocol without a constant — fails the call (NO
	// CARRIER).
	Authentication *Authentication
	// EscapeGuard is the silence required around +++ (register S12); a
	// negative guard counts as zero.
	EscapeGuard time.Duration
}

// DefaultConfig returns a 9575 with good reception (four bars) on an
// instant link, a gateway that asks for no credentials, the default
// [NetworkConfig], and the factory escape guard of one second (S12 = 50
// fiftieths of a second).
func DefaultConfig() Config {
	return Config{
		Model:          defaultModel,
		IMEI:           defaultIMEI,
		SignalBars:     defaultSignalBars,
		Link:           InstantLinkProfile(),
		Network:        DefaultNetworkConfig(),
		Authentication: nil,
		EscapeGuard:    defaultEscapeGuard,
	}
}

// String returns the configuration as %v prints a struct, with the
// credentials the gateway demands redacted.
func (c Config) String() string {
	return fmt.Sprintf("{%v %v %v %v %v %v %v}",
		c.Model, c.IMEI, c.SignalBars, c.Link, c.Network, c.Authentication, c.EscapeGuard)
}

// GoString returns the configuration in Go syntax, with the credentials the
// gateway demands redacted.
func (c Config) GoString() string {
	return fmt.Sprintf("sim.Config{Model:%#v, IMEI:%#v, SignalBars:%#v, Link:%#v, "+
		"Network:%#v, Authentication:%#v, EscapeGuard:%#v}",
		c.Model, c.IMEI, c.SignalBars, c.Link, c.Network, c.Authentication, c.EscapeGuard)
}

// stringWithFieldNames returns the configuration as %+v prints a struct,
// with the credentials the gateway demands redacted.
func (c Config) stringWithFieldNames() string {
	return fmt.Sprintf("{Model:%v IMEI:%v SignalBars:%v Link:%+v Network:%+v Authentication:%+v EscapeGuard:%v}",
		c.Model, c.IMEI, c.SignalBars, c.Link, c.Network, c.Authentication, c.EscapeGuard)
}

// Format prints GoString for %#v, the fields with their names for %+v, and
// String, what %v prints, for every other verb. With String alone, fmt
// would print %+v without the names, quote the text for %q, hex-encode it
// for %x and %X, and print each field with any other verb, such as %d or
// %t, whether the verb fits the field or not (%!t(int=4) for SignalBars
// under %t). As each text formats the Authentication pointer with %v, %+v
// or %#v, fmt always reaches the credentials' redacting methods: no verb
// shows the password.
func (c Config) Format(state fmt.State, verb rune) {
	text := c.String()
	switch {
	case verb == 'v' && state.Flag('#'):
		text = c.GoString()
	case verb == 'v' && state.Flag('+'):
		text = c.stringWithFieldNames()
	}
	// fmt.Formatter has no way to report a failed write, and fmt itself
	// ignores them: the error is dropped on purpose.
	_, _ = io.WriteString(state, text)
}

// LinkProfile is the timing of the satellite link. Delay and throughput
// apply to each direction separately, as on the real link. Negative
// durations count as zero.
type LinkProfile struct {
	// CallSetup is the time from ATD to CONNECT.
	CallSetup time.Duration
	// OneWayDelay is the latency in each direction while connected.
	OneWayDelay time.Duration
	// BytesPerSecond caps the throughput per direction; zero means
	// unlimited, and a negative cap is treated as one byte per second.
	BytesPerSecond int
}

// InstantLinkProfile returns no call setup time, no latency and unlimited
// throughput: for tests that should run as fast as the code under test
// allows.
func InstantLinkProfile() LinkProfile {
	return LinkProfile{CallSetup: 0, OneWayDelay: 0, BytesPerSecond: 0}
}

// IridiumLinkProfile returns measured real-world figures for Iridium
// circuit-switched data: about 20 seconds to connect, 1.5-second round
// trips and 2.4 kbit/s.
func IridiumLinkProfile() LinkProfile {
	return LinkProfile{
		CallSetup:      20 * time.Second,
		OneWayDelay:    750 * time.Millisecond,
		BytesPerSecond: 300,
	}
}

// NetworkConfig is the network behind the PPP gateway. Start from
// [DefaultNetworkConfig].
//
// Every IPv4 address but the SilentAddresses answers with the same
// services: echo on TCP and UDP port 7, discard on TCP port 9 (counted in
// [Stats].DiscardedBytes), WebPage on TCP port 80, DNS on UDP port 53
// resolving Hosts, and echo inside TLS on TCP port 443, presenting a
// certificate for secure.test that verifies against [SecureTestCAPEM].
//
// Every address in it — the gateway's, the client's, the DNS servers' and
// those of Hosts — must be IPv4, and GatewayMRU at least ppp.MinMRU.
// Otherwise the PPP gateway cannot start, which [New] does not refuse:
// data calls then fail with NO CARRIER, and the reason is logged. The zero
// NetworkConfig is such a configuration.
type NetworkConfig struct {
	// GatewayAddress is the PPP server's own address.
	GatewayAddress netip.Addr
	// ClientAddress is the address IPCP assigns to the app.
	ClientAddress netip.Addr
	// DNSServers are the DNS servers IPCP offers, in the order the app
	// should ask them; with none, IPCP offers no DNS server.
	DNSServers []netip.Addr
	// SilentAddresses are the addresses where nothing answers: whatever
	// the app sends them is lost. Only IPv4 addresses receive datagrams,
	// so any other address in it changes nothing.
	SilentAddresses []netip.Addr
	// Hosts are the DNS A records, matched without regard to case or a
	// trailing dot. A name listed more than once resolves to all of its
	// addresses.
	Hosts []HostAddress
	// WebPage is the body served as text/plain on TCP port 80 by every
	// address.
	WebPage string
	// GatewayMRU is the largest packet the gateway accepts: the MRU its PPP
	// asks for.
	GatewayMRU uint16
}

// HostAddress is a DNS A record: a name and one of its addresses.
type HostAddress struct {
	// Name is the host name.
	Name string
	// Address is its IPv4 address.
	Address netip.Addr
}

// DefaultNetworkConfig returns echo.test at 192.0.2.7, web.test at
// 192.0.2.80 and secure.test (the name on the TLS echo's certificate) at
// 192.0.2.43, behind a gateway at 10.64.0.1 that assigns 10.64.0.2, offers
// itself as the DNS server, asks for PPP's default MRU and serves a
// greeting; no address is silent. The hosts are under the reserved .test
// domain (RFC 2606) with addresses from TEST-NET-1 (RFC 5737), so none can
// be mistaken for a real destination.
func DefaultNetworkConfig() NetworkConfig {
	return NetworkConfig{
		GatewayAddress:  defaultGatewayAddress,
		ClientAddress:   defaultClientAddress,
		DNSServers:      defaultDNSServers(),
		SilentAddresses: nil,
		Hosts:           defaultHosts(),
		WebPage:         defaultWebPage,
		GatewayMRU:      ppp.DefaultMRU,
	}
}
