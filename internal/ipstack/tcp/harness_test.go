package tcp

// This file is the test harness that every test of package tcp shares: the
// port of the "Constants" and "Helper functions" sections of smoltcp's
// socket tests (src/socket/tcp.rs:2926-3312), plus socket_listen from its
// LISTEN section (:3351), which later sections use too, and the helpers that
// several sections share. Tests stay white-box, as smoltcp's are: they read
// and set the socket's unexported fields directly.
//
// The ported tests follow smoltcp's sections, one file each, in smoltcp's
// order: closed, listen, syn_received, syn_sent, established, fin_wait,
// closing, time_wait, close_wait, last_ack, transitions, retransmission,
// window, flow_control, zero_window_probe, timeout, keep_alive, reassembly,
// receive_close, delayed_ack, nagle, filtering, timer, rtt_estimator,
// timestamp and source_address (each name + _test.go). The Go-specific
// tests sit beside the code they test: socket_test.go, stream_test.go,
// connection_test.go, process_test.go, dispatch_test.go and
// pull_model_test.go; fuzz_test.go holds the fuzz target. smoltcp's section on
// time-to-live configuration, test_set_hop_limit and test_set_hop_limit_zero,
// is not ported, because the stack always sends wire.DefaultHopLimit
// (PORTING-ipstack.md, sections 3 and 6); the other tests left out are named
// in the files of their sections.
//
// How smoltcp's test code ports:
//
//	smoltcp                                        Go
//	TcpRepr { …, ..SEND_TEMPL }                    fromRemote(with…(…), …)
//	TcpRepr { …, ..RECV_TEMPL }                    fromLocal(with…(…), …)
//	  control: TcpControl::Syn                       withControl(wire.TCPControlSYN)
//	  seq_number: REMOTE_SEQ + 1                     withSeq(remoteSeq.Add(1))
//	  ack_number: Some(LOCAL_SEQ + 1) / None         withAck(localSeq.Add(1)) / withoutAck()
//	  window_len: 58                                 withWindow(58)
//	  window_scale: Some(0)                          withWindowScale(0)
//	  max_seg_size: Some(BASE_MSS)                   withMSS(baseMSS)
//	  sack_permitted: true                           withSACKPermitted()
//	  sack_ranges: [Some((l, r)), None, None]        withSACKRanges(sackRangeOf(l, r)), l and r sequence numbers
//	  timestamp: Some(TcpTimestampRepr::new(v, e))   withTimestamp(v, e)
//	  payload: &b"abc"[..]                           withPayload([]byte("abc"))
//	  src_port: …, dst_port: …                       withSrcPort(…), withDstPort(…)
//	  (fields equal to the template's, such as window_scale: None, need no option)
//	the socket's SYN in SYN-SENT                   initialSYN(…)
//	the socket's SYN|ACK in SYN-RECEIVED           synACK(…)
//	the remote's SYN (seq REMOTE_SEQ, no ACK)      remoteSYN(…)
//	once both SYNs are acknowledged:
//	  the socket's data at LOCAL_SEQ + 1 + n         dataFromLocal(n, "abc", …)
//	  the remote's data at REMOTE_SEQ + 1 + n        dataFromRemote(n, "abc", …)
//	  the remote's ACK of LOCAL_SEQ + 1 + n          ackFromRemote(n, …)
//	  the socket's ACK of REMOTE_SEQ + 1 + n         ackFromLocal(n, window, …)
//	an IpRepr carrying repr from src to dst        ipHeader(src, dst, repr)
//	send!(s, repr)                                 send(t, s, repr)
//	send!(s, repr, Some(reply))                    sendExpectingReply(t, s, repr, reply)
//	send(&mut s, Instant::from_millis(0), &repr)   deliver(t, s, repr), which returns (reply, ok)
//	recv!(s, [a, b])                               receiveOnly(t, s, a, b)
//	recv!(s, Ok(a))                                receive(t, s, a)
//	recv!(s, time T, Ok(a), exact)                 receiveExact(t, s.at(T*time.Millisecond), a)
//	recv!(s, []) / recv_nothing!(s)                receiveNothing(t, s)
//	any of them with `time T`                      the same helper with s.at(T*time.Millisecond) for s,
//	                                               as in send(t, s.at(2000*time.Millisecond), repr)
//	sanity!(s1, s2)                                sanity(t, s1, s2)
//	socket(), socket_with_buffer_sizes(tx, rx)     socketClosed(t), socketClosedWithBufferSizes(t, tx, rx)
//	socket_listen()                                socketListen(t)
//	socket_syn_received(), …_with_buffer_sizes     socketSynReceived(t), socketSynReceivedWithBufferSizes(t, tx, rx)
//	socket_syn_sent(), …_with_buffer_sizes         socketSynSent(t), socketSynSentWithBufferSizes(t, tx, rx)
//	socket_established(), …_with_buffer_sizes      socketEstablished(t), socketEstablishedWithBufferSizes(t, tx, rx)
//	socket_fin_wait_1(), socket_fin_wait_2()       socketFinWait1(t), socketFinWait2(t)
//	socket_closing()                               socketClosing(t)
//	socket_time_wait(false) / (true)               socketTimeWaitFromFinWait2(t) / socketTimeWaitFromClosing(t)
//	socket_close_wait(), socket_last_ack()         socketCloseWait(t), socketLastAck(t)
//	socket_recved()                                socketWithReceivedData(t)
//	Instant::from_millis(T)                        instant(T*time.Millisecond)
//	assert_eq!(s.state, State::X)                  assertState(t, s, StateX)
//	assert_eq!(s.tuple, Some(TUPLE)) / None        assertTuple(t, s, testTuple) / assertNoTuple(t, s)
//	assert_eq!(s.timer, Timer::Close { … })        assertTimer(t, s.timer, timer{kind: timerClose, expiresAt: …})
//	assert_eq!(s.socket.poll_at(&mut s.cx), p)     assertPollAt(t, s, socket.PollAtTime(instant(…)))
//	assert_eq!(s.tx_buffer.len(), n)               assertTransmitBufferLen(t, s, n)
//	s.socket.accepts(&mut s.cx, &ip, &repr)        s.Accepts(ip, repr)
//	s.socket.connect(&mut s.cx, REMOTE_END, p)     s.Connect(s.cx, remoteEnd, wire.ListenEndpoint{Port: p})
//	s.socket.connect(&mut s.cx, REMOTE_END, LOCAL_END)  s.Connect(s.cx, remoteEnd, localListenEnd)
//	s.cx.set_ip_addrs(…)                           s.cx.SetIPAddrs(…) (returns an error)
//	s.send_slice(b"abc").unwrap()                  mustSendSlice(t, s, []byte("abc"))
//	s.recv(|data| { assert_eq!(data, b"abc"); (3, ()) }).unwrap()
//	                                               receiveExactly(t, s, "abc")
//	s.recv(|data| { assert_eq!(&data[..2], b"ab"); (2, ()) }).unwrap()
//	                                               receivePrefix(t, s, "ab")
//	s.recv(|data| (data.len(), ())).unwrap()       mustReceive(t, s, consumeAll)
//	s.recv(|data| { …; (n, ()) }).unwrap()         mustReceive(t, s, func(data []byte) int { …; return n })
//	s.recv_slice(&mut buffer).unwrap()             mustReceiveSlice(t, s, buffer)
//	s.recv(…) == Err(RecvError::Finished)          _, err := s.Receive(…); errors.Is(err, ErrFinished)
//	s.rx_buffer = SocketBuffer::new(vec![0; n]); s.assembler = Assembler::new()
//	                                               replaceReceiveBuffer(s, n)
//	s.tx_buffer = SocketBuffer::new(vec![0; n])    s.transmitBuffer = NewSocketBuffer(n)
//	Timer::new(), RttEstimator::default()          timer{}, newRTTEstimator()
//	ACK_DELAY_DEFAULT, CLOSE_DELAY, DEFAULT_MSS, MIN_REMOTE_MSS   defaultACKDelay, closeDelay, defaultMSS, minRemoteMSS
//
// The socket's fields, as tests read and set them:
//
//	state, timer, rtte → rttEstimator, assembler, rx_buffer → receiveBuffer,
//	rx_fin_received → finReceived, tx_buffer → transmitBuffer, timeout,
//	keep_alive → keepAlive, listen_endpoint → listenEndpoint, tuple
//	(wire.Optional[fourTuple]), local_seq_no → localSeqNumber,
//	remote_seq_no → remoteSeqNumber, remote_last_seq → remoteLastSeq,
//	remote_last_ack → remoteLastAck, remote_last_win → remoteLastWindow,
//	remote_win_shift → remoteWindowShift, remote_win_len → remoteWindowLen,
//	remote_win_scale → remoteWindowScale, remote_has_sack → remoteHasSACK,
//	remote_mss → remoteMSS, remote_last_ts → remoteLastTimestamp,
//	local_rx_last_seq → localReceiveLastSeq, local_rx_last_ack →
//	localReceiveLastAck, local_rx_dup_acks → localReceiveDuplicateACKs,
//	pending_fast_retransmit → pendingFastRetransmit, ack_delay → ackDelay,
//	ack_delay_timer → ackDelayTimer, challenge_ack_timer →
//	challengeACKTimer, nagle → nagleEnabled. An Option is a wire.Optional
//	(wire.Some(x) for Some(x), the zero value for None); an Option<Duration>
//	setting is a time.Duration, 0 for None.
//
// Times are durations after the epoch of the tests' clock, the zero
// time.Time (smoltcp's Instant::ZERO), so that every time compares with ==.
// Like smoltcp's macros, every helper acts at the epoch unless given
// s.at(T), which sets the time for that one call only.
//
// smoltcp's tests sometimes set local_seq_no before connect; Connect resets
// it and takes localSeq from the test sockets' fixed ISN, so such an
// assignment needs no port. smoltcp's assert_eq!(…) becomes an if with
// t.Errorf or t.Fatalf, or one of the assert… helpers below.

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
	// ends (smoltcp's LOCAL_PORT and REMOTE_PORT).
	localPort  uint16 = 80
	remotePort uint16 = 49500
	// localSeq is the initial sequence number of every test socket: smoltcp
	// fixes it at 10000 in its test build.
	localSeq = wire.TCPSeqNumber(10000)
	// remoteSeq is the initial sequence number of the tests' peer.
	remoteSeq = wire.TCPSeqNumber(-10001)
	// testIPMTU is the MTU of smoltcp's TestingDevice for Medium::Ip, which
	// the test context takes as its IP MTU.
	testIPMTU = 1500
	// testRandomSeed is the random seed of the test context: that of
	// smoltcp's tests::setup, its Config's default.
	testRandomSeed = 0
	// baseMSS is the MSS a test socket announces: the test IP MTU less the
	// IPv4 and TCP headers.
	baseMSS uint16 = testIPMTU - wire.IPv4HeaderLen - wire.TCPHeaderLen
	// peerMSS is the MSS the tests' peer announces in its SYN (smoltcp's
	// BASE_MSS - 80): smaller than the socket's.
	peerMSS = baseMSS - 80
	// tinyMSS is an MSS below minRemoteMSS, which the socket raises.
	tinyMSS uint16 = 10
	// defaultBufferLen is the size of a test socket's buffers unless a test
	// chooses others.
	defaultBufferLen = 64
	// remoteTemplateWindow is the window the tests' peer announces unless a
	// test chooses another (SEND_TEMPL's).
	remoteTemplateWindow uint16 = 256
	// localTemplateWindow is the window a test socket announces with an
	// empty receive buffer of defaultBufferLen octets (RECV_TEMPL's).
	localTemplateWindow uint16 = defaultBufferLen
)

