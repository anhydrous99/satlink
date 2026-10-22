package sim_test

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/ipstack"
	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
	"github.com/anhydrous99/satlink/ppp"
	"github.com/anhydrous99/satlink/sim"
)

// The simulated internet's services, reached with a real TCP/IP stack on
// the app's side of the PPP link.

const (
	firstLocalPort = 49_152
	getRequest     = "GET / HTTP/1.0\r\nHost: web.test\r\n\r\n"
	discardPort    = 9
	clientUDPPort  = 40_000
	// clientUDPSlots and clientUDPBufferLen are the client's UDP buffers.
	clientUDPSlots     = 4
	clientUDPBufferLen = 2048
	// drainChunk is how much the tests read from a connection at a time.
	drainChunk = 512
)

// unlistedHost is an address no name resolves to, which serves all the
// same.
var unlistedHost = netip.MustParseAddr("198.51.100.1")

// slowLinkConfig returns Iridium's latency without its throughput cap, so
// that a test takes seconds rather than minutes. A round trip outlasts a
// TCP client's first retransmission timeout of one second (RFC 6298), so
// every connection request reaches the gateway twice.
func slowLinkConfig() sim.Config {
	config := fastConfig()
	config.Link = sim.LinkProfile{OneWayDelay: sim.IridiumLinkProfile().OneWayDelay}
	return config
}

// establish waits until every connection is established.
func establish(network *clientNetwork, handles []ipstack.SocketHandle) {
	network.t.Helper()
	established := network.runUntil(func(sockets *ipstack.SocketSet) bool {
		return allHandles(handles, func(handle ipstack.SocketHandle) bool { return sockets.TCP(handle).MaySend() })
	})
	if !established {
		network.t.Fatal("a connection was never established")
	}
}

// connectToEcho opens a connection to the echo service from each of count
// local ports, the first of them firstPort.
func connectToEcho(network *clientNetwork, firstPort uint16, count int) []ipstack.SocketHandle {
	handles := make([]ipstack.SocketHandle, 0, count)
	for port := range uint16(count) {
		handles = append(handles, network.connectTCP(echoHost, echoPort, firstPort+port))
	}
	return handles
}

// assertEchoed sends message on every connection and checks that each
// echoes it back, rather than being reset.
func assertEchoed(network *clientNetwork, handles []ipstack.SocketHandle, message []byte) {
	network.t.Helper()
	for _, handle := range handles {
		send(network, handle, message)
	}
	echoed := make([][]byte, len(handles))
	// Whether this finishes or times out, the comparison below tells what
	// came back on which connection.
	network.runUntil(func(sockets *ipstack.SocketSet) bool {
		allAnswered := true
		for i, handle := range handles {
			socket := sockets.TCP(handle)
			echoed[i] = drainInto(socket, echoed[i])
			allAnswered = allAnswered && (len(echoed[i]) >= len(message) || !socket.IsOpen())
		}
		return allAnswered
	})
	for i, handle := range handles {
		if !bytes.Equal(echoed[i], message) {
			network.t.Errorf("connection %v echoed %q; want %q", handle, echoed[i], message)
		}
	}
}

// closeConnections closes the connections and waits until the echo service
// has closed its side of each too.
func closeConnections(network *clientNetwork, handles []ipstack.SocketHandle) {
	network.t.Helper()
	for _, handle := range handles {
		network.sockets.TCP(handle).Close()
	}
	allClosed := network.runUntil(func(sockets *ipstack.SocketSet) bool {
		return allHandles(handles, func(handle ipstack.SocketHandle) bool { return isOver(sockets.TCP(handle)) })
	})
	if !allClosed {
		network.t.Fatal("the service did not close when the client did")
	}
}

// isOver reports whether both sides have closed the connection.
func isOver(socket *tcp.Socket) bool {
	return socket.State() == tcp.StateTimeWait || socket.State() == tcp.StateClosed
}

func allHandles(handles []ipstack.SocketHandle, holds func(ipstack.SocketHandle) bool) bool {
	for _, handle := range handles {
		if !holds(handle) {
			return false
		}
	}
	return true
}

func send(network *clientNetwork, handle ipstack.SocketHandle, data []byte) {
	network.t.Helper()
	sent, err := network.sockets.TCP(handle).SendSlice(data)
	if err != nil || sent != len(data) {
		network.t.Fatalf("sent %d of %d bytes: %v", sent, len(data), err)
	}
}

