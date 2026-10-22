package satnet

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// policyOf parses rules, failing the test if one does not parse.
func policyOf(t *testing.T, rules ...string) Policy {
	t.Helper()
	policy, err := ParsePolicy(rules...)
	if err != nil {
		t.Fatalf("ParsePolicy(%q): %v", rules, err)
	}
	return policy
}

// ipv4 returns the IPv4 address a.b.c.d.
func ipv4(a, b, c, d byte) netip.Addr {
	return netip.AddrFrom4([4]byte{a, b, c, d})
}

// allowance is one question put to a policy and the expected answer.
type allowance struct {
	host    string
	port    uint16
	allowed bool
}

// checkHosts asks policy each question of cases with AllowsHost.
func checkHosts(t *testing.T, policy Policy, cases []allowance) {
	t.Helper()
	for _, c := range cases {
		if got := policy.AllowsHost(c.host, c.port); got != c.allowed {
			t.Errorf("AllowsHost(%q, %d) = %t, want %t", c.host, c.port, got, c.allowed)
		}
	}
}

func TestDeniesEverythingByDefault(t *testing.T) {
	for name, policy := range map[string]Policy{"DenyAll": DenyAll(), "zero": {}} {
		t.Run(name, func(t *testing.T) {
			if policy.AllowsLookup("example.com") {
				t.Error("the lookup of example.com is allowed")
			}
			if policy.AllowsHost("example.com", 443) {
				t.Error("example.com:443 is allowed")
			}
			if policy.AllowsAddress(ipv4(1, 1, 1, 1), 53) {
				t.Error("1.1.1.1:53 is allowed")
			}
		})
	}
}

func TestExactHostsMatchCaseInsensitivelyAndOnlyOnTheirPorts(t *testing.T) {
	policy := policyOf(t, "API.Example.com:443")

	checkHosts(t, policy, []allowance{
		{"api.example.com.", 443, true},
		{"api.example.com", 80, false},
		{"www.example.com", 443, false},
	})
	if !policy.AllowsLookup("api.example.com") {
		t.Error("the lookup of api.example.com is refused")
	}
	if policy.AllowsLookup("example.com") {
		t.Error("the lookup of example.com is allowed")
	}
}

func TestSubdomainRulesExcludeTheDomainItselfAndLookalikes(t *testing.T) {
	policy := policyOf(t, "*.example.com:443")

	checkHosts(t, policy, []allowance{
		{"mail.example.com", 443, true},
		{"a.b.example.com", 443, true},
		{"example.com", 443, false},
		{"badexample.com", 443, false},
	})
}

func TestNetworksAndPortRanges(t *testing.T) {
	policy := policyOf(t, "203.0.113.0/24:8000-8099", "198.51.100.7:25")

	for _, c := range []struct {
		address netip.Addr
		port    uint16
		allowed bool
	}{
		{ipv4(203, 0, 113, 200), 8050, true},
		{ipv4(203, 0, 114, 1), 8050, false},
		{ipv4(203, 0, 113, 200), 8100, false},
	} {
		if got := policy.AllowsAddress(c.address, c.port); got != c.allowed {
			t.Errorf("AllowsAddress(%v, %d) = %t, want %t", c.address, c.port, got, c.allowed)
		}
	}
	checkHosts(t, policy, []allowance{{"198.51.100.7", 25, true}})
	if policy.AllowsLookup("198.51.100.7") {
		t.Error("an address may be looked up")
	}
}

func TestNamesDoNotSatisfyAddressRulesAndViceVersa(t *testing.T) {
	policy := policyOf(t, "*:443", "10.0.0.0/8:*")

	checkHosts(t, policy, []allowance{
		{"anything.example", 443, true},
		{"anything.example", 22, false},
	})
	if policy.AllowsAddress(ipv4(93, 184, 216, 34), 443) {
		t.Error("a name rule allows a bare address")
	}
	if !policy.AllowsAddress(ipv4(10, 1, 2, 3), 22) {
		t.Error("the network rule refuses 10.1.2.3:22")
	}
}

func TestHostNamesMayStartWithDigits(t *testing.T) {
	policy := policyOf(t, "1password.com:443")

	checkHosts(t, policy, []allowance{{"1password.com", 443, true}})
}

func TestZeroLengthPrefixCoversEveryAddress(t *testing.T) {
	policy := policyOf(t, "0.0.0.0/0:53")

	if !policy.AllowsAddress(ipv4(8, 8, 8, 8), 53) {
		t.Error("0.0.0.0/0 does not cover 8.8.8.8")
	}
}

func TestRulesRoundTripThroughText(t *testing.T) {
	for _, text := range []string{
		"*:*",
		"api.example.com:443",
		"*.example.com:1-1024",
		"10.0.0.0/8:22",
		"192.0.2.1:7",
	} {
		rule, err := ParseAllowRule(text)
		if err != nil {
			t.Errorf("ParseAllowRule(%q): %v", text, err)
			continue
		}
		if got := rule.String(); got != text {
			t.Errorf("ParseAllowRule(%q).String() = %q", text, got)
		}
	}
}

