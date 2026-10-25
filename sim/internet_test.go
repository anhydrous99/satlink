package sim

import (
	"log/slog"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/logtest"
)

// These tests record the process-wide logger, so they do not run in
// parallel.

func TestAnAddressTheStackCannotUseIsLeftOutWithAWarning(t *testing.T) {
	cases := []struct {
		address string
		warned  bool
	}{
		{address: "10.64.0.1", warned: false},
		{address: "0.0.0.0", warned: true},
		{address: "255.255.255.255", warned: true},
		{address: "224.0.0.1", warned: true},
	}

	for _, test := range cases {
		t.Run(test.address, func(t *testing.T) {
			lines := logtest.Record(t, logPackage, slog.LevelDebug)
			network := DefaultNetworkConfig()
			network.GatewayAddress = netip.MustParseAddr(test.address)

			newInternet(network, int(network.GatewayMRU), time.Now()).close()

			warning := logtest.Line{
				Level: slog.LevelWarn,
				Text:  "simulated internet: " + test.address + " cannot be the gateway's own address",
			}
			if warned := slices.Contains(lines(), warning); warned != test.warned {
				t.Errorf("warned %v; want %v", warned, test.warned)
			}
		})
	}
}

// What `satlink --simulate -vv` shows of an answer the UDP echo or the DNS
// server cannot send: here the answer to a datagram from port 0, where no
// datagram can go.
func TestTheUDPServicesLogTheAnswersTheyCannotSend(t *testing.T) {
	network := DefaultNetworkConfig()
	unanswerable := netip.AddrPortFrom(network.ClientAddress, 0)
	// The services answer on every address, the gateway's among them.
	cases := []struct {
		service string
		to      netip.AddrPort
		payload []byte
		want    string
	}{
		{
			service: "the UDP echo",
			to:      netip.AddrPortFrom(network.GatewayAddress, echoPort),
			payload: []byte("ping"),
			want:    "simulated internet: UDP echo dropped a datagram: unaddressable",
		},
		{
			service: "DNS",
			to:      netip.AddrPortFrom(network.GatewayAddress, dnsPort),
			payload: dnsQuery(1, "echo.test", dnsTypeA),
			want:    "simulated internet: DNS dropped a response: unaddressable",
		},
	}

	for _, test := range cases {
		t.Run(test.service, func(t *testing.T) {
			lines := logtest.Record(t, logPackage, slog.LevelDebug)
			now := time.Now()
			simulated := newInternet(network, int(network.GatewayMRU), now)
			t.Cleanup(simulated.close)

			simulated.deliver(UDPDatagram(unanswerable, test.to, test.payload))
			simulated.poll(now)

			want := logtest.Line{Level: slog.LevelDebug, Text: test.want}
			if got := lines(); !slices.Contains(got, want) {
				t.Errorf("records %v; want one of them %v", got, want)
			}
		})
	}
}
