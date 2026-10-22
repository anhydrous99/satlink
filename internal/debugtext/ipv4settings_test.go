package debugtext_test

import (
	"net/netip"
	"testing"

	"github.com/anhydrous99/satlink/internal/debugtext"
	"github.com/anhydrous99/satlink/ppp"
)

func TestIPv4SettingsAreWrittenAsDebugStructs(t *testing.T) {
	local, peer := netip.MustParseAddr("10.64.0.2"), netip.MustParseAddr("10.64.0.1")
	primary, secondary := netip.MustParseAddr("10.64.0.1"), netip.MustParseAddr("10.64.0.3")
	tests := []struct {
		name     string
		settings ppp.IPv4Settings
		want     string
	}{
		{
			name:     "a peer address and one DNS server, as the simulator's gateway gives",
			settings: ppp.IPv4Settings{LocalAddress: local, PeerAddress: peer, DNSServers: []netip.Addr{primary}},
			want:     "Ipv4Settings { local_address: 10.64.0.2, peer_address: Some(10.64.0.1), dns_servers: [10.64.0.1] }",
		},
		{
			name:     "two DNS servers, primary first",
			settings: ppp.IPv4Settings{LocalAddress: local, PeerAddress: peer, DNSServers: []netip.Addr{primary, secondary}},
			want: "Ipv4Settings { local_address: 10.64.0.2, peer_address: Some(10.64.0.1), " +
				"dns_servers: [10.64.0.1, 10.64.0.3] }",
		},
		{
			name:     "no peer address and no DNS server",
			settings: ppp.IPv4Settings{LocalAddress: local},
			want:     "Ipv4Settings { local_address: 10.64.0.2, peer_address: None, dns_servers: [] }",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := debugtext.IPv4Settings(test.settings.LocalAddress, test.settings.PeerAddress, test.settings.DNSServers); got != test.want {
				t.Errorf("IPv4Settings = %q, want %q", got, test.want)
			}
		})
	}
}
