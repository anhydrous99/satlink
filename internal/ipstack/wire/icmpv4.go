package wire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
)

// ICMPv4Message is the type field of an ICMPv4 message. Types without a
// constant here are valid on the wire; ParseICMPv4Repr rejects them.
type ICMPv4Message uint8

// The message types the stack parses or sends (RFC 792).
const (
	// ICMPv4EchoReply answers an echo request.
	ICMPv4EchoReply ICMPv4Message = 0
	// ICMPv4DstUnreachable reports a datagram that could not be delivered;
	// its code says why.
	ICMPv4DstUnreachable ICMPv4Message = 3
	// ICMPv4EchoRequest asks for an echo reply (a ping).
	ICMPv4EchoRequest ICMPv4Message = 8
	// ICMPv4TimeExceeded reports a datagram whose time-to-live ran out.
	ICMPv4TimeExceeded ICMPv4Message = 11
)

// The message types smoltcp names although the stack neither parses nor
// sends them (RFC 792, RFC 1256). They matter only for how a message of one
// of these types is shown.
const (
	icmpv4Redirect       ICMPv4Message = 5
	icmpv4RouterAdvert   ICMPv4Message = 9
	icmpv4RouterSolicit  ICMPv4Message = 10
	icmpv4ParamProblem   ICMPv4Message = 12
	icmpv4Timestamp      ICMPv4Message = 13
	icmpv4TimestampReply ICMPv4Message = 14
)

// icmpv4MessageNames are the descriptions smoltcp's Display gives the
// message types it knows.
var icmpv4MessageNames = map[ICMPv4Message]string{
	ICMPv4EchoReply:      "echo reply",
	ICMPv4DstUnreachable: "destination unreachable",
	icmpv4Redirect:       "message redirect",
	ICMPv4EchoRequest:    "echo request",
	icmpv4RouterAdvert:   "router advertisement",
	icmpv4RouterSolicit:  "router solicitation",
	ICMPv4TimeExceeded:   "time exceeded",
	icmpv4ParamProblem:   "parameter problem",
	icmpv4Timestamp:      "timestamp",
	icmpv4TimestampReply: "timestamp reply",
}

