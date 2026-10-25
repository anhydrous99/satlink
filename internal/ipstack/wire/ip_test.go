package wire

import (
	"net/netip"
	"testing"
)

func TestIPProtocolString(t *testing.T) {
	t.Parallel()
	names := map[IPProtocol]string{
		0x00:             "Hop-by-Hop",
		IPProtocolICMP:   "ICMP",
		0x02:             "IGMP",
		IPProtocolTCP:    "TCP",
		IPProtocolUDP:    "UDP",
		0x2b:             "IPv6-Route",
		0x2c:             "IPv6-Frag",
		0x32:             "IPsec-ESP",
		0x33:             "IPsec-AH",
		0x3a:             "ICMPv6",
		0x3b:             "IPv6-NoNxt",
		0x3c:             "IPv6-Opts",
		IPProtocol(0x0c): "0x0c",
		IPProtocol(0x2f): "0x2f",
		IPProtocol(0xff): "0xff",
	}
	for protocol, want := range names {
		if got := protocol.String(); got != want {
			t.Errorf("IPProtocol(%d).String() = %q, want %q", uint8(protocol), got, want)
		}
	}
}

func TestListenEndpointString(t *testing.T) {
	t.Parallel()
	address := netip.MustParseAddr("192.168.1.1")
	cases := map[string]ListenEndpoint{
		"*:0":            {},
		"*:80":           {Port: 80},
		"192.168.1.1:0":  {Addr: address},
		"192.168.1.1:80": {Addr: address, Port: 80},
		"0.0.0.0:53":     {Addr: netip.IPv4Unspecified(), Port: 53},
	}
	for want, endpoint := range cases {
		if got := endpoint.String(); got != want {
			t.Errorf("String() of %+v = %q, want %q", endpoint, got, want)
		}
	}
}

func TestIPv4OctetsRefusesAddressesThatAreNotIPv4(t *testing.T) {
	t.Parallel()
	if got := ipv4Octets(netip.MustParseAddr("10.64.0.2")); got != [4]byte{10, 64, 0, 2} {
		t.Errorf("ipv4Octets(10.64.0.2) = %v", got)
	}
	expectPanicWith(t, "wire: ::ffff:10.64.0.2 is not an IPv4 address",
		func() { ipv4Octets(netip.MustParseAddr("::ffff:10.64.0.2")) })
	expectPanicWith(t, "wire: invalid IP is not an IPv4 address", func() { ipv4Octets(netip.Addr{}) })
}
