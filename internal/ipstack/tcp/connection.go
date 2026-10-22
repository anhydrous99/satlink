package tcp

import (
	"net/netip"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/storage"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// connection is the part of a socket that a reset reinitialises.
type connection struct {
	state        State
	timer        timer
	rttEstimator rttEstimator
	assembler    storage.Assembler
	// finReceived says whether the peer's FIN has been received: the end of
	// the stream it sends.
	finReceived bool
	// listenEndpoint is the endpoint given to Listen, kept so that a RST in
	// SYN-RECEIVED can go back to listening on it.
	listenEndpoint wire.ListenEndpoint
	// tuple is the current four-tuple (local and remote endpoints).
	tuple wire.Optional[fourTuple]
	// localSeqNumber is the sequence number corresponding to the beginning
	// of the transmit buffer: an ACK of localSeqNumber+n removes n octets
	// from it.
	localSeqNumber wire.TCPSeqNumber
	// remoteSeqNumber is the sequence number corresponding to the beginning
	// of the receive buffer: reading n octets adds n to it.
	remoteSeqNumber wire.TCPSeqNumber
	// remoteLastSeq is the last sequence number sent; in an idle socket,
	// localSeqNumber plus the transmit buffer's length.
	remoteLastSeq wire.TCPSeqNumber
	// remoteLastAck is the last acknowledgement number sent; in an idle
	// socket, remoteSeqNumber plus the receive buffer's length.
	remoteLastAck wire.Optional[wire.TCPSeqNumber]
	// remoteLastWindow is the last window field value sent.
	remoteLastWindow uint16
	// remoteWindowShift is the receive window scaling factor advertised to
	// remotes which support RFC 1323. It is zero if the window fits 64 KiB
	// or the remote does not support it.
	remoteWindowShift uint8
	// remoteWindowLen is the remote window size, relative to localSeqNumber:
	// the socket may send octets until localSeqNumber+remoteWindowLen.
	remoteWindowLen int
	// remoteWindowScale is the window scale the remote announced (RFC 1323),
	// absent if it does not support scaling.
	remoteWindowScale wire.Optional[uint8]
	// remoteMSS is the maximum number of data octets that the remote side may
	// receive in one segment.
	remoteMSS int
	// remoteLastTimestamp is when the peer was last heard from: the time of
	// the last segment received or, once a quiet spell ends (Connect, or data
	// queued into an empty transmit buffer, which clears it), the time of the
	// next Dispatch. The timeout runs from it.
	remoteLastTimestamp wire.Optional[time.Time]
	// ackDelayTimer holds back segments carrying only an ACK or a window
	// update until it expires.
	ackDelayTimer ackDelayTimer
	// challengeACKTimer rate-limits challenge ACKs: none are sent before it.
	challengeACKTimer time.Time
}

// fourTuple is a connection's local and remote endpoints.
type fourTuple struct {
	local  netip.AddrPort
	remote netip.AddrPort
}

// newConnection returns the state of a socket without a connection, for a
// receive buffer of receiveCapacity octets.
func newConnection(receiveCapacity int) connection {
	return connection{
		rttEstimator:      newRTTEstimator(),
		remoteWindowShift: windowShiftFor(receiveCapacity),
		remoteMSS:         defaultMSS,
	}
}

// reset forgets the connection and empties both buffers, keeping the
// settings.
func (s *Socket) reset() {
	s.connection = newConnection(s.receiveBuffer.Capacity())
	s.transmitBuffer.Clear()
	s.receiveBuffer.Clear()
}

// forgetTuple drops the four-tuple: the socket no longer has a peer to send
// anything to.
func (s *Socket) forgetTuple() {
	s.tuple = wire.Optional[fourTuple]{}
}

// Listen makes the socket wait for one connection to local; an open Addr
// listens on every address of the interface. It returns ErrUnaddressable
// for port 0 or an Addr that is neither open nor IPv4 (Is4), and
// ErrInvalidState when the socket is open, except that listening again on
// the same endpoint while listening succeeds and changes nothing (so an
// acceptor loop needs no Abort, which could drop a connection arriving in
// between).
func (s *Socket) Listen(local wire.ListenEndpoint) error {
	if local.Port == 0 || (local.Addr.IsValid() && !local.Addr.Is4()) {
		return ErrUnaddressable
	}
	if s.IsOpen() {
		// If we were already listening to same endpoint there is nothing to
		// do; exit early.
		//
		// In the past listening on a socket that was already listening was
		// an error, however this makes writing an acceptor loop with
		// multiple sockets impossible. Without this early exit, if you tried
		// to listen on a socket that's already listening you'll immediately
		// get an error. The only way around this is to abort the socket first
		// before listening again, but this means that incoming connections
		// can actually get aborted between the abort() and the next listen().
		if s.state == StateListen && s.listenEndpoint == local {
			return nil
		}
		return ErrInvalidState
	}
	s.reset()
	s.listenEndpoint = local
	s.setState(StateListen)
	return nil
}

// Connect starts connecting to remote from local; the SYN leaves on the next
// poll. local.Port must be set; an open local.Addr is chosen with
// cx.SourceAddress. Connect returns ErrInvalidState when the socket is open,
// and ErrUnaddressable for a zero remote or local port, a remote or local
// address that is unspecified or not IPv4 (Is4), or when the interface has
// no address to send from.
func (s *Socket) Connect(cx *socket.Context, remote netip.AddrPort, local wire.ListenEndpoint) error {
	if s.IsOpen() {
		return ErrInvalidState
	}
	if remote.Port() == 0 || !isConnectableAddress(remote.Addr()) || local.Port == 0 {
		return ErrUnaddressable
	}
	localAddr, ok := localAddressFor(cx, remote.Addr(), local.Addr)
	if !ok {
		return ErrUnaddressable
	}
	s.reset()
	s.tuple = wire.Some(fourTuple{local: netip.AddrPortFrom(localAddr, local.Port), remote: remote})
	s.setState(StateSynSent)
	initialSeq := s.chooseISN(cx)
	s.localSeqNumber = initialSeq
	s.remoteLastSeq = initialSeq
	return nil
}

// localAddressFor returns the address to connect to remote from: requested
// when it is set and usable, or else, if the local address is not provided,
// the one the interface chooses automatically.
func localAddressFor(cx *socket.Context, remote, requested netip.Addr) (netip.Addr, bool) {
	if requested.IsValid() {
		return requested, isConnectableAddress(requested)
	}
	return cx.SourceAddress(remote)
}

// isConnectableAddress reports whether addr can be one end of a connection:
// an IPv4 address other than 0.0.0.0.
func isConnectableAddress(addr netip.Addr) bool {
	return addr.Is4() && !addr.IsUnspecified()
}

// Close closes the transmit half of the connection: a FIN follows the data
// already queued, while receiving goes on until the peer closes too. A
// listening socket closes at once, and so does one whose SYN is unanswered
// (SYN-SENT), which then sends a RST on the next poll. There is no way to
// close only the receive half; use Abort to drop the connection.
//
// If the peer's FIN arrives after Close, the socket then waits in TIME-WAIT
// for 10 s (restarted whenever the peer repeats its FIN) and resets: octets
// still unread at that point are discarded, and receiving fails with
// ErrInvalidState.
func (s *Socket) Close() {
	switch s.state {
	case StateListen:
		// In the LISTEN state there is no established connection.
		s.setState(StateClosed)
	case StateSynSent:
		// In the SYN-SENT state the remote endpoint is not yet synchronized
		// and, upon receiving an RST, will abort the connection.
		s.setState(StateClosed)
	case StateSynReceived, StateEstablished:
		// In the SYN-RECEIVED, ESTABLISHED and CLOSE-WAIT states the transmit
		// half of the connection is open, and needs to be explicitly closed
		// with a FIN.
		s.setState(StateFinWait1)
	case StateCloseWait:
		s.setState(StateLastAck)
	default:
		// In the FIN-WAIT-1, FIN-WAIT-2, CLOSING, LAST-ACK, TIME-WAIT and
		// CLOSED states, the transmit half of the connection is already
		// closed, and no further action is needed.
	}
}

// Abort closes the socket at once, from any state. If it had a peer, one RST
// goes to it on the next poll.
func (s *Socket) Abort() {
	s.setState(StateClosed)
}
