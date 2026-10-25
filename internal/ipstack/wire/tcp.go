package wire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
)

// TCPHeaderLen is the length of a TCP header without options.
const TCPHeaderLen = 20

// MaxSACKRanges is how many SACK blocks the stack keeps from, and sends in, a
// segment: an option holding four would leave no room for timestamps, so
// RFC 2018 expects at most three in practice, and the stack drops a fourth.
const MaxSACKRanges = 3

// tcpOptionsAlignment is the multiple of octets a TCP header length must be.
const tcpOptionsAlignment = headerWordLen

// maxWindowScale is the largest window scale shift count (RFC 7323 § 2.3).
const maxWindowScale = 14

// Offsets of the TCP header fields (RFC 9293 § 3.1).
const (
	tcpSrcPortOffset   = 0
	tcpDstPortOffset   = 2
	tcpSeqNumberOffset = 4
	tcpAckNumberOffset = 8
	tcpFlagsOffset     = 12 // the data offset, then the reserved bits and the flags
	tcpWindowOffset    = 14
	tcpChecksumOffset  = 16
	tcpUrgentOffset    = 18
)

// The bits of the 16-bit word that holds the data offset and the flags.
const (
	// tcpDataOffsetShift moves the data offset, the high four bits: the
	// header length in 32-bit words.
	tcpDataOffsetShift        = 12
	tcpDataOffsetMask  uint16 = 0xf000
	// tcpFlagsMask selects the flags and the reserved bits beside them.
	tcpFlagsMask uint16 = 0x0fff

	tcpFlagFIN uint16 = 0x001
	tcpFlagSYN uint16 = 0x002
	tcpFlagRST uint16 = 0x004
	tcpFlagPSH uint16 = 0x008
	tcpFlagACK uint16 = 0x010
)

// TCPControl is the control flag of a segment. A segment the stack
// understands carries at most one of SYN, FIN and RST; PSH is reported only
// when none of them is set.
type TCPControl int

const (
	// TCPControlNone is a segment with no control flag.
	TCPControlNone TCPControl = iota
	// TCPControlPSH is a segment with the PSH flag only.
	TCPControlPSH
	// TCPControlSYN opens a connection.
	TCPControlSYN
	// TCPControlFIN closes one direction of a connection.
	TCPControlFIN
	// TCPControlRST resets a connection.
	TCPControlRST
)

// Len returns the length of the flag in sequence space: 1 for SYN and FIN,
// which each occupy a sequence number, and 0 otherwise.
func (c TCPControl) Len() int {
	if c == TCPControlSYN || c == TCPControlFIN {
		return 1
	}
	return 0
}

// QuashPSH returns TCPControlNone for TCPControlPSH and c otherwise: the
// stack ignores PSH on received segments.
func (c TCPControl) QuashPSH() TCPControl {
	if c == TCPControlPSH {
		return TCPControlNone
	}
	return c
}

// String returns the variant name ("Syn").
func (c TCPControl) String() string {
	switch c {
	case TCPControlNone:
		return "None"
	case TCPControlPSH:
		return "Psh"
	case TCPControlSYN:
		return "Syn"
	case TCPControlFIN:
		return "Fin"
	case TCPControlRST:
		return "Rst"
	default:
		return "TCPControl(" + strconv.Itoa(int(c)) + ")"
	}
}

// flagName is how the control flag is shown in a segment.
func (c TCPControl) flagName() string {
	switch c {
	case TCPControlSYN:
		return "syn"
	case TCPControlFIN:
		return "fin"
	case TCPControlRST:
		return "rst"
	case TCPControlPSH:
		return "psh"
	default:
		return ""
	}
}

// SACKRange is one block of a selective acknowledgement (RFC 2018): the
// sequence numbers, as on the wire, of the first octet received and of the
// octet just after the last.
type SACKRange struct {
	// Left is the sequence number of the block's first octet.
	Left uint32
	// Right is the sequence number just after the block's last octet.
	Right uint32
}

