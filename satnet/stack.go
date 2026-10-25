package satnet

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The TCP/IP stack (internal/ipstack, a port of smoltcp) that runs inside
// the app, fed by the PPP link. Its TCP connections are fed what the app
// wrote a segment at a time (stackconnections.go), and its name lookups
// tell an answer from silence (lookup.go).

// hostPrefixLen is the length of the interface's prefix: a point-to-point
// link needs no subnet, only our address, and no routes, because the stack
// sends everything straight to the device.
const hostPrefixLen = 32

// ackDelay is how long a bare acknowledgement waits, hoping to ride on a
// reply.
const ackDelay = 100 * time.Millisecond

// udpSlots is the number of datagram slots per UDP socket, in each
// direction.
const udpSlots = 4

// udpOverhead is the IPv4 and UDP headers, which a UDP payload must leave
// room for.
const udpOverhead = wire.IPv4HeaderLen + wire.UDPHeaderLen

// stack is the TCP/IP stack with its device and sockets. It is not safe for
// concurrent use: the engine's lock guards it.
type stack struct {
	iface   *ipstack.Interface
	device  *linkDevice
	sockets ipstack.SocketSet
	// dns is the resolver's socket. It is never removed: lookups come and
	// go within it.
	dns ipstack.SocketHandle
	// dnsServers are the DNS servers IPCP gave, as many as the resolver
	// keeps: it tries them in turn.
	dnsServers []netip.Addr
	// lookups are the lookups under way, and nextLookup the identifier of
	// the next one.
	lookups    []nameLookup
	nextLookup lookupID
	// outboxes hold what the app wrote on each connection that the stack
	// has yet to take: one for every TCP socket.
	outboxes map[ipstack.SocketHandle]*outbox
	ports    portAllocator
	// closingTCP and closingUDP are the sockets the app let go of, kept
	// until what they still hold is sent: a TCP goodbye, or UDP datagrams
	// already accepted.
	closingTCP []ipstack.SocketHandle
	closingUDP []ipstack.SocketHandle
	// cutOff are the connections that lost data still on its way, where
	// nothing else would show it: those that had data undelivered when our
	// address changed, which the stack resets without a word, emptying their
	// send buffers as if the peer had acknowledged everything; and those
	// whose outbox a reset or the hang-up emptied.
	cutOff []ipstack.SocketHandle
	tuning Tuning
}

// newStack returns a stack without addresses, tuned by tuning, at now.
func newStack(tuning Tuning, now time.Time) *stack {
	s := &stack{
		iface:    newInterface(tuning.MTU, now),
		device:   newLinkDevice(tuning.MTU),
		outboxes: map[ipstack.SocketHandle]*outbox{},
		ports:    newPortAllocator(randomSeed()),
		tuning:   tuning,
	}
	s.dns = s.sockets.AddDNS(dns.NewSocket(nil))
	return s
}

// configure adopts the addressing IPCP agreed on, and the largest packet the
// peer accepts, mtu.
func (s *stack) configure(settings IPv4Settings, mtu int, now time.Time) error {
	address := settings.LocalAddress
	// The stack refuses these, and the PPP endpoint already does.
	if !wire.IsUnicast(address) {
		return notAHostAddress(address)
	}
	if current, ok := s.localAddress(); ok && current != address {
		s.cutOffUndelivered()
	}
	if mtu != s.device.mtu {
		// The stack reads the MTU once, when the interface is created.
		s.device.mtu = mtu
		s.iface = newInterface(mtu, now)
	}
	if err := s.iface.SetIPAddrs([]netip.Prefix{netip.PrefixFrom(address, hostPrefixLen)}); err != nil {
		return notAHostAddress(address)
	}
	s.dnsServers = slices.Clone(settings.DNSServers[:min(len(settings.DNSServers), dns.MaxServerCount)])
	s.dnsSocket().UpdateServers(s.dnsServers)
	return nil
}

// notAHostAddress is the error of an address IPCP assigned that the stack
// cannot use.
func notAHostAddress(address netip.Addr) error {
	return invalidConfig(fmt.Sprintf("the peer assigned %v, which is not a host address", address))
}

