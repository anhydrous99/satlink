package wire

import (
	"encoding/binary"
	"fmt"
	"math"
	"net/netip"
)

// IPv4HeaderLen is the length of an IPv4 header without options, the only
// kind the stack emits.
const IPv4HeaderLen = 20

// IPv4MinMTU is the datagram size every IPv4 host must be able to receive,
// in one piece or in fragments (RFC 791 § 3.1). ICMP error messages quote no
// more of the offending datagram than fits into it (RFC 1812 § 4.3.2.3).
const IPv4MinMTU = 576

// DefaultHopLimit is the time-to-live of every datagram the stack sends: the
// value IANA recommends.
const DefaultHopLimit uint8 = 64

// limitedBroadcast is 255.255.255.255, the broadcast address of whatever
// network the sender is on (RFC 919).
var limitedBroadcast = netip.AddrFrom4([4]byte{255, 255, 255, 255})

// pointToPointPrefixLen is the prefix length of a point-to-point link
// (RFC 3021). Such a network, like a single host's /32, has no broadcast
// address.
const pointToPointPrefixLen = 31

// IsBroadcast reports whether addr is the limited broadcast address,
// 255.255.255.255. Subnet broadcasts depend on the interface's addresses; see
// socket.Context.IsBroadcast.
func IsBroadcast(addr netip.Addr) bool {
	return addr == limitedBroadcast
}

// IsUnicast reports whether addr is an IPv4 address that a single host can
// have: neither broadcast, multicast nor unspecified. Non-IPv4 addresses are
// never unicast here.
func IsUnicast(addr netip.Addr) bool {
	return addr.Is4() && !IsBroadcast(addr) && !addr.IsMulticast() && !addr.IsUnspecified()
}

// SubnetBroadcast returns the broadcast address of the IPv4 network prefix
// belongs to, and false for /31 and /32 networks, which have none, and for a
// prefix that is not a valid IPv4 one. The prefix's address need not be masked.
func SubnetBroadcast(prefix netip.Prefix) (netip.Addr, bool) {
	if !prefix.Addr().Is4() || !prefix.IsValid() || prefix.Bits() >= pointToPointPrefixLen {
		return netip.Addr{}, false
	}
	network := prefix.Masked().Addr().As4()
	hostBits := uint32(math.MaxUint32) >> prefix.Bits()
	var broadcast [4]byte
	binary.BigEndian.PutUint32(broadcast[:], binary.BigEndian.Uint32(network[:])|hostBits)
	return netip.AddrFrom4(broadcast), true
}

// ipv4Version is the version field of every datagram the stack handles.
const ipv4Version = 4

// minIPv4HeaderLen is the shortest header the header length field may
// announce: five 32-bit words, a header without options (RFC 791 § 3.1).
const minIPv4HeaderLen = IPv4HeaderLen

// headerWordLen is the unit of IPv4's header length field and of TCP's data
// offset, which both count 32-bit words.
const headerWordLen = 4

// Offsets of the IPv4 header fields (RFC 791 § 3.1).
const (
	ipv4VersionAndIHLOffset = 0 // version, then the header length (IHL)
	ipv4DSCPAndECNOffset    = 1
	ipv4TotalLenOffset      = 2
	ipv4IDOffset            = 4
	ipv4FlagsOffset         = 6 // the flags, then the fragment offset
	ipv4TTLOffset           = 8
	ipv4ProtocolOffset      = 9
	ipv4ChecksumOffset      = 10
	ipv4SrcAddrOffset       = 12
	ipv4DstAddrOffset       = 16
)

// The fields that share an octet or a 16-bit word of the IPv4 header.
const (
	// ipv4VersionShift moves the version, the high nibble of the first
	// octet; the header length is the low nibble, ipv4IHLMask.
	ipv4VersionShift = 4
	ipv4IHLMask      = 0x0f
	// ipv4DSCPShift moves the DSCP, the high six bits of its octet; the ECN
	// is the low two bits, ipv4ECNMask.
	ipv4DSCPShift = 2
	ipv4ECNMask   = 0x03
	// ipv4FlagsMask selects the three flags (a reserved bit, "don't
	// fragment" and "more fragments") above the 13-bit fragment offset.
	ipv4FlagsMask         uint16 = 0xe000
	ipv4DontFragmentFlag  uint16 = 0x4000
	ipv4MoreFragmentsFlag uint16 = 0x2000
	// ipv4FragmentOffsetShift converts between the fragment offset field,
	// which counts 8-octet blocks, and octets.
	ipv4FragmentOffsetShift = 3
)

