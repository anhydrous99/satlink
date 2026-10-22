package ipstack

// The tests here check the set's own behaviour. The set never calls its
// sockets, so fresh ones do.

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
)

func newUnboundUDPSocket() *udp.Socket {
	return udp.NewSocket(udp.NewPacketBuffer(1, 1), udp.NewPacketBuffer(1, 1))
}

func TestZeroSocketSetIsEmptyAndReady(t *testing.T) {
	t.Parallel()
	var sockets SocketSet

	for handle := range sockets.TCPSockets() {
		t.Errorf("the zero set has TCP socket %v", handle)
	}
	for handle := range sockets.UDPSockets() {
		t.Errorf("the zero set has UDP socket %v", handle)
	}
	for handle := range sockets.DNSSockets() {
		t.Errorf("the zero set has DNS socket %v", handle)
	}
	if handle := sockets.AddUDP(newUnboundUDPSocket()); handle.String() != "#0" {
		t.Errorf("the first socket of the zero set got %v, want #0", handle)
	}
}

func TestTypedAccessorsReturnTheSocketsAdded(t *testing.T) {
	t.Parallel()
	var sockets SocketSet
	stream, datagrams, resolver := newTCPSocket(), newUnboundUDPSocket(), dns.NewSocket(nil)

	streamHandle := sockets.AddTCP(stream)
	datagramsHandle := sockets.AddUDP(datagrams)
	resolverHandle := sockets.AddDNS(resolver)

	if sockets.TCP(streamHandle) != stream || sockets.UDP(datagramsHandle) != datagrams || sockets.DNS(resolverHandle) != resolver {
		t.Error("an accessor returned another socket than the one added")
	}
}

func TestAddUsesTheLowestFreeSlot(t *testing.T) {
	t.Parallel()
	var sockets SocketSet
	handles := []SocketHandle{
		sockets.AddTCP(newTCPSocket()),
		sockets.AddUDP(newUnboundUDPSocket()),
		sockets.AddDNS(dns.NewSocket(nil)),
	}

	sockets.Remove(handles[2])
	sockets.Remove(handles[0])
	reused := sockets.AddDNS(dns.NewSocket(nil))
	reusedNext := sockets.AddTCP(newTCPSocket())
	added := sockets.AddUDP(newUnboundUDPSocket())

	if reused != handles[0] || reusedNext != handles[2] {
		t.Errorf("the next sockets got %v and %v, want the freed %v and %v, lowest first", reused, reusedNext, handles[0], handles[2])
	}
	if added.String() != "#3" {
		t.Errorf("with every slot taken, the next socket got %v, want #3", added)
	}
}

func TestSocketHandleStringIsTheIndex(t *testing.T) {
	t.Parallel()
	var sockets SocketSet
	first := sockets.AddTCP(newTCPSocket())
	second := sockets.AddTCP(newTCPSocket())

	for _, test := range []struct {
		handle SocketHandle
		want   string
	}{{first, "#0"}, {second, "#1"}, {SocketHandle{}, "#invalid"}} {
		if got := test.handle.String(); got != test.want {
			t.Errorf("String = %q, want %q", got, test.want)
		}
	}
}

func TestRemoveFreesTheSlot(t *testing.T) {
	t.Parallel()
	var sockets SocketSet
	removed := sockets.AddTCP(newTCPSocket())
	kept := sockets.AddTCP(newTCPSocket())

	sockets.Remove(removed)

	var remaining []SocketHandle
	for handle := range sockets.TCPSockets() {
		remaining = append(remaining, handle)
	}
	if !slices.Equal(remaining, []SocketHandle{kept}) {
		t.Errorf("after Remove the set holds %v, want %v", remaining, []SocketHandle{kept})
	}
	assertPanicsWith(t, "TCP of a removed handle", "handle does not refer to a valid socket", func() { sockets.TCP(removed) })
}

func TestRemoveLeavesTheSocketAsItIs(t *testing.T) {
	t.Parallel()
	var sockets SocketSet
	listener := newListener(t, 80)
	handle := sockets.AddTCP(listener)

	sockets.Remove(handle)

	if !listener.IsListening() {
		t.Errorf("a removed listener is %v, want it still listening", listener.State())
	}
}

