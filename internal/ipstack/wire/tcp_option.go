package wire

import (
	"encoding/binary"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
)

// The kind octets of the TCP options the stack knows (RFC 9293 § 3.2,
// RFC 7323, RFC 2018).
const (
	tcpOptionKindEnd           = 0x00
	tcpOptionKindNOP           = 0x01
	tcpOptionKindMSS           = 0x02
	tcpOptionKindWindowScale   = 0x03
	tcpOptionKindSACKPermitted = 0x04
	tcpOptionKindSACKRange     = 0x05
	tcpOptionKindTimestamp     = 0x08
)

// The layout of a TCP option other than end-of-list and no-operation, which
// are a lone kind octet: a kind octet, a length octet counting the whole
// option, then the option's data.
const (
	tcpOptionKindOffset = 0
	tcpOptionLenOffset  = 1
	tcpOptionHeaderLen  = 2
	// singleOctetOptionLen is the length of end-of-list and no-operation.
	singleOctetOptionLen = 1
)

// Lengths of the TCP options the stack emits, kind and length octets included.
const (
	maxSegmentSizeOptionLen = 4
	windowScaleOptionLen    = 3
	sackPermittedOptionLen  = 2
	timestampOptionLen      = 10
	sackOptionHeaderLen     = 2
	sackRangeLen            = 8
)

// Offsets of the second 32-bit value in a SACK block and in the timestamps
// option's data: each holds two sequence numbers or timestamps.
const (
	sackRightOffset          = 4
	timestampEchoReplyOffset = 4
)

// tcpOptionKind says which option a tcpOption is.
type tcpOptionKind int

const (
	tcpOptionEndOfList tcpOptionKind = iota
	tcpOptionNoOperation
	tcpOptionMaxSegmentSize
	tcpOptionWindowScale
	tcpOptionSACKPermitted
	tcpOptionSACKRange
	tcpOptionTimestamp
	tcpOptionUnknown
)

// tcpOption is one TCP option. kind says which, and the
// fields below say which kinds use them.
type tcpOption struct {
	kind tcpOptionKind
	// maxSegmentSize is the value of a maximum segment size option.
	maxSegmentSize uint16
	// windowScale is the shift count of a window scale option, as sent.
	windowScale uint8
	// sackRanges are the blocks of a SACK option, the present ones first.
	sackRanges [MaxSACKRanges]Optional[SACKRange]
	// timestamp is the content of a timestamps option.
	timestamp TCPTimestamp
	// unknownKind and data are the kind octet and the data of an option the
	// stack does not know.
	unknownKind uint8
	data        []byte
}

// fixedTCPOptionLens are the lengths of the options that have only one; an
// option of one of these kinds with another length is malformed.
var fixedTCPOptionLens = map[uint8]int{
	tcpOptionKindMSS:           maxSegmentSizeOptionLen,
	tcpOptionKindWindowScale:   windowScaleOptionLen,
	tcpOptionKindSACKPermitted: sackPermittedOptionLen,
}

// parseTCPOption parses the option at the start of buffer and returns it
// with the octets that follow it. It fails with
// ErrMalformed for an empty buffer, an option that does not fit into it, an
// option of a fixed length with another length, and a SACK option that is
// not a whole number of blocks.
func parseTCPOption(buffer []byte) (option tcpOption, rest []byte, err error) {
	if len(buffer) == 0 {
		return tcpOption{}, nil, ErrMalformed
	}
	switch buffer[tcpOptionKindOffset] {
	case tcpOptionKindEnd:
		return tcpOption{kind: tcpOptionEndOfList}, buffer[singleOctetOptionLen:], nil
	case tcpOptionKindNOP:
		return tcpOption{kind: tcpOptionNoOperation}, buffer[singleOctetOptionLen:], nil
	}
	length, err := tcpOptionLen(buffer)
	if err != nil {
		return tcpOption{}, nil, err
	}
	option, err = decodeTCPOption(buffer[:length:length])
	if err != nil {
		return tcpOption{}, nil, err
	}
	return option, buffer[length:], nil
}

// tcpOptionLen returns the length of the option at the start of buffer,
// after checking that it covers the kind and length octets and fits into
// buffer.
func tcpOptionLen(buffer []byte) (int, error) {
	if len(buffer) < tcpOptionHeaderLen {
		return 0, ErrMalformed
	}
	length := int(buffer[tcpOptionLenOffset])
	if length < tcpOptionHeaderLen || length > len(buffer) {
		return 0, ErrMalformed
	}
	return length, nil
}

// decodeTCPOption decodes raw, exactly one option with a length octet.
func decodeTCPOption(raw []byte) (tcpOption, error) {
	kind, data := raw[tcpOptionKindOffset], raw[tcpOptionHeaderLen:]
	if fixedLen, isFixed := fixedTCPOptionLens[kind]; isFixed && len(raw) != fixedLen {
		return tcpOption{}, ErrMalformed
	}
	switch kind {
	case tcpOptionKindMSS:
		return tcpOption{kind: tcpOptionMaxSegmentSize, maxSegmentSize: binary.BigEndian.Uint16(data)}, nil
	case tcpOptionKindWindowScale:
		return tcpOption{kind: tcpOptionWindowScale, windowScale: data[0]}, nil
	case tcpOptionKindSACKPermitted:
		return tcpOption{kind: tcpOptionSACKPermitted}, nil
	case tcpOptionKindSACKRange:
		return decodeSACKRangeOption(raw)
	}
	if kind == tcpOptionKindTimestamp && len(raw) == timestampOptionLen {
		return tcpOption{kind: tcpOptionTimestamp, timestamp: TCPTimestamp{
			Value:     binary.BigEndian.Uint32(data),
			EchoReply: binary.BigEndian.Uint32(data[timestampEchoReplyOffset:]),
		}}, nil
	}
	// An option it does not know, and a timestamps option of another length,
	// are kept as unknown options.
	return tcpOption{kind: tcpOptionUnknown, unknownKind: kind, data: data}, nil
}

