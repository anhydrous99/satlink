package sim

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The simulated internet behind the gateway: one interface that answers
// for every address but the silent ones, with the services behind it.

// hostPrefixLen is the gateway's prefix: it is a single host on the
// point-to-point link.
const hostPrefixLen = 32

// internet is every destination the app can reach through the gateway.
type internet struct {
	iface    *ipstack.Interface
	device   *packetQueue
	services *services
	// silentAddresses are where nothing answers.
	silentAddresses []netip.Addr
}

// newInternet returns an internet that exchanges datagrams of at most mtu
// bytes.
func newInternet(network NetworkConfig, mtu int, now time.Time) *internet {
	device := newPacketQueue(mtu)
	// The seed makes TCP initial sequence numbers, which must differ
	// between runs.
	iface := ipstack.NewInterface(ipstack.Config{RandomSeed: randomSeed()}, device.capabilities(), now)
	// Every destination address is somewhere on this internet.
	iface.SetAnyIP(true)
	assignGatewayAddress(iface, network.GatewayAddress)
	return &internet{
		iface:           iface,
		device:          device,
		services:        newServices(network),
		silentAddresses: network.SilentAddresses,
	}
}

// mtu returns the largest datagram the internet sends.
func (i *internet) mtu() int {
	return i.device.mtu
}

// deliver hands the internet a datagram from the app; one for a silent
// address is lost.
func (i *internet) deliver(datagram []byte) {
	if i.isForASilentAddress(datagram) {
		return
	}
	i.device.pushReceived(datagram)
}

// isForASilentAddress reports whether datagram is an IPv4 datagram for one
// of the silent addresses.
func (i *internet) isForASilentAddress(datagram []byte) bool {
	packet, err := wire.NewIPv4Packet(datagram)
	return err == nil && slices.Contains(i.silentAddresses, packet.DstAddr())
}

// poll processes what was delivered, lets the services answer, and sends
// what is due by now.
func (i *internet) poll(now time.Time) {
	for i.processOneDatagram(now) {
	}
	i.services.serve()
	i.iface.Poll(now, i.device, i.services.socketSet())
}

// nextOutgoing returns the next datagram for the app, if any.
func (i *internet) nextOutgoing() ([]byte, bool) {
	return i.device.popTransmitted()
}

// takeDiscardedBytes returns the bytes the discard service has read since
// the last call.
func (i *internet) takeDiscardedBytes() uint64 {
	return i.services.takeDiscardedBytes()
}

// close ends the internet's TLS sessions, whose goroutines would otherwise
// outlive it: a goroutine waiting for records keeps its session alive.
func (i *internet) close() {
	i.services.close()
}

// processOneDatagram processes the next delivered datagram, if any, and
// reports whether there was one. One at a time, so that each connection
// request in a burst finds a listener waiting, and a repeated request finds
// the connection the first one opened.
func (i *internet) processOneDatagram(now time.Time) bool {
	i.services.prepareListeners()
	if i.nextDatagramRepeatsAConnectionRequest() {
		i.device.discardNextReceived()
		return true
	}
	return i.iface.PollIngressSingle(now, i.device, i.services.socketSet()) != ipstack.IngressNone
}

// nextDatagramRepeatsAConnectionRequest reports whether the next datagram
// asks again for a connection that is already open or opening, as a client
// does when the round trip outlasts its retransmission timeout.
//
// The connection's own socket ignores such a repeat. But the stack hands a
// segment to the first socket in the set that accepts it, and a listener
// that took a freed slot ahead of the connection's socket would accept the
// repeat and open a second connection, which then resets the first.
func (i *internet) nextDatagramRepeatsAConnectionRequest() bool {
	datagram, ok := i.device.nextReceived()
	if !ok {
		return false
	}
	request, ok := parseConnectionRequest(datagram)
	return ok && i.services.hasConnectionFor(request)
}

// assignGatewayAddress gives the interface the gateway's own address. The
// stack refuses broadcast and multicast addresses, and 0.0.0.0 is none;
// with any-IP the interface works without one.
func assignGatewayAddress(iface *ipstack.Interface, address netip.Addr) {
	if address.IsUnspecified() {
		warnUnusableGatewayAddress(address)
		return
	}
	if err := iface.SetIPAddrs([]netip.Prefix{netip.PrefixFrom(address, hostPrefixLen)}); err != nil {
		warnUnusableGatewayAddress(address)
	}
}

func warnUnusableGatewayAddress(address netip.Addr) {
	logWarn(fmt.Sprintf("simulated internet: %v cannot be the gateway's own address", address))
}

// randomSeed returns a seed that is unpredictable on every run.
func randomSeed() uint64 {
	var seed [8]byte
	// crypto/rand.Read never fails: it fills the buffer or ends the
	// program.
	_, _ = rand.Read(seed[:])
	return binary.LittleEndian.Uint64(seed[:])
}
