package ipstack

// These tests pin the pull model the consumers rely on: the rounds of
// sending, the reply slot of a received datagram, and when the interface asks
// for a transmit slot. "Layers and their contracts" in docs/IPSTACK.md
// describes it.

import (
	"bytes"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/udp"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// echoPort is the port of the UDP sockets of these tests.
const echoPort = 7

// queueDatagrams queues one datagram for the peer's echo port for each
// payload.
func queueDatagrams(t *testing.T, s *udp.Socket, payloads ...string) {
	t.Helper()
	for _, payload := range payloads {
		if err := s.SendSlice([]byte(payload), udp.Metadata{Endpoint: netip.AddrPortFrom(remoteAddr, echoPort)}); err != nil {
			t.Fatalf("SendSlice of %d octets: %v", len(payload), err)
		}
	}
}

// assertSentPayloads checks the payloads of the UDP datagrams the device sent
// since the last call, in order.
func assertSentPayloads(t *testing.T, device *testingDevice, want ...string) {
	t.Helper()
	var got []string
	for _, sent := range device.takeSent() {
		_, _, payload := parseUDP(t, sent.datagram)
		got = append(got, string(payload))
	}
	if !slices.Equal(got, want) {
		t.Errorf("sent %q, want %q", got, want)
	}
}

func TestPollRunsRoundsWhileARoundSendsSomething(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	first := newUDPSocket(t, wire.ListenEndpoint{Port: echoPort})
	queueDatagrams(t, first, "first 1", "first 2")
	second := newUDPSocket(t, wire.ListenEndpoint{Port: echoPort + 1})
	queueDatagrams(t, second, "second 1")
	sockets.AddUDP(first)
	sockets.AddUDP(second)

	result := iface.Poll(at(0), device, sockets)

	// Each round gives every socket one turn, in slot order.
	assertSentPayloads(t, device, "first 1", "second 1", "first 2")
	if result != PollSocketStateChanged || device.transmitRequests != 3 {
		t.Errorf("Poll = %v after %d transmit requests, want %v after 3", result, device.transmitRequests, PollSocketStateChanged)
	}
}

func TestSocketsImmediateReplyGoesThroughTheReplySlot(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	established := establishedConnection(t, iface, sockets, device)
	// Data ten octets ahead of what the socket expects is out of order, so
	// the socket acknowledges what it has at once.
	outOfOrder := wire.TCPRepr{
		SrcPort:   49500,
		DstPort:   80,
		SeqNumber: established.remoteSeq.Add(10),
		AckNumber: wire.Some(established.localSeq),
		WindowLen: 1024,
		Payload:   []byte("later"),
	}
	device.deliver(tcpDatagram(remoteAddr, localAddr, outOfOrder))

	iface.Poll(at(2*time.Millisecond), device, sockets)

	reply := onlySent(t, device)
	_, ack := parseTCP(t, reply.datagram)
	if !reply.isReply || ack.AckNumber != wire.Some(established.remoteSeq) || len(ack.Payload) != 0 {
		t.Errorf("sent %v (through the reply slot: %t), want an ACK of %v through it", ack, reply.isReply, established.remoteSeq)
	}
}

func TestPollIngressSingleProcessesOneDatagramAndSendsOnlyItsReply(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	datagrams := newUDPSocket(t, wire.ListenEndpoint{Port: echoPort})
	queueDatagrams(t, datagrams, "waits for Poll")
	sockets.AddUDP(datagrams)
	device.deliver(
		tcpDatagram(remoteAddr, localAddr, syn(49500, 81)),
		udpDatagram(netip.AddrPortFrom(remoteAddr, 49500), netip.AddrPortFrom(localAddr, echoPort), []byte("second")),
	)

	result := iface.PollIngressSingle(at(0), device, sockets)

	if result != IngressSocketStateChanged {
		t.Errorf("PollIngressSingle = %v, want %v", result, IngressSocketStateChanged)
	}
	if reply := onlySent(t, device); !reply.isReply {
		t.Error("the RST did not go through the reply slot")
	}
	if device.transmitRequests != 0 || datagrams.SendQueue() == 0 {
		t.Errorf("PollIngressSingle asked for %d transmit slots and left %d octets queued; want none asked, the datagram queued", device.transmitRequests, datagrams.SendQueue())
	}
	if len(device.received) != 1 || datagrams.CanReceive() {
		t.Fatalf("%d datagrams wait in the device, want the second one", len(device.received))
	}

	if result := iface.PollIngressSingle(at(0), device, sockets); result != IngressSocketStateChanged || !datagrams.CanReceive() {
		t.Errorf("the second PollIngressSingle = %v, received %t; want %v and the datagram", result, datagrams.CanReceive(), IngressSocketStateChanged)
	}
	assertNothingSent(t, device)
}

func TestPollIngressSingleReturnsNoneWhenNothingWaits(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	datagrams := newUDPSocket(t, wire.ListenEndpoint{Port: echoPort})
	queueDatagrams(t, datagrams, "waits for Poll")
	sockets.AddUDP(datagrams)

	result := iface.PollIngressSingle(at(0), device, sockets)

	if result != IngressNone || device.transmitRequests != 0 {
		t.Errorf("PollIngressSingle = %v after %d transmit requests, want %v after none", result, device.transmitRequests, IngressNone)
	}
	assertNothingSent(t, device)
}

func TestOversizedOutgoingDatagramTakesItsSlotAndCountsAsSent(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	datagrams := newUDPSocket(t, wire.ListenEndpoint{Port: echoPort})
	// One octet more than a datagram of the MTU carries.
	oversized := string(bytes.Repeat([]byte{'x'}, testingDeviceMTU-wire.IPv4HeaderLen-wire.UDPHeaderLen+1))
	queueDatagrams(t, datagrams, oversized, "fits")
	sockets.AddUDP(datagrams)

	result := iface.Poll(at(0), device, sockets)

	// The oversized datagram took a slot and was dropped, but counted as
	// sent: the socket dequeued it, and another round sent the next one.
	assertSentPayloads(t, device, "fits")
	if result != PollSocketStateChanged || device.transmitRequests != 2 || datagrams.SendQueue() != 0 {
		t.Errorf("Poll = %v after %d transmit requests with %d octets left; want %v after 2 with none",
			result, device.transmitRequests, datagrams.SendQueue(), PollSocketStateChanged)
	}
}

func TestDatagramOfTheMTUIsSent(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	datagrams := newUDPSocket(t, wire.ListenEndpoint{Port: echoPort})
	payload := string(bytes.Repeat([]byte{'x'}, testingDeviceMTU-wire.IPv4HeaderLen-wire.UDPHeaderLen))
	queueDatagrams(t, datagrams, payload)
	sockets.AddUDP(datagrams)

	iface.Poll(at(0), device, sockets)

	if sent := onlySent(t, device); len(sent.datagram) != testingDeviceMTU {
		t.Errorf("sent %d octets, want a datagram of the MTU, %d", len(sent.datagram), testingDeviceMTU)
	}
}

func TestOversizedIncomingDatagramIsProcessed(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	datagrams := newUDPSocket(t, wire.ListenEndpoint{Port: echoPort})
	sockets.AddUDP(datagrams)
	payload := bytes.Repeat([]byte{'x'}, 2*testingDeviceMTU)
	device.deliver(udpDatagram(netip.AddrPortFrom(remoteAddr, 49500), netip.AddrPortFrom(localAddr, echoPort), payload))

	iface.Poll(at(0), device, sockets)

	if received, _, err := datagrams.Receive(); err != nil || !bytes.Equal(received, payload) {
		t.Errorf("Receive = %d octets, %v; want the %d-octet datagram", len(received), err, len(payload))
	}
}

func TestPollsSetTheContextsTime(t *testing.T) {
	t.Parallel()
	iface, sockets, device := setup(t)
	polls := map[string]func(now time.Time){
		"Poll":              func(now time.Time) { iface.Poll(now, device, sockets) },
		"PollIngressSingle": func(now time.Time) { iface.PollIngressSingle(now, device, sockets) },
		"PollAt":            func(now time.Time) { iface.PollAt(now, sockets) },
	}
	elapsed := time.Duration(0)
	for name, poll := range polls {
		elapsed += time.Second
		now := at(elapsed)

		poll(now)

		if got := iface.Context().Now(); !got.Equal(now) {
			t.Errorf("after %s at %v the context's time is %v", name, elapsed, got.Sub(time.Time{}))
		}
	}
}
