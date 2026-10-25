package tcp

// This file is the test harness that every test of package tcp shares: the
// constants, the helper functions, and socket_listen, which later tests use
// too. Tests stay white-box: they read and set the socket's unexported fields
// directly.
//
// The tests are grouped in one file per state or feature. The tests of
// behaviour beyond the states sit beside the code they test: socket_test.go, stream_test.go,
// connection_test.go, process_test.go and dispatch_test.go.
// fuzz_test.go holds the fuzz target.

import (
	"bytes"
	"fmt"
	"math"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/storage"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// =========================================================================
// Constants
// =========================================================================

const (
	// localPort and remotePort are the ports of the test connection's two
	// ends.
	localPort  uint16 = 80
	remotePort uint16 = 49500
	// localSeq is the initial sequence number of every test socket.
	localSeq = wire.TCPSeqNumber(10000)
	// remoteSeq is the initial sequence number of the tests' peer.
	remoteSeq = wire.TCPSeqNumber(-10001)
	// testIPMTU is the IP MTU of the test context.
	testIPMTU = 1500
	// testRandomSeed is the random seed of the test context.
	testRandomSeed = 0
	// baseMSS is the MSS a test socket announces: the test IP MTU less the
	// IPv4 and TCP headers.
	baseMSS uint16 = testIPMTU - wire.IPv4HeaderLen - wire.TCPHeaderLen
	// peerMSS is the MSS the tests' peer announces in its SYN: smaller than
	// the socket's.
	peerMSS = baseMSS - 80
	// tinyMSS is an MSS below minRemoteMSS, which the socket raises.
	tinyMSS uint16 = 10
	// defaultBufferLen is the size of a test socket's buffers unless a test
	// chooses others.
	defaultBufferLen = 64
	// remoteTemplateWindow is the window the tests' peer announces unless a
	// test chooses another.
	remoteTemplateWindow uint16 = 256
	// localTemplateWindow is the window a test socket announces with an
	// empty receive buffer of defaultBufferLen octets.
	localTemplateWindow uint16 = defaultBufferLen
)

var (
	// localAddr and remoteAddr are the addresses of the test connection's
	// two ends; localAddr is the test context's own.
	localAddr  = netip.AddrFrom4([4]byte{192, 168, 1, 1})
	remoteAddr = netip.AddrFrom4([4]byte{192, 168, 1, 2})
	// otherAddr is an address of the test network that is neither end of the
	// connection.
	otherAddr = netip.AddrFrom4([4]byte{192, 168, 1, 3})

	// localEnd and remoteEnd are the test connection's endpoints.
	localEnd  = netip.AddrPortFrom(localAddr, localPort)
	remoteEnd = netip.AddrPortFrom(remoteAddr, remotePort)
	// listenEnd is the local port on every address.
	listenEnd = wire.ListenEndpoint{Port: localPort}
	// localListenEnd is the local port on the local address, where a listen
	// endpoint is expected.
	localListenEnd = wire.ListenEndpoint{Addr: localAddr, Port: localPort}
	// testTuple is the connection of the test sockets.
	testTuple = fourTuple{local: localEnd, remote: remoteEnd}

	// sendIPTemplate is the IP header the tests hand to Accepts: from the local to the remote address, which only a
	// listening socket without a listen address accepts.
	sendIPTemplate = wire.IPv4Repr{
		SrcAddr:    localAddr,
		DstAddr:    remoteAddr,
		NextHeader: wire.IPProtocolTCP,
		PayloadLen: wire.TCPHeaderLen,
		HopLimit:   wire.DefaultHopLimit,
	}
)

// smallBufferLen is the receive buffer that many tests swap in:
// six octets, which "abcdef" fills.
const smallBufferLen = 6

// maxSeqOffset is the furthest one sequence number can lie after another,
// 2³¹ − 1: one further on, at 2³¹, each compares as before the other.
const maxSeqOffset = math.MaxInt32

// largeBufferLen is a receive buffer of 1 MiB, which needs a window scale
// of 5 to be announced.
const largeBufferLen = 1 << 20

// receiveBufferWindowShifts are receive buffer sizes and the window scale a
// socket offers for each.
var receiveBufferWindowShifts = []struct {
	bufferLen int
	shift     uint8
}{
	{64, 0},
	{128, 0},
	{1024, 0},
	{65535, 0},
	{65536, 1},
	{65537, 1},
	{131071, 1},
	{131072, 2},
	{524287, 3},
	{524288, 4},
	{655350, 4},
	{largeBufferLen, 5},
}

// unscaledWindow returns the window field of a SYN announcing a receive
// buffer of bufferLen octets, which is not scaled: the buffer size, up to the
// largest the field holds.
func unscaledWindow(bufferLen int) uint16 {
	return uint16(min(bufferLen, math.MaxUint16))
}

// =========================================================================
// Segments
// =========================================================================

// segmentOption sets a field of a test segment.
type segmentOption func(segment *wire.TCPRepr)

// fromRemote returns a segment the peer sends to the socket: from remotePort
// to localPort, no flags, sequence number 0,
// acknowledgement number 0, a window of remoteTemplateWindow, no options and
// no payload) with options applied in order.
func fromRemote(options ...segmentOption) wire.TCPRepr {
	return applyOptions(wire.TCPRepr{
		SrcPort:   remotePort,
		DstPort:   localPort,
		AckNumber: wire.Some[wire.TCPSeqNumber](0),
		WindowLen: remoteTemplateWindow,
	}, options)
}

// fromLocal returns a segment the socket sends to the peer: from localPort
// to remotePort, no flags, sequence number 0,
// acknowledgement number 0, a window of localTemplateWindow, no options and
// no payload) with options applied in order.
func fromLocal(options ...segmentOption) wire.TCPRepr {
	return applyOptions(wire.TCPRepr{
		SrcPort:   localPort,
		DstPort:   remotePort,
		AckNumber: wire.Some[wire.TCPSeqNumber](0),
		WindowLen: localTemplateWindow,
	}, options)
}

func applyOptions(segment wire.TCPRepr, options []segmentOption) wire.TCPRepr {
	for _, option := range options {
		option(&segment)
	}
	return segment
}

// initialSYN returns the SYN a connecting test socket sends: sequence number
// localSeq, no ACK, an MSS of baseMSS, window scale 0 and SACK permitted,
// then options.
func initialSYN(options ...segmentOption) wire.TCPRepr {
	return fromLocal(append([]segmentOption{
		withControl(wire.TCPControlSYN),
		withSeq(localSeq),
		withoutAck(),
		withMSS(baseMSS),
		withWindowScale(0),
		withSACKPermitted(),
	}, options...)...)
}

// synACK returns the SYN|ACK a test socket in SYN-RECEIVED sends in answer
// to remoteSYN: sequence number localSeq, ACK of remoteSeq + 1 and an MSS of
// baseMSS, then options.
func synACK(options ...segmentOption) wire.TCPRepr {
	return fromLocal(append([]segmentOption{
		withControl(wire.TCPControlSYN),
		withSeq(localSeq),
		withAck(remoteSeq.Add(1)),
		withMSS(baseMSS),
	}, options...)...)
}

// remoteSYN returns the SYN the tests' peer opens with: sequence number
// remoteSeq and no ACK, then options.
func remoteSYN(options ...segmentOption) wire.TCPRepr {
	return fromRemote(append([]segmentOption{
		withControl(wire.TCPControlSYN),
		withSeq(remoteSeq),
		withoutAck(),
	}, options...)...)
}

// dataFromLocal returns a segment the socket sends with payload at offset
// octets into its stream (sequence number localSeq+1+offset), acknowledging
// the peer's SYN (remoteSeq+1), then options.
func dataFromLocal(offset int, payload string, options ...segmentOption) wire.TCPRepr {
	return fromLocal(append([]segmentOption{
		withSeq(localSeq.Add(1 + offset)),
		withAck(remoteSeq.Add(1)),
		withPayload([]byte(payload)),
	}, options...)...)
}

// ackFromRemote returns the peer's acknowledgement of the first
// acknowledged octets of the socket's stream (acknowledgement number
// localSeq+1+acknowledged), from a peer that has sent nothing since its SYN
// (sequence number remoteSeq+1), then options.
func ackFromRemote(acknowledged int, options ...segmentOption) wire.TCPRepr {
	return fromRemote(append([]segmentOption{
		withSeq(remoteSeq.Add(1)),
		withAck(localSeq.Add(1 + acknowledged)),
	}, options...)...)
}

// dataFromRemote returns a segment the peer sends with payload at offset
// octets into its stream (sequence number remoteSeq+1+offset), acknowledging
// the socket's SYN (localSeq+1), then options.
func dataFromRemote(offset int, payload string, options ...segmentOption) wire.TCPRepr {
	return fromRemote(append([]segmentOption{
		withSeq(remoteSeq.Add(1 + offset)),
		withAck(localSeq.Add(1)),
		withPayload([]byte(payload)),
	}, options...)...)
}

// ackFromLocal returns the socket's acknowledgement of the first received
// sequence numbers of the peer's stream, its data and its FIN
// (acknowledgement number remoteSeq+1+received), announcing window, from a
// socket that has sent nothing since its SYN (sequence number localSeq+1),
// then options.
func ackFromLocal(received int, window uint16, options ...segmentOption) wire.TCPRepr {
	return fromLocal(append([]segmentOption{
		withSeq(localSeq.Add(1)),
		withAck(remoteSeq.Add(1 + received)),
		withWindow(window),
	}, options...)...)
}

// keepAliveFromLocal returns the keep-alive of a socket that has sent the
// sequence numbers before next, to a peer that has sent nothing since its
// SYN: one garbage octet at the last of them, which the peer has already
// acknowledged.
func keepAliveFromLocal(next wire.TCPSeqNumber) wire.TCPRepr {
	return fromLocal(withSeq(next.Add(-1)), withAck(remoteSeq.Add(1)), withPayload([]byte{0}))
}

func withControl(control wire.TCPControl) segmentOption {
	return func(segment *wire.TCPRepr) { segment.Control = control }
}

func withSeq(seq wire.TCPSeqNumber) segmentOption {
	return func(segment *wire.TCPRepr) { segment.SeqNumber = seq }
}

func withAck(ack wire.TCPSeqNumber) segmentOption {
	return func(segment *wire.TCPRepr) { segment.AckNumber = wire.Some(ack) }
}

func withoutAck() segmentOption {
	return func(segment *wire.TCPRepr) { segment.AckNumber = wire.Optional[wire.TCPSeqNumber]{} }
}

func withWindow(window uint16) segmentOption {
	return func(segment *wire.TCPRepr) { segment.WindowLen = window }
}

func withWindowScale(shift uint8) segmentOption {
	return func(segment *wire.TCPRepr) { segment.WindowScale = wire.Some(shift) }
}

func withMSS(mss uint16) segmentOption {
	return func(segment *wire.TCPRepr) { segment.MaxSegmentSize = wire.Some(mss) }
}

func withSACKPermitted() segmentOption {
	return func(segment *wire.TCPRepr) { segment.SACKPermitted = true }
}

// sackRangeOf returns the SACK block from left to right, its edges as the
// wire carries them: the low 32 bits of each sequence number.
func sackRangeOf(left, right wire.TCPSeqNumber) wire.SACKRange {
	return wire.SACKRange{Left: uint32(left), Right: uint32(right)}
}

// withSACKRanges sets the first SACK blocks, at most wire.MaxSACKRanges; the
// others stay absent.
func withSACKRanges(ranges ...wire.SACKRange) segmentOption {
	if len(ranges) > wire.MaxSACKRanges {
		panic(fmt.Sprintf("a segment carries at most %d SACK blocks, not %d", wire.MaxSACKRanges, len(ranges)))
	}
	return func(segment *wire.TCPRepr) {
		for i, sackRange := range ranges {
			segment.SACKRanges[i] = wire.Some(sackRange)
		}
	}
}

func withTimestamp(value, echoReply uint32) segmentOption {
	return func(segment *wire.TCPRepr) {
		segment.Timestamp = wire.Some(wire.TCPTimestamp{Value: value, EchoReply: echoReply})
	}
}

func withPayload(payload []byte) segmentOption {
	return func(segment *wire.TCPRepr) { segment.Payload = payload }
}

func withSrcPort(port uint16) segmentOption {
	return func(segment *wire.TCPRepr) { segment.SrcPort = port }
}

func withDstPort(port uint16) segmentOption {
	return func(segment *wire.TCPRepr) { segment.DstPort = port }
}

// ipHeader returns the IP header of a datagram from src to dst that
// carries segment, as the interface hands it to Accepts.
func ipHeader(src, dst netip.Addr, segment wire.TCPRepr) wire.IPv4Repr {
	return wire.IPv4Repr{
		SrcAddr:    src,
		DstAddr:    dst,
		NextHeader: wire.IPProtocolTCP,
		PayloadLen: segment.BufferLen(),
		HopLimit:   wire.DefaultHopLimit,
	}
}

// =========================================================================
// Helper functions
// =========================================================================

// testSocket is a socket together with the interface context it runs in.
// The embedded *Socket gives tests every method and
// field of the socket.
type testSocket struct {
	*Socket
	cx *socket.Context
	// elapsed is when, after the epoch, the helper given this testSocket
	// acts: 0 but for a testSocket from at.
	elapsed time.Duration
}

// at returns s for a helper to act at elapsed after the epoch; s itself still
// acts at the epoch.
func (s *testSocket) at(elapsed time.Duration) *testSocket {
	timed := *s
	timed.elapsed = elapsed
	return &timed
}

// instant returns the time elapsed after the epoch of the tests' clock.
func instant(elapsed time.Duration) time.Time {
	return time.Time{}.Add(elapsed)
}

// fixedISN is the initial sequence number source of every test socket.
func fixedISN(*socket.Context) wire.TCPSeqNumber {
	return localSeq
}

// newTestContext returns the test context: at the epoch, random seed testRandomSeed, an IP MTU of testIPMTU, and the
// addresses of testIPAddrs.
func newTestContext(t *testing.T) *socket.Context {
	t.Helper()
	return newTestContextWith(t, testRandomSeed, testIPMTU)
}

// newTestContextWith returns the test context of newTestContext with another
// random seed and IP MTU.
func newTestContextWith(t *testing.T, randomSeed uint64, ipMTU int) *socket.Context {
	t.Helper()
	cx := socket.NewContext(instant(0), randomSeed, ipMTU)
	if err := cx.SetIPAddrs(testIPAddrs()); err != nil {
		t.Fatalf("SetIPAddrs(%v): %v", testIPAddrs(), err)
	}
	return cx
}

// testIPAddrs returns the addresses of the test context: 192.168.1.1/24 and
// 127.0.0.1/8.
func testIPAddrs() []netip.Prefix {
	return []netip.Prefix{
		netip.PrefixFrom(localAddr, 24),
		netip.PrefixFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 8),
	}
}