var (
	// localAddr and remoteAddr are the addresses of the test connection's
	// two ends (smoltcp's LOCAL_ADDR and REMOTE_ADDR); localAddr is the test
	// context's own.
	localAddr  = netip.AddrFrom4([4]byte{192, 168, 1, 1})
	remoteAddr = netip.AddrFrom4([4]byte{192, 168, 1, 2})
	// otherAddr is an address of the test network that is neither end of the
	// connection.
	otherAddr = netip.AddrFrom4([4]byte{192, 168, 1, 3})

	// localEnd and remoteEnd are the test connection's endpoints (smoltcp's
	// LOCAL_END and REMOTE_END).
	localEnd  = netip.AddrPortFrom(localAddr, localPort)
	remoteEnd = netip.AddrPortFrom(remoteAddr, remotePort)
	// listenEnd is smoltcp's LISTEN_END: the local port on every address.
	listenEnd = wire.ListenEndpoint{Port: localPort}
	// localListenEnd is smoltcp's LOCAL_END where a listen endpoint is
	// expected: the local port on the local address.
	localListenEnd = wire.ListenEndpoint{Addr: localAddr, Port: localPort}
	// testTuple is smoltcp's TUPLE: the connection of the test sockets.
	testTuple = fourTuple{local: localEnd, remote: remoteEnd}

	// sendIPTemplate is smoltcp's SEND_IP_TEMPL, the IP header the tests
	// hand to Accepts: from the local to the remote address, which only a
	// listening socket without a listen address accepts.
	sendIPTemplate = wire.IPv4Repr{
		SrcAddr:    localAddr,
		DstAddr:    remoteAddr,
		NextHeader: wire.IPProtocolTCP,
		PayloadLen: wire.TCPHeaderLen,
		HopLimit:   wire.DefaultHopLimit,
	}
)

