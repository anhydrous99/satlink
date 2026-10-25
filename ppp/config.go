package ppp

import (
	"fmt"
	"io"
	"net/netip"
	"slices"
	"time"
)

// What an [Endpoint] needs to know before the link starts.

// DefaultMRU is the largest information field every PPP peer accepts by
// default (RFC 1661 §6.1).
const DefaultMRU uint16 = 1500

// MinMRU is the smallest MRU that still carries any IPv4 packet (RFC 791's
// minimum MTU).
const MinMRU uint16 = 68

// maxCredentialLen is the longest name or password: PAP and CHAP put them
// behind one-octet length fields.
const maxCredentialLen = 255

// Timers are the retransmission timer and counters (RFC 1661 §4.6).
type Timers struct {
	// Restart is how long to wait for an answer before retransmitting a
	// request.
	Restart time.Duration
	// MaxConfigure is how many Configure-Requests are sent without an answer
	// before giving up. Authentication has the same limit: on the PAP
	// requests, CHAP challenges and CHAP responses sent without an answer,
	// and on the peer's CHAP challenges answered since its last Success.
	MaxConfigure int
	// MaxTerminate is how many Terminate-Requests are sent without an answer
	// before giving up.
	MaxTerminate int
	// MaxFailure is how many Configure-Naks are sent before refusing an
	// option outright, which stops a peer that ignores our suggestions from
	// looping forever. LCP gives up on a peer once more than MaxFailure of
	// its Naks and Rejects have left our request as it was.
	MaxFailure int
}

// The defaults of [Timers]: RFC 1661's counters (§4.6), and a restart
// timer longer than its 3 s, for the reasons [DefaultTimers] gives.
const (
	defaultRestart      = 5 * time.Second
	defaultMaxConfigure = 10
	defaultMaxTerminate = 2
	defaultMaxFailure   = 5
)

// DefaultTimers returns RFC 1661's counters with a longer restart timer.
// RFC 1661 §4.6 asks for at least two round trips plus the packet time;
// over Iridium that is about 4.5 s, and a 3 s timer was seen to provoke
// duplicate answers.
func DefaultTimers() Timers {
	return Timers{
		Restart:      defaultRestart,
		MaxConfigure: defaultMaxConfigure,
		MaxTerminate: defaultMaxTerminate,
		MaxFailure:   defaultMaxFailure,
	}
}

// Keepalive configures LCP echo probes that notice a peer which has
// silently gone away.
//
// A probe is only sent after Interval passes without hearing anything from
// the peer, so a busy link pays nothing for it.
type Keepalive struct {
	// Interval is the silence on the link before a probe is sent.
	Interval time.Duration
	// MaxMissed is how many consecutive probes may go unanswered before the
	// link is declared dead.
	MaxMissed int
}

// The defaults of [Keepalive]: a silent peer is declared dead after
// defaultKeepaliveInterval × (defaultMaxMissed + 1), 75 s.
const (
	// defaultKeepaliveInterval is the silence before a probe is sent.
	defaultKeepaliveInterval = 15 * time.Second
	// defaultMaxMissed is how many probes in a row may go unanswered.
	defaultMaxMissed = 4
)

// DefaultKeepalive returns a probe after 15 s of silence, and the link
// declared dead after four unanswered probes.
func DefaultKeepalive() Keepalive {
	return Keepalive{
		Interval:  defaultKeepaliveInterval,
		MaxMissed: defaultMaxMissed,
	}
}

// Credentials are a name and password for PAP or CHAP. Formatting them with
// fmt never shows the password, and neither does formatting any value of
// this package that holds them, but for fmt's own exceptions. fmt calls no
// methods of what it reaches through an unexported struct field, nor of an
// operand whose verb does not fit it, which it reports as
// %!verb(type=value); it prints those field by field. So the password
// shows:
//   - in a struct of yours that keeps credentials in an unexported field;
//   - with %p, which fits only a pointer, for Credentials, an
//     Authentication, a client Role or Config, a copy of an Endpoint, or a
//     struct holding one of these;
//   - with %w, which fits only an error in fmt.Errorf, for those and for
//     pointers to them;
//   - with a verb that does not fit a pointer, such as %s, %q or %t, for a
//     pointer of yours to a value holding credentials, held in a slice, map
//     or struct.
//
// go vet reports a misplaced %w, but not %p for Credentials, a Role, a
// Config or an Endpoint: as they implement fmt.Formatter, vet assumes that
// they take every verb.
type Credentials struct {
	// Username is the Peer-ID for PAP, the Name for CHAP.
	Username string
	// Password is the password for PAP, the shared secret for CHAP.
	Password string
}