// deliver hands segment to the socket, as the interface would, and returns
// its reply. It fails the test unless the socket
// accepts the segment, and checks the reply's IP header as
// checkOutgoingIPHeader does.
func deliver(t *testing.T, s *testSocket, segment wire.TCPRepr) (wire.TCPRepr, bool) {
	t.Helper()
	s.cx.SetNow(instant(s.elapsed))
	ip := ipHeader(remoteAddr, localAddr, segment)
	if !s.Accepts(ip, segment) {
		t.Fatalf("the socket does not accept %s", segmentDetails(segment))
	}
	replyIP, reply, replied := s.Process(s.cx, ip, segment)
	if replied {
		checkOutgoingIPHeader(t, replyIP, reply)
	}
	return reply, replied
}

// send hands segment to the socket and expects no reply.
func send(t *testing.T, s *testSocket, segment wire.TCPRepr) {
	t.Helper()
	if reply, replied := deliver(t, s, segment); replied {
		t.Fatalf("the socket replied %s to %s, want no reply", segmentDetails(reply), segmentDetails(segment))
	}
}

// sendExpectingReply hands segment to the socket and expects want in reply.
func sendExpectingReply(t *testing.T, s *testSocket, segment, want wire.TCPRepr) {
	t.Helper()
	reply, replied := deliver(t, s, segment)
	if !replied {
		t.Fatalf("the socket did not reply to %s, want %s", segmentDetails(segment), segmentDetails(want))
	}
	assertSegment(t, reply, want)
}

