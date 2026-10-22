package ppp

import (
	"encoding/binary"
	"math"
	"slices"
)

// PPP in HDLC-like framing (RFC 1662): flag-delimited frames with byte
// stuffing, control-character transparency (ACCM) and a frame check
// sequence. The encoder and decoder are internal.

// DefaultACCM is the Async-Control-Character-Map in force until LCP agrees
// on another: every control character (0x00–0x1F) is escaped (RFC 1662
// §7.1).
const DefaultACCM uint32 = 0xFFFF_FFFF

const (
	// flag delimits frames (RFC 1662 §4.1).
	flag byte = 0x7E
	// controlEscape announces an escaped byte (RFC 1662 §4.2).
	controlEscape byte = 0x7D
	// escapeMask: an escaped byte travels as controlEscape followed by
	// byte ^ escapeMask.
	escapeMask byte = 0x20
	// allStationsAddress is the address field of every PPP frame (RFC 1662
	// §3.1).
	allStationsAddress byte = 0xFF
	// unnumberedInformation is the control field of every PPP frame (RFC
	// 1662 §3.1).
	unnumberedInformation byte = 0x03
	// lastControlCharacter is the highest byte the ACCM covers.
	lastControlCharacter byte = 0x1F
)

// fcsLen is the size of the frame check sequence.
const fcsLen = 2

// addressAndControlLen is the size of the address and control fields.
const addressAndControlLen = 2

// maxHeaderLen is the address, control and an uncompressed protocol field.
const maxHeaderLen = addressAndControlLen + uint16Len

// minFrameLen is the shortest frame that can carry anything: a compressed
// protocol field and the FCS.
const minFrameLen = 1 + fcsLen

// frame is a received frame, reduced to its protocol and information
// fields.
type frame struct {
	protocol    uint16
	information []byte
}

// frameEncoding is how we write frames, as agreed with the peer through
// LCP.
type frameEncoding struct {
	// accm holds the control characters the peer asked us to escape.
	accm uint32
	// compressAddressAndControl means that the peer accepts frames
	// without address and control fields (ACFC).
	compressAddressAndControl bool
	// compressProtocol means that the peer accepts one-octet protocol
	// fields (PFC).
	compressProtocol bool
}

// conservativeEncoding returns the encoding every peer understands, which
// is always used for LCP (RFC 1661 §6.5–6.6).
func conservativeEncoding() frameEncoding {
	return frameEncoding{accm: DefaultACCM}
}

// appendFrame appends information to dst as one complete frame and returns
// the extended slice.
func appendFrame(dst []byte, protocol uint16, information []byte, encoding frameEncoding) []byte {
	header := frameHeader(protocol, encoding)
	fcs := ^updateFCS(updateFCS(fcsInitial, header), information)
	dst = append(dst, flag)
	dst = appendEscaped(dst, header, encoding.accm)
	dst = appendEscaped(dst, information, encoding.accm)
	dst = appendEscaped(dst, binary.LittleEndian.AppendUint16(nil, fcs), encoding.accm)
	return append(dst, flag)
}

// frameHeader returns the address, control and protocol fields of a frame,
// compressed as far as encoding allows.
func frameHeader(protocol uint16, encoding frameEncoding) []byte {
	header := make([]byte, 0, maxHeaderLen)
	if !encoding.compressAddressAndControl {
		header = append(header, allStationsAddress, unnumberedInformation)
	}
	// Protocols below 0x100 fit in one octet (RFC 1661 §6.5).
	if encoding.compressProtocol && protocol <= math.MaxUint8 {
		return append(header, byte(protocol))
	}
	return binary.BigEndian.AppendUint16(header, protocol)
}

// appendEscaped appends data to dst with every byte that needs it escaped.
func appendEscaped(dst, data []byte, accm uint32) []byte {
	for _, b := range data {
		if needsEscape(b, accm) {
			dst = append(dst, controlEscape, b^escapeMask)
		} else {
			dst = append(dst, b)
		}
	}
	return dst
}

// needsEscape reports whether b must travel escaped to a peer that asked
// for accm.
func needsEscape(b byte, accm uint32) bool {
	return b == flag || b == controlEscape || isMapped(b, accm)
}

// isMapped reports whether b is a control character accm marks.
func isMapped(b byte, accm uint32) bool {
	return b <= lastControlCharacter && accm&(1<<b) != 0
}