func TestAccessorsPanicOnAnInvalidHandle(t *testing.T) {
	t.Parallel()
	var sockets SocketSet
	sockets.AddTCP(newTCPSocket())
	const invalid = "handle does not refer to a valid socket"
	for _, handle := range []SocketHandle{{}, {slot: 2}} {
		assertPanicsWith(t, "TCP of "+handle.String(), invalid, func() { sockets.TCP(handle) })
		assertPanicsWith(t, "UDP of "+handle.String(), invalid, func() { sockets.UDP(handle) })
		assertPanicsWith(t, "DNS of "+handle.String(), invalid, func() { sockets.DNS(handle) })
		assertPanicsWith(t, "Remove of "+handle.String(), invalid, func() { sockets.Remove(handle) })
	}
}

func TestAccessorsPanicOnASocketOfAnotherKind(t *testing.T) {
	t.Parallel()
	var sockets SocketSet
	tcpHandle := sockets.AddTCP(newTCPSocket())
	udpHandle := sockets.AddUDP(newUnboundUDPSocket())
	const wrongKind = "handle refers to a socket of a wrong type"

	assertPanicsWith(t, "UDP of a TCP socket", wrongKind, func() { sockets.UDP(tcpHandle) })
	assertPanicsWith(t, "DNS of a TCP socket", wrongKind, func() { sockets.DNS(tcpHandle) })
	assertPanicsWith(t, "TCP of a UDP socket", wrongKind, func() { sockets.TCP(udpHandle) })
}

func TestIteratorsGoInSlotOrder(t *testing.T) {
	t.Parallel()
	var sockets SocketSet
	first, second := newTCPSocket(), newTCPSocket()
	resolver := dns.NewSocket([]netip.Addr{remoteAddr})
	firstHandle := sockets.AddTCP(first)
	datagramHandle := sockets.AddUDP(newUnboundUDPSocket())
	sockets.AddTCP(second)
	dnsHandle := sockets.AddDNS(resolver)
	// A socket added later into a freed slot comes before those after it.
	sockets.Remove(firstHandle)
	third := newTCPSocket()
	thirdHandle := sockets.AddTCP(third)

	var tcpSockets []*tcp.Socket
	var tcpHandles []SocketHandle
	for handle, s := range sockets.TCPSockets() {
		tcpHandles, tcpSockets = append(tcpHandles, handle), append(tcpSockets, s)
	}

	if !slices.Equal(tcpSockets, []*tcp.Socket{third, second}) || tcpHandles[0] != thirdHandle {
		t.Errorf("TCPSockets yields %v, want the sockets in slots #0 and #2", tcpHandles)
	}
	for handle, s := range sockets.UDPSockets() {
		if handle != datagramHandle || s != sockets.UDP(datagramHandle) {
			t.Errorf("UDPSockets yields %v, want only %v", handle, datagramHandle)
		}
	}
	for handle, s := range sockets.DNSSockets() {
		if handle != dnsHandle || s != resolver {
			t.Errorf("DNSSockets yields %v, want only %v", handle, dnsHandle)
		}
	}
}

func TestIteratorsStopWhenTheLoopDoes(t *testing.T) {
	t.Parallel()
	var sockets SocketSet
	first := sockets.AddUDP(newUnboundUDPSocket())
	sockets.AddUDP(newUnboundUDPSocket())

	var visited []SocketHandle
	for handle := range sockets.UDPSockets() {
		visited = append(visited, handle)
		break
	}

	if !slices.Equal(visited, []SocketHandle{first}) {
		t.Errorf("visited %v, want only %v", visited, first)
	}
}

// assertPanicsWith checks that operation panics with message.
func assertPanicsWith(t *testing.T, what, message string, operation func()) {
	t.Helper()
	defer func() {
		t.Helper()
		if recovered := recover(); recovered != message {
			t.Errorf("%s panicked with %v, want %q", what, recovered, message)
		}
	}()
	operation()
}