// decodeSACKRangeOption decodes raw, one SACK option, keeping its first
// MaxSACKRanges blocks.
func decodeSACKRangeOption(raw []byte) (tcpOption, error) {
	blocks := raw[sackOptionHeaderLen:]
	if len(blocks) < sackRangeLen || len(blocks)%sackRangeLen != 0 {
		return tcpOption{}, ErrMalformed
	}
	if len(blocks) > MaxSACKRanges*sackRangeLen {
		// It's possible for a remote to send 4 SACK blocks, but extremely
		// rare. Better to "lose" that 4th block and save the extra RAM and
		// CPU cycles in the vastly more common case.
		//
		// RFC 2018: SACK option that specifies n blocks will have a length of
		// 8*n+2 bytes, so the 40 bytes available for TCP options can specify
		// a maximum of 4 blocks. It is expected that SACK will often be used
		// in conjunction with the Timestamp option used for RTTM [...] thus a
		// maximum of 3 SACK blocks will be allowed in this case.
		netlog.Debug("sACK with >3 blocks, truncating to 3")
	}
	option := tcpOption{kind: tcpOptionSACKRange}
	// RFC 2018: Each contiguous block of data queued at the data receiver is
	// defined in the SACK option by two 32-bit unsigned integers in network
	// byte order[...]
	for i := range option.sackRanges {
		if len(blocks) == 0 {
			break
		}
		option.sackRanges[i] = Some(SACKRange{
			Left:  binary.BigEndian.Uint32(blocks),
			Right: binary.BigEndian.Uint32(blocks[sackRightOffset:]),
		})
		blocks = blocks[sackRangeLen:]
	}
	return option, nil
}

// bufferLen returns the length of the option emit writes.
func (o tcpOption) bufferLen() int {
	switch o.kind {
	case tcpOptionEndOfList, tcpOptionNoOperation:
		return singleOctetOptionLen
	case tcpOptionMaxSegmentSize:
		return maxSegmentSizeOptionLen
	case tcpOptionWindowScale:
		return windowScaleOptionLen
	case tcpOptionSACKPermitted:
		return sackPermittedOptionLen
	case tcpOptionSACKRange:
		return sackOptionHeaderLen + countSACKRanges(o.sackRanges)*sackRangeLen
	case tcpOptionTimestamp:
		return timestampOptionLen
	default:
		return tcpOptionHeaderLen + len(o.data)
	}
}

// emit writes the option at the start of buffer and returns the octets after
// it. An end-of-list option fills the whole buffer: there may be padding
// space, which also should be initialized.
func (o tcpOption) emit(buffer []byte) []byte {
	switch o.kind {
	case tcpOptionEndOfList:
		for i := range buffer {
			buffer[i] = tcpOptionKindEnd
		}
		return buffer[singleOctetOptionLen:]
	case tcpOptionNoOperation:
		buffer[tcpOptionKindOffset] = tcpOptionKindNOP
		return buffer[singleOctetOptionLen:]
	}
	length := o.bufferLen()
	buffer[tcpOptionLenOffset] = byte(length)
	o.emitKindAndData(buffer[:length])
	return buffer[length:]
}

// emitKindAndData writes the kind octet and the data of an option with a
// length octet into raw, exactly as long as the option.
func (o tcpOption) emitKindAndData(raw []byte) {
	data := raw[tcpOptionHeaderLen:]
	switch o.kind {
	case tcpOptionMaxSegmentSize:
		raw[tcpOptionKindOffset] = tcpOptionKindMSS
		binary.BigEndian.PutUint16(data, o.maxSegmentSize)
	case tcpOptionWindowScale:
		raw[tcpOptionKindOffset] = tcpOptionKindWindowScale
		data[0] = o.windowScale
	case tcpOptionSACKPermitted:
		raw[tcpOptionKindOffset] = tcpOptionKindSACKPermitted
	case tcpOptionSACKRange:
		raw[tcpOptionKindOffset] = tcpOptionKindSACKRange
		emitSACKRanges(data, o.sackRanges)
	case tcpOptionTimestamp:
		raw[tcpOptionKindOffset] = tcpOptionKindTimestamp
		binary.BigEndian.PutUint32(data, o.timestamp.Value)
		binary.BigEndian.PutUint32(data[timestampEchoReplyOffset:], o.timestamp.EchoReply)
	default:
		raw[tcpOptionKindOffset] = o.unknownKind
		copy(data, o.data)
	}
}

// emitSACKRanges writes the present blocks of ranges into data, one after
// the other, skipping absent ones.
func emitSACKRanges(data []byte, ranges [MaxSACKRanges]Optional[SACKRange]) {
	for _, sackRange := range ranges {
		block, present := sackRange.Get()
		if !present {
			continue
		}
		binary.BigEndian.PutUint32(data, block.Left)
		binary.BigEndian.PutUint32(data[sackRightOffset:], block.Right)
		data = data[sackRangeLen:]
	}
}

// countSACKRanges returns how many of ranges are present.
func countSACKRanges(ranges [MaxSACKRanges]Optional[SACKRange]) int {
	count := 0
	for _, sackRange := range ranges {
		if sackRange.IsSome() {
			count++
		}
	}
	return count
}
