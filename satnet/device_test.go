package satnet

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The device tests' connections run between local ports of ours and port
// 80 of one peer, as the Rust tests' do.
const (
	deviceMTU      = 576
	devicePeerPort = 80
	// ourAcknowledgement is our acknowledgement of the peer's data,
	// unchanged unless a test says otherwise.
	ourAcknowledgement = 1
	deviceWindow       = 1024
	// The Rust tests' bare datagram to port 53: an IPv4 header and 20
	// bytes that no TCP rule touches.
	bareDatagramLen = 40
	bareHeaderLen   = 20
)

var (
	deviceLocal = ipv4(10, 64, 0, 2)
	devicePeer  = ipv4(192, 0, 2, 7)
)

// deviceNow is the time the device tests hand the device, which ignores it.
var deviceNow time.Time

// handOver hands datagram to the device, as the stack does.
func handOver(t *testing.T, device *linkDevice, datagram []byte) {
	t.Helper()
	slot, ok := device.Transmit(deviceNow)
	if !ok {
		t.Fatal("no slot, where the device has one whenever the stack asks")
	}
	slot(datagram)
}

// released returns who sent each datagram that goes now, one line slot at a
// time.
func released(t *testing.T, device *linkDevice) []uint16 {
	t.Helper()
	var ports []uint16
	for {
		device.setLineReady(true)
		datagram, ok := device.popOutgoing()
		if !ok {
			return ports
		}
		ports = append(ports, sourcePortOf(t, datagram))
	}
}

// sourcePortOf returns the source port of a TCP or UDP datagram.
func sourcePortOf(t *testing.T, datagram []byte) uint16 {
	t.Helper()
	packet, err := wire.NewIPv4Packet(datagram)
	if err != nil {
		t.Fatalf("not an IPv4 datagram: %v", err)
	}
	return binary.BigEndian.Uint16(packet.Payload()[0:2])
}

// expectNothingOutgoing checks that no datagram goes now, for why.
func expectNothingOutgoing(t *testing.T, device *linkDevice, why string) {
	t.Helper()
	if _, ok := device.popOutgoing(); ok {
		t.Errorf("a datagram goes, though %s", why)
	}
}

// mustPop returns the datagram that goes now, failing the test if none
// does.
func mustPop(t *testing.T, device *linkDevice, what string) []byte {
	t.Helper()
	datagram, ok := device.popOutgoing()
	if !ok {
		t.Fatalf("nothing goes, though %s", what)
	}
	return datagram
}

// segmentOf returns the TCP segment of datagram, failing the test if it
// has none.
func segmentOf(t *testing.T, datagram []byte) wire.TCPPacket {
	t.Helper()
	_, segment, ok := tcpSegment(datagram)
	if !ok {
		t.Fatal("not a TCP segment")
	}
	return segment
}

// testSegment is a TCP segment between our local port and the peer, as the
// Rust tests' Segment describes one.
type testSegment struct {
	port            uint16
	fromPeer        bool
	control         wire.TCPControl
	sequence        int32
	acknowledgement wire.Optional[wire.TCPSeqNumber]
	window          uint16
	payload         []byte
}

// dataSegment is length bytes from port, starting at sequence.
func dataSegment(port uint16, sequence int32, length int) testSegment {
	return testSegment{
		port:            port,
		control:         wire.TCPControlNone,
		sequence:        sequence,
		acknowledgement: wire.Some(wire.TCPSeqNumber(ourAcknowledgement)),
		window:          deviceWindow,
		payload:         bytes.Repeat([]byte{0x42}, length),
	}
}

// bareAcknowledgement is an acknowledgement alone, from port.
func bareAcknowledgement(port uint16) testSegment {
	return dataSegment(port, 0, 0)
}

// synSegment is the SYN that opens a connection from port, its data to
// start at first.
func synSegment(port uint16, first int32) testSegment {
	syn := dataSegment(port, first-1, 0)
	syn.control = wire.TCPControlSYN
	syn.acknowledgement = wire.Optional[wire.TCPSeqNumber]{}
	return syn
}

// acknowledgementTo is the peer acknowledging what port sent, up to
// sequence.
func acknowledgementTo(port uint16, sequence int32) testSegment {
	answer := dataSegment(port, 1, 0)
	answer.fromPeer = true
	answer.acknowledgement = wire.Some(wire.TCPSeqNumber(sequence))
	return answer
}