// drainInto appends what the socket has received to received.
func drainInto(socket *tcp.Socket, received []byte) []byte {
	chunk := make([]byte, drainChunk)
	for {
		count, err := socket.ReceiveSlice(chunk)
		if err != nil || count == 0 {
			return received
		}
		received = append(received, chunk[:count]...)
	}
}

// readToEnd returns everything the server sends until it closes the
// connection.
func readToEnd(network *clientNetwork, handle ipstack.SocketHandle) []byte {
	network.t.Helper()
	var received []byte
	finished := network.runUntil(func(sockets *ipstack.SocketSet) bool {
		socket := sockets.TCP(handle)
		received = drainInto(socket, received)
		// The server's FIN arrived and everything before it has been read.
		return !socket.MayReceive() && socket.State() != tcp.StateSynSent
	})
	if !finished {
		network.t.Fatalf("the server never finished; got %q", received)
	}
	return received
}

func webResponse(page string) []byte {
	return fmt.Appendf(nil, "HTTP/1.0 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s", len(page), page)
}

// lookUp returns the addresses the stack's own resolver finds for name
// through the gateway.
func lookUp(network *clientNetwork, name string) []netip.Addr {
	network.t.Helper()
	resolver, query := network.startLookup(gatewayAddress, name)
	var addresses []netip.Addr
	resolved := network.runUntil(func(sockets *ipstack.SocketSet) bool {
		found, err := sockets.DNS(resolver).TakeQueryResult(query)
		switch {
		case errors.Is(err, dns.ErrPending):
			return false
		case err != nil:
			network.t.Fatalf("the lookup of %s failed: %v", name, err)
		}
		addresses = found
		return true
	})
	if !resolved {
		network.t.Fatalf("the lookup of %s never finished", name)
	}
	return addresses
}

func TestDiscardCountsWhatItReceivesAndClosesWithTheClient(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, fastConfig())
	discard := network.connectTCP(echoHost, discardPort, firstLocalPort)
	establish(network, []ipstack.SocketHandle{discard})

	message := bytes.Repeat([]byte("into the void"), 100)
	send(network, discard, message)
	counted := network.runUntil(func(*ipstack.SocketSet) bool {
		return network.control.Stats().DiscardedBytes == uint64(len(message))
	})

	if !counted {
		t.Errorf("counted %d bytes; want %d", network.control.Stats().DiscardedBytes, len(message))
	}
	closeConnections(network, []ipstack.SocketHandle{discard})
}

func TestTCPEchoSendsBackWhatItReceives(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, fastConfig())
	echo := connectToEcho(network, firstLocalPort, 1)
	establish(network, echo)

	assertEchoed(network, echo, []byte("over the horizon and back"))
	closeConnections(network, echo)
}

func TestEveryAddressServesTheWebPage(t *testing.T) {
	t.Parallel()
	// Larger than a TCP send buffer, so the page goes out in several
	// writes.
	page := strings.Repeat("A page from beyond the sky.\n", 300)
	config := fastConfig()
	config.Network.WebPage = page
	network := connectNetwork(t, config)

	for i, address := range []netip.Addr{webHost, unlistedHost} {
		handle := network.connectTCP(address, httpPort, firstLocalPort+uint16(i))
		establish(network, []ipstack.SocketHandle{handle})
		send(network, handle, []byte(getRequest))

		if got := readToEnd(network, handle); !bytes.Equal(got, webResponse(page)) {
			t.Errorf("%v served %d bytes; want the %d of the page's response", address, len(got), len(webResponse(page)))
		}
	}
}

func TestSeveralConnectionsAreServedAtOnce(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, fastConfig())
	var handles []ipstack.SocketHandle
	for port := range uint16(6) {
		handles = append(handles, network.connectTCP(webHost, httpPort, firstLocalPort+port))
	}
	establish(network, handles)
	for _, handle := range handles {
		send(network, handle, []byte(getRequest))
	}

	expected := webResponse(sim.DefaultNetworkConfig().WebPage)
	for _, handle := range handles {
		if got := readToEnd(network, handle); !bytes.Equal(got, expected) {
			t.Errorf("connection %v got %q; want %q", handle, got, expected)
		}
	}
}

