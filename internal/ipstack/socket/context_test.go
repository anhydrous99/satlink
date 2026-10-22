package socket

import (
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"
)

// epoch is smoltcp's Instant::ZERO, from which every test time derives.
var epoch = time.Time{}

// newTestContext returns the context of smoltcp's tests::setup(Medium::Ip):
// addresses 192.168.1.1/24 and 127.0.0.1/8, an MTU of 1500 and seed 0.
func newTestContext(t *testing.T) *Context {
	t.Helper()
	cx := NewContext(epoch, 0, 1500)
	setAddrs(t, cx, "192.168.1.1/24", "127.0.0.1/8")
	return cx
}

func setAddrs(t *testing.T, cx *Context, prefixes ...string) {
	t.Helper()
	addrs := make([]netip.Prefix, len(prefixes))
	for i, prefix := range prefixes {
		addrs[i] = netip.MustParsePrefix(prefix)
	}
	if err := cx.SetIPAddrs(addrs); err != nil {
		t.Fatalf("SetIPAddrs(%v): %v", prefixes, err)
	}
}

func addr(text string) netip.Addr {
	return netip.MustParseAddr(text)
}

// replaceFirstAddr does what smoltcp's tests do with update_ip_addrs and
// iter_mut().next(): it replaces the first address, keeping the others.
func replaceFirstAddr(t *testing.T, cx *Context, prefix string) {
	t.Helper()
	addrs := cx.IPAddrs()
	addrs[0] = netip.MustParsePrefix(prefix)
	if err := cx.SetIPAddrs(addrs); err != nil {
		t.Fatalf("SetIPAddrs: %v", err)
	}
}

func assertBroadcasts(t *testing.T, cx *Context, broadcasts map[string]bool) {
	t.Helper()
	for address, want := range broadcasts {
		if got := cx.IsBroadcast(addr(address)); got != want {
			t.Errorf("with %v, IsBroadcast(%s) = %v, want %v", cx.IPAddrs(), address, got, want)
		}
	}
}

// Ported from smoltcp's iface test test_local_subnet_broadcasts.
func TestContextLocalSubnetBroadcasts(t *testing.T) {
	t.Parallel()
	cx := newTestContext(t)

	replaceFirstAddr(t, cx, "192.168.1.23/24")
	assertBroadcasts(t, cx, map[string]bool{
		"255.255.255.255": true,
		"255.255.255.254": false,
		"192.168.1.255":   true,
		"192.168.1.254":   false,
	})

	replaceFirstAddr(t, cx, "192.168.23.24/16")
	assertBroadcasts(t, cx, map[string]bool{
		"255.255.255.255": true,
		"255.255.255.254": false,
		"192.168.23.255":  false,
		"192.168.23.254":  false,
		"192.168.255.254": false,
		"192.168.255.255": true,
	})

	replaceFirstAddr(t, cx, "192.168.23.24/8")
	assertBroadcasts(t, cx, map[string]bool{
		"255.255.255.255": true,
		"255.255.255.254": false,
		"192.23.1.255":    false,
		"192.23.1.254":    false,
		"192.255.255.254": false,
		"192.255.255.255": true,
	})
}

// Ported from smoltcp's iface test get_source_address.
func TestContextSourceAddress(t *testing.T) {
	t.Parallel()
	cx := newTestContext(t)
	setAddrs(t, cx, "172.18.1.2/24", "172.24.24.14/24")
	cases := map[string]string{
		"172.18.1.254":  "172.18.1.2",   // in the first network
		"172.24.24.12":  "172.24.24.14", // in the second network
		"172.24.23.254": "172.18.1.2",   // in neither: the first address
	}
	for destination, want := range cases {
		got, ok := cx.SourceAddress(addr(destination))
		if !ok || got != addr(want) {
			t.Errorf("SourceAddress(%s) = %v, %v; want %s", destination, got, ok, want)
		}
	}
}

// Ported from smoltcp's iface test get_source_address_empty_interface.
func TestContextSourceAddressEmptyInterface(t *testing.T) {
	t.Parallel()
	cx := newTestContext(t)
	setAddrs(t, cx)

	for _, destination := range []string{"172.18.1.254", "172.24.24.12", "172.24.23.254"} {
		if got, ok := cx.SourceAddress(addr(destination)); ok {
			t.Errorf("SourceAddress(%s) = %v, want none", destination, got)
		}
	}
}

func TestContextSourceAddressRefusesNonIPv4Destinations(t *testing.T) {
	t.Parallel()
	cx := newTestContext(t)

	for _, destination := range []netip.Addr{{}, addr("::1"), addr("::ffff:192.168.1.2")} {
		if got, ok := cx.SourceAddress(destination); ok {
			t.Errorf("SourceAddress(%v) = %v, want none", destination, got)
		}
	}
}

func TestContextSetIPAddrsRejectsAddressesAnInterfaceCannotHave(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		prefix   netip.Prefix
		wantText string
	}{
		"multicast":             {netip.MustParsePrefix("224.0.0.1/24"), "invalid interface address: IP address 224.0.0.1 is not unicast"},
		"limited broadcast":     {netip.MustParsePrefix("255.255.255.255/32"), "invalid interface address: IP address 255.255.255.255 is not unicast"},
		"IPv6":                  {netip.MustParsePrefix("2001:db8::1/64"), "invalid interface address: IP address 2001:db8::1 is not IPv4"},
		"IPv4-mapped IPv6":      {netip.MustParsePrefix("::ffff:192.168.1.1/120"), "invalid interface address: IP address ::ffff:192.168.1.1 is not IPv4"},
		"invalid prefix length": {netip.PrefixFrom(addr("192.168.1.1"), 33), "invalid interface address: IP address 192.168.1.1 has an invalid prefix length"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cx := newTestContext(t)
			before := cx.IPAddrs()

			err := cx.SetIPAddrs([]netip.Prefix{netip.MustParsePrefix("10.0.0.1/8"), c.prefix})

			if !errors.Is(err, ErrInvalidIPAddr) || err.Error() != c.wantText {
				t.Errorf("SetIPAddrs: %v, want %q", err, c.wantText)
			}
			if after := cx.IPAddrs(); !slices.Equal(after, before) {
				t.Errorf("addresses changed to %v", after)
			}
		})
	}
}

