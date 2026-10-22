package ppp

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

// secretPassword is the password the redaction tests look for.
const secretPassword = "hunter2"

// testServerConfig returns a valid server configuration demanding PAP.
func testServerConfig() ServerConfig {
	return ServerConfig{
		LocalAddress: serverAddress,
		PeerAddress:  clientAddress,
		DNSServers:   []netip.Addr{dnsServer},
		Authentication: &Authentication{
			Protocol:    AuthProtocolPAP,
			Credentials: Credentials{Username: "pilot", Password: secretPassword},
		},
	}
}

// assertConfigError fails the test unless err is a *ConfigError equal to
// want.
func assertConfigError(t *testing.T, err error, want ConfigError) {
	t.Helper()
	var configError *ConfigError
	if !errors.As(err, &configError) {
		t.Fatalf("error = %v, want a *ConfigError", err)
	}
	if *configError != want {
		t.Fatalf("error = %+v, want %+v", *configError, want)
	}
}

func TestRejectsUnusableSettings(t *testing.T) {
	config := NewConfig(ClientRole(Credentials{}))
	if err := config.validate(); err != nil {
		t.Fatalf("the defaults were refused: %v", err)
	}

	config.MRU = 20
	assertConfigError(t, config.validate(), ConfigError{Kind: ErrMRUTooSmall, MRU: 20})

	config = NewConfig(ClientRole(Credentials{Username: strings.Repeat("x", 256)}))
	assertConfigError(t, config.validate(), ConfigError{Kind: ErrCredentialTooLong, Credential: "username"})
}

func TestDebugOutputHidesThePassword(t *testing.T) {
	text := fmt.Sprintf("%#v", Credentials{Username: "user", Password: secretPassword})

	if !strings.Contains(text, "user") || strings.Contains(text, secretPassword) {
		t.Errorf("%%#v printed %q", text)
	}
}

func TestNewConfigHasTheDefaults(t *testing.T) {
	config := NewConfig(ClientRole(Credentials{}))

	if config.Timers != (Timers{Restart: 5 * time.Second, MaxConfigure: 10, MaxTerminate: 2, MaxFailure: 5}) {
		t.Errorf("timers = %+v", config.Timers)
	}
	if config.Keepalive == nil || *config.Keepalive != (Keepalive{Interval: 15 * time.Second, MaxMissed: 4}) {
		t.Errorf("keepalive = %+v", config.Keepalive)
	}
	if config.ReceiveACCM != 0 || config.MRU != DefaultMRU {
		t.Errorf("receive ACCM = %#x, MRU = %d; want 0 and %d", config.ReceiveACCM, config.MRU, DefaultMRU)
	}
}

func TestNewEndpointRefusesUnusableSettingsInOrder(t *testing.T) {
	tooLong := strings.Repeat("x", maxCredentialLen+1)
	assertRefusals(t, []refusal{
		{"an MRU below IPv4's minimum", func(c *Config) { c.MRU = MinMRU - 1 }, ConfigError{Kind: ErrMRUTooSmall, MRU: MinMRU - 1}},
		{"the MRU before the timers", func(c *Config) { c.MRU = 20; c.Timers = Timers{} }, ConfigError{Kind: ErrMRUTooSmall, MRU: 20}},
		{"a zero restart timer", func(c *Config) { c.Timers.Restart = 0 }, ConfigError{Kind: ErrInvalidTimers}},
		{"a negative restart timer", func(c *Config) { c.Timers.Restart = -time.Second }, ConfigError{Kind: ErrInvalidTimers}},
		{"no Configure-Requests", func(c *Config) { c.Timers.MaxConfigure = 0 }, ConfigError{Kind: ErrInvalidTimers}},
		{"no Terminate-Requests", func(c *Config) { c.Timers.MaxTerminate = 0 }, ConfigError{Kind: ErrInvalidTimers}},
		{"a negative Max-Failure", func(c *Config) { c.Timers.MaxFailure = -1 }, ConfigError{Kind: ErrInvalidTimers}},
		{"the timers before the keepalive", func(c *Config) { c.Timers.MaxConfigure = 0; c.Keepalive.Interval = 0 }, ConfigError{Kind: ErrInvalidTimers}},
		{"a zero keepalive interval", func(c *Config) { c.Keepalive.Interval = 0 }, ConfigError{Kind: ErrInvalidKeepalive}},
		{"a negative keepalive interval", func(c *Config) { c.Keepalive.Interval = -time.Second }, ConfigError{Kind: ErrInvalidKeepalive}},
		{"a zero miss limit", func(c *Config) { c.Keepalive.MaxMissed = 0 }, ConfigError{Kind: ErrInvalidKeepalive}},
		{"the keepalive before the credentials", func(c *Config) { c.Keepalive.MaxMissed = -1; c.Role = ClientRole(Credentials{Password: tooLong}) }, ConfigError{Kind: ErrInvalidKeepalive}},
		{"a client's long name", func(c *Config) { c.Role = ClientRole(Credentials{Username: tooLong}) }, ConfigError{Kind: ErrCredentialTooLong, Credential: "username"}},
		{"a client's long password", func(c *Config) { c.Role = ClientRole(Credentials{Password: tooLong}) }, ConfigError{Kind: ErrCredentialTooLong, Credential: "password"}},
		{"a server's demanded long name", serverWith(func(s *ServerConfig) { s.Authentication.Credentials.Username = tooLong }), ConfigError{Kind: ErrCredentialTooLong, Credential: "username"}},
		{"a server's demanded long password", serverWith(func(s *ServerConfig) { s.Authentication.Credentials.Password = tooLong }), ConfigError{Kind: ErrCredentialTooLong, Credential: "password"}},
	})
}