// dispatchOne runs Dispatch with an emit that takes every segment, and
// returns the one segment the socket sent, its payload copied. It fails the test unless exactly one segment was sent, with
// the IP header checkOutgoingIPHeader expects.
func dispatchOne(t *testing.T, s *testSocket) wire.TCPRepr {
	t.Helper()
	sent := dispatchCollecting(t, s)
	if len(sent) != 1 {
		t.Fatalf("exactly one packet should be sent, %d were", len(sent))
	}
	return sent[0]
}

// dispatchCollecting runs Dispatch and returns what it sent, with the
// payloads copied.
func dispatchCollecting(t *testing.T, s *testSocket) []wire.TCPRepr {
	t.Helper()
	s.cx.SetNow(instant(s.elapsed))
	var sent []wire.TCPRepr
	s.Dispatch(s.cx, func(ip wire.IPv4Repr, segment wire.TCPRepr) {
		checkOutgoingIPHeader(t, ip, segment)
		segment.Payload = bytes.Clone(segment.Payload)
		sent = append(sent, segment)
	})
	return sent
}

// checkOutgoingIPHeader checks the IP header of a segment the socket sends:
// from the local to the remote address, carrying TCP with the default hop
// limit, and as long as the segment.
func checkOutgoingIPHeader(t *testing.T, ip wire.IPv4Repr, segment wire.TCPRepr) {
	t.Helper()
	want := ipHeader(localAddr, remoteAddr, segment)
	if ip != want {
		t.Fatalf("IP header %v (payload %d, hop limit %d), want %v (payload %d, hop limit %d)",
			ip, ip.PayloadLen, ip.HopLimit, want, want.PayloadLen, want.HopLimit)
	}
}

