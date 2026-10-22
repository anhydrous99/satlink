package sim_test

import (
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
	"github.com/anhydrous99/satlink/ppp"
	"github.com/anhydrous99/satlink/sim"
)

// Data calls through satlink.DataCall, with PPP and DNS over them.

// assertAnswersAT fails the test unless the handset is back in command
// mode and answers AT.
func assertAnswersAT(t *testing.T, handset *satlink.Handset) {
	t.Helper()
	if _, err := handset.Command("AT"); err != nil {
		t.Errorf("the handset does not answer AT: %v", err)
	}
}

// hangUp ends call, failing the test if it cannot.
func hangUp(t *testing.T, call *satlink.DataCall) {
	t.Helper()
	if err := call.HangUp(); err != nil {
		t.Fatalf("the call does not end: %v", err)
	}
}

func TestDiallingConnectsAndPPPAssignsAddressesAndDNS(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())

	call := dial(t, handset)
	if call.ConnectMessage() != "19200" {
		t.Errorf("connected with %q; want 19200", call.ConnectMessage())
	}
	settings := startPPP(t, call, ppp.Credentials{}).expectUp()

	want := ppp.IPv4Settings{LocalAddress: clientAddress, PeerAddress: gatewayAddress, DNSServers: []netip.Addr{gatewayAddress}}
	if !settings.Equal(want) {
		t.Errorf("IPCP agreed %+v; want %+v", settings, want)
	}
	stats := control.Stats()
	if stats.CallsPlaced != 1 || stats.CallsConnected != 1 || !stats.InDataCall {
		t.Errorf("stats %+v; want one data call up", stats)
	}
	if stats.DataBytesFromApp == 0 || stats.DataBytesToApp == 0 {
		t.Errorf("stats %+v; want data both ways", stats)
	}
}

func TestTheGatewayResolvesConfiguredNames(t *testing.T) {
	t.Parallel()
	handset, _ := readyHandset(t, fastConfig())
	client := startPPP(t, dial(t, handset), ppp.Credentials{})
	client.expectUp()

	client.send(dnsQueryDatagram(0x1234, "echo.test"))
	answer := parseDNSAnswer(t, client.mustReceive("DNS answer"))

	if answer.id != 0x1234 || answer.rcode != wire.DNSRcodeNoError || !slices.Equal(answer.addresses, []netip.Addr{echoHost}) {
		t.Errorf("answer %+v; want id 0x1234, NoError, [%v]", answer, echoHost)
	}
}

func TestUnknownNamesDoNotExistOnTheNetwork(t *testing.T) {
	t.Parallel()
	handset, _ := readyHandset(t, fastConfig())
	client := startPPP(t, dial(t, handset), ppp.Credentials{})
	client.expectUp()

	client.send(dnsQueryDatagram(7, "nowhere.test"))
	answer := parseDNSAnswer(t, client.mustReceive("DNS answer"))

	if answer.rcode != wire.DNSRcodeNXDomain || len(answer.addresses) != 0 {
		t.Errorf("answer %+v; want NXDomain without addresses", answer)
	}
}

func TestHangingUpReturnsTheHandsetToCommandMode(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())
	call := dial(t, handset)
	startPPP(t, call, ppp.Credentials{}).expectUp()

	hangUp(t, call)

	assertAnswersAT(t, handset)
	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
	assertLines(t, handset, "AT+CEER", "+CEER: Normal call clearing")
}

func TestEndingPPPLeavesTheCallUp(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())
	call := dial(t, handset)
	client := startPPP(t, call, ppp.Credentials{})
	client.expectUp()

	client.close()
	event := client.nextEvent(patience)

	want := ppp.Event{Kind: ppp.EventDown, Reason: ppp.DownReason{Kind: ppp.DownReasonClosed}}
	if !event.Equal(want) {
		t.Errorf("event %+v; want %+v", event, want)
	}
	if !control.Stats().InDataCall {
		t.Error("only hanging up ends the call, but it ended")
	}
	hangUp(t, call)
	assertAnswersAT(t, handset)
}