// String returns the credentials with the password redacted.
func (c Credentials) String() string {
	return fmt.Sprintf("{%s <redacted>}", c.Username)
}

// GoString returns the credentials in Go syntax, with the password
// redacted.
func (c Credentials) GoString() string {
	return fmt.Sprintf("ppp.Credentials{Username:%q, Password:\"<redacted>\"}", c.Username)
}

// stringWithFieldNames returns the credentials as %+v prints a struct,
// with the password redacted.
func (c Credentials) stringWithFieldNames() string {
	return fmt.Sprintf("{Username:%s Password:<redacted>}", c.Username)
}

// Format prints the redacted texts for every verb fmt hands it — GoString
// for %#v, the fields with their names for %+v, String otherwise — so that
// none of them, %d or %t included, for which fmt would otherwise print the
// fields, shows the password.
func (c Credentials) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, c)
}

// redactedStringer is a value whose String and GoString hide its secrets.
type redactedStringer interface {
	fmt.Stringer
	fmt.GoStringer
}

// fieldNamer is a redactedStringer whose fields are its API, as a struct's
// are: %+v names them, as fmt does for any struct. The opaque Role and
// Endpoint have no fields to name, and print String for %+v.
type fieldNamer interface {
	// stringWithFieldNames returns String's text with the fields' names,
	// secrets redacted alike.
	stringWithFieldNames() string
}

// writeRedacted writes value's GoString for %#v, its fields with their
// names for %+v when it is a fieldNamer, and its String for every other
// verb.
func writeRedacted(state fmt.State, verb rune, value redactedStringer) {
	text := value.String()
	named, hasFieldNames := value.(fieldNamer)
	switch {
	case verb == 'v' && state.Flag('#'):
		text = value.GoString()
	case verb == 'v' && state.Flag('+') && hasFieldNames:
		text = named.stringWithFieldNames()
	}
	writeText(state, text)
}

// writeText writes what a Format method prints.
func writeText(state fmt.State, text string) {
	// fmt.Formatter has no way to report a failed write, and fmt itself
	// ignores them: the error is dropped on purpose.
	_, _ = io.WriteString(state, text)
}

// AuthProtocol is an authentication protocol the endpoint speaks.
type AuthProtocol int

// The authentication protocols.
const (
	// AuthProtocolPAP is the Password Authentication Protocol (RFC 1334):
	// the password crosses the link in clear text.
	AuthProtocolPAP AuthProtocol = iota
	// AuthProtocolCHAPMD5 is the Challenge-Handshake Authentication Protocol
	// with MD5 (RFC 1994).
	AuthProtocolCHAPMD5
)

// String returns the protocol's name, "PAP" or "CHAPMD5".
func (p AuthProtocol) String() string {
	switch p {
	case AuthProtocolPAP:
		return "PAP"
	case AuthProtocolCHAPMD5:
		return "CHAPMD5"
	default:
		return fmt.Sprintf("AuthProtocol(%d)", int(p))
	}
}

// Authentication is a protocol and the credentials it checks.
type Authentication struct {
	// Protocol is the protocol the server asks the peer to authenticate
	// with. The peer may steer the server to the other one with a
	// Configure-Nak, so asking for CHAP-MD5 does not keep the password off
	// the line: with PAP it crosses in clear text.
	Protocol AuthProtocol
	// Credentials are the ones the peer must present.
	Credentials Credentials
}

// Format prints a *Authentication as fmt prints any pointer to a struct,
// whatever the verb fmt hands it and wherever it is held: "&", then the
// struct printed with the verb, its credentials redacted; a nil one as
// <nil>, or (*ppp.Authentication)(nil) for %#v. Without it, fmt prints one
// held in a slice, map or struct with a verb that does not fit a pointer,
// such as %s, by its target's fields without calling their methods: the
// password included.
func (a *Authentication) Format(state fmt.State, verb rune) {
	text := "<nil>"
	switch {
	case a != nil:
		// The struct itself has no methods, so fmt prints its fields, which
		// redact themselves.
		text = "&" + fmt.Sprintf(fmt.FormatString(state, verb), *a)
	case verb == 'v' && state.Flag('#'):
		text = "(*ppp.Authentication)(nil)"
	}
	writeText(state, text)
}