// Go only, for a path the Rust tests leave out: the web server lets a
// client go that hangs up before its request has ended, without an answer.
func TestAWebClientThatGivesUpBeforeItsRequestEndsIsLetGoUnanswered(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, fastConfig())
	handle := network.connectTCP(webHost, httpPort, firstLocalPort)
	establish(network, []ipstack.SocketHandle{handle})
	send(network, handle, []byte("GET / HTTP/1.0\r\n"))

	closeConnections(network, []ipstack.SocketHandle{handle})

	if answer := drainInto(network.sockets.TCP(handle), nil); len(answer) != 0 {
		t.Errorf("the server answered %q", answer)
	}
}

// Go only: the Rust tests check this through the CLI's net tcp, which
// fails an upload whose end the web server reset. The client never closes
// its side, so only a reset can close its connection.
func TestDataSentAfterTheWebServerClosedIsAnsweredWithAReset(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, fastConfig())
	handle := network.connectTCP(webHost, httpPort, firstLocalPort)
	establish(network, []ipstack.SocketHandle{handle})
	send(network, handle, []byte(getRequest))
	readToEnd(network, handle)

	send(network, handle, []byte("more after the response"))
	reset := network.runUntil(func(sockets *ipstack.SocketSet) bool {
		return sockets.TCP(handle).State() == tcp.StateClosed
	})

	if !reset {
		t.Errorf("the connection is %v; want it reset", network.sockets.TCP(handle).State())
	}
}

// On a slow link, a repeated connection request reaches the gateway while
// its connection is still being set up. It must not open a second
// connection, which would reset the first. Each connection here also
// follows a finished one, as it was from the second connection on that
// the repeats used to do harm.
func TestConnectionsOneAfterAnotherSurviveRepeatedConnectionRequests(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, slowLinkConfig())
	echo := connectToEcho(network, firstLocalPort, 1)
	establish(network, echo)
	assertEchoed(network, echo, []byte("first"))
	closeConnections(network, echo)

	web := network.connectTCP(webHost, httpPort, firstLocalPort+1)
	establish(network, []ipstack.SocketHandle{web})
	send(network, web, []byte(getRequest))

	if got, want := readToEnd(network, web), webResponse(sim.DefaultNetworkConfig().WebPage); !bytes.Equal(got, want) {
		t.Errorf("the web page came back as %q; want %q", got, want)
	}
}

func TestSeveralConnectionsAtOnceSurviveRepeatedConnectionRequests(t *testing.T) {
	t.Parallel()
	const connectionsPerRound = 3
	network := connectNetwork(t, slowLinkConfig())

	// The second round starts after the first round's connections are
	// over.
	for round := range uint16(2) {
		echo := connectToEcho(network, firstLocalPort+round*connectionsPerRound, connectionsPerRound)
		establish(network, echo)
		assertEchoed(network, echo, []byte("side by side"))
		closeConnections(network, echo)
	}
}

func TestUDPEchoAnswersFromTheAddressItWasSentTo(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, fastConfig())
	socket := udp.NewSocket(
		udp.NewPacketBuffer(clientUDPSlots, clientUDPBufferLen),
		udp.NewPacketBuffer(clientUDPSlots, clientUDPBufferLen),
	)
	if err := socket.Bind(wire.ListenEndpoint{Port: clientUDPPort}); err != nil {
		t.Fatal(err)
	}
	handle := network.sockets.AddUDP(socket)
	echo := netip.AddrPortFrom(echoHost, echoPort)
	if err := network.sockets.UDP(handle).SendSlice([]byte("ping"), udp.Metadata{Endpoint: echo}); err != nil {
		t.Fatal(err)
	}

	var reply []byte
	var from netip.AddrPort
	answered := network.runUntil(func(sockets *ipstack.SocketSet) bool {
		payload, metadata, err := sockets.UDP(handle).Receive()
		if err != nil {
			return false
		}
		reply, from = bytes.Clone(payload), metadata.Endpoint
		return true
	})

	if !answered || string(reply) != "ping" || from != echo {
		t.Errorf("answered %v with %q from %v; want ping from %v", answered, reply, from, echo)
	}
}

func TestAResolverFindsHostsThroughTheGateway(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, fastConfig())

	if addresses := lookUp(network, "web.test"); !slices.Equal(addresses, []netip.Addr{webHost}) {
		t.Errorf("web.test resolved to %v; want [%v]", addresses, webHost)
	}
}

// Echo inside TLS on port 443, reached by a TLS client that trusts nothing
// but the test CA.