func TestACallDroppedByTheNetworkIsNoticed(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())
	call := dial(t, handset)

	control.DropCall()
	readUntil(t, call, "NO CARRIER")

	if !call.CarrierLost() || control.Stats().InDataCall {
		t.Errorf("carrier lost %v, call up %v; want the drop noticed", call.CarrierLost(), control.Stats().InDataCall)
	}
	hangUp(t, call)
	assertAnswersAT(t, handset)
}

// The next session's handset is made over the earlier one's Transport, which
// takes it back from the earlier handset.
func TestACallEscapedFromButNeverHungUpIsEndedByTheNextSession(t *testing.T) {
	t.Parallel()
	earlier, control := readyHandset(t, fastConfig())
	if _, err := earlier.CommandTimeout("ATD"+iridiumInternetNumber, patience); err != nil {
		t.Fatalf("the data call does not connect: %v", err)
	}
	// The hang-up stopped after the escape, as when the app is killed then:
	// the handset holds the call in online-command mode.
	transport := earlier.Transport()
	// The escape needs a guard time of silence before it; this sleep is
	// that silence, not a wait for something to happen.
	time.Sleep(clientGuard)
	writeAll(t, transport, "+++")
	readUntil(t, transport, "\r\nOK\r\n")
	if !control.Stats().InDataCall {
		t.Fatal("the escape ended the call")
	}

	handset := satlink.NewHandset(transport)
	if err := handset.Initialize(); err != nil {
		t.Fatalf("the handset is not recovered: %v", err)
	}

	if control.Stats().InDataCall {
		t.Error("the call was not ended")
	}
}

// Dropping the call drops the port with it; here closing the handset closes
// the port.
func TestClosingThePortEndsTheCallWhenTheAppAskedForIt(t *testing.T) {
	t.Parallel()
	for _, endCallWhenDTRDrops := range []bool{true, false} {
		handset, control := readyHandset(t, fastConfig())
		settings := callSettings()
		settings.EndCallWhenDTRDrops = endCallWhenDTRDrops
		dialWith(t, handset, settings)

		if err := handset.Close(); err != nil {
			t.Fatal(err)
		}

		if inCall := control.Stats().InDataCall; inCall != !endCallWhenDTRDrops {
			t.Errorf("with EndCallWhenDTRDrops %v, the call is up: %v", endCallWhenDTRDrops, inCall)
		}
	}
}

func TestTheGatewayChecksCredentials(t *testing.T) {
	t.Parallel()
	for _, protocol := range []ppp.AuthProtocol{ppp.AuthProtocolPAP, ppp.AuthProtocolCHAPMD5} {
		t.Run(protocol.String(), func(t *testing.T) {
			t.Parallel()
			config := fastConfig()
			config.Authentication = &sim.Authentication{
				Protocol:    protocol,
				Credentials: ppp.Credentials{Username: "pilot", Password: "s3cret"},
			}

			handset, _ := readyHandset(t, config)
			right := startPPP(t, dial(t, handset), ppp.Credentials{Username: "pilot", Password: "s3cret"})
			if settings := right.expectUp(); settings.LocalAddress != clientAddress {
				t.Errorf("the right credentials got %v", settings.LocalAddress)
			}

			handset, _ = readyHandset(t, config)
			wrong := startPPP(t, dial(t, handset), ppp.Credentials{Username: "pilot", Password: "guess"})
			if event := wrong.nextEvent(patience); event.Kind != ppp.EventDown || event.Reason.Kind != ppp.DownReasonAuthenticationFailed {
				t.Errorf("the wrong credentials got %v: %v; want an authentication failure", event.Kind, event.Reason)
			}
		})
	}
}

