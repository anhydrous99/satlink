package main

import (
	"errors"
	"io"
	"math"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/satnet"
)

// noVariables is an environment in which no variable is set.
func noVariables(string) (string, bool) {
	return "", false
}

// parseNet reads `satlink net` followed by commandLine as the command line
// gives it, in an environment that lookupEnv describes, and returns what the
// subcommand would run with, or the exit that ends the run instead. Nothing
// is dialled.
func parseNet(t *testing.T, commandLine []string, lookupEnv func(string) (string, bool)) (netArguments, *commandLineExit) {
	t.Helper()
	var parsed netArguments
	net := newNetCommand(func(arguments netArguments) error {
		parsed = arguments
		return nil
	})
	root := newRoot(programName, "").withSubcommands(net)
	named, exit := parseCommandLine(root, append([]string{"net"}, commandLine...), surroundings{lookupEnv: lookupEnv})
	if exit != nil {
		return netArguments{}, exit
	}
	if err := named.run(t.Context()); err != nil {
		t.Fatalf("satlink net %s: %v", strings.Join(commandLine, " "), err)
	}
	return parsed, nil
}

// mustParseNet is parseNet without environment variables, failing the test
// if the command line does not parse.
func mustParseNet(t *testing.T, commandLine ...string) netArguments {
	t.Helper()
	arguments, exit := parseNet(t, commandLine, noVariables)
	if exit != nil {
		t.Fatalf("satlink net %s: %s", strings.Join(commandLine, " "), exit.text)
	}
	return arguments
}

// errNotDialled is how a linkRecorder fails every lookup and dial.
var errNotDialled = errors.New("not dialled")

// linkRecorder is a link that reaches nothing: it keeps the timeout that
// each lookup and dial was given, and the connector a dial was to secure
// its connection with, and fails them all.
type linkRecorder struct {
	timeouts     []time.Duration
	tlsConnector *satnet.TLSConnector
}

func (l *linkRecorder) Resolve(_ string, timeout time.Duration) ([]netip.Addr, error) {
	l.timeouts = append(l.timeouts, timeout)
	return nil, errNotDialled
}

func (l *linkRecorder) Dial(_ string, _ uint16, timeout time.Duration) (*satnet.TCPConn, error) {
	l.timeouts = append(l.timeouts, timeout)
	return nil, errNotDialled
}

func (l *linkRecorder) DialTLS(_ string, _ uint16, connector *satnet.TLSConnector, timeout time.Duration) (*satnet.TLSConn, error) {
	l.timeouts = append(l.timeouts, timeout)
	l.tlsConnector = connector
	return nil, errNotDialled
}

func (*linkRecorder) RequestClose() {}

// The time allowed is checked here. Over Iridium a full TLS handshake takes
// about 8 s, and a lookup that waits out a silent first DNS server about 10 s:
// a shorter time would fail them after the call was billed.
func TestALookupAConnectionOrAHandshakeIsAllowedTwoMinutes(t *testing.T) {
	tests := []struct {
		name   string
		action netAction
	}{
		{"a lookup", resolveAction{host: "echo.test"}},
		{"a relay's connection", tcpAction{host: "echo.test", port: 7}},
		{"a fetch's connection", getAction{url: mustParseHTTPURL(t, "http://web.test/")}},
		{"a fetch's handshake", getAction{url: mustParseHTTPURL(t, "https://secure.test/")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := &linkRecorder{}

			err := test.action.execute(link, streams{stdout: io.Discard, stderr: io.Discard})

			// The connect timeout.
			const allowed = 2 * time.Minute
			if !errors.Is(err, errNotDialled) || !slices.Equal(link.timeouts, []time.Duration{allowed}) {
				t.Errorf("the command failed with %v, having been given the times %v; want one of %v",
					err, link.timeouts, allowed)
			}
		})
	}
}

// Targets that are rules rather than hosts are refused before dialling.
func TestTargetsThatAreRulesRatherThanHostsAreRefusedBeforeDialling(t *testing.T) {
	for _, commandLine := range [][]string{
		{"get", "http://*.web.test/"},
		{"get", "http://*/"},
		{"tcp", "10.0.0.0/8", "7"},
		{"tcp", "*", "7"},
		{"resolve", "*.web.test"},
		{"resolve", "10.0.0.0/8"},
	} {
		if _, err := mustParseNet(t, commandLine...).linkConfig(); err == nil {
			t.Errorf("%q made a link configuration", commandLine)
		}
	}
	for _, commandLine := range [][]string{
		{"get", "http://web.test/"},
		{"get", "https://secure.test:8443/"},
		{"tcp", "192.0.2.7", "7"},
		{"resolve", "echo.test"},
		{"status"},
	} {
		if _, err := mustParseNet(t, commandLine...).linkConfig(); err != nil {
			t.Errorf("%q made no link configuration: %v", commandLine, err)
		}
	}
}