// acknowledging returns the segment with another acknowledgement.
func (s testSegment) acknowledging(acknowledgement int32) testSegment {
	s.acknowledgement = wire.Some(wire.TCPSeqNumber(acknowledgement))
	return s
}

// with returns the segment with control.
func (s testSegment) with(control wire.TCPControl) testSegment {
	s.control = control
	return s
}

// acknowledgingNothing returns the segment without an acknowledgement.
func (s testSegment) acknowledgingNothing() testSegment {
	s.acknowledgement = wire.Optional[wire.TCPSeqNumber]{}
	return s
}

// offering returns the segment with another window.
func (s testSegment) offering(window uint16) testSegment {
	s.window = window
	return s
}

// datagram returns the segment in an IPv4 datagram, with its checksums.
func (s testSegment) datagram() []byte {
	ours := netip.AddrPortFrom(deviceLocal, s.port)
	theirs := netip.AddrPortFrom(devicePeer, devicePeerPort)
	source, destination := ours, theirs
	if s.fromPeer {
		source, destination = theirs, ours
	}
	segment := wire.TCPRepr{
		SrcPort:   source.Port(),
		DstPort:   destination.Port(),
		Control:   s.control,
		SeqNumber: wire.TCPSeqNumber(s.sequence),
		AckNumber: s.acknowledgement,
		WindowLen: s.window,
		Payload:   s.payload,
	}
	header := wire.IPv4Repr{
		SrcAddr:    source.Addr(),
		DstAddr:    destination.Addr(),
		NextHeader: wire.IPProtocolTCP,
		PayloadLen: segment.BufferLen(),
		HopLimit:   wire.DefaultHopLimit,
	}
	datagram := make([]byte, header.BufferLen())
	packet := wire.IPv4Packet(datagram)
	header.Emit(packet, wire.ChecksumCapabilities{})
	segment.Emit(wire.TCPPacket(packet.Payload()), source.Addr(), destination.Addr(), wire.ChecksumCapabilities{})
	return datagram
}

// datagramFrom returns a UDP-sized datagram from sourcePort to port 53,
// which no TCP rule touches, built with the packet's setters as the Rust
// test builds it.
func datagramFrom(sourcePort uint16) []byte {
	datagram := make([]byte, bareDatagramLen)
	packet := wire.IPv4Packet(datagram)
	packet.SetVersion(4)
	packet.SetHeaderLen(bareHeaderLen)
	packet.SetTotalLen(bareDatagramLen)
	packet.SetNextHeader(wire.IPProtocolUDP)
	packet.SetDstAddr(devicePeer)
	binary.BigEndian.PutUint16(packet.Payload()[0:2], sourcePort)
	binary.BigEndian.PutUint16(packet.Payload()[2:4], dnsPort)
	return datagram
}

// sendFirst sends segment at once, so that the device knows what its
// connection told the peer.
func sendFirst(t *testing.T, device *linkDevice, segment testSegment) {
	t.Helper()
	device.setLineReady(true)
	handOver(t, device, segment.datagram())
	mustPop(t, device, "the line is ready")
}

// open opens a connection from port whose data starts at first.
func open(t *testing.T, device *linkDevice, port uint16, first int32) {
	t.Helper()
	sendFirst(t, device, synSegment(port, first))
}

// acknowledgedByThePeer records that the stack took bytes of port's data
// off its send buffer.
func acknowledgedByThePeer(device *linkDevice, port uint16, bytes int) {
	device.peerAcknowledged(port, netip.AddrPortFrom(devicePeer, devicePeerPort), bytes)
}

// expectReleased checks who sent the datagrams that go now, one line slot
// at a time.
func expectReleased(t *testing.T, device *linkDevice, want []uint16, why string) {
	t.Helper()
	if got := released(t, device); !slices.Equal(got, want) {
		t.Errorf("released from ports %v, want %v: %s", got, want, why)
	}
}

func TestBusyConnectionsTakeTurns(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	// The stack asks the busy connection first, every time.
	handOver(t, device, dataSegment(1000, 0, 100).datagram())
	handOver(t, device, dataSegment(1000, 100, 100).datagram())
	handOver(t, device, dataSegment(2000, 0, 100).datagram())
	handOver(t, device, dataSegment(1000, 200, 100).datagram())

	expectReleased(t, device, []uint16{1000, 2000, 1000, 1000}, "the connections take turns")
}

