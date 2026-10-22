package satnet

import (
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// Keeping the satellite line busy without overfilling it.
//
// The pacer releases datagrams to the handset no faster than the link
// carries them, so any backlog waits in the TCP stack. The repeat filter
// drops TCP segments and DNS queries resent before an answer could have
// arrived.

// pacer tracks when the line will have finished sending what it was given.
// It reads no clock: every method takes the time.
type pacer struct {
	bytesPerSecond int
	lineFreeAt     time.Time
}

// newPacer returns a pacer for a line carrying bytesPerSecond, at least one,
// that is free at now.
func newPacer(bytesPerSecond int, now time.Time) *pacer {
	return &pacer{bytesPerSecond: max(bytesPerSecond, 1), lineFreeAt: now}
}

// isReady reports whether the line has drained at now and can take the
// next datagram.
func (p *pacer) isReady(now time.Time) bool {
	return !now.Before(p.lineFreeAt)
}

// completionOf returns when bytes handed over at now would have left the
// line: idle time is not banked, so a line free since long ago starts at
// now.
func (p *pacer) completionOf(bytes int, now time.Time) time.Time {
	start := p.lineFreeAt
	if now.After(start) {
		start = now
	}
	return start.Add(p.transmissionTime(bytes))
}

// sent accounts for bytes just written to the line at now.
func (p *pacer) sent(bytes int, now time.Time) {
	p.lineFreeAt = p.completionOf(bytes, now)
}

// transmissionTime is how long the line takes to carry bytes, to the
// microsecond.
func (p *pacer) transmissionTime(bytes int) time.Duration {
	micros := uint64(bytes) * uint64(time.Second/time.Microsecond) / uint64(p.bytesPerSecond)
	return time.Duration(micros) * time.Microsecond
}

// repeatFilter drops a TCP segment or DNS query that repeats one which left
// the line less than window ago.
//
// The TCP/IP stack retransmits TCP after one second at first, and DNS
// queries after one second too — both shorter than a satellite round trip
// — so it resends packets whose answers are still on their way. Each copy
// costs as much airtime as the original. Dropping early repeats stretches
// those first timeouts to window; a packet that was really lost is
// recovered by the next, later retransmission.
type repeatFilter struct {
	window time.Duration
	// recent are the packets that left the line less than window ago,
	// oldest first.
	recent []recentPacket
}

// recentPacket is a packet the filter let through, and when it finished
// leaving the line.
type recentPacket struct {
	key  repeatKey
	left time.Time
}

// newRepeatFilter returns a filter that drops repeats within window; zero
// disables it.
func newRepeatFilter(window time.Duration) *repeatFilter {
	return &repeatFilter{window: window}
}

// admit reports whether datagram, handed over at now, should be sent; if
// so it is remembered as leaving the line at leavesLineAt.
func (f *repeatFilter) admit(datagram []byte, now, leavesLineAt time.Time) bool {
	key, ok := repeatKeyOf(datagram)
	if !ok {
		return true
	}
	f.recent = slices.DeleteFunc(f.recent, func(packet recentPacket) bool {
		return max(now.Sub(packet.left), 0) >= f.window
	})
	if slices.ContainsFunc(f.recent, func(packet recentPacket) bool { return packet.key == key }) {
		logDebug("dropped an early repeat of " + key.String())
		return false
	}
	f.recent = append(f.recent, recentPacket{key: key, left: leavesLineAt})
	return true
}

// repeatKey is what makes a packet a repeat of an earlier one. It is
// comparable with ==; its kind says which fields count.
type repeatKey struct {
	kind repeatKind
	// sourcePort and destination are the sender's port and the address
	// the packet goes to (both kinds).
	sourcePort  uint16
	destination netip.Addr
	// The TCP segment's connection, place in the stream, length and flags,
	// and the news it carries for the peer (repeatTCP).
	destinationPort uint16
	sequence        int32
	length          int
	syn             bool
	fin             bool
	acknowledgement wire.Optional[int32]
	window          uint16
	// message is the whole DNS message (repeatDNS). A string rather than a
	// hash keeps the key comparable and exact, for a few hundred bytes per
	// remembered query.
	message string
}

// repeatKind says what kind of packet a [repeatKey] identifies.
type repeatKind int

const (
	// repeatTCP is a TCP segment: same connection, same place in the
	// stream, same length and flags — and no news for the peer: a resend
	// carrying a newer acknowledgement or window still goes, as the peer
	// may be waiting for exactly that.
	repeatTCP repeatKind = iota + 1
	// repeatDNS is a DNS query: a retransmission repeats the message byte
	// for byte, while another message to port 53 may share its id and
	// length.
	repeatDNS
)

// repeatKeyOf returns what identifies datagram as a repeat, and false for a
// datagram no repeat of which is ever dropped.
func repeatKeyOf(datagram []byte) (repeatKey, bool) {
	ip, err := wire.NewIPv4Packet(datagram)
	if err != nil {
		return repeatKey{}, false
	}
	switch ip.NextHeader() {
	case wire.IPProtocolTCP:
		return tcpRepeatKey(ip.DstAddr(), ip.Payload())
	case wire.IPProtocolUDP:
		return dnsRepeatKey(ip.DstAddr(), ip.Payload())
	default:
		return repeatKey{}, false
	}
}

// tcpRepeatKey identifies a segment carrying data, SYN or FIN: bare
// acknowledgements are never retransmitted.
func tcpRepeatKey(destination netip.Addr, payload []byte) (repeatKey, bool) {
	segment, err := wire.NewTCPPacket(payload)
	if err != nil {
		return repeatKey{}, false
	}
	length := len(segment.Payload())
	if length == 0 && !segment.SYN() && !segment.FIN() {
		return repeatKey{}, false
	}
	var acknowledgement wire.Optional[int32]
	if segment.ACK() {
		acknowledgement = wire.Some(int32(segment.AckNumber()))
	}
	return repeatKey{
		kind:            repeatTCP,
		sourcePort:      segment.SrcPort(),
		destination:     destination,
		destinationPort: segment.DstPort(),
		sequence:        int32(segment.SeqNumber()),
		length:          length,
		syn:             segment.SYN(),
		fin:             segment.FIN(),
		acknowledgement: acknowledgement,
		window:          segment.WindowLen(),
	}, true
}

// dnsRepeatKey identifies a DNS query: a UDP datagram to port 53.
func dnsRepeatKey(destination netip.Addr, payload []byte) (repeatKey, bool) {
	datagram, err := wire.NewUDPPacket(payload)
	if err != nil || datagram.DstPort() != dnsPort {
		return repeatKey{}, false
	}
	return repeatKey{
		kind:        repeatDNS,
		sourcePort:  datagram.SrcPort(),
		destination: destination,
		message:     string(datagram.Payload()),
	}, true
}

// String describes the packet the key identifies, for the log. A DNS query
// is described by the length of its message rather than the message.
func (k repeatKey) String() string {
	if k.kind == repeatDNS {
		return fmt.Sprintf("Dns { source_port: %d, destination: %v, message_len: %d }",
			k.sourcePort, k.destination, len(k.message))
	}
	acknowledgement := "None"
	if value, ok := k.acknowledgement.Get(); ok {
		acknowledgement = fmt.Sprintf("Some(%d)", value)
	}
	return fmt.Sprintf("Tcp { source_port: %d, destination: %v, destination_port: %d, sequence: %d, "+
		"length: %d, syn: %t, fin: %t, acknowledgement: %s, window: %d }",
		k.sourcePort, k.destination, k.destinationPort, k.sequence,
		k.length, k.syn, k.fin, acknowledgement, k.window)
}