// receive expects the socket to send want, ignoring the PSH flag, which most
// tests don't care about.
func receive(t *testing.T, s *testSocket, want wire.TCPRepr) {
	t.Helper()
	got := dispatchOne(t, s)
	got.Control = got.Control.QuashPSH()
	assertSegment(t, got, want)
}

// receiveExact expects the socket to send want, PSH flag included.
func receiveExact(t *testing.T, s *testSocket, want wire.TCPRepr) {
	t.Helper()
	assertSegment(t, dispatchOne(t, s), want)
}

// receiveOnly expects the socket to send wants, one per Dispatch, and then
// nothing.
func receiveOnly(t *testing.T, s *testSocket, wants ...wire.TCPRepr) {
	t.Helper()
	for _, want := range wants {
		receive(t, s, want)
	}
	receiveNothing(t, s)
}

// receiveNothing expects the socket to send nothing.
func receiveNothing(t *testing.T, s *testSocket) {
	t.Helper()
	if sent := dispatchCollecting(t, s); len(sent) > 0 {
		t.Fatalf("should not send a packet, but sent %s", segmentDetails(sent[0]))
	}
}

// sanity compares the state of two sockets, field by field.
func sanity(t *testing.T, got, want *testSocket) {
	t.Helper()
	compareField(t, "state", got.state, want.state)
	compareField(t, "tuple", got.tuple, want.tuple)
	compareField(t, "localSeqNumber", got.localSeqNumber, want.localSeqNumber)
	compareField(t, "remoteSeqNumber", got.remoteSeqNumber, want.remoteSeqNumber)
	compareField(t, "remoteLastSeq", got.remoteLastSeq, want.remoteLastSeq)
	compareField(t, "remoteLastAck", got.remoteLastAck, want.remoteLastAck)
	compareField(t, "remoteLastWindow", got.remoteLastWindow, want.remoteLastWindow)
	compareField(t, "remoteWindowLen", got.remoteWindowLen, want.remoteWindowLen)
	assertTimer(t, got.timer, want.timer)
}