// IPv4Packet is a read/write view of an IPv4 datagram. Converting a buffer,
// IPv4Packet(buffer), does not check it; NewIPv4Packet does.
type IPv4Packet []byte

// NewIPv4Packet returns buffer as an IPv4Packet after checking, as CheckLen
// does, that no accessor will panic.
func NewIPv4Packet(buffer []byte) (IPv4Packet, error) {
	packet := IPv4Packet(buffer)
	if err := packet.CheckLen(); err != nil {
		return nil, err
	}
	return packet, nil
}

// CheckLen returns ErrMalformed unless the buffer holds the whole header and
// the total length the header announces, and the header length field is
// between the minimum (20) and the total length. Setting the header or total
// length invalidates the check.
func (p IPv4Packet) CheckLen() error {
	// The conditions are evaluated in order, so each may read the fields
	// that the ones before it showed to be there.
	switch {
	case len(p) < IPv4HeaderLen,
		len(p) < p.HeaderLen(),
		p.HeaderLen() > int(p.TotalLen()),
		len(p) < int(p.TotalLen()),
		p.HeaderLen() < minIPv4HeaderLen:
		return ErrMalformed
	default:
		return nil
	}
}

// Version returns the version field.
func (p IPv4Packet) Version() uint8 {
	return p[ipv4VersionAndIHLOffset] >> ipv4VersionShift
}

// HeaderLen returns the header length, in octets.
func (p IPv4Packet) HeaderLen() int {
	return int(p[ipv4VersionAndIHLOffset]&ipv4IHLMask) * headerWordLen
}

// DSCP returns the Differentiated Services Code Point field.
func (p IPv4Packet) DSCP() uint8 {
	return p[ipv4DSCPAndECNOffset] >> ipv4DSCPShift
}

// ECN returns the Explicit Congestion Notification field.
func (p IPv4Packet) ECN() uint8 {
	return p[ipv4DSCPAndECNOffset] & ipv4ECNMask
}

// TotalLen returns the total length field: header and payload, in octets.
func (p IPv4Packet) TotalLen() uint16 {
	return binary.BigEndian.Uint16(p[ipv4TotalLenOffset:])
}

// ID returns the identification field, which groups fragments.
func (p IPv4Packet) ID() uint16 {
	return binary.BigEndian.Uint16(p[ipv4IDOffset:])
}

// DontFragment returns the "don't fragment" flag.
func (p IPv4Packet) DontFragment() bool {
	return p.flagsAndOffset()&ipv4DontFragmentFlag != 0
}

// MoreFragments returns the "more fragments" flag.
func (p IPv4Packet) MoreFragments() bool {
	return p.flagsAndOffset()&ipv4MoreFragmentsFlag != 0
}

// FragmentOffset returns the fragment offset, in octets.
func (p IPv4Packet) FragmentOffset() uint16 {
	// The shift drops the flags above the offset.
	return p.flagsAndOffset() << ipv4FragmentOffsetShift
}

// HopLimit returns the time-to-live field.
func (p IPv4Packet) HopLimit() uint8 {
	return p[ipv4TTLOffset]
}

// NextHeader returns the protocol field.
func (p IPv4Packet) NextHeader() IPProtocol {
	return IPProtocol(p[ipv4ProtocolOffset])
}

// Checksum returns the header checksum field.
func (p IPv4Packet) Checksum() uint16 {
	return binary.BigEndian.Uint16(p[ipv4ChecksumOffset:])
}

// SrcAddr returns the source address field.
func (p IPv4Packet) SrcAddr() netip.Addr {
	return netip.AddrFrom4([ipv4AddrLen]byte(p[ipv4SrcAddrOffset:]))
}

// DstAddr returns the destination address field.
func (p IPv4Packet) DstAddr() netip.Addr {
	return netip.AddrFrom4([ipv4AddrLen]byte(p[ipv4DstAddrOffset:]))
}

// VerifyChecksum reports whether the header checksum is correct.
func (p IPv4Packet) VerifyChecksum() bool {
	return dataChecksum(p[:p.HeaderLen()]) == correctChecksumSum
}