func TestNewEndpointRefusesServerPoliciesItCannotServe(t *testing.T) {
	ipv6 := netip.MustParseAddr("2001:db8::1")
	mapped := netip.MustParseAddr("::ffff:10.64.0.53")
	assertRefusals(t, []refusal{
		{"the credentials before the addresses", serverWith(func(s *ServerConfig) {
			s.Authentication.Credentials.Password = strings.Repeat("x", maxCredentialLen+1)
			s.LocalAddress = netip.Addr{}
		}), ConfigError{Kind: ErrCredentialTooLong, Credential: "password"}},
		{"no local address", serverWith(func(s *ServerConfig) { s.LocalAddress = netip.Addr{} }), ConfigError{Kind: ErrInvalidAddress}},
		{"an IPv6 peer address", serverWith(func(s *ServerConfig) { s.PeerAddress = ipv6 }), ConfigError{Kind: ErrInvalidAddress, Address: ipv6}},
		{"an IPv4-mapped DNS server", serverWith(func(s *ServerConfig) { s.DNSServers = append(s.DNSServers, mapped) }), ConfigError{Kind: ErrInvalidAddress, Address: mapped}},
		{"the addresses before the protocol", serverWith(func(s *ServerConfig) { s.PeerAddress = ipv6; s.Authentication.Protocol = 7 }), ConfigError{Kind: ErrInvalidAddress, Address: ipv6}},
		{"an unknown protocol", serverWith(func(s *ServerConfig) { s.Authentication.Protocol = 7 }), ConfigError{Kind: ErrInvalidAuthProtocol, Protocol: 7}},
	})
}

// refusal is a configuration change NewEndpoint must refuse, and how.
type refusal struct {
	name   string
	modify func(*Config)
	want   ConfigError
}

// assertRefusals fails the test unless NewEndpoint refuses each change to
// the default client configuration with its error.
func assertRefusals(t *testing.T, refusals []refusal) {
	t.Helper()
	for _, test := range refusals {
		t.Run(test.name, func(t *testing.T) {
			config := NewConfig(ClientRole(Credentials{}))
			test.modify(&config)

			endpoint, err := NewEndpoint(config)

			if endpoint != nil {
				t.Error("an endpoint was created")
			}
			assertConfigError(t, err, test.want)
		})
	}
}

// serverWith returns a configuration change that makes the configuration a
// server's, its testServerConfig changed by modify.
func serverWith(modify func(*ServerConfig)) func(*Config) {
	return func(config *Config) {
		server := testServerConfig()
		modify(&server)
		config.Role = ServerRole(server)
	}
}