func TestRejectsMalformedRules(t *testing.T) {
	for _, c := range []struct {
		rule string
		kind ParseRuleErrorKind
		text string
	}{
		{"example.com", ErrRuleMissingPort, "example.com"},
		{"example.com:0", ErrRuleInvalidPort, "0"},
		{"example.com:90-80", ErrRuleInvalidPort, "90-80"},
		{"exa mple.com:80", ErrRuleInvalidHost, "exa mple.com"},
		{"-bad.com:80", ErrRuleInvalidHost, "-bad.com"},
		{"10.0.0.0/33:80", ErrRuleInvalidNetwork, "10.0.0.0/33"},
		{"300.1.1.1:80", ErrRuleInvalidNetwork, "300.1.1.1"},
		// More rules that are malformed in other ways.
		{"  example.com  ", ErrRuleMissingPort, "  example.com  "},
		{"*.-bad.com:80", ErrRuleInvalidHost, "*.-bad.com"},
		{":80", ErrRuleInvalidNetwork, ""},
		{"10.0.0.0/8/9:80", ErrRuleInvalidNetwork, "10.0.0.0/8/9"},
		{"01.2.3.4:80", ErrRuleInvalidNetwork, "01.2.3.4"},
		{"1.2.3:80", ErrRuleInvalidNetwork, "1.2.3"},
		{"example.com:65536", ErrRuleInvalidPort, "65536"},
		{"example.com:-80", ErrRuleInvalidPort, "-80"},
		{"example.com:80-", ErrRuleInvalidPort, "80-"},
		{"example.com:1-2-3", ErrRuleInvalidPort, "1-2-3"},
		{"example.com:", ErrRuleInvalidPort, ""},
	} {
		_, err := ParseAllowRule(c.rule)
		var ruleErr *ParseRuleError
		if !errors.As(err, &ruleErr) || ruleErr.Kind != c.kind || ruleErr.Text != c.text {
			t.Errorf("ParseAllowRule(%q) = %v, want a %v error about %q", c.rule, err, c.kind, c.text)
		}
		if !errors.Is(err, c.kind) {
			t.Errorf("errors.Is(ParseAllowRule(%q), %v) is false", c.rule, c.kind)
		}
	}
}

func TestInvalidNamesAreNeverAllowed(t *testing.T) {
	policy := policyOf(t, "*:*")

	if policy.AllowsLookup("bad name.com") {
		t.Error("the lookup of an invalid name is allowed")
	}
	checkHosts(t, policy, []allowance{
		{"", 80, false},
		{strings.Repeat("a", 300), 80, false},
	})
}

func TestRulesAreTrimmedAndLowerCased(t *testing.T) {
	rule, err := ParseAllowRule("  Mail.Example.COM.:25 \t")

	if err != nil || rule.String() != "mail.example.com:25" {
		t.Errorf(`ParseAllowRule = %v, %v; want "mail.example.com:25"`, rule, err)
	}
}

func TestPortsMayCarryAPlusSign(t *testing.T) {
	rule, err := ParseAllowRule("example.com:+80-+81")

	if err != nil || rule.String() != "example.com:80-81" {
		t.Errorf(`ParseAllowRule = %v, %v; want "example.com:80-81"`, rule, err)
	}
}

func TestOnlyASCIILettersChangeCase(t *testing.T) {
	// Unicode case mapping would turn the Kelvin sign into an ASCII 'k'.
	if _, ok := normalizeHost("Kelvin.example"); ok {
		t.Error("a name with the Kelvin sign is valid")
	}
}

func TestPortZeroIsNeverAllowed(t *testing.T) {
	policy := policyOf(t, "*:*", "0.0.0.0/0:*")

	checkHosts(t, policy, []allowance{
		{"example.com", 0, false},
		{"192.0.2.1", 0, false},
	})
	if policy.AllowsAddress(ipv4(192, 0, 2, 1), 0) {
		t.Error("port 0 of an address is allowed")
	}
}

func TestTheZeroRuleAllowsNothingAndHasNoText(t *testing.T) {
	policy := DenyAll().Allow(AllowRule{})

	if policy.AllowsHost("example.com", 80) || policy.AllowsAddress(ipv4(192, 0, 2, 1), 80) || policy.AllowsLookup("example.com") {
		t.Error("the zero rule allows something")
	}
	if got := (AllowRule{}).String(); got != ":0" {
		t.Errorf("AllowRule{}.String() = %q, want \":0\"", got)
	}
}

func TestOnlyIPv4AddressesMatchNetworks(t *testing.T) {
	policy := policyOf(t, "0.0.0.0/0:*")

	for _, address := range []netip.Addr{
		netip.MustParseAddr("::ffff:192.0.2.1"),
		netip.MustParseAddr("2001:db8::1"),
		{},
	} {
		if policy.AllowsAddress(address, 80) {
			t.Errorf("%v is allowed", address)
		}
	}
	checkHosts(t, policy, []allowance{{"::ffff:192.0.2.1", 80, false}})
}

