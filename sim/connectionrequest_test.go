package sim

import (
	"net/netip"
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

const (
	requestClientPort = 49_152
	requestServerPort = 80
)

var (
	requestClient = netip.MustParseAddr("10.64.0.2")
	requestServer = netip.MustParseAddr("192.0.2.80")
)

// clientSegment returns a datagram from the client to the server with one
// TCP segment.
func clientSegment(control wire.TCPControl, ackNumber wire.Optional[wire.TCPSeqNumber]) []byte {
	tcp := wire.TCPRepr{
		SrcPort:   requestClientPort,
		DstPort:   requestServerPort,
		Control:   control,
		SeqNumber: wire.TCPSeqNumber(1),
		AckNumber: ackNumber,
		WindowLen: 1024,
	}
	ip := wire.IPv4Repr{
		SrcAddr:    requestClient,
		DstAddr:    requestServer,
		NextHeader: wire.IPProtocolTCP,
		PayloadLen: tcp.BufferLen(),
		HopLimit:   wire.DefaultHopLimit,
	}
	checksums := wire.ChecksumCapabilities{}
	datagram := make([]byte, ip.BufferLen())
	packet := wire.IPv4Packet(datagram)
	ip.Emit(packet, checksums)
	tcp.Emit(wire.TCPPacket(packet.Payload()), requestClient, requestServer, checksums)
	return datagram
}

func TestASYNWithoutACKRequestsAConnection(t *testing.T) {
	t.Parallel()

	request, ok := parseConnectionRequest(clientSegment(wire.TCPControlSYN, wire.Optional[wire.TCPSeqNumber]{}))

	want := connectionRequest{
		local:  netip.AddrPortFrom(requestServer, requestServerPort),
		remote: netip.AddrPortFrom(requestClient, requestClientPort),
	}
	if !ok || request != want {
		t.Errorf("parseConnectionRequest = %+v, %v; want %+v", request, ok, want)
	}
}

func TestOtherSegmentsAndProtocolsRequestNothing(t *testing.T) {
	t.Parallel()
	acknowledgement := wire.Some(wire.TCPSeqNumber(1))
	notTCP := clientSegment(wire.TCPControlSYN, wire.Optional[wire.TCPSeqNumber]{})
	wire.IPv4Packet(notTCP).SetNextHeader(wire.IPProtocolUDP)
	datagrams := map[string][]byte{
		"SYN with ACK": clientSegment(wire.TCPControlSYN, acknowledgement),
		"ACK":          clientSegment(wire.TCPControlNone, acknowledgement),
		"not TCP":      notTCP,
	}

	for name, datagram := range datagrams {
		if request, ok := parseConnectionRequest(datagram); ok {
			t.Errorf("%s: requested %+v", name, request)
		}
	}
}

func TestTheConnectionRequestParserNeverPanicsOnArbitraryBytes(t *testing.T) {
	t.Parallel()
	random := newXorShift(0x1F83_D9AB_FB41_BD6B)

	for range 20_000 {
		datagram := clientSegment(wire.TCPControlSYN, wire.Optional[wire.TCPSeqNumber]{})
		for range random.next() % 4 {
			datagram[random.next()%uint64(len(datagram))] = byte(random.next())
		}
		datagram = datagram[:random.next()%uint64(len(datagram)+1)]
		parseConnectionRequest(datagram)
	}
}
