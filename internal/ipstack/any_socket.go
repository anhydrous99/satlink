package ipstack

import (
	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// anySocket is a socket of any kind the stack has, as the interface drives
// it when sending: each kind implements dispatch and poll_at. Ingress needs no such abstraction, as each protocol
// reaches only its own kinds of socket.
type anySocket interface {
	// dispatch lets the socket send at most one packet through emit.
	dispatch(cx *socket.Context, emit emitPacket)
	// pollAt returns when the socket next needs dispatch.
	pollAt(cx *socket.Context) socket.PollAt
}

// emitPacket sends one packet a socket hands over in dispatch.
type emitPacket func(packet ipPacket)

// The kinds of socket a SocketSet holds.
type (
	tcpSocket struct{ *tcp.Socket }
	udpSocket struct{ *udp.Socket }
	dnsSocket struct{ *dns.Socket }
)

func (s tcpSocket) dispatch(cx *socket.Context, emit emitPacket) {
	s.Dispatch(cx, func(header wire.IPv4Repr, segment wire.TCPRepr) {
		emit(newTCPPacket(header, segment))
	})
}

func (s tcpSocket) pollAt(cx *socket.Context) socket.PollAt {
	return s.PollAt(cx)
}

func (s udpSocket) dispatch(cx *socket.Context, emit emitPacket) {
	s.Dispatch(cx, emitDatagram(emit))
}

func (s udpSocket) pollAt(*socket.Context) socket.PollAt {
	return s.PollAt()
}

func (s dnsSocket) dispatch(cx *socket.Context, emit emitPacket) {
	s.Dispatch(cx, emitDatagram(emit))
}

func (s dnsSocket) pollAt(*socket.Context) socket.PollAt {
	return s.PollAt()
}

// emitDatagram adapts emit to the EmitFunc of the UDP and DNS sockets, which
// both hand over UDP datagrams.
func emitDatagram(emit emitPacket) func(header wire.IPv4Repr, udpHeader wire.UDPRepr, payload []byte) {
	return func(header wire.IPv4Repr, udpHeader wire.UDPRepr, payload []byte) {
		emit(newUDPPacket(header, udpHeader, payload))
	}
}