func TestOnlineCommandModeKeepsThePPPSession(t *testing.T) {
	t.Parallel()
	handset, _ := readyHandset(t, fastConfig())
	call := dial(t, handset)
	client := startPPP(t, call, ppp.Credentials{})
	client.expectUp()

	// The escape needs a guard time of silence before it; this sleep is
	// that silence, not a wait for something to happen.
	time.Sleep(clientGuard)
	writeAll(t, call, "+++")
	readUntil(t, call, "\r\nOK\r\n")
	writeAll(t, call, "AT+CSQ\r")
	readUntil(t, call, "+CSQ:4\r\n\r\nOK\r\n")
	writeAll(t, call, "ATO\r")
	readUntil(t, call, "CONNECT 19200\r\n")

	client.send(dnsQueryDatagram(9, "web.test"))
	answer := parseDNSAnswer(t, client.mustReceive("DNS answer"))
	if !slices.Equal(answer.addresses, []netip.Addr{webHost}) {
		t.Errorf("addresses %v; want [%v]", answer.addresses, webHost)
	}
}

func writeAll(t *testing.T, transport satlink.Transport, data string) {
	t.Helper()
	if err := transport.WriteAll([]byte(data)); err != nil {
		t.Fatalf("the simulator failed: %v", err)
	}
}

// This test asserts real-time behaviour, so it runs serially and is
// skipped under -short.
func TestASlowLinkPacesDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("paces traffic in real time")
	}
	const (
		callSetup      = 100 * time.Millisecond
		oneWayDelay    = 60 * time.Millisecond
		bytesPerSecond = 1_000
	)
	config := fastConfig()
	config.Link = sim.LinkProfile{CallSetup: callSetup, OneWayDelay: oneWayDelay, BytesPerSecond: bytesPerSecond}
	handset, _ := readyHandset(t, config)

	dialled := time.Now()
	call := dial(t, handset)
	if elapsed := time.Since(dialled); elapsed < callSetup {
		t.Errorf("connected after %v; want at least %v", elapsed, callSetup)
	}
	client := startPPP(t, call, ppp.Credentials{})
	client.expectUp()
	query := dnsQueryDatagram(1, "echo.test")
	sent := time.Now()
	client.send(query)
	answer := client.mustReceive("DNS answer")
	roundTrip := time.Since(sent)

	// A delay each way, and a millisecond for every byte of the query and
	// the answer (more, in fact, as PPP framing adds bytes).
	transferred := len(query) + len(answer)
	least := 2*oneWayDelay + time.Duration(transferred*1_000/bytesPerSecond)*time.Millisecond
	if roundTrip < least {
		t.Errorf("%v is faster than the link allows (%v)", roundTrip, least)
	}
	if roundTrip >= 5*time.Second {
		t.Errorf("the round trip took %v", roundTrip)
	}
	if addresses := parseDNSAnswer(t, answer).addresses; !slices.Equal(addresses, []netip.Addr{echoHost}) {
		t.Errorf("addresses %v; want [%v]", addresses, echoHost)
	}
}

func TestConfiguredHostsReplaceTheDefaults(t *testing.T) {
	t.Parallel()
	satellite := netip.MustParseAddr("203.0.113.9")
	config := fastConfig()
	config.Network.Hosts = []sim.HostAddress{{Name: "Satellite.Example", Address: satellite}}
	handset, _ := readyHandset(t, config)
	client := startPPP(t, dial(t, handset), ppp.Credentials{})
	client.expectUp()

	client.send(dnsQueryDatagram(3, "satellite.example"))
	configured := parseDNSAnswer(t, client.mustReceive("DNS answer"))
	client.send(dnsQueryDatagram(4, "echo.test"))
	defaults := parseDNSAnswer(t, client.mustReceive("DNS answer"))

	if !slices.Equal(configured.addresses, []netip.Addr{satellite}) {
		t.Errorf("satellite.example resolved to %v; want [%v]", configured.addresses, satellite)
	}
	if defaults.rcode != wire.DNSRcodeNXDomain {
		t.Errorf("echo.test answered %v; want NXDomain", defaults.rcode)
	}
}

func TestANetworkTheGatewayCannotServeFailsTheCall(t *testing.T) {
	t.Parallel()
	config := fastConfig()
	config.Network.Hosts = []sim.HostAddress{{Name: "v6.test", Address: netip.MustParseAddr("2001:db8::1")}}
	handset, control := readyHandset(t, config)

	_, err := handset.DialData(iridiumInternetNumber, callSettings())

	assertHandsetError(t, err, satlink.Error{Kind: satlink.ErrCall, Message: "NO CARRIER"})
	if control.Stats().InDataCall {
		t.Error("a call is up")
	}
	assertLines(t, handset, "AT+CEER", "+CEER: Service or option not available")
}