// ServerConfig is the answering side's addressing and authentication
// policy. Its addresses must be IPv4 and its protocol one of the
// AuthProtocol constants; [NewEndpoint] refuses anything else. Formatting
// a ServerConfig with fmt never shows a password, whatever the verb.
type ServerConfig struct {
	// LocalAddress is our own IPv4 address on the link.
	LocalAddress netip.Addr
	// PeerAddress is the IPv4 address handed to the client.
	PeerAddress netip.Addr
	// DNSServers are offered to the client; at most the first two are
	// offered.
	DNSServers []netip.Addr
	// Authentication is how the client must authenticate: with its
	// credentials, over its protocol or the other one the client may steer
	// the server to (see [Authentication]); nil asks for no authentication.
	Authentication *Authentication
}

// String returns the policy as %v prints a struct, with the credentials it
// demands redacted.
func (s ServerConfig) String() string {
	return fmt.Sprintf("{%v %v %v %v}", s.LocalAddress, s.PeerAddress, s.DNSServers, s.Authentication)
}

// GoString returns the policy in Go syntax, with the credentials it demands
// redacted.
func (s ServerConfig) GoString() string {
	return fmt.Sprintf("ppp.ServerConfig{LocalAddress:%#v, PeerAddress:%#v, DNSServers:%#v, Authentication:%#v}",
		s.LocalAddress, s.PeerAddress, s.DNSServers, s.Authentication)
}

// stringWithFieldNames returns the policy as %+v prints a struct, with
// the credentials it demands redacted.
func (s ServerConfig) stringWithFieldNames() string {
	return fmt.Sprintf("{LocalAddress:%v PeerAddress:%v DNSServers:%v Authentication:%+v}",
		s.LocalAddress, s.PeerAddress, s.DNSServers, s.Authentication)
}

// Format prints GoString for %#v, the fields with their names for %+v, and
// String for every other verb, as for [Credentials], so that %s, %d or %t
// print the policy as %v does rather than each field with a verb that may
// not fit it. Each text formats the Authentication pointer with %v, %+v or
// %#v, which reach its redacting methods; (*Authentication).Format redacts
// it for any other verb by itself: two guards, each enough alone.
func (s ServerConfig) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, s)
}

// Role is which end of the link an endpoint is: the dialling side, which
// authenticates when asked to and accepts the address it is given, or the
// answering side, which hands out addresses and may demand authentication.
//
// Make one with [ClientRole] or [ServerRole]; the zero Role is the dialling
// side without credentials. Formatting a Role with fmt never shows a
// password, but for fmt's exceptions that [Credentials] lists.
type Role struct {
	// server is the answering side's policy; nil for the dialling side.
	server *ServerConfig
	// credentials are what the dialling side authenticates with.
	credentials Credentials
}

// ClientRole returns the dialling side's role, authenticating with
// credentials when the peer asks for it.
func ClientRole(credentials Credentials) Role {
	return Role{credentials: credentials}
}

// ServerRole returns the answering side's role with server's addressing
// and authentication policy. The role keeps its own copy of server.
func ServerRole(server ServerConfig) Role {
	policy := server
	policy.DNSServers = slices.Clone(server.DNSServers)
	if server.Authentication != nil {
		authentication := *server.Authentication
		policy.Authentication = &authentication
	}
	return Role{server: &policy}
}

// String returns "client" or "server".
func (r Role) String() string {
	if r.server != nil {
		return "server"
	}
	return "client"
}

// GoString returns the role in Go syntax, with passwords redacted.
func (r Role) GoString() string {
	if r.server != nil {
		return fmt.Sprintf("ppp.ServerRole(%#v)", *r.server)
	}
	return fmt.Sprintf("ppp.ClientRole(%#v)", r.credentials)
}

// Format prints GoString for %#v and String for every other verb, %+v
// included, as a Role has no fields to name: fmt cannot call the methods of
// the role's unexported credentials, and would otherwise print their
// fields.
func (r Role) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, r)
}

