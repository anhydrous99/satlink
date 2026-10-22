package satnet

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/anhydrous99/satlink/internal/asciicase"
	"github.com/anhydrous99/satlink/internal/debugtext"
)

// Which destinations the app may reach over the satellite link; the rules
// are described in the package documentation.

// maxHostLen is the longest host name DNS allows, in bytes, without the
// trailing dot (RFC 1035 §2.3.4, as resolvers count it).
const maxHostLen = 253

// maxLabelLen is the longest label of a host name (RFC 1035 §2.3.4).
const maxLabelLen = 63

// ipv4Bits is the length of an IPv4 address in bits, the longest prefix a
// network rule may give.
const ipv4Bits = 32

// Policy is the set of destinations the app may reach. The zero Policy, like
// [DenyAll], allows nothing. Policies are values: [Policy.Allow] returns a
// new one and leaves the original unchanged.
type Policy struct {
	rules []AllowRule
}

// DenyAll returns a policy that allows nothing.
func DenyAll() Policy {
	return Policy{}
}

// ParsePolicy returns a policy from rules written as text, such as
// "api.example.com:443" (see the table in the package documentation). It
// fails with the [*ParseRuleError] of the first rule that does not parse.
func ParsePolicy(rules ...string) (Policy, error) {
	parsed := make([]AllowRule, 0, len(rules))
	for _, text := range rules {
		rule, err := ParseAllowRule(text)
		if err != nil {
			return Policy{}, err
		}
		parsed = append(parsed, rule)
	}
	return Policy{rules: parsed}, nil
}

// Allow returns a policy that also allows what rule allows.
func (p Policy) Allow(rule AllowRule) Policy {
	return Policy{rules: append(slices.Clip(p.rules), rule)}
}

// Rules returns the rules in the order they were added, as a copy for the
// caller to keep.
func (p Policy) Rules() []AllowRule {
	return slices.Clone(p.rules)
}

// AllowsLookup reports whether host may be looked up in DNS: some rule
// names it, by name, for any port. Names are compared in lower case without
// a trailing dot; an invalid name is never allowed.
func (p Policy) AllowsLookup(host string) bool {
	name, ok := normalizeHost(host)
	if !ok {
		return false
	}
	return slices.ContainsFunc(p.rules, func(rule AllowRule) bool {
		return rule.target.matchesHost(name)
	})
}

// AllowsHost reports whether host — a name or an IPv4 literal — may be
// reached on port. A literal needs an address or network rule; a name
// needs a rule naming it. Port 0 is never allowed.
func (p Policy) AllowsHost(host string, port uint16) bool {
	if address, ok := parseIPv4(host); ok {
		return p.AllowsAddress(address, port)
	}
	name, ok := normalizeHost(host)
	if !ok {
		return false
	}
	return slices.ContainsFunc(p.rules, func(rule AllowRule) bool {
		return rule.ports.contains(port) && rule.target.matchesHost(name)
	})
}

// AllowsAddress reports whether the bare IPv4 address may be reached on
// port: some address or network rule covers it. Port 0 is never allowed.
func (p Policy) AllowsAddress(address netip.Addr, port uint16) bool {
	return slices.ContainsFunc(p.rules, func(rule AllowRule) bool {
		return rule.ports.contains(port) && rule.target.matchesAddress(address)
	})
}

// AllowRule is one allowed destination: a target plus the ports allowed on
// it. Make one with [ParseAllowRule]; the zero AllowRule allows nothing. It
// is comparable with ==.
type AllowRule struct {
	target ruleTarget
	ports  PortRange
}