// From here to the end of the file: the network's DNS servers and silent
// addresses, and PPP restarted by the network.

func TestIPCPOffersTheConfiguredDNSServersInOrder(t *testing.T) {
	t.Parallel()
	cases := map[string][]netip.Addr{
		"two":  {netip.MustParseAddr("10.64.0.99"), gatewayAddress},
		"none": nil,
	}

	for name, servers := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config := fastConfig()
			config.Network.DNSServers = servers
			handset, _ := readyHandset(t, config)

			settings := startPPP(t, dial(t, handset), ppp.Credentials{}).expectUp()

			if !slices.Equal(settings.DNSServers, servers) {
				t.Errorf("IPCP offered %v; want %v", settings.DNSServers, servers)
			}
		})
	}
}

func TestNothingAnswersAtASilentAddress(t *testing.T) {
	t.Parallel()
	silent := netip.MustParseAddr("192.0.2.99")
	config := fastConfig()
	config.Network.SilentAddresses = []netip.Addr{silent}
	handset, _ := readyHandset(t, config)
	client := startPPP(t, dial(t, handset), ppp.Credentials{})
	client.expectUp()
	from := netip.AddrPortFrom(clientAddress, clientUDPPort)

	client.send(sim.UDPDatagram(from, netip.AddrPortFrom(silent, echoPort), []byte("lost")))
	client.send(sim.UDPDatagram(from, netip.AddrPortFrom(echoHost, echoPort), []byte("echoed")))
	// The echo answers in order, so the first answer shows whether the
	// silent address answered.
	source, _, payload := parseUDP(t, client.mustReceive("UDP echo"))

	if source.Addr() != echoHost || string(payload) != "echoed" {
		t.Errorf("the first answer came from %v with %q; want %v with %q", source, payload, echoHost, "echoed")
	}
}

func TestARestartedPPPSessionGetsTheNewAddress(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())
	client := startPPP(t, dial(t, handset), ppp.Credentials{})
	client.expectUp()

	if err := control.RestartPPP(renumberedAddress); err != nil {
		t.Fatalf("PPP does not restart: %v", err)
	}
	settings := client.expectUp()

	if settings.LocalAddress != renumberedAddress || settings.PeerAddress != gatewayAddress {
		t.Errorf("IPCP agreed %+v; want %v for the app and %v for the gateway", settings, renumberedAddress, gatewayAddress)
	}
	if !control.Stats().InDataCall {
		t.Error("the restart ended the call")
	}
}

func TestARestartWithAnAddressThatIsNotIPv4FailsAndTheSessionGoesOn(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())
	client := startPPP(t, dial(t, handset), ppp.Credentials{})
	client.expectUp()

	err := control.RestartPPP(netip.MustParseAddr("2001:db8::2"))

	var configErr *sim.ConfigError
	if !errors.As(err, &configErr) || configErr.Kind != ppp.ErrInvalidAddress {
		t.Errorf("the restart failed with %v; want a *sim.ConfigError of kind ErrInvalidAddress", err)
	}
	client.send(dnsQueryDatagram(5, "echo.test"))
	if answer := parseDNSAnswer(t, client.mustReceive("DNS answer")); !slices.Equal(answer.addresses, []netip.Addr{echoHost}) {
		t.Errorf("after the failed restart, echo.test resolved to %v; want [%v]", answer.addresses, echoHost)
	}
}

func TestARestartWithoutACallChangesNothing(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())

	if err := control.RestartPPP(renumberedAddress); err != nil {
		t.Fatalf("a restart without a call failed: %v", err)
	}

	if settings := startPPP(t, dial(t, handset), ppp.Credentials{}).expectUp(); settings.LocalAddress != clientAddress {
		t.Errorf("the next call got %v; want the configured %v", settings.LocalAddress, clientAddress)
	}
}