// TCPTimestamp is the content of a TCP timestamps option (RFC 7323).
type TCPTimestamp struct {
	// Value is TSval, the sender's timestamp clock.
	Value uint32
	// EchoReply is TSecr, the TSval most recently received from the peer.
	EchoReply uint32
}

// TCPPacket is a read/write view of a TCP segment. Converting a buffer,
// TCPPacket(buffer), does not check it; NewTCPPacket does.
type TCPPacket []byte

// NewTCPPacket returns buffer as a TCPPacket after checking, as CheckLen does,
// that no accessor will panic.
func NewTCPPacket(buffer []byte) (TCPPacket, error) {
	packet := TCPPacket(buffer)
	if err := packet.CheckLen(); err != nil {
		return nil, err
	}
	return packet, nil
}

// CheckLen returns ErrMalformed unless the buffer holds the fixed header and
// the header length field is between 20 and the buffer's length. Setting the
// header length invalidates the check.
func (p TCPPacket) CheckLen() error {
	if len(p) < TCPHeaderLen {
		return ErrMalformed
	}
	if headerLen := p.HeaderLen(); len(p) < headerLen || headerLen < TCPHeaderLen {
		return ErrMalformed
	}
	return nil
}

// SrcPort returns the source port field.
func (p TCPPacket) SrcPort() uint16 {
	return binary.BigEndian.Uint16(p[tcpSrcPortOffset:])
}

// DstPort returns the destination port field.
func (p TCPPacket) DstPort() uint16 {
	return binary.BigEndian.Uint16(p[tcpDstPortOffset:])
}

// SeqNumber returns the sequence number field.
func (p TCPPacket) SeqNumber() TCPSeqNumber {
	return TCPSeqNumber(binary.BigEndian.Uint32(p[tcpSeqNumberOffset:]))
}

// AckNumber returns the acknowledgement number field, which means something
// only when the ACK flag is set.
func (p TCPPacket) AckNumber() TCPSeqNumber {
	return TCPSeqNumber(binary.BigEndian.Uint32(p[tcpAckNumberOffset:]))
}

// FIN returns the FIN flag.
func (p TCPPacket) FIN() bool {
	return p.hasFlag(tcpFlagFIN)
}

// SYN returns the SYN flag.
func (p TCPPacket) SYN() bool {
	return p.hasFlag(tcpFlagSYN)
}

// RST returns the RST flag.
func (p TCPPacket) RST() bool {
	return p.hasFlag(tcpFlagRST)
}

// PSH returns the PSH flag.
func (p TCPPacket) PSH() bool {
	return p.hasFlag(tcpFlagPSH)
}

// ACK returns the ACK flag.
func (p TCPPacket) ACK() bool {
	return p.hasFlag(tcpFlagACK)
}

// HeaderLen returns the header length, options included, in octets.
func (p TCPPacket) HeaderLen() int {
	return int(p.dataOffsetAndFlags()>>tcpDataOffsetShift) * headerWordLen
}

// WindowLen returns the window size field.
func (p TCPPacket) WindowLen() uint16 {
	return binary.BigEndian.Uint16(p[tcpWindowOffset:])
}

// Checksum returns the checksum field. The stack checks it with
// VerifyChecksum; the getter stays for the tests, which pin, corrupt and
// rewrite the field.
func (p TCPPacket) Checksum() uint16 {
	return binary.BigEndian.Uint16(p[tcpChecksumOffset:])
}

// Options returns the options octets between the fixed header and the payload.
func (p TCPPacket) Options() []byte {
	end := p.HeaderLen()
	return p[TCPHeaderLen:end:end]
}

// Payload returns the octets after the header; it shares the packet's
// storage, so writing to it fills in the payload.
func (p TCPPacket) Payload() []byte {
	return p[p.HeaderLen():]
}

// VerifyChecksum reports whether the checksum is correct for a segment sent
// from src to dst. It panics unless both addresses are IPv4 (Is4).
func (p TCPPacket) VerifyChecksum(src, dst netip.Addr) bool {
	return transportChecksum(src, dst, IPProtocolTCP, p) == correctChecksumSum
}

