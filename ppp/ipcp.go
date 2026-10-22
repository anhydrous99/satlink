package ppp

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
)

// The IPCP option policy (RFC 1332) with DNS server discovery (RFC 1877).

// IPCP configuration option types we act on; everything else (Van Jacobson
// compression, NetBIOS name servers, …) is rejected.
const (
	ipcpOptionIPAddress    uint8 = 3
	ipcpOptionPrimaryDNS   uint8 = 129
	ipcpOptionSecondaryDNS uint8 = 131
)

// dnsServerOptions are the DNS server options, primary first: an option's
// index is that of the server it asks for. It is never assigned to.
var dnsServerOptions = [...]uint8{ipcpOptionPrimaryDNS, ipcpOptionSecondaryDNS}

// limitedBroadcast is the broadcast address of every IPv4 network (RFC
// 919), 255.255.255.255.
var limitedBroadcast = netip.AddrFrom4([ipv4Len]byte{0xFF, 0xFF, 0xFF, 0xFF})

// errNoAddressAssigned is why a client cannot work with a peer that refuses
// its IP-Address option.
var errNoAddressAssigned = errors.New("the peer will not assign an IPv4 address")

// dnsIndex returns which server an option of the given kind asks for, and
// false for an option that is not a DNS server's.
func dnsIndex(kind uint8) (int, bool) {
	index := slices.Index(dnsServerOptions[:], kind)
	return index, index >= 0
}

// isHostAddress reports whether address can belong to a single host: an IP
// stack cannot use 0.0.0.0, broadcast, multicast or loopback as its own or
// a server's address. The zero Addr is not an IPv4 address at all.
func isHostAddress(address netip.Addr) bool {
	return address.Is4() &&
		!address.IsUnspecified() &&
		address != limitedBroadcast &&
		!address.IsMulticast() &&
		!address.IsLoopback()
}

// ipcpNegotiator is IPCP's half of the negotiation, for either end of the
// link.
type ipcpNegotiator interface {
	negotiator
	// settings returns the addressing agreed once IPCP is open, or why it
	// cannot be used.
	settings() (IPv4Settings, error)
}

// clientIPCP asks the peer for an address and DNS servers by proposing
// 0.0.0.0 and adopting the values the peer suggests instead (RFC 1332
// §3.3).
type clientIPCP struct {
	address netip.Addr
	// dnsServers are the servers we ask for, each the zero Addr once the
	// peer refuses to provide it.
	dnsServers [len(dnsServerOptions)]netip.Addr
	// peerAddress is the peer's own address; the zero Addr when it announced
	// none.
	peerAddress netip.Addr
}

// newClientIPCP returns a client that asks for an address and both DNS
// servers.
func newClientIPCP() *clientIPCP {
	client := &clientIPCP{address: netip.IPv4Unspecified()}
	for index := range client.dnsServers {
		client.dnsServers[index] = netip.IPv4Unspecified()
	}
	return client
}

// settings returns the address the peer assigned, the peer's own and the
// usable DNS servers.
func (c *clientIPCP) settings() (IPv4Settings, error) {
	if !isHostAddress(c.address) {
		return IPv4Settings{}, fmt.Errorf("the peer assigned %v, which is not a host address", c.address)
	}
	var dnsServers []netip.Addr
	for _, server := range c.dnsServers {
		if isHostAddress(server) {
			dnsServers = append(dnsServers, server)
		}
	}
	return IPv4Settings{LocalAddress: c.address, PeerAddress: c.peerAddress, DNSServers: dnsServers}, nil
}

// ownOptions proposes our address and asks for the DNS servers the peer has
// not refused.
func (c *clientIPCP) ownOptions() []configOption {
	options := []configOption{ipv4Option(ipcpOptionIPAddress, c.address)}
	for index, kind := range dnsServerOptions {
		if server := c.dnsServers[index]; server.IsValid() {
			options = append(options, ipv4Option(kind, server))
		}
	}
	return options
}

// applyNak adopts the address and DNS servers the peer suggests.
func (c *clientIPCP) applyNak(suggestions []configOption) error {
	for _, suggestion := range suggestions {
		// A suggestion we could never use is ignored: the peer then has to
		// offer something else, or give up.
		value, ok := suggestion.ipv4Value()
		if !ok || !isHostAddress(value) {
			continue
		}
		c.adopt(suggestion.kind, value)
	}
	return nil
}