// poll processes received datagrams and timers, queues whatever is due, and
// lets go of the sockets that are done. The connections are fed what the
// app wrote first; the device then learns what the peers acknowledged
// during the poll.
func (s *stack) poll(now time.Time) {
	s.feedConnections()
	backlogs := s.sendBacklogs()
	s.device.setAppUDPPorts(s.appUDPPorts())
	s.iface.Poll(now, s.device, &s.sockets)
	s.passOnAcknowledgements(backlogs)
	s.removeFinishedSockets()
}

// setLineReady says whether the line can take a datagram now.
func (s *stack) setLineReady(ready bool) {
	s.device.setLineReady(ready)
}

// deliver hands the stack a datagram that arrived over PPP, noting first
// whether it is a DNS server's answer about a name being looked up.
func (s *stack) deliver(datagram []byte) {
	if answer, ok := parseDNSAnswer(datagram); ok {
		s.noteAnswer(answer)
	}
	s.device.pushReceived(datagram)
}

// nextOutgoing returns the next datagram to send, and false when none may go
// now. The stack's ICMP errors (port or protocol unreachable) never go: each
// quotes up to 548 bytes of what arrived, and may be addressed outside the
// policy.
func (s *stack) nextOutgoing() ([]byte, bool) {
	for {
		datagram, ok := s.device.popOutgoing()
		if !ok || !isICMP(datagram) {
			return datagram, ok
		}
	}
}

// isICMP reports whether datagram is an ICMP message.
func isICMP(datagram []byte) bool {
	packet, err := wire.NewIPv4Packet(datagram)
	return err == nil && packet.NextHeader() == wire.IPProtocolICMP
}

// hasUndeliveredData reports whether data the app handed over has yet to
// arrive: TCP data not acknowledged on a live connection, whether the stack
// has taken it or not, or datagrams still queued.
func (s *stack) hasUndeliveredData() bool {
	if s.device.hasOutgoing() {
		return true
	}
	for handle, socket := range s.sockets.TCPSockets() {
		if s.tcpUnacknowledged(handle) > 0 && socket.IsActive() {
			return true
		}
	}
	for _, socket := range s.sockets.UDPSockets() {
		if socket.SendQueue() > 0 {
			return true
		}
	}
	return false
}

// abortAll ends every connection at once: the link is gone. What waits in
// the outboxes is dropped, and the connections it was written on are cut
// off. Sockets stay in the set until their owners let go, so that no handle
// is reused under them.
func (s *stack) abortAll() {
	for _, socket := range s.sockets.TCPSockets() {
		socket.Abort()
	}
	for handle := range s.outboxes {
		s.dropOutbox(handle)
	}
	s.device.clear()
}

// openUDP returns a UDP socket on a fresh local port.
func (s *stack) openUDP() (ipstack.SocketHandle, error) {
	payloadCapacity := udpSlots * s.tuning.MTU
	socket := udp.NewSocket(udp.NewPacketBuffer(udpSlots, payloadCapacity), udp.NewPacketBuffer(udpSlots, payloadCapacity))
	localPort, err := s.freePort()
	if err != nil {
		return ipstack.SocketHandle{}, err
	}
	if err := socket.Bind(wire.ListenEndpoint{Port: localPort}); err != nil {
		return ipstack.SocketHandle{}, invalidConfig("cannot bind a UDP port")
	}
	return s.sockets.AddUDP(socket), nil
}

// udp returns the UDP socket handle names.
func (s *stack) udp(handle ipstack.SocketHandle) *udp.Socket {
	return s.sockets.UDP(handle)
}

// closeUDP records that the app is done with the socket; it goes once its
// queue is sent.
func (s *stack) closeUDP(handle ipstack.SocketHandle) {
	s.closingUDP = append(s.closingUDP, handle)
}

// udpTurnHasCome reports whether the turn on the line of the UDP socket
// handle names, sending to peer, has come: none of its datagrams waits for
// the line.
func (s *stack) udpTurnHasCome(handle ipstack.SocketHandle, peer netip.AddrPort) bool {
	return !s.device.isWaiting(udpFlow(s.udp(handle).Endpoint().Port, peer))
}

// maxUDPPayload returns the largest UDP payload that fits one packet;
// larger ones would be dropped.
func (s *stack) maxUDPPayload() int {
	return s.device.mtu - udpOverhead
}

