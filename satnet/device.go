package satnet

import (
	"encoding/binary"
	"net/netip"
	"slices"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The TCP/IP stack's device: queues of bare IPv4 datagrams between the
// stack and the PPP link.

// portsLen is the length of the source and destination ports that start
// both TCP and UDP headers, which say what connection a datagram is for.
const portsLen = 4

// keepAliveProbeLen is the length of the stack's keep-alive probe, which
// repeats an acknowledged byte on purpose: a segment no longer than this is
// never dropped as acknowledged while it waited.
const keepAliveProbeLen = 1

// linkDevice is the stack's device. The stack may hand over a datagram
// whenever it likes: what is small and awaited goes at once, and the rest
// waits for the line, each flow in its own queue, the flows taking turns.
// It is not safe for concurrent use: the engine's lock guards it.
type linkDevice struct {
	// received are the datagrams that arrived over PPP, oldest first.
	received [][]byte
	// urgent is what goes at once, without waiting for the line: TCP
	// acknowledgements, window updates, SYNs and RSTs, DNS queries, and
	// what the stack answers to a datagram that arrived. All small, and
	// something waits for each.
	urgent [][]byte
	// waiting is the data waiting for the line, flow by flow, in the order
	// their turns come: one datagram per line slot, so that a busy
	// connection cannot hold up the others. A flow leaves when its queue
	// empties, so no queue here is ever empty.
	waiting []flowQueue
	// connections is what each TCP connection's peer acknowledged, and
	// what we told it.
	connections map[flow]*observedConnection
	// appUDPPorts are the local ports of the app's UDP sockets: what they
	// send to port 53 is their data, not a query of the stack's own DNS
	// socket.
	appUDPPorts []uint16
	// mtu is the largest datagram the stack may send.
	mtu int
	// lineReady reports whether the line can take a datagram right now.
	lineReady bool
}

// flowQueue is one flow's data waiting for the line, oldest first.
type flowQueue struct {
	flow      flow
	datagrams [][]byte
}

// The device is what the stack sends through and receives from.
var _ ipstack.Device = (*linkDevice)(nil)

// newLinkDevice returns an empty device for datagrams of up to mtu bytes,
// with the line ready.
func newLinkDevice(mtu int) *linkDevice {
	return &linkDevice{connections: map[flow]*observedConnection{}, mtu: mtu, lineReady: true}
}

// setLineReady says whether the line can take a datagram now.
func (d *linkDevice) setLineReady(ready bool) {
	d.lineReady = ready
}

// setAppUDPPorts records the local ports of the app's UDP sockets, as of
// this poll.
func (d *linkDevice) setAppUDPPorts(ports []uint16) {
	d.appUDPPorts = ports
}

// pushReceived queues a datagram that arrived over PPP.
func (d *linkDevice) pushReceived(datagram []byte) {
	d.received = append(d.received, datagram)
}

// isWaiting reports whether data of the flow waits for the line.
func (d *linkDevice) isWaiting(target flow) bool {
	return slices.ContainsFunc(d.waiting, func(queue flowQueue) bool { return queue.flow == target })
}

// peerAcknowledged records that the peer of our connection from localPort
// to peer acknowledged bytes more of our data. This comes from the stack,
// which took them off its send buffer once it had checked the
// acknowledgement — not from the segment that carried it, which may be
// corrupt, forged, or one whose acknowledgement the stack ignores.
func (d *linkDevice) peerAcknowledged(localPort uint16, peer netip.AddrPort, bytes int) {
	connection, ok := d.connections[tcpFlow(localPort, peer)]
	if !ok {
		return
	}
	if acknowledged, ok := connection.acknowledged.Get(); ok {
		connection.acknowledged = wire.Some(acknowledged.Add(bytes))
	}
}

// popOutgoing returns the next datagram to send, and false when none may go
// now: the urgent ones at once, the waiting ones one per line slot.
func (d *linkDevice) popOutgoing() ([]byte, bool) {
	datagram, ok := popFront(&d.urgent)
	if !ok {
		if datagram, ok = d.nextWaiting(); !ok {
			return nil, false
		}
	}
	d.observe(datagram)
	return datagram, true
}

// hasOutgoing reports whether datagrams wait to be sent.
func (d *linkDevice) hasOutgoing() bool {
	return len(d.urgent) > 0 || len(d.waiting) > 0
}

// clear forgets everything queued: the link is gone.
func (d *linkDevice) clear() {
	d.received = nil
	d.urgent = nil
	d.waiting = nil
	clear(d.connections)
}

// nextWaiting returns the datagram whose turn it is, if the line can take
// it now, and false otherwise. Its flow goes to the back of the line, if it
// has more.
func (d *linkDevice) nextWaiting() ([]byte, bool) {
	if !d.lineReady {
		return nil, false
	}
	for turn, ok := popFront(&d.waiting); ok; turn, ok = popFront(&d.waiting) {
		datagram := d.takeTurn(turn)
		if d.wentStale(datagram) {
			logDebug("dropped a retransmission acknowledged while it waited")
			continue
		}
		d.bringUpToDate(datagram)
		d.lineReady = false
		return datagram, true
	}
	return nil, false
}

// takeTurn takes the oldest datagram of turn, the flow whose turn has come,
// and sends the flow to the back of the line if it has more.
func (d *linkDevice) takeTurn(turn flowQueue) []byte {
	// A flow waits only while its queue holds a datagram.
	datagram := turn.datagrams[0]
	turn.datagrams = slices.Delete(turn.datagrams, 0, 1)
	if len(turn.datagrams) > 0 {
		d.waiting = append(d.waiting, turn)
	}
	return datagram
}

// bringUpToDate gives a TCP segment that waited for the line the newest
// acknowledgement and window its connection has told the peer since: those
// do not wait, and some stacks drop a segment — data and all — whose
// acknowledgement is older than one they already had.
func (d *linkDevice) bringUpToDate(datagram []byte) {
	connection, segment, ok := segmentToPeer(datagram)
	if !ok || !segment.ACK() {
		return
	}
	told, ok := d.toldPeer(connection)
	if !ok || segment.AckNumber().Compare(told.acknowledgement) >= 0 {
		return
	}
	segment.SetAckNumber(told.acknowledgement)
	segment.SetWindowLen(told.window)
	packet := wire.IPv4Packet(datagram)
	segment.FillChecksum(packet.SrcAddr(), packet.DstAddr())
}

// toldPeer returns the acknowledgement and window we last sent on
// connection, and false before we sent any.
func (d *linkDevice) toldPeer(connection flow) (toldPeer, bool) {
	observed, ok := d.connections[connection]
	if !ok {
		return toldPeer{}, false
	}
	return observed.told.Get()
}

// wentStale reports whether datagram is a TCP segment whose data the peer
// acknowledged while it waited — a retransmission whose original got there
// after all — and which tells the peer nothing new. A one-byte segment
// always goes: the stack's keep-alive probe repeats an acknowledged byte on
// purpose.
func (d *linkDevice) wentStale(datagram []byte) bool {
	connection, segment, ok := segmentToPeer(datagram)
	if !ok {
		return false
	}
	length := len(segment.Payload())
	if length <= keepAliveProbeLen || segment.SYN() || segment.FIN() || segment.RST() {
		return false
	}
	observed, ok := d.connections[connection]
	return ok && observed.hasAcknowledged(segment.SeqNumber().Add(length)) && !observed.isNews(segment)
}

// observe records what an outgoing TCP segment tells its connection's peer.
func (d *linkDevice) observe(datagram []byte) {
	connection, segment, ok := segmentToPeer(datagram)
	if !ok {
		return
	}
	if segment.SYN() && !segment.ACK() {
		// A new connection, perhaps on the ports of an earlier one.
		d.connections[connection] = openedBy(segment)
		return
	}
	observed, ok := d.connections[connection]
	if !ok {
		observed = &observedConnection{}
		d.connections[connection] = observed
	}
	observed.recordTold(segment)
}

// Receive hands the stack the oldest datagram that arrived, with a slot for
// its reply, which is sorted as any datagram the stack hands over. The
// stack's ICMP errors are dropped before PPP (see stack.nextOutgoing).
func (d *linkDevice) Receive(time.Time) ([]byte, ipstack.TransmitSlot, bool) {
	datagram, ok := popFront(&d.received)
	if !ok {
		return nil, nil, false
	}
	return datagram, d.sort, true
}

// Transmit offers the stack a slot whenever it asks: what it hands over is
// sorted there.
func (d *linkDevice) Transmit(time.Time) (ipstack.TransmitSlot, bool) {
	return d.sort, true
}

// sort is the device's transmit slot: an urgent datagram goes at once, any
// other waits for the line.
func (d *linkDevice) sort(datagram []byte) {
	if isUrgent(datagram, d.appUDPPorts) {
		d.urgent = append(d.urgent, datagram)
		return
	}
	d.waitForTheLine(datagram)
}

// waitForTheLine queues datagram behind its flow's data, or starts a turn
// for its flow. A resent TCP segment replaces its original while that still
// waits: the copy is newer, and one of them is enough. A datagram whose
// flow cannot be read is dropped.
func (d *linkDevice) waitForTheLine(datagram []byte) {
	datagramFlow, ok := flowOf(datagram)
	if !ok {
		return
	}
	index := slices.IndexFunc(d.waiting, func(queue flowQueue) bool { return queue.flow == datagramFlow })
	if index < 0 {
		d.waiting = append(d.waiting, flowQueue{flow: datagramFlow, datagrams: [][]byte{datagram}})
		return
	}
	queue := &d.waiting[index]
	original := slices.IndexFunc(queue.datagrams, func(queued []byte) bool { return isSameSegment(queued, datagram) })
	if original < 0 {
		queue.datagrams = append(queue.datagrams, datagram)
		return
	}
	queue.datagrams[original] = datagram
}

// isUrgent reports whether datagram is small and something waits for it: a
// TCP segment without data — an acknowledgement, window update, SYN or RST,
// but not a FIN, which must follow its connection's data — or one of the
// stack's own DNS queries.
func isUrgent(datagram []byte, appUDPPorts []uint16) bool {
	if _, segment, ok := tcpSegment(datagram); ok {
		return len(segment.Payload()) == 0 && !segment.FIN()
	}
	datagramFlow, ok := flowOf(datagram)
	return ok && datagramFlow.protocol == wire.IPProtocolUDP &&
		datagramFlow.destinationPort == dnsPort && !slices.Contains(appUDPPorts, datagramFlow.sourcePort)
}

// isSameSegment reports whether two TCP segments of a flow carry the same
// part of the stream.
func isSameSegment(queued, datagram []byte) bool {
	_, original, ok := tcpSegment(queued)
	if !ok {
		return false
	}
	_, segment, ok := tcpSegment(datagram)
	return ok && original.SeqNumber() == segment.SeqNumber() &&
		len(original.Payload()) == len(segment.Payload()) &&
		original.SYN() == segment.SYN() && original.FIN() == segment.FIN()
}

// observedConnection is what the device has seen of one TCP connection.
type observedConnection struct {
	// acknowledged is how far the peer has acknowledged our data, as the
	// stack counts it.
	acknowledged wire.Optional[wire.TCPSeqNumber]
	// told is the acknowledgement and window we last sent the peer.
	told wire.Optional[toldPeer]
}

// toldPeer is an acknowledgement and a window as we sent them to the peer.
type toldPeer struct {
	acknowledgement wire.TCPSeqNumber
	window          uint16
}

// openedBy returns the connection that our syn opens: its data starts just
// after the SYN.
func openedBy(syn wire.TCPPacket) *observedConnection {
	return &observedConnection{acknowledged: wire.Some(syn.SeqNumber().Add(wire.TCPControlSYN.Len()))}
}

// recordTold records the acknowledgement and window segment, ours, tells
// the peer, if it carries an acknowledgement.
func (c *observedConnection) recordTold(segment wire.TCPPacket) {
	if segment.ACK() {
		c.told = wire.Some(toldPeer{acknowledgement: segment.AckNumber(), window: segment.WindowLen()})
	}
}

// hasAcknowledged reports whether the peer has acknowledged our data up to
// end.
func (c *observedConnection) hasAcknowledged(end wire.TCPSeqNumber) bool {
	acknowledged, ok := c.acknowledged.Get()
	return ok && end.Compare(acknowledged) <= 0
}

// isNews reports whether segment acknowledges more of the peer's data than
// we last told it, or offers another window: the peer may be waiting for
// that, as the repeat filter also assumes.
func (c *observedConnection) isNews(segment wire.TCPPacket) bool {
	if !segment.ACK() {
		return false
	}
	told, ok := c.told.Get()
	if !ok {
		return true
	}
	acknowledgement := segment.AckNumber()
	if acknowledgement == told.acknowledgement {
		return segment.WindowLen() != told.window
	}
	return acknowledgement.Compare(told.acknowledgement) > 0
}

// flow is the connection a datagram is for: protocol, destination and both
// ports.
type flow struct {
	protocol        wire.IPProtocol
	destination     netip.Addr
	sourcePort      uint16
	destinationPort uint16
}

// flowOf returns the connection datagram is for, and false for a datagram
// that does not say.
func flowOf(datagram []byte) (flow, bool) {
	packet, err := wire.NewIPv4Packet(datagram)
	if err != nil {
		return flow{}, false
	}
	payload := packet.Payload()
	if len(payload) < portsLen {
		return flow{}, false
	}
	return flow{
		protocol:        packet.NextHeader(),
		destination:     packet.DstAddr(),
		sourcePort:      binary.BigEndian.Uint16(payload[0:2]),
		destinationPort: binary.BigEndian.Uint16(payload[2:4]),
	}, true
}

// tcpFlow returns the flow of our TCP connection from localPort to peer.
func tcpFlow(localPort uint16, peer netip.AddrPort) flow {
	return flow{
		protocol:        wire.IPProtocolTCP,
		destination:     peer.Addr(),
		sourcePort:      localPort,
		destinationPort: peer.Port(),
	}
}

// segmentToPeer returns one of our TCP segments, a view into datagram, with
// its connection; false for a datagram that is not a TCP segment.
func segmentToPeer(datagram []byte) (flow, wire.TCPPacket, bool) {
	packet, segment, ok := tcpSegment(datagram)
	if !ok {
		return flow{}, nil, false
	}
	return flow{
		protocol:        wire.IPProtocolTCP,
		destination:     packet.DstAddr(),
		sourcePort:      segment.SrcPort(),
		destinationPort: segment.DstPort(),
	}, segment, true
}

// tcpSegment returns datagram and the TCP segment it carries, views into
// it; false for a datagram that does not carry one.
func tcpSegment(datagram []byte) (wire.IPv4Packet, wire.TCPPacket, bool) {
	packet, err := wire.NewIPv4Packet(datagram)
	if err != nil || packet.NextHeader() != wire.IPProtocolTCP {
		return nil, nil, false
	}
	segment, err := wire.NewTCPPacket(packet.Payload())
	if err != nil {
		return nil, nil, false
	}
	return packet, segment, true
}

// popFront removes and returns the first item of queue, and false when it
// is empty.
func popFront[T any](queue *[]T) (T, bool) {
	var none T
	if len(*queue) == 0 {
		return none, false
	}
	first := (*queue)[0]
	// Cleared, so that the queue's storage does not keep it alive.
	(*queue)[0] = none
	*queue = (*queue)[1:]
	return first, true
}
