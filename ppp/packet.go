package ppp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"net/netip"
	"slices"
)

// The packet format LCP, IPCP, PAP and CHAP share (RFC 1661 §5) and the
// option lists carried by Configure packets (RFC 1661 §6).

// PPP protocol numbers (assigned by IANA).
const (
	protocolIPv4 uint16 = 0x0021
	protocolIPCP uint16 = 0x8021
	protocolLCP  uint16 = 0xC021
	protocolPAP  uint16 = 0xC023
	protocolCHAP uint16 = 0xC223
)

// headerLen is the code, identifier and two-octet length in front of a
// control packet's data.
const headerLen = 4

// optionHeaderLen is the type and length octets in front of an option's
// value.
const optionHeaderLen = 2

// The sizes of the fixed-width values options carry, in octets.
const (
	uint16Len = 2
	uint32Len = 4
	ipv4Len   = 4
)

// code is a control packet's Code field. LCP and IPCP share the codes
// below (RFC 1661 §5; IPCP uses codes 1–7 only); PAP and CHAP number their
// packets their own way (auth.go).
type code uint8

// The LCP and IPCP packet codes, with the RFC's own names, Code-Reject
// included.
const (
	codeConfigureRequest code = 1
	codeConfigureAck     code = 2
	codeConfigureNak     code = 3
	codeConfigureReject  code = 4
	codeTerminateRequest code = 5
	codeTerminateAck     code = 6
	codeCodeReject       code = 7
	codeProtocolReject   code = 8
	codeEchoRequest      code = 9
	codeEchoReply        code = 10
	codeDiscardRequest   code = 11
	// codeIdentification is RFC 1570's; sent by Microsoft peers,
	// informational only.
	codeIdentification code = 12
	// codeTimeRemaining is RFC 1570's; informational only.
	codeTimeRemaining code = 13
)

// codeNames are the names of the LCP and IPCP codes, indexed by code.
var codeNames = [...]string{
	codeConfigureRequest: "ConfigureRequest",
	codeConfigureAck:     "ConfigureAck",
	codeConfigureNak:     "ConfigureNak",
	codeConfigureReject:  "ConfigureReject",
	codeTerminateRequest: "TerminateRequest",
	codeTerminateAck:     "TerminateAck",
	codeCodeReject:       "CodeReject",
	codeProtocolReject:   "ProtocolReject",
	codeEchoRequest:      "EchoRequest",
	codeEchoReply:        "EchoReply",
	codeDiscardRequest:   "DiscardRequest",
	codeIdentification:   "Identification",
	codeTimeRemaining:    "TimeRemaining",
}

// String returns the code's name, such as "ConfigureRequest", as "the peer
// does not understand %v" reports it; an unknown code prints as "Code(n)".
func (c code) String() string {
	if int(c) < len(codeNames) && codeNames[c] != "" {
		return codeNames[c]
	}
	return fmt.Sprintf("Code(%d)", uint8(c))
}

// isNegotiation reports whether c is a code every negotiated protocol must
// understand (RFC 1661 §5.1–5.6).
func (c code) isNegotiation() bool {
	return c >= codeConfigureRequest && c <= codeCodeReject
}

// packet is a control packet: code, identifier and the data its length
// field covers. A packet's data is never changed once the packet is built,
// so packets may share it.
type packet struct {
	code       code
	identifier uint8
	data       []byte
}

// parsePacket parses the information field of a control frame, and reports
// false when its length field is inconsistent. Octets past the length field
// are padding and ignored (RFC 1661 §5).
func parsePacket(information []byte) (packet, bool) {
	if len(information) < headerLen {
		return packet{}, false
	}
	length := int(binary.BigEndian.Uint16(information[uint16Len:headerLen]))
	if length < headerLen || length > len(information) {
		return packet{}, false
	}
	return packet{
		code:       code(information[0]),
		identifier: information[1],
		data:       slices.Clone(information[headerLen:length]),
	}, true
}

