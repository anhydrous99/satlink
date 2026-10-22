package mobile

import (
	"net/netip"

	"github.com/anhydrous99/satlink/satnet"
)

// From the app's LinkSettings to satnet's configuration. What only satnet
// can judge — durations, limits too small to be of use, the escape guard —
// it checks itself when the link is established, before dialling; this
// side checks what its own conversion needs: the rules, the hosts'
// addresses, a byte limit that an unsigned count cannot hold.

// negativeLimit is the reason a negative limit is refused, worded as satnet
// words it for the limits it checks itself.
const negativeLimit = "limits must not be negative; use 0 for no limit"

// invalidIPv4Syntax is the reason a host address that is not IPv4 is
// refused, as the Rust bindings gave it: the error text of Rust's Ipv4Addr
// parser, which names no address.
const invalidIPv4Syntax = "invalid IPv4 address syntax"

// config returns the link configuration the settings describe, with
// cancellation, if not nil, to stop the link's setup. It refuses with
// SatlinkErrorInvalidArgument rules that do not parse, host addresses that
// are not IPv4 and a negative MaxBytes.
func (s *LinkSettings) config(cancellation *Cancellation) (satnet.Config, error) {
	allowRules, hostEntries := s.entries()
	policy, err := satnet.ParsePolicy(allowRules...)
	if err != nil {
		return satnet.Config{}, fromError(err)
	}
	limits, err := s.limits()
	if err != nil {
		return satnet.Config{}, err
	}
	hosts, err := hostAddresses(hostEntries)
	if err != nil {
		return satnet.Config{}, err
	}
	config := satnet.NewConfig(policy)
	if cancellation != nil {
		// Without one, the configuration keeps a cancellation of its own,
		// which nobody else can reach.
		config.Call.Cancellation = &cancellation.core
	}
	config.Number = s.Number
	config.Credentials = satnet.Credentials{Username: s.Username, Password: s.Password}
	config.Limits = limits
	config.Tuning.CloseGrace = durationFromSeconds(s.CloseGraceSeconds)
	config.Hosts = hosts
	return config, nil
}

// limits returns the link's limits; negative durations pass on to satnet,
// which refuses them, while a negative MaxBytes is refused here, as
// satnet's byte limit is unsigned.
func (s *LinkSettings) limits() (satnet.Limits, error) {
	if s.MaxBytes < 0 {
		return satnet.Limits{}, invalidArgument(negativeLimit)
	}
	return satnet.Limits{
		MaxCallDuration: durationFromSeconds(s.MaxCallSeconds),
		MaxBytes:        uint64(s.MaxBytes),
		IdleTimeout:     durationFromSeconds(s.IdleTimeoutSeconds),
	}, nil
}

// hostAddresses returns hosts, the names answered locally with their
// addresses as text, each address checked to be IPv4.
func hostAddresses(hosts map[string]string) (map[string]netip.Addr, error) {
	addresses := make(map[string]netip.Addr, len(hosts))
	for name, text := range hosts {
		address, err := netip.ParseAddr(text)
		if err != nil || !address.Is4() {
			return nil, invalidArgument(invalidIPv4Syntax)
		}
		addresses[name] = address
	}
	return addresses, nil
}
