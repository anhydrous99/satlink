package ipstack_test

// The loopback tests run the whole stack end to end: two interfaces joined by
// an in-memory line, one the app's stack as satnet runs it, the other the
// simulated internet as sim runs it, which answers for every address with
// any-IP. Both devices work in the pull model, and the line can be busy or
// lose chosen datagrams. Each test checks what the applications see and the
// traffic that crossed the line.

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/dns"
	"github.com/anhydrous99/satlink/internal/ipstack/tcp"
	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

const (
	// timeWaitDuration is how long a TCP socket stays in TIME-WAIT.
	timeWaitDuration = 10 * time.Second
	// lineBusyTime is how long the busy-line test keeps the client's line
	// busy: longer than the initial retransmission timeout of 1 s, as a
	// satellite round trip of 1.5 to 3 s is.
	lineBusyTime = 3 * time.Second
)

func TestLoopbackTCPCarriesBulkDataBothWaysAndClosesInOrder(t *testing.T) {
	t.Parallel()
	n := newNetwork(t)
	exchange := startBulkExchange(t, n)

	n.runUntil(exchange.isOver, exchange.serve)

	exchange.assertDelivered(t)
	// The internet answered from the address the client wrote to.
	service := netip.AddrPortFrom(echoHostAddr, bulkPort)
	for _, c := range n.crossingsFrom(n.internet) {
		if header, segment := mustParseTCP(t, c); header.SrcAddr != service.Addr() || segment.SrcPort != service.Port() {
			t.Fatalf("the internet sent %v from %v, want every segment from %v", segment, header.SrcAddr, service)
		}
	}
	assertClosedInOrder(t, n)
}

// assertClosedInOrder checks that the bulk exchange on n closed in order:
// with no RST, the client's FIN first, and the client in TIME-WAIT for
// timeWaitDuration after the internet's FIN before closing.
func assertClosedInOrder(t *testing.T, n *network) {
	t.Helper()
	for _, c := range n.crossings {
		if _, segment := mustParseTCP(t, c); segment.Control == wire.TCPControlRST {
			t.Fatalf("%s sent %v, want no RST in an orderly close", c.from.name, segment)
		}
	}
	clientFIN, clientSentFIN := firstFIN(t, n.client, n.crossings)
	internetFIN, internetSentFIN := firstFIN(t, n.internet, n.crossings)
	if !clientSentFIN || !internetSentFIN || clientFIN > internetFIN {
		t.Fatalf("FINs at crossings %d (client, sent %t) and %d (internet, sent %t), want the client's first", clientFIN, clientSentFIN, internetFIN, internetSentFIN)
	}
	if waited := n.now.Sub(n.crossings[internetFIN].at); waited < timeWaitDuration {
		t.Errorf("the client closed %v after the internet's FIN, want it in TIME-WAIT for at least %v", waited, timeWaitDuration)
	}
}

// firstFIN returns the index in crossings of the first segment with a FIN
// that from sent; false if it sent none.
func firstFIN(t *testing.T, from *host, crossings []crossing) (int, bool) {
	t.Helper()
	for i, c := range crossings {
		if _, segment := mustParseTCP(t, c); c.from == from && segment.Control == wire.TCPControlFIN {
			return i, true
		}
	}
	return 0, false
}

// side says which host a test means: the client or the internet.
type side int

const (
	clientSide side = iota
	internetSide
)

// host returns the host on side s of n.
func (s side) host(n *network) *host {
	if s == clientSide {
		return n.client
	}
	return n.internet
}

// segmentToLose picks the segment a lost-segment test has the line drop,
// given the exchange it belongs to.
type segmentToLose func(e *bulkExchange, segment wire.TCPRepr) bool

func isSYN(_ *bulkExchange, segment wire.TCPRepr) bool {
	return segment.Control == wire.TCPControlSYN
}

func carriesData(_ *bulkExchange, segment wire.TCPRepr) bool {
	return len(segment.Payload) > 0
}

