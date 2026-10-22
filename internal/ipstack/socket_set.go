package ipstack

import (
	"iter"
	"slices"
	"strconv"

	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
)

// SocketHandle identifies a socket in a SocketSet. Handles come from the
// set's Add methods; the zero SocketHandle identifies no socket.
type SocketHandle struct {
	// slot is the socket's index in the set plus one, so that the zero
	// handle is invalid.
	slot int
}

// String formats the handle as smoltcp does: "#" and the socket's index in
// its set ("#0" for the first); "#invalid" for the zero handle.
func (h SocketHandle) String() string {
	if h.slot == 0 {
		return "#invalid"
	}
	return "#" + strconv.Itoa(h.slot-1)
}

// handleAt returns the handle of the socket at index in its set.
func handleAt(index int) SocketHandle {
	return SocketHandle{slot: index + 1}
}

// SocketSet holds the sockets an Interface serves, each in a numbered slot.
//
// Adding a socket puts it in the lowest free slot, so the handle of a removed
// socket is soon given to another: keep a socket in the set until nothing
// refers to its handle any more. The interface offers a received segment or
// datagram to the sockets in slot order, and the first one that accepts it
// gets it.
//
// The zero SocketSet is empty and ready to use. A SocketSet is not safe for
// concurrent use, and must not change while one of its iterators runs.
type SocketSet struct {
	// slots holds the sockets by index, a free slot being nil. It grows as
	// needed, as smoltcp's set does with growable storage.
	slots []anySocket
}

// AddTCP adds a TCP socket and returns its handle.
func (s *SocketSet) AddTCP(socket *tcp.Socket) SocketHandle {
	return s.add(tcpSocket{socket})
}

// AddUDP adds a UDP socket and returns its handle.
func (s *SocketSet) AddUDP(socket *udp.Socket) SocketHandle {
	return s.add(udpSocket{socket})
}

// AddDNS adds a DNS socket and returns its handle.
func (s *SocketSet) AddDNS(socket *dns.Socket) SocketHandle {
	return s.add(dnsSocket{socket})
}

// TCP returns the TCP socket handle identifies. It panics if handle
// identifies no socket of the set or one of another kind.
func (s *SocketSet) TCP(handle SocketHandle) *tcp.Socket {
	return socketOfKind[tcpSocket](s, handle).Socket
}

// UDP returns the UDP socket handle identifies. It panics if handle
// identifies no socket of the set or one of another kind.
func (s *SocketSet) UDP(handle SocketHandle) *udp.Socket {
	return socketOfKind[udpSocket](s, handle).Socket
}

// DNS returns the DNS socket handle identifies. It panics if handle
// identifies no socket of the set or one of another kind.
func (s *SocketSet) DNS(handle SocketHandle) *dns.Socket {
	return socketOfKind[dnsSocket](s, handle).Socket
}

// Remove takes the socket handle identifies out of the set, as it is: a TCP
// connection is not closed, nothing more is sent for it, and its slot becomes
// free. It panics if handle identifies no socket of the set.
func (s *SocketSet) Remove(handle SocketHandle) {
	netlog.Trace("removing", "socket", handle)
	s.slots[s.index(handle)] = nil
}

// TCPSockets returns the set's TCP sockets with their handles, in slot order.
func (s *SocketSet) TCPSockets() iter.Seq2[SocketHandle, *tcp.Socket] {
	return socketsOfKind(s, func(item tcpSocket) *tcp.Socket { return item.Socket })
}

// UDPSockets returns the set's UDP sockets with their handles, in slot order.
func (s *SocketSet) UDPSockets() iter.Seq2[SocketHandle, *udp.Socket] {
	return socketsOfKind(s, func(item udpSocket) *udp.Socket { return item.Socket })
}

// DNSSockets returns the set's DNS sockets with their handles, in slot order.
func (s *SocketSet) DNSSockets() iter.Seq2[SocketHandle, *dns.Socket] {
	return socketsOfKind(s, func(item dnsSocket) *dns.Socket { return item.Socket })
}

// add puts a socket in the lowest free slot, adding a slot when none is
// free, and returns its handle.
func (s *SocketSet) add(socket anySocket) SocketHandle {
	index := slices.Index(s.slots, nil)
	if index < 0 {
		s.slots = append(s.slots, nil)
		index = len(s.slots) - 1
	}
	s.slots[index] = socket
	handle := handleAt(index)
	netlog.Trace("adding", "socket", handle)
	return handle
}

// item returns the socket handle identifies. It panics, as smoltcp does, if
// handle identifies no socket of the set.
func (s *SocketSet) item(handle SocketHandle) anySocket {
	return s.slots[s.index(handle)]
}

// index returns the index of the slot of the socket handle identifies. It
// panics, as smoltcp does, if handle identifies no socket of the set.
func (s *SocketSet) index(handle SocketHandle) int {
	index := handle.slot - 1
	if index < 0 || index >= len(s.slots) || s.slots[index] == nil {
		panic("handle does not refer to a valid socket")
	}
	return index
}

// items returns every socket of the set with its handle, in slot order.
func (s *SocketSet) items() iter.Seq2[SocketHandle, anySocket] {
	return func(yield func(SocketHandle, anySocket) bool) {
		for index, item := range s.slots {
			if item != nil && !yield(handleAt(index), item) {
				return
			}
		}
	}
}

// socketOfKind returns the socket handle identifies, which must be of kind
// K. It panics, as smoltcp does, if handle identifies no socket of the set or
// one of another kind.
func socketOfKind[K anySocket](s *SocketSet, handle SocketHandle) K {
	socket, ok := s.item(handle).(K)
	if !ok {
		panic("handle refers to a socket of a wrong type")
	}
	return socket
}

// socketsOfKind returns the set's sockets of kind K, as unwrap gives them,
// with their handles, in slot order.
func socketsOfKind[K anySocket, S any](s *SocketSet, unwrap func(K) S) iter.Seq2[SocketHandle, S] {
	return func(yield func(SocketHandle, S) bool) {
		for handle, item := range s.items() {
			socket, ok := item.(K)
			if ok && !yield(handle, unwrap(socket)) {
				return
			}
		}
	}
}
