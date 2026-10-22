package satnet

import (
	"bytes"
	"net/netip"
	"slices"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The stack's TCP connections: what the app wrote waits in an outbox until
// the connection's turn on the line comes, and the device hears what the
// peers acknowledged.

// tcpOverhead is the IPv4 and TCP headers, which a segment's data must
// leave room for.
const tcpOverhead = wire.IPv4HeaderLen + wire.TCPHeaderLen

// outbox is what the app wrote on a connection that the stack has yet to
// take. The stack sends all it holds as soon as it may, and it may whenever
// it asks — its acknowledgements and SYNs must not wait for the line — so
// the data goes to it a segment at a time, as the connection's turns on the
// line come.
type outbox struct {
	data bytes.Buffer
	// finishing reports that our half of the connection ends once all of
	// data is handed over.
	finishing bool
}

// sendBacklog is how much of a connection's data the stack held
// unacknowledged, at the start of a poll.
type sendBacklog struct {
	handle ipstack.SocketHandle
	queued int
}

// openTCP starts connecting to remote; the SYN leaves on the next poll.
func (s *stack) openTCP(remote netip.AddrPort) (ipstack.SocketHandle, error) {
	socket := tcp.NewSocket(tcp.NewSocketBuffer(s.tuning.TCPReceiveBuffer), tcp.NewSocketBuffer(s.tuning.TCPSendBuffer))
	socket.SetTimeout(s.tuning.TCPTimeout)
	socket.SetKeepAlive(s.tuning.TCPKeepalive)
	// Every write should leave at once; pacing already merges small ones.
	socket.SetNagleEnabled(false)
	socket.SetAckDelay(ackDelay)
	localPort, err := s.freePort()
	if err != nil {
		return ipstack.SocketHandle{}, err
	}
	if err := socket.Connect(s.iface.Context(), remote, wire.ListenEndpoint{Port: localPort}); err != nil {
		return ipstack.SocketHandle{}, &Error{Kind: ErrConnectionFailed, Addr: remote}
	}
	handle := s.sockets.AddTCP(socket)
	s.outboxes[handle] = &outbox{}
	return handle, nil
}

// tcp returns the TCP socket handle names.
func (s *stack) tcp(handle ipstack.SocketHandle) *tcp.Socket {
	return s.sockets.TCP(handle)
}

// sendTCP queues what fits of data on the connection and returns how much
// did: what the stack has yet to take and what it holds unacknowledged stay
// within the send buffer together. It fails with tcp.ErrInvalidState once
// the connection takes no more data.
func (s *stack) sendTCP(handle ipstack.SocketHandle, data []byte) (int, error) {
	if !s.tcpMaySend(handle) {
		return 0, tcp.ErrInvalidState
	}
	room := max(s.tuning.TCPSendBuffer-s.tcpUnacknowledged(handle), 0)
	taken := min(len(data), room)
	s.outbox(handle).data.Write(data[:taken])
	return taken, nil
}

// tcpCanSend reports whether the connection takes data now: it may send,
// and has room.
func (s *stack) tcpCanSend(handle ipstack.SocketHandle) bool {
	return s.tcpMaySend(handle) && s.tcpUnacknowledged(handle) < s.tuning.TCPSendBuffer
}

// tcpMaySend reports whether the connection still takes data: open — or
// closed by the peer only — and not finished by the app.
func (s *stack) tcpMaySend(handle ipstack.SocketHandle) bool {
	return s.tcp(handle).MaySend() && !s.outbox(handle).finishing
}

// tcpUnacknowledged returns how much of what the app wrote on the
// connection its peer has yet to acknowledge, whether the stack has taken
// it or not.
func (s *stack) tcpUnacknowledged(handle ipstack.SocketHandle) int {
	return s.outbox(handle).data.Len() + s.tcp(handle).SendQueue()
}

// shutdownTCP finishes sending: our half of the connection ends once
// everything written is on its way, and the peer can still be heard.
func (s *stack) shutdownTCP(handle ipstack.SocketHandle) {
	s.outbox(handle).finishing = true
}

// resetTCP ends the connection at once with a reset, dropping what is still
// queued. The socket stays, so its handle stays valid.
func (s *stack) resetTCP(handle ipstack.SocketHandle) {
	s.dropOutbox(handle)
	s.tcp(handle).Abort()
}

// dropOutbox drops what the app wrote on the connection that the stack has
// yet to take, and records the connection as cut off if that was anything:
// an empty outbox would leave nothing to show that the data never left.
// What the stack took needs no record, as its send buffer keeps it through
// a reset.
func (s *stack) dropOutbox(handle ipstack.SocketHandle) {
	unsent := s.outbox(handle)
	if unsent.data.Len() > 0 {
		s.recordCutOff(handle)
	}
	unsent.data.Reset()
}

// closeTCP finishes sending, then says goodbye; the socket goes once that
// completes.
func (s *stack) closeTCP(handle ipstack.SocketHandle) {
	// Nobody reads this connection any more: no airtime for keepalives, so
	// a peer that never says goodbye is timed out instead.
	s.tcp(handle).SetKeepAlive(0)
	s.shutdownTCP(handle)
	s.closingTCP = append(s.closingTCP, handle)
}

// abortTCP drops the connection at once.
func (s *stack) abortTCP(handle ipstack.SocketHandle) {
	s.resetTCP(handle)
	s.closingTCP = append(s.closingTCP, handle)
}

// wasCutOff reports whether the connection lost data still on its way: to
// an address change, a reset, or the hang-up.
func (s *stack) wasCutOff(handle ipstack.SocketHandle) bool {
	return slices.Contains(s.cutOff, handle)
}

// recordCutOff records that the connection lost data still on its way.
func (s *stack) recordCutOff(handle ipstack.SocketHandle) {
	if !s.wasCutOff(handle) {
		s.cutOff = append(s.cutOff, handle)
	}
}

// outbox returns the outbox of the connection handle names: every TCP
// socket has one, from openTCP until removeTCP.
func (s *stack) outbox(handle ipstack.SocketHandle) *outbox {
	return s.outboxes[handle]
}

// feedConnections hands each connection whose turn on the line has come —
// none of its data waits for the line — a segment's worth of what the app
// wrote, and ends our half of those the app has finished, once it is all
// handed over.
func (s *stack) feedConnections() {
	segmentLen := s.device.mtu - tcpOverhead
	for handle, unsent := range s.outboxes {
		socket := s.tcp(handle)
		if !socket.MaySend() {
			continue
		}
		if unsent.data.Len() > 0 && s.turnHasCome(socket) {
			chunk := unsent.data.Bytes()
			// MaySend holds, so the socket takes what fits.
			if taken, err := socket.SendSlice(chunk[:min(len(chunk), segmentLen)]); err == nil {
				unsent.data.Next(taken)
			}
		}
		if unsent.finishing && unsent.data.Len() == 0 {
			socket.Close()
		}
	}
}

// turnHasCome reports whether the turn on the line of the connection, which
// may send, has come: none of its data waits for the line.
func (s *stack) turnHasCome(socket *tcp.Socket) bool {
	// A connection that may send has a peer.
	connection := tcpFlow(socket.LocalEndpoint().Port(), socket.RemoteEndpoint())
	return !s.device.isWaiting(connection)
}

// cutOffUndelivered remembers the connections with data undelivered: our
// address is about to change, and the stack will reset every connection
// on the old one. What still waits in an outbox counts too: the next poll
// may hand it to the stack just before the reset, which empties the send
// buffer and leaves nothing to show that the data was lost.
func (s *stack) cutOffUndelivered() {
	for handle := range s.sockets.TCPSockets() {
		if s.tcpUnacknowledged(handle) > 0 {
			s.recordCutOff(handle)
		}
	}
}

// sendBacklogs returns each connection's backlog: its data still awaiting
// acknowledgement.
func (s *stack) sendBacklogs() []sendBacklog {
	var backlogs []sendBacklog
	for handle, socket := range s.sockets.TCPSockets() {
		backlogs = append(backlogs, sendBacklog{handle: handle, queued: socket.SendQueue()})
	}
	return backlogs
}

// passOnAcknowledgements tells the device what the peers acknowledged
// during a poll, from before, the backlogs at its start: what the stack
// took off the send buffers, having checked each acknowledgement. The
// device then drops copies of that data still waiting for the line.
func (s *stack) passOnAcknowledgements(before []sendBacklog) {
	for _, backlog := range before {
		socket := s.tcp(backlog.handle)
		acknowledged := max(backlog.queued-socket.SendQueue(), 0)
		local, remote := socket.LocalEndpoint(), socket.RemoteEndpoint()
		// A reset empties the buffer too, but forgets the endpoints.
		if acknowledged > 0 && local.IsValid() && remote.IsValid() {
			s.device.peerAcknowledged(local.Port(), remote, acknowledged)
		}
	}
}

// removeTCP removes a connection whose goodbye is over, with its outbox:
// the set hands its handle out again, so it is no longer cut off either.
func (s *stack) removeTCP(handle ipstack.SocketHandle) {
	s.sockets.Remove(handle)
	delete(s.outboxes, handle)
	s.cutOff = slices.DeleteFunc(s.cutOff, func(lost ipstack.SocketHandle) bool { return lost == handle })
}