func TestDataWaitsForTheLineAndGoesOneDatagramPerSlot(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	handOver(t, device, dataSegment(1000, 0, 100).datagram())
	handOver(t, device, dataSegment(2000, 0, 100).datagram())

	expectNothingOutgoing(t, device, "the line is busy")
	device.setLineReady(true)
	mustPop(t, device, "the line is ready")
	expectNothingOutgoing(t, device, "one datagram goes per slot")
}

func TestRepliesDoNotWaitForTheLine(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	device.pushReceived(acknowledgementTo(1000, 1).datagram())
	_, reply, ok := device.Receive(deviceNow)
	if !ok {
		t.Fatal("the received datagram is not handed over")
	}

	reply(dataSegment(1000, 1, 0).with(wire.TCPControlRST).datagram())

	mustPop(t, device, "a reply does not wait for the line")
}

func TestBareAcknowledgementsDoNotWaitForTheLine(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	handOver(t, device, dataSegment(1000, 0, 500).datagram())
	handOver(t, device, bareAcknowledgement(2000).datagram())

	acknowledgement := mustPop(t, device, "it goes at once")

	if port := sourcePortOf(t, acknowledgement); port != 2000 {
		t.Errorf("the datagram from port %d went first", port)
	}
	expectNothingOutgoing(t, device, "the data waits")
}

func TestOpeningAConnectionDoesNotWaitForTheLine(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	handOver(t, device, dataSegment(1000, 0, 500).datagram())
	handOver(t, device, synSegment(2000, 1).datagram())

	if port := sourcePortOf(t, mustPop(t, device, "the SYN goes at once")); port != 2000 {
		t.Errorf("the datagram from port %d went first", port)
	}
}

func TestAGoodbyeWaitsBehindItsConnectionsData(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	handOver(t, device, dataSegment(1000, 0, 500).datagram())
	handOver(t, device, dataSegment(1000, 500, 0).with(wire.TCPControlFIN).datagram())

	expectNothingOutgoing(t, device, "the goodbye waits too")
	device.setLineReady(true)
	first := mustPop(t, device, "the line is ready")

	if len(segmentOf(t, first).Payload()) == 0 {
		t.Error("the goodbye went before the data")
	}
}

func TestANewDNSQueryDoesNotWaitForTheLine(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	handOver(t, device, dataSegment(1000, 5000, 100).datagram())
	handOver(t, device, datagramFrom(5353))

	if port := sourcePortOf(t, mustPop(t, device, "the query goes at once")); port != 5353 {
		t.Errorf("the datagram from port %d went first", port)
	}
}

func TestTheAppsOwnDatagramsToPort53WaitForTheLine(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	device.setAppUDPPorts([]uint16{6000})
	handOver(t, device, datagramFrom(6000))
	handOver(t, device, datagramFrom(5353))

	if port := sourcePortOf(t, mustPop(t, device, "the query goes at once")); port != 5353 {
		t.Errorf("the datagram from port %d went first", port)
	}
	expectNothingOutgoing(t, device, "the app's datagram waits")
}

func TestARetransmissionAcknowledgedWhileItWaitedIsDropped(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	open(t, device, 1000, 5000)
	sendFirst(t, device, dataSegment(1000, 5000, 100))
	device.setLineReady(false)
	handOver(t, device, dataSegment(2000, 9000, 100).datagram())
	handOver(t, device, dataSegment(1000, 5000, 100).datagram())

	// The first copy arrives, and the peer says so.
	acknowledgedByThePeer(device, 1000, 100)

	expectReleased(t, device, []uint16{2000}, "the copy is not sent")
}

func TestAResendOfASegmentStillWaitingReplacesIt(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	handOver(t, device, dataSegment(1000, 5000, 100).datagram())
	// The stack gave up waiting, and resends it with news for the peer.
	resent := dataSegment(1000, 5000, 100).acknowledging(ourAcknowledgement + 50)
	handOver(t, device, resent.datagram())

	device.setLineReady(true)
	only := mustPop(t, device, "the line is ready")

	if got := segmentOf(t, only).AckNumber(); got != ourAcknowledgement+50 {
		t.Errorf("the segment that went acknowledges %v, not the resend's", got)
	}
	device.setLineReady(true)
	expectNothingOutgoing(t, device, "one copy went")
}