// ParseAllowRule parses one rule, such as "*.example.com:443" (see the
// table in the package documentation). Surrounding white space is ignored,
// and host names are kept in lower case without a trailing dot.
//
// It fails with a [*ParseRuleError]: ErrRuleMissingPort without a ":port",
// ErrRuleInvalidPort for ports outside 1 to 65535 or a range that runs
// backwards, ErrRuleInvalidHost for an invalid host name, and
// ErrRuleInvalidNetwork for an invalid IPv4 address or prefix length.
// Targets of digits, dots and '/' only are addresses; names such as
// 1password.com have letters.
func ParseAllowRule(rule string) (AllowRule, error) {
	trimmed := strings.TrimSpace(rule)
	separator := strings.LastIndexByte(trimmed, ':')
	if separator < 0 {
		return AllowRule{}, &ParseRuleError{Kind: ErrRuleMissingPort, Text: rule}
	}
	target, err := parseTarget(trimmed[:separator])
	if err != nil {
		return AllowRule{}, err
	}
	ports, err := ParsePortRange(trimmed[separator+1:])
	if err != nil {
		return AllowRule{}, err
	}
	return AllowRule{target: target, ports: ports}, nil
}

// String returns the rule as text that parses back to it, such as
// "*.example.com:1-1024". The zero AllowRule, which allows nothing, has no
// such text: it prints as ":0", which does not parse.
func (r AllowRule) String() string {
	return r.target.String() + ":" + r.ports.String()
}

// ruleTarget is what a rule allows: any host reached by name, one host,
// every subdomain of a domain, or an IPv4 network.
type ruleTarget struct {
	kind targetKind
	// name is the host or the domain, in lower case without a trailing dot
	// (targetHost, targetSubdomains).
	name string
	// network is the IPv4 network as written, its address not masked
	// (targetNetwork); a single address is a /32.
	network netip.Prefix
}

// targetKind says what a [ruleTarget] covers.
type targetKind int

const (
	// targetNone covers nothing: it is the zero kind, so that a rule built
	// without ParseAllowRule allows nothing rather than everything.
	targetNone targetKind = iota
	// targetAnyHost covers every host reached by name: "*".
	targetAnyHost
	// targetHost covers one host: "api.example.com".
	targetHost
	// targetSubdomains covers every subdomain of a domain, but not the
	// domain itself: "*.example.com".
	targetSubdomains
	// targetNetwork covers an IPv4 address or network: "203.0.113.0/24".
	targetNetwork
)

// anyHostTarget and subdomainPrefix are how a rule writes "any host" and
// "every subdomain of".
const (
	anyHostTarget   = "*"
	subdomainPrefix = "*."
)

// parseTarget parses the part of a rule before its ports.
func parseTarget(target string) (ruleTarget, error) {
	if target == anyHostTarget {
		return ruleTarget{kind: targetAnyHost}, nil
	}
	if domain, ok := strings.CutPrefix(target, subdomainPrefix); ok {
		name, ok := normalizeHost(domain)
		if !ok {
			return ruleTarget{}, &ParseRuleError{Kind: ErrRuleInvalidHost, Text: target}
		}
		return ruleTarget{kind: targetSubdomains, name: name}, nil
	}
	// Digits, dots and a prefix length mean an address; names like
	// 1password.com have letters.
	if isAddressNotation(target) {
		return parseNetwork(target)
	}
	name, ok := normalizeHost(target)
	if !ok {
		return ruleTarget{}, &ParseRuleError{Kind: ErrRuleInvalidHost, Text: target}
	}
	return ruleTarget{kind: targetHost, name: name}, nil
}

// isAddressNotation reports whether target holds only digits, dots and
// '/', as an IPv4 address or network does. The empty target does too, and
// then fails as a network.
func isAddressNotation(target string) bool {
	return strings.Trim(target, "0123456789./") == ""
}

// parseNetwork parses an IPv4 address, with an optional prefix length: a
// single address is a /32.
func parseNetwork(target string) (ruleTarget, error) {
	invalid := &ParseRuleError{Kind: ErrRuleInvalidNetwork, Text: target}
	addressText, lengthText, hasLength := strings.Cut(target, "/")
	prefixLen := ipv4Bits
	if hasLength {
		length, err := parseUnsigned(lengthText, 8)
		if err != nil || length > ipv4Bits {
			return ruleTarget{}, invalid
		}
		prefixLen = int(length)
	}
	address, ok := parseIPv4(addressText)
	if !ok {
		return ruleTarget{}, invalid
	}
	return ruleTarget{kind: targetNetwork, network: netip.PrefixFrom(address, prefixLen)}, nil
}