func TestNewEndpointAcceptsWhatItCanWorkWith(t *testing.T) {
	longest := strings.Repeat("x", maxCredentialLen)
	tests := []struct {
		name   string
		modify func(*Config)
	}{
		{"the defaults", func(*Config) {}},
		{"the smallest MRU", func(c *Config) { c.MRU = MinMRU }},
		{"the largest MRU", func(c *Config) { c.MRU = 0xFFFF }},
		{"no Max-Failure", func(c *Config) { c.Timers.MaxFailure = 0 }},
		{"the fewest retries and the shortest timers", func(c *Config) {
			c.Timers = Timers{Restart: time.Nanosecond, MaxConfigure: 1, MaxTerminate: 1}
			c.Keepalive = &Keepalive{Interval: time.Nanosecond, MaxMissed: 1}
		}},
		{"no keepalive", func(c *Config) { c.Keepalive = nil }},
		{"the longest credentials", func(c *Config) { c.Role = ClientRole(Credentials{Username: longest, Password: longest}) }},
		{"a server", serverWith(func(*ServerConfig) {})},
		{"a server demanding CHAP", serverWith(func(s *ServerConfig) { s.Authentication.Protocol = AuthProtocolCHAPMD5 })},
		{"a server demanding nothing", serverWith(func(s *ServerConfig) { s.Authentication = nil })},
		{"a server without DNS", serverWith(func(s *ServerConfig) { s.DNSServers = nil })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := NewConfig(ClientRole(Credentials{}))
			test.modify(&config)

			endpoint, err := NewEndpoint(config)

			if err != nil || endpoint == nil {
				t.Fatalf("NewEndpoint = %v, %v", endpoint, err)
			}
			if endpoint.Phase() != PhaseDead {
				t.Errorf("phase = %v, want Dead", endpoint.Phase())
			}
		})
	}
}

