package dns

import (
	"bytes"
	"iter"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// A name on the wire is a sequence of labels, each a length octet and that
// many octets, ending with the zero-length root label (RFC 1035 § 3.1).
const (
	// maxLabelLen is the longest label a name may have (RFC 1035 § 2.3.4).
	maxLabelLen = 63
	// rootLabel is the length octet of the root label, which ends a name.
	rootLabel = 0x00
	// labelSeparator separates the labels of a name as people write it.
	labelSeparator = '.'
)

// encodeName returns the wire form of name, written as "example.com" with or
// without a trailing dot. It fails with ErrInvalidName for an empty name or
// an empty or over-long label, and with ErrNameTooLong when the wire form
// exceeds MaxNameSize octets; each label is checked, then added, in turn,
// so the first problem decides which.
func encodeName(name string) ([]byte, error) {
	if name == "" {
		netlog.Trace("invalid name: zero length")
		return nil, ErrInvalidName
	}
	trimmed := bytes.TrimSuffix([]byte(name), []byte{labelSeparator})
	encoded := make([]byte, 0, MaxNameSize)
	for label := range bytes.SplitSeq(trimmed, []byte{labelSeparator}) {
		if err := checkLabel(label); err != nil {
			return nil, err
		}
		if !appendLabel(&encoded, label) {
			return nil, ErrNameTooLong
		}
	}
	if !appendWithinNameSize(&encoded, rootLabel) {
		return nil, ErrNameTooLong
	}
	return encoded, nil
}

// checkLabel returns ErrInvalidName for a label that is empty or longer than
// maxLabelLen octets.
func checkLabel(label []byte) error {
	switch {
	case len(label) > maxLabelLen:
		netlog.Trace("invalid name: too long label")
		return ErrInvalidName
	case len(label) == 0:
		netlog.Trace("invalid name: zero length label")
		return ErrInvalidName
	default:
		return nil
	}
}

// copyName makes name the wire form of labels, which replaces it. It fails
// with the first error labels yields, or with wire.ErrMalformed when the name
// would exceed MaxNameSize octets, and then leaves name holding what it copied
// before the failure.
func copyName(name *[]byte, labels iter.Seq2[[]byte, error]) error {
	*name = (*name)[:0]
	for label, err := range labels {
		if err != nil {
			return err
		}
		if !appendLabel(name, label) {
			return wire.ErrMalformed
		}
	}
	if !appendWithinNameSize(name, rootLabel) {
		return wire.ErrMalformed
	}
	return nil
}

// appendLabel appends label to the wire form of a name: its length octet,
// then its octets. Each part is appended only if it keeps the name within
// MaxNameSize octets; it
// reports whether both were. A label is never longer than maxLabelLen, so its
// length fits its octet.
func appendLabel(name *[]byte, label []byte) bool {
	return appendWithinNameSize(name, byte(len(label))) && appendWithinNameSize(name, label...)
}

// appendWithinNameSize appends octets to name if the result is at most
// MaxNameSize octets long, and reports whether it did.
func appendWithinNameSize(name *[]byte, octets ...byte) bool {
	if len(*name)+len(octets) > MaxNameSize {
		return false
	}
	*name = append(*name, octets...)
	return true
}

// namesEqual reports whether two names have the same labels, compared octet
// for octet in step: it fails with the first
// error either name yields before they differ or end.
func namesEqual(a, b iter.Seq2[[]byte, error]) (bool, error) {
	nextA, stopA := iter.Pull2(a)
	defer stopA()
	nextB, stopB := iter.Pull2(b)
	defer stopB()
	for {
		labelA, errA, moreA := nextA()
		labelB, errB, moreB := nextB()
		switch {
		case errA != nil:
			return false, errA
		case errB != nil:
			return false, errB
		case !moreA && !moreB:
			// Both finished: equal.
			return true, nil
		case !moreA || !moreB:
			// One finished before the other: not equal.
			return false, nil
		case !bytes.Equal(labelA, labelB):
			return false, nil
		}
	}
}