// handshake connects to port 443 of address and completes a handshake for
// secure.test, which succeeds only if the server proves to be it.
func handshake(network *clientNetwork, address netip.Addr, localPort uint16) *tlsSession {
	network.t.Helper()
	handle := network.connectTCP(address, secureEchoPort, localPort)
	establish(network, []ipstack.SocketHandle{handle})
	session := network.startTLS(handle)
	finished := network.runUntil(func(*ipstack.SocketSet) bool { return session.client.HandshakeFinished() })
	if err := session.client.AwaitHandshake(0); !finished || err != nil {
		network.t.Fatalf("the handshake with %v never finished: %v", address, err)
	}
	return session
}

// echoInsideTLS sends message inside TLS and returns what comes back.
func echoInsideTLS(network *clientNetwork, session *tlsSession, message []byte) []byte {
	network.t.Helper()
	if _, err := session.client.TLS.Write(message); err != nil {
		network.t.Fatalf("the session is closed: %v", err)
	}
	// Whether this finishes or times out, the caller's comparison tells
	// what came back.
	network.runUntil(func(*ipstack.SocketSet) bool { return len(session.client.Plaintext()) >= len(message) })
	return session.client.Plaintext()
}

// sayGoodbye says goodbye with close_notify, and checks that the server
// answers with its own close_notify and then FIN, without waiting for the
// client's FIN.
func sayGoodbye(network *clientNetwork, session *tlsSession) {
	network.t.Helper()
	if err := session.client.TLS.CloseWrite(); err != nil {
		network.t.Fatalf("saying goodbye: %v", err)
	}
	answered := network.runUntil(func(sockets *ipstack.SocketSet) bool {
		return session.client.ServerSaidGoodbye() && sockets.TCP(session.handle).State() == tcp.StateCloseWait
	})
	if !answered {
		network.t.Error("the server did not answer close_notify with its own and FIN")
	}
}

func TestEveryAddressProvesToBeSecureTestAndEchoesInsideTLS(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, fastConfig())
	if addresses := lookUp(network, sim.TLSServerName); !slices.Equal(addresses, []netip.Addr{secureHost}) {
		t.Errorf("secure.test resolved to %v; want [%v]", addresses, secureHost)
	}
	// More than a TCP buffer holds, so that the records span several
	// segments and buffer refills both ways.
	message := bytes.Repeat([]byte("Sealed all the way to orbit and back. "), 160)

	for i, address := range []netip.Addr{secureHost, unlistedHost} {
		session := handshake(network, address, firstLocalPort+uint16(i))

		if echoed := echoInsideTLS(network, session, message); !bytes.Equal(echoed, message) {
			t.Errorf("%v echoed %d bytes; want the %d sent", address, len(echoed), len(message))
		}
		sayGoodbye(network, session)
		closeConnections(network, []ipstack.SocketHandle{session.handle})
	}
}

func TestAClientThatHangsUpWithoutCloseNotifyIsStillToldGoodbye(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, fastConfig())
	session := handshake(network, secureHost, firstLocalPort)

	network.sockets.TCP(session.handle).Close()
	answered := network.runUntil(func(sockets *ipstack.SocketSet) bool {
		return session.client.ServerSaidGoodbye() && isOver(sockets.TCP(session.handle))
	})

	if !answered {
		t.Error("the server did not answer FIN with close_notify and FIN")
	}
}

// Go only, for a rule of Rust's that its tests leave out: the server
// closes the connection only once the rest of the echo and its
// close_notify have gone, as Rust's is_finished waits until rustls has
// nothing left to write. The client hangs up as soon as its records are on
// the connection, with most of the echo still to come.
func TestAClientThatHangsUpStillGetsTheRestOfItsEchoBeforeTheGoodbye(t *testing.T) {
	t.Parallel()
	network := connectNetwork(t, fastConfig())
	session := handshake(network, secureHost, firstLocalPort)
	// Several TCP buffers' worth, so that more of the echo than one holds
	// is still on its way when the client hangs up.
	message := bytes.Repeat([]byte("Sealed all the way to orbit and back. "), 400)
	if _, err := session.client.TLS.Write(message); err != nil {
		t.Fatalf("the session is closed: %v", err)
	}
	if !network.runUntil(func(*ipstack.SocketSet) bool { return session.client.Conn.Pending() == 0 }) {
		t.Fatal("the client's records never all went onto the connection")
	}

	network.sockets.TCP(session.handle).Close()
	toldGoodbye := network.runUntil(func(*ipstack.SocketSet) bool { return session.client.ServerSaidGoodbye() })

	if echoed := session.client.Plaintext(); !toldGoodbye || !bytes.Equal(echoed, message) {
		t.Errorf("told goodbye: %v, after %d of the %d bytes echoed; want all of them, then goodbye",
			toldGoodbye, len(echoed), len(message))
	}
}