// SetSrcPort sets the source port field.
func (p TCPPacket) SetSrcPort(port uint16) {
	binary.BigEndian.PutUint16(p[tcpSrcPortOffset:], port)
}

// SetDstPort sets the destination port field.
func (p TCPPacket) SetDstPort(port uint16) {
	binary.BigEndian.PutUint16(p[tcpDstPortOffset:], port)
}

// SetSeqNumber sets the sequence number field.
func (p TCPPacket) SetSeqNumber(number TCPSeqNumber) {
	binary.BigEndian.PutUint32(p[tcpSeqNumberOffset:], uint32(number))
}

// SetAckNumber sets the acknowledgement number field.
func (p TCPPacket) SetAckNumber(number TCPSeqNumber) {
	binary.BigEndian.PutUint32(p[tcpAckNumberOffset:], uint32(number))
}

// ClearFlags clears every flag, leaving the header length.
func (p TCPPacket) ClearFlags() {
	p.setDataOffsetAndFlags(p.dataOffsetAndFlags() &^ tcpFlagsMask)
}

// SetFIN sets the FIN flag.
func (p TCPPacket) SetFIN(set bool) {
	p.setFlag(tcpFlagFIN, set)
}

// SetSYN sets the SYN flag.
func (p TCPPacket) SetSYN(set bool) {
	p.setFlag(tcpFlagSYN, set)
}

// SetRST sets the RST flag.
func (p TCPPacket) SetRST(set bool) {
	p.setFlag(tcpFlagRST, set)
}

// SetPSH sets the PSH flag.
func (p TCPPacket) SetPSH(set bool) {
	p.setFlag(tcpFlagPSH, set)
}

// SetACK sets the ACK flag.
func (p TCPPacket) SetACK(set bool) {
	p.setFlag(tcpFlagACK, set)
}

// SetHeaderLen sets the header length, options included, in octets (a
// multiple of 4).
func (p TCPPacket) SetHeaderLen(length int) {
	dataOffset := uint16(length/headerWordLen) << tcpDataOffsetShift
	p.setDataOffsetAndFlags(p.dataOffsetAndFlags()&^tcpDataOffsetMask | dataOffset)
}

// SetWindowLen sets the window size field.
func (p TCPPacket) SetWindowLen(window uint16) {
	binary.BigEndian.PutUint16(p[tcpWindowOffset:], window)
}

// SetChecksum sets the checksum field.
func (p TCPPacket) SetChecksum(checksum uint16) {
	binary.BigEndian.PutUint16(p[tcpChecksumOffset:], checksum)
}

// SetUrgentPointer sets the urgent pointer field.
func (p TCPPacket) SetUrgentPointer(pointer uint16) {
	binary.BigEndian.PutUint16(p[tcpUrgentOffset:], pointer)
}

// FillChecksum computes the checksum for a segment sent from src to dst and
// writes it to the header. It panics unless both addresses are IPv4 (Is4).
func (p TCPPacket) FillChecksum(src, dst netip.Addr) {
	p.SetChecksum(0)
	p.SetChecksum(^transportChecksum(src, dst, IPProtocolTCP, p))
}

func (p TCPPacket) dataOffsetAndFlags() uint16 {
	return binary.BigEndian.Uint16(p[tcpFlagsOffset:])
}

func (p TCPPacket) setDataOffsetAndFlags(word uint16) {
	binary.BigEndian.PutUint16(p[tcpFlagsOffset:], word)
}

func (p TCPPacket) hasFlag(flag uint16) bool {
	return p.dataOffsetAndFlags()&flag != 0
}

// setFlag sets or clears one flag, given as its bit.
func (p TCPPacket) setFlag(flag uint16, value bool) {
	if value {
		p.setDataOffsetAndFlags(p.dataOffsetAndFlags() | flag)
	} else {
		p.setDataOffsetAndFlags(p.dataOffsetAndFlags() &^ flag)
	}
}

