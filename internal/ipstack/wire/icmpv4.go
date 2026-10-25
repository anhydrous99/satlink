package wire

import (
	"encoding/binary"
	"fmt"
	"strconv"
)

// ICMPv4Message is the type field of an ICMPv4 message. ICMPv4Repr
// represents only the type the stack sends.
type ICMPv4Message uint8

// ICMPv4DstUnreachable is the type of the only ICMPv4 message the stack
// sends (RFC 792): it reports a datagram that could not be delivered, and
// its code says why.
const ICMPv4DstUnreachable ICMPv4Message = 3

// String describes the message type, "destination unreachable", or gives its
// number for any other type.
func (m ICMPv4Message) String() string {
	if m == ICMPv4DstUnreachable {
		return "destination unreachable"
	}
	return strconv.Itoa(int(m))
}

// Codes of the destination-unreachable messages the stack sends (RFC 792).
const (
	// ICMPv4ProtoUnreachable answers a datagram of a protocol the stack does
	// not handle.
	ICMPv4ProtoUnreachable uint8 = 2
	// ICMPv4PortUnreachable answers a UDP datagram no socket accepts.
	ICMPv4PortUnreachable uint8 = 3
)

// icmpv4HeaderLen is the header length of every ICMPv4 message the stack
// sends: the type, the code and the checksum, then four octets whose
// meaning depends on the type, unused in an error message (RFC 792).
const icmpv4HeaderLen = 8

// Offsets of the ICMPv4 header fields (RFC 792).
const (
	icmpv4TypeOffset     = 0
	icmpv4CodeOffset     = 1
	icmpv4ChecksumOffset = 2
	// icmpv4UnusedOffset starts an error message's unused field, which runs
	// to the end of the header.
	icmpv4UnusedOffset = 4
)

// ICMPv4Packet is a write-only view of an ICMPv4 message: the stack sends
// ICMP errors but reads no ICMP message. Converting a buffer,
// ICMPv4Packet(buffer), does not check it.
type ICMPv4Packet []byte

// HeaderLen returns the length of the header: 8 for every message the stack
// sends.
func (p ICMPv4Packet) HeaderLen() int {
	return icmpv4HeaderLen
}

// Data returns the octets after the header; it shares the packet's storage,
// so writing to it fills in the data.
func (p ICMPv4Packet) Data() []byte {
	return p[p.HeaderLen():]
}

// SetType sets the message type field.
func (p ICMPv4Packet) SetType(message ICMPv4Message) {
	p[icmpv4TypeOffset] = byte(message)
}

// SetCode sets the message code field.
func (p ICMPv4Packet) SetCode(code uint8) {
	p[icmpv4CodeOffset] = code
}

// SetChecksum sets the checksum field.
func (p ICMPv4Packet) SetChecksum(checksum uint16) {
	binary.BigEndian.PutUint16(p[icmpv4ChecksumOffset:], checksum)
}

// FillChecksum computes the checksum over the whole message and writes it to
// the header.
func (p ICMPv4Packet) FillChecksum() {
	p.SetChecksum(0)
	p.SetChecksum(^dataChecksum(p))
}

// ICMPv4Repr is a high-level representation of the ICMPv4 messages the stack
// can emit: the destination-unreachable errors that answer a datagram it
// cannot deliver. It is not comparable with ==, because of its data.
type ICMPv4Repr struct {
	// Message is ICMPv4DstUnreachable.
	Message ICMPv4Message
	// Code is the reason for the error, such as ICMPv4PortUnreachable.
	Code uint8
	// Header is the IPv4 header of the datagram the error is about,
	// unchanged: PayloadLen is that datagram's full payload length, which
	// Emit writes into the quoted total-length field, however little of the
	// payload Data quotes.
	Header IPv4Repr
	// Data is the start of the payload of the datagram the error is about.
	// The stack quotes at most 528 octets, so that the error fits into
	// IPv4MinMTU, and possibly none.
	Data []byte
}

// BufferLen returns the length of the message Emit writes: the 8-octet
// header, the quoted IPv4 header (IPv4HeaderLen) and Data. It panics if
// Message is not a type ICMPv4Repr represents.
func (r ICMPv4Repr) BufferLen() int {
	r.checkRepresentable()
	return icmpv4HeaderLen + r.Header.HeaderLen() + len(r.Data)
}

// Emit writes the message into packet, which must be exactly BufferLen
// octets long. The checksum is computed when checksums.ICMPv4 computes on
// send and is zero otherwise; the quoted header is written as IPv4Repr.Emit
// writes it, total length and checksum included, and may quote less payload
// than its total length announces. Emit does not check that the message
// quotes 8 octets, so that the stack can answer a datagram with a shorter
// payload. It panics if Message is not a type ICMPv4Repr represents, or if
// the quoted header's addresses are not IPv4.
func (r ICMPv4Repr) Emit(packet ICMPv4Packet, checksums ChecksumCapabilities) {
	r.checkRepresentable()
	packet.SetType(r.Message)
	packet.SetCode(r.Code)
	// Zeroing the unused field, as RFC 792 asks, makes the message independent
	// of what the buffer held.
	clear(packet[icmpv4UnusedOffset:icmpv4HeaderLen])
	quoted := IPv4Packet(packet.Data())
	r.Header.Emit(quoted, checksums)
	copy(quoted[r.Header.HeaderLen():][:len(r.Data)], r.Data)
	if checksums.ICMPv4.OnSend() {
		packet.FillChecksum()
	} else {
		// Make sure we get a consistently zeroed checksum, since
		// implementations might rely on it.
		packet.SetChecksum(0)
	}
}

// checkRepresentable panics unless Message is ICMPv4DstUnreachable, the one
// type ICMPv4Repr can hold.
func (r ICMPv4Repr) checkRepresentable() {
	if r.Message != ICMPv4DstUnreachable {
		panic(fmt.Sprintf("wire: ICMPv4Repr does not represent messages of type %d", uint8(r.Message)))
	}
}
