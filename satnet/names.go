package satnet

import (
	"maps"
	"net/netip"
	"slices"
	"time"
)

// Answers for host names that need no DNS query: fixed local entries, and
// recent answers. Every query costs a satellite round trip of airtime.

// nameCache holds the configured hosts and the DNS answers still fresh. It
// is not safe for concurrent use: the engine's lock guards it.
type nameCache struct {
	// hosts maps normalised names to their fixed addresses.
	hosts map[string]netip.Addr
	// answers maps normalised names to what DNS said about them.
	answers map[string]cachedAnswer
	// lifetime is how long an answer is reused.
	lifetime time.Duration
}

// cachedAnswer is a DNS answer and when it stops being reused.
type cachedAnswer struct {
	addresses []netip.Addr
	expires   time.Time
}

// newNameCache returns a cache of hosts, which maps names to fixed
// addresses, keeping DNS answers for lifetime. It keeps its own copy of
// hosts, normalised; names that are not valid host names are left out.
func newNameCache(hosts map[string]netip.Addr, lifetime time.Duration) *nameCache {
	normalised := make(map[string]netip.Addr, len(hosts))
	for name, address := range hosts {
		if canonical, ok := normalizeHost(name); ok {
			normalised[canonical] = address
		}
	}
	return &nameCache{
		hosts:    normalised,
		answers:  map[string]cachedAnswer{},
		lifetime: lifetime,
	}
}

// lookup returns the addresses of name, which must be normalised, if they
// are known at now: a fixed host, or an answer that has not expired. The
// slice is the caller's.
func (c *nameCache) lookup(name string, now time.Time) ([]netip.Addr, bool) {
	if address, ok := c.hosts[name]; ok {
		return []netip.Addr{address}, true
	}
	answer, ok := c.answers[name]
	if !ok || !now.Before(answer.expires) {
		return nil, false
	}
	return slices.Clone(answer.addresses), true
}

// remember keeps addresses as the answer for name, learnt at now, and
// forgets the answers that have expired.
func (c *nameCache) remember(name string, addresses []netip.Addr, now time.Time) {
	maps.DeleteFunc(c.answers, func(_ string, answer cachedAnswer) bool {
		return !now.Before(answer.expires)
	})
	c.answers[name] = cachedAnswer{
		addresses: slices.Clone(addresses),
		expires:   now.Add(c.lifetime),
	}
}