// smallBufferLen is the receive buffer that many of smoltcp's tests swap in:
// six octets, which "abcdef" fills.
const smallBufferLen = 6

// maxSeqOffset is the furthest one sequence number can lie after another,
// 2³¹ − 1: one further on, at 2³¹, each compares as before the other.
const maxSeqOffset = math.MaxInt32

// largeBufferLen is a receive buffer of 1 MiB, which needs a window scale
// of 5 to be announced.
const largeBufferLen = 1 << 20

// receiveBufferWindowShifts are receive buffer sizes and the window scale a
// socket offers for each: what smoltcp's *_win_scale_buffers tests try.
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

// segmentOption sets a field of a test segment, as a field of a smoltcp
// TcpRepr literal does.
type segmentOption func(segment *wire.TCPRepr)

// fromRemote returns a segment the peer sends to the socket: smoltcp's
// SEND_TEMPL (from remotePort to localPort, no flags, sequence number 0,
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

// fromLocal returns a segment the socket sends to the peer: smoltcp's
// RECV_TEMPL (from localPort to remotePort, no flags, sequence number 0,
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
// wire carries them: smoltcp's (left.0 as u32, right.0 as u32).
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

// testSocket is a socket together with the interface context it runs in:
// smoltcp's TestSocket. The embedded *Socket gives tests every method and
// field of the socket.
type testSocket struct {
	*Socket
	cx *socket.Context
	// elapsed is when, after the epoch, the helper given this testSocket
	// acts: 0 but for a testSocket from at.
	elapsed time.Duration
}