// String describes the message type as smoltcp does ("echo request"), or
// gives its number when smoltcp does not know it.
func (m ICMPv4Message) String() string {
	if name, ok := icmpv4MessageNames[m]; ok {
		return name
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
// handles, and smoltcp's conservative assumption for every other: the type,
// the code and the checksum, then four octets whose meaning depends on the
// type (RFC 792).
const icmpv4HeaderLen = 8

// Offsets of the ICMPv4 header fields (RFC 792).
const (
	icmpv4TypeOffset     = 0
	icmpv4CodeOffset     = 1
	icmpv4ChecksumOffset = 2
	// icmpv4UnusedOffset starts an error message's unused field, which runs
	// to the end of the header.
	icmpv4UnusedOffset         = 4
	icmpv4EchoIdentifierOffset = 4
	icmpv4EchoSeqNumberOffset  = 6
)

// icmpv4EchoCode is the code of every echo request and reply.
const icmpv4EchoCode = 0

// icmpv4MinQuotedPayloadLen is how much of the payload of the datagram an
// error message is about it must quote. RFC 792 requires exactly eight
// octets; smoltcp allows more, since there isn't a reason not to, but
// requires at least eight.
const icmpv4MinQuotedPayloadLen = 8

// ICMPv4Packet is a read/write view of an ICMPv4 message. Converting a
// buffer, ICMPv4Packet(buffer), does not check it; NewICMPv4Packet does.
type ICMPv4Packet []byte

// NewICMPv4Packet returns buffer as an ICMPv4Packet after checking, as
// CheckLen does, that no accessor will panic.
func NewICMPv4Packet(buffer []byte) (ICMPv4Packet, error) {
	packet := ICMPv4Packet(buffer)
	if err := packet.CheckLen(); err != nil {
		return nil, err
	}
	return packet, nil
}

// CheckLen returns ErrMalformed unless the buffer holds the 8-octet header.
func (p ICMPv4Packet) CheckLen() error {
	if len(p) < icmpv4HeaderLen {
		return ErrMalformed
	}
	return nil
}

// Type returns the message type field.
func (p ICMPv4Packet) Type() ICMPv4Message {
	return ICMPv4Message(p[icmpv4TypeOffset])
}

// Code returns the message code field.
func (p ICMPv4Packet) Code() uint8 {
	return p[icmpv4CodeOffset]
}

// Checksum returns the checksum field.
func (p ICMPv4Packet) Checksum() uint16 {
	return binary.BigEndian.Uint16(p[icmpv4ChecksumOffset:])
}

// EchoIdentifier returns the identifier of an echo request or reply.
func (p ICMPv4Packet) EchoIdentifier() uint16 {
	return binary.BigEndian.Uint16(p[icmpv4EchoIdentifierOffset:])
}

// EchoSeqNumber returns the sequence number of an echo request or reply.
func (p ICMPv4Packet) EchoSeqNumber() uint16 {
	return binary.BigEndian.Uint16(p[icmpv4EchoSeqNumberOffset:])
}

// HeaderLen returns the length of the header, which depends on the message
// type; it is 8 for every type the stack handles.
func (p ICMPv4Packet) HeaderLen() int {
	return icmpv4HeaderLen
}

// VerifyChecksum reports whether the checksum over the whole message is
// correct.
func (p ICMPv4Packet) VerifyChecksum() bool {
	return dataChecksum(p) == correctChecksumSum
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

// SetEchoIdentifier sets the identifier of an echo request or reply.
func (p ICMPv4Packet) SetEchoIdentifier(identifier uint16) {
	binary.BigEndian.PutUint16(p[icmpv4EchoIdentifierOffset:], identifier)
}

// SetEchoSeqNumber sets the sequence number of an echo request or reply.
func (p ICMPv4Packet) SetEchoSeqNumber(number uint16) {
	binary.BigEndian.PutUint16(p[icmpv4EchoSeqNumberOffset:], number)
}

// FillChecksum computes the checksum over the whole message and writes it to
// the header.
func (p ICMPv4Packet) FillChecksum() {
	p.SetChecksum(0)
	p.SetChecksum(^dataChecksum(p))
}

// ICMPv4Repr is a high-level representation of the ICMPv4 messages the stack
// understands. Message says which one it is, and the fields below say which
// messages use them.
//
// It is not comparable with == because of its data; use Equal.
type ICMPv4Repr struct {
	// Message is ICMPv4EchoRequest, ICMPv4EchoReply, ICMPv4DstUnreachable or
	// ICMPv4TimeExceeded.
	Message ICMPv4Message
	// Identifier identifies the exchange an echo request or reply belongs
	// to.
	Identifier uint16
	// SeqNumber numbers an echo request, and the reply that answers it,
	// within that exchange.
	SeqNumber uint16
	// Code is the reason for an error message, such as
	// ICMPv4PortUnreachable.
	Code uint8
	// Header is the IPv4 header of the datagram an error message is about,
	// unchanged: when the stack builds an error, PayloadLen is that
	// datagram's full payload length, which Emit writes into the quoted
	// total-length field, however little of the payload Data quotes.
	// ParseICMPv4Repr sets PayloadLen to len(Data), as smoltcp does.
	Header IPv4Repr
	// Data is the payload of an echo message, or the start of the payload of
	// the datagram an error message is about. ParseICMPv4Repr requires at
	// least 8 octets of it (RFC 792); the stack quotes at most 528 octets,
	// so that the error fits into IPv4MinMTU, and possibly none.
	Data []byte
}

// ParseICMPv4Repr checks packet and returns its representation. It fails
// with ErrMalformed when the packet fails CheckLen, the checksum is bad
// (when checksums.ICMPv4 verifies on receive), an echo message has a non-zero
// code, an error message quotes a malformed header or fewer than 8 payload
// octets, or the message type is not one ICMPv4Repr represents. The data of
// the representation shares the packet's storage.
func ParseICMPv4Repr(packet ICMPv4Packet, checksums ChecksumCapabilities) (ICMPv4Repr, error) {
	if err := packet.CheckLen(); err != nil {
		return ICMPv4Repr{}, err
	}
	if checksums.ICMPv4.OnReceive() && !packet.VerifyChecksum() {
		return ICMPv4Repr{}, ErrMalformed
	}
	switch packet.Type() {
	case ICMPv4EchoRequest, ICMPv4EchoReply:
		return parseICMPv4Echo(packet)
	case ICMPv4DstUnreachable, ICMPv4TimeExceeded:
		return parseICMPv4Error(packet)
	default:
		return ICMPv4Repr{}, ErrMalformed
	}
}

// parseICMPv4Echo parses an echo request or reply, which must have code 0.
func parseICMPv4Echo(packet ICMPv4Packet) (ICMPv4Repr, error) {
	if packet.Code() != icmpv4EchoCode {
		return ICMPv4Repr{}, ErrMalformed
	}
	return ICMPv4Repr{
		Message:    packet.Type(),
		Identifier: packet.EchoIdentifier(),
		SeqNumber:  packet.EchoSeqNumber(),
		Data:       packet.Data(),
	}, nil
}

// parseICMPv4Error parses a destination-unreachable or time-exceeded
// message: the header of the datagram it is about, which must pass
// IPv4Packet.CheckLen, then at least 8 octets of that datagram's payload.
func parseICMPv4Error(packet ICMPv4Packet) (ICMPv4Repr, error) {
	quoted, err := NewIPv4Packet(packet.Data())
	if err != nil {
		return ICMPv4Repr{}, err
	}
	payload := packet.Data()[quoted.HeaderLen():]
	if len(payload) < icmpv4MinQuotedPayloadLen {
		return ICMPv4Repr{}, ErrMalformed
	}
	return ICMPv4Repr{
		Message: packet.Type(),
		Code:    packet.Code(),
		Header: IPv4Repr{
			SrcAddr:    quoted.SrcAddr(),
			DstAddr:    quoted.DstAddr(),
			NextHeader: quoted.NextHeader(),
			PayloadLen: len(payload),
			HopLimit:   quoted.HopLimit(),
		},
		Data: payload,
	}, nil
}

// BufferLen returns the length of the message Emit writes: the 8-octet
// header and Data for an echo message; the 8-octet header, the quoted IPv4
// header (IPv4HeaderLen) and Data for an error message. It panics if Message
// is not a type ICMPv4Repr represents.
func (r ICMPv4Repr) BufferLen() int {
	switch {
	case r.isEcho():
		return icmpv4HeaderLen + len(r.Data)
	case r.isError():
		return icmpv4HeaderLen + r.Header.HeaderLen() + len(r.Data)
	default:
		panic(r.unrepresentable())
	}
}

// Emit writes the message into packet, which must be exactly BufferLen
// octets long. The checksum is computed when checksums.ICMPv4 computes on
// send and is zero otherwise; the quoted header of an error message is
// written as IPv4Repr.Emit writes it, total length and checksum included, and
// may quote less payload than its total length announces. Emit does not
// check that an error message quotes 8 octets, so that the stack can answer
// a datagram with a shorter payload. It panics if Message is not a type
// ICMPv4Repr represents, or if the quoted header's addresses are not IPv4.
func (r ICMPv4Repr) Emit(packet ICMPv4Packet, checksums ChecksumCapabilities) {
	switch {
	case r.isEcho():
		r.emitEcho(packet)
	case r.isError():
		r.emitError(packet, checksums)
	default:
		panic(r.unrepresentable())
	}
	if checksums.ICMPv4.OnSend() {
		packet.FillChecksum()
	} else {
		// Make sure we get a consistently zeroed checksum, since
		// implementations might rely on it.
		packet.SetChecksum(0)
	}
}

// emitEcho writes an echo request or reply, but its checksum.
func (r ICMPv4Repr) emitEcho(packet ICMPv4Packet) {
	packet.SetType(r.Message)
	packet.SetCode(icmpv4EchoCode)
	packet.SetEchoIdentifier(r.Identifier)
	packet.SetEchoSeqNumber(r.SeqNumber)
	copy(packet.Data(), r.Data)
}

// emitError writes a destination-unreachable or time-exceeded message, but
// its checksum.
func (r ICMPv4Repr) emitError(packet ICMPv4Packet, checksums ChecksumCapabilities) {
	packet.SetType(r.Message)
	packet.SetCode(r.Code)
	// smoltcp leaves the unused field as the buffer had it; zeroing it, as
	// RFC 792 asks, makes the message independent of what the buffer held,
	// as it is in smoltcp's zeroed buffers.
	clear(packet[icmpv4UnusedOffset:icmpv4HeaderLen])
	quoted := IPv4Packet(packet.Data())
	r.Header.Emit(quoted, checksums)
	copy(quoted[r.Header.HeaderLen():][:len(r.Data)], r.Data)
}

// Equal reports whether r and other describe the same message, data octets
// included.
func (r ICMPv4Repr) Equal(other ICMPv4Repr) bool {
	return r.Message == other.Message &&
		r.Identifier == other.Identifier &&
		r.SeqNumber == other.SeqNumber &&
		r.Code == other.Code &&
		r.Header == other.Header &&
		bytes.Equal(r.Data, other.Data)
}

func (r ICMPv4Repr) isEcho() bool {
	return r.Message == ICMPv4EchoRequest || r.Message == ICMPv4EchoReply
}

func (r ICMPv4Repr) isError() bool {
	return r.Message == ICMPv4DstUnreachable || r.Message == ICMPv4TimeExceeded
}

// unrepresentable is the panic message for a Repr whose Message is none of
// the four that smoltcp's Repr can hold.
func (r ICMPv4Repr) unrepresentable() string {
	return fmt.Sprintf("wire: ICMPv4Repr does not represent %v (type %d) messages", r.Message, uint8(r.Message))
}