func TestASegmentThatWaitedCarriesTheNewestAcknowledgement(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	handOver(t, device, dataSegment(1000, 5000, 100).datagram())
	// Meanwhile more of the peer's data came, and was acknowledged.
	newer := bareAcknowledgement(1000).acknowledging(ourAcknowledgement + 300)
	newer.window = 512
	handOver(t, device, newer.datagram())
	mustPop(t, device, "the acknowledgement goes at once")

	device.setLineReady(true)
	data := mustPop(t, device, "the line is ready")

	packet, segment, _ := tcpSegment(data)
	if segment.AckNumber() != ourAcknowledgement+300 || segment.WindowLen() != 512 {
		t.Errorf("the segment acknowledges %v with window %d", segment.AckNumber(), segment.WindowLen())
	}
	if !segment.VerifyChecksum(packet.SrcAddr(), packet.DstAddr()) {
		t.Error("the segment's checksum is wrong")
	}
}

func TestARetransmissionThatAcknowledgesMoreStillGoes(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	open(t, device, 1000, 5000)
	sendFirst(t, device, dataSegment(1000, 5000, 100))
	device.setLineReady(false)
	// Resent with an acknowledgement of data the peer sent meanwhile.
	resent := dataSegment(1000, 5000, 100).acknowledging(ourAcknowledgement + 50)
	handOver(t, device, resent.datagram())

	acknowledgedByThePeer(device, 1000, 100)

	expectReleased(t, device, []uint16{1000}, "the peer awaits the news")
}

func TestARetransmissionCarryingTheGoodbyeStillGoes(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	open(t, device, 1000, 5000)
	sendFirst(t, device, dataSegment(1000, 5000, 100))
	device.setLineReady(false)
	// Resent with the FIN of a close that came meanwhile.
	handOver(t, device, dataSegment(1000, 5000, 100).with(wire.TCPControlFIN).datagram())

	// The first copy's data arrived; the FIN, which follows it, did not.
	acknowledgedByThePeer(device, 1000, 100)

	expectReleased(t, device, []uint16{1000}, "the FIN still goes")
}

func TestACopyWithoutTheGoodbyeDoesNotReplaceTheSegmentCarryingIt(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	handOver(t, device, dataSegment(1000, 5000, 100).with(wire.TCPControlFIN).datagram())

	handOver(t, device, dataSegment(1000, 5000, 100).datagram())

	expectReleased(t, device, []uint16{1000, 1000}, "the segment carrying the FIN stays")
}

func TestKeepAliveProbesStillGo(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	open(t, device, 1000, 5000)
	sendFirst(t, device, dataSegment(1000, 5000, 100))
	acknowledgedByThePeer(device, 1000, 100)
	// The stack's probe: the last acknowledged byte, again.
	handOver(t, device, dataSegment(1000, 5099, 1).datagram())

	expectReleased(t, device, []uint16{1000}, "a probe repeats an acknowledged byte on purpose")
}

func TestANewConnectionOnTheSamePortsStartsAfresh(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	open(t, device, 1000, 900_000)
	sendFirst(t, device, dataSegment(1000, 900_000, 100))
	acknowledgedByThePeer(device, 1000, 100)
	// A new connection on the same ports: its numbers start elsewhere.
	open(t, device, 1000, 5000)
	handOver(t, device, dataSegment(1000, 5000, 100).datagram())

	expectReleased(t, device, []uint16{1000}, "the new connection's data is new")
}

func TestAFlowWithDataWaitingIsKnown(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	handOver(t, device, dataSegment(1000, 0, 100).datagram())
	waiting := tcpFlow(1000, netip.AddrPortFrom(devicePeer, devicePeerPort))

	if !device.isWaiting(waiting) {
		t.Error("the flow's data is not known to wait")
	}
	released(t, device)
	if device.isWaiting(waiting) {
		t.Error("the flow still waits once its data went")
	}
}

func TestADatagramWithoutAFlowIsDropped(t *testing.T) {
	for name, datagram := range map[string][]byte{
		"not an IP datagram":       []byte("not an IP datagram"),
		"too short for both ports": withoutBothPorts(),
	} {
		t.Run(name, func(t *testing.T) {
			device := newLinkDevice(deviceMTU)

			handOver(t, device, datagram)

			if device.hasOutgoing() {
				t.Error("a datagram that names no flow is kept")
			}
		})
	}
}