// matchesHost reports whether the target covers the host name host, which
// is normalised.
func (t ruleTarget) matchesHost(host string) bool {
	switch t.kind {
	case targetAnyHost:
		return true
	case targetHost:
		return host == t.name
	case targetSubdomains:
		// At least one label in front of the domain, so that the domain
		// itself and look-alikes such as badexample.com do not match.
		prefix, ok := strings.CutSuffix(host, t.name)
		return ok && len(prefix) > 1 && strings.HasSuffix(prefix, ".")
	default:
		return false
	}
}

// matchesAddress reports whether the target is a network that holds
// address.
func (t ruleTarget) matchesAddress(address netip.Addr) bool {
	return t.kind == targetNetwork && t.network.Contains(address)
}

// String returns the target as a rule writes it.
func (t ruleTarget) String() string {
	switch t.kind {
	case targetAnyHost:
		return anyHostTarget
	case targetHost:
		return t.name
	case targetSubdomains:
		return subdomainPrefix + t.name
	case targetNetwork:
		if t.network.IsSingleIP() {
			return t.network.Addr().String()
		}
		return t.network.String()
	case targetNone:
		// No rule can write it, so it has no text.
	}
	return ""
}

// PortRange is an inclusive range of ports. It is comparable with ==. Make
// one with [ParsePortRange] or [AllPorts]; the zero PortRange holds only
// port 0, which no rule allows.
type PortRange struct {
	first uint16
	last  uint16
}

// AllPorts returns the range of every port, 1 to 65535, which a rule
// writes as "*".
func AllPorts() PortRange {
	return PortRange{first: 1, last: 65535}
}

// allPortsText is how a rule writes every port.
const allPortsText = "*"

// ParsePortRange parses the ports part of a rule: "*", a port such as
// "443", or a range such as "8000-8099", with ports from 1 to 65535 and the
// range not running backwards. It fails with a [*ParseRuleError] of kind
// ErrRuleInvalidPort whose Text is text. [ParseAllowRule] parses a rule's
// ports the same way.
func ParsePortRange(text string) (PortRange, error) {
	if text == allPortsText {
		return AllPorts(), nil
	}
	invalid := &ParseRuleError{Kind: ErrRuleInvalidPort, Text: text}
	firstText, lastText, isRange := strings.Cut(text, "-")
	if !isRange {
		lastText = firstText
	}
	first, firstOK := parsePort(firstText)
	last, lastOK := parsePort(lastText)
	if !firstOK || !lastOK || first > last {
		return PortRange{}, invalid
	}
	return PortRange{first: first, last: last}, nil
}

// parsePort parses one port of a rule, 1 to 65535.
func parsePort(text string) (uint16, bool) {
	port, err := parseUnsigned(text, 16)
	return uint16(port), err == nil && port != 0
}

// parseUnsigned parses a decimal number of at most bits bits. A leading
// '+' is allowed, any other sign is not.
func parseUnsigned(text string, bits int) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(text, "+"), 10, bits)
}

// contains reports whether port is in the range. Port 0 never is.
func (r PortRange) contains(port uint16) bool {
	return port != 0 && r.first <= port && port <= r.last
}

// String returns the range as a rule writes it: "*", "443" or "1-1024".
func (r PortRange) String() string {
	switch {
	case r == AllPorts():
		return allPortsText
	case r.first == r.last:
		return strconv.Itoa(int(r.first))
	default:
		return fmt.Sprintf("%d-%d", r.first, r.last)
	}
}

// ParseRuleErrorKind classifies a [ParseRuleError]. Each kind is also a
// sentinel for errors.Is.
type ParseRuleErrorKind int