// Limits too small to be of use are refused before dialling.
func TestLimitsTooSmallToBeOfUseAreRefusedBeforeDialling(t *testing.T) {
	for _, commandLine := range [][]string{
		{"--max-kb", "1", "status"},
		{"--idle-secs", "24", "status"},
	} {
		if _, err := mustParseNet(t, commandLine...).linkConfig(); !errors.Is(err, satnet.ErrInvalidConfig) {
			t.Errorf("%q: %v, want an invalid configuration", commandLine, err)
		}
	}
	for _, commandLine := range [][]string{
		{"--max-kb", "2", "status"},
		{"--idle-secs", "25", "status"},
		{"--max-minutes", "1", "status"},
	} {
		if _, err := mustParseNet(t, commandLine...).linkConfig(); err != nil {
			t.Errorf("%q made no link configuration: %v", commandLine, err)
		}
	}
}

func TestARefusedTargetIsNamed(t *testing.T) {
	_, err := mustParseNet(t, "tcp", "*", "7").linkConfig()

	if want := `"*" is not a host name or IPv4 address`; err == nil || err.Error() != want {
		t.Errorf("error = %v, want %s", err, want)
	}
	// The message escapes the bell character as \u{7}.
	if err, want := (netTarget{host: "*\a", port: 7}).check(satnet.DenyAll()), `"*\u{7}" is not a host name or IPv4 address`; err == nil || err.Error() != want {
		t.Errorf("check fails with %v, want %s", err, want)
	}
}

// An address is resolved without a call.
func TestAnAddressIsResolvedWithoutACall(t *testing.T) {
	address, ok := mustParseNet(t, "resolve", "192.0.2.7").action.literalAddress()
	if !ok || address != netip.MustParseAddr("192.0.2.7") {
		t.Errorf("literal address = %v, %t, want 192.0.2.7", address, ok)
	}

	if address, ok := mustParseNet(t, "resolve", "echo.test").action.literalAddress(); ok {
		t.Errorf("a name has the literal address %v", address)
	}
}

func TestOnlyIPv4AddressesAreLiteral(t *testing.T) {
	for _, host := range []string{"2001:db8::7", "::ffff:192.0.2.7", "192.0.2", "192.000.2.7"} {
		if address, ok := (resolveAction{host: host}).literalAddress(); ok {
			t.Errorf("%q has the literal address %v", host, address)
		}
	}
}

func TestTheCommandsOwnTargetIsAllowedWithTheOthers(t *testing.T) {
	config, err := mustParseNet(t, "--allow", "relay.example.com:443", "tcp", "echo.test", "7").linkConfig()
	if err != nil {
		t.Fatal(err)
	}

	policy := config.Policy
	if !policy.AllowsHost("relay.example.com", 443) || !policy.AllowsHost("echo.test", 7) || policy.AllowsHost("echo.test", 8) {
		t.Errorf("policy %v allows other destinations than relay.example.com:443 and echo.test:7", policy.Rules())
	}
}

func TestARuleThatDoesNotParseIsRefused(t *testing.T) {
	for _, subcommand := range [][]string{{"status"}, {"tcp", "echo.test", "7"}} {
		commandLine := append([]string{"--allow", "no port"}, subcommand...)
		if _, err := mustParseNet(t, commandLine...).linkConfig(); err == nil {
			t.Errorf("%q: an allow rule without a port made a link configuration", commandLine)
		}
	}
}

func TestTheCallTakesTheSettingsGiven(t *testing.T) {
	config, err := mustParseNet(t,
		"--number", "66666", "--username", "user", "--password", "secret",
		"--idle-secs", "+30", "--max-minutes", "5", "--max-kb", "2", "status").linkConfig()
	if err != nil {
		t.Fatal(err)
	}

	want := satnet.Limits{IdleTimeout: 30 * time.Second, MaxCallDuration: 5 * time.Minute, MaxBytes: 2048}
	credentials := satnet.Credentials{Username: "user", Password: "secret"}
	if config.Number != "66666" || config.Credentials != credentials || config.Limits != want {
		t.Errorf("config has number %q, %v and limits %+v; want 66666, user and %+v",
			config.Number, config.Credentials, config.Limits, want)
	}
}