// adopt takes value as our address or as a DNS server we still ask for.
func (c *clientIPCP) adopt(kind uint8, value netip.Addr) {
	if kind == ipcpOptionIPAddress {
		c.address = value
		return
	}
	if index, ok := dnsIndex(kind); ok && c.dnsServers[index].IsValid() {
		c.dnsServers[index] = value
	}
}

// applyReject stops asking for the DNS servers the peer refused; a client
// cannot do without an address.
func (c *clientIPCP) applyReject(rejected []configOption) error {
	for _, option := range rejected {
		if option.kind == ipcpOptionIPAddress {
			return errNoAddressAssigned
		}
		if index, ok := dnsIndex(option.kind); ok {
			c.dnsServers[index] = netip.Addr{}
		}
	}
	return nil
}

// judge accepts the peer's announcement of its own address, and nothing
// else.
func (c *clientIPCP) judge(option configOption) verdict {
	address, ok := option.ipv4Value()
	// A peer proposing 0.0.0.0 wants us to pick its address, which a client
	// cannot do.
	if option.kind == ipcpOptionIPAddress && ok && !address.IsUnspecified() {
		return ackVerdict()
	}
	return rejectVerdict()
}

// acceptPeerOptions records the address the peer announced, if any.
func (c *clientIPCP) acceptPeerOptions(options []configOption) {
	c.peerAddress = netip.Addr{}
	for _, option := range options {
		if option.kind == ipcpOptionIPAddress {
			// A malformed value leaves the zero Addr: no address.
			c.peerAddress, _ = option.ipv4Value()
			return
		}
	}
}

// serverIPCP hands out a fixed address and DNS servers by Nak-ing anything
// else.
type serverIPCP struct {
	// announcedAddress is our address as we propose it; the zero Addr once
	// the peer refuses to hear it.
	announcedAddress netip.Addr
	localAddress     netip.Addr
	assignedAddress  netip.Addr
	dnsServers       []netip.Addr
}

// newServerIPCP returns a server that hands out config's addressing, at
// most two DNS servers included.
func newServerIPCP(config ServerConfig) *serverIPCP {
	offered := config.DNSServers[:min(len(config.DNSServers), len(dnsServerOptions))]
	return &serverIPCP{
		announcedAddress: config.LocalAddress,
		localAddress:     config.LocalAddress,
		assignedAddress:  config.PeerAddress,
		dnsServers:       slices.Clone(offered),
	}
}

// settings returns the addressing the server hands out; it cannot fail.
func (s *serverIPCP) settings() (IPv4Settings, error) {
	return IPv4Settings{
		LocalAddress: s.localAddress,
		PeerAddress:  s.assignedAddress,
		DNSServers:   slices.Clone(s.dnsServers),
	}, nil
}

// ownOptions announces our address, unless the peer refused to hear it.
func (s *serverIPCP) ownOptions() []configOption {
	if !s.announcedAddress.IsValid() {
		return nil
	}
	return []configOption{ipv4Option(ipcpOptionIPAddress, s.announcedAddress)}
}

// applyNak changes nothing: our address is fixed, so the request simply
// goes out again.
func (s *serverIPCP) applyNak([]configOption) error {
	return nil
}

// applyReject stops announcing our address once the peer refuses to hear
// it.
func (s *serverIPCP) applyReject(rejected []configOption) error {
	isAddress := func(option configOption) bool { return option.kind == ipcpOptionIPAddress }
	if slices.ContainsFunc(rejected, isAddress) {
		s.announcedAddress = netip.Addr{}
	}
	return nil
}

// judge accepts exactly the addressing we hand out and suggests it instead
// of anything else.
func (s *serverIPCP) judge(option configOption) verdict {
	offer, ok := s.offerFor(option.kind)
	if !ok {
		return rejectVerdict()
	}
	if proposed, ok := option.ipv4Value(); ok && proposed == offer {
		return ackVerdict()
	}
	return nakVerdict(offer.AsSlice())
}

// offerFor returns the address we hand out through an option of the given
// kind, and false for an option we do not provide.
func (s *serverIPCP) offerFor(kind uint8) (netip.Addr, bool) {
	if kind == ipcpOptionIPAddress {
		return s.assignedAddress, true
	}
	index, ok := dnsIndex(kind)
	if !ok || index >= len(s.dnsServers) {
		return netip.Addr{}, false
	}
	return s.dnsServers[index], true
}

// acceptPeerOptions records nothing: the client's options are ours.
func (s *serverIPCP) acceptPeerOptions([]configOption) {}
