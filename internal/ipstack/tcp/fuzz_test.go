package tcp

// The fuzz target of the socket: a peer that
// sends whatever segments it likes, an application that sends, receives,
// closes and opens the socket again at any moment, and an interface that
// polls the socket, and whose address changes now and then. go test runs its
// seeds; fuzz it with
//
//	go test -run='^$' -fuzz='^FuzzSocketAgainstAHostilePeer$' -fuzztime=60s -parallel=2 ./internal/ipstack/tcp

import (
	"bytes"
	"errors"
	"math"
	"net/netip"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// FuzzSocketAgainstAHostilePeer runs a script of steps against one socket set
// up as config says (fuzzConfig). The script's next octet chooses each step
// (fuzzSteps), which takes its parameters from the octets after it. Whatever
// the script, the socket must not panic, and must keep its account of both
// streams: the application reads only octets the peer sent, each at its
// sequence number, up to the peer's FIN; the socket takes a FIN only from a
// segment that carries one, where the peer sent it, after every octet before
// it, and acknowledges no sequence number beyond the furthest the peer sent;
// it sends only octets the application wrote, each at its sequence number,
// in segments no longer than the effective MSS, and within the peer's window
// but for a zero-window probe and a fast retransmission, which are sent
// whatever the window; it never answers a RST; and its assembler holds no
// data beyond the receive buffer.
//
// The peer may also send data beyond its FIN, contradicting itself. The
// socket queues such data, and the octets after the FIN are
// not checked. If the socket already holds octets beyond the FIN when the
// FIN's segment arrives, it may end the stream where they end, and
// acknowledge them: then the FIN's position is not checked, nor, for the
// rest of the connection, are the acknowledgements.
func FuzzSocketAgainstAHostilePeer(f *testing.F) {
	for _, seed := range fuzzSeeds() {
		f.Add(uint8(seed.config), seed.script)
	}
	f.Fuzz(func(t *testing.T, config uint8, script []byte) {
		run := newFuzzRun(t, fuzzConfig(config))
		steps := &scriptReader{script: script}
		for !steps.done() {
			run.take(steps)
			run.checkInvariants()
		}
	})
}

// =========================================================================
// The script
// =========================================================================

// scriptReader hands out the octets of a fuzz script in order, and zeros
// once they run out.
type scriptReader struct {
	script []byte
}

func (r *scriptReader) done() bool {
	return len(r.script) == 0
}

func (r *scriptReader) next() byte {
	if r.done() {
		return 0
	}
	octet := r.script[0]
	r.script = r.script[1:]
	return octet
}

// fuzzConfig is the octet that sets up the socket of a fuzz run: its buffers
// (fuzzBuffers, chosen by the two lowest bits), whether it connects or
// listens, its settings, and the half of sequence space its initial sequence
// numbers lie in.
type fuzzConfig uint8

const (
	// configBuffers chooses the buffers from fuzzBuffers.
	configBuffers fuzzConfig = 0b11
	// configConnect has the socket connect at once, rather than listen.
	configConnect fuzzConfig = 1 << 2
	// configNagleOff turns Nagle's algorithm off; configACKDelay,
	// configTimeout and configKeepAlive turn on the default ACK delay,
	// fuzzTimeout and fuzzKeepAlive.
	configNagleOff  fuzzConfig = 1 << 3
	configACKDelay  fuzzConfig = 1 << 4
	configTimeout   fuzzConfig = 1 << 5
	configKeepAlive fuzzConfig = 1 << 6
	// configUpperHalfISN puts the socket's initial sequence numbers in the
	// upper half of sequence space, rather than in the lower half with
	// localSeq (isnsAcrossSequenceSpace).
	configUpperHalfISN fuzzConfig = 1 << 7
)

func (c fuzzConfig) has(setting fuzzConfig) bool {
	return c&setting != 0
}

// socketISNBase returns the number that the socket's initial sequence
// numbers of a run count from, a stride per connection (chooseISN).
func (c fuzzConfig) socketISNBase() wire.TCPSeqNumber {
	if c.has(configUpperHalfISN) {
		// localSeq with the top bit set.
		return localSeq | math.MinInt32
	}
	return localSeq
}

// fuzzBuffers are the buffer sizes the socket of a fuzz run may have, with
// the octets that one unit of a script's payload length stands for: small
// buffers, medium ones, a receive buffer of 128 KiB, which needs a window
// scale of 2, and a transmit buffer smaller than the receive buffer.
var fuzzBuffers = [...]struct {
	receive, transmit, payloadUnit int
}{
	{receive: 64, transmit: 64, payloadUnit: 1},
	{receive: 2048, transmit: 4096, payloadUnit: 8},
	{receive: 128 << 10, transmit: 4096, payloadUnit: 8},
	{receive: 1024, transmit: 64, payloadUnit: 4},
}

// The settings that a fuzz run's configuration turns on, short enough for a
// script to see them work.
const (
	fuzzTimeout   = 10 * time.Second
	fuzzKeepAlive = time.Second
)

// The steps of a fuzz script, by the number that a script's octet chooses
// (modulo their count).
const (
	stepPeerSends byte = iota
	stepDispatch
	stepWait
	stepAppSends
	stepAppReceives
	stepAppCloses
	stepAppAborts
	stepAppOpens
	stepAddressLeavesAndReturns
	stepCount
)

// fuzzSteps are what each step has the peer, the interface or the
// application do.
var fuzzSteps = [stepCount]func(r *fuzzRun, script *scriptReader){
	stepPeerSends:               (*fuzzRun).peerSends,
	stepDispatch:                (*fuzzRun).dispatch,
	stepWait:                    (*fuzzRun).wait,
	stepAppSends:                (*fuzzRun).appSends,
	stepAppReceives:             (*fuzzRun).appReceives,
	stepAppCloses:               (*fuzzRun).appCloses,
	stepAppAborts:               (*fuzzRun).appAborts,
	stepAppOpens:                (*fuzzRun).appOpens,
	stepAddressLeavesAndReturns: (*fuzzRun).addressLeavesAndReturns,
}

// waitCountBits is how many of the lowest bits of a wait's octet give the
// count of its unit; the two highest choose the unit from fuzzClockUnits.
const waitCountBits = 6

var fuzzClockUnits = [...]time.Duration{time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second}

// waitInSeconds is the bits of a wait's octet that choose seconds.
const waitInSeconds = 3 << waitCountBits

// =========================================================================
// The peer's segments
// =========================================================================

// The control flags of the peer's segments, by the number a script gives.
const (
	peerNone byte = iota
	peerSYN
	peerFIN
	peerRST
	peerPSH
)

var peerControls = [...]wire.TCPControl{
	peerNone: wire.TCPControlNone,
	peerSYN:  wire.TCPControlSYN,
	peerFIN:  wire.TCPControlFIN,
	peerRST:  wire.TCPControlRST,
	peerPSH:  wire.TCPControlPSH,
}

// peerFlags is the flags octet of a peer's segment in a fuzz script.
type peerFlags byte

const (
	// peerFlagACK sets the ACK flag.
	peerFlagACK peerFlags = 1 << iota
	// peerFlagACKAll offsets the acknowledgement from the end of what the
	// socket sent, rather than from the oldest octet it has yet to see
	// acknowledged.
	peerFlagACKAll
	// peerFlagFarSeq and peerFlagFarACK count the sequence or the
	// acknowledgement offset in units of fuzzFarOffset octets.
	peerFlagFarSeq
	peerFlagFarACK
	// peerFlagWindowScale, peerFlagMSS and peerFlagSACKPermitted add those
	// options.
	peerFlagWindowScale
	peerFlagMSS
	peerFlagSACKPermitted
	// peerFlagHalfSpaceSeq moves the sequence number of a segment other than
	// a SYN maxSeqOffset further on, where comparisons modulo 2³² wrap
	// around: beyond every receive window.
	peerFlagHalfSpaceSeq
)

// fuzzFarOffset is the unit of a far offset: a few of them reach past any
// receive buffer of fuzzBuffers.
const fuzzFarOffset = 2048

// maxPeerWindowScale is RFC 7323's largest window scale, at which wire's
// parser caps the option.
const maxPeerWindowScale = 14

// peerMSSUnit is the octets that one unit of a script's MSS option stands
// for.
const peerMSSUnit = 8

func (f peerFlags) has(flag peerFlags) bool {
	return f&flag != 0
}

// offset returns the offset that a script's octet stands for: a signed count
// of octets, or of fuzzFarOffset octets when f has far.
func (f peerFlags) offset(octet byte, far peerFlags) int {
	unit := 1
	if f.has(far) {
		unit = fuzzFarOffset
	}
	return int(int8(octet)) * unit
}

// addOptions adds the options the flags ask for: a window scale of scale,
// capped as wire's parser caps it, and an MSS of mssUnits peerMSSUnit.
func (f peerFlags) addOptions(segment *wire.TCPRepr, scale, mssUnits byte) {
	if f.has(peerFlagWindowScale) {
		segment.WindowScale = wire.Some(min(scale, maxPeerWindowScale))
	}
	if f.has(peerFlagMSS) {
		segment.MaxSegmentSize = wire.Some(uint16(mssUnits) * peerMSSUnit)
	}
	segment.SACKPermitted = f.has(peerFlagSACKPermitted)
}

// The multipliers of the hashes that peerOctet and appOctet take: Knuth's
// multiplicative constant and xxHash's second prime, odd numbers that spread
// neighbouring sequence numbers over every octet value.
const (
	peerOctetMultiplier = 2654435761
	appOctetMultiplier  = 2246822519
)

// peerOctet is the octet the peer sends at sequence number seq, and
// appOctet the one the application writes there: the highest octet of a
// multiplicative hash of the number, so that an octet out of place shows.
func peerOctet(seq wire.TCPSeqNumber) byte {
	return byte(uint32(seq) * peerOctetMultiplier >> 24)
}

func appOctet(seq wire.TCPSeqNumber) byte {
	return byte(uint32(seq) * appOctetMultiplier >> 24)
}

// octetsFrom returns length octets of a stream from sequence number first
// on, as octet has them.
func octetsFrom(first wire.TCPSeqNumber, length int, octet func(wire.TCPSeqNumber) byte) []byte {
	octets := make([]byte, length)
	for i := range octets {
		octets[i] = octet(first.Add(i))
	}
	return octets
}

// =========================================================================
// A run
// =========================================================================

// fuzzRun is one run of the fuzz target: the socket, the clock, and the
// initial sequence numbers of the connections.
type fuzzRun struct {
	t           *testing.T
	s           *testSocket
	payloadUnit int
	// elapsed is the clock: the time after the epoch.
	elapsed time.Duration
	// socketISN and peerISN are the initial sequence numbers of the current
	// connection; each connection of a run has its own, so that octets left
	// from an earlier one never pass for the next one's.
	socketISN, peerISN wire.TCPSeqNumber
	// peerSeqEnd is the sequence number after the furthest that the peer has
	// sent on the current connection, its SYN and FIN counted, in a segment
	// the socket accepted but for one beyond every receive window; absent
	// until there is one. The socket acknowledges no more than that.
	peerSeqEnd wire.Optional[wire.TCPSeqNumber]
	// acknowledgementsUnchecked says that the socket took the peer's FIN
	// while it held octets beyond it (noteTakenFIN), so that it may
	// acknowledge sequence numbers beyond peerSeqEnd for the rest of the
	// connection.
	acknowledgementsUnchecked bool
	// takenFIN is the sequence number at which the socket took the peer's
	// FIN, while it has taken one (finReceived): where the peer's stream
	// ends in the socket's account.
	takenFIN wire.TCPSeqNumber
	// fastRetransmissionDue says whether a fast retransmission was pending,
	// or its timer set, when the current Dispatch began: the socket sends it
	// whatever the peer's window.
	fastRetransmissionDue bool
}

// fuzzISNStride separates the initial sequence numbers of a run's
// connections.
const fuzzISNStride = 7_919_993

func newFuzzRun(t *testing.T, config fuzzConfig) *fuzzRun {
	t.Helper()
	buffers := fuzzBuffers[config&configBuffers]
	r := &fuzzRun{
		t:           t,
		s:           socketClosedWithBufferSizes(t, buffers.transmit, buffers.receive),
		payloadUnit: buffers.payloadUnit,
		socketISN:   config.socketISNBase(),
		peerISN:     remoteSeq,
	}
	r.s.chooseISN = r.chooseISN
	r.applySettings(config)
	if config.has(configConnect) {
		r.connect()
	} else {
		r.listen()
	}
	return r
}

func (r *fuzzRun) applySettings(config fuzzConfig) {
	r.s.SetNagleEnabled(!config.has(configNagleOff))
	if config.has(configACKDelay) {
		r.s.SetAckDelay(defaultACKDelay)
	}
	if config.has(configTimeout) {
		r.s.SetTimeout(fuzzTimeout)
	}
	if config.has(configKeepAlive) {
		r.s.SetKeepAlive(fuzzKeepAlive)
	}
}

// chooseISN is the socket's source of initial sequence numbers: a new one
// for each connection.
func (r *fuzzRun) chooseISN(*socket.Context) wire.TCPSeqNumber {
	r.socketISN = r.socketISN.Add(fuzzISNStride)
	return r.socketISN
}

func (r *fuzzRun) now() time.Time {
	return instant(r.elapsed)
}

func (r *fuzzRun) take(script *scriptReader) {
	fuzzSteps[script.next()%stepCount](r, script)
}

// peerSends has the peer send the segment the script describes next, which
// the socket gets only if it accepts it, as from the interface.
func (r *fuzzRun) peerSends(script *scriptReader) {
	segment, beyondEveryWindow := r.peerSegment(script)
	ip := ipHeader(remoteAddr, localAddr, segment)
	if !r.s.Accepts(ip, segment) {
		return
	}
	// The socket acknowledges nothing of a segment beyond every receive
	// window, and peerSeqEnd, which compares modulo 2³² as the socket does,
	// holds only within half the sequence space of the peer's stream.
	if !beyondEveryWindow {
		r.notePeerSeqEnd(segment)
	}
	heldEnd := r.heldSeqEnd()
	finWasReceived := r.s.finReceived
	r.s.cx.SetNow(r.now())
	replyIP, reply, replied := r.s.Process(r.s.cx, ip, segment)
	// The FIN first: noting it says whether the reply's acknowledgement,
	// which covers the FIN, can be checked.
	if r.s.finReceived && !finWasReceived {
		r.noteTakenFIN(segment, heldEnd)
	}
	if replied {
		r.checkReply(segment, replyIP, reply)
	}
}

// notePeerSeqEnd records how far the sequence numbers of a segment the peer
// sent reach: its data, and its SYN or FIN, which take one each.
func (r *fuzzRun) notePeerSeqEnd(segment wire.TCPRepr) {
	seqEnd := segment.SeqNumber.Add(segment.SegmentLen())
	if furthest, ok := r.peerSeqEnd.Get(); ok {
		seqEnd = seqEnd.Max(furthest)
	}
	r.peerSeqEnd = wire.Some(seqEnd)
}

// heldSeqEnd returns the sequence number after the furthest octet of the
// peer's that the socket holds, in its receive buffer or its assembler.
func (r *fuzzRun) heldSeqEnd() wire.TCPSeqNumber {
	windowStart := r.s.nextExpectedSeq()
	heldEnd := windowStart
	for _, rangeEnd := range r.s.assembler.DataRanges() {
		heldEnd = heldEnd.Max(windowStart.Add(rangeEnd))
	}
	return heldEnd
}

// peerSegment returns the segment the script describes next: its control
// flag, its flags (peerFlags), the offsets of its sequence and
// acknowledgement numbers, its window, payload length and options. A SYN's
// sequence number is offset from the peer's initial one, any other
// segment's from the one the socket expects next, and beyond every receive
// window with peerFlagHalfSpaceSeq, which peerSegment reports; the
// acknowledgement number is offset from the oldest octet the socket has yet
// to see acknowledged or, with peerFlagACKAll, from the end of what it sent.
func (r *fuzzRun) peerSegment(script *scriptReader) (segment wire.TCPRepr, beyondEveryWindow bool) {
	control := peerControls[int(script.next())%len(peerControls)]
	flags := peerFlags(script.next())
	seq := r.peerSeqAnchor(control).Add(flags.offset(script.next(), peerFlagFarSeq))
	beyondEveryWindow = control != wire.TCPControlSYN && flags.has(peerFlagHalfSpaceSeq)
	if beyondEveryWindow {
		seq = seq.Add(maxSeqOffset)
	}
	ack := r.peerACKAnchor(flags).Add(flags.offset(script.next(), peerFlagFarACK))
	window := uint16(script.next())<<8 | uint16(script.next())
	payload := octetsFrom(seq, int(script.next())*r.payloadUnit, peerOctet)
	segment = fromRemote(withControl(control), withSeq(seq), withAck(ack), withWindow(window), withPayload(payload))
	if !flags.has(peerFlagACK) {
		segment.AckNumber = wire.Optional[wire.TCPSeqNumber]{}
	}
	scale, mssUnits := script.next(), script.next()
	flags.addOptions(&segment, scale, mssUnits)
	return segment, beyondEveryWindow
}

func (r *fuzzRun) peerSeqAnchor(control wire.TCPControl) wire.TCPSeqNumber {
	if control == wire.TCPControlSYN {
		return r.peerISN
	}
	return r.s.nextExpectedSeq()
}

func (r *fuzzRun) peerACKAnchor(flags peerFlags) wire.TCPSeqNumber {
	if flags.has(peerFlagACKAll) {
		return r.s.remoteLastSeq
	}
	return r.s.localSeqNumber
}

// dispatch has the interface poll the socket, and checks what it sends.
func (r *fuzzRun) dispatch(*scriptReader) {
	r.s.cx.SetNow(r.now())
	r.fastRetransmissionDue = r.s.pendingFastRetransmit || r.s.timer.kind == timerFastRetransmit
	r.s.Dispatch(r.s.cx, r.checkSent)
}

// wait moves the clock on by as long as the script's octet says
// (waitCountBits).
func (r *fuzzRun) wait(script *scriptReader) {
	octet := script.next()
	count, unit := octet&(1<<waitCountBits-1), fuzzClockUnits[octet>>waitCountBits]
	r.elapsed += time.Duration(count) * unit
}

// appSends has the application send as many units of payload as the script
// says, each octet the application's for the sequence number it is to take.
func (r *fuzzRun) appSends(script *scriptReader) {
	first := r.s.localSeqNumber.Add(r.s.SendQueue())
	data := octetsFrom(first, int(script.next())*r.payloadUnit, appOctet)
	// A socket whose transmit half is closed takes nothing, which is all
	// the error says.
	_, _ = r.s.SendSlice(data)
}

// appReceives has the application read as many units as the script says,
// and checks what it reads.
func (r *fuzzRun) appReceives(script *scriptReader) {
	first := r.firstUnreadSeq()
	data := make([]byte, int(script.next())*r.payloadUnit)
	received, err := r.s.ReceiveSlice(data)
	if errors.Is(err, ErrFinished) && r.s.receiveBuffer.Len() != 0 {
		r.t.Fatalf("ReceiveSlice reported the end of the stream with %d octets unread", r.s.receiveBuffer.Len())
	}
	r.checkReceived(first, data[:received])
}

func (r *fuzzRun) appCloses(*scriptReader) {
	r.s.Close()
}

func (r *fuzzRun) appAborts(*scriptReader) {
	r.s.Abort()
}

// appOpens has the application listen again, or connect again, as the
// script's octet says; an open socket refuses either.
func (r *fuzzRun) appOpens(script *scriptReader) {
	if script.next()%2 == 0 {
		r.listen()
	} else {
		r.connect()
	}
}

// addressLeavesAndReturns has the interface lose its address while it polls
// the socket, and get it back, as when PPP renegotiates: a connection from
// the address is reset.
func (r *fuzzRun) addressLeavesAndReturns(script *scriptReader) {
	r.setIPAddrs([]netip.Prefix{netip.PrefixFrom(otherAddr, 24)})
	r.dispatch(script)
	r.setIPAddrs(testIPAddrs())
}

func (r *fuzzRun) setIPAddrs(addresses []netip.Prefix) {
	if err := r.s.cx.SetIPAddrs(addresses); err != nil {
		r.t.Fatalf("SetIPAddrs(%v): %v", addresses, err)
	}
}

// listen and connect open the socket, which starts a new connection, and the
// peer a new stream; an open socket refuses either, and the error says no
// more.
func (r *fuzzRun) listen() {
	if err := r.s.Listen(listenEnd); err == nil {
		r.startPeerStream()
	}
}

func (r *fuzzRun) connect() {
	if err := r.s.Connect(r.s.cx, remoteEnd, listenEnd); err == nil {
		r.startPeerStream()
	}
}

// startPeerStream gives the peer's stream of a new connection its own
// initial sequence number, and nothing sent yet.
func (r *fuzzRun) startPeerStream() {
	r.peerISN = r.peerISN.Add(fuzzISNStride)
	r.peerSeqEnd = wire.Optional[wire.TCPSeqNumber]{}
	r.acknowledgementsUnchecked = false
}

// firstUnreadSeq returns the sequence number of the oldest octet in the
// receive buffer: remoteSeqNumber, which counts the peer's FIN too once the
// socket has taken it. That holds for the octets before the FIN; those the
// socket queues after it sit one sequence number further on, and are not
// part of the peer's stream (streamOctetsFrom).
func (r *fuzzRun) firstUnreadSeq() wire.TCPSeqNumber {
	if r.s.finReceived {
		return r.s.remoteSeqNumber.Add(-1)
	}
	return r.s.remoteSeqNumber
}

// streamOctetsFrom returns how many of length octets, read from sequence
// number first on, are part of the peer's stream: all of them until the
// socket takes the peer's FIN, then those before it.
func (r *fuzzRun) streamOctetsFrom(first wire.TCPSeqNumber, length int) int {
	switch {
	case !r.s.finReceived:
		return length
	case r.takenFIN.Compare(first) <= 0:
		return 0
	default:
		return min(length, r.takenFIN.Sub(first))
	}
}

// =========================================================================
// The checks
// =========================================================================

// checkReceived checks that data, read from sequence number first on, is
// what the peer sent there, as far as the peer's stream goes.
func (r *fuzzRun) checkReceived(first wire.TCPSeqNumber, data []byte) {
	r.t.Helper()
	data = data[:r.streamOctetsFrom(first, len(data))]
	if want := octetsFrom(first, len(data), peerOctet); !bytes.Equal(data, want) {
		r.t.Fatalf("read %x from sequence number %v on, where the peer sent %x", data, first, want)
	}
}

// noteTakenFIN records where the socket took the peer's FIN, from segment,
// which arrived while the socket held the peer's octets up to heldEnd, and
// checks it: the segment must carry a FIN, and the socket must take it where
// the peer sent it, right after the segment's data. A segment that ends
// before heldEnd, though, may join octets held beyond it to the stream, and
// the socket then ends the stream where they end, and
// acknowledges sequence numbers the peer never sent: the run stops checking
// the FIN's position, and acknowledgements for the rest of the connection.
func (r *fuzzRun) noteTakenFIN(segment wire.TCPRepr, heldEnd wire.TCPSeqNumber) {
	r.t.Helper()
	r.takenFIN = r.s.nextExpectedSeq().Add(-1)
	if segment.Control != wire.TCPControlFIN {
		r.t.Fatalf("the socket took a FIN from %s, which carries none", segmentDetails(segment))
	}
	sentAt := segment.SeqNumber.Add(len(segment.Payload))
	if heldEnd.Compare(sentAt) > 0 {
		r.acknowledgementsUnchecked = true
		return
	}
	if r.takenFIN != sentAt {
		r.t.Fatalf("the socket took the FIN of %s at %v, where the peer sent it at %v (state %v)",
			segmentDetails(segment), r.takenFIN, sentAt, r.s.state)
	}
}

// checkSent checks a segment the socket emits: its IP header, its
// acknowledgement, a payload no longer than the effective MSS and, but for a
// keep-alive's fake octet, a payload within the peer's window of the
// application's octets at their sequence numbers.
func (r *fuzzRun) checkSent(ip wire.IPv4Repr, segment wire.TCPRepr) {
	r.t.Helper()
	checkOutgoingIPHeader(r.t, ip, segment)
	r.checkAcknowledgement(segment)
	if mss := effectiveMSS(r.s.cx, r.s.remoteMSS); len(segment.Payload) > mss {
		r.t.Fatalf("sent %d octets in one segment, more than the effective MSS of %d", len(segment.Payload), mss)
	}
	if r.isKeepAlive(segment) {
		return
	}
	r.checkWithinThePeersWindow(segment)
	if want := octetsFrom(segment.SeqNumber, len(segment.Payload), appOctet); !bytes.Equal(segment.Payload, want) {
		r.t.Fatalf("sent %x from sequence number %v on, where the application wrote %x",
			segment.Payload, segment.SeqNumber, want)
	}
}

// isKeepAlive reports whether segment, which the socket is emitting, is a
// keep-alive: one fake octet just before the end of what it sent.
func (r *fuzzRun) isKeepAlive(segment wire.TCPRepr) bool {
	return bytes.Equal(segment.Payload, keepAlivePayload()) && segment.SeqNumber.Add(1) == r.s.remoteLastSeq
}

// checkWithinThePeersWindow checks that the data of segment, which the
// socket is emitting, ends within the peer's window, unless it is a
// zero-window probe or a fast retransmission.
func (r *fuzzRun) checkWithinThePeersWindow(segment wire.TCPRepr) {
	r.t.Helper()
	windowEnd := r.s.localSeqNumber.Add(r.s.remoteWindowLen)
	dataEnd := segment.SeqNumber.Add(len(segment.Payload))
	withinWindow := len(segment.Payload) == 0 || dataEnd.Compare(windowEnd) <= 0
	if withinWindow || r.isZeroWindowProbe(segment, windowEnd) || r.isFastRetransmission(segment) {
		return
	}
	r.t.Fatalf("sent %s, whose data ends at %v, beyond the peer's window, which ends at %v",
		segmentDetails(segment), dataEnd, windowEnd)
}

// isZeroWindowProbe reports whether segment, which the socket is emitting,
// is a zero-window probe: one octet after what it has sent, for which the
// peer's window, ending at windowEnd, has no room.
func (r *fuzzRun) isZeroWindowProbe(segment wire.TCPRepr, windowEnd wire.TCPSeqNumber) bool {
	return len(segment.Payload) == 1 && segment.SeqNumber == r.s.remoteLastSeq &&
		windowEnd.Compare(r.s.remoteLastSeq) <= 0
}

// isFastRetransmission reports whether segment, which the socket is
// emitting, is a fast retransmission: data from the oldest octet not yet
// acknowledged on, while one was due as Dispatch began.
func (r *fuzzRun) isFastRetransmission(segment wire.TCPRepr) bool {
	return r.fastRetransmissionDue && segment.SeqNumber == r.s.localSeqNumber
}

// checkAcknowledgement checks that segment, which the socket sends,
// acknowledges no sequence number beyond the furthest the peer sent
// (peerSeqEnd), unless acknowledgementsUnchecked. A RST goes unchecked: one
// that aborts a connection before the peer's SYN came acknowledges the
// stream the socket never synchronized with at zero.
func (r *fuzzRun) checkAcknowledgement(segment wire.TCPRepr) {
	r.t.Helper()
	ack, acknowledges := segment.AckNumber.Get()
	peerEnd, peerSent := r.peerSeqEnd.Get()
	if !acknowledges || !peerSent || r.acknowledgementsUnchecked || segment.Control == wire.TCPControlRST {
		return
	}
	if ack.Compare(peerEnd) > 0 {
		r.t.Fatalf("the socket sent %s, which acknowledges %v, beyond the peer's furthest sequence number, %v",
			segmentDetails(segment), ack, peerEnd)
	}
}

// checkReply checks the socket's reply to a segment from the peer: its IP
// header, its acknowledgement and its lack of payload, and that the segment
// was no RST, which is never answered.
func (r *fuzzRun) checkReply(segment wire.TCPRepr, ip wire.IPv4Repr, reply wire.TCPRepr) {
	r.t.Helper()
	checkOutgoingIPHeader(r.t, ip, reply)
	r.checkAcknowledgement(reply)
	if segment.Control == wire.TCPControlRST {
		r.t.Fatalf("the socket answered the RST %s with %s", segmentDetails(segment), segmentDetails(reply))
	}
	if len(reply.Payload) > 0 {
		r.t.Fatalf("the socket's reply %s carries data", segmentDetails(reply))
	}
}

// checkInvariants checks what holds after every step: the end of what the
// socket sent is never behind what the peer acknowledged, the assembler holds
// no data beyond the receive buffer, and PollAt, which only looks, works in
// whatever state the step left.
func (r *fuzzRun) checkInvariants() {
	r.t.Helper()
	s := r.s
	if s.remoteLastSeq.Compare(s.localSeqNumber) < 0 {
		r.t.Fatalf("remoteLastSeq %v is behind localSeqNumber %v", s.remoteLastSeq, s.localSeqNumber)
	}
	for start, end := range s.assembler.DataRanges() {
		if end > s.receiveBuffer.Window() {
			r.t.Fatalf("the assembler holds octets %d to %d, beyond the %d free in the receive buffer",
				start, end, s.receiveBuffer.Window())
		}
	}
	s.cx.SetNow(r.now())
	s.PollAt(s.cx)
}

// =========================================================================
// The seeds
// =========================================================================

// fuzzSeed is a script that takes the socket somewhere worth fuzzing from.
type fuzzSeed struct {
	config fuzzConfig
	script []byte
}

// seedSegment is a peer's segment in a seed's script: the parameters that
// peerSegment reads.
type seedSegment struct {
	control              byte
	flags                peerFlags
	seqOffset, ackOffset int8
	window               uint16
	payloadUnits         byte
	windowScale          byte
	mssUnits             byte
}

// scriptBuilder writes the script of a seed, a step at a time.
type scriptBuilder struct {
	script []byte
}

func (b *scriptBuilder) step(step byte, parameters ...byte) *scriptBuilder {
	b.script = append(append(b.script, step), parameters...)
	return b
}

func (b *scriptBuilder) peer(segment seedSegment) *scriptBuilder {
	return b.step(stepPeerSends, segment.control, byte(segment.flags), byte(segment.seqOffset), byte(segment.ackOffset),
		byte(segment.window>>8), byte(segment.window), segment.payloadUnits, segment.windowScale, segment.mssUnits)
}

func (b *scriptBuilder) waitSeconds(seconds byte) *scriptBuilder {
	return b.step(stepWait, waitInSeconds|seconds)
}

// The peer's segments that seeds use most: its SYN, with every option, and
// its acknowledgement of everything the socket sent.
var (
	seedSYN = seedSegment{control: peerSYN, flags: peerFlagWindowScale | peerFlagMSS | peerFlagSACKPermitted,
		window: 1024, mssUnits: 182}
	seedACKAll = seedSegment{control: peerNone, flags: peerFlagACK | peerFlagACKAll, window: 1024}
)

func fuzzSeeds() []fuzzSeed {
	return []fuzzSeed{
		{0, passiveOpenAndCloseScript()},
		{1 | configConnect | configNagleOff, lossAndRecoveryScript()},
		{0, zeroWindowScript()},
		{configKeepAlive | configTimeout, keepAliveAndTimeoutScript()},
		{1 | configConnect | configACKDelay, delayedACKScript()},
		{3 | configConnect, simultaneousCloseThenReuseScript()},
		{configConnect, peerSYNBeforeTheSocketsOwnScript()},
		{configConnect, closeBeforeThePeersSYNScript()},
		{0, remoteCloseScript()},
		{0, dataAfterThePeersFINScript()},
		{0, finJoiningHeldDataScript()},
		{0, finAfterDataCutAtTheWindowsEndScript()},
		{0, farSegmentsAfterAFINAtTheWindowsEndScript()},
		{1 | configNagleOff, closeThroughFinWait2Script()},
		{0, addressChangeScript()},
		{2, dataBeyondAScaledBufferAfterAPassiveOpenScript()},
		{2 | configConnect, dataBeyondAScaledBufferAfterAnActiveOpenScript()},
		{configConnect | configUpperHalfISN, peerSYNBeforeTheSocketsOwnScript()},
	}
}

// passiveOpenAndCloseScript has a listening socket take a connection,
// exchange data, and close first.
func passiveOpenAndCloseScript() []byte {
	var b scriptBuilder
	b.peer(seedSYN).step(stepDispatch).peer(seedACKAll)
	b.peer(seedSegment{control: peerPSH, flags: peerFlagACK | peerFlagACKAll, window: 1024, payloadUnits: 10})
	b.step(stepDispatch).step(stepAppReceives, 10).step(stepAppSends, 10).step(stepDispatch).peer(seedACKAll)
	b.step(stepAppCloses).step(stepDispatch)
	b.peer(seedSegment{control: peerFIN, flags: peerFlagACK | peerFlagACKAll, window: 1024})
	b.step(stepDispatch).waitSeconds(11).step(stepDispatch)
	return b.script
}

// lossAndRecoveryScript has a connecting socket send data in segments of the
// peer's small MSS, get duplicate ACKs that bring a fast retransmission, and
// then a retransmission timeout.
func lossAndRecoveryScript() []byte {
	duplicateACK := seedSegment{control: peerNone, flags: peerFlagACK, window: 4096}
	var b scriptBuilder
	b.step(stepDispatch)
	b.peer(seedSegment{control: peerSYN, flags: peerFlagACK | peerFlagACKAll | peerFlagWindowScale | peerFlagMSS,
		window: 4096, mssUnits: 13})
	b.step(stepAppSends, 50).step(stepDispatch).step(stepDispatch).step(stepDispatch).step(stepDispatch)
	b.peer(duplicateACK).peer(duplicateACK).peer(duplicateACK).peer(duplicateACK).step(stepDispatch)
	b.waitSeconds(1).step(stepDispatch).peer(seedSegment{control: peerNone, flags: peerFlagACK | peerFlagACKAll, window: 4096})
	return b.script
}

// remoteCloseScript has the peer close first, with data: the socket reads,
// answers and closes in turn, and the peer acknowledges part of its data
// before its FIN.
func remoteCloseScript() []byte {
	var b scriptBuilder
	b.peer(seedSYN).step(stepDispatch).peer(seedACKAll)
	b.peer(seedSegment{control: peerFIN, flags: peerFlagACK | peerFlagACKAll, window: 1024, payloadUnits: 5})
	b.step(stepDispatch).step(stepAppReceives, 5).step(stepAppSends, 8).step(stepAppCloses).step(stepDispatch)
	b.peer(seedSegment{control: peerNone, flags: peerFlagACK, ackOffset: 4, window: 1024})
	b.peer(seedSegment{control: peerNone, flags: peerFlagACK, ackOffset: 4, window: 1024}).peer(seedACKAll)
	return b.script
}

// dataAfterThePeersFINScript has the peer close with data and then send more
// beyond its FIN, which the socket queues in CLOSE-WAIT for
// the application to read after the data before the FIN.
func dataAfterThePeersFINScript() []byte {
	var b scriptBuilder
	b.peer(seedSYN).step(stepDispatch).peer(seedACKAll)
	b.peer(seedSegment{control: peerFIN, flags: peerFlagACK | peerFlagACKAll, window: 1024, payloadUnits: 3})
	b.peer(seedSegment{control: peerPSH, flags: peerFlagACK | peerFlagACKAll, window: 1024, payloadUnits: 3})
	b.step(stepAppReceives, 6)
	return b.script
}

// finJoiningHeldDataScript has the peer send data beyond a gap, and then
// close with a segment that fills the gap: the socket takes
// the FIN where the data it held ends, beyond where the peer sent it.
func finJoiningHeldDataScript() []byte {
	var b scriptBuilder
	b.peer(seedSYN).step(stepDispatch).peer(seedACKAll)
	b.peer(seedSegment{control: peerPSH, flags: peerFlagACK | peerFlagACKAll, seqOffset: 5, window: 1024,
		payloadUnits: 5})
	b.peer(seedSegment{control: peerFIN, flags: peerFlagACK | peerFlagACKAll, window: 1024, payloadUnits: 5})
	b.step(stepDispatch).step(stepAppReceives, 10)
	return b.script
}

// finAfterDataCutAtTheWindowsEndScript has the peer close with a segment one
// octet longer than the receive window of fuzzBuffers[0]: the socket takes
// the data up to the window's end but not the FIN, which the peer sends
// again with the last octet once the application has read and the window
// has opened.
func finAfterDataCutAtTheWindowsEndScript() []byte {
	windowLen := fuzzBuffers[0].receive
	var b scriptBuilder
	b.peer(seedSYN).step(stepDispatch).peer(seedACKAll)
	b.peer(seedSegment{control: peerFIN, flags: peerFlagACK | peerFlagACKAll, window: 1024,
		payloadUnits: byte(windowLen + 1)})
	b.step(stepDispatch).step(stepAppReceives, byte(windowLen)).step(stepDispatch)
	b.peer(seedSegment{control: peerFIN, flags: peerFlagACK | peerFlagACKAll, window: 1024, payloadUnits: 1})
	b.step(stepDispatch).step(stepAppReceives, 1)
	return b.script
}

// farSegmentsAfterAFINAtTheWindowsEndScript has the peer close with data that
// fills the receive window of fuzzBuffers[0], its FIN right at the window's
// end, so that the window ends before it starts until the socket's next
// segment; meanwhile the peer sends a RST and data maxSeqOffset past the
// window's start: taken for inside the window, it makes the stack panic.
func farSegmentsAfterAFINAtTheWindowsEndScript() []byte {
	windowLen := fuzzBuffers[0].receive
	var b scriptBuilder
	b.peer(seedSYN).step(stepDispatch).peer(seedACKAll)
	b.peer(seedSegment{control: peerFIN, flags: peerFlagACK | peerFlagACKAll, window: 1024,
		payloadUnits: byte(windowLen)})
	b.peer(seedSegment{control: peerRST, flags: peerFlagHalfSpaceSeq, window: 1024})
	b.peer(seedSegment{control: peerNone, flags: peerFlagACK | peerFlagACKAll | peerFlagHalfSpaceSeq, window: 1024,
		payloadUnits: 1})
	b.step(stepDispatch)
	return b.script
}

// closeThroughFinWait2Script has the socket close first, the peer acknowledge
// its FIN, send more data, and close a second later.
func closeThroughFinWait2Script() []byte {
	var b scriptBuilder
	b.peer(seedSYN).step(stepDispatch).peer(seedACKAll).step(stepAppSends, 4).step(stepAppCloses).step(stepDispatch)
	b.peer(seedACKAll)
	b.peer(seedSegment{control: peerPSH, flags: peerFlagACK | peerFlagACKAll, window: 1024, payloadUnits: 30})
	b.step(stepDispatch).step(stepAppReceives, 30).waitSeconds(1)
	b.peer(seedSegment{control: peerFIN, flags: peerFlagACK | peerFlagACKAll, window: 1024}).step(stepDispatch)
	return b.script
}

// addressChangeScript has the interface's address leave and return while a
// connection has data in both directions, and the socket listen again.
func addressChangeScript() []byte {
	var b scriptBuilder
	b.peer(seedSYN).step(stepDispatch).peer(seedACKAll).step(stepAppSends, 10)
	b.peer(seedSegment{control: peerPSH, flags: peerFlagACK, window: 1024, payloadUnits: 10})
	b.step(stepAddressLeavesAndReturns).step(stepAppOpens, 0).peer(seedSYN).step(stepDispatch)
	return b.script
}

// zeroWindowScript has the peer close its window, and the socket probe it.
func zeroWindowScript() []byte {
	var b scriptBuilder
	b.peer(seedSYN).step(stepDispatch).peer(seedSegment{control: peerNone, flags: peerFlagACK | peerFlagACKAll})
	b.step(stepAppSends, 20).step(stepDispatch).waitSeconds(1).step(stepDispatch).waitSeconds(2).step(stepDispatch)
	b.peer(seedSegment{control: peerNone, flags: peerFlagACK | peerFlagACKAll, window: 64}).step(stepDispatch)
	return b.script
}

// keepAliveAndTimeoutScript has an idle connection send keep-alives, which
// the peer leaves unanswered until the timeout.
func keepAliveAndTimeoutScript() []byte {
	var b scriptBuilder
	b.peer(seedSYN).step(stepDispatch).peer(seedACKAll).step(stepDispatch)
	b.waitSeconds(1).step(stepDispatch).waitSeconds(10).step(stepDispatch)
	return b.script
}

// delayedACKScript has a connecting socket that delays its ACKs send data
// and receive some: it holds back the ACK of the peer's first segment until
// the delay has passed, and acknowledges at once when more than an MSS of
// data waits.
func delayedACKScript() []byte {
	var b scriptBuilder
	b.step(stepDispatch)
	b.peer(seedSegment{control: peerSYN, flags: peerFlagACK | peerFlagACKAll, window: 4096})
	b.step(stepAppSends, 100).step(stepDispatch).step(stepDispatch)
	b.peer(seedSegment{control: peerPSH, flags: peerFlagACK, window: 4096, payloadUnits: 20})
	b.step(stepDispatch).waitSeconds(1).step(stepDispatch).step(stepAppReceives, 20)
	b.peer(seedSegment{control: peerPSH, flags: peerFlagACK | peerFlagACKAll, window: 4096, payloadUnits: 40})
	b.peer(seedSegment{control: peerPSH, flags: peerFlagACK | peerFlagACKAll, window: 4096, payloadUnits: 40})
	b.step(stepDispatch).step(stepAppReceives, 80)
	return b.script
}

// simultaneousCloseThenReuseScript has both ends close at once, TIME-WAIT
// end, and the socket listen for another connection.
func simultaneousCloseThenReuseScript() []byte {
	var b scriptBuilder
	b.step(stepDispatch).peer(seedSegment{control: peerSYN, flags: peerFlagACK | peerFlagACKAll, window: 1024})
	b.step(stepAppSends, 3).step(stepAppCloses)
	b.peer(seedSegment{control: peerFIN, flags: peerFlagACK, window: 1024, payloadUnits: 2})
	b.step(stepDispatch).peer(seedACKAll).waitSeconds(11).step(stepDispatch)
	b.step(stepAppOpens, 0).peer(seedSYN).step(stepDispatch).peer(seedACKAll)
	return b.script
}

// peerSYNBeforeTheSocketsOwnScript has a connecting socket get the peer's SYN
// before its own has gone out, where poll_at panics.
func peerSYNBeforeTheSocketsOwnScript() []byte {
	var b scriptBuilder
	b.peer(seedSegment{control: peerSYN, window: 1024}).step(stepDispatch)
	return b.script
}

// closeBeforeThePeersSYNScript has a connecting socket drop a stray RST with
// a sequence number below zero, and close before the peer's SYN comes: the
// RST it sends then acknowledges sequence number zero, beyond the stray
// one's.
func closeBeforeThePeersSYNScript() []byte {
	var b scriptBuilder
	b.step(stepDispatch).peer(seedSegment{control: peerRST, seqOffset: -74, window: 1024})
	b.step(stepAppCloses).step(stepDispatch)
	return b.script
}

// seedDataBeyondTheScaledBuffer is data that the peer of a socket with a
// receive buffer of 128 KiB sends right after the handshake, 128 KiB past
// the start of its stream: past the buffer's end, which the socket refuses.
var seedDataBeyondTheScaledBuffer = seedSegment{control: peerNone, flags: peerFlagACK | peerFlagACKAll | peerFlagFarSeq,
	seqOffset: 64, window: 1024, payloadUnits: 12}

// dataBeyondAScaledBufferAfterAPassiveOpenScript has a listening socket take
// seedDataBeyondTheScaledBuffer after the handshake.
func dataBeyondAScaledBufferAfterAPassiveOpenScript() []byte {
	var b scriptBuilder
	b.peer(seedSegment{control: peerSYN, flags: peerFlagWindowScale, window: 1024}).step(stepDispatch).peer(seedACKAll)
	b.peer(seedDataBeyondTheScaledBuffer)
	return b.script
}

// dataBeyondAScaledBufferAfterAnActiveOpenScript has a connecting socket take
// seedDataBeyondTheScaledBuffer after the peer's SYN|ACK.
func dataBeyondAScaledBufferAfterAnActiveOpenScript() []byte {
	var b scriptBuilder
	b.step(stepDispatch).peer(seedSegment{control: peerSYN, flags: peerFlagACK | peerFlagACKAll | peerFlagWindowScale,
		window: 1024})
	b.peer(seedDataBeyondTheScaledBuffer)
	return b.script
}
