package mobile

import (
	"math"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/ppp"
	"github.com/anhydrous99/satlink/satnet"
)

func TestEveryCloseReasonHasItsConstantAndDetail(t *testing.T) {
	pppDown := ppp.DownReason{Kind: ppp.DownReasonTerminatedByPeer}
	cases := []struct {
		reason satnet.CloseReason
		want   LinkCloseReason
	}{
		{satnet.CloseReason{Kind: satnet.CloseReasonRequested}, LinkCloseReason{Kind: LinkCloseReasonRequested}},
		{satnet.CloseReason{Kind: satnet.CloseReasonCarrierLost}, LinkCloseReason{Kind: LinkCloseReasonCarrierLost}},
		{satnet.CloseReason{Kind: satnet.CloseReasonIdleTimeout}, LinkCloseReason{Kind: LinkCloseReasonIdleTimeout}},
		{satnet.CloseReason{Kind: satnet.CloseReasonCallTimeLimit}, LinkCloseReason{Kind: LinkCloseReasonCallTimeLimit}},
		{satnet.CloseReason{Kind: satnet.CloseReasonDataLimit}, LinkCloseReason{Kind: LinkCloseReasonDataLimit}},
		{satnet.CloseReason{Kind: satnet.CloseReasonNegotiationTimedOut}, LinkCloseReason{Kind: LinkCloseReasonNegotiationTimedOut}},
		{satnet.CloseReason{Kind: satnet.CloseReasonPPP, PPP: pppDown}, LinkCloseReason{Kind: LinkCloseReasonPPP, Detail: pppDown.String()}},
		{satnet.CloseReason{Kind: satnet.CloseReasonTransport, Detail: "unplugged"}, LinkCloseReason{Kind: LinkCloseReasonTransport, Detail: "unplugged"}},
		{satnet.CloseReason{Kind: satnet.CloseReasonInternal, Detail: "a bug"}, LinkCloseReason{Kind: LinkCloseReasonInternal, Detail: "a bug"}},
	}
	for _, c := range cases {
		if got := closeReasonOf(c.reason); *got != c.want {
			t.Errorf("closeReasonOf(%v) = %+v, want %+v", c.reason, *got, c.want)
		}
	}
}

func TestOnlyAClosingOrClosedLinkHasAReason(t *testing.T) {
	carrierLost := satnet.CloseReason{Kind: satnet.CloseReasonCarrierLost}
	cases := []struct {
		state      satnet.LinkState
		wantKind   int
		wantReason bool
	}{
		{satnet.LinkState{Kind: satnet.LinkStateNegotiating}, LinkStateNegotiating, false},
		{satnet.LinkState{Kind: satnet.LinkStateUp}, LinkStateUp, false},
		{satnet.LinkState{Kind: satnet.LinkStateClosing, Reason: carrierLost}, LinkStateClosing, true},
		{satnet.LinkState{Kind: satnet.LinkStateClosed, Reason: carrierLost}, LinkStateClosed, true},
	}
	for _, c := range cases {
		got := linkStateOf(c.state)

		if got.Kind != c.wantKind || (got.Reason != nil) != c.wantReason {
			t.Errorf("linkStateOf(%v) = kind %d with reason %v, want kind %d, a reason: %t", c.state, got.Kind, got.Reason, c.wantKind, c.wantReason)
			continue
		}
		if c.wantReason && got.Reason.Kind != LinkCloseReasonCarrierLost {
			t.Errorf("linkStateOf(%v) has reason %+v, want CarrierLost", c.state, *got.Reason)
		}
	}
}

func TestAStatusCopiesTheAddressesAndUsage(t *testing.T) {
	status := satnet.Status{
		State:          satnet.LinkState{Kind: satnet.LinkStateUp},
		ConnectMessage: "9600",
		IPv4: &satnet.IPv4Settings{
			LocalAddress: netip.MustParseAddr("10.64.0.2"),
			DNSServers:   []netip.Addr{netip.MustParseAddr("10.64.0.1"), netip.MustParseAddr("192.0.2.53")},
		},
		Usage:            satnet.Usage{ConnectedFor: 61900 * time.Millisecond, BytesSent: 1200, BytesReceived: math.MaxUint64},
		CallMayStillBeUp: true,
	}

	got := linkStatusOf(status)

	if got.State.Kind != LinkStateUp || got.ConnectMessage != "9600" || got.LocalAddress != "10.64.0.2" {
		t.Errorf("status is %+v", *got)
	}
	if want := []string{"10.64.0.1", "192.0.2.53"}; !slices.Equal(got.DNSServers.items, want) {
		t.Errorf("DNS servers are %q, want %q", got.DNSServers.items, want)
	}
	usage := [...]int64{got.ConnectedSeconds, got.BytesSent, got.BytesReceived}
	if want := [...]int64{61, 1200, math.MaxInt64}; usage != want || !got.CallMayStillBeUp {
		t.Errorf("seconds, bytes sent and received are %v, want %v; call perhaps up: %t", usage, want, got.CallMayStillBeUp)
	}
}

func TestAStatusBeforePPPIsUpHasNoAddresses(t *testing.T) {
	got := linkStatusOf(satnet.Status{State: satnet.LinkState{Kind: satnet.LinkStateNegotiating}})

	if got.LocalAddress != "" || got.DNSServers == nil || got.DNSServers.Len() != 0 || got.State == nil {
		t.Errorf("status is %+v, want no addresses, but a State and an empty DNSServers", *got)
	}
}
