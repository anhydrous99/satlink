package wire

import (
	"fmt"
	"net/netip"
)

// IPProtocol is the protocol an IP datagram carries: its IPv4 Protocol field.
// Values without a constant here are valid too; the stack answers them with
// an ICMP protocol-unreachable error.
type IPProtocol uint8

// The protocols the stack handles.
const (
	// IPProtocolICMP is the Internet Control Message Protocol (RFC 792).
	IPProtocolICMP IPProtocol = 0x01
	// IPProtocolTCP is the Transmission Control Protocol (RFC 9293).
	IPProtocolTCP IPProtocol = 0x06
	// IPProtocolUDP is the User Datagram Protocol (RFC 768).
	IPProtocolUDP IPProtocol = 0x11
)

// The protocols the stack names although it does not handle them
// (IANA's protocol numbers). They matter only for how a datagram that
// carries one is shown.
const (
	ipProtocolHopByHop  IPProtocol = 0x00 // IPv6 hop-by-hop options (RFC 8200)
	ipProtocolIGMP      IPProtocol = 0x02 // group management (RFC 3376)
	ipProtocolIPv6Route IPProtocol = 0x2b // IPv6 routing header (RFC 8200)
	ipProtocolIPv6Frag  IPProtocol = 0x2c // IPv6 fragment header (RFC 8200)
	ipProtocolIPsecESP  IPProtocol = 0x32 // encapsulating security payload (RFC 4303)
	ipProtocolIPsecAH   IPProtocol = 0x33 // authentication header (RFC 4302)
	ipProtocolICMPv6    IPProtocol = 0x3a // ICMP for IPv6 (RFC 4443)
	ipProtocolIPv6NoNxt IPProtocol = 0x3b // no next header for IPv6 (RFC 8200)
	ipProtocolIPv6Opts  IPProtocol = 0x3c // IPv6 destination options (RFC 8200)
)

// ipProtocolNames are the names of the protocols the stack knows.
var ipProtocolNames = map[IPProtocol]string{
	ipProtocolHopByHop:  "Hop-by-Hop",
	IPProtocolICMP:      "ICMP",
	ipProtocolIGMP:      "IGMP",
	IPProtocolTCP:       "TCP",
	IPProtocolUDP:       "UDP",
	ipProtocolIPv6Route: "IPv6-Route",
	ipProtocolIPv6Frag:  "IPv6-Frag",
	ipProtocolIPsecESP:  "IPsec-ESP",
	ipProtocolIPsecAH:   "IPsec-AH",
	ipProtocolICMPv6:    "ICMPv6",
	ipProtocolIPv6NoNxt: "IPv6-NoNxt",
	ipProtocolIPv6Opts:  "IPv6-Opts",
}

// String returns the protocol's name ("TCP", "IPsec-ESP"), or its number in
// hexadecimal ("0x2f") when it has no name.
func (p IPProtocol) String() string {
	if name, ok := ipProtocolNames[p]; ok {
		return name
	}
	return fmt.Sprintf("0x%02x", uint8(p))
}

// ListenEndpoint is the local end a socket listens on or binds to. Unlike a
// netip.AddrPort, its address may be left open.
type ListenEndpoint struct {
	// Addr is the local address; the zero Addr stands for every address of
	// the interface.
	Addr netip.Addr
	// Port is the local port; 0 is not a usable port.
	Port uint16
}

// IsSpecified reports whether both the address and the port are set.
func (e ListenEndpoint) IsSpecified() bool {
	return e.Addr.IsValid() && e.Port != 0
}

// String formats the endpoint as "address:port", or "*:port" when the address
// is open.
func (e ListenEndpoint) String() string {
	if !e.Addr.IsValid() {
		return fmt.Sprintf("*:%d", e.Port)
	}
	return netip.AddrPortFrom(e.Addr, e.Port).String()
}

// ipv4AddrLen is the length of an IPv4 address, in octets.
const ipv4AddrLen = 4

// ipv4Octets returns the four octets of addr, for a function that writes the
// address into a packet or includes it in a checksum. It panics unless addr
// is an IPv4 address (Is4): the sockets refuse other addresses where they
// enter the stack, so one that reaches the wire is a programming error.
func ipv4Octets(addr netip.Addr) [ipv4AddrLen]byte {
	requireIPv4(addr)
	return addr.As4()
}

// requireIPv4 panics unless addr is an IPv4 address (Is4), as ipv4Octets
// does. IPv4-mapped IPv6 addresses are refused too: the stack compares
// addresses with ==, so they must be unmapped before they enter it.
func requireIPv4(addr netip.Addr) {
	if !addr.Is4() {
		panic(fmt.Sprintf("wire: %v is not an IPv4 address", addr))
	}
}