func isFIN(_ *bulkExchange, segment wire.TCPRepr) bool {
	return segment.Control == wire.TCPControlFIN
}

// isSentInTimeWait picks what the client sends once in TIME-WAIT: the ACK
// of the internet's FIN.
func isSentInTimeWait(e *bulkExchange, _ wire.TCPRepr) bool {
	return e.client.socket.State() == tcp.StateTimeWait
}

func TestLoopbackTCPRecoversFromALostSegment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		from       side
		lose       segmentToLose
		occurrence int
	}{
		{"the client's SYN", clientSide, isSYN, 1},
		{"the internet's SYN|ACK", internetSide, isSYN, 1},
		{"a data segment of the client", clientSide, carriesData, 3},
		{"a data segment of the internet", internetSide, carriesData, 2},
		{"the client's FIN", clientSide, isFIN, 1},
		{"the internet's FIN", internetSide, isFIN, 1},
		// The internet retransmits what the lost ACK acknowledged, and the
		// client, in TIME-WAIT, acknowledges each segment again. The first
		// ACK closes the internet's socket; the others reach no socket and
		// draw RSTs, which end the client's TIME-WAIT at once: a socket closes on
		// a RST in every state but SYN-RECEIVED.
		{"the client's ACK of the internet's FIN", clientSide, isSentInTimeWait, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			n := newNetwork(t)
			exchange := startBulkExchange(t, n)
			n.dropOccurrence(test.from.host(n), test.occurrence, func(segment wire.TCPRepr) bool {
				return test.lose(exchange, segment)
			})

			n.runUntil(exchange.isOver, exchange.serve)

			exchange.assertDelivered(t)
			assertLostSegmentWasSentAgain(t, n)
		})
	}
}

// assertLostSegmentWasSentAgain checks that the line lost exactly one
// segment on n, and that its sender later sent a segment from the same
// sequence number that covers at least as much.
func assertLostSegmentWasSentAgain(t *testing.T, n *network) {
	t.Helper()
	lostAt := slices.IndexFunc(n.crossings, func(c crossing) bool { return c.dropped })
	if lostAt < 0 || slices.ContainsFunc(n.crossings[lostAt+1:], func(c crossing) bool { return c.dropped }) {
		t.Fatalf("the line lost a segment at crossing %d of %d, want exactly one lost", lostAt, len(n.crossings))
	}
	lost := n.crossings[lostAt]
	_, lostSegment := mustParseTCP(t, lost)
	for _, c := range n.crossings[lostAt+1:] {
		_, segment := mustParseTCP(t, c)
		if c.from == lost.from && segment.SeqNumber == lostSegment.SeqNumber && segment.SegmentLen() >= lostSegment.SegmentLen() {
			return
		}
	}
	t.Errorf("%s never sent the lost %v again", lost.from.name, lostSegment)
}

func TestLoopbackBusyLineHoldsBackTheClientsTransmissionsButNotItsReplies(t *testing.T) {
	t.Parallel()
	n := newNetwork(t)
	exchange := startBulkExchange(t, n)
	n.runUntil(exchange.isEstablished, exchange.serve)
	firstCrossingWhileBusy := len(n.crossings)
	busyUntil := n.now.Add(lineBusyTime)
	n.client.device.busyUntil = busyUntil

	n.runUntil(exchange.isOver, exchange.serve)

	exchange.assertDelivered(t)
	if n.client.device.refusals == 0 {
		t.Fatal("the busy line refused no transmit slot")
	}
	repliesWhileBusy := 0
	for _, c := range n.crossings[firstCrossingWhileBusy:] {
		if c.from != n.client || !c.at.Before(busyUntil) {
			continue
		}
		if !c.isReply {
			t.Errorf("the client sent %x through a transmit slot while its line was busy", c.datagram)
		}
		repliesWhileBusy++
	}
	if repliesWhileBusy == 0 {
		t.Error("the client sent no reply while its line was busy, want its reply slots used")
	}
}