// encode returns the packet as a frame's information field carries it.
func (p packet) encode() []byte {
	length := headerLen + len(p.data)
	if length > math.MaxUint16 {
		panic("control packets are built far smaller than 64 KiB")
	}
	encoded := make([]byte, 0, length)
	encoded = append(encoded, byte(p.code), p.identifier)
	encoded = binary.BigEndian.AppendUint16(encoded, uint16(length))
	return append(encoded, p.data...)
}

// equal reports whether p and other are the same packet.
func (p packet) equal(other packet) bool {
	return p.code == other.code && p.identifier == other.identifier && bytes.Equal(p.data, other.data)
}

// configOption is one Type-Length-Value configuration option. Its value may
// share the data of the packet it was parsed from, and is never changed.
type configOption struct {
	kind  uint8
	value []byte
}

// uint16Option returns an option carrying a two-octet value.
func uint16Option(kind uint8, value uint16) configOption {
	return configOption{kind: kind, value: binary.BigEndian.AppendUint16(nil, value)}
}

// uint32Option returns an option carrying a four-octet value.
func uint32Option(kind uint8, value uint32) configOption {
	return configOption{kind: kind, value: binary.BigEndian.AppendUint32(nil, value)}
}

// ipv4Option returns an option carrying an IPv4 address, which must be
// valid.
func ipv4Option(kind uint8, address netip.Addr) configOption {
	return configOption{kind: kind, value: address.AsSlice()}
}

// parseOptionList parses an option list, and reports false if any length
// field is inconsistent.
func parseOptionList(data []byte) ([]configOption, bool) {
	var options []configOption
	for len(data) >= optionHeaderLen {
		length := int(data[1])
		if length < optionHeaderLen || length > len(data) {
			return nil, false
		}
		options = append(options, configOption{kind: data[0], value: data[optionHeaderLen:length]})
		data = data[length:]
	}
	return options, len(data) == 0
}

// encodeOptionList returns options as the data of a Configure packet.
func encodeOptionList(options []configOption) []byte {
	var encoded []byte
	for _, option := range options {
		encoded = option.appendTo(encoded)
	}
	return encoded
}

// appendTo appends the encoded option to dst and returns the extended
// slice.
func (o configOption) appendTo(dst []byte) []byte {
	length := optionHeaderLen + len(o.value)
	if length > math.MaxUint8 {
		panic("option values are at most a few octets")
	}
	dst = append(dst, o.kind, uint8(length))
	return append(dst, o.value...)
}

// uint16Value returns the option's two-octet value, and false for a value
// of another size.
func (o configOption) uint16Value() (uint16, bool) {
	if len(o.value) != uint16Len {
		return 0, false
	}
	return binary.BigEndian.Uint16(o.value), true
}

// uint32Value returns the option's four-octet value, and false for a value
// of another size.
func (o configOption) uint32Value() (uint32, bool) {
	if len(o.value) != uint32Len {
		return 0, false
	}
	return binary.BigEndian.Uint32(o.value), true
}

// ipv4Value returns the IPv4 address the option carries; the zero Addr and
// false for a value of another size.
func (o configOption) ipv4Value() (netip.Addr, bool) {
	if len(o.value) != ipv4Len {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([ipv4Len]byte(o.value)), true
}

// outgoingPacket is a control packet waiting to be framed, with the number
// of the protocol it belongs to.
type outgoingPacket struct {
	protocol uint16
	packet   packet
}

// outbox holds the control packets waiting to be framed.
type outbox struct {
	packets []outgoingPacket
}

// push queues p for protocol.
func (o *outbox) push(protocol uint16, p packet) {
	o.packets = append(o.packets, outgoingPacket{protocol: protocol, packet: p})
}

// take returns the waiting packets, oldest first, and empties the outbox.
func (o *outbox) take() []outgoingPacket {
	packets := o.packets
	o.packets = nil
	return packets
}
