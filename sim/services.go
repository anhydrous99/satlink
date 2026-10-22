package sim

import (
	"bytes"
	"crypto/tls"
	"fmt"

	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// What the simulated internet offers on every address: echo on TCP and UDP
// port 7, discard on TCP port 9, a web page over HTTP on TCP port 80, DNS
// on UDP port 53 and echo inside TLS on TCP port 443.

const (
	echoPort    = 7
	discardPort = 9
	httpPort    = 80
	// secureEchoPort is the HTTPS port, which TLS clients use unless told
	// otherwise.
	secureEchoPort = 443
	dnsPort        = 53
	// idleListenersPerService is one: the stack has no accept backlog, so
	// a listening socket takes exactly one connection, but a fresh listener
	// is put in place before every datagram (see internet.poll), a datagram
	// carries at most one connection request, and repeats of a request
	// never reach a listener (see internet.processOneDatagram).
	idleListenersPerService = 1
	// tcpBufferLen is per direction and connection: several seconds of
	// Iridium traffic.
	tcpBufferLen   = 4096
	udpPacketSlots = 16
	// udpBufferLen is room for several datagrams as large as a PPP link
	// carries.
	udpBufferLen = 16 * 1024
	// chunkLen is the bytes moved per read when echoing or reading a
	// request.
	chunkLen = 1024
)

// services are the sockets behind every address, and what each TCP
// connection is doing.
type services struct {
	sockets     ipstack.SocketSet
	connections []*connection
	udpEcho     ipstack.SocketHandle
	dns         ipstack.SocketHandle
	zone        zone
	webResponse []byte
	// tls is the TLS settings, and with them the session ticket keys,
	// shared by every secure echo connection of this internet, so that a
	// client can resume a session on a later connection as it would with a
	// real server.
	tls *tls.Config
	// discardedBytes counts the bytes the discard service has read since
	// they were last taken.
	discardedBytes uint64
}

// connection is a TCP socket of one of the services: listening, connected
// or closing.
type connection struct {
	handle  ipstack.SocketHandle
	session session
}

// serviceKind is one of the TCP services, each on its own port.
type serviceKind int

const (
	serviceEcho serviceKind = iota
	serviceDiscard
	serviceWeb
	serviceSecureEcho
)

// allServiceKinds lists every TCP service.
var allServiceKinds = []serviceKind{serviceEcho, serviceDiscard, serviceWeb, serviceSecureEcho}

// servicePorts are the TCP services' ports.
var servicePorts = map[serviceKind]uint16{
	serviceEcho:       echoPort,
	serviceDiscard:    discardPort,
	serviceWeb:        httpPort,
	serviceSecureEcho: secureEchoPort,
}

// session is a connection's progress through its service.
type session struct {
	kind       serviceKind
	web        *webExchange // serviceWeb
	secureEcho *secureEcho  // serviceSecureEcho
}

// webState is where a web connection is in its single request and
// response.
type webState int

const (
	webAwaitingRequest webState = iota
	webResponding
	webFinished
)

// webExchange is a web connection's request and response.
type webExchange struct {
	state webState
	head  requestHead // webAwaitingRequest
	sent  int         // webResponding: bytes of the response sent so far
}

// newServices binds the UDP services and starts listening for TCP
// connections.
func newServices(network NetworkConfig) *services {
	s := &services{
		zone:        newZone(network.Hosts),
		webResponse: pageResponse(network.WebPage),
		tls:         newSecureEchoConfig(),
	}
	s.udpEcho = s.sockets.AddUDP(boundUDPSocket(echoPort))
	s.dns = s.sockets.AddUDP(boundUDPSocket(dnsPort))
	s.prepareListeners()
	return s
}

// socketSet returns the sockets, for the interface to poll.
func (s *services) socketSet() *ipstack.SocketSet {
	return &s.sockets
}

// prepareListeners forgets connections that have closed (the stack reuses
// their handles) and makes sure each service has a listener waiting.
func (s *services) prepareListeners() {
	s.removeClosedConnections()
	for _, kind := range allServiceKinds {
		for range idleListenersPerService - s.idleListeners(kind) {
			handle := s.sockets.AddTCP(listeningSocket(servicePorts[kind]))
			s.connections = append(s.connections, &connection{handle: handle, session: s.startSession(kind)})
		}
	}
}

// hasConnectionFor reports whether a connection between the endpoints
// request names exists already, so that request repeats the one that
// opened it.
func (s *services) hasConnectionFor(request connectionRequest) bool {
	for _, c := range s.connections {
		socket := s.sockets.TCP(c.handle)
		if socket.LocalEndpoint() == request.local && socket.RemoteEndpoint() == request.remote {
			return true
		}
	}
	return false
}

// serve lets every service handle what has arrived and queue its answers.
func (s *services) serve() {
	for _, c := range s.connections {
		socket := s.sockets.TCP(c.handle)
		switch c.session.kind {
		case serviceEcho:
			serveEcho(socket)
		case serviceDiscard:
			s.discardedBytes += serveDiscard(socket)
		case serviceWeb:
			serveWeb(socket, c.session.web, s.webResponse)
		case serviceSecureEcho:
			serveSecureEcho(socket, c.session.secureEcho)
		}
	}
	echoDatagrams(s.sockets.UDP(s.udpEcho))
	answerDNSQueries(s.sockets.UDP(s.dns), s.zone)
}

// takeDiscardedBytes returns the bytes the discard service has read since
// the last call.
func (s *services) takeDiscardedBytes() uint64 {
	discarded := s.discardedBytes
	s.discardedBytes = 0
	return discarded
}

// close ends every connection's session: the TLS echo's goroutines.
func (s *services) close() {
	for _, c := range s.connections {
		c.session.close()
	}
}

func (s *services) removeClosedConnections() {
	open := s.connections[:0]
	for _, c := range s.connections {
		if s.sockets.TCP(c.handle).State() != tcp.StateClosed {
			open = append(open, c)
			continue
		}
		s.sockets.Remove(c.handle)
		c.session.close()
	}
	clear(s.connections[len(open):])
	s.connections = open
}

func (s *services) idleListeners(kind serviceKind) int {
	idle := 0
	for _, c := range s.connections {
		if c.session.kind == kind && s.sockets.TCP(c.handle).IsListening() {
			idle++
		}
	}
	return idle
}

// startSession returns the session a new connection to kind starts with.
func (s *services) startSession(kind serviceKind) session {
	switch kind {
	case serviceWeb:
		return session{kind: kind, web: &webExchange{state: webAwaitingRequest}}
	case serviceSecureEcho:
		return session{kind: kind, secureEcho: newSecureEcho(s.tls)}
	default:
		return session{kind: kind}
	}
}

// close ends what the session runs in the background, if anything.
func (s session) close() {
	if s.secureEcho != nil {
		s.secureEcho.close()
	}
}

func listeningSocket(port uint16) *tcp.Socket {
	socket := tcp.NewSocket(tcp.NewSocketBuffer(tcpBufferLen), tcp.NewSocketBuffer(tcpBufferLen))
	if err := socket.Listen(wire.ListenEndpoint{Port: port}); err != nil {
		panic(fmt.Sprintf("a new socket can listen on any non-zero port: %v", err))
	}
	return socket
}

func boundUDPSocket(port uint16) *udp.Socket {
	socket := udp.NewSocket(
		udp.NewPacketBuffer(udpPacketSlots, udpBufferLen),
		udp.NewPacketBuffer(udpPacketSlots, udpBufferLen),
	)
	if err := socket.Bind(wire.ListenEndpoint{Port: port}); err != nil {
		panic(fmt.Sprintf("a new socket can bind to any non-zero port: %v", err))
	}
	return socket
}

// serveEcho sends back whatever arrives, and finishes when the client
// does.
func serveEcho(socket *tcp.Socket) {
	var chunk [chunkLen]byte
	for socket.CanReceive() && socket.CanSend() {
		// Read only what the send buffer can take, so nothing read is lost.
		room := socket.SendCapacity() - socket.SendQueue()
		count, err := socket.ReceiveSlice(chunk[:min(room, len(chunk))])
		if err != nil || count == 0 {
			break
		}
		if _, err := socket.SendSlice(chunk[:count]); err != nil {
			break
		}
	}
	closeOnceClientHasFinished(socket)
}

// serveDiscard reads and drops whatever arrives, finishes when the client
// does, and returns how many bytes it read.
func serveDiscard(socket *tcp.Socket) uint64 {
	discarded := uint64(len(takeReceived(socket)))
	closeOnceClientHasFinished(socket)
	return discarded
}

// serveWeb reads the request head, sends the page, and closes: HTTP/1.0.
func serveWeb(socket *tcp.Socket, exchange *webExchange, response []byte) {
	if exchange.state == webAwaitingRequest {
		if exchange.head.absorb(takeReceived(socket)) {
			exchange.state = webResponding
		} else if clientHasFinished(socket) {
			// The client gave up before finishing its request.
			socket.Close()
			exchange.state = webFinished
		}
	}
	if exchange.state == webResponding {
		exchange.sent += countOrZero(socket.SendSlice(response[exchange.sent:]))
		if exchange.sent == len(response) {
			socket.Close()
			exchange.state = webFinished
		}
	}
	if exchange.state == webFinished {
		// As a real server's closed socket does, data that arrives after
		// the close is answered with a reset.
		if len(takeReceived(socket)) > 0 {
			socket.Abort()
		}
	}
}

// serveSecureEcho echoes inside TLS, and finishes when the client does:
// with close_notify or with FIN.
func serveSecureEcho(socket *tcp.Socket, echo *secureEcho) {
	if socket.IsListening() {
		return // No client yet.
	}
	echo.start(socket.LocalEndpoint(), socket.RemoteEndpoint())
	// Records move both ways until neither direction takes more.
	for {
		if clientHasFinished(socket) {
			echo.hangUp()
		}
		sent := countOrZero(socket.Send(echo.send))
		taken := countOrZero(socket.Receive(echo.receive))
		if sent == 0 && taken == 0 {
			break
		}
	}
	if echo.isFinished() {
		socket.Close()
	}
}

// clientHasFinished reports whether the client has sent its FIN, and
// everything before it has been read.
func clientHasFinished(socket *tcp.Socket) bool {
	return socket.State() == tcp.StateCloseWait && !socket.CanReceive()
}

// closeOnceClientHasFinished closes the server's side too, once the client
// has finished.
func closeOnceClientHasFinished(socket *tcp.Socket) {
	if clientHasFinished(socket) {
		socket.Close()
	}
}

// countOrZero returns how many bytes a socket moved: none when it reports
// an error, as a socket that can no longer send or receive takes nothing.
func countOrZero(count int, err error) int {
	if err != nil {
		return 0
	}
	return count
}

// takeReceived reads everything the socket holds.
func takeReceived(socket *tcp.Socket) []byte {
	var received []byte
	var chunk [chunkLen]byte
	for {
		count, err := socket.ReceiveSlice(chunk[:])
		if err != nil || count == 0 {
			return received
		}
		received = append(received, chunk[:count]...)
	}
}

func echoDatagrams(socket *udp.Socket) {
	for {
		payload, metadata, err := socket.Receive()
		if err != nil {
			return
		}
		// The received metadata names the address the client wrote to, so
		// the echo comes back from that address.
		if err := socket.SendSlice(bytes.Clone(payload), metadata); err != nil {
			logDebug(fmt.Sprintf("simulated internet: UDP echo dropped a datagram: %v", err))
		}
	}
}

func answerDNSQueries(socket *udp.Socket, names zone) {
	for {
		query, metadata, err := socket.Receive()
		if err != nil {
			return
		}
		response, ok := answerDNS(query, names)
		if !ok {
			continue
		}
		if err := socket.SendSlice(response, metadata); err != nil {
			logDebug(fmt.Sprintf("simulated internet: DNS dropped a response: %v", err))
		}
	}
}