// Config is an endpoint's configuration. Start from [NewConfig]. Two
// Configs compared with == compare Keepalive, and a server role's policy,
// by pointer. Formatting a Config with fmt never shows a password, but for
// fmt's exceptions that [Credentials] lists.
type Config struct {
	// Role is which end of the link we are.
	Role Role
	// Timers are the retransmission behaviour.
	Timers Timers
	// Keepalive is dead-peer detection; nil relies on the lower layer
	// alone.
	Keepalive *Keepalive
	// ReceiveACCM holds the control characters (bit n stands for byte n)
	// the peer must escape when sending to us.
	ReceiveACCM uint32
	// MRU is the largest information field we accept from the peer.
	MRU uint16
}

// NewConfig returns the defaults for role: [DefaultTimers],
// [DefaultKeepalive], no control characters escaped towards us, and
// [DefaultMRU].
func NewConfig(role Role) Config {
	keepalive := DefaultKeepalive()
	return Config{
		Role:        role,
		Timers:      DefaultTimers(),
		Keepalive:   &keepalive,
		ReceiveACCM: 0,
		MRU:         DefaultMRU,
	}
}

// String returns the configuration as %v prints a struct, with passwords
// redacted.
func (c Config) String() string {
	return fmt.Sprintf("{%v %v %v %v %v}", c.Role, c.Timers, c.Keepalive, c.ReceiveACCM, c.MRU)
}

// GoString returns the configuration in Go syntax, with passwords
// redacted.
func (c Config) GoString() string {
	return fmt.Sprintf("ppp.Config{Role:%#v, Timers:%#v, Keepalive:%#v, ReceiveACCM:%#v, MRU:%#v}",
		c.Role, c.Timers, c.Keepalive, c.ReceiveACCM, c.MRU)
}

// stringWithFieldNames returns the configuration as %+v prints a struct,
// with passwords redacted.
func (c Config) stringWithFieldNames() string {
	return fmt.Sprintf("{Role:%v Timers:%+v Keepalive:%+v ReceiveACCM:%v MRU:%v}",
		c.Role, c.Timers, c.Keepalive, c.ReceiveACCM, c.MRU)
}

// Format prints GoString for %#v, the fields with their names for %+v, and
// String for every other verb, as for [ServerConfig]: a verb that does not
// fit a pointer, such as %s, makes fmt print a *Config held in a slice, map
// or struct without calling its role's methods.
func (c Config) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, c)
}

// validate checks the configuration in a fixed order: MRU, timers,
// keepalive, credentials, then a server's addresses, then its authentication
// protocol.
func (c Config) validate() error {
	if c.MRU < MinMRU {
		return &ConfigError{Kind: ErrMRUTooSmall, MRU: c.MRU}
	}
	if !c.Timers.valid() {
		return &ConfigError{Kind: ErrInvalidTimers}
	}
	if c.Keepalive != nil && !c.Keepalive.valid() {
		return &ConfigError{Kind: ErrInvalidKeepalive}
	}
	if c.Role.server == nil {
		return c.Role.credentials.validate()
	}
	return c.Role.server.validate()
}

// valid reports whether the timers can run: a restart timer and retry
// limits above zero, and a Max-Failure that is not negative.
func (t Timers) valid() bool {
	return t.Restart > 0 && t.MaxConfigure > 0 && t.MaxTerminate > 0 && t.MaxFailure >= 0
}

// valid reports whether the probes can run: an interval and a miss limit
// above zero.
func (k Keepalive) valid() bool {
	return k.Interval > 0 && k.MaxMissed > 0
}

// validate checks that PAP and CHAP can carry the credentials.
func (c Credentials) validate() error {
	if len(c.Username) > maxCredentialLen {
		return &ConfigError{Kind: ErrCredentialTooLong, Credential: "username"}
	}
	if len(c.Password) > maxCredentialLen {
		return &ConfigError{Kind: ErrCredentialTooLong, Credential: "password"}
	}
	return nil
}

// validate checks the credentials a server demands, then its addresses,
// then its authentication protocol.
func (s ServerConfig) validate() error {
	if s.Authentication != nil {
		if err := s.Authentication.Credentials.validate(); err != nil {
			return err
		}
	}
	if err := s.validateAddresses(); err != nil {
		return err
	}
	return s.validateAuthProtocol()
}

