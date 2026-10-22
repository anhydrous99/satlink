package satnet

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// The addresses, ports and window of the pacing tests' datagrams.
var (
	pacingSource      = ipv4(10, 64, 0, 2)
	pacingDestination = ipv4(192, 0, 2, 7)
)

const (
	repeatWindow       = 3500 * time.Millisecond
	segmentSourcePort  = 49152
	segmentDestination = 7
	segmentWindow      = 1024
	queryPort          = 50000
	// txtRecordType is the DNS type of TXT records (RFC 1035 §3.2.2).
	txtRecordType = 16
	// aRecordType is the DNS type of A records (RFC 1035 §3.2.2).
	aRecordType = 1
)

// pacingEpoch is the moment the pacing tests start from.
var pacingEpoch = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// ipv4Header is the header of a datagram from pacingSource to
// pacingDestination carrying payloadLen bytes of protocol.
func ipv4Header(protocol wire.IPProtocol, payloadLen int) wire.IPv4Repr {
	return wire.IPv4Repr{
		SrcAddr:    pacingSource,
		DstAddr:    pacingDestination,
		NextHeader: protocol,
		PayloadLen: payloadLen,
		HopLimit:   wire.DefaultHopLimit,
	}
}

// segment returns a TCP segment without acknowledgement.
func segment(sequence int32, control wire.TCPControl, payload string) []byte {
	return segmentWith(sequence, control, wire.Optional[wire.TCPSeqNumber]{}, payload)
}

// ackingSegment returns a data segment acknowledging acknowledgement.
func ackingSegment(sequence int32, acknowledgement wire.Optional[wire.TCPSeqNumber], payload string) []byte {
	return segmentWith(sequence, wire.TCPControlPSH, acknowledgement, payload)
}

// acknowledging returns the acknowledgement number number.
func acknowledging(number int32) wire.Optional[wire.TCPSeqNumber] {
	return wire.Some(wire.TCPSeqNumber(number))
}

// segmentWith returns a datagram holding a TCP segment from port 49152 to
// port 7.
func segmentWith(sequence int32, control wire.TCPControl, acknowledgement wire.Optional[wire.TCPSeqNumber], payload string) []byte {
	tcpSegment := wire.TCPRepr{
		SrcPort:   segmentSourcePort,
		DstPort:   segmentDestination,
		Control:   control,
		SeqNumber: wire.TCPSeqNumber(sequence),
		AckNumber: acknowledgement,
		WindowLen: segmentWindow,
		Payload:   []byte(payload),
	}
	header := ipv4Header(wire.IPProtocolTCP, tcpSegment.BufferLen())
	datagram := make([]byte, header.BufferLen())
	packet := wire.IPv4Packet(datagram)
	header.Emit(packet, wire.ChecksumCapabilities{})
	tcpSegment.Emit(wire.TCPPacket(packet.Payload()), pacingSource, pacingDestination, wire.ChecksumCapabilities{})
	return datagram
}

// dnsQuery returns a query for the A record of example.com.
func dnsQuery(transactionID uint16) []byte {
	return dnsMessage(transactionID, aRecordType)
}

// dnsMessage returns a query about example.com for records of queryType.
func dnsMessage(transactionID uint16, queryType byte) []byte {
	message := binary.BigEndian.AppendUint16(nil, transactionID)
	// Recursion desired; one question, no other records.
	message = append(message, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0)
	message = append(message, "\x07example\x03com\x00"...)
	// The type, then class IN.
	message = append(message, 0, queryType, 0, 1)
	header := wire.UDPRepr{SrcPort: queryPort, DstPort: dnsPort}
	ip := ipv4Header(wire.IPProtocolUDP, header.HeaderLen()+len(message))
	datagram := make([]byte, ip.BufferLen())
	packet := wire.IPv4Packet(datagram)
	ip.Emit(packet, wire.ChecksumCapabilities{})
	header.Emit(wire.UDPPacket(packet.Payload()), pacingSource, pacingDestination, message, wire.ChecksumCapabilities{})
	return datagram
}

// admission is a datagram handed to the repeat filter at now, which would
// have left the line at leaves, and whether the filter should let it go.
type admission struct {
	datagram []byte
	now      time.Time
	leaves   time.Time
	admitted bool
}

// expectAdmissions hands filter each datagram of admissions in turn, and
// checks its verdicts.
func expectAdmissions(t *testing.T, filter *repeatFilter, admissions ...admission) {
	t.Helper()
	for i, a := range admissions {
		if got := filter.admit(a.datagram, a.now, a.leaves); got != a.admitted {
			t.Errorf("admission %d at %v: admit = %t, want %t", i, a.now.Sub(pacingEpoch), got, a.admitted)
		}
	}
}

// at is pacingEpoch plus offset.
func at(offset time.Duration) time.Time {
	return pacingEpoch.Add(offset)
}

func TestPacerSpacesDatagramsByTheirTransmissionTime(t *testing.T) {
	pacer := newPacer(300, pacingEpoch)
	if !pacer.isReady(pacingEpoch) {
		t.Fatal("a new pacer is not ready")
	}

	pacer.sent(600, pacingEpoch)

	if pacer.isReady(at(1999 * time.Millisecond)) {
		t.Error("ready before 600 bytes have left at 300 B/s")
	}
	if !pacer.isReady(at(2 * time.Second)) {
		t.Error("not ready once 600 bytes have left")
	}
	if got := pacer.completionOf(300, pacingEpoch); !got.Equal(at(3 * time.Second)) {
		t.Errorf("completionOf(300) = %v after the start, want 3s", got.Sub(pacingEpoch))
	}
}

