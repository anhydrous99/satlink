package satnet

import (
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// expectPort checks what the allocator hands out next: want, or none for 0,
// which is never handed out.
func expectPort(t *testing.T, ports *portAllocator, isFree func(uint16) bool, want uint16) {
	t.Helper()
	if got, ok := ports.allocate(isFree); got != want || ok != (want != 0) {
		t.Errorf("allocate() = %d, %t; want %d", got, ok, want)
	}
}

// anyPort accepts every port.
func anyPort(uint16) bool { return true }

func TestPortsStayInTheEphemeralRangeAndSkipBusyOnes(t *testing.T) {
	ports := newPortAllocator(ephemeralPortCount - 1)

	expectPort(t, &ports, anyPort, 65535)
	expectPort(t, &ports, anyPort, firstEphemeralPort)
	expectPort(t, &ports, func(port uint16) bool { return port != firstEphemeralPort+1 }, firstEphemeralPort+2)
	expectPort(t, &ports, func(uint16) bool { return false }, 0)
}

func TestRefusesAddressesTheStackCannotTake(t *testing.T) {
	now := time.Now()
	stack := newStack(DefaultTuning(), now)

	for _, address := range []netip.Addr{
		netip.AddrFrom4([4]byte{255, 255, 255, 255}),
		netip.AddrFrom4([4]byte{224, 0, 0, 1}),
		netip.AddrFrom4([4]byte{0, 0, 0, 0}),
		// An IPv6 address, which IPv4 settings cannot hold.
		netip.MustParseAddr("2001:db8::1"),
		{},
	} {
		if err := stack.configure(IPv4Settings{LocalAddress: address}, engineMTU, now); err == nil {
			t.Errorf("configure accepted %v", address)
		}
	}
}

// unassignedProtocol is an IP protocol number no stack speaks (RFC 3692
// sets 253 aside for experiments).
const unassignedProtocol = 253

// strayPort is where the stray datagrams of the ICMP test come from: the
// echo service's (RFC 862).
const strayPort = 7

// withProtocol returns datagram as an IPv4 datagram of protocol, its
// header's checksum filled again.
func withProtocol(datagram []byte, protocol wire.IPProtocol) []byte {
	packet := wire.IPv4Packet(datagram)
	packet.SetNextHeader(protocol)
	packet.FillChecksum()
	return datagram
}

func TestTheStacksICMPErrorsNeverGo(t *testing.T) {
	toNobody := []byte("to a port nobody listens on")
	for name, datagram := range map[string][]byte{
		"port unreachable":     udpFrom(strayPort, toNobody),
		"protocol unreachable": withProtocol(udpFrom(strayPort, toNobody), unassignedProtocol),
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			s := connectionsStack(t, now)
			s.setLineReady(true)

			s.deliver(datagram)
			s.poll(now)

			if !s.device.hasOutgoing() {
				t.Fatal("the stack answered nothing, so this shows nothing")
			}
			if sent := drain(s); len(sent) != 0 {
				t.Errorf("%d datagrams went: the stack's ICMP error", len(sent))
			}
		})
	}
}

func TestTheAppsOwnDatagramToADNSServerWaitsForTheLine(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, err := s.openUDP()
	if err != nil {
		t.Fatal(err)
	}
	query := udp.Metadata{Endpoint: netip.AddrPortFrom(lookupServer, dnsPort)}
	if err := s.udp(handle).SendSlice([]byte("the app's own query"), query); err != nil {
		t.Fatal(err)
	}

	s.setLineReady(false)
	s.poll(now)

	// Taken for one of the stack's own lookups, it would go at once.
	if sent := drain(s); len(sent) != 0 {
		t.Errorf("%d datagrams went while the line was busy", len(sent))
	}
	s.setLineReady(true)
	if sent := drain(s); len(sent) != 1 {
		t.Errorf("%d datagrams went once the line was free, want the app's", len(sent))
	}
}

func TestASmallerPeerMRUShrinksWhatWeSend(t *testing.T) {
	now := time.Now()
	stack := newStack(DefaultTuning(), now)

	if err := stack.configure(clientSettings(), minMTU, now); err != nil {
		t.Fatal(err)
	}

	if got := stack.maxUDPPayload(); got != minMTU-udpOverhead {
		t.Errorf("maxUDPPayload() = %d, want %d", got, minMTU-udpOverhead)
	}
}