func TestConfigErrorsSayWhatIsWrong(t *testing.T) {
	tests := []struct {
		err  ConfigError
		want string
	}{
		{ConfigError{Kind: ErrMRUTooSmall, MRU: 20}, "MRU 20 is below the IPv4 minimum of 68"},
		{ConfigError{Kind: ErrInvalidTimers}, "the restart timer and retry limits must be greater than zero"},
		{ConfigError{Kind: ErrInvalidKeepalive}, "keepalive needs a non-zero interval and miss limit"},
		{ConfigError{Kind: ErrCredentialTooLong, Credential: "username"}, "the username is longer than 255 bytes"},
		{ConfigError{Kind: ErrCredentialTooLong, Credential: "password"}, "the password is longer than 255 bytes"},
		{ConfigError{Kind: ErrInvalidAddress, Address: netip.MustParseAddr("2001:db8::1")}, "the server's addresses must be IPv4, not 2001:db8::1"},
		{ConfigError{Kind: ErrInvalidAddress}, "the server's addresses must be IPv4, not invalid IP"},
		{ConfigError{Kind: ErrInvalidAuthProtocol, Protocol: 7}, "7 is not an authentication protocol"},
		{ConfigError{Kind: 99}, "ppp.ConfigErrorKind(99)"},
	}
	for _, test := range tests {
		if got := test.err.Error(); got != test.want {
			t.Errorf("%+v.Error() = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestConfigErrorKindsAreLabelledAndMatchTheirErrors(t *testing.T) {
	kinds := []struct {
		kind  ConfigErrorKind
		label string
	}{
		{ErrMRUTooSmall, "MRU too small"},
		{ErrInvalidTimers, "invalid timers"},
		{ErrInvalidKeepalive, "invalid keepalive"},
		{ErrCredentialTooLong, "credential too long"},
		{ErrInvalidAddress, "invalid address"},
		{ErrInvalidAuthProtocol, "invalid authentication protocol"},
	}
	for _, test := range kinds {
		t.Run(test.label, func(t *testing.T) {
			err := fmt.Errorf("starting the link: %w", &ConfigError{Kind: test.kind})

			if test.kind.Error() != test.label {
				t.Errorf("label = %q, want %q", test.kind.Error(), test.label)
			}
			if !errors.Is(err, test.kind) {
				t.Errorf("errors.Is(%v, %v) = false", err, test.kind)
			}
			if other := (test.kind + 1) % ConfigErrorKind(len(kinds)); errors.Is(err, other) {
				t.Errorf("errors.Is(%v, %v) = true", err, other)
			}
			if errors.Is(err, ErrNotOpen) {
				t.Errorf("errors.Is(%v, ErrNotOpen) = true", err)
			}
		})
	}
}

func TestAuthProtocolsAreNamed(t *testing.T) {
	tests := map[AuthProtocol]string{
		AuthProtocolPAP:     "PAP",
		AuthProtocolCHAPMD5: "CHAPMD5",
		7:                   "AuthProtocol(7)",
	}
	for protocol, want := range tests {
		if got := protocol.String(); got != want {
			t.Errorf("AuthProtocol(%d).String() = %q, want %q", int(protocol), got, want)
		}
	}
}

func TestAServerRoleKeepsItsOwnCopyOfThePolicy(t *testing.T) {
	server := testServerConfig()
	role := ServerRole(server)

	server.DNSServers[0] = netip.MustParseAddr("192.0.2.1")
	server.Authentication.Credentials.Password = "changed"

	if got := role.server.DNSServers[0]; got != dnsServer {
		t.Errorf("DNS server = %v, want %v", got, dnsServer)
	}
	if got := role.server.Authentication.Credentials.Password; got != secretPassword {
		t.Errorf("demanded password = %q, want %q", got, secretPassword)
	}
}

func TestRolesAndCredentialsPrintRedacted(t *testing.T) {
	credentials := Credentials{Username: "user", Password: secretPassword}
	tests := []struct {
		name  string
		value fmt.Stringer
		text  string
		goSyn string
	}{
		{"credentials", credentials, "{user <redacted>}", `ppp.Credentials{Username:"user", Password:"<redacted>"}`},
		{"a client role", ClientRole(credentials), "client", `ppp.ClientRole(ppp.Credentials{Username:"user", Password:"<redacted>"})`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fmt.Sprint(test.value); got != test.text {
				t.Errorf("%%v = %q, want %q", got, test.text)
			}
			if got := fmt.Sprintf("%#v", test.value); got != test.goSyn {
				t.Errorf("%%#v = %q, want %q", got, test.goSyn)
			}
		})
	}
	if got := ServerRole(testServerConfig()).String(); got != "server" {
		t.Errorf("a server role prints as %q, want \"server\"", got)
	}
}

func TestServerConfigsPrintTheirCredentialsRedacted(t *testing.T) {
	server := testServerConfig()

	if got, want := fmt.Sprint(server), "{10.64.0.1 10.64.0.2 [10.64.0.53] &{PAP {pilot <redacted>}}}"; got != want {
		t.Errorf("%%v = %q, want %q", got, want)
	}
	goSyntax := fmt.Sprintf("%#v", server)
	wantEnd := `Authentication:&ppp.Authentication{Protocol:0, Credentials:ppp.Credentials{Username:"pilot", Password:"<redacted>"}}}`
	if !strings.HasPrefix(goSyntax, "ppp.ServerConfig{LocalAddress:") || !strings.HasSuffix(goSyntax, wantEnd) {
		t.Errorf("%%#v = %q", goSyntax)
	}
	if got := fmt.Sprintf("%#v", ServerRole(server)); !strings.HasPrefix(got, "ppp.ServerRole(ppp.ServerConfig{") {
		t.Errorf("a server role's %%#v = %q", got)
	}
}

func TestPointersToAuthenticationPrintAsPointersToStructsDo(t *testing.T) {
	authentication := &Authentication{Protocol: AuthProtocolPAP, Credentials: Credentials{Username: "pilot", Password: secretPassword}}
	var none *Authentication
	tests := []struct {
		format string
		value  any
		want   string
	}{
		{"%v", authentication, "&{PAP {pilot <redacted>}}"},
		{"%+v", authentication, "&{Protocol:PAP Credentials:{Username:pilot Password:<redacted>}}"},
		{"%#v", authentication, `&ppp.Authentication{Protocol:0, Credentials:ppp.Credentials{Username:"pilot", Password:"<redacted>"}}`},
		{"%v", []*Authentication{authentication, none}, "[&{PAP {pilot <redacted>}} <nil>]"},
		{"%v", none, "<nil>"},
		{"%#v", none, "(*ppp.Authentication)(nil)"},
	}
	for _, test := range tests {
		if got := fmt.Sprintf(test.format, test.value); got != test.want {
			t.Errorf("%s printed %q, want %q", test.format, got, test.want)
		}
	}
}

func TestPlusVNamesTheFieldsOfWhatHasFieldsToName(t *testing.T) {
	// %+v names a struct's fields, as fmt does for any struct,
	// with the passwords redacted; the opaque Role and Endpoint have none to
	// name.
	credentials := Credentials{Username: "pilot", Password: secretPassword}
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"Credentials", credentials, "{Username:pilot Password:<redacted>}"},
		{"a ServerConfig", testServerConfig(),
			"{LocalAddress:10.64.0.1 PeerAddress:10.64.0.2 DNSServers:[10.64.0.53] " +
				"Authentication:&{Protocol:PAP Credentials:{Username:pilot Password:<redacted>}}}"},
		{"a Config", NewConfig(ClientRole(credentials)),
			"{Role:client Timers:{Restart:5s MaxConfigure:10 MaxTerminate:2 MaxFailure:5} " +
				"Keepalive:&{Interval:15s MaxMissed:4} ReceiveACCM:0 MRU:1500}"},
		{"a Role", ClientRole(credentials), "client"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fmt.Sprintf("%+v", test.value); got != test.want {
				t.Errorf("%%+v = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConfigsPrintTheirFieldsWithCredentialsRedacted(t *testing.T) {
	config := NewConfig(ClientRole(Credentials{Username: "pilot", Password: secretPassword}))

	if got, want := fmt.Sprint(config), "{client {5s 10 2 5} &{15s 4} 0 1500}"; got != want {
		t.Errorf("%%v = %q, want %q", got, want)
	}
	goSyntax := fmt.Sprintf("%#v", config)
	wantStart := `ppp.Config{Role:ppp.ClientRole(ppp.Credentials{Username:"pilot", Password:"<redacted>"}), Timers:ppp.Timers{`
	if !strings.HasPrefix(goSyntax, wantStart) || !strings.HasSuffix(goSyntax, "ReceiveACCM:0x0, MRU:0x5dc}") {
		t.Errorf("%%#v = %q", goSyntax)
	}
}

func TestAnEndpointPrintsItsRoleAndPhase(t *testing.T) {
	endpoint := mustEndpoint(t, NewConfig(ClientRole(Credentials{Username: "pilot", Password: secretPassword})))

	if got, want := fmt.Sprint(endpoint), "client endpoint in phase Dead"; got != want {
		t.Errorf("%%v = %q, want %q", got, want)
	}
	want := `&ppp.Endpoint{role:ppp.ClientRole(ppp.Credentials{Username:"pilot", Password:"<redacted>"}), phase:ppp.PhaseDead}`
	if got := fmt.Sprintf("%#v", endpoint); got != want {
		t.Errorf("%%#v = %q, want %q", got, want)
	}
}

func TestNoFormatVerbShowsThePassword(t *testing.T) {
	credentials := Credentials{Username: "user", Password: secretPassword}
	authentication := Authentication{Protocol: AuthProtocolCHAPMD5, Credentials: credentials}
	server := testServerConfig()
	client := NewConfig(ClientRole(server.Authentication.Credentials))
	// Once the server has authenticated the client, both ends hold the
	// password.
	link := newTestLink(t, client, NewConfig(ServerRole(server)))
	link.settle(t)
	if link.server.Phase() != PhaseOpen {
		t.Fatalf("the server's phase = %v, want Open", link.server.Phase())
	}
	// fmt hands every verb in verbs to a Format method, but handles %p
	// itself: it prints a pointer as an address, and any other value field
	// by field, as the Credentials doc says. So %p joins them only for what
	// keeps the credentials behind pointers.
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%t"}
	verbsAndP := append(slices.Clone(verbs), "%p")
	values := []struct {
		name  string
		value any
		verbs []string
	}{
		{"Credentials", credentials, verbs},
		{"*Credentials", &credentials, verbsAndP},
		{"Authentication", authentication, verbs},
		{"*Authentication", &authentication, verbsAndP},
		{"a []*Authentication", []*Authentication{&authentication}, verbsAndP},
		{"a map of *Authentication", map[string]*Authentication{"gateway": &authentication}, verbsAndP},
		{"a struct holding a *Authentication", struct{ A *Authentication }{&authentication}, verbsAndP},
		{"a client Role", ClientRole(credentials), verbs},
		{"a ServerConfig with authentication", server, verbsAndP},
		{"a *ServerConfig with authentication", &server, verbsAndP},
		{"a server Role with authentication", ServerRole(server), verbsAndP},
		{"a client Config", client, verbs},
		{"a server Config", NewConfig(ServerRole(server)), verbsAndP},
		{"a []*Config", []*Config{&client}, verbsAndP},
		{"a struct holding a *Config", struct{ C *Config }{&client}, verbsAndP},
		{"a client *Endpoint", link.client, verbsAndP},
		{"a server *Endpoint that authenticated its peer", link.server, verbsAndP},
		{"a []*Endpoint", []*Endpoint{link.client, link.server}, verbsAndP},
		// An Endpoint must not be copied, but a copy made to be printed is
		// redacted too.
		{"a copy of a client Endpoint", *link.client, verbs},
		{"a copy of a server Endpoint", *link.server, verbs},
		{"a struct holding a copy of an Endpoint", struct{ E Endpoint }{*link.client}, verbs},
	}
	hexPassword := hex.EncodeToString([]byte(secretPassword))
	for _, value := range values {
		for _, verb := range value.verbs {
			t.Run(value.name+" "+verb, func(t *testing.T) {
				text := fmt.Sprintf(verb, value.value)

				if strings.Contains(text, secretPassword) || strings.Contains(strings.ToLower(text), hexPassword) {
					t.Errorf("%s printed the password: %s", verb, text)
				}
			})
		}
	}
}