func TestThePublishedKeyHashIsThatOfTheBundledCertificate(t *testing.T) {
	t.Parallel()
	bundled, err := os.ReadFile("testdata/secure.test.pem")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(bundled)
	if block == nil {
		t.Fatal("the bundled certificate is not valid PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("the bundled certificate is not valid: %v", err)
	}

	if sha256.Sum256(certificate.RawSubjectPublicKeyInfo) != sim.SecureTestKeySHA256() {
		t.Error("SecureTestKeySHA256 is not the hash of the bundled certificate's key")
	}
}

// Go only: a TLS session runs in a goroutine of its own, which must end
// when the session would end in Rust, with the internet it belongs to or
// with the connection the internet forgets. These tests count the
// goroutines of the whole test binary, so none runs in parallel.

func TestATLSSessionEndsWhenTheNetworkDropsTheCall(t *testing.T) {
	network := connectNetwork(t, fastConfig())
	baseline := runtime.NumGoroutine()
	session := handshake(network, secureHost, firstLocalPort)

	network.control.DropCall()
	session.client.Close()

	sim.AwaitGoroutines(t, baseline)
}

func TestATLSSessionEndsWhenPPPGoesDown(t *testing.T) {
	network := connectNetwork(t, fastConfig())
	baseline := runtime.NumGoroutine()
	session := handshake(network, secureHost, firstLocalPort)

	network.ppp.close()
	if event := network.ppp.nextEvent(patience); event.Kind != ppp.EventDown {
		t.Fatalf("PPP reported %v; want it down", event.Kind)
	}
	session.client.Close()

	sim.AwaitGoroutines(t, baseline)
}

func TestATLSSessionEndsWhenTheNetworkRestartsPPP(t *testing.T) {
	network := connectNetwork(t, fastConfig())
	baseline := runtime.NumGoroutine()
	session := handshake(network, secureHost, firstLocalPort)

	if err := network.control.RestartPPP(renumberedAddress); err != nil {
		t.Fatalf("PPP does not restart: %v", err)
	}
	session.client.Close()

	sim.AwaitGoroutines(t, baseline)
}

// A reset reaches the session's socket, not the session: the session ends
// only because the internet forgets the closed connection, while the call
// and PPP stay up.
func TestATLSSessionEndsWhenItsClientResetsTheConnection(t *testing.T) {
	network := connectNetwork(t, fastConfig())
	baseline := runtime.NumGoroutine()
	session := handshake(network, secureHost, firstLocalPort)

	network.sockets.TCP(session.handle).Abort()
	session.client.Close()
	// The exchanges carry the reset to the internet. Whether this finishes
	// or times out, AwaitGoroutines tells.
	network.runUntil(func(*ipstack.SocketSet) bool { return runtime.NumGoroutine() <= baseline })

	sim.AwaitGoroutines(t, baseline)
}

func TestATLSSessionEndsWithASimulatorNobodyHolds(t *testing.T) {
	baseline := runtime.NumGoroutine()

	startSessionAndLetGo(t)

	sim.AwaitGoroutines(t, baseline)
}

// startSessionAndLetGo starts a TLS session over a simulator, keeps its
// call up and lets go of everything that refers to the simulator. The call
// is dialled without AT&D2, so that the handset's own cleanup, which drops
// DTR, leaves it up: only the cleanup of the modem that the handset shares
// with its controls can then end the session.
func startSessionAndLetGo(t *testing.T) {
	t.Helper()
	simulator := sim.New(fastConfig())
	handset := satlink.NewHandset(simulator)
	if err := handset.Initialize(); err != nil {
		t.Fatal(err)
	}
	settings := callSettings()
	settings.EndCallWhenDTRDrops = false
	network := connectNetworkThrough(t, dialWith(t, handset, settings), simulator.Control())
	session := handshake(network, secureHost, firstLocalPort)
	session.client.Close()
}