// appUDPPorts returns the local ports of the app's UDP sockets.
func (s *stack) appUDPPorts() []uint16 {
	var ports []uint16
	for _, socket := range s.sockets.UDPSockets() {
		ports = append(ports, socket.Endpoint().Port)
	}
	return ports
}

// localAddress returns our address on the link, and false before IPCP has
// assigned one.
func (s *stack) localAddress() (netip.Addr, bool) {
	return s.iface.Context().IPv4Addr()
}

// localEndpoint returns port at our address on the link: the zero Addr
// until IPCP has assigned one.
func (s *stack) localEndpoint(port uint16) netip.AddrPort {
	address, _ := s.localAddress()
	return netip.AddrPortFrom(address, port)
}

// dnsSocket returns the resolver.
func (s *stack) dnsSocket() *dns.Socket {
	return s.sockets.DNS(s.dns)
}

// freePort returns a local port no socket uses.
func (s *stack) freePort() (uint16, error) {
	inUse := s.portsInUse()
	port, ok := s.ports.allocate(func(port uint16) bool { return !slices.Contains(inUse, port) })
	if !ok {
		return 0, invalidConfig("every local port is in use")
	}
	return port, nil
}

// portsInUse returns the local ports of the sockets: those of TCP
// connections, including the ones still saying goodbye, and those UDP
// sockets are bound to.
func (s *stack) portsInUse() []uint16 {
	var ports []uint16
	for _, socket := range s.sockets.TCPSockets() {
		if local := socket.LocalEndpoint(); local.IsValid() {
			ports = append(ports, local.Port())
		}
	}
	return append(ports, s.appUDPPorts()...)
}

// removeFinishedSockets removes the TCP sockets whose goodbye is over —
// TIME-WAIT included, so that the final acknowledgement still goes out —
// and the UDP sockets with nothing left to send.
func (s *stack) removeFinishedSockets() {
	s.closingTCP = slices.DeleteFunc(s.closingTCP, func(handle ipstack.SocketHandle) bool {
		socket := s.tcp(handle)
		// An aborted socket is closed at once, and forgets its peer only
		// once its reset has gone.
		finished := socket.State() == tcp.StateClosed && !socket.RemoteEndpoint().IsValid()
		if finished {
			s.removeTCP(handle)
		}
		return finished
	})
	s.closingUDP = slices.DeleteFunc(s.closingUDP, func(handle ipstack.SocketHandle) bool {
		finished := s.udp(handle).SendQueue() == 0
		if finished {
			s.sockets.Remove(handle)
		}
		return finished
	})
}

// newInterface returns an interface for a device of mtu bytes, at now.
func newInterface(mtu int, now time.Time) *ipstack.Interface {
	// The seed makes TCP initial sequence numbers and DNS query ids.
	config := ipstack.Config{RandomSeed: randomSeed()}
	return ipstack.NewInterface(config, ipstack.DeviceCapabilities{MTU: mtu}, now)
}

// randomSeed returns a random number, read from crypto/rand.
func randomSeed() uint64 {
	var seed [8]byte
	// crypto/rand.Read never returns an error: it crashes the program
	// instead.
	_, _ = rand.Read(seed[:])
	return binary.LittleEndian.Uint64(seed[:])
}

// portAllocator hands out ephemeral ports (the RFC 6335 range), starting at
// a random point.
type portAllocator struct {
	next uint16
}

// The ephemeral port range of RFC 6335: 49152 to 65535.
const (
	firstEphemeralPort = 49152
	ephemeralPortCount = 65535 - firstEphemeralPort + 1
)

// newPortAllocator returns an allocator that starts at a point of the range
// chosen by seed.
func newPortAllocator(seed uint64) portAllocator {
	return portAllocator{next: firstEphemeralPort + uint16(seed%ephemeralPortCount)}
}

// allocate returns the next port that isFree accepts, and false if none
// does.
func (a *portAllocator) allocate(isFree func(port uint16) bool) (uint16, bool) {
	for range ephemeralPortCount {
		port := a.next
		a.next = firstEphemeralPort + uint16((int(port)-firstEphemeralPort+1)%ephemeralPortCount)
		if isFree(port) {
			return port, true
		}
	}
	return 0, false
}
