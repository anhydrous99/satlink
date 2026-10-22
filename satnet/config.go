package satnet

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/rustdebug"
	"github.com/anhydrous99/satlink/ppp"
)

// What to dial, what the app may reach, and the limits that keep airtime in
// check.

// minMTU is the smallest MTU that leaves room for useful TCP segments after
// 40 bytes of headers.
const minMTU = 296

// maxMTU is PPP's default MRU; larger packets would only be fragmented on
// the way.
const maxMTU = 1500

// maxDuration bounds configured durations, far beyond any sensible setting,
// so that deadline arithmetic cannot overflow.
const maxDuration = 365 * 24 * time.Hour

// minMaxBytes is the smallest data limit: PPP negotiation alone moves
// 350–500 bytes over Iridium, authentication included, so a data limit
// under this would end the call before a request and its answer could
// pass.
const minMaxBytes = 2048

// athAllowance is what ATH and its answer take, on top of the escape's
// guard times.
const athAllowance = time.Second

// linkSetupTime is how long PPP takes to come up over Iridium — 7 to 10
// seconds of round trips for LCP, authentication and IPCP — with room to
// use the link after.
const linkSetupTime = 20 * time.Second

// minIdleTimeout is the shortest idle limit. The idle limit only counts
// what the app does, not what it waits for: a connection or a request waits
// a round trip or more for its answer — 1.5 to 3 seconds over Iridium,
// longer behind other connections' data — and a lookup up to longestLookup
// when a DNS server does not answer. A shorter limit hangs up while they
// wait.
const minIdleTimeout = longestLookup + 5*time.Second

// IridiumInternetNumber is Iridium's own internet gateway (the "PPP data
// service", formerly Direct Internet). It is what Iridium's guides and
// ModemManager dial; whether a SIM may use it depends on the service plan.
const IridiumInternetNumber = "008816000025"

// IridiumInternetBearer returns the bearer Iridium documents for its
// gateway: 9600 bit/s V.110, error-corrected.
func IridiumInternetBearer() satlink.BearerService {
	return satlink.BearerService{Speed: 71, Name: 0, ConnectionElement: 1}
}

// Credentials are the PPP name and password; Iridium's gateway accepts any.
type Credentials = ppp.Credentials

// Config is everything needed to bring a satellite IP link up and keep it
// within bounds. Start from [NewConfig]. [Establish] checks it before
// dialling, as [Config.Validate] does, so a configuration that cannot work
// never costs airtime, and keeps its own copy.
type Config struct {
	// Number is dialled for the data call.
	Number string
	// Call holds the bearer, call-setup timeout and escape guard for the
	// data call, and its Cancellation, which stops [Establish] from another
	// goroutine. The escape guard must be at most satlink.MaxEscapeGuard;
	// it sets how long hanging up takes, which a call time limit must allow
	// for (see [Limits]).
	Call satlink.DataCallSettings
	// Credentials are the PPP credentials; Iridium's gateway accepts any.
	Credentials Credentials
	// Policy lists the destinations the app may reach. Nothing else is
	// ever sent, but for a TCP reset answering a segment that belongs to no
	// connection.
	Policy Policy
	// Limits are the airtime, volume and idle limits.
	Limits Limits
	// Hosts are names answered locally, saving a DNS round trip of airtime
	// each. Names are matched without regard to case or a trailing dot; the
	// addresses must be IPv4.
	Hosts map[string]netip.Addr
	// Tuning holds the transport parameters for the satellite link.
	Tuning Tuning
}

// NewConfig returns a configuration for Iridium's internet gateway
// ([IridiumInternetNumber] with [IridiumInternetBearer] and the default
// call settings, a new satlink.Cancellation among them), no PPP
// credentials, [DefaultLimits], no local hosts, [DefaultTuning], and policy
// deciding what the app may reach.
func NewConfig(policy Policy) Config {
	bearer := IridiumInternetBearer()
	call := satlink.DefaultDataCallSettings()
	call.Bearer = &bearer
	return Config{
		Number:      IridiumInternetNumber,
		Call:        call,
		Credentials: Credentials{},
		Policy:      policy,
		Limits:      DefaultLimits(),
		Hosts:       map[string]netip.Addr{},
		Tuning:      DefaultTuning(),
	}
}

