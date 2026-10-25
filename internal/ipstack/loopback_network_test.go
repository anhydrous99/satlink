package ipstack_test

// This file holds the loopback tests' network: two interfaces joined by an
// in-memory line, as PPP joins satnet's stack in the app to the simulated
// internet behind the gateway, with the clock both are polled with and a
// record of every datagram that crossed the line.

import (
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The loopback network's addresses: the simulator's defaults.
var (
	// gatewayAddr is the simulated internet's own address on the link,
	// where its DNS server answers.
	gatewayAddr = netip.AddrFrom4([4]byte{10, 64, 0, 1})
	// clientAddr is the address the gateway assigns the app.
	clientAddr = netip.AddrFrom4([4]byte{10, 64, 0, 2})
	// echoHostAddr and webHostAddr are hosts behind the gateway, which the
	// simulated internet answers for with any-IP.
	echoHostAddr = netip.AddrFrom4([4]byte{192, 0, 2, 7})
	webHostAddr  = netip.AddrFrom4([4]byte{192, 0, 2, 80})
)

const (
	// hostPrefixLen is the prefix length of both hosts' addresses: a
	// point-to-point link needs no subnet.
	hostPrefixLen = 32
	// lineMTU is the largest datagram the line carries: satnet's default
	// MTU, the datagram every IPv4 host must accept (RFC 791).
	lineMTU = wire.IPv4MinMTU
	// clientSeed and internetSeed seed the interfaces' random sources. They
	// are fixed, so every run picks the same initial sequence numbers and
	// DNS transaction ids.
	clientSeed   = 0x5eed
	internetSeed = 0x1e7
	// driverPollInterval is how often satnet's driver polls its stack, and
	// so how soon a socket that still wants a poll after one is polled again.
	driverPollInterval = 20 * time.Millisecond
	// maxSimulatedTime and maxSteps bound a scenario, which fails if it is
	// not over by then; none needs nearly as long.
	maxSimulatedTime = 10 * time.Minute
	maxSteps         = 100_000
)

// epoch is when every scenario starts: an ordinary wall-clock time, far from
// the zero time.Time, which the stack takes as "a time already past".
var epoch = time.Date(2026, time.October, 19, 3, 17, 0, 0, time.UTC)

// host is one end of the line: an interface with its sockets and device, and
// the way its owner polls it.
type host struct {
	// name says which end the host is, in failure messages.
	name    string
	iface   *ipstack.Interface
	sockets ipstack.SocketSet
	device  *pullDevice
	// poll polls the interface at now as the host's owner does, and
	// reports whether any socket may have changed.
	poll func(now time.Time) bool
}

// newClient returns the app's end of the line, polled as satnet polls its
// stack: with one Poll for everything.
func newClient(t *testing.T) *host {
	t.Helper()
	h := newHost(t, "the client", clientSeed, clientAddr)
	h.poll = h.pollEverything
	return h
}

// newInternet returns the gateway's end of the line, the simulated internet:
// with any-IP on, it answers for every address, and it is polled as the
// simulator polls it, one received datagram at a time.
func newInternet(t *testing.T) *host {
	t.Helper()
	h := newHost(t, "the internet", internetSeed, gatewayAddr)
	h.iface.SetAnyIP(true)
	h.poll = h.pollDatagramByDatagram
	return h
}

// newHost returns a host with address and no sockets, its interface made at
// the epoch with seed.
func newHost(t *testing.T, name string, seed uint64, address netip.Addr) *host {
	t.Helper()
	iface := ipstack.NewInterface(ipstack.Config{RandomSeed: seed}, ipstack.DeviceCapabilities{MTU: lineMTU}, epoch)
	if err := iface.SetIPAddrs([]netip.Prefix{netip.PrefixFrom(address, hostPrefixLen)}); err != nil {
		t.Fatalf("SetIPAddrs(%v): %v", address, err)
	}
	return &host{name: name, iface: iface, device: &pullDevice{}}
}

// pollEverything processes what arrived and sends what is due in one Poll,
// and reports whether any socket may have changed.
func (h *host) pollEverything(now time.Time) bool {
	return h.iface.Poll(now, h.device, &h.sockets) == ipstack.PollSocketStateChanged
}

// pollDatagramByDatagram processes each received datagram with a
// PollIngressSingle of its own, then sends what is due with Poll, and
// reports whether any socket may have changed.
func (h *host) pollDatagramByDatagram(now time.Time) bool {
	received := false
	for {
		result := h.iface.PollIngressSingle(now, h.device, &h.sockets)
		if result == ipstack.IngressNone {
			break
		}
		received = received || result == ipstack.IngressSocketStateChanged
	}
	polled := h.iface.Poll(now, h.device, &h.sockets) == ipstack.PollSocketStateChanged
	return received || polled
}

// nextPoll returns when h next needs polling after its last poll at now, and
// false if only incoming datagrams can change anything.
func (h *host) nextPoll(now time.Time) (time.Time, bool) {
	at, ok := h.iface.PollAt(now, &h.sockets)
	switch {
	case !ok:
		return time.Time{}, false
	case at.After(now):
		return at, true
	default:
		// A socket still wants a poll at now, after the poll at now: the
		// driver polls it again at its next poll.
		return now.Add(driverPollInterval), true
	}
}

// pullDevice is a host's end of the line, a Device in the pull model: it
// hands the interface what the other end sent, each datagram with a reply
// slot that always takes the answer, and takes every datagram the interface
// sends.
type pullDevice struct {
	// inbox holds what the other end sent that the interface has yet to
	// receive, oldest first.
	inbox [][]byte
	// outbox holds what the interface sent during the current poll, for the
	// line to carry, oldest first.
	outbox []sentDatagram
}

// A pullDevice is the Device of each end of the line.
var _ ipstack.Device = (*pullDevice)(nil)

// sentDatagram is a datagram an interface sent.
type sentDatagram struct {
	datagram []byte
	// isReply says it went through a received datagram's reply slot rather
	// than through a slot from Transmit.
	isReply bool
}

// Receive hands over the oldest datagram the other end sent, with a reply
// slot that always takes the answer.
func (d *pullDevice) Receive(time.Time) ([]byte, ipstack.TransmitSlot, bool) {
	if len(d.inbox) == 0 {
		return nil, nil, false
	}
	datagram := d.inbox[0]
	d.inbox = d.inbox[1:]
	return datagram, d.sendReply, true
}

// Transmit hands out a slot for the line.
func (d *pullDevice) Transmit(time.Time) ipstack.TransmitSlot {
	return d.sendTransmission
}

func (d *pullDevice) sendReply(datagram []byte) {
	d.outbox = append(d.outbox, sentDatagram{datagram: datagram, isReply: true})
}

func (d *pullDevice) sendTransmission(datagram []byte) {
	d.outbox = append(d.outbox, sentDatagram{datagram: datagram})
}

// takeOutbox returns what the interface sent since the last call.
func (d *pullDevice) takeOutbox() []sentDatagram {
	sent := d.outbox
	d.outbox = nil
	return sent
}

// crossing is a datagram one host sent the other, as the line saw it.
type crossing struct {
	// at is the time of the poll that sent it.
	at       time.Time
	from     *host
	datagram []byte
	// isReply says it went through a received datagram's reply slot.
	isReply bool
	// dropped says the line lost it, as the test asked.
	dropped bool
}

// loss is a datagram a test has the line drop: the occurrence-th one from a
// host whose TCP segment lose picks.
type loss struct {
	from       *host
	lose       func(segment wire.TCPRepr) bool
	occurrence int
	// seen counts the datagrams lose picked so far.
	seen int
}

// network is the loopback network: the client and the internet, the line
// between them with a record of what crossed it, and the clock.
type network struct {
	t        *testing.T
	now      time.Time
	client   *host
	internet *host
	// crossings holds every datagram either host sent, oldest first.
	crossings []crossing
	// losses are the datagrams the line is to drop.
	losses []*loss
}

// newNetwork returns a network at the epoch whose hosts have no sockets.
func newNetwork(t *testing.T) *network {
	t.Helper()
	return &network{t: t, now: epoch, client: newClient(t), internet: newInternet(t)}
}

// dropOccurrence has the line drop the occurrence-th datagram (counting from
// 1) from from whose TCP segment lose picks.
func (n *network) dropOccurrence(from *host, occurrence int, lose func(segment wire.TCPRepr) bool) {
	n.losses = append(n.losses, &loss{from: from, lose: lose, occurrence: occurrence})
}

// runUntil runs the network until done reports true, letting simulated time
// pass whenever neither host has anything to send. Before every poll it calls
// each of serve, the applications on the hosts. It fails the test if done is
// still false after maxSimulatedTime or maxSteps.
func (n *network) runUntil(done func() bool, serve ...func()) {
	n.t.Helper()
	giveUpAt := n.now.Add(maxSimulatedTime)
	for range maxSteps {
		if done() {
			return
		}
		// The applications may finish the scenario in a step in which no
		// socket changed; the clock then has nothing to wait for.
		if changed := n.step(serve); !changed && !done() {
			n.advanceClock()
		}
		if n.now.After(giveUpAt) {
			n.t.Fatalf("the scenario is not over after %v of simulated time", maxSimulatedTime)
		}
	}
	n.t.Fatalf("the scenario is not over after %d steps", maxSteps)
}

// settle polls both hosts at the current time, carrying what each sends to
// the other, until no socket changes any more.
func (n *network) settle() {
	n.t.Helper()
	for range maxSteps {
		if !n.step(nil) {
			return
		}
	}
	n.t.Fatalf("the hosts still talk after %d steps", maxSteps)
}

// step lets the applications act, then polls the client and the internet in
// turn at the current time, carrying what each sends to the other. It
// reports whether any socket may have changed: the applications then have
// something to do, or something more is due at once.
func (n *network) step(serve []func()) bool {
	for _, application := range serve {
		application()
	}
	clientChanged := n.pollAndCarry(n.client, n.internet)
	internetChanged := n.pollAndCarry(n.internet, n.client)
	return clientChanged || internetChanged
}

// pollAndCarry polls from and carries what it sent to the other end, to,
// unless the line is to drop it. It reports whether any socket of from may
// have changed.
func (n *network) pollAndCarry(from, to *host) bool {
	changed := from.poll(n.now)
	sent := from.device.takeOutbox()
	for _, s := range sent {
		c := crossing{at: n.now, from: from, datagram: s.datagram, isReply: s.isReply, dropped: n.takeLoss(from, s.datagram)}
		n.crossings = append(n.crossings, c)
		if !c.dropped {
			to.device.inbox = append(to.device.inbox, s.datagram)
		}
	}
	return changed
}

// takeLoss reports whether datagram, which from sent, is one the line is to
// drop. It counts the datagram toward every loss that picks it, so that each
// loss happens once, at its occurrence.
func (n *network) takeLoss(from *host, datagram []byte) bool {
	_, segment, ok := parseTCPDatagram(datagram)
	if !ok {
		return false
	}
	for _, l := range n.losses {
		if l.from != from || l.seen == l.occurrence || !l.lose(segment) {
			continue
		}
		l.seen++
		if l.seen == l.occurrence {
			return true
		}
	}
	return false
}

// advanceClock lets time pass until either host next needs polling. It fails
// the test when neither will: nothing can happen any more.
func (n *network) advanceClock() {
	n.t.Helper()
	var next time.Time
	found := false
	for _, h := range []*host{n.client, n.internet} {
		at, ok := h.nextPoll(n.now)
		if ok && (!found || at.Before(next)) {
			next, found = at, true
		}
	}
	if !found {
		n.t.Fatal("the network is stuck: no socket has a timer running or anything to send")
	}
	n.now = next
}

// crossingsFrom returns what h sent, oldest first.
func (n *network) crossingsFrom(h *host) []crossing {
	var sent []crossing
	for _, c := range n.crossings {
		if c.from == h {
			sent = append(sent, c)
		}
	}
	return sent
}

// parseIPv4Datagram returns the header and payload of an IPv4 datagram the
// line carried, with its checksum verified; false if it is malformed.
func parseIPv4Datagram(datagram []byte) (wire.IPv4Repr, []byte, bool) {
	packet, err := wire.NewIPv4Packet(datagram)
	if err != nil {
		return wire.IPv4Repr{}, nil, false
	}
	header, err := wire.ParseIPv4Repr(packet, wire.ChecksumCapabilities{})
	if err != nil {
		return wire.IPv4Repr{}, nil, false
	}
	return header, packet.Payload(), true
}

// parseTCPDatagram returns the header and segment of a datagram the line
// carried, with its checksums verified; false unless it is a well-formed TCP
// segment.
func parseTCPDatagram(datagram []byte) (wire.IPv4Repr, wire.TCPRepr, bool) {
	header, payload, ok := parseIPv4Datagram(datagram)
	if !ok || header.NextHeader != wire.IPProtocolTCP {
		return wire.IPv4Repr{}, wire.TCPRepr{}, false
	}
	packet, err := wire.NewTCPPacket(payload)
	if err != nil {
		return wire.IPv4Repr{}, wire.TCPRepr{}, false
	}
	segment, err := wire.ParseTCPRepr(packet, header.SrcAddr, header.DstAddr, wire.ChecksumCapabilities{})
	if err != nil {
		return wire.IPv4Repr{}, wire.TCPRepr{}, false
	}
	return header, segment, true
}
