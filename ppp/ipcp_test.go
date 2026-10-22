package ppp

import (
	"net/netip"
	"testing"
)

// testServerIPCP returns a server handing out clientAddress with one DNS
// server.
func testServerIPCP() *serverIPCP {
	return newServerIPCP(ServerConfig{
		LocalAddress: serverAddress,
		PeerAddress:  clientAddress,
		DNSServers:   []netip.Addr{dnsServer},
	})
}

// assertSettings fails the test unless got is want.
func assertSettings(t *testing.T, got, want IPv4Settings) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
}

func TestClientAdoptsAssignedAddressAndDNS(t *testing.T) {
	client := newClientIPCP()
	server := testServerIPCP()
	var suggestions []configOption
	for _, proposed := range client.ownOptions() {
		if judged := server.judge(proposed); judged.kind == verdictNak {
			suggestions = append(suggestions, configOption{kind: proposed.kind, value: judged.suggestion})
		}
	}
	if err := client.applyNak(suggestions); err != nil {
		t.Fatal(err)
	}

	var rejected []configOption
	for _, proposed := range client.ownOptions() {
		if server.judge(proposed).kind == verdictReject {
			rejected = append(rejected, proposed)
		}
	}
	assertOptions(t, rejected, []configOption{ipv4Option(ipcpOptionSecondaryDNS, netip.IPv4Unspecified())})
	if err := client.applyReject(rejected); err != nil {
		t.Fatal(err)
	}
	client.acceptPeerOptions([]configOption{ipv4Option(ipcpOptionIPAddress, serverAddress)})

	for _, proposed := range client.ownOptions() {
		assertVerdict(t, server.judge(proposed), ackVerdict())
	}
	settings, err := client.settings()
	if err != nil {
		t.Fatal(err)
	}
	assertSettings(t, settings, IPv4Settings{
		LocalAddress: clientAddress,
		PeerAddress:  serverAddress,
		DNSServers:   []netip.Addr{dnsServer},
	})
}

func TestClientCannotWorkWithoutAnAddress(t *testing.T) {
	client := newClientIPCP()

	if _, err := client.settings(); err == nil {
		t.Error("settings without an assigned address succeeded")
	}
	if err := client.applyReject([]configOption{ipv4Option(ipcpOptionIPAddress, netip.IPv4Unspecified())}); err == nil {
		t.Error("a rejected address was accepted")
	}
}

func TestClientRefusesAddressesNoHostCanHave(t *testing.T) {
	for _, unusable := range []string{"255.255.255.255", "224.0.0.1", "127.0.0.1"} {
		t.Run(unusable, func(t *testing.T) {
			address := netip.MustParseAddr(unusable)
			client := newClientIPCP()

			if err := client.applyNak([]configOption{ipv4Option(ipcpOptionIPAddress, address)}); err != nil {
				t.Fatal(err)
			}
			if client.address != netip.IPv4Unspecified() {
				t.Errorf("address = %v after a Nak suggesting %v, want 0.0.0.0", client.address, address)
			}
			client.address = address // as if the peer had Acked it anyway
			_, err := client.settings()

			if err == nil || err.Error() != "the peer assigned "+unusable+", which is not a host address" {
				t.Errorf("settings error = %v, want the not-a-host failure", err)
			}
		})
	}
}

func TestClientKeepsOnlyDNSServersThatAreHostAddresses(t *testing.T) {
	client := newClientIPCP()
	// The peer suggests an address and leaves the DNS servers we ask for
	// as 0.0.0.0, which it may then acknowledge.
	if err := client.applyNak([]configOption{ipv4Option(ipcpOptionIPAddress, clientAddress)}); err != nil {
		t.Fatal(err)
	}

	settings, err := client.settings()

	if err != nil {
		t.Fatal(err)
	}
	if len(settings.DNSServers) != 0 {
		t.Errorf("DNS servers = %v, want none", settings.DNSServers)
	}
}

func TestClientRejectsCompressionAndUnaddressedPeers(t *testing.T) {
	client := newClientIPCP()
	vanJacobson := configOption{kind: 2, value: []byte{0x00, 0x2D, 0x0F, 0x01}}

	assertVerdict(t, client.judge(vanJacobson), rejectVerdict())
	assertVerdict(t, client.judge(ipv4Option(ipcpOptionIPAddress, netip.IPv4Unspecified())), rejectVerdict())
	assertVerdict(t, client.judge(ipv4Option(ipcpOptionIPAddress, serverAddress)), ackVerdict())
}