// at returns s for a helper to act at elapsed after the epoch, as smoltcp's
// `time T` argument has its macros do; s itself still acts at the epoch.
func (s *testSocket) at(elapsed time.Duration) *testSocket {
	timed := *s
	timed.elapsed = elapsed
	return &timed
}

// instant returns the time elapsed after the epoch of the tests' clock: what
// smoltcp's Instant::from_millis(ms) is for elapsed = ms*time.Millisecond.
func instant(elapsed time.Duration) time.Time {
	return time.Time{}.Add(elapsed)
}

// fixedISN is the initial sequence number source of every test socket, as
// smoltcp's test build has it.
func fixedISN(*socket.Context) wire.TCPSeqNumber {
	return localSeq
}

// newTestContext returns the context of smoltcp's tests::setup(Medium::Ip):
// at the epoch, random seed testRandomSeed, an IP MTU of testIPMTU, and the
// addresses 192.168.1.1/24 and 127.0.0.1/8.
func newTestContext(t *testing.T) *socket.Context {
	t.Helper()
	return newTestContextWith(t, testRandomSeed, testIPMTU)
}

// newTestContextWith returns the test context of newTestContext with another
// random seed and IP MTU.
func newTestContextWith(t *testing.T, randomSeed uint64, ipMTU int) *socket.Context {
	t.Helper()
	cx := socket.NewContext(instant(0), randomSeed, ipMTU)
	addresses := []netip.Prefix{
		netip.PrefixFrom(localAddr, 24),
		netip.PrefixFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 8),
	}
	if err := cx.SetIPAddrs(addresses); err != nil {
		t.Fatalf("SetIPAddrs(%v): %v", addresses, err)
	}
	return cx
}