func TestLoopbackUDPEchoAnswersFromTheAddressTheClientWroteTo(t *testing.T) {
	t.Parallel()
	n := newNetwork(t)
	echo := &udpEcho{t: t, socket: newDatagramSocket(t, wire.ListenEndpoint{Port: echoPort})}
	n.internet.sockets.AddUDP(echo.socket)
	client := &datagramReader{t: t, socket: newDatagramSocket(t, wire.ListenEndpoint{Port: clientPort})}
	n.client.sockets.AddUDP(client.socket)
	destinations := []netip.AddrPort{netip.AddrPortFrom(echoHostAddr, echoPort), netip.AddrPortFrom(webHostAddr, echoPort)}
	for i, destination := range destinations {
		sendDatagram(t, client.socket, pingPayload(i), destination)
	}

	n.runUntil(func() bool { return len(client.received) == len(destinations) }, echo.serve, client.serve)

	var wantAtEcho, wantAtClient []receivedDatagram
	for i, destination := range destinations {
		fromClient := udp.Metadata{Endpoint: netip.AddrPortFrom(clientAddr, clientPort), LocalAddress: destination.Addr()}
		wantAtEcho = append(wantAtEcho, receivedDatagram{payload: pingPayload(i), meta: fromClient})
		fromDestination := udp.Metadata{Endpoint: destination, LocalAddress: clientAddr}
		wantAtClient = append(wantAtClient, receivedDatagram{payload: pingPayload(i), meta: fromDestination})
	}
	if !slices.Equal(echo.received, wantAtEcho) {
		t.Errorf("the echo service read %+v, want %+v", echo.received, wantAtEcho)
	}
	if !slices.Equal(client.received, wantAtClient) {
		t.Errorf("the client read %+v, want %+v", client.received, wantAtClient)
	}
}

// pingPayload is the payload of the i-th datagram of the UDP test.
func pingPayload(i int) string {
	return fmt.Sprintf("ping %d", i)
}

func TestLoopbackDNSQueryIsAnsweredByAHandBuiltResponder(t *testing.T) {
	t.Parallel()
	webHostAddrs := testZone["web.test"].addresses
	tests := []struct {
		name, host string
		want       []netip.Addr
		wantErr    error
	}{
		{"an A record", "echo.test", []netip.Addr{echoHostAddr}, nil},
		{"two A records, for a name with a trailing dot", "web.test.", webHostAddrs, nil},
		{"an alias, through its CNAME record", "www.test", webHostAddrs, nil},
		{"an unknown name, which is NXDOMAIN", "nowhere.test", nil, dns.ErrFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			n := newNetwork(t)
			responder := newDNSResponder(t, n)
			lookup := startLookup(t, n, test.host)

			n.runUntil(lookup.isSettled, responder.serve, lookup.collect)

			if !slices.Equal(lookup.addresses, test.want) || !errors.Is(lookup.err, test.wantErr) {
				t.Errorf("the lookup of %q gave %v, %v; want %v, %v", test.host, lookup.addresses, lookup.err, test.want, test.wantErr)
			}
			assertAskedOnce(t, responder, strings.TrimSuffix(test.host, "."))
		})
	}
}

// assertAskedOnce checks that responder was asked about name exactly once,
// as satnet's resolver asks: by the client, of the gateway, recursion
// desired.
func assertAskedOnce(t *testing.T, responder *dnsResponder, name string) {
	t.Helper()
	if len(responder.asked) != 1 {
		t.Fatalf("the responder was asked %d times, want once: %+v", len(responder.asked), responder.asked)
	}
	query := responder.asked[0]
	if query.from.Addr() != clientAddr || query.to != gatewayAddr || query.name != name || !query.recursionDesired {
		t.Errorf("the responder was asked %+v, want %q from %v to %v, recursion desired", query, name, clientAddr, gatewayAddr)
	}
}