func TestTheSettingsDefaultToTheLinksOwn(t *testing.T) {
	config, err := mustParseNet(t, "status").linkConfig()
	if err != nil {
		t.Fatal(err)
	}

	want := satnet.Limits{IdleTimeout: 5 * time.Minute, MaxCallDuration: time.Hour}
	if config.Number != satnet.IridiumInternetNumber || config.Limits != want {
		t.Errorf("config has number %q and limits %+v; want %s and %+v",
			config.Number, config.Limits, satnet.IridiumInternetNumber, want)
	}
}

func TestTheDataNumberComesFromTheEnvironmentUnlessGiven(t *testing.T) {
	tests := []struct {
		name        string
		variable    string
		commandLine []string
		number      string
	}{
		{"from the environment", "12345", []string{"status"}, "12345"},
		{"given", "12345", []string{"--number", "66666", "status"}, "66666"},
		// A variable set to "" is set, and the dial then fails.
		{"empty in the environment", "", []string{"status"}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookupEnv := func(key string) (string, bool) {
				return test.variable, key == dataNumberVariable
			}

			arguments, exit := parseNet(t, test.commandLine, lookupEnv)

			if exit != nil || arguments.number != test.number {
				t.Errorf("%q dials %q (%v), want %q", test.commandLine, arguments.number, exit, test.number)
			}
		})
	}
}

func TestThePasswordComesFromTheEnvironmentUnlessGiven(t *testing.T) {
	tests := []struct {
		name        string
		commandLine []string
		password    string
	}{
		{"from the environment", []string{"status"}, "hunter2"},
		{"given", []string{"--password", "letmein", "status"}, "letmein"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookupEnv := func(key string) (string, bool) {
				return "hunter2", key == passwordVariable
			}

			arguments, exit := parseNet(t, test.commandLine, lookupEnv)
			config, err := arguments.linkConfig()

			if exit != nil || err != nil || config.Credentials.Password != test.password {
				t.Errorf("%q gives the link the password %q (%v, %v), want %q", test.commandLine,
					config.Credentials.Password, exit, err, test.password)
			}
		})
	}
}

func TestAbsurdLimitsSaturateSoThatTheLinkRefusesThem(t *testing.T) {
	arguments := mustParseNet(t,
		"--idle-secs", "18446744073709551615", "--max-minutes", "18446744073709551615",
		"--max-kb", "18446744073709551615", "status")

	want := satnet.Limits{IdleTimeout: math.MaxInt64, MaxCallDuration: math.MaxInt64, MaxBytes: math.MaxUint64}
	if got := arguments.limits(); got != want {
		t.Errorf("limits = %+v, want %+v", got, want)
	}
}

func TestLimitsOfZeroAreRefusedAsTheCommandLineIsParsed(t *testing.T) {
	for _, flag := range []string{"--idle-secs", "--max-minutes", "--max-kb"} {
		_, exit := parseNet(t, []string{flag, "0", "status"}, noVariables)
		if !isUsageProblem(exit) || !strings.Contains(exit.text, "0 is not in 1..18446744073709551615") {
			t.Errorf("%s 0: %v, want it refused", flag, exit)
		}
	}
}

func TestSubcommandArgumentsAreCheckedAsTheyAreParsed(t *testing.T) {
	tests := []struct {
		commandLine []string
		says        string
	}{
		{[]string{"get", "ftp://example.com"}, `invalid value 'ftp://example.com' for '<URL>'`},
		{[]string{"tcp", "echo.test", "seven"}, `invalid value 'seven' for '<PORT>': invalid digit found in string`},
		{[]string{"tcp", "echo.test", "65536"}, `65536 is not in 0..=65535`},
		{[]string{"tcp", "echo.test"}, "the following required arguments were not provided:\n  <PORT>"},
		{[]string{"resolve", "a", "b"}, "unexpected argument 'b' found"},
		{[]string{"ping"}, "unrecognized subcommand 'ping'"},
		{[]string{"--number", "5"}, "'satlink net' requires a subcommand but one was not provided\n" +
			"  [subcommands: status, resolve, get, tcp, help]"},
		{[]string{"status", "--number", "5"}, "unexpected argument '--number' found"},
	}
	for _, test := range tests {
		_, exit := parseNet(t, test.commandLine, noVariables)
		if !isUsageProblem(exit) || !strings.Contains(exit.text, test.says) {
			t.Errorf("%q: %v, want a usage error that says %q", test.commandLine, exit, test.says)
		}
	}
}