// control returns the segment's control flag, and ErrMalformed when more
// than one of SYN, FIN and RST is set.
func (p TCPPacket) control() (TCPControl, error) {
	switch p.dataOffsetAndFlags() & (tcpFlagSYN | tcpFlagFIN | tcpFlagRST) {
	case 0:
		if p.PSH() {
			return TCPControlPSH, nil
		}
		return TCPControlNone, nil
	case tcpFlagSYN:
		return TCPControlSYN, nil
	case tcpFlagFIN:
		return TCPControlFIN, nil
	case tcpFlagRST:
		return TCPControlRST, nil
	default:
		return TCPControlNone, ErrMalformed
	}
}

// setControl sets the flag that control stands for; the others must be
// clear already.
func (p TCPPacket) setControl(control TCPControl) {
	switch control {
	case TCPControlPSH:
		p.SetPSH(true)
	case TCPControlSYN:
		p.SetSYN(true)
	case TCPControlFIN:
		p.SetFIN(true)
	case TCPControlRST:
		p.SetRST(true)
	}
}

// TCPRepr is a high-level representation of a TCP segment.
//
// It is not comparable with == because of its payload; use Equal.
type TCPRepr struct {
	// SrcPort is the source port.
	SrcPort uint16
	// DstPort is the destination port.
	DstPort uint16
	// Control is the segment's control flag: SYN, FIN or RST, PSH alone, or
	// none.
	Control TCPControl
	// SeqNumber is the sequence number of the first octet of the segment
	// (or of the SYN).
	SeqNumber TCPSeqNumber
	// AckNumber is present exactly when the ACK flag is set.
	AckNumber Optional[TCPSeqNumber]
	// WindowLen is the window field as sent, not scaled.
	WindowLen uint16
	// WindowScale is the window scale option (RFC 7323); parsing caps it at 14.
	WindowScale Optional[uint8]
	// MaxSegmentSize is the MSS option.
	MaxSegmentSize Optional[uint16]
	// SACKPermitted is the SACK-permitted option (RFC 2018).
	SACKPermitted bool
	// SACKRanges are the blocks of a SACK option, in the order sent; Emit
	// writes them only on a segment with an acknowledgement and without
	// SACKPermitted.
	SACKRanges [MaxSACKRanges]Optional[SACKRange]
	// Timestamp is the timestamps option (RFC 7323).
	Timestamp Optional[TCPTimestamp]
	// Payload is the segment's data. A parsed Repr's payload shares the
	// packet's storage.
	Payload []byte
}

// ParseTCPRepr checks packet, a segment sent from src to dst, and returns its
// representation. It fails with ErrMalformed when the packet fails CheckLen,
// either port is 0, the checksum is bad (when checksums.TCP verifies on
// receive), more than one of SYN, FIN and RST is set, or an option is
// malformed. The PSH flag is reported only as TCPControlPSH on a segment
// without other flags; URG and the urgent pointer are ignored. Verifying the
// checksum panics, as VerifyChecksum does, unless both addresses are IPv4.
func ParseTCPRepr(packet TCPPacket, src, dst netip.Addr, checksums ChecksumCapabilities) (TCPRepr, error) {
	if err := packet.CheckLen(); err != nil {
		return TCPRepr{}, err
	}
	if packet.SrcPort() == 0 || packet.DstPort() == 0 {
		return TCPRepr{}, ErrMalformed
	}
	if checksums.TCP.OnReceive() && !packet.VerifyChecksum(src, dst) {
		return TCPRepr{}, ErrMalformed
	}
	control, err := packet.control()
	if err != nil {
		return TCPRepr{}, err
	}
	repr := TCPRepr{
		SrcPort:   packet.SrcPort(),
		DstPort:   packet.DstPort(),
		Control:   control,
		SeqNumber: packet.SeqNumber(),
		WindowLen: packet.WindowLen(),
		Payload:   packet.Payload(),
	}
	if packet.ACK() {
		repr.AckNumber = Some(packet.AckNumber())
	}
	// The PSH flag is ignored: the socket quashes TCPControlPSH.
	// The URG flag and the urgent field are ignored. This behavior is
	// standards-compliant, however, most deployed systems (e.g. Linux) are
	// *not* standards-compliant, and would cut the byte at the urgent pointer
	// from the stream.
	return repr.withOptions(packet.Options(), src, dst)
}