// Payload returns the octets between the header and the total length; it
// shares the packet's storage, so writing to it fills in the payload.
func (p IPv4Packet) Payload() []byte {
	end := int(p.TotalLen())
	return p[p.HeaderLen():end:end]
}

// SetVersion sets the version field.
func (p IPv4Packet) SetVersion(version uint8) {
	p[ipv4VersionAndIHLOffset] = p[ipv4VersionAndIHLOffset]&ipv4IHLMask | version<<ipv4VersionShift
}

// SetHeaderLen sets the header length, in octets (a multiple of 4).
func (p IPv4Packet) SetHeaderLen(length int) {
	words := byte(length/headerWordLen) & ipv4IHLMask
	p[ipv4VersionAndIHLOffset] = p[ipv4VersionAndIHLOffset]&^ipv4IHLMask | words
}

// SetDSCP sets the Differentiated Services Code Point field.
func (p IPv4Packet) SetDSCP(dscp uint8) {
	p[ipv4DSCPAndECNOffset] = p[ipv4DSCPAndECNOffset]&ipv4ECNMask | dscp<<ipv4DSCPShift
}

// SetECN sets the Explicit Congestion Notification field.
func (p IPv4Packet) SetECN(ecn uint8) {
	p[ipv4DSCPAndECNOffset] = p[ipv4DSCPAndECNOffset]&^ipv4ECNMask | ecn&ipv4ECNMask
}

// SetTotalLen sets the total length field.
func (p IPv4Packet) SetTotalLen(length uint16) {
	binary.BigEndian.PutUint16(p[ipv4TotalLenOffset:], length)
}

// SetID sets the identification field.
func (p IPv4Packet) SetID(id uint16) {
	binary.BigEndian.PutUint16(p[ipv4IDOffset:], id)
}

// ClearFlags clears the flags, leaving the fragment offset.
func (p IPv4Packet) ClearFlags() {
	p.setFlagsAndOffset(p.flagsAndOffset() &^ ipv4FlagsMask)
}

// SetDontFragment sets the "don't fragment" flag.
func (p IPv4Packet) SetDontFragment(dontFragment bool) {
	p.setFlag(ipv4DontFragmentFlag, dontFragment)
}

// SetMoreFragments sets the "more fragments" flag.
func (p IPv4Packet) SetMoreFragments(moreFragments bool) {
	p.setFlag(ipv4MoreFragmentsFlag, moreFragments)
}

// SetFragmentOffset sets the fragment offset, in octets (a multiple of 8).
func (p IPv4Packet) SetFragmentOffset(offset uint16) {
	p.setFlagsAndOffset(p.flagsAndOffset()&ipv4FlagsMask | offset>>ipv4FragmentOffsetShift)
}

// SetHopLimit sets the time-to-live field.
func (p IPv4Packet) SetHopLimit(hopLimit uint8) {
	p[ipv4TTLOffset] = hopLimit
}

// SetNextHeader sets the protocol field.
func (p IPv4Packet) SetNextHeader(protocol IPProtocol) {
	p[ipv4ProtocolOffset] = byte(protocol)
}

// SetChecksum sets the header checksum field.
func (p IPv4Packet) SetChecksum(checksum uint16) {
	binary.BigEndian.PutUint16(p[ipv4ChecksumOffset:], checksum)
}

// SetSrcAddr sets the source address field. It panics unless addr is IPv4
// (Is4).
func (p IPv4Packet) SetSrcAddr(addr netip.Addr) {
	octets := ipv4Octets(addr)
	copy(p[ipv4SrcAddrOffset:], octets[:])
}

// SetDstAddr sets the destination address field. It panics unless addr is
// IPv4 (Is4).
func (p IPv4Packet) SetDstAddr(addr netip.Addr) {
	octets := ipv4Octets(addr)
	copy(p[ipv4DstAddrOffset:], octets[:])
}

// FillChecksum computes the header checksum and writes it to the header.
func (p IPv4Packet) FillChecksum() {
	p.SetChecksum(0)
	p.SetChecksum(^dataChecksum(p[:p.HeaderLen()]))
}

func (p IPv4Packet) flagsAndOffset() uint16 {
	return binary.BigEndian.Uint16(p[ipv4FlagsOffset:])
}

func (p IPv4Packet) setFlagsAndOffset(flagsAndOffset uint16) {
	binary.BigEndian.PutUint16(p[ipv4FlagsOffset:], flagsAndOffset)
}

