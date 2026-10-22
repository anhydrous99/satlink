package wire

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// UDPHeaderLen is the length of a UDP header.
const UDPHeaderLen = 8

// Offsets of the UDP header fields (RFC 768).
const (
	udpSrcPortOffset  = 0
	udpDstPortOffset  = 2
	udpLengthOffset   = 4
	udpChecksumOffset = 6
)

// The checksum field's special values (RFC 768).
const (
	// udpNoChecksum in the checksum field means that the sender computed no
	// checksum, which UDP over IPv4 allows.
	udpNoChecksum uint16 = 0
	// udpZeroChecksum is how a computed checksum of zero is sent:
	// arithmetically, RFC 1071 checksums of all-zeroes and all-ones behave
	// identically, so no action is necessary on the remote end.
	udpZeroChecksum uint16 = 0xffff
)

// UDPPacket is a read/write view of a UDP datagram. Converting a buffer,
// UDPPacket(buffer), does not check it; NewUDPPacket does.
type UDPPacket []byte

// NewUDPPacket returns buffer as a UDPPacket after checking, as CheckLen does,
// that no accessor will panic.
func NewUDPPacket(buffer []byte) (UDPPacket, error) {
	packet := UDPPacket(buffer)
	if err := packet.CheckLen(); err != nil {
		return nil, err
	}
	return packet, nil
}

// CheckLen returns ErrMalformed unless the buffer holds the header and the
// length field is between 8 and the buffer's length. Setting the length
// invalidates the check.
func (p UDPPacket) CheckLen() error {
	if len(p) < UDPHeaderLen {
		return ErrMalformed
	}
	if length := int(p.Length()); len(p) < length || length < UDPHeaderLen {
		return ErrMalformed
	}
	return nil
}

// SrcPort returns the source port field; 0 means the sender expects no reply.
func (p UDPPacket) SrcPort() uint16 {
	return binary.BigEndian.Uint16(p[udpSrcPortOffset:])
}

// DstPort returns the destination port field.
func (p UDPPacket) DstPort() uint16 {
	return binary.BigEndian.Uint16(p[udpDstPortOffset:])
}

// Length returns the length field: header and payload, in octets.
func (p UDPPacket) Length() uint16 {
	return binary.BigEndian.Uint16(p[udpLengthOffset:])
}

// Checksum returns the checksum field; 0 means the sender computed none.
func (p UDPPacket) Checksum() uint16 {
	return binary.BigEndian.Uint16(p[udpChecksumOffset:])
}

// VerifyChecksum reports whether the checksum is correct for a datagram sent
// from src to dst; a zero checksum, meaning none was computed, always
// verifies. It panics unless both addresses are IPv4 (Is4).
func (p UDPPacket) VerifyChecksum(src, dst netip.Addr) bool {
	requireIPv4(src)
	requireIPv4(dst)
	// From the RFC: "An all zero transmitted checksum value means that the
	// transmitter generated no checksum (for debugging or for higher level
	// protocols that don't care)."
	if p.Checksum() == udpNoChecksum {
		return true
	}
	return transportChecksum(src, dst, IPProtocolUDP, p.datagram()) == correctChecksumSum
}

// Payload returns the octets between the header and the length field's end;
// it shares the packet's storage, so writing to it fills in the payload.
func (p UDPPacket) Payload() []byte {
	end := int(p.Length())
	return p[UDPHeaderLen:end:end]
}

// SetSrcPort sets the source port field.
func (p UDPPacket) SetSrcPort(port uint16) {
	binary.BigEndian.PutUint16(p[udpSrcPortOffset:], port)
}

// SetDstPort sets the destination port field.
func (p UDPPacket) SetDstPort(port uint16) {
	binary.BigEndian.PutUint16(p[udpDstPortOffset:], port)
}

// SetLength sets the length field.
func (p UDPPacket) SetLength(length uint16) {
	binary.BigEndian.PutUint16(p[udpLengthOffset:], length)
}

// SetChecksum sets the checksum field.
func (p UDPPacket) SetChecksum(checksum uint16) {
	binary.BigEndian.PutUint16(p[udpChecksumOffset:], checksum)
}

// FillChecksum computes the checksum for a datagram sent from src to dst and
// writes it to the header. A computed checksum of zero is sent as 0xffff, as
// zero would mean "no checksum" (RFC 768); all ones tells the remote end to
// verify it. FillChecksum panics unless both addresses are IPv4 (Is4).
func (p UDPPacket) FillChecksum(src, dst netip.Addr) {
	p.SetChecksum(0)
	checksum := ^transportChecksum(src, dst, IPProtocolUDP, p.datagram())
	if checksum == udpNoChecksum {
		checksum = udpZeroChecksum
	}
	p.SetChecksum(checksum)
}

// datagram returns the octets the length field covers: the header and the
// payload, without what may follow them in the buffer.
func (p UDPPacket) datagram() []byte {
	return p[:p.Length()]
}

// UDPRepr is a high-level representation of a UDP header; the payload
// travels beside it.
type UDPRepr struct {
	// SrcPort is the source port; 0 means the sender expects no reply.
	SrcPort uint16
	// DstPort is the destination port.
	DstPort uint16
}

// ParseUDPRepr checks packet, a datagram sent from src to dst, and returns
// its header. It fails with ErrMalformed when the packet fails CheckLen, the
// destination port is 0, or the checksum is bad (when checksums.UDP
// verifies on receive) and not zero: over IPv4 the checksum may be omitted.
// Verifying the checksum panics, as VerifyChecksum does, unless both
// addresses are IPv4.
func ParseUDPRepr(packet UDPPacket, src, dst netip.Addr, checksums ChecksumCapabilities) (UDPRepr, error) {
	if err := packet.CheckLen(); err != nil {
		return UDPRepr{}, err
	}
	// Destination port cannot be omitted (but source port can be).
	if packet.DstPort() == 0 {
		return UDPRepr{}, ErrMalformed
	}
	// Valid checksum is expected... except on UDP-over-IPv4, where it can be
	// omitted: VerifyChecksum accepts a zero checksum.
	if checksums.UDP.OnReceive() && !packet.VerifyChecksum(src, dst) {
		return UDPRepr{}, ErrMalformed
	}
	return UDPRepr{SrcPort: packet.SrcPort(), DstPort: packet.DstPort()}, nil
}

// HeaderLen returns the length of the header Emit writes.
func (r UDPRepr) HeaderLen() int {
	return UDPHeaderLen
}

// Emit writes the header and payload into packet, which must be exactly
// HeaderLen + len(payload) octets long, for a datagram sent from src to dst.
// The checksum is computed when checksums.UDP computes on send and is zero
// otherwise. Emit panics unless both addresses are IPv4 (Is4).
func (r UDPRepr) Emit(packet UDPPacket, src, dst netip.Addr, payload []byte, checksums ChecksumCapabilities) {
	requireIPv4(src)
	requireIPv4(dst)
	packet.SetSrcPort(r.SrcPort)
	packet.SetDstPort(r.DstPort)
	packet.SetLength(uint16(r.HeaderLen() + len(payload)))
	copy(packet.Payload(), payload)
	if checksums.UDP.OnSend() {
		packet.FillChecksum(src, dst)
	} else {
		// Make sure we get a consistently zeroed checksum, since
		// implementations might rely on it.
		packet.SetChecksum(0)
	}
}

// String formats the header as "UDP src=… dst=…".
func (r UDPRepr) String() string {
	return fmt.Sprintf("UDP src=%d dst=%d", r.SrcPort, r.DstPort)
}