func compareField[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %+v, want %+v", name, got, want)
	}
}

// assertTimer compares two timers, their times with time.Time.Equal.
func assertTimer(t *testing.T, got, want timer) {
	t.Helper()
	gotKeepAlive, gotArmed := got.keepAliveAt.Get()
	wantKeepAlive, wantArmed := want.keepAliveAt.Get()
	equal := got.kind == want.kind && gotArmed == wantArmed && gotKeepAlive.Equal(wantKeepAlive) &&
		got.expiresAt.Equal(want.expiresAt) && got.delay == want.delay
	if !equal {
		t.Errorf("timer = %+v, want %+v", got, want)
	}
}

// assertSegment fails the test unless got and want are the same segment,
// payload included.
func assertSegment(t *testing.T, got, want wire.TCPRepr) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("segment mismatch\n got: %s\nwant: %s", segmentDetails(got), segmentDetails(want))
	}
}

// segmentDetails formats every field of a segment, for test failures.
func segmentDetails(segment wire.TCPRepr) string {
	return fmt.Sprintf("{%s ctl=%v ws=%v mss=%v sackPermitted=%v sack=%v ts=%v payload=%q}",
		segment, segment.Control, optionalString(segment.WindowScale), optionalString(segment.MaxSegmentSize),
		segment.SACKPermitted, sackRangesString(segment.SACKRanges), optionalString(segment.Timestamp), segment.Payload)
}

func optionalString[T comparable](option wire.Optional[T]) string {
	value, present := option.Get()
	if !present {
		return "none"
	}
	return fmt.Sprint(value)
}

func sackRangesString(ranges [wire.MaxSACKRanges]wire.Optional[wire.SACKRange]) string {
	descriptions := make([]string, len(ranges))
	for i, sackRange := range ranges {
		descriptions[i] = optionalString(sackRange)
	}
	return fmt.Sprint(descriptions)
}

// assertState fails the test unless the socket is in the state want.
func assertState(t *testing.T, s *testSocket, want State) {
	t.Helper()
	if s.state != want {
		t.Fatalf("state = %v, want %v", s.state, want)
	}
}

// assertTuple fails the test unless the socket's four-tuple is want.
func assertTuple(t *testing.T, s *testSocket, want fourTuple) {
	t.Helper()
	if s.tuple != wire.Some(want) {
		t.Fatalf("tuple = %+v, want %+v", s.tuple, want)
	}
}

// assertNoTuple fails the test unless the socket has no four-tuple.
func assertNoTuple(t *testing.T, s *testSocket) {
	t.Helper()
	if tuple, ok := s.tuple.Get(); ok {
		t.Fatalf("tuple = %+v, want none", tuple)
	}
}