func TestAllowLeavesTheOriginalPolicyUnchanged(t *testing.T) {
	original := policyOf(t, "a.example:1")
	first := original.Allow(mustRule(t, "b.example:2"))
	second := original.Allow(mustRule(t, "c.example:3"))

	if len(original.Rules()) != 1 {
		t.Errorf("the original has %d rules", len(original.Rules()))
	}
	if !first.AllowsHost("b.example", 2) || first.AllowsHost("c.example", 3) {
		t.Errorf("the first extension is %v", first.Rules())
	}
	if !second.AllowsHost("c.example", 3) || second.AllowsHost("b.example", 2) {
		t.Errorf("the second extension is %v", second.Rules())
	}
}

func TestRulesAreACopy(t *testing.T) {
	policy := policyOf(t, "a.example:1")

	policy.Rules()[0] = AllowRule{}

	if !policy.AllowsHost("a.example", 1) {
		t.Error("changing what Rules returned changed the policy")
	}
}

func TestParsePolicyReportsTheFirstBadRule(t *testing.T) {
	_, err := ParsePolicy("good.example:1", "bad", "worse:0")

	var ruleErr *ParseRuleError
	if !errors.As(err, &ruleErr) || ruleErr.Text != "bad" {
		t.Errorf("ParsePolicy = %v, want the error about \"bad\"", err)
	}
}

func TestPortRangesParseAndPrint(t *testing.T) {
	for _, c := range []struct {
		text, printed string
	}{
		{"*", "*"},
		{"1-65535", "*"},
		{"443", "443"},
		{"8000-8099", "8000-8099"},
		{"7-7", "7"},
	} {
		ports, err := ParsePortRange(c.text)
		if err != nil || ports.String() != c.printed {
			t.Errorf("ParsePortRange(%q) = %v, %v; want %q", c.text, ports, err, c.printed)
		}
	}
	if AllPorts().String() != "*" || (PortRange{}).String() != "0" {
		t.Errorf("AllPorts() is %v and PortRange{} is %v", AllPorts(), PortRange{})
	}
}

func TestRuleErrorMessages(t *testing.T) {
	for _, c := range []struct {
		err  *ParseRuleError
		text string
	}{
		{&ParseRuleError{Kind: ErrRuleMissingPort, Text: "a"}, `"a" has no port; write host:port, host:first-last or host:*`},
		{&ParseRuleError{Kind: ErrRuleInvalidPort, Text: "0"}, `"0" is not a port or port range`},
		{&ParseRuleError{Kind: ErrRuleInvalidHost, Text: "a b"}, `"a b" is not a valid host name`},
		{&ParseRuleError{Kind: ErrRuleInvalidNetwork, Text: "1.2.3"}, `"1.2.3" is not a valid IPv4 address or network`},
		// Control characters are escaped as \u{..}, for every kind, where Go's
		// %q would write "\x01", "\v" and "\u0085".
		{&ParseRuleError{Kind: ErrRuleMissingPort, Text: "a\x01b"}, `"a\u{1}b" has no port; write host:port, host:first-last or host:*`},
		{&ParseRuleError{Kind: ErrRuleInvalidPort, Text: "\v"}, `"\u{b}" is not a port or port range`},
		{&ParseRuleError{Kind: ErrRuleInvalidHost, Text: "a\x01b"}, `"a\u{1}b" is not a valid host name`},
		{&ParseRuleError{Kind: ErrRuleInvalidNetwork, Text: "1.2.3\u0085"}, `"1.2.3\u{85}" is not a valid IPv4 address or network`},
		{&ParseRuleError{Kind: ParseRuleErrorKind(9)}, "satnet.ParseRuleErrorKind(9)"},
	} {
		if got := c.err.Error(); got != c.text {
			t.Errorf("Error() = %q, want %q", got, c.text)
		}
	}
	if ErrRuleInvalidPort.Error() != "invalid port in rule" || errors.Is(&ParseRuleError{Kind: ErrRuleInvalidHost}, ErrRuleInvalidPort) {
		t.Error("the kinds are not sentinels of their own")
	}
}

// mustRule parses rule, failing the test if it does not parse.
func mustRule(t *testing.T, rule string) AllowRule {
	t.Helper()
	parsed, err := ParseAllowRule(rule)
	if err != nil {
		t.Fatalf("ParseAllowRule(%q): %v", rule, err)
	}
	return parsed
}

func TestRuleErrorKindsHaveLabels(t *testing.T) {
	for kind, label := range map[ParseRuleErrorKind]string{
		ErrRuleMissingPort:     "rule without port",
		ErrRuleInvalidPort:     "invalid port in rule",
		ErrRuleInvalidHost:     "invalid host in rule",
		ErrRuleInvalidNetwork:  "invalid network in rule",
		ParseRuleErrorKind(-1): "satnet.ParseRuleErrorKind(-1)",
	} {
		if got := kind.Error(); got != label {
			t.Errorf("Error() = %q, want %q", got, label)
		}
	}
}
