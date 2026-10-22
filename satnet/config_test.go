package satnet

import (
	"errors"
	"math"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/ppp"
)

// expectInvalidConfig checks that config is refused as ErrInvalidConfig, and
// with message when it is not empty.
func expectInvalidConfig(t *testing.T, config Config, message string) {
	t.Helper()
	err := config.Validate()
	var configErr *Error
	if !errors.As(err, &configErr) || configErr.Kind != ErrInvalidConfig {
		t.Fatalf("Validate() = %v, want ErrInvalidConfig", err)
	}
	if message != "" && configErr.Message != message {
		t.Errorf("Validate() says %q, want %q", configErr.Message, message)
	}
}

func TestDefaultsTargetIridiumsGatewayAndAreValid(t *testing.T) {
	config := NewConfig(DenyAll())

	if config.Number != IridiumInternetNumber {
		t.Errorf("Number = %q", config.Number)
	}
	if config.Call.Bearer == nil || *config.Call.Bearer != IridiumInternetBearer() {
		t.Errorf("Bearer = %v", config.Call.Bearer)
	}
	if err := config.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestRejectsUnusableTuningAndHosts(t *testing.T) {
	smallMTU := NewConfig(DenyAll())
	smallMTU.Tuning.MTU = 100
	expectInvalidConfig(t, smallMTU, "")

	badHost := NewConfig(DenyAll())
	badHost.Hosts["not a host"] = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	expectInvalidConfig(t, badHost, "")

	endless := NewConfig(DenyAll())
	endless.Tuning.NegotiationTimeout = math.MaxInt64
	expectInvalidConfig(t, endless, "")
}

func TestAcceptsTheShortestUsefulLimits(t *testing.T) {
	config := NewConfig(DenyAll())
	config.Limits = Limits{
		MaxCallDuration: 24 * time.Second,
		MaxBytes:        minMaxBytes,
		IdleTimeout:     25 * time.Second,
	}

	if err := config.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

// TestRejectsZeroAndAbsurdLimits checks the limits that refuse absurd values:
// zero is "no limit" in Go, so the invalid durations are negative. A zero
// data limit is left out, as MaxBytes is unsigned and 0 means none.
func TestRejectsZeroAndAbsurdLimits(t *testing.T) {
	defaults := DefaultLimits()
	for name, change := range map[string]func(*Limits){
		"negative idle timeout":  func(l *Limits) { l.IdleTimeout = -1 },
		"negative call duration": func(l *Limits) { l.MaxCallDuration = -1 },
		"absurd call duration":   func(l *Limits) { l.MaxCallDuration = math.MaxInt64 },
		// Over before PPP is up and the hang-up done.
		"call duration before PPP is up": func(l *Limits) { l.MaxCallDuration = 23 * time.Second },
		"data limit below PPP's":         func(l *Limits) { l.MaxBytes = minMaxBytes - 1 },
		// Over while a lookup waits for its second DNS server.
		"idle time of a lookup": func(l *Limits) { l.IdleTimeout = 24 * time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			limits := defaults
			change(&limits)
			config := NewConfig(DenyAll())
			config.Limits = limits

			expectInvalidConfig(t, config, "")
		})
	}
}

func TestConfigurationsAreRefusedWithTheirMessages(t *testing.T) {
	for _, c := range []struct {
		name    string
		change  func(*Config)
		message string
	}{
		{"MTU", func(c *Config) { c.Tuning.MTU = maxMTU + 1 }, "MTU must be between 296 and 1500 bytes"},
		{"link rate", func(c *Config) { c.Tuning.LinkRate = 0 }, "the link rate must be above zero"},
		{"buffers", func(c *Config) { c.Tuning.TCPSendBuffer = c.Tuning.MTU - 1 }, "TCP buffers must hold at least one full packet"},
		{"host name", func(c *Config) { c.Hosts["bad name"] = ipv4(192, 0, 2, 1) }, `"bad name" in hosts is not a valid host name`},
		// Control characters are escaped as \u{7f}, where Go's %q would write
		// "bad\x7fname".
		{"host name with a control character", func(c *Config) { c.Hosts["bad\x7fname"] = ipv4(192, 0, 2, 1) },
			`"bad\u{7f}name" in hosts is not a valid host name`},
		{"host address", func(c *Config) { c.Hosts["six.example"] = netip.MustParseAddr("2001:db8::1") }, `"six.example" in hosts needs an IPv4 address`},
		{"negative duration", func(c *Config) { c.Call.ConnectTimeout = -time.Second }, "durations must not be negative"},
		{"long duration", func(c *Config) { c.Tuning.TCPKeepalive = maxDuration + 1 }, "durations must be at most a year"},
		{"escape guard", func(c *Config) { c.Call.EscapeGuard = satlink.MaxEscapeGuard + 1 }, "the escape guard must be at most 10s"},
		{"negative limit", func(c *Config) { c.Limits.IdleTimeout = -time.Second }, "limits must not be negative; use 0 for no limit"},
		{"long limit", func(c *Config) { c.Limits.IdleTimeout = maxDuration + 1 }, "limits must be at most a year"},
		{"call time", func(c *Config) { c.Limits.MaxCallDuration = 23 * time.Second }, "the call time limit must leave time to bring PPP up and to hang up: at least 24s"},
		{"idle time", func(c *Config) { c.Limits.IdleTimeout = 24 * time.Second }, "the idle limit must be at least 25s, or the link hangs up while waiting for an answer"},
		{"data limit", func(c *Config) { c.Limits.MaxBytes = 100 }, "the data limit must be at least 2048 bytes, as PPP alone takes up to 500"},
	} {
		t.Run(c.name, func(t *testing.T) {
			config := NewConfig(DenyAll())
			c.change(&config)

			expectInvalidConfig(t, config, c.message)
		})
	}
}

func TestTheCallTimeLimitAllowsForTheEscapeGuard(t *testing.T) {
	config := NewConfig(DenyAll())
	config.Call.EscapeGuard = 60 * time.Millisecond
	// Bringing PPP up, then 2 × 60 ms and a second to hang up.
	shortestCall := 21120 * time.Millisecond
	config.Limits.MaxCallDuration = shortestCall - time.Millisecond
	expectInvalidConfig(t, config, "the call time limit must leave time to bring PPP up and to hang up: at least 21.12s")

	config.Limits.MaxCallDuration = shortestCall

	if err := config.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestHangingUpTakesTwoGuardsAndASecond(t *testing.T) {
	config := NewConfig(DenyAll())

	if got := config.hangUpTime(); got != 4*time.Second {
		t.Errorf("hangUpTime() = %v, want 4s", got)
	}
}

func TestZeroLimitsMeanNoLimit(t *testing.T) {
	config := NewConfig(DenyAll())
	config.Limits = Limits{}

	if err := config.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestHostsAreCheckedInOrder(t *testing.T) {
	config := NewConfig(DenyAll())
	for _, name := range []string{"zz bad", "aa bad", "mm bad"} {
		config.Hosts[name] = ipv4(192, 0, 2, 1)
	}

	expectInvalidConfig(t, config, `"aa bad" in hosts is not a valid host name`)
}

func TestPPPAsksTheGatewayToEscapeCRAndTheFlowControlCharacters(t *testing.T) {
	config := NewConfig(DenyAll())

	// Bit n stands for byte n: CR, so that the handset's "\r\nNO CARRIER\r"
	// never appears inside a frame, then XON and XOFF.
	const escaped uint32 = 1<<0x0D | 1<<0x11 | 1<<0x13
	if got := pppClientConfig(&config).ReceiveACCM; got != escaped {
		t.Errorf("ReceiveACCM = %#x, want %#x", got, escaped)
	}
}

func TestPPPTakesPacketsOfUpToTheMTU(t *testing.T) {
	// The MRU the gateway hears is the link's MTU (link.rs:414), 576 bytes
	// by default.
	defaults := NewConfig(DenyAll())
	if got := pppClientConfig(&defaults).MRU; got != 576 {
		t.Errorf("the default MRU = %d, want 576", got)
	}
	for _, mtu := range []int{296, 1500} {
		config := NewConfig(DenyAll())
		config.Tuning.MTU = mtu

		if got := pppClientConfig(&config).MRU; int(got) != mtu {
			t.Errorf("with an MTU of %d, the MRU = %d", mtu, got)
		}
	}
}

func TestPPPRefusesWhatItCannotCarry(t *testing.T) {
	config := NewConfig(DenyAll())
	config.Credentials = ppp.Credentials{Username: string(make([]byte, 256))}

	_, err := prepare(&config)

	var refused *ppp.ConfigError
	if !errors.Is(err, ErrInvalidConfig) || !errors.As(err, &refused) || refused.Kind != ppp.ErrCredentialTooLong {
		t.Fatalf("prepare() = %v, want ErrInvalidConfig wrapping ppp's ErrCredentialTooLong", err)
	}
	// The text wraps ppp's own message.
	if want := "invalid configuration: the username is longer than 255 bytes"; err.Error() != want {
		t.Errorf("prepare() = %q, want %q", err, want)
	}
}
