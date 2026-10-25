package udp

import (
	"net/netip"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/socket"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The methods in this file are the interface's side of the socket: it hands
// in received datagrams and asks for datagrams to send. Applications do not
// call them.

// EmitFunc sends one datagram for Dispatch. ip.PayloadLen covers the UDP
// header and payload, and payload shares the socket's transmit buffer, so the
// datagram must be serialised before EmitFunc returns.
type EmitFunc func(ip wire.IPv4Repr, header wire.UDPRepr, payload []byte)

// Accepts reports whether a datagram with header, which arrived in datagram
// ip, is for this socket: its destination port is the bound port and, if
// the socket is bound to an address, it was sent to that address, to a
// broadcast address or to a multicast group. The interface gives a datagram
// to the first UDP socket in the set that accepts it.
func (s *Socket) Accepts(cx *socket.Context, ip wire.IPv4Repr, header wire.UDPRepr) bool {
	if s.endpoint.Port != header.DstPort {
		return false
	}
	boundToAnother := s.endpoint.Addr.IsValid() && s.endpoint.Addr != ip.DstAddr
	return !boundToAnother || cx.IsBroadcast(ip.DstAddr) || ip.DstAddr.IsMulticast()
}

// Process queues a datagram Accepts accepted, with Metadata naming its source
// and the address it was sent to; it drops the datagram when the receive
// buffer is full. payload is copied, not kept.
func (s *Socket) Process(ip wire.IPv4Repr, header wire.UDPRepr, payload []byte) {
	remote := netip.AddrPortFrom(ip.SrcAddr, header.SrcPort)
	netlog.Trace("udp: receiving octets", "local", s.endpoint, "remote", remote, "octets", len(payload))
	meta := Metadata{Endpoint: remote, LocalAddress: ip.DstAddr}
	buffer, err := s.receiveBuffer.Enqueue(len(payload), meta)
	if err != nil {
		netlog.Trace("udp: buffer full, dropped incoming packet", "local", s.endpoint, "remote", remote)
		return
	}
	copy(buffer, payload)
}

// Dispatch dequeues the oldest queued datagram, if any, and emits it from the
// address its Metadata names, the bound address or the interface's choice. A
// datagram with no possible source address is dropped.
func (s *Socket) Dispatch(cx *socket.Context, emit EmitFunc) {
	meta, payload, err := s.transmitBuffer.Dequeue()
	if err != nil {
		// The queue is empty: there is nothing to send.
		return
	}
	s.emitDatagram(cx, meta, payload, emit)
}

// PollAt returns now while datagrams wait to be sent, and on ingress
// otherwise.
func (s *Socket) PollAt() socket.PollAt {
	if s.transmitBuffer.IsEmpty() {
		return socket.PollOnIngress()
	}
	return socket.PollNow()
}

// emitDatagram emits a dequeued datagram with meta and payload; a datagram
// without a source address is dropped instead.
func (s *Socket) emitDatagram(cx *socket.Context, meta Metadata, payload []byte, emit EmitFunc) {
	source, ok := s.sourceAddress(cx, meta)
	if !ok {
		netlog.Trace("udp: cannot find suitable source address, dropping.", "local", s.endpoint, "remote", meta.Endpoint)
		return
	}
	netlog.Trace("udp: sending octets", "local", s.endpoint, "remote", meta.Endpoint, "octets", len(payload))
	header := wire.UDPRepr{SrcPort: s.endpoint.Port, DstPort: meta.Endpoint.Port()}
	ip := wire.IPv4Repr{
		SrcAddr:    source,
		DstAddr:    meta.Endpoint.Addr(),
		NextHeader: wire.IPProtocolUDP,
		PayloadLen: header.HeaderLen() + len(payload),
		HopLimit:   wire.DefaultHopLimit,
	}
	emit(ip, header, payload)
}

// sourceAddress returns the address a queued datagram with meta goes from:
// the one meta names, else the bound address, else the interface's choice
// for its destination; false when there is none.
func (s *Socket) sourceAddress(cx *socket.Context, meta Metadata) (netip.Addr, bool) {
	if meta.LocalAddress.IsValid() {
		return meta.LocalAddress, true
	}
	if s.endpoint.Addr.IsValid() {
		return s.endpoint.Addr, true
	}
	return cx.SourceAddress(meta.Endpoint.Addr())
}
