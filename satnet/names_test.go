package satnet

import (
	"net/netip"
	"slices"
	"testing"
	"time"
)

// cacheAddress is the address the name cache tests look up.
var cacheAddress = netip.AddrFrom4([4]byte{192, 0, 2, 1})

// cacheEpoch is the moment the name cache tests start from.
var cacheEpoch = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// cacheQuery is a name looked up in the cache, and when.
type cacheQuery struct {
	name string
	at   time.Time
}

// expectLookup checks what the cache knows about a name at a moment: want,
// or nothing for nil.
func expectLookup(t *testing.T, cache *nameCache, query cacheQuery, want []netip.Addr) {
	t.Helper()
	got, ok := cache.lookup(query.name, query.at)
	if ok != (want != nil) || !slices.Equal(got, want) {
		t.Errorf("lookup(%q) = %v, %t; want %v", query.name, got, ok, want)
	}
}

func TestFixedHostsAreNormalisedAndNeverExpire(t *testing.T) {
	cache := newNameCache(map[string]netip.Addr{"Relay.Example.com.": cacheAddress}, 0)
	muchLater := cacheEpoch.Add(1_000_000 * time.Second)

	expectLookup(t, cache, cacheQuery{"relay.example.com", muchLater}, []netip.Addr{cacheAddress})
}

func TestAnswersExpire(t *testing.T) {
	cache := newNameCache(nil, time.Minute)

	cache.remember("example.com", []netip.Addr{cacheAddress}, cacheEpoch)

	expectLookup(t, cache, cacheQuery{"example.com", cacheEpoch.Add(59 * time.Second)}, []netip.Addr{cacheAddress})
	expectLookup(t, cache, cacheQuery{"example.com", cacheEpoch.Add(60 * time.Second)}, nil)
}

func TestInvalidHostNamesAreLeftOut(t *testing.T) {
	cache := newNameCache(map[string]netip.Addr{"not a host": cacheAddress}, time.Minute)

	if len(cache.hosts) != 0 {
		t.Errorf("the cache keeps %v", cache.hosts)
	}
}

func TestTheCacheKeepsItsOwnCopies(t *testing.T) {
	hosts := map[string]netip.Addr{"relay.example": cacheAddress}
	cache := newNameCache(hosts, time.Minute)
	answer := []netip.Addr{cacheAddress}
	cache.remember("example.com", answer, cacheEpoch)

	hosts["relay.example"] = netip.Addr{}
	answer[0] = netip.Addr{}
	looked, _ := cache.lookup("example.com", cacheEpoch)
	looked[0] = netip.Addr{}

	expectLookup(t, cache, cacheQuery{"relay.example", cacheEpoch}, []netip.Addr{cacheAddress})
	expectLookup(t, cache, cacheQuery{"example.com", cacheEpoch}, []netip.Addr{cacheAddress})
}

func TestRememberingForgetsExpiredAnswers(t *testing.T) {
	cache := newNameCache(nil, time.Minute)
	cache.remember("old.example", []netip.Addr{cacheAddress}, cacheEpoch)

	cache.remember("new.example", []netip.Addr{cacheAddress}, cacheEpoch.Add(time.Hour))

	if _, kept := cache.answers["old.example"]; kept {
		t.Error("the expired answer is kept")
	}
}
