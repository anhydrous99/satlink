package tcp

import (
	"net/netip"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/storage"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// SocketBuffer is a socket's receive or transmit buffer: a ring of octets.
type SocketBuffer = storage.RingBuffer[byte]

// NewSocketBuffer returns an empty buffer of capacity octets. A receive
// buffer's capacity is the largest window the socket offers. RFC 7323 limits
// window scaling to a shift of 14, enough for any buffer smaller than 1 GiB.
// NewSocket refuses a larger one; one of exactly 1 GiB is offered with a
// shift of 15, which peers read as 14.
func NewSocketBuffer(capacity int) *SocketBuffer {
	return storage.NewRingBuffer[byte](capacity)
}

// maxReceiveBufferLen is the largest receive buffer a socket takes. From
// RFC 1323: "[...] the above constraints imply that 2 * the max window size
// must be less than 2**31 [...] Thus, the shift count must be limited to 14
// (which allows windows of 2**30 = 1 Gbyte)." The check here lets 2**30 itself
// through, and windowShiftFor gives it a shift of 15.
const maxReceiveBufferLen = 1 << 30

// defaultACKDelay is how long a new socket waits before sending a bare
// acknowledgement, hoping it can ride on outgoing data instead.
const defaultACKDelay = 10 * time.Millisecond

// defaultMSS is the peer's maximum segment size until its SYN announces one:
// the 536 octets every TCP must accept, RFC 879's default.
const defaultMSS = 536

// minRemoteMSS is the smallest MSS the socket accepts from the peer, the same
// value as Linux's TCP_MIN_SND_MSS: without it, a peer advertising a tiny MSS
// could force segments that carry next to nothing. The value exceeds the 12
// octets of TCP timestamps, so that a segment still carries some payload once
// they are subtracted from the effective MSS. This socket sends no options, and
// the value is kept all the same.
const minRemoteMSS = 48

// Socket is a TCP socket. Its zero value is not usable; create one with
// NewSocket. A Socket is not safe for concurrent use.
type Socket struct {
	// connection is everything that describes the current connection; reset
	// starts it afresh. Its fields are promoted.
	connection

	receiveBuffer  *SocketBuffer
	transmitBuffer *SocketBuffer

	// timeout is the interval after which, if no inbound packets are
	// received, the connection is aborted; 0 when there is none.
	timeout time.Duration
	// keepAlive is the interval at which keep-alive packets are sent; 0 when
	// keep-alive is off.
	keepAlive time.Duration
	// ackDelay is the duration for delayed ACKs; 0 when ACKs are not delayed.
	ackDelay time.Duration
	// nagleEnabled says whether Nagle's algorithm is on.
	nagleEnabled bool

	// The fields below describe the connection too, but a reset does not
	// reinitialise them, so a new connection starts with what the last one left
	// in them.

	// remoteHasSACK says whether the remote supports selective ACK as
	// described in RFC 2018.
	remoteHasSACK bool
	// localReceiveLastSeq is the sequence number of the last packet
	// received, used for SACK.
	localReceiveLastSeq wire.Optional[wire.TCPSeqNumber]
	// localReceiveLastAck is the acknowledgement number of the last packet
	// received.
	localReceiveLastAck wire.Optional[wire.TCPSeqNumber]
	// localReceiveDuplicateACKs is the number of packets received directly
	// after each other which have the same acknowledgement number; it
	// saturates at 255.
	localReceiveDuplicateACKs uint8
	// pendingFastRetransmit says whether a fast retransmit needs to occur.
	pendingFastRetransmit bool

	// chooseISN returns the initial sequence number of a new connection. It
	// draws one from the interface's random source; internal tests replace
	// it with a fixed number.
	chooseISN func(cx *socket.Context) wire.TCPSeqNumber
}

// NewSocket returns a closed socket that receives into receiveBuffer and
// sends from transmitBuffer. It starts with Nagle's algorithm on, a 10 ms ACK
// delay, and no timeout or keep-alive. It panics if receiveBuffer is larger
// than 1 GiB.
func NewSocket(receiveBuffer, transmitBuffer *SocketBuffer) *Socket {
	if receiveBuffer.Capacity() > maxReceiveBufferLen {
		panic("receiving buffer too large, cannot exceed 1 GiB")
	}
	return &Socket{
		connection:     newConnection(receiveBuffer.Capacity()),
		receiveBuffer:  receiveBuffer,
		transmitBuffer: transmitBuffer,
		ackDelay:       defaultACKDelay,
		nagleEnabled:   true,
		chooseISN:      randomISN,
	}
}

// randomISN draws an initial sequence number from the interface's random
// source.
func randomISN(cx *socket.Context) wire.TCPSeqNumber {
	return wire.TCPSeqNumber(int32(cx.Rand().Uint32()))
}

func (s *Socket) setState(state State) {
	if s.state != state {
		netlog.Trace("state", "from", s.state, "to", state)
	}
	s.state = state
}

// optionalDuration returns d as a duration option, where zero means none: a
// negative duration is none too.
func optionalDuration(d time.Duration) time.Duration {
	return max(d, 0)
}

// SetTimeout makes the socket abort the connection, sending a RST, once the
// peer has been silent for timeout, whether the socket waits for an answer
// or is idle: with keep-alive on, a live peer keeps answering, but without
// it an idle connection is aborted too. The silence counts from the last
// segment received or, after a quiet spell (Connect, or data queued into an
// empty transmit buffer), from the next Dispatch. Zero or negative means no
// timeout.
func (s *Socket) SetTimeout(timeout time.Duration) {
	s.timeout = optionalDuration(timeout)
}

// SetAckDelay makes the socket wait up to delay before sending a bare
// acknowledgement, hoping it can ride on outgoing data instead; it is 10 ms
// by default. Zero or negative sends acknowledgements at once.
func (s *Socket) SetAckDelay(delay time.Duration) {
	s.ackDelay = optionalDuration(delay)
}

// SetNagleEnabled turns Nagle's algorithm ("tinygram prevention") on or off;
// it is on by default. While on, at most one segment smaller than the MSS is
// in flight at a time. Turning it off is Linux's TCP_NODELAY.
func (s *Socket) SetNagleEnabled(enabled bool) {
	s.nagleEnabled = enabled
}

// SetKeepAlive makes an idle socket send a keep-alive segment whenever it has
// heard nothing for interval; together with SetTimeout, that detects a peer
// that went away. Zero or negative turns keep-alive off, though a keep-alive
// already timed may still go out. Turning it on takes effect at once on an
// idle connection.
func (s *Socket) SetKeepAlive(interval time.Duration) {
	s.keepAlive = optionalDuration(interval)
	if s.keepAlive > 0 {
		// If the connection is idle and we've just set the option, it would
		// not take effect until the next packet, unless we wind up the timer
		// explicitly.
		s.timer.setKeepAlive()
	}
}

// LocalEndpoint returns the local end of the connection's four-tuple, or the
// zero AddrPort when the socket has none: before Connect, while listening,
// and once the connection is over (a RST received, the last ACK in LAST-ACK,
// the end of TIME-WAIT, a reset). A socket moved to CLOSED by Abort, by a
// timeout or by Close while connecting keeps its four-tuple until its RST
// has been sent, and TIME-WAIT keeps it too; its port therefore still counts
// as in use.
func (s *Socket) LocalEndpoint() netip.AddrPort {
	tuple, _ := s.tuple.Get()
	return tuple.local
}

// RemoteEndpoint returns the remote end of the connection's four-tuple, or
// the zero AddrPort when the socket has none; see LocalEndpoint.
func (s *Socket) RemoteEndpoint() netip.AddrPort {
	tuple, _ := s.tuple.Get()
	return tuple.remote
}

// State returns the socket's state in the TCP state machine.
func (s *Socket) State() State {
	return s.state
}

// IsListening reports whether the socket is in the LISTEN state.
func (s *Socket) IsListening() bool {
	return s.state == StateListen
}

// IsOpen reports whether the socket is in use, listening or with a
// connection that is not over: any state except CLOSED and TIME-WAIT. A
// TIME-WAIT socket still answers a repeated FIN, though, and an aborted one
// still sends its RST. IsOpen says nothing about whether data can be sent or
// received; see CanSend and CanReceive.
func (s *Socket) IsOpen() bool {
	return s.state != StateClosed && s.state != StateTimeWait
}

// IsActive reports whether the socket exchanges segments with a peer: any
// state except CLOSED, TIME-WAIT and LISTEN. Aborting an active socket sends
// a RST.
func (s *Socket) IsActive() bool {
	return s.IsOpen() && s.state != StateListen
}

// MaySend reports whether data sent now can reach the peer: the transmit half
// is open (ESTABLISHED or CLOSE-WAIT). The transmit buffer may still be full.
func (s *Socket) MaySend() bool {
	// In CLOSE-WAIT, the remote endpoint has closed our receive half of the
	// connection but we still can transmit indefinitely.
	return s.state == StateEstablished || s.state == StateCloseWait
}

// MayReceive reports whether data can still be received: the receive half is
// open (ESTABLISHED, FIN-WAIT-1 or FIN-WAIT-2) or data is still buffered.
func (s *Socket) MayReceive() bool {
	switch s.state {
	case StateEstablished, StateFinWait1, StateFinWait2:
		// In FIN-WAIT-1/2, we have closed our transmit half of the
		// connection but we still can receive indefinitely.
		return true
	default:
		// If we have something in the receive buffer, we can receive that.
		return s.CanReceive()
	}
}

// CanSend reports whether MaySend holds and the transmit buffer has room.
func (s *Socket) CanSend() bool {
	return s.MaySend() && !s.transmitBuffer.IsFull()
}

// CanReceive reports whether the receive buffer holds data.
func (s *Socket) CanReceive() bool {
	return !s.receiveBuffer.IsEmpty()
}

// SendCapacity returns the transmit buffer's capacity, in octets.
func (s *Socket) SendCapacity() int {
	return s.transmitBuffer.Capacity()
}