// setFlag sets or clears one of the flags, given as its bit.
func (p IPv4Packet) setFlag(flag uint16, value bool) {
	if value {
		p.setFlagsAndOffset(p.flagsAndOffset() | flag)
	} else {
		p.setFlagsAndOffset(p.flagsAndOffset() &^ flag)
	}
}

// IPv4Repr is a high-level representation of an IPv4 header: everything the
// stack reads from or writes into one.
type IPv4Repr struct {
	// SrcAddr is the source address, an IPv4 address (Is4).
	SrcAddr netip.Addr
	// DstAddr is the destination address, an IPv4 address (Is4).
	DstAddr netip.Addr
	// NextHeader is the protocol of the payload: the IPv4 protocol field.
	NextHeader IPProtocol
	// PayloadLen is the length of what follows the header, in octets.
	PayloadLen int
	// HopLimit is the time-to-live: the TTL of a received datagram, and
	// DefaultHopLimit on every datagram the stack sends.
	HopLimit uint8
}

// ParseIPv4Repr checks packet and returns its header. It fails with
// ErrMalformed for a packet that fails CheckLen, is not version 4, has a bad
// checksum (when checksums.IPv4 verifies on receive), or is a fragment: the
// stack does not reassemble fragments.
func ParseIPv4Repr(packet IPv4Packet, checksums ChecksumCapabilities) (IPv4Repr, error) {
	if err := packet.CheckLen(); err != nil {
		return IPv4Repr{}, err
	}
	if packet.Version() != ipv4Version {
		return IPv4Repr{}, ErrMalformed
	}
	if checksums.IPv4.OnReceive() && !packet.VerifyChecksum() {
		return IPv4Repr{}, ErrMalformed
	}
	// We do not support fragmentation.
	if packet.MoreFragments() || packet.FragmentOffset() != 0 {
		return IPv4Repr{}, ErrMalformed
	}
	// All DSCP values are acceptable, since they are of no concern to the
	// receiving endpoint. All ECN values are acceptable, since ECN requires
	// opt-in from both endpoints. All TTL values are acceptable, since we do
	// not perform routing.
	return IPv4Repr{
		SrcAddr:    packet.SrcAddr(),
		DstAddr:    packet.DstAddr(),
		NextHeader: packet.NextHeader(),
		PayloadLen: int(packet.TotalLen()) - packet.HeaderLen(),
		HopLimit:   packet.HopLimit(),
	}, nil
}

// HeaderLen returns the length of the header Emit writes; the stack never
// emits options.
func (r IPv4Repr) HeaderLen() int {
	return IPv4HeaderLen
}

// BufferLen returns the length of the whole datagram: header and payload.
func (r IPv4Repr) BufferLen() int {
	return r.HeaderLen() + r.PayloadLen
}

// Emit writes the header into packet: version 4, no options, DSCP and ECN
// 0, identification 0, "don't fragment" set, a total length of HeaderLen +
// PayloadLen, and the checksum when checksums.IPv4 computes on send (zero
// otherwise). packet must be at least HeaderLen octets long; the payload is
// the caller's to write. Emit panics unless both addresses are IPv4 (Is4).
func (r IPv4Repr) Emit(packet IPv4Packet, checksums ChecksumCapabilities) {
	packet.SetVersion(ipv4Version)
	packet.SetHeaderLen(r.HeaderLen())
	packet.SetDSCP(0)
	packet.SetECN(0)
	packet.SetTotalLen(uint16(r.BufferLen()))
	packet.SetID(0)
	packet.ClearFlags()
	packet.SetMoreFragments(false)
	packet.SetDontFragment(true)
	packet.SetFragmentOffset(0)
	packet.SetHopLimit(r.HopLimit)
	packet.SetNextHeader(r.NextHeader)
	packet.SetSrcAddr(r.SrcAddr)
	packet.SetDstAddr(r.DstAddr)
	if checksums.IPv4.OnSend() {
		packet.FillChecksum()
	} else {
		// Make sure we get a consistently zeroed checksum, since
		// implementations might rely on it.
		packet.SetChecksum(0)
	}
}

// String formats the header as "IPv4 src=… dst=… proto=…".
func (r IPv4Repr) String() string {
	return fmt.Sprintf("IPv4 src=%v dst=%v proto=%v", r.SrcAddr, r.DstAddr, r.NextHeader)
}
