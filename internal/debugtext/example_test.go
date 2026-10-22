package debugtext_test

import (
	"fmt"
	"net/netip"

	"github.com/anhydrous99/satlink/internal/debugtext"
	"github.com/anhydrous99/satlink/ppp"
)

func ExampleIPv4Settings() {
	settings := ppp.IPv4Settings{
		LocalAddress: netip.MustParseAddr("10.64.0.2"),
		PeerAddress:  netip.MustParseAddr("10.64.0.1"),
		DNSServers:   []netip.Addr{netip.MustParseAddr("10.64.0.1")},
	}
	fmt.Println("IPv4 up: " + debugtext.IPv4Settings(settings.LocalAddress, settings.PeerAddress, settings.DNSServers))
	// Output: IPv4 up: Ipv4Settings { local_address: 10.64.0.2, peer_address: Some(10.64.0.1), dns_servers: [10.64.0.1] }
}

func ExampleQuote() {
	fmt.Println(debugtext.Quote("http://web.test/a\vb") + " is not a valid http:// or https:// URL")
	// Output: "http://web.test/a\u{b}b" is not a valid http:// or https:// URL
}
