package mobile

import (
	"fmt"
	"math"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/satnet"
)

// linkConfig returns the configuration of settings.
func linkConfig(t *testing.T, settings *LinkSettings) satnet.Config {
	t.Helper()
	config, err := settings.config(nil)
	if err != nil {
		t.Fatalf("config(): %v", err)
	}
	return config
}

func TestNewLinkSettingsAreTheLibrarysDefaults(t *testing.T) {
	settings := NewLinkSettings()

	if settings.Number != satnet.IridiumInternetNumber || settings.Username != "" || settings.Password != "" {
		t.Errorf("number %q, user %q, password %q", settings.Number, settings.Username, settings.Password)
	}
	limits := [...]int64{settings.MaxCallSeconds, settings.IdleTimeoutSeconds, settings.MaxBytes, settings.CloseGraceSeconds}
	if want := [...]int64{60 * 60, 5 * 60, 0, 30}; limits != want {
		t.Errorf("call limit, idle timeout, data limit and close grace are %v, want %v", limits, want)
	}
	if len(settings.allowRules) != 0 || len(settings.hosts) != 0 {
		t.Errorf("new settings allow %q and answer %v locally, want nothing", settings.allowRules, settings.hosts)
	}
}

func TestSettingsBecomeTheLinkConfiguration(t *testing.T) {
	settings := NewLinkSettings()
	settings.Allow("echo.test:7")
	settings.Number = "0088160000000"
	settings.Username, settings.Password = "app", "secret"
	settings.MaxCallSeconds, settings.IdleTimeoutSeconds, settings.MaxBytes = 600, 60, 4096
	settings.CloseGraceSeconds = 10
	settings.AddHost("relay.test", "192.0.2.7")

	config, err := settings.config(nil)

	if err != nil {
		t.Fatalf("config(): %v", err)
	}
	if config.Number != "0088160000000" || config.Credentials != (satnet.Credentials{Username: "app", Password: "secret"}) {
		t.Errorf("number %q, credentials %v", config.Number, config.Credentials)
	}
	if want := (satnet.Limits{MaxCallDuration: 10 * time.Minute, MaxBytes: 4096, IdleTimeout: time.Minute}); config.Limits != want {
		t.Errorf("limits are %+v, want %+v", config.Limits, want)
	}
	if config.Tuning.CloseGrace != 10*time.Second || len(config.Hosts) != 1 || config.Hosts["relay.test"] != netip.MustParseAddr("192.0.2.7") {
		t.Errorf("close grace %v, hosts %v", config.Tuning.CloseGrace, config.Hosts)
	}
	if !config.Policy.AllowsHost("echo.test", 7) || config.Policy.AllowsHost("echo.test", 8) {
		t.Errorf("the policy %v does not allow exactly echo.test:7", config.Policy.Rules())
	}
}

func TestAbsurdSecondsSaturateRatherThanWrap(t *testing.T) {
	settings := NewLinkSettings()
	settings.MaxCallSeconds = math.MaxInt64
	settings.CloseGraceSeconds = math.MinInt64

	config, err := settings.config(nil)

	if err != nil {
		t.Fatalf("config(): %v", err)
	}
	if config.Limits.MaxCallDuration != math.MaxInt64 || config.Tuning.CloseGrace != math.MinInt64 {
		t.Errorf("call limit %v and close grace %v, want the longest and the shortest durations",
			config.Limits.MaxCallDuration, config.Tuning.CloseGrace)
	}
}

func TestANegativeByteLimitIsRefused(t *testing.T) {
	settings := NewLinkSettings()
	settings.MaxBytes = -1

	_, err := settings.config(nil)

	assertSatlinkError(t, err,
		&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "limits must not be negative; use 0 for no limit"},
		"invalid argument: limits must not be negative; use 0 for no limit")
}

func TestARuleThatDoesNotParseIsAnInvalidArgument(t *testing.T) {
	settings := NewLinkSettings()
	settings.Allow("not a rule")
	_, parseErr := satnet.ParsePolicy("not a rule")

	_, err := settings.config(nil)

	assertSatlinkError(t, err,
		&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: parseErr.Error()},
		"invalid argument: "+parseErr.Error())
}

func TestHostAddressesMustBeIPv4(t *testing.T) {
	cases := map[string]string{
		"an IPv6 address":              "2001:db8::1",
		"an IPv4-mapped IPv6 address":  "::ffff:192.0.2.7",
		"an octet above 255":           "192.0.2.256",
		"an octet with a leading zero": "01.2.3.4",
		"a host name":                  "relay.test",
		"nothing":                      "",
	}
	for name, address := range cases {
		t.Run(name, func(t *testing.T) {
			settings := NewLinkSettings()
			settings.AddHost("relay.test", address)

			_, err := settings.config(nil)

			// The reason, which names no address.
			assertSatlinkError(t, err,
				&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "invalid IPv4 address syntax"},
				"invalid argument: invalid IPv4 address syntax")
		})
	}
}

func TestANameAddedTwiceKeepsTheLastAddress(t *testing.T) {
	settings := NewLinkSettings()
	settings.AddHost("relay.test", "192.0.2.7")
	settings.AddHost("relay.test", "192.0.2.8")

	config := linkConfig(t, settings)

	if len(config.Hosts) != 1 || config.Hosts["relay.test"] != netip.MustParseAddr("192.0.2.8") {
		t.Errorf("hosts are %v, want relay.test at its last address, 192.0.2.8", config.Hosts)
	}
}

// changesMeanwhile is how many entries a test adds to settings from one
// goroutine while another reads them: enough for the race detector, and a
// map's iteration, to meet the writes.
const changesMeanwhile = 200

func TestHostsAndRulesMayBeAddedWhileALinkReadsTheSettings(t *testing.T) {
	settings := NewLinkSettings()
	adding := make(chan struct{})
	go func() {
		defer close(adding)
		for i := range changesMeanwhile {
			settings.AddHost(fmt.Sprintf("relay%d.test", i), "192.0.2.7")
			settings.Allow(fmt.Sprintf("relay%d.test:7", i))
		}
	}()

	for range changesMeanwhile {
		if _, err := settings.config(nil); err != nil {
			t.Fatalf("config(): %v", err)
		}
	}

	<-adding
}

func TestAddHostWorksOnZeroSettings(t *testing.T) {
	var settings LinkSettings

	settings.AddHost("relay.test", "192.0.2.7")

	if settings.hosts["relay.test"] != "192.0.2.7" {
		t.Errorf("hosts are %v", settings.hosts)
	}
}