func TestPacerDoesNotBankIdleTime(t *testing.T) {
	pacer := newPacer(300, pacingEpoch)
	later := at(60 * time.Second)

	pacer.sent(300, later)

	if pacer.isReady(later.Add(500 * time.Millisecond)) {
		t.Error("an idle minute let 300 bytes leave at once")
	}
}

func TestPacerTakesAtLeastOneBytePerSecond(t *testing.T) {
	pacer := newPacer(0, pacingEpoch)

	if got := pacer.completionOf(2, pacingEpoch); !got.Equal(at(2 * time.Second)) {
		t.Errorf("completionOf(2) = %v after the start, want 2s", got.Sub(pacingEpoch))
	}
}

func TestTheWindowRunsFromWhenTheOriginalLeftTheLine(t *testing.T) {
	filter := newRepeatFilter(repeatWindow)
	data := segment(1000, wire.TCPControlPSH, "hello")
	// The original takes two seconds to leave the line.
	left := at(2 * time.Second)

	expectAdmissions(t, filter,
		admission{data, pacingEpoch, left, true},
		admission{data, at(5 * time.Second), left, false},
		admission{data, left.Add(repeatWindow), left.Add(repeatWindow), true},
	)
}

func TestTreatsSYNAndFINAsWorthFilteringButNotBareACKs(t *testing.T) {
	filter := newRepeatFilter(repeatWindow)
	syn := segment(1, wire.TCPControlSYN, "")
	fin := segment(5, wire.TCPControlFIN, "")
	ack := segment(2, wire.TCPControlNone, "")

	expectAdmissions(t, filter,
		admission{syn, pacingEpoch, pacingEpoch, true},
		admission{syn, pacingEpoch, pacingEpoch, false},
		admission{fin, pacingEpoch, pacingEpoch, true},
		admission{fin, pacingEpoch, pacingEpoch, false},
		admission{ack, pacingEpoch, pacingEpoch, true},
		admission{ack, pacingEpoch, pacingEpoch, true},
	)
}

func TestARepeatWithANewerAcknowledgementStillGoes(t *testing.T) {
	filter := newRepeatFilter(repeatWindow)

	expectAdmissions(t, filter,
		admission{ackingSegment(1000, acknowledging(500), "data"), pacingEpoch, pacingEpoch, true},
		admission{ackingSegment(1000, acknowledging(500), "data"), pacingEpoch, pacingEpoch, false},
		admission{ackingSegment(1000, acknowledging(900), "data"), pacingEpoch, pacingEpoch, true},
	)
}

func TestEarlyDNSRetransmissionsAreDropped(t *testing.T) {
	filter := newRepeatFilter(repeatWindow)

	expectAdmissions(t, filter,
		admission{dnsQuery(7), pacingEpoch, pacingEpoch, true},
		admission{dnsQuery(7), at(time.Second), pacingEpoch, false},
		// A different query.
		admission{dnsQuery(8), pacingEpoch, pacingEpoch, true},
	)
}

func TestADifferentMessageWithTheSameDNSIDStillGoes(t *testing.T) {
	filter := newRepeatFilter(repeatWindow)

	expectAdmissions(t, filter,
		admission{dnsQuery(7), pacingEpoch, pacingEpoch, true},
		admission{dnsMessage(7, txtRecordType), pacingEpoch, pacingEpoch, true},
	)
}

func TestDifferentSegmentsAndOtherTrafficPass(t *testing.T) {
	filter := newRepeatFilter(repeatWindow)

	expectAdmissions(t, filter,
		admission{segment(1000, wire.TCPControlPSH, "one"), pacingEpoch, pacingEpoch, true},
		admission{segment(1003, wire.TCPControlPSH, "two"), pacingEpoch, pacingEpoch, true},
		admission{[]byte("not an IP packet"), pacingEpoch, pacingEpoch, true},
	)
}

func TestAZeroWindowDisablesFiltering(t *testing.T) {
	filter := newRepeatFilter(0)
	data := segment(1000, wire.TCPControlPSH, "hello")

	expectAdmissions(t, filter,
		admission{data, pacingEpoch, pacingEpoch, true},
		admission{data, pacingEpoch, pacingEpoch, true},
	)
}

func TestRepeatKeysDescribeTheirPackets(t *testing.T) {
	tcpKey, _ := repeatKeyOf(ackingSegment(1000, acknowledging(500), "data"))
	dnsKey, _ := repeatKeyOf(dnsQuery(7))

	for key, want := range map[repeatKey]string{
		tcpKey: "Tcp { source_port: 49152, destination: 192.0.2.7, destination_port: 7, sequence: 1000, " +
			"length: 4, syn: false, fin: false, acknowledgement: Some(500), window: 1024 }",
		dnsKey: "Dns { source_port: 50000, destination: 192.0.2.7, message_len: 29 }",
	} {
		if got := key.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
	if bare, _ := repeatKeyOf(segment(1, wire.TCPControlSYN, "")); !strings.Contains(bare.String(), "acknowledgement: None") {
		t.Errorf("String() = %q, want no acknowledgement", bare.String())
	}
}
