package satnet

import (
	"bytes"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The connection tests' server, the sequence number its data starts at,
// and the window it offers.
var connectionServer = netip.AddrPortFrom(ipv4(192, 0, 2, 7), 80)

const (
	serverFirst  = 7001
	serverWindow = 8192
	// segmentData is the most data a segment carries at the tests' MTU:
	// 576 bytes less 40 of IPv4 and TCP headers.
	segmentData = engineMTU - tcpOverhead
)

// A corrupt segment's acknowledgement is farBeyond what was sent; its TCP
// checksum, at tcpChecksumOffset in the TCP header (RFC 9293 §3.1), is
// wrong.
const (
	farBeyond         = 1 << 20
	tcpChecksumOffset = 16
)

// The steps of run: a pass every 20 ms, and a line slot every two seconds.
const (
	runStep        = 20 * time.Millisecond
	stepsPerSecond = int(time.Second / runStep)
	stepsPerSlot   = 100
)

// fromServer returns a segment from the server to our port.
func fromServer(port uint16, sequence, acknowledgement int32, payload []byte) []byte {
	segment := wire.TCPRepr{
		SrcPort:   connectionServer.Port(),
		DstPort:   port,
		Control:   wire.TCPControlNone,
		SeqNumber: wire.TCPSeqNumber(sequence),
		AckNumber: wire.Some(wire.TCPSeqNumber(acknowledgement)),
		WindowLen: serverWindow,
		Payload:   payload,
	}
	header := wire.IPv4Repr{
		SrcAddr:    connectionServer.Addr(),
		DstAddr:    lookupLocal,
		NextHeader: wire.IPProtocolTCP,
		PayloadLen: segment.BufferLen(),
		HopLimit:   wire.DefaultHopLimit,
	}
	datagram := make([]byte, header.BufferLen())
	packet := wire.IPv4Packet(datagram)
	header.Emit(packet, wire.ChecksumCapabilities{})
	segment.Emit(wire.TCPPacket(packet.Payload()), connectionServer.Addr(), lookupLocal, wire.ChecksumCapabilities{})
	return datagram
}

// connectionsStack returns a stack at lookupLocal, without DNS servers.
func connectionsStack(t *testing.T, now time.Time) *stack {
	t.Helper()
	return stackWith(t, nil, now)
}

// connected returns a connection to the server, its handshake done by
// hand: the handle, our port, and where our data starts.
func connected(t *testing.T, s *stack, now time.Time) (ipstack.SocketHandle, uint16, int32) {
	t.Helper()
	handle, err := s.openTCP(connectionServer)
	if err != nil {
		t.Fatal(err)
	}
	s.setLineReady(true)
	s.poll(now)
	syn, ok := s.nextOutgoing()
	if !ok {
		t.Fatal("the SYN does not go")
	}
	port, first := segmentOf(t, syn).SrcPort(), int32(segmentOf(t, syn).SeqNumber())+1
	synAck := fromServer(port, serverFirst-1, first, nil)
	packet := wire.IPv4Packet(synAck)
	reply := wire.TCPPacket(packet.Payload())
	reply.SetSYN(true)
	reply.FillChecksum(packet.SrcAddr(), packet.DstAddr())
	s.deliver(synAck)
	s.poll(now)
	drain(s)
	return handle, port, first
}

// drain sends everything that may go now, and returns it.
func drain(s *stack) [][]byte {
	var sent [][]byte
	for datagram, ok := s.nextOutgoing(); ok; datagram, ok = s.nextOutgoing() {
		sent = append(sent, datagram)
	}
	return sent
}

// run runs the stack for seconds, from *now, with a line slot every two
// seconds and a server, at serverSequence, that acknowledges each data
// segment at once. It returns the data bytes that left.
func run(s *stack, now *time.Time, seconds int, serverSequence int32) int {
	sent := 0
	for step := range seconds * stepsPerSecond {
		*now = now.Add(runStep)
		s.setLineReady(step%stepsPerSlot == 0)
		s.poll(*now)
		for _, datagram := range drain(s) {
			_, segment, ok := tcpSegment(datagram)
			if !ok || len(segment.Payload()) <= keepAliveProbeLen {
				continue
			}
			length := len(segment.Payload())
			sent += length
			end := int32(segment.SeqNumber().Add(length))
			s.deliver(fromServer(segment.SrcPort(), serverSequence, end, nil))
		}
	}
	return sent
}

// expectSendQueue checks how much a connection's socket holds
// unacknowledged.
func expectSendQueue(t *testing.T, socket *tcp.Socket, want int, why string) {
	t.Helper()
	if got := socket.SendQueue(); got != want {
		t.Errorf("the socket holds %d bytes, want %d: %s", got, want, why)
	}
}

// sentBeforeTheFIN sends what may go of s's one connection, with the line
// ready, for at most polls polls, and returns how many bytes of data left
// up to its FIN, the FIN's own segment included, and whether the FIN left.
func sentBeforeTheFIN(t *testing.T, s *stack, now time.Time, polls int) (int, bool) {
	t.Helper()
	sent := 0
	for range polls {
		s.setLineReady(true)
		s.poll(now)
		for _, datagram := range drain(s) {
			segment := segmentOf(t, datagram)
			sent += len(segment.Payload())
			if segment.FIN() {
				return sent, true
			}
		}
	}
	return sent, false
}

func TestACorruptAcknowledgementHoldsNoDataBack(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, port, first := connected(t, s, now)
	if _, err := s.tcp(handle).SendSlice(bytes.Repeat([]byte{0x42}, 2000)); err != nil {
		t.Fatal(err)
	}
	// Damaged past PPP's check: its TCP checksum is wrong, and its
	// acknowledgement reads far beyond what was sent.
	corrupt := fromServer(port, serverFirst, first+farBeyond, nil)
	corrupt[wire.IPv4HeaderLen+tcpChecksumOffset] ^= 0x5a
	s.deliver(corrupt)

	sent := run(s, &now, 20, serverFirst)

	if sent != 2000 {
		t.Errorf("%d bytes left, want 2000", sent)
	}
	expectSendQueue(t, s.tcp(handle), 0, "everything was acknowledged")
}

func TestAnAcknowledgementTheStackIgnoredHoldsNoDataBack(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, port, first := connected(t, s, now)
	if _, err := s.tcp(handle).SendSlice(bytes.Repeat([]byte{0x42}, 400)); err != nil {
		t.Fatal(err)
	}
	s.setLineReady(true)
	s.poll(now)
	drain(s)
	// The server's reply crossed our request, and our ACK of it is slow;
	// the server resends it, acknowledging the request too. The stack
	// answers the copy, but takes nothing from it.
	s.setLineReady(false)
	s.deliver(fromServer(port, serverFirst, first, []byte("hello")))
	now = now.Add(300 * time.Millisecond)
	s.poll(now)
	s.deliver(fromServer(port, serverFirst, first+400, []byte("hello")))
	now = now.Add(300 * time.Millisecond)
	s.poll(now)
	drain(s)

	run(s, &now, 20, serverFirst+5)

	expectSendQueue(t, s.tcp(handle), 0, "the request is resent")
}

func TestAResendAcknowledgedBeforeTheLineTookItIsDropped(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, port, first := connected(t, s, now)
	if _, err := s.tcp(handle).SendSlice(bytes.Repeat([]byte{0x42}, 400)); err != nil {
		t.Fatal(err)
	}
	s.setLineReady(true)
	s.poll(now)
	if _, ok := s.nextOutgoing(); !ok {
		t.Fatal("the first copy does not leave")
	}
	// No answer yet: the stack resends, and the copy waits for the line.
	now = now.Add(5 * time.Second)
	s.setLineReady(true)
	s.poll(now)

	// The first copy's acknowledgement comes first.
	s.deliver(fromServer(port, serverFirst, first+400, nil))
	s.poll(now)

	expectSendQueue(t, s.tcp(handle), 0, "the first copy was acknowledged")
	if _, ok := s.nextOutgoing(); ok {
		t.Error("the resend went, though it went stale")
	}
}

func TestAnAbortedConnectionLetGoOfAtOnceStillSendsItsReset(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	s.tcp(handle).Abort()
	// Another connection's turn is on the line.
	s.setLineReady(false)
	s.closeTCP(handle)
	s.poll(now)

	s.setLineReady(true)
	s.poll(now)

	reset, ok := s.nextOutgoing()
	if !ok || !segmentOf(t, reset).RST() {
		t.Fatal("the reset does not leave")
	}
	if len(s.closingTCP) != 0 {
		t.Error("the socket stays once its reset left")
	}
}

func TestAnAcknowledgementDoesNotWaitBehindAnotherConnectionsData(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	upload, _, _ := connected(t, s, now)
	chat, chatPort, chatFirst := connected(t, s, now)
	// The upload's data waits for the line.
	if _, err := s.sendTCP(upload, bytes.Repeat([]byte{0x42}, 2000)); err != nil {
		t.Fatal(err)
	}
	s.setLineReady(false)
	s.poll(now)

	// The chat's peer says something; the acknowledgement is due once the
	// delay for riding on a reply is over.
	s.deliver(fromServer(chatPort, serverFirst, chatFirst, []byte("hello")))
	s.poll(now)
	s.poll(now.Add(200 * time.Millisecond))

	acknowledgement, ok := s.nextOutgoing()
	if !ok {
		t.Fatal("the acknowledgement does not go at once")
	}
	if segment := segmentOf(t, acknowledgement); segment.SrcPort() != chatPort || len(segment.Payload()) != 0 {
		t.Errorf("a segment from port %d with %d bytes went", segment.SrcPort(), len(segment.Payload()))
	}
	if _, ok := s.nextOutgoing(); ok {
		t.Error("the upload did not wait for the line")
	}
	received := make([]byte, segmentData)
	if n, err := s.tcp(chat).ReceiveSlice(received); err != nil || string(received[:n]) != "hello" {
		t.Errorf("the chat received %q, %v; want %q", received[:n], err, "hello")
	}
}

func TestOpeningAConnectionDoesNotWaitBehindAnotherConnectionsData(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	upload, _, _ := connected(t, s, now)
	if _, err := s.sendTCP(upload, bytes.Repeat([]byte{0x42}, 2000)); err != nil {
		t.Fatal(err)
	}
	s.setLineReady(false)
	s.poll(now)

	if _, err := s.openTCP(connectionServer); err != nil {
		t.Fatal(err)
	}
	s.poll(now)

	syn, ok := s.nextOutgoing()
	if !ok || !segmentOf(t, syn).SYN() {
		t.Error("the SYN does not go at once")
	}
}

func TestAConnectionIsHandedASegmentAtATime(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	if _, err := s.sendTCP(handle, bytes.Repeat([]byte{0x42}, 2000)); err != nil {
		t.Fatal(err)
	}
	s.setLineReady(false)
	s.poll(now)
	s.poll(now)
	expectSendQueue(t, s.tcp(handle), segmentData, "one segment waits")

	s.setLineReady(true)
	segment, _ := s.nextOutgoing()
	if got := len(segmentOf(t, segment).Payload()); got != segmentData {
		t.Errorf("a segment of %d bytes went, want %d", got, segmentData)
	}
	s.setLineReady(false)
	s.poll(now)

	expectSendQueue(t, s.tcp(handle), 2*segmentData, "its next turn's")
	if got := s.tcpUnacknowledged(handle); got != 2000 {
		t.Errorf("%d bytes are unacknowledged, want 2000", got)
	}
}

func TestTheGoodbyeFollowsAllThatWasWritten(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	if _, err := s.sendTCP(handle, bytes.Repeat([]byte{0x42}, 1000)); err != nil {
		t.Fatal(err)
	}

	s.shutdownTCP(handle)

	if s.tcpMaySend(handle) {
		t.Error("the connection still takes data")
	}
	if sent, finished := sentBeforeTheFIN(t, s, now, 4); !finished || sent != 1000 {
		t.Errorf("%d bytes went, then a FIN: %t; want 1000 bytes, then the FIN", sent, finished)
	}
}

func TestAcknowledgementsCountInAPollThatAlsoHandsDataOver(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, port, first := connected(t, s, now)
	if _, err := s.sendTCP(handle, bytes.Repeat([]byte{0x42}, 1000)); err != nil {
		t.Fatal(err)
	}
	s.setLineReady(true)
	s.poll(now)
	if _, ok := s.nextOutgoing(); !ok {
		t.Fatal("the first segment does not leave")
	}
	// Its acknowledgement comes in the poll that hands over the rest.
	s.deliver(fromServer(port, serverFirst, first+segmentData, nil))
	s.setLineReady(true)
	s.poll(now)
	if _, ok := s.nextOutgoing(); !ok {
		t.Fatal("the rest does not leave")
	}

	// No answer in time: the stack resends the rest, and the copy waits for
	// the line while the original's acknowledgement comes.
	now = now.Add(5 * time.Second)
	s.setLineReady(false)
	s.poll(now)
	s.deliver(fromServer(port, serverFirst, first+1000, nil))
	s.poll(now)

	s.setLineReady(true)
	if _, ok := s.nextOutgoing(); ok {
		t.Error("the copy went, though it went stale")
	}
}

func TestASmallWriteDoesNotWaitForTheLastOnesAcknowledgement(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	// The first write leaves; its acknowledgement is a satellite round trip
	// away.
	if _, err := s.sendTCP(handle, []byte("first")); err != nil {
		t.Fatal(err)
	}
	s.setLineReady(true)
	s.poll(now)
	drain(s)
	second := []byte("second")
	if _, err := s.sendTCP(handle, second); err != nil {
		t.Fatal(err)
	}

	s.setLineReady(true)
	s.poll(now)

	// Nagle's algorithm would hold it back until then.
	if sent := drain(s); len(sent) != 1 || !bytes.Equal(segmentOf(t, sent[0]).Payload(), second) {
		t.Errorf("%d segments went, want the second write at once", len(sent))
	}
}

func TestABareAcknowledgementWaitsAWhileForAReplyToRideOn(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	_, port, first := connected(t, s, now)
	s.deliver(fromServer(port, serverFirst, first, []byte("hello")))
	s.poll(now)

	s.poll(now.Add(ackDelay - time.Millisecond))
	early := drain(s)
	s.poll(now.Add(ackDelay))
	due := drain(s)

	if len(early) != 0 || len(due) != 1 {
		t.Errorf("%d segments went within the ACK delay, and %d once it was over; want the acknowledgement, once it was over",
			len(early), len(due))
	}
}

func TestAConnectionTakesNoMoreThanItsSendBuffer(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	buffer := s.tuning.TCPSendBuffer

	taken, err := s.sendTCP(handle, make([]byte, buffer+100))

	if err != nil || taken != buffer {
		t.Fatalf("sendTCP = %d, %v; want the %d bytes the buffer holds", taken, err, buffer)
	}
	if s.tcpCanSend(handle) {
		t.Error("a full connection takes more")
	}
	if taken, err := s.sendTCP(handle, []byte("more")); err != nil || taken != 0 {
		t.Errorf("sendTCP on a full connection = %d, %v", taken, err)
	}
}

func TestTheSendBufferBoundsTheOutboxAndTheSocketTogether(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	if _, err := s.sendTCP(handle, make([]byte, s.tuning.TCPSendBuffer)); err != nil {
		t.Fatal(err)
	}
	// The stack takes its first segment off the outbox.
	s.setLineReady(true)
	s.poll(now)
	if s.tcp(handle).SendQueue() == 0 {
		t.Fatal("the stack took nothing off the outbox")
	}

	taken, err := s.sendTCP(handle, []byte("more"))

	if err != nil || taken != 0 {
		t.Errorf("sendTCP with the buffer full between outbox and socket = %d, %v; want 0, nil", taken, err)
	}
	if s.tcpCanSend(handle) {
		t.Error("a connection whose socket holds the rest of its buffer takes more")
	}
}

func TestAFinishedConnectionTakesNothing(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	s.shutdownTCP(handle)

	if _, err := s.sendTCP(handle, []byte("late")); err == nil {
		t.Error("a finished connection takes data")
	}
}

func TestDataUndeliveredWhenTheAddressChangesIsCutOff(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	quiet, _, _ := connected(t, s, now)
	busy, _, _ := connected(t, s, now)
	if _, err := s.sendTCP(busy, []byte("on its way")); err != nil {
		t.Fatal(err)
	}

	moved := IPv4Settings{LocalAddress: ipv4(10, 64, 0, 3)}
	if err := s.configure(moved, engineMTU, now); err != nil {
		t.Fatal(err)
	}

	if s.wasCutOff(quiet) || !s.wasCutOff(busy) {
		t.Errorf("cut off: quiet %t, busy %t; want only the busy one", s.wasCutOff(quiet), s.wasCutOff(busy))
	}
	s.poll(now)
	s.closeTCP(busy)
	s.poll(now)
	if s.wasCutOff(busy) {
		t.Error("a connection let go of is still cut off, under a handle the set hands out again")
	}
}

func TestDataTheStackTakesJustBeforeTheResetStaysCutOff(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	// Written just before the address changes, it still waits in the outbox.
	if _, err := s.sendTCP(handle, []byte("on its way")); err != nil {
		t.Fatal(err)
	}
	moved := IPv4Settings{LocalAddress: ipv4(10, 64, 0, 3)}
	if err := s.configure(moved, engineMTU, now); err != nil {
		t.Fatal(err)
	}

	// The next poll hands the data to the stack, which then resets the
	// connection on the old address and empties its send buffer.
	s.setLineReady(true)
	s.poll(now)

	if s.tcp(handle).IsOpen() || s.tcpUnacknowledged(handle) != 0 {
		t.Fatalf("open %t with %d bytes unacknowledged; want a reset that left nothing", s.tcp(handle).IsOpen(), s.tcpUnacknowledged(handle))
	}
	if !s.wasCutOff(handle) {
		t.Error("data lost to the address change would count as delivered")
	}
}

func TestDataTheStackHoldsWhenTheAddressChangesIsCutOff(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	if _, err := s.sendTCP(handle, []byte("a short report")); err != nil {
		t.Fatal(err)
	}
	// The stack takes it and sends it; no acknowledgement has come.
	s.setLineReady(true)
	s.poll(now)
	drain(s)
	if s.outbox(handle).data.Len() != 0 || s.tcp(handle).SendQueue() == 0 {
		t.Fatalf("%d bytes in the outbox and %d in the send buffer; want the send buffer alone",
			s.outbox(handle).data.Len(), s.tcp(handle).SendQueue())
	}
	moved := IPv4Settings{LocalAddress: ipv4(10, 64, 0, 3)}

	if err := s.configure(moved, engineMTU, now); err != nil {
		t.Fatal(err)
	}

	// The next poll resets the connection on the old address, which
	// empties its send buffer.
	s.poll(now)
	if s.tcp(handle).IsOpen() || s.tcpUnacknowledged(handle) != 0 {
		t.Fatalf("open %t with %d bytes unacknowledged; want a reset that left nothing", s.tcp(handle).IsOpen(), s.tcpUnacknowledged(handle))
	}
	if !s.wasCutOff(handle) {
		t.Error("data the reset emptied out of the send buffer would count as delivered")
	}
}

func TestTheSameAddressAgainCutsNothingOff(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	if _, err := s.sendTCP(handle, []byte("on its way")); err != nil {
		t.Fatal(err)
	}

	if err := s.configure(IPv4Settings{LocalAddress: lookupLocal}, engineMTU, now); err != nil {
		t.Fatal(err)
	}

	if s.wasCutOff(handle) {
		t.Error("a renegotiation that kept our address cut the connection off")
	}
}

func TestAConnectionTheAppLetGoOfSendsNoKeepalives(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	_, keptPort, _ := connected(t, s, now)
	letGo, letGoPort, letGoFirst := connected(t, s, now)
	s.closeTCP(letGo)
	s.poll(now)
	if fin := segmentOf(t, mustPop(t, s.device, "the app let go")); !fin.FIN() {
		t.Fatalf("a segment without FIN went, from port %d", fin.SrcPort())
	}
	// The server acknowledges the FIN, which leaves nothing to send.
	s.deliver(fromServer(letGoPort, serverFirst, letGoFirst+1, nil))
	s.poll(now)

	keptProbed := false
	for range 2 {
		// A line slot for a probe from each connection.
		s.setLineReady(true)
		s.poll(now.Add(s.tuning.TCPKeepalive))
		for _, datagram := range drain(s) {
			switch segmentOf(t, datagram).SrcPort() {
			case keptPort:
				keptProbed = true
			case letGoPort:
				t.Error("a keepalive on a connection nobody reads")
			}
		}
	}
	if !keptProbed {
		t.Error("the connection the app kept sent no keepalive")
	}
}

func TestAbortingEverythingDropsWhatTheAppWrote(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	if _, err := s.sendTCP(handle, []byte("never sent")); err != nil {
		t.Fatal(err)
	}
	if !s.hasUndeliveredData() {
		t.Fatal("written data counts as delivered")
	}

	s.abortAll()

	if got := s.tcpUnacknowledged(handle); got != 0 {
		t.Errorf("%d bytes are still to go", got)
	}
	if s.hasUndeliveredData() {
		t.Error("data is still undelivered on a link that is gone")
	}
	if !s.wasCutOff(handle) {
		t.Error("the data dropped would count as delivered")
	}
}

func TestAResetDropsWhatTheAppWroteButKeepsTheSocket(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, _, _ := connected(t, s, now)
	if _, err := s.sendTCP(handle, []byte("never sent")); err != nil {
		t.Fatal(err)
	}

	s.resetTCP(handle)

	if got := s.tcpUnacknowledged(handle); got != 0 {
		t.Errorf("%d bytes are still to go", got)
	}
	if !s.wasCutOff(handle) {
		t.Error("the data dropped would count as delivered")
	}
	if s.tcpMaySend(handle) {
		t.Error("a reset connection still takes data")
	}
	s.poll(now)
	if reset, ok := s.nextOutgoing(); !ok || !segmentOf(t, reset).RST() {
		t.Error("no reset leaves")
	}
}

// connectionsInEachState returns three connections on s, which it polls: one
// with nothing outstanding, one whose data the stack holds, sent and not
// acknowledged, and one whose data still waits in its outbox.
func connectionsInEachState(t *testing.T, s *stack, now time.Time) (quiet, held, unsent ipstack.SocketHandle) {
	t.Helper()
	quiet, _, _ = connected(t, s, now)
	held, _, _ = connected(t, s, now)
	if _, err := s.sendTCP(held, []byte("on its way")); err != nil {
		t.Fatal(err)
	}
	s.setLineReady(true)
	s.poll(now)
	drain(s)
	unsent, _, _ = connected(t, s, now)
	if _, err := s.sendTCP(unsent, []byte("never sent")); err != nil {
		t.Fatal(err)
	}
	if s.outbox(held).data.Len() != 0 || s.tcp(held).SendQueue() == 0 {
		t.Fatalf("%d bytes in the outbox and %d in the send buffer; want the send buffer alone",
			s.outbox(held).data.Len(), s.tcp(held).SendQueue())
	}
	return quiet, held, unsent
}

func TestAResetOrTheHangUpCutsOffTheConnectionsWhoseOutboxItEmptied(t *testing.T) {
	for name, end := range map[string]func(s *stack, handles ...ipstack.SocketHandle){
		"resets": func(s *stack, handles ...ipstack.SocketHandle) {
			for _, handle := range handles {
				s.resetTCP(handle)
			}
		},
		"the hang-up": func(s *stack, _ ...ipstack.SocketHandle) { s.abortAll() },
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			s := connectionsStack(t, now)
			quiet, held, unsent := connectionsInEachState(t, s, now)

			end(s, quiet, held, unsent)
			// The resets leave.
			s.setLineReady(true)
			s.poll(now)
			drain(s)

			if s.wasCutOff(quiet) || s.wasCutOff(held) || !s.wasCutOff(unsent) {
				t.Errorf("cut off: quiet %t, held %t, unsent %t; want only the one whose outbox was emptied",
					s.wasCutOff(quiet), s.wasCutOff(held), s.wasCutOff(unsent))
			}
			// What the stack held needs no record: it shows itself. It will
			// never arrive, so a requested close must not wait for it.
			if s.tcpUnacknowledged(held) == 0 {
				t.Error("the data the stack held went with the reset, and nothing shows that it was lost")
			}
			if s.hasUndeliveredData() {
				t.Error("the data the stack held counts as undelivered: a requested close would wait out its grace for it")
			}
		})
	}
}

func TestDataOnAConnectionThePeerResetIsNoLongerUndelivered(t *testing.T) {
	now := time.Now()
	s := connectionsStack(t, now)
	handle, port, first := connected(t, s, now)
	// The stack takes it and sends it; no acknowledgement comes.
	if _, err := s.sendTCP(handle, []byte("on its way")); err != nil {
		t.Fatal(err)
	}
	s.setLineReady(true)
	s.poll(now)
	drain(s)
	if !s.hasUndeliveredData() {
		t.Fatal("data sent and not acknowledged counts as delivered")
	}

	s.deliver(resetFromServer(port, first))
	s.poll(now)

	// The reset keeps the data in the send buffer, but it will never arrive.
	if s.tcpUnacknowledged(handle) == 0 || s.hasUndeliveredData() {
		t.Errorf("%d bytes unacknowledged, undelivered: %t; want the data kept, but not waited for",
			s.tcpUnacknowledged(handle), s.hasUndeliveredData())
	}
}
