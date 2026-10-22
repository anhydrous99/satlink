package ipstack

import (
	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// anySocket is a socket of any kind the stack has, as the interface drives
// it when sending: smoltcp's Socket enum, whose kinds each implement
// dispatch and poll_at. Ingress needs no such abstraction, as each protocol
// reaches only its own kinds of socket.
type anySocket interface {
	// dispatch lets the socket send at most one packet through emit, and
	// returns emit's error.
	dispatch(cx *socket.Context, emit emitPacket) error
	// pollAt returns when the socket next needs dispatch.
	pollAt(cx *socket.Context) socket.PollAt
}

// emitPacket sends one packet a socket hands over in dispatch; it fails,
// leaving the packet unsent, when the device has no room for it.
type emitPacket func(packet ipPacket) error

// The kinds of socket a SocketSet holds.
type (
	tcpSocket struct{ *tcp.Socket }
	udpSocket struct{ *udp.Socket }
	dnsSocket struct{ *dns.Socket }
)

func (s tcpSocket) dispatch(cx *socket.Context, emit emitPacket) error {
	return s.Dispatch(cx, func(header wire.IPv4Repr, segment wire.TCPRepr) error {
		return emit(newTCPPacket(header, segment))
	})
}

func (s tcpSocket) pollAt(cx *socket.Context) socket.PollAt {
	return s.PollAt(cx)
}

func (s udpSocket) dispatch(cx *socket.Context, emit emitPacket) error {
	return s.Dispatch(cx, emitDatagram(emit))
}

func (s udpSocket) pollAt(*socket.Context) socket.PollAt {
	return s.PollAt()
}

func (s dnsSocket) dispatch(cx *socket.Context, emit emitPacket) error {
	return s.Dispatch(cx, emitDatagram(emit))
}

func (s dnsSocket) pollAt(*socket.Context) socket.PollAt {
	return s.PollAt()
}

// emitDatagram adapts emit to the EmitFunc of the UDP and DNS sockets, which
// both hand over UDP datagrams.
func emitDatagram(emit emitPacket) func(header wire.IPv4Repr, udpHeader wire.UDPRepr, payload []byte) error {
	return func(header wire.IPv4Repr, udpHeader wire.UDPRepr, payload []byte) error {
		return emit(newUDPPacket(header, udpHeader, payload))
	}
}