// Limits are the points at which the link hangs up by itself. Airtime is
// billed by the minute whether or not data flows, so an idle link must not
// linger. Zero means no limit; negative values are refused, and so are
// durations over a year.
//
// A limit reached before the link is any use would be a billed call for
// nothing, so [Establish] refuses it too: a MaxCallDuration shorter than
// bringing PPP up (20 s) and hanging up take — twice the escape guard,
// plus a second for ATH and its answer: 24 s by default — an IdleTimeout
// under 25 s, shorter than an answer may take to come (a DNS lookup may
// wait 10 s for each of two servers), or a MaxBytes under 2048 bytes, as
// PPP alone takes up to 500.
type Limits struct {
	// MaxCallDuration is the longest the call may last, hanging up
	// included: the hang-up starts early enough to end the call in time.
	// The other limits start the hang-up when they are reached, and it
	// takes a few seconds.
	MaxCallDuration time.Duration
	// MaxBytes is the most bytes that may cross the serial line, both
	// directions together.
	MaxBytes uint64
	// IdleTimeout is how long the link may carry no application traffic
	// before hanging up. Negotiating PPP is not idling, and the peer
	// renegotiating later is not traffic.
	IdleTimeout time.Duration
}

// DefaultLimits returns a 60-minute call limit, no data limit, and a
// 5-minute idle timeout.
func DefaultLimits() Limits {
	return Limits{
		MaxCallDuration: 60 * time.Minute,
		MaxBytes:        0,
		IdleTimeout:     5 * time.Minute,
	}
}

// Tuning holds the transport parameters, tuned by default for Iridium
// circuit-switched data: about 300 bytes per second with round trips of 1.5
// to 3 seconds. Durations must be between zero and a year.
type Tuning struct {
	// MTU is the largest IP packet we send, and the most we ask the peer to
	// send; 296 to 1500. A 576-byte packet already holds the line for two
	// seconds; larger ones would delay everything behind them. The peer's
	// own limit applies too.
	MTU int

	// LinkRate is the bytes per second handed to the handset, PPP framing
	// included; above zero. Packets are released no faster, so a backlog
	// waits in the app rather than in the handset's buffer: in front of the
	// line, about a packet per connection, made when its turn came. Set too
	// high, packets queue in the handset and everything behind them waits;
	// set too low, the line sits idle.
	LinkRate int

	// RepeatSuppression is how soon after the original leaves the line a
	// repeated TCP segment or DNS query is dropped, as no answer could have
	// arrived yet. See [DefaultTuning]. Zero disables it.
	RepeatSuppression time.Duration

	// TCPReceiveBuffer is the TCP receive window per connection, at least
	// MTU. The link holds about 1 KB in flight, so a small window costs
	// nothing and limits duplicate data.
	TCPReceiveBuffer int

	// TCPSendBuffer is the TCP send buffer per connection, at least MTU.
	TCPSendBuffer int

	// TCPTimeout is how long a connection may go without hearing from its
	// peer; zero sets none, so that a connection to a peer that has gone
	// waits for as long as the link lives. (The Rust version took zero for
	// a timeout at once, which failed every connection before its SYN
	// left.)
	TCPTimeout time.Duration

	// TCPKeepalive is the interval of keepalive probes on idle connections,
	// so that they outlive TCPTimeout; zero sends none.
	TCPKeepalive time.Duration

	// NegotiationTimeout is how long PPP may take to come up after CONNECT.
	NegotiationTimeout time.Duration

	// DNSCacheLifetime is how long a DNS answer is reused before asking
	// again.
	DNSCacheLifetime time.Duration

	// CloseGrace is how long closing the link waits for data already
	// written to be delivered before hanging up.
	CloseGrace time.Duration
}

// DefaultTuning returns parameters for Iridium.
//
// The TCP/IP stack starts every connection with a one-second retransmission
// timeout, and resends DNS queries after one second too: both shorter than a
// satellite round trip, so first copies are always resent before an answer
// can arrive. Suppressing repeats within 3.5 s of the original leaving the
// line removes that waste; a packet that was really lost is still recovered
// by a later retransmission.
//
// Iridium's 2.4 kbit/s line carries about 300 bytes per second of PPP
// frames, which is where the pacing is set.
func DefaultTuning() Tuning {
	return Tuning{
		MTU:                576,
		LinkRate:           300,
		RepeatSuppression:  3500 * time.Millisecond,
		TCPReceiveBuffer:   2048,
		TCPSendBuffer:      4096,
		TCPTimeout:         120 * time.Second,
		TCPKeepalive:       60 * time.Second,
		NegotiationTimeout: 90 * time.Second,
		DNSCacheLifetime:   10 * time.Minute,
		CloseGrace:         30 * time.Second,
	}
}

// Validate refuses a configuration that cannot work, or limits too small to
// be of use, with an [*Error] of kind ErrInvalidConfig saying why.
// [Establish] checks this before dialling, so such a configuration never
// costs airtime; an app can check sooner, before saying a call is on its
// way.
//
// It checks in the Rust version's order — the MTU, the link rate, the TCP
// buffers, the hosts, the durations, the escape guard — and then the
// limits, which depend on how long hanging up takes.
func (c Config) Validate() error {
	if err := c.Tuning.validateSizes(); err != nil {
		return err
	}
	if err := validateHosts(c.Hosts); err != nil {
		return err
	}
	if err := validateDurations(c.durations()); err != nil {
		return err
	}
	if c.Call.EscapeGuard > satlink.MaxEscapeGuard {
		return invalidConfig(fmt.Sprintf("the escape guard must be at most %v", satlink.MaxEscapeGuard))
	}
	return c.Limits.validate(c.hangUpTime())
}