func TestClientIgnoresSuggestionsItCannotUse(t *testing.T) {
	client := newClientIPCP()
	if err := client.applyReject([]configOption{ipv4Option(ipcpOptionSecondaryDNS, netip.IPv4Unspecified())}); err != nil {
		t.Fatal(err)
	}

	err := client.applyNak([]configOption{
		{kind: ipcpOptionIPAddress, value: []byte{10, 64}},
		ipv4Option(ipcpOptionSecondaryDNS, dnsServer),
		ipv4Option(2, dnsServer),
	})

	if err != nil {
		t.Fatal(err)
	}
	if client.address != netip.IPv4Unspecified() {
		t.Errorf("address = %v, want 0.0.0.0", client.address)
	}
	if client.dnsServers[1].IsValid() {
		t.Errorf("secondary DNS server = %v after it was refused, want none", client.dnsServers[1])
	}
}

func TestClientRecordsWhetherThePeerAnnouncedAnAddress(t *testing.T) {
	tests := []struct {
		name    string
		options []configOption
		want    netip.Addr
	}{
		{"an announced address", []configOption{ipv4Option(ipcpOptionIPAddress, serverAddress)}, serverAddress},
		{"no address", nil, netip.Addr{}},
		{"a malformed address", []configOption{{kind: ipcpOptionIPAddress, value: []byte{10}}}, netip.Addr{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newClientIPCP()
			client.acceptPeerOptions([]configOption{ipv4Option(ipcpOptionIPAddress, dnsServer)})

			client.acceptPeerOptions(test.options)

			if client.peerAddress != test.want {
				t.Errorf("peer address = %v, want %v", client.peerAddress, test.want)
			}
		})
	}
}

func TestServerAcksItsOwnOfferAndNaksAnythingElse(t *testing.T) {
	server := testServerIPCP()
	otherAddress := netip.MustParseAddr("192.0.2.1")
	tests := []struct {
		name     string
		proposed configOption
		want     verdict
	}{
		{"the assigned address", ipv4Option(ipcpOptionIPAddress, clientAddress), ackVerdict()},
		{"another address", ipv4Option(ipcpOptionIPAddress, otherAddress), nakVerdict(clientAddress.AsSlice())},
		{"the primary DNS server", ipv4Option(ipcpOptionPrimaryDNS, dnsServer), ackVerdict()},
		{"another primary DNS server", ipv4Option(ipcpOptionPrimaryDNS, otherAddress), nakVerdict(dnsServer.AsSlice())},
		{"a secondary DNS server it does not have", ipv4Option(ipcpOptionSecondaryDNS, dnsServer), rejectVerdict()},
		{"Van Jacobson compression", configOption{kind: 2, value: []byte{0x00, 0x2D, 0x0F, 0x01}}, rejectVerdict()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertVerdict(t, server.judge(test.proposed), test.want)
		})
	}
}

func TestServerStopsAnnouncingItsAddressOnceRefused(t *testing.T) {
	server := testServerIPCP()
	assertOptions(t, server.ownOptions(), []configOption{ipv4Option(ipcpOptionIPAddress, serverAddress)})

	if err := server.applyNak([]configOption{ipv4Option(ipcpOptionIPAddress, clientAddress)}); err != nil {
		t.Fatal(err)
	}
	assertOptions(t, server.ownOptions(), []configOption{ipv4Option(ipcpOptionIPAddress, serverAddress)})
	if err := server.applyReject([]configOption{ipv4Option(ipcpOptionIPAddress, serverAddress)}); err != nil {
		t.Fatal(err)
	}

	if proposed := server.ownOptions(); len(proposed) != 0 {
		t.Errorf("options = %+v once the peer refused our address, want none", proposed)
	}
}

func TestServerHandsOutAtMostTwoDNSServers(t *testing.T) {
	servers := []netip.Addr{dnsServer, serverAddress, clientAddress}
	server := newServerIPCP(ServerConfig{LocalAddress: serverAddress, PeerAddress: clientAddress, DNSServers: servers})
	server.acceptPeerOptions([]configOption{ipv4Option(ipcpOptionIPAddress, clientAddress)})

	settings, err := server.settings()

	if err != nil {
		t.Fatal(err)
	}
	assertSettings(t, settings, IPv4Settings{
		LocalAddress: serverAddress,
		PeerAddress:  clientAddress,
		DNSServers:   servers[:2],
	})
}

func TestOnlyUnicastIPv4AddressesBelongToHosts(t *testing.T) {
	tests := []struct {
		address netip.Addr
		want    bool
	}{
		{clientAddress, true},
		{netip.MustParseAddr("192.0.2.255"), true},
		{netip.IPv4Unspecified(), false},
		{limitedBroadcast, false},
		{netip.MustParseAddr("239.255.255.250"), false},
		{netip.MustParseAddr("127.0.0.53"), false},
		{netip.MustParseAddr("2001:db8::1"), false},
		{netip.Addr{}, false},
	}
	for _, test := range tests {
		if got := isHostAddress(test.address); got != test.want {
			t.Errorf("isHostAddress(%v) = %t, want %t", test.address, got, test.want)
		}
	}
}