// withOptions returns r with the fields that options, those of a segment
// sent from src to dst, carry. The last of repeated options wins, options the
// stack does not know are skipped, and an end-of-list option ends the
// options. It fails with the first malformed option's error.
func (r TCPRepr) withOptions(options []byte, src, dst netip.Addr) (TCPRepr, error) {
	for len(options) > 0 {
		option, rest, err := parseTCPOption(options)
		if err != nil {
			return TCPRepr{}, err
		}
		if option.kind == tcpOptionEndOfList {
			break
		}
		r = r.withOption(option, src, dst)
		options = rest
	}
	return r, nil
}

// withOption returns r with the field that option carries. No-operation and
// unknown options carry nothing the stack uses.
func (r TCPRepr) withOption(option tcpOption, src, dst netip.Addr) TCPRepr {
	switch option.kind {
	case tcpOptionMaxSegmentSize:
		r.MaxSegmentSize = Some(option.maxSegmentSize)
	case tcpOptionWindowScale:
		r.WindowScale = Some(r.capWindowScale(option.windowScale, src, dst))
	case tcpOptionSACKPermitted:
		r.SACKPermitted = true
	case tcpOptionSACKRange:
		r.SACKRanges = option.sackRanges
	case tcpOptionTimestamp:
		r.Timestamp = Some(option.timestamp)
	}
	return r
}

// capWindowScale returns the shift count to use for a received window scale
// option. RFC 1323: Thus, the shift count must be limited to 14 (which allows
// windows of 2**30 = 1 Gigabyte). If a Window Scale option is received with a
// shift.cnt value exceeding 14, the TCP should log the error but use 14
// instead of the specified value.
func (r TCPRepr) capWindowScale(scale uint8, src, dst netip.Addr) uint8 {
	if scale <= maxWindowScale {
		return scale
	}
	netlog.Debug("parsed window scaling factor >14, setting to 14",
		"src", netip.AddrPortFrom(src, r.SrcPort),
		"dst", netip.AddrPortFrom(dst, r.DstPort))
	return maxWindowScale
}

// HeaderLen returns the length of the header Emit writes, options included:
// a multiple of 4.
func (r TCPRepr) HeaderLen() int {
	length := TCPHeaderLen
	if r.MaxSegmentSize.IsSome() {
		length += maxSegmentSizeOptionLen
	}
	if r.WindowScale.IsSome() {
		length += windowScaleOptionLen
	}
	if r.SACKPermitted {
		length += sackPermittedOptionLen
	}
	if r.Timestamp.IsSome() {
		length += timestampOptionLen
	}
	if ranges := countSACKRanges(r.SACKRanges); ranges > 0 {
		length += sackOptionHeaderLen + ranges*sackRangeLen
	}
	return roundUpToMultiple(length, tcpOptionsAlignment)
}

// BufferLen returns the length of the whole segment Emit writes: header and
// payload.
func (r TCPRepr) BufferLen() int {
	return r.HeaderLen() + len(r.Payload)
}

// SegmentLen returns the length of the segment in sequence space: the payload
// plus one for a SYN or FIN.
func (r TCPRepr) SegmentLen() int {
	return len(r.Payload) + r.Control.Len()
}

// IsEmpty reports whether the segment carries no data and no flag other than
// PSH: a bare acknowledgement or window update.
func (r TCPRepr) IsEmpty() bool {
	return len(r.Payload) == 0 && (r.Control == TCPControlNone || r.Control == TCPControlPSH)
}

// Emit writes the segment into packet, which must be exactly BufferLen octets
// long, for a segment sent from src to dst. The checksum is computed when
// checksums.TCP computes on send and is zero otherwise. Emit panics unless
// both addresses are IPv4 (Is4).
func (r TCPRepr) Emit(packet TCPPacket, src, dst netip.Addr, checksums ChecksumCapabilities) {
	requireIPv4(src)
	requireIPv4(dst)
	r.emitHeader(packet)
	r.emitOptions(packet.Options())
	copy(packet.Payload()[:len(r.Payload)], r.Payload)
	if checksums.TCP.OnSend() {
		packet.FillChecksum(src, dst)
	} else {
		// Make sure we get a consistently zeroed checksum, since
		// implementations might rely on it.
		packet.SetChecksum(0)
	}
}