// withoutBothPorts returns an IPv4 datagram whose payload is one byte too
// short to hold the source and destination ports.
func withoutBothPorts() []byte {
	datagram := datagramFrom(1000)[:bareHeaderLen+portsLen-1]
	wire.IPv4Packet(datagram).SetTotalLen(uint16(len(datagram)))
	return datagram
}

func TestATCPDatagramWithoutASegmentWaitsForTheLine(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	datagram := datagramFrom(1000)
	wire.IPv4Packet(datagram).SetNextHeader(wire.IPProtocolTCP)

	handOver(t, device, datagram)

	expectNothingOutgoing(t, device, "what is not a segment is not urgent")
	expectReleased(t, device, []uint16{1000}, "it still goes")
}

func TestDatagramsThatAreNotSegmentsNeverReplaceEachOther(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.setLineReady(false)
	device.setAppUDPPorts([]uint16{6000})

	handOver(t, device, datagramFrom(6000))
	handOver(t, device, datagramFrom(6000))

	expectReleased(t, device, []uint16{6000, 6000}, "both of the app's datagrams go")
}

func TestAcknowledgementsOfAConnectionNotSeenOpeningChangeNothing(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	// Neither connection's SYN went through the device: it does not know
	// where their data starts.
	acknowledgedByThePeer(device, 2000, 100)
	sendFirst(t, device, dataSegment(1000, 5000, 100))
	acknowledgedByThePeer(device, 1000, 100)
	handOver(t, device, dataSegment(1000, 5000, 100).datagram())

	expectReleased(t, device, []uint16{1000}, "the retransmission may be needed")
}

func TestWhatIsNewsForThePeer(t *testing.T) {
	told := observedConnection{told: wire.Some(toldPeer{acknowledgement: 100, window: deviceWindow})}
	for _, c := range []struct {
		name       string
		connection observedConnection
		segment    testSegment
		news       bool
	}{
		{"no acknowledgement", told, dataSegment(1000, 0, 10).with(wire.TCPControlNone).acknowledgingNothing(), false},
		{"nothing told yet", observedConnection{}, dataSegment(1000, 0, 10), true},
		{"more acknowledged", told, dataSegment(1000, 0, 10).acknowledging(101), true},
		{"another window", told, dataSegment(1000, 0, 10).acknowledging(100).offering(deviceWindow / 2), true},
		{"the same again", told, dataSegment(1000, 0, 10).acknowledging(100), false},
		{"less acknowledged", told, dataSegment(1000, 0, 10).acknowledging(99), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.connection.isNews(segmentOf(t, c.segment.datagram())); got != c.news {
				t.Errorf("isNews = %t, want %t", got, c.news)
			}
		})
	}
}

func TestReceivedDatagramsComeInOrder(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	device.pushReceived(datagramFrom(1))
	device.pushReceived(datagramFrom(2))

	for _, want := range []uint16{1, 2} {
		datagram, _, ok := device.Receive(deviceNow)
		if !ok || sourcePortOf(t, datagram) != want {
			t.Fatalf("received %v, %t; want the datagram from port %d", datagram, ok, want)
		}
	}
	if _, _, ok := device.Receive(deviceNow); ok {
		t.Error("a datagram was received twice")
	}
}

func TestClearingForgetsEverythingQueued(t *testing.T) {
	device := newLinkDevice(deviceMTU)
	open(t, device, 1000, 5000)
	device.setLineReady(false)
	device.pushReceived(acknowledgementTo(1000, 5000).datagram())
	handOver(t, device, dataSegment(1000, 5000, 100).datagram())
	handOver(t, device, bareAcknowledgement(2000).datagram())
	if !device.hasOutgoing() {
		t.Fatal("nothing outgoing")
	}

	device.clear()

	if device.hasOutgoing() {
		t.Error("datagrams are still outgoing")
	}
	if _, _, ok := device.Receive(deviceNow); ok {
		t.Error("a received datagram is still waiting")
	}
	if len(device.connections) != 0 {
		t.Errorf("the device still knows %d connections", len(device.connections))
	}
}
