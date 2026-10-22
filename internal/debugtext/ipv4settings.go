package debugtext

import (
	"fmt"
	"net/netip"
	"strings"
)

// IPv4Settings returns the IPv4 settings PPP negotiated in the Debug style
// of a struct: "Ipv4Settings { local_address: 10.64.0.2,
// peer_address: Some(10.64.0.1), dns_servers: [10.64.0.1, 10.64.0.3] }",
// with None for a peer that announced no address, the zero Addr. It takes
// the fields of a ppp.IPv4Settings rather than the struct, so that this
// package imports nothing of the module's and the core can quote with it.
func IPv4Settings(local, peer netip.Addr, dnsServers []netip.Addr) string {
	return fmt.Sprintf("Ipv4Settings { local_address: %v, peer_address: %s, dns_servers: %s }",
		local, optionalAddress(peer), addressList(dnsServers))
}

// optionalAddress writes an address that may be absent, the zero Addr, as
// Some(address) or None.
func optionalAddress(address netip.Addr) string {
	if !address.IsValid() {
		return "None"
	}
	return "Some(" + address.String() + ")"
}

// addressList writes addresses in brackets, separated by a comma and a
// space.
func addressList(addresses []netip.Addr) string {
	texts := make([]string, len(addresses))
	for i, address := range addresses {
		texts[i] = address.String()
	}
	return "[" + strings.Join(texts, ", ") + "]"
}