// frameDecoder splits the received byte stream into frames.
//
// Anything that is not a well-formed frame — line noise, modem result codes
// such as NO CARRIER, aborted or oversized frames — is dropped and counted.
type frameDecoder struct {
	// inProgress holds the unescaped bytes of the frame being received.
	inProgress []byte
	// escaping means that the last byte was a Control Escape.
	escaping bool
	// overflowed means that the frame in progress outgrew maxFrameLen.
	overflowed  bool
	maxFrameLen int
	// receiveACCM holds the control characters the peer escapes for us.
	receiveACCM uint32
	// invalidFrames counts the frames discarded so far because they were
	// malformed or corrupted.
	invalidFrames uint64
}

// newFrameDecoder returns a decoder for frames whose information fields
// hold at most maxInformationLen bytes.
func newFrameDecoder(maxInformationLen int) *frameDecoder {
	return &frameDecoder{
		maxFrameLen: maxInformationLen + maxHeaderLen + fcsLen,
		receiveACCM: DefaultACCM,
	}
}

// setReceiveACCM sets the control characters the peer escapes for us.
// Unescaped ones were inserted along the way (by flow control, say) and are
// dropped (RFC 1662 §7.1).
func (d *frameDecoder) setReceiveACCM(accm uint32) {
	d.receiveACCM = accm
}

// decode feeds received bytes to the decoder and returns the frames they
// complete.
func (d *frameDecoder) decode(data []byte) []frame {
	var frames []frame
	for _, b := range data {
		switch {
		case b == flag:
			if completed, ok := d.finishFrame(); ok {
				frames = append(frames, completed)
			}
		case isMapped(b, d.receiveACCM):
			// Inserted along the way: not part of the frame.
		case d.escaping:
			// A pending escape applies to any byte, a second Control
			// Escape included.
			d.escaping = false
			d.push(b ^ escapeMask)
		case b == controlEscape:
			d.escaping = true
		default:
			d.push(b)
		}
	}
	return frames
}

// push adds an unescaped byte to the frame in progress, unless the frame
// has grown too large.
func (d *frameDecoder) push(b byte) {
	if len(d.inProgress) < d.maxFrameLen {
		d.inProgress = append(d.inProgress, b)
	} else {
		d.overflowed = true
	}
}

// finishFrame ends the frame in progress at a flag and returns it, if it is
// a well-formed frame.
func (d *frameDecoder) finishFrame() (frame, bool) {
	// An escape right before the flag aborts the frame (RFC 1662 §4.2), which
	// is discarded as invalid (§4.3).
	aborted, overflowed, raw := d.escaping, d.overflowed, d.inProgress
	d.escaping, d.overflowed, d.inProgress = false, false, d.inProgress[:0]
	if len(raw) == 0 {
		return frame{}, false // inter-frame fill, or a flag shared by adjacent frames
	}
	if aborted || overflowed {
		d.invalidFrames++
		return frame{}, false
	}
	received, ok := parseFrame(raw)
	if !ok {
		d.invalidFrames++
	}
	return received, ok
}

// parseFrame checks an unescaped frame and reduces it to its protocol and
// information fields, which it copies.
func parseFrame(raw []byte) (frame, bool) {
	if len(raw) < minFrameLen || updateFCS(fcsInitial, raw) != fcsGoodResidue {
		return frame{}, false
	}
	// Accept compressed headers even if not negotiated: be liberal in what
	// we receive.
	body := withoutAddressAndControl(raw[:len(raw)-fcsLen])
	protocol, information, ok := splitProtocol(body)
	if !ok {
		return frame{}, false
	}
	return frame{protocol: protocol, information: slices.Clone(information)}, true
}

// withoutAddressAndControl returns body without its address and control
// fields, if it has them.
func withoutAddressAndControl(body []byte) []byte {
	if len(body) >= addressAndControlLen && body[0] == allStationsAddress && body[1] == unnumberedInformation {
		return body[addressAndControlLen:]
	}
	return body
}

// splitProtocol reads a protocol field: one odd octet when compressed,
// otherwise two octets whose second is odd (RFC 1661 §2).
func splitProtocol(body []byte) (protocol uint16, information []byte, ok bool) {
	switch {
	case len(body) >= 1 && body[0]&1 == 1:
		return uint16(body[0]), body[1:], true
	case len(body) >= uint16Len && body[1]&1 == 1:
		return binary.BigEndian.Uint16(body), body[uint16Len:], true
	default:
		return 0, nil, false
	}
}
