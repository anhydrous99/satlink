package udp

import (
	"net/netip"

	"github.com/anhydrous99/satlink/internal/ipstack/storage"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// Metadata travels with each datagram in a socket's buffers.
type Metadata struct {
	// Endpoint is where a received datagram came from, or where a queued one
	// goes: an IPv4 address (Is4) and a port.
	Endpoint netip.AddrPort
	// LocalAddress is the address a received datagram was sent to, which is
	// always set. On a queued datagram it is the address to send from, which
	// must be IPv4 when set; when it is the zero Addr, the socket's bound
	// address is used, or else the interface chooses one
	// (socket.Context.SourceAddress). Sending a received datagram's Metadata
	// back therefore answers from the address the peer wrote to.
	LocalAddress netip.Addr
}

// String formats the metadata as its endpoint.
func (m Metadata) String() string {
	return m.Endpoint.String()
}

// PacketBuffer is a socket's receive or transmit buffer.
type PacketBuffer = storage.PacketBuffer[Metadata]

// NewPacketBuffer returns an empty buffer for up to packetCapacity datagrams
// of payloadCapacity octets in total.
func NewPacketBuffer(packetCapacity, payloadCapacity int) *PacketBuffer {
	return storage.NewPacketBuffer[Metadata](packetCapacity, payloadCapacity)
}

// Socket is a UDP socket. Its zero value is not usable; create one with
// NewSocket. A Socket is not safe for concurrent use.
type Socket struct {
	// endpoint is the endpoint the socket is bound to; its port is 0 while
	// the socket is unbound.
	endpoint       wire.ListenEndpoint
	receiveBuffer  *PacketBuffer
	transmitBuffer *PacketBuffer
}

// NewSocket returns an unbound socket that receives into receiveBuffer and
// sends from transmitBuffer.
func NewSocket(receiveBuffer, transmitBuffer *PacketBuffer) *Socket {
	return &Socket{receiveBuffer: receiveBuffer, transmitBuffer: transmitBuffer}
}

// Endpoint returns the endpoint the socket is bound to; its port is 0 while
// the socket is unbound.
func (s *Socket) Endpoint() wire.ListenEndpoint {
	return s.endpoint
}

// Bind binds the socket to local; an open Addr accepts datagrams for every
// address of the interface. It returns ErrUnaddressable for port 0 or an
// Addr that is neither open nor IPv4 (Is4), and ErrInvalidState when the
// socket is already bound.
func (s *Socket) Bind(local wire.ListenEndpoint) error {
	if local.Port == 0 || !isOpenOrIPv4(local.Addr) {
		return ErrUnaddressable
	}
	if s.IsOpen() {
		return ErrInvalidState
	}
	s.endpoint = local
	return nil
}

// Close unbinds the socket and drops every datagram it holds, in both
// directions.
func (s *Socket) Close() {
	s.endpoint = wire.ListenEndpoint{}
	s.transmitBuffer.Clear()
	s.receiveBuffer.Clear()
}

// IsOpen reports whether the socket is bound.
func (s *Socket) IsOpen() bool {
	return s.endpoint.Port != 0
}

// CanSend reports whether the transmit buffer has a free datagram slot;
// there may still be no room for a particular payload.
func (s *Socket) CanSend() bool {
	return !s.transmitBuffer.IsFull()
}

// CanReceive reports whether a received datagram is waiting.
func (s *Socket) CanReceive() bool {
	return !s.receiveBuffer.IsEmpty()
}

// PacketReceiveCapacity returns how many datagrams the receive buffer holds.
func (s *Socket) PacketReceiveCapacity() int {
	return s.receiveBuffer.PacketCapacity()
}

// PacketSendCapacity returns how many datagrams the transmit buffer holds.
func (s *Socket) PacketSendCapacity() int {
	return s.transmitBuffer.PacketCapacity()
}

// PayloadReceiveCapacity returns how many payload octets the receive buffer
// holds.
func (s *Socket) PayloadReceiveCapacity() int {
	return s.receiveBuffer.PayloadCapacity()
}

// PayloadSendCapacity returns how many payload octets the transmit buffer
// holds.
func (s *Socket) PayloadSendCapacity() int {
	return s.transmitBuffer.PayloadCapacity()
}

// isOpenOrIPv4 reports whether addr may be a socket's local address: open
// (the zero Addr, every address of the interface) or IPv4. The stack carries
// nothing else.
func isOpenOrIPv4(addr netip.Addr) bool {
	return !addr.IsValid() || addr.Is4()
}