// emitHeader writes the fixed header but the checksum.
func (r TCPRepr) emitHeader(packet TCPPacket) {
	packet.SetSrcPort(r.SrcPort)
	packet.SetDstPort(r.DstPort)
	packet.SetSeqNumber(r.SeqNumber)
	packet.SetAckNumber(r.AckNumber.Or(0))
	packet.SetWindowLen(r.WindowLen)
	packet.SetHeaderLen(r.HeaderLen())
	packet.ClearFlags()
	packet.setControl(r.Control)
	packet.SetACK(r.AckNumber.IsSome())
	packet.SetUrgentPointer(0)
}

// emitOptions writes the options into options, the space HeaderLen leaves
// between the fixed header and the payload, and pads what is left with
// end-of-list octets.
func (r TCPRepr) emitOptions(options []byte) {
	if mss, ok := r.MaxSegmentSize.Get(); ok {
		options = tcpOption{kind: tcpOptionMaxSegmentSize, maxSegmentSize: mss}.emit(options)
	}
	if scale, ok := r.WindowScale.Get(); ok {
		options = tcpOption{kind: tcpOptionWindowScale, windowScale: scale}.emit(options)
	}
	if r.SACKPermitted {
		options = tcpOption{kind: tcpOptionSACKPermitted}.emit(options)
	} else if r.AckNumber.IsSome() && countSACKRanges(r.SACKRanges) > 0 {
		options = tcpOption{kind: tcpOptionSACKRange, sackRanges: r.SACKRanges}.emit(options)
	}
	if timestamp, ok := r.Timestamp.Get(); ok {
		options = tcpOption{kind: tcpOptionTimestamp, timestamp: timestamp}.emit(options)
	}
	if len(options) > 0 {
		tcpOption{kind: tcpOptionEndOfList}.emit(options)
	}
}

// Equal reports whether r and other describe the same segment, payload
// octets included.
func (r TCPRepr) Equal(other TCPRepr) bool {
	return r.headerEqual(other) && r.optionsEqual(other) && bytes.Equal(r.Payload, other.Payload)
}

// String formats the segment for logs and test failures:
// "TCP src=… dst=… syn seq=… ack=… win=… len=… mss=…".
func (r TCPRepr) String() string {
	parts := []string{fmt.Sprintf("TCP src=%d dst=%d", r.SrcPort, r.DstPort)}
	if flag := r.Control.flagName(); flag != "" {
		parts = append(parts, flag)
	}
	parts = append(parts, "seq="+r.SeqNumber.String())
	if ack, ok := r.AckNumber.Get(); ok {
		parts = append(parts, "ack="+ack.String())
	}
	parts = append(parts, fmt.Sprintf("win=%d len=%d", r.WindowLen, len(r.Payload)))
	if mss, ok := r.MaxSegmentSize.Get(); ok {
		parts = append(parts, fmt.Sprintf("mss=%d", mss))
	}
	return strings.Join(parts, " ")
}

func (r TCPRepr) headerEqual(other TCPRepr) bool {
	return r.SrcPort == other.SrcPort &&
		r.DstPort == other.DstPort &&
		r.Control == other.Control &&
		r.SeqNumber == other.SeqNumber &&
		r.AckNumber == other.AckNumber &&
		r.WindowLen == other.WindowLen
}

func (r TCPRepr) optionsEqual(other TCPRepr) bool {
	return r.WindowScale == other.WindowScale &&
		r.MaxSegmentSize == other.MaxSegmentSize &&
		r.SACKPermitted == other.SACKPermitted &&
		r.SACKRanges == other.SACKRanges &&
		r.Timestamp == other.Timestamp
}

func roundUpToMultiple(length, multiple int) int {
	if remainder := length % multiple; remainder != 0 {
		return length + multiple - remainder
	}
	return length
}
