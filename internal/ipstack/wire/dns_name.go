package wire

import (
	"encoding/binary"
	"iter"
)

// A name on the wire is a sequence of labels, each a length octet and that
// many octets, ending with the zero-length label of the root or with a
// compression pointer to a prior occurrence of the rest of the name
// (RFC 1035 § 3.1, § 4.1.4). The top two bits of a length octet say which.
const (
	dnsLabelTypeMask    = 0xc0
	dnsLabelTypeNormal  = 0x00
	dnsLabelTypePointer = 0xc0
	dnsLabelLenMask     = 0x3f
	// dnsNameEnd is the length octet of the root label, which ends a name.
	dnsNameEnd = 0x00
	// dnsLengthOctetLen is the length of the octet that starts a label.
	dnsLengthOctetLen = 1
	// dnsPointerLen is the length of a compression pointer, whose low 14
	// bits are the offset into the message it points to.
	dnsPointerLen               = 2
	dnsPointerOffsetMask uint16 = 0x3fff
)

// splitName splits the name at the start of buffer from the octets that
// follow it, without following compression pointers (smoltcp's
// parse_name_part). The name shares the buffer's storage.
func splitName(buffer []byte) (name, rest []byte, err error) {
	nameLen, err := encodedNameLen(buffer)
	if err != nil {
		return nil, nil, err
	}
	return buffer[:nameLen:nameLen], buffer[nameLen:], nil
}

// encodedNameLen returns the length of the name at the start of buffer,
// which ends with its root label or with its first compression pointer. It
// fails with ErrMalformed for a truncated name or a label of a reserved type.
func encodedNameLen(buffer []byte) (int, error) {
	offset := 0
	for offset < len(buffer) {
		first := buffer[offset]
		switch {
		case first == dnsNameEnd:
			return offset + dnsLengthOctetLen, nil
		case first&dnsLabelTypeMask == dnsLabelTypeNormal:
			offset += dnsLengthOctetLen + int(first&dnsLabelLenMask)
			if offset > len(buffer) {
				return 0, ErrMalformed
			}
		case first&dnsLabelTypeMask == dnsLabelTypePointer:
			if offset+dnsPointerLen > len(buffer) {
				return 0, ErrMalformed
			}
			return offset + dnsPointerLen, nil
		default:
			return 0, ErrMalformed
		}
	}
	return 0, ErrMalformed
}

// NameLabels returns the labels of the name encoded at the start of name,
// following compression pointers into the packet (smoltcp's parse_name).
// name may lie inside the packet or outside it. The first pointer may point
// anywhere in the packet, every later one only before the octets the one
// before it pointed to, which rules out loops. The sequence yields one label
// at a time, or a final ErrMalformed and stops.
func (p DNSPacket) NameLabels(name []byte) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		walker := nameWalker{rest: name, pointable: p}
		for {
			label, end, err := walker.nextLabel()
			switch {
			case err != nil:
				yield(nil, err)
				return
			case end:
				return
			case !yield(label, nil):
				return
			}
		}
	}
}

// nameWalker walks the labels of a name for NameLabels.
type nameWalker struct {
	// rest is what is left of the name, from the next label on.
	rest []byte
	// pointable is the part of the packet a compression pointer may still
	// point into.
	pointable []byte
}

// nextLabel returns the next label of the name, or end at the root label. It
// fails with ErrMalformed for a truncated name, a label of a reserved type,
// or a pointer outside the part of the packet it may point into.
func (w *nameWalker) nextLabel() (label []byte, end bool, err error) {
	for {
		if len(w.rest) == 0 {
			return nil, false, ErrMalformed
		}
		first := w.rest[0]
		switch {
		case first == dnsNameEnd:
			return nil, true, nil
		case first&dnsLabelTypeMask == dnsLabelTypeNormal:
			label, err := w.takeLabel(int(first & dnsLabelLenMask))
			return label, false, err
		case first&dnsLabelTypeMask == dnsLabelTypePointer:
			if err := w.followPointer(); err != nil {
				return nil, false, err
			}
		default:
			return nil, false, ErrMalformed
		}
	}
}

// takeLabel returns the label of labelLen octets that starts rest, and moves
// past it.
func (w *nameWalker) takeLabel(labelLen int) ([]byte, error) {
	end := dnsLengthOctetLen + labelLen
	if len(w.rest) < end {
		return nil, ErrMalformed
	}
	label := w.rest[dnsLengthOctetLen:end:end]
	w.rest = w.rest[end:]
	return label, nil
}

// followPointer continues the name where the compression pointer that starts
// rest points.
//
// RFC 1035 says: "In this scheme, an entire domain name or a list of labels
// at the end of a domain name is replaced with a pointer to a ***prior***
// occurrence of the same name." It is unclear if this means the pointer MUST
// point backwards in the packet or not. Either way, pointers that don't
// point backwards are never seen in the fields, so use this to check that
// there are no pointer loops: the walker goes on in the part of the packet
// after the pointer's target and keeps only the part before it to point
// into. It never parses the same octet twice, which eliminates pointer loops.
func (w *nameWalker) followPointer() error {
	if len(w.rest) < dnsPointerLen {
		return ErrMalformed
	}
	offset := int(binary.BigEndian.Uint16(w.rest) & dnsPointerOffsetMask)
	if offset >= len(w.pointable) {
		return ErrMalformed
	}
	w.rest = w.pointable[offset:]
	w.pointable = w.pointable[:offset]
	return nil
}