// validateAddresses checks that every address a server hands out or
// announces is IPv4, which the zero Addr is not.
func (s ServerConfig) validateAddresses() error {
	addresses := append([]netip.Addr{s.LocalAddress, s.PeerAddress}, s.DNSServers...)
	for _, address := range addresses {
		if !address.Is4() {
			return &ConfigError{Kind: ErrInvalidAddress, Address: address}
		}
	}
	return nil
}

// validateAuthProtocol checks that a server demands authentication, if at
// all, with a protocol the endpoint speaks.
func (s ServerConfig) validateAuthProtocol() error {
	if s.Authentication == nil || s.Authentication.Protocol.isKnown() {
		return nil
	}
	return &ConfigError{Kind: ErrInvalidAuthProtocol, Protocol: s.Authentication.Protocol}
}

// isKnown reports whether p is one of the AuthProtocol constants.
func (p AuthProtocol) isKnown() bool {
	return p == AuthProtocolPAP || p == AuthProtocolCHAPMD5
}

// ConfigErrorKind classifies a [ConfigError]. Each kind is also a sentinel
// for errors.Is.
type ConfigErrorKind int

// The kinds of [ConfigError].
const (
	// ErrMRUTooSmall reports an MRU that cannot carry an IPv4 packet; the
	// error's MRU field holds it.
	ErrMRUTooSmall ConfigErrorKind = iota
	// ErrInvalidTimers reports a restart timer or retry limit that is not
	// above zero, or a negative MaxFailure.
	ErrInvalidTimers
	// ErrInvalidKeepalive reports a keepalive interval or miss limit that is
	// not above zero.
	ErrInvalidKeepalive
	// ErrCredentialTooLong reports a name or password PAP and CHAP cannot
	// carry; the error's Credential field names which.
	ErrCredentialTooLong
	// ErrInvalidAddress reports a server address — LocalAddress,
	// PeerAddress or a DNS server — that is not IPv4, the zero Addr
	// included; the error's Address field holds it.
	ErrInvalidAddress
	// ErrInvalidAuthProtocol reports a server authentication protocol
	// without a constant of its own; the error's Protocol field holds it.
	ErrInvalidAuthProtocol
)

// Error returns the kind's label, such as "MRU too small".
func (k ConfigErrorKind) Error() string {
	switch k {
	case ErrMRUTooSmall:
		return "MRU too small"
	case ErrInvalidTimers:
		return "invalid timers"
	case ErrInvalidKeepalive:
		return "invalid keepalive"
	case ErrCredentialTooLong:
		return "credential too long"
	case ErrInvalidAddress:
		return "invalid address"
	case ErrInvalidAuthProtocol:
		return "invalid authentication protocol"
	default:
		return fmt.Sprintf("ppp.ConfigErrorKind(%d)", int(k))
	}
}

// ConfigError is a configuration the endpoint cannot work with.
type ConfigError struct {
	// Kind classifies the error.
	Kind ConfigErrorKind
	// MRU is the refused MRU (ErrMRUTooSmall).
	MRU uint16
	// Credential is "username" or "password" (ErrCredentialTooLong).
	Credential string
	// Address is the refused server address (ErrInvalidAddress).
	Address netip.Addr
	// Protocol is the refused authentication protocol
	// (ErrInvalidAuthProtocol).
	Protocol AuthProtocol
}

// Error returns the message, such as "MRU 20 is below the IPv4 minimum of
// 68".
func (e *ConfigError) Error() string {
	switch e.Kind {
	case ErrMRUTooSmall:
		return fmt.Sprintf("MRU %d is below the IPv4 minimum of %d", e.MRU, MinMRU)
	case ErrInvalidTimers:
		return "the restart timer and retry limits must be greater than zero"
	case ErrInvalidKeepalive:
		return "keepalive needs a non-zero interval and miss limit"
	case ErrCredentialTooLong:
		return fmt.Sprintf("the %s is longer than %d bytes", e.Credential, maxCredentialLen)
	case ErrInvalidAddress:
		return fmt.Sprintf("the server's addresses must be IPv4, not %v", e.Address)
	case ErrInvalidAuthProtocol:
		return fmt.Sprintf("%d is not an authentication protocol", int(e.Protocol))
	default:
		return e.Kind.Error()
	}
}

// Is reports whether target is e's kind, so that errors.Is(err,
// ErrMRUTooSmall) matches every *ConfigError of that kind.
func (e *ConfigError) Is(target error) bool {
	kind, ok := target.(ConfigErrorKind)
	return ok && kind == e.Kind
}