func TestContextSetIPAddrsAcceptsTheUnspecifiedAddress(t *testing.T) {
	t.Parallel()
	cx := NewContext(epoch, 0, 1500)

	setAddrs(t, cx, "0.0.0.0/0")

	if got := cx.IPAddrs(); !slices.Equal(got, []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}) {
		t.Errorf("IPAddrs() = %v", got)
	}
}

func TestContextIPAddrsReturnsACopy(t *testing.T) {
	t.Parallel()
	cx := newTestContext(t)

	cx.IPAddrs()[0] = netip.MustParsePrefix("10.0.0.1/8")

	if first := cx.IPAddrs()[0]; first != netip.MustParsePrefix("192.168.1.1/24") {
		t.Errorf("first address = %v after changing the copy", first)
	}
}

func TestContextSetIPAddrsKeepsItsOwnCopy(t *testing.T) {
	t.Parallel()
	cx := NewContext(epoch, 0, 1500)
	addrs := []netip.Prefix{netip.MustParsePrefix("192.168.1.1/24")}
	if err := cx.SetIPAddrs(addrs); err != nil {
		t.Fatalf("SetIPAddrs: %v", err)
	}

	addrs[0] = netip.MustParsePrefix("10.0.0.1/8")

	if !cx.HasIPAddr(addr("192.168.1.1")) {
		t.Error("the context follows the caller's slice")
	}
}

func TestContextHasIPAddr(t *testing.T) {
	t.Parallel()
	cx := newTestContext(t)
	cases := map[string]struct {
		anyIP bool
		addr  string
		want  bool
	}{
		"own address":                {addr: "192.168.1.1", want: true},
		"second own address":         {addr: "127.0.0.1", want: true},
		"same network, other host":   {addr: "192.168.1.2", want: false},
		"any-IP, foreign address":    {anyIP: true, addr: "8.8.8.8", want: true},
		"any-IP, broadcast":          {anyIP: true, addr: "255.255.255.255", want: true},
		"any-IP off, broadcast":      {addr: "255.255.255.255", want: false},
		"any-IP off, foreign":        {addr: "8.8.8.8", want: false},
		"any-IP off, network prefix": {addr: "192.168.1.0", want: false},
	}
	for name, c := range cases {
		cx.SetAnyIP(c.anyIP)
		if got := cx.HasIPAddr(addr(c.addr)); got != c.want {
			t.Errorf("%s: HasIPAddr(%s) = %v, want %v", name, c.addr, got, c.want)
		}
	}
}

func TestContextIPv4Addr(t *testing.T) {
	t.Parallel()
	cx := NewContext(epoch, 0, 1500)
	if got, ok := cx.IPv4Addr(); ok {
		t.Errorf("IPv4Addr() of an interface without addresses = %v", got)
	}

	setAddrs(t, cx, "10.0.0.2/8", "192.168.1.1/24")

	if got, ok := cx.IPv4Addr(); !ok || got != addr("10.0.0.2") {
		t.Errorf("IPv4Addr() = %v, %v; want the first address", got, ok)
	}
}

func TestContextIsUnicast(t *testing.T) {
	t.Parallel()
	cx := newTestContext(t)
	cases := map[string]bool{
		"192.168.1.2":     true,
		"8.8.8.8":         true,
		"192.168.1.255":   false, // the broadcast address of 192.168.1.1/24
		"127.255.255.255": false, // the broadcast address of 127.0.0.1/8
		"255.255.255.255": false,
		"224.0.0.1":       false,
		"0.0.0.0":         false,
		"::1":             false,
	}
	for address, want := range cases {
		if got := cx.IsUnicast(addr(address)); got != want {
			t.Errorf("IsUnicast(%s) = %v, want %v", address, got, want)
		}
	}
}

func TestContextAccessors(t *testing.T) {
	t.Parallel()
	later := epoch.Add(250 * time.Millisecond)
	cx := NewContext(epoch, 0, 1280)

	cx.SetNow(later)
	cx.SetAnyIP(true)

	if cx.Now() != later || cx.IPMTU() != 1280 || !cx.AnyIP() {
		t.Errorf("Now %v, IPMTU %d, AnyIP %v; want %v, 1280, true", cx.Now(), cx.IPMTU(), cx.AnyIP(), later)
	}
	if NewContext(epoch, 0, 1280).AnyIP() {
		t.Error("a new context has any-IP on")
	}
}

func TestZeroContextIsAtTheEpochSeededWithZeroWithoutMTUOrAddresses(t *testing.T) {
	t.Parallel()
	var cx Context

	if !cx.Now().IsZero() || cx.IPMTU() != 0 || len(cx.IPAddrs()) != 0 || cx.AnyIP() {
		t.Errorf("zero context: Now %v, IPMTU %d, IPAddrs %v, AnyIP %t; want the epoch, 0, none, false", cx.Now(), cx.IPMTU(), cx.IPAddrs(), cx.AnyIP())
	}
	if first := cx.Rand().Uint32(); first != 0x3 {
		t.Errorf("the zero context's first Uint32 is %#x, want seed 0's 0x3", first)
	}
}