// hangUpTime is how long hanging up takes: silence, +++, silence again,
// then ATH and its answer. Once the configuration is valid, the guard is
// between zero and satlink.MaxEscapeGuard, so this is between 1 s and 21 s.
func (c Config) hangUpTime() time.Duration {
	return 2*c.Call.EscapeGuard + athAllowance
}

// durations returns every configured duration that must be between zero and
// a year: the tuning's and the call's.
func (c Config) durations() []time.Duration {
	tuning := &c.Tuning
	return []time.Duration{
		tuning.RepeatSuppression,
		tuning.TCPTimeout,
		tuning.NegotiationTimeout,
		tuning.DNSCacheLifetime,
		tuning.CloseGrace,
		c.Call.ConnectTimeout,
		c.Call.EscapeGuard,
		tuning.TCPKeepalive,
	}
}

// validateSizes checks the MTU, the link rate and the TCP buffers.
func (t *Tuning) validateSizes() error {
	if t.MTU < minMTU || t.MTU > maxMTU {
		return invalidConfig(fmt.Sprintf("MTU must be between %d and %d bytes", minMTU, maxMTU))
	}
	if t.LinkRate <= 0 {
		return invalidConfig("the link rate must be above zero")
	}
	if t.TCPReceiveBuffer < t.MTU || t.TCPSendBuffer < t.MTU {
		return invalidConfig("TCP buffers must hold at least one full packet")
	}
	return nil
}

// validateHosts checks that every local host is a valid host name and, as
// a DNS A record holds four octets, has an IPv4 address. The names are
// checked in order, so that the error does not depend on the map's.
func validateHosts(hosts map[string]netip.Addr) error {
	names := slices.Sorted(maps.Keys(hosts))
	for _, name := range names {
		if _, ok := normalizeHost(name); !ok {
			return invalidConfig(rustdebug.Quote(name) + " in hosts is not a valid host name")
		}
	}
	for _, name := range names {
		if !hosts[name].Is4() {
			return invalidConfig(rustdebug.Quote(name) + " in hosts needs an IPv4 address")
		}
	}
	return nil
}

// validateDurations checks that durations are between zero and a year.
func validateDurations(durations []time.Duration) error {
	return checkRange(durations, "durations must not be negative", "durations must be at most a year")
}

// checkRange refuses durations below zero, saying negative, or over a year,
// saying tooLong.
func checkRange(durations []time.Duration, negative, tooLong string) error {
	if slices.ContainsFunc(durations, func(duration time.Duration) bool { return duration < 0 }) {
		return invalidConfig(negative)
	}
	if slices.ContainsFunc(durations, func(duration time.Duration) bool { return duration > maxDuration }) {
		return invalidConfig(tooLong)
	}
	return nil
}

// validate refuses limits reached before the link is any use — a call time
// that bringing PPP up and hanging up (hangUpTime) would use up, an idle
// time shorter than an answer may take to come, or about as many bytes as
// PPP negotiation takes — as a billed call for nothing, and limits that
// cannot be meant: negative ones, as zero is how to say "no limit", and
// durations over a year.
func (l *Limits) validate(hangUpTime time.Duration) error {
	err := checkRange([]time.Duration{l.MaxCallDuration, l.IdleTimeout},
		"limits must not be negative; use 0 for no limit", "limits must be at most a year")
	if err != nil {
		return err
	}
	if shortestCall := linkSetupTime + hangUpTime; l.MaxCallDuration != 0 && l.MaxCallDuration < shortestCall {
		return invalidConfig(fmt.Sprintf(
			"the call time limit must leave time to bring PPP up and to hang up: at least %v", shortestCall))
	}
	if l.IdleTimeout != 0 && l.IdleTimeout < minIdleTimeout {
		return invalidConfig(fmt.Sprintf(
			"the idle limit must be at least %v, or the link hangs up while waiting for an answer", minIdleTimeout))
	}
	if l.MaxBytes != 0 && l.MaxBytes < minMaxBytes {
		return invalidConfig(fmt.Sprintf(
			"the data limit must be at least %d bytes, as PPP alone takes up to 500", minMaxBytes))
	}
	return nil
}

// invalidConfig is the error of a configuration that cannot work, for the
// reason given.
func invalidConfig(reason string) *Error {
	return &Error{Kind: ErrInvalidConfig, Message: reason}
}