// assertEndpoints fails the test unless the socket's local and remote
// endpoints are local and remote; the zero AddrPort stands for none.
func assertEndpoints(t *testing.T, s *testSocket, local, remote netip.AddrPort) {
	t.Helper()
	if s.LocalEndpoint() != local || s.RemoteEndpoint() != remote {
		t.Fatalf("endpoints %v and %v, want %v and %v", s.LocalEndpoint(), s.RemoteEndpoint(), local, remote)
	}
}

// assertPollAt fails the test unless PollAt says want.
func assertPollAt(t *testing.T, s *testSocket, want socket.PollAt) {
	t.Helper()
	if got := s.PollAt(s.cx); !got.Equal(want) {
		t.Fatalf("PollAt = %+v, want %+v", got, want)
	}
}

// assertTransmitBufferLen fails the test unless the transmit buffer holds
// want octets.
func assertTransmitBufferLen(t *testing.T, s *testSocket, want int) {
	t.Helper()
	if got := s.transmitBuffer.Len(); got != want {
		t.Fatalf("the transmit buffer holds %d octets, want %d", got, want)
	}
}

// mustSendSlice queues data and fails the test unless it all fits.
func mustSendSlice(t *testing.T, s *testSocket, data []byte) {
	t.Helper()
	sent, err := s.SendSlice(data)
	if err != nil || sent != len(data) {
		t.Fatalf("SendSlice(%q) = %d, %v; want %d, nil", data, sent, err, len(data))
	}
}

// mustReceive calls Receive with consume and fails the test if it fails.
func mustReceive(t *testing.T, s *testSocket, consume func(data []byte) int) int {
	t.Helper()
	received, err := s.Receive(consume)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	return received
}

// mustReceiveSlice calls ReceiveSlice and fails the test if it fails.
func mustReceiveSlice(t *testing.T, s *testSocket, data []byte) int {
	t.Helper()
	received, err := s.ReceiveSlice(data)
	if err != nil {
		t.Fatalf("ReceiveSlice: %v", err)
	}
	return received
}

// receiveExactly reads from the socket and takes all of it: it
// fails the test unless Receive succeeds and offers exactly want, which it
// consumes.
func receiveExactly(t *testing.T, s *testSocket, want string) {
	t.Helper()
	var offers []string
	received := mustReceive(t, s, func(data []byte) int {
		offers = append(offers, string(data))
		return len(data)
	})
	if len(offers) != 1 || offers[0] != want || received != len(want) {
		t.Fatalf("Receive offered %q and returned %d, want one offer of %q, all taken", offers, received, want)
	}
}

// receivePrefix reads from the socket and takes the first len(want) octets,
// which must be want: it fails the test unless Receive succeeds and offers octets
// starting with want, of which it consumes len(want).
func receivePrefix(t *testing.T, s *testSocket, want string) {
	t.Helper()
	var offered []byte
	mustReceive(t, s, func(data []byte) int {
		offered = bytes.Clone(data)
		return min(len(want), len(data))
	})
	if !bytes.HasPrefix(offered, []byte(want)) {
		t.Fatalf("Receive offered %q, want it to start with %q", offered, want)
	}
}

// consumeAll is a Receive callback that consumes every octet it is
// offered.
func consumeAll(data []byte) int {
	return len(data)
}

// replaceReceiveBuffer swaps the socket's receive buffer for an empty one of
// capacity octets, with an empty assembler to match. The window the socket last announced stays the one of
// its old buffer.
func replaceReceiveBuffer(s *testSocket, capacity int) {
	s.receiveBuffer = NewSocketBuffer(capacity)
	s.assembler = storage.Assembler{}
}

// =========================================================================
// Socket builders: sockets in each state, built by setting the fields a
// connection in that state has.
// =========================================================================

// socketClosed returns a closed socket with buffers of defaultBufferLen
// octets.
func socketClosed(t *testing.T) *testSocket {
	t.Helper()
	return socketClosedWithBufferSizes(t, defaultBufferLen, defaultBufferLen)
}

// socketClosedWithBufferSizes returns a closed socket with the given buffer
// sizes, transmit buffer first. Its
// delayed ACKs are off, and its initial sequence number is localSeq.
func socketClosedWithBufferSizes(t *testing.T, transmitLen, receiveLen int) *testSocket {
	t.Helper()
	s := NewSocket(NewSocketBuffer(receiveLen), NewSocketBuffer(transmitLen))
	s.SetAckDelay(0)
	s.chooseISN = fixedISN
	return &testSocket{Socket: s, cx: newTestContext(t)}
}