func TestLoopbackUDPToAPortWithoutASocketGetsPortUnreachable(t *testing.T) {
	t.Parallel()
	n := newNetwork(t)
	client := newDatagramSocket(t, wire.ListenEndpoint{Port: clientPort})
	n.client.sockets.AddUDP(client)
	sendDatagram(t, client, "anyone there?", netip.AddrPortFrom(echoHostAddr, unservedPort))

	n.settle()

	// The datagram and the error, and nothing in answer to the error.
	if len(n.crossings) != 2 {
		t.Fatalf("%d datagrams crossed the line, want the client's and the internet's ICMP error", len(n.crossings))
	}
	sent, answer := n.crossings[0], n.crossings[1]
	header, message := mustParseICMPv4(t, answer)
	if answer.from != n.internet || !answer.isReply || header.SrcAddr != echoHostAddr || header.DstAddr != clientAddr {
		t.Errorf("%s answered from %v to %v (through the reply slot: %t), want the internet from %v to %v through it",
			answer.from.name, header.SrcAddr, header.DstAddr, answer.isReply, echoHostAddr, clientAddr)
	}
	if message.Type() != wire.ICMPv4DstUnreachable || message.Code() != wire.ICMPv4PortUnreachable || !bytes.Equal(message.Data(), sent.datagram) {
		t.Errorf("ICMP %v code %d quoting %x, want port unreachable quoting the client's datagram %x", message.Type(), message.Code(), message.Data(), sent.datagram)
	}
	if client.CanReceive() {
		t.Error("the client's socket received something")
	}
}

func TestLoopbackTCPToAPortWithoutAListenerGetsRST(t *testing.T) {
	t.Parallel()
	n := newNetwork(t)
	service := netip.AddrPortFrom(echoHostAddr, unservedPort)
	client := connectClient(t, n, service)

	n.settle()

	if len(n.crossings) != 2 {
		t.Fatalf("%d datagrams crossed the line, want the client's SYN and the internet's RST", len(n.crossings))
	}
	_, syn := mustParseTCP(t, n.crossings[0])
	answer := n.crossings[1]
	header, rst := mustParseTCP(t, answer)
	wantACK := wire.Some(syn.SeqNumber.Add(1))
	if answer.from != n.internet || !answer.isReply || netip.AddrPortFrom(header.SrcAddr, rst.SrcPort) != service ||
		rst.Control != wire.TCPControlRST || rst.AckNumber != wantACK {
		t.Errorf("%s answered %v from %v (through the reply slot: %t), want the internet's RST|ACK %v from %v through it",
			answer.from.name, rst, header.SrcAddr, answer.isReply, wantACK, service)
	}
	// The connection was refused: CLOSED before ESTABLISHED, with nothing
	// left to send and no timer running.
	if client.State() != tcp.StateClosed {
		t.Errorf("the client is %v, want %v", client.State(), tcp.StateClosed)
	}
	if at, ok := n.client.iface.PollAt(n.now, &n.client.sockets); ok {
		t.Errorf("the refused client still wants a poll at %v", at)
	}
}

// mustParseTCP returns the header and segment of c, which must carry a
// well-formed TCP segment.
func mustParseTCP(t *testing.T, c crossing) (wire.IPv4Repr, wire.TCPRepr) {
	t.Helper()
	header, segment, ok := parseTCPDatagram(c.datagram)
	if !ok {
		t.Fatalf("%s sent %x, want a TCP segment", c.from.name, c.datagram)
	}
	return header, segment
}

// mustParseICMPv4 returns the header and message of c, which must carry a
// well-formed ICMPv4 message.
func mustParseICMPv4(t *testing.T, c crossing) (wire.IPv4Repr, wire.ICMPv4Packet) {
	t.Helper()
	header, payload, ok := parseIPv4Datagram(c.datagram)
	if !ok || header.NextHeader != wire.IPProtocolICMP {
		t.Fatalf("%s sent %x, want an ICMP message", c.from.name, c.datagram)
	}
	message, err := wire.NewICMPv4Packet(payload)
	if err != nil || !message.VerifyChecksum() {
		t.Fatalf("%s sent a malformed ICMP message %x (%v)", c.from.name, payload, err)
	}
	return header, message
}