// The reasons a rule does not parse.
const (
	// ErrRuleMissingPort reports a rule without a port: rules end in :port,
	// :first-last or :*.
	ErrRuleMissingPort ParseRuleErrorKind = iota
	// ErrRuleInvalidPort reports ports outside 1 to 65535, or a range that
	// runs backwards.
	ErrRuleInvalidPort
	// ErrRuleInvalidHost reports a target that is not a valid host name.
	ErrRuleInvalidHost
	// ErrRuleInvalidNetwork reports a target that is not a valid IPv4
	// address or network.
	ErrRuleInvalidNetwork
)

// Error returns the kind's label, such as "invalid port in rule".
func (k ParseRuleErrorKind) Error() string {
	switch k {
	case ErrRuleMissingPort:
		return "rule without port"
	case ErrRuleInvalidPort:
		return "invalid port in rule"
	case ErrRuleInvalidHost:
		return "invalid host in rule"
	case ErrRuleInvalidNetwork:
		return "invalid network in rule"
	default:
		return fmt.Sprintf("satnet.ParseRuleErrorKind(%d)", int(k))
	}
}

// ParseRuleError is why a rule could not be parsed.
type ParseRuleError struct {
	// Kind classifies the error.
	Kind ParseRuleErrorKind
	// Text is the part of the rule that is wrong: the whole rule
	// (ErrRuleMissingPort), its ports (ErrRuleInvalidPort) or its target
	// (ErrRuleInvalidHost, ErrRuleInvalidNetwork).
	Text string
}

// Error returns the message, such as `"0" is not a port or port range`.
func (e *ParseRuleError) Error() string {
	switch e.Kind {
	case ErrRuleMissingPort:
		return debugtext.Quote(e.Text) + " has no port; write host:port, host:first-last or host:*"
	case ErrRuleInvalidPort:
		return debugtext.Quote(e.Text) + " is not a port or port range"
	case ErrRuleInvalidHost:
		return debugtext.Quote(e.Text) + " is not a valid host name"
	case ErrRuleInvalidNetwork:
		return debugtext.Quote(e.Text) + " is not a valid IPv4 address or network"
	default:
		return e.Kind.Error()
	}
}

// Is reports whether target is e's kind, so that errors.Is(err,
// ErrRuleInvalidPort) matches every *ParseRuleError of that kind.
func (e *ParseRuleError) Is(target error) bool {
	kind, ok := target.(ParseRuleErrorKind)
	return ok && kind == e.Kind
}

// normalizeHost returns host in canonical form — lower case, without a
// trailing dot — and false if it is not a valid DNS name: at most 253
// bytes of labels of 1 to 63 letters, digits, '-' (not at either end) and
// '_'. Only ASCII letters change case, as only they can be valid: Unicode
// case mapping would turn some letters beyond ASCII, such as the Kelvin
// sign, into ASCII ones, and so make an invalid name valid.
func normalizeHost(host string) (string, bool) {
	name := asciicase.Lower(strings.TrimSuffix(host, "."))
	if len(name) > maxHostLen {
		return "", false
	}
	for label := range strings.SplitSeq(name, ".") {
		if !isValidLabel(label) {
			return "", false
		}
	}
	return name, true
}

// isValidLabel reports whether label is a valid label of a host name.
func isValidLabel(label string) bool {
	if len(label) == 0 || len(label) > maxLabelLen {
		return false
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for i := range len(label) {
		if !isLabelByte(label[i]) {
			return false
		}
	}
	return true
}

// isLabelByte reports whether b may appear in a label: an ASCII letter or
// digit, '-' or '_'.
func isLabelByte(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' || b == '-' || b == '_'
}

// parseIPv4 parses an IPv4 address in dotted decimal: four decimal octets,
// without leading zeros.
func parseIPv4(text string) (netip.Addr, bool) {
	address, err := netip.ParseAddr(text)
	if err != nil || !address.Is4() {
		return netip.Addr{}, false
	}
	return address, true
}