// socketListen returns a socket listening on listenEnd.
func socketListen(t *testing.T) *testSocket {
	t.Helper()
	s := socketClosed(t)
	s.state = StateListen
	s.listenEndpoint = listenEnd
	return s
}

// socketSynReceived returns a socket that received remoteSYN.
func socketSynReceived(t *testing.T) *testSocket {
	t.Helper()
	return socketSynReceivedWithBufferSizes(t, defaultBufferLen, defaultBufferLen)
}

func socketSynReceivedWithBufferSizes(t *testing.T, transmitLen, receiveLen int) *testSocket {
	t.Helper()
	s := socketClosedWithBufferSizes(t, transmitLen, receiveLen)
	s.state = StateSynReceived
	s.tuple = wire.Some(testTuple)
	s.localSeqNumber = localSeq
	s.remoteSeqNumber = remoteSeq.Add(1)
	s.remoteLastSeq = localSeq
	s.remoteWindowLen = int(remoteTemplateWindow)
	return s
}

// socketSynSent returns a socket that is to send initialSYN.
func socketSynSent(t *testing.T) *testSocket {
	t.Helper()
	return socketSynSentWithBufferSizes(t, defaultBufferLen, defaultBufferLen)
}

func socketSynSentWithBufferSizes(t *testing.T, transmitLen, receiveLen int) *testSocket {
	t.Helper()
	s := socketClosedWithBufferSizes(t, transmitLen, receiveLen)
	s.state = StateSynSent
	s.tuple = wire.Some(testTuple)
	s.localSeqNumber = localSeq
	s.remoteLastSeq = localSeq
	return s
}

// socketEstablished returns a socket whose handshake is complete, with
// nothing sent or received since.
func socketEstablished(t *testing.T) *testSocket {
	t.Helper()
	return socketEstablishedWithBufferSizes(t, defaultBufferLen, defaultBufferLen)
}

func socketEstablishedWithBufferSizes(t *testing.T, transmitLen, receiveLen int) *testSocket {
	t.Helper()
	s := socketSynReceivedWithBufferSizes(t, transmitLen, receiveLen)
	s.state = StateEstablished
	s.localSeqNumber = localSeq.Add(1)
	s.remoteLastSeq = localSeq.Add(1)
	s.remoteLastAck = wire.Some(remoteSeq.Add(1))
	s.remoteLastWindow = s.scaledWindow()
	return s
}

// socketFinWait1 returns an established socket that was closed, and is to
// send its FIN.
func socketFinWait1(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	s.state = StateFinWait1
	return s
}

// socketFinWait2 returns a socket whose FIN was acknowledged.
func socketFinWait2(t *testing.T) *testSocket {
	t.Helper()
	s := socketFinWait1(t)
	s.state = StateFinWait2
	s.localSeqNumber = localSeq.Add(1 + 1)
	s.remoteLastSeq = localSeq.Add(1 + 1)
	return s
}

// socketClosing returns a socket that sent its FIN and received the peer's
// before its own was acknowledged.
func socketClosing(t *testing.T) *testSocket {
	t.Helper()
	s := socketFinWait1(t)
	s.state = StateClosing
	s.remoteLastSeq = localSeq.Add(1 + 1)
	s.remoteSeqNumber = remoteSeq.Add(1 + 1)
	s.timer = timer{kind: timerRetransmit, expiresAt: instant(1000 * time.Millisecond)}
	return s
}

// socketTimeWaitFromFinWait2 returns a socket that received the peer's FIN
// in FIN-WAIT-2 at 1 s.
func socketTimeWaitFromFinWait2(t *testing.T) *testSocket {
	t.Helper()
	s := socketFinWait2(t)
	s.state = StateTimeWait
	s.remoteSeqNumber = remoteSeq.Add(1 + 1)
	s.timer = timer{kind: timerClose, expiresAt: instant(time.Second).Add(closeDelay)}
	return s
}

// socketTimeWaitFromClosing returns a socket that came to TIME-WAIT from
// CLOSING at 1 s, having already acknowledged the peer's FIN.
func socketTimeWaitFromClosing(t *testing.T) *testSocket {
	t.Helper()
	s := socketTimeWaitFromFinWait2(t)
	s.remoteLastAck = wire.Some(remoteSeq.Add(1 + 1))
	return s
}

// socketCloseWait returns an established socket that received and
// acknowledged the peer's FIN.
func socketCloseWait(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	s.state = StateCloseWait
	s.remoteSeqNumber = remoteSeq.Add(1 + 1)
	s.remoteLastAck = wire.Some(remoteSeq.Add(1 + 1))
	return s
}

