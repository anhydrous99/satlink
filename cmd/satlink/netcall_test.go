package main

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/satnet"
)

func TestTheCallSummarySaysHowLongTheCallLastedAndWhatItCarried(t *testing.T) {
	status := satnet.Status{Usage: satnet.Usage{ConnectedFor: 42900 * time.Millisecond, BytesSent: 1234, BytesReceived: 5678}}

	want := "Call lasted 42 s: 1234 bytes sent, 5678 received."
	if got := callSummary(status); got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestTheCallSummaryWarnsWhenTheCallMayStillBeUp(t *testing.T) {
	status := satnet.Status{
		Usage:            satnet.Usage{ConnectedFor: time.Minute, BytesSent: 12, BytesReceived: 34},
		CallMayStillBeUp: true,
	}

	want := "warning: the hang-up was not confirmed (12 bytes sent, 34 received), so the call may still be up " +
		"and billing. End it with `satlink hangup`, or power-cycle the handset."
	if got := callSummary(status); got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestTheWarningThatTheCallMayStillBeUpSaysHowToEndIt(t *testing.T) {
	tests := []struct {
		details string
		want    string
	}{
		{"", "warning: the hang-up was not confirmed, so the call may still be up and billing. " +
			"End it with `satlink hangup`, or power-cycle the handset.\n"},
		{" (quitting at once)", "warning: the hang-up was not confirmed (quitting at once), so the call may " +
			"still be up and billing. End it with `satlink hangup`, or power-cycle the handset.\n"},
	}
	for _, test := range tests {
		var stderr strings.Builder

		warnCallMayStillBeUp(&stderr, test.details)

		if stderr.String() != test.want {
			t.Errorf("warning %q, want %q", stderr.String(), test.want)
		}
	}
}

func TestTheResultIsTheOutcomeUnlessTheHangUpFailed(t *testing.T) {
	failed := errors.New("the command failed")
	hangUpFailed := errors.New("the hang-up failed")
	tests := []struct {
		name            string
		outcome, hangUp error
		result          error
		reported        string
	}{
		{"both succeeded", nil, nil, nil, ""},
		{"the command failed", failed, nil, failed, ""},
		{"the hang-up failed", nil, hangUpFailed, hangUpFailed, ""},
		{"both failed", failed, hangUpFailed, hangUpFailed, "error: the command failed\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr strings.Builder

			result := netResult(&stderr, test.outcome, test.hangUp)

			if !errors.Is(result, test.result) || stderr.String() != test.reported {
				t.Errorf("result %v, reported %q; want %v, %q", result, stderr.String(), test.result, test.reported)
			}
		})
	}
}

func TestAPanicBecomesTheOutcomeOnceReported(t *testing.T) {
	var stderr strings.Builder

	outcome := outcomeOf(&stderr, func() error { panic("a bug") })

	if !errors.Is(outcome, errCommandPanicked) {
		t.Errorf("outcome = %v, want %v", outcome, errCommandPanicked)
	}
	if !strings.HasPrefix(stderr.String(), "panic: a bug\n") {
		t.Errorf("reported %q, want the panic", stderr.String())
	}
}

func TestAnOutcomeWithoutPanicIsWhatTheCommandReturned(t *testing.T) {
	failed := errors.New("the command failed")

	if outcome := outcomeOf(brokenWriter{}, func() error { return failed }); !errors.Is(outcome, failed) {
		t.Errorf("outcome = %v, want %v", outcome, failed)
	}
}

func TestTheLinkShowsItsAddressesOnceNegotiated(t *testing.T) {
	tests := []struct {
		name   string
		status satnet.Status
		want   string
	}{
		{
			name:   "negotiating",
			status: satnet.Status{ConnectMessage: "19200"},
			want:   "Connected (19200).\n",
		},
		{
			name: "up",
			status: satnet.Status{ConnectMessage: "19200", IPv4: &satnet.IPv4Settings{
				LocalAddress: netip.MustParseAddr("10.64.0.2"),
				PeerAddress:  netip.MustParseAddr("10.64.0.1"),
				DNSServers:   []netip.Addr{netip.MustParseAddr("10.64.0.1"), netip.MustParseAddr("8.8.8.8")},
			}},
			want: "Connected (19200).\n  address:     10.64.0.2\n  gateway:     10.64.0.1\n" +
				"  DNS servers: 10.64.0.1, 8.8.8.8\n",
		},
		{
			name: "up without a gateway address or DNS",
			status: satnet.Status{ConnectMessage: "9600", IPv4: &satnet.IPv4Settings{
				LocalAddress: netip.MustParseAddr("10.64.0.2"),
			}},
			want: "Connected (9600).\n  address:     10.64.0.2\n  DNS servers: \n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr strings.Builder

			if err := printLink(&stderr, test.status); err != nil || stderr.String() != test.want {
				t.Errorf("printLink = %v, wrote %q; want %q", err, stderr.String(), test.want)
			}
		})
	}
}

func TestShowingTheLinkFailsWhenStandardErrorDoes(t *testing.T) {
	if err := printLink(brokenWriter{}, satnet.Status{}); err == nil {
		t.Error("printLink succeeded on a broken standard error")
	}
}