// deliver hands segment to the socket, as the interface would, and returns
// its reply: smoltcp's send function. It fails the test unless the socket
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

// send hands segment to the socket and expects no reply: smoltcp's
// send!(s, repr).
func send(t *testing.T, s *testSocket, segment wire.TCPRepr) {
	t.Helper()
	if reply, replied := deliver(t, s, segment); replied {
		t.Fatalf("the socket replied %s to %s, want no reply", segmentDetails(reply), segmentDetails(segment))
	}
}

// sendExpectingReply hands segment to the socket and expects want in reply:
// smoltcp's send!(s, repr, Some(want)).
func sendExpectingReply(t *testing.T, s *testSocket, segment, want wire.TCPRepr) {
	t.Helper()
	reply, replied := deliver(t, s, segment)
	if !replied {
		t.Fatalf("the socket did not reply to %s, want %s", segmentDetails(segment), segmentDetails(want))
	}
	assertSegment(t, reply, want)
}

// dispatchOne runs Dispatch with an emit that takes every segment, and
// returns the one segment the socket sent, its payload copied: smoltcp's
// recv function. It fails the test unless exactly one segment was sent, with
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
	err := s.Dispatch(s.cx, func(ip wire.IPv4Repr, segment wire.TCPRepr) error {
		checkOutgoingIPHeader(t, ip, segment)
		segment.Payload = bytes.Clone(segment.Payload)
		sent = append(sent, segment)
		return nil
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
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
// tests don't care about: smoltcp's recv!(s, Ok(want)).
func receive(t *testing.T, s *testSocket, want wire.TCPRepr) {
	t.Helper()
	got := dispatchOne(t, s)
	got.Control = got.Control.QuashPSH()
	assertSegment(t, got, want)
}

// receiveExact expects the socket to send want, PSH flag included: smoltcp's
// recv!(s, time T, Ok(want), exact).
func receiveExact(t *testing.T, s *testSocket, want wire.TCPRepr) {
	t.Helper()
	assertSegment(t, dispatchOne(t, s), want)
}

// receiveOnly expects the socket to send wants, one per Dispatch, and then
// nothing: smoltcp's recv!(s, [wants…]).
func receiveOnly(t *testing.T, s *testSocket, wants ...wire.TCPRepr) {
	t.Helper()
	for _, want := range wants {
		receive(t, s, want)
	}
	receiveNothing(t, s)
}

// receiveNothing expects the socket to send nothing: smoltcp's
// recv_nothing!(s).
func receiveNothing(t *testing.T, s *testSocket) {
	t.Helper()
	if sent := dispatchCollecting(t, s); len(sent) > 0 {
		t.Fatalf("should not send a packet, but sent %s", segmentDetails(sent[0]))
	}
}

// sanity compares the state of two sockets, field by field: smoltcp's
// sanity! macro.
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
// want octets: smoltcp's assert_eq!(s.tx_buffer.len(), want).
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

// receiveExactly reads from the socket as smoltcp's tests do with
// s.recv(|data| { assert_eq!(data, want); (data.len(), ()) }).unwrap(): it
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

// receivePrefix reads from the socket as smoltcp's tests do with
// s.recv(|data| { assert_eq!(&data[..n], want); (n, ()) }).unwrap(), where n
// is len(want): it fails the test unless Receive succeeds and offers octets
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
// capacity octets, with an empty assembler to match: smoltcp's
// s.rx_buffer = SocketBuffer::new(vec![0; capacity]); s.assembler =
// Assembler::new(). The window the socket last announced stays the one of
// its old buffer.
func replaceReceiveBuffer(s *testSocket, capacity int) {
	s.receiveBuffer = NewSocketBuffer(capacity)
	s.assembler = storage.Assembler{}
}

// =========================================================================
// Socket builders: sockets in each state, as smoltcp's tests build them,
// by setting the fields a connection in that state has.
// =========================================================================

// socketClosed returns a closed socket with buffers of defaultBufferLen
// octets: smoltcp's socket().
func socketClosed(t *testing.T) *testSocket {
	t.Helper()
	return socketClosedWithBufferSizes(t, defaultBufferLen, defaultBufferLen)
}

// socketClosedWithBufferSizes returns a closed socket with the given buffer
// sizes, transmit buffer first as in smoltcp's socket_with_buffer_sizes. Its
// delayed ACKs are off, and its initial sequence number is localSeq.
func socketClosedWithBufferSizes(t *testing.T, transmitLen, receiveLen int) *testSocket {
	t.Helper()
	s := NewSocket(NewSocketBuffer(receiveLen), NewSocketBuffer(transmitLen))
	s.SetAckDelay(0)
	s.chooseISN = fixedISN
	return &testSocket{Socket: s, cx: newTestContext(t)}
}

// socketListen returns a socket listening on listenEnd: smoltcp's
// socket_listen().
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
// in FIN-WAIT-2 at 1 s: smoltcp's socket_time_wait(false).
func socketTimeWaitFromFinWait2(t *testing.T) *testSocket {
	t.Helper()
	s := socketFinWait2(t)
	s.state = StateTimeWait
	s.remoteSeqNumber = remoteSeq.Add(1 + 1)
	s.timer = timer{kind: timerClose, expiresAt: instant(time.Second).Add(closeDelay)}
	return s
}

// socketTimeWaitFromClosing returns a socket that came to TIME-WAIT from
// CLOSING at 1 s, having already acknowledged the peer's FIN: smoltcp's
// socket_time_wait(true).
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
// "abcdef" and acknowledged it, the data still unread: smoltcp's
// socket_recved().
func socketWithReceivedData(t *testing.T) *testSocket {
	t.Helper()
	s := socketEstablished(t)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abcdef"))))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+6)), withWindow(58)))
	return s
}

// =========================================================================
// Tests of the helpers themselves: the porting table above relies on them.
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

func TestTemplatesAreSmoltcpsSendAndReceiveTemplates(t *testing.T) {
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

func TestTestContextIsSmoltcpsTestSetup(t *testing.T) {
	t.Parallel()
	cx := newTestContext(t)

	if cx.IPMTU() != testIPMTU || !cx.Now().Equal(instant(0)) {
		t.Errorf("IPMTU = %d, Now = %v; want %d at the epoch", cx.IPMTU(), cx.Now(), testIPMTU)
	}
	if source, ok := cx.SourceAddress(remoteAddr); !ok || source != localAddr {
		t.Errorf("SourceAddress(%v) = %v, %v; want %v", remoteAddr, source, ok, localAddr)
	}
	if baseMSS != 1460 {
		t.Errorf("baseMSS = %d, want smoltcp's BASE_MSS of 1460", baseMSS)
	}
}