// socketLastAck returns a socket in CLOSE-WAIT that was closed, and is to
// send its FIN.
func socketLastAck(t *testing.T) *testSocket {
	t.Helper()
	s := socketCloseWait(t)
	s.state = StateLastAck
	return s
}

// socketWithReceivedData returns an established socket that received
// "abcdef" and acknowledged it, the data still unread.
func socketWithReceivedData(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abcdef"))))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+6)), withWindow(58)))
	return s
}

// =========================================================================
// Tests of the helpers themselves.
// =========================================================================

func TestSegmentOptionsSetOneFieldEach(t *testing.T) {
	t.Parallel()
	sackRange := sackRangeOf(-1, 2)
	tests := []struct {
		name   string
		option segmentOption
		want   func(segment *wire.TCPRepr)
	}{
		{"withControl", withControl(wire.TCPControlFIN), func(r *wire.TCPRepr) { r.Control = wire.TCPControlFIN }},
		{"withSeq", withSeq(7), func(r *wire.TCPRepr) { r.SeqNumber = 7 }},
		{"withAck", withAck(9), func(r *wire.TCPRepr) { r.AckNumber = wire.Some[wire.TCPSeqNumber](9) }},
		{"withoutAck", withoutAck(), func(r *wire.TCPRepr) { r.AckNumber = wire.Optional[wire.TCPSeqNumber]{} }},
		{"withWindow", withWindow(3), func(r *wire.TCPRepr) { r.WindowLen = 3 }},
		{"withWindowScale", withWindowScale(2), func(r *wire.TCPRepr) { r.WindowScale = wire.Some[uint8](2) }},
		{"withMSS", withMSS(100), func(r *wire.TCPRepr) { r.MaxSegmentSize = wire.Some[uint16](100) }},
		{"withSACKPermitted", withSACKPermitted(), func(r *wire.TCPRepr) { r.SACKPermitted = true }},
		{"withSACKRanges", withSACKRanges(sackRange), func(r *wire.TCPRepr) { r.SACKRanges[0] = wire.Some(sackRange) }},
		{"withTimestamp", withTimestamp(5, 6), func(r *wire.TCPRepr) {
			r.Timestamp = wire.Some(wire.TCPTimestamp{Value: 5, EchoReply: 6})
		}},
		{"withPayload", withPayload([]byte("x")), func(r *wire.TCPRepr) { r.Payload = []byte("x") }},
		{"withSrcPort", withSrcPort(1), func(r *wire.TCPRepr) { r.SrcPort = 1 }},
		{"withDstPort", withDstPort(2), func(r *wire.TCPRepr) { r.DstPort = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			want := fromRemote()
			test.want(&want)

			got := fromRemote(test.option)

			assertSegment(t, got, want)
		})
	}
}

func TestTemplatesSetTheirFields(t *testing.T) {
	t.Parallel()
	wantFromRemote := wire.TCPRepr{SrcPort: 49500, DstPort: 80, AckNumber: wire.Some[wire.TCPSeqNumber](0), WindowLen: 256}
	wantFromLocal := wire.TCPRepr{SrcPort: 80, DstPort: 49500, AckNumber: wire.Some[wire.TCPSeqNumber](0), WindowLen: 64}

	assertSegment(t, fromRemote(), wantFromRemote)
	assertSegment(t, fromLocal(), wantFromLocal)
}

func TestWithSACKRangesRefusesMoreBlocksThanASegmentCarries(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("withSACKRanges took more than wire.MaxSACKRanges blocks")
		}
	}()

	withSACKRanges(make([]wire.SACKRange, wire.MaxSACKRanges+1)...)
}

func TestSACKRangeOfTakesTheWireFormOfSequenceNumbers(t *testing.T) {
	t.Parallel()

	got := sackRangeOf(-1, 2)

	if got != (wire.SACKRange{Left: 0xffffffff, Right: 2}) {
		t.Errorf("sackRangeOf(-1, 2) = %+v, want {Left:4294967295 Right:2}", got)
	}
}

func TestTestContextSetup(t *testing.T) {
	t.Parallel()
	cx := newTestContext(t)

	if cx.IPMTU() != testIPMTU || !cx.Now().Equal(instant(0)) {
		t.Errorf("IPMTU = %d, Now = %v; want %d at the epoch", cx.IPMTU(), cx.Now(), testIPMTU)
	}
	if source, ok := cx.SourceAddress(remoteAddr); !ok || source != localAddr {
		t.Errorf("SourceAddress(%v) = %v, %v; want %v", remoteAddr, source, ok, localAddr)
	}
	if baseMSS != 1460 {
		t.Errorf("baseMSS = %d, want 1460", baseMSS)
	}
}
