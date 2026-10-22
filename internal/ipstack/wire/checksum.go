package wire

import (
	"encoding/binary"
	"math"
	"net/netip"
)

// Checksum says whether the stack verifies a protocol's checksum on received
// packets and computes it on sent ones; a device that does either in
// hardware asks the stack not to. The zero Checksum is ChecksumBoth.
type Checksum int

const (
	// ChecksumBoth verifies checksums when receiving and computes them when
	// sending.
	ChecksumBoth Checksum = iota
	// ChecksumOnReceive only verifies checksums when receiving.
	ChecksumOnReceive
	// ChecksumOnSend only computes checksums when sending.
	ChecksumOnSend
	// ChecksumNone ignores checksums completely.
	ChecksumNone
)

// OnReceive reports whether checksums are verified when receiving.
func (c Checksum) OnReceive() bool {
	return c == ChecksumBoth || c == ChecksumOnReceive
}

// OnSend reports whether checksums are computed when sending.
func (c Checksum) OnSend() bool {
	return c == ChecksumBoth || c == ChecksumOnSend
}

// ChecksumCapabilities says, per protocol, which checksums the stack verifies
// and computes. The zero value verifies and computes all of them, which is
// what a device without checksum offload (every device in this module) needs.
type ChecksumCapabilities struct {
	// IPv4 applies to the IPv4 header checksum.
	IPv4 Checksum
	// UDP applies to the UDP checksum, which covers a pseudo-header too.
	UDP Checksum
	// TCP applies to the TCP checksum, which covers a pseudo-header too.
	TCP Checksum
	// ICMPv4 applies to the ICMPv4 checksum.
	ICMPv4 Checksum
}

// The Internet checksum (RFC 1071) is the one's complement of the one's
// complement sum of the 16-bit words of the data it covers. The functions
// below compute the sum; a packet's FillChecksum
// writes its complement.

// correctChecksumSum is what the sum over data that includes its own correct
// checksum comes to: all ones (RFC 1071 § 1).
const correctChecksumSum uint16 = 0xffff

// checksumWordLen is the length of the words the checksum adds up, in octets.
const checksumWordLen = 2

// dataChecksum returns the one's complement sum of data, read as big-endian
// 16-bit words with an odd last octet padded with a zero octet, without the
// final complement. The sum does not depend on
// the order in which words are added (RFC 1071 § 2), so sums over parts of
// the data combine with combineChecksums.
func dataChecksum(data []byte) uint16 {
	var sum uint64
	for len(data) >= checksumWordLen {
		sum += uint64(binary.BigEndian.Uint16(data))
		data = data[checksumWordLen:]
	}
	if len(data) > 0 {
		padded := [checksumWordLen]byte{data[0], 0}
		sum += uint64(binary.BigEndian.Uint16(padded[:]))
	}
	return foldCarries(sum)
}

// combineChecksums returns the one's complement sum of checksums, sums that
// dataChecksum or pseudoHeaderChecksum computed.
func combineChecksums(checksums ...uint16) uint16 {
	var sum uint64
	for _, checksum := range checksums {
		sum += uint64(checksum)
	}
	return foldCarries(sum)
}

// foldCarries adds the carries out of the low 16 bits of sum back into them,
// which turns a plain sum of words into their one's complement sum
// (RFC 1071 § 4.1). The sum is 64 bits wide so that no carry is lost however
// long the data is.
func foldCarries(sum uint64) uint16 {
	const wordBits = 16
	for sum > math.MaxUint16 {
		sum = sum>>wordBits + sum&math.MaxUint16
	}
	return uint16(sum)
}

// pseudoHeaderChecksum returns the sum over the IPv4 pseudo-header that the
// TCP and UDP checksums cover (RFC 9293 § 3.1, RFC 768): the source and
// destination addresses, a zero octet, the protocol, and the length of the
// segment, of which only the low 16 bits fit. It panics unless both addresses
// are IPv4.
func pseudoHeaderChecksum(src, dst netip.Addr, protocol IPProtocol, length int) uint16 {
	srcOctets, dstOctets := ipv4Octets(src), ipv4Octets(dst)
	zeroAndProtocol := []byte{0, byte(protocol)}
	protocolAndLength := binary.BigEndian.AppendUint16(zeroAndProtocol, uint16(length))
	return combineChecksums(
		dataChecksum(srcOctets[:]),
		dataChecksum(dstOctets[:]),
		dataChecksum(protocolAndLength),
	)
}

// transportChecksum returns the sum over segment, a whole TCP segment or UDP
// datagram sent from src to dst, and its pseudo-header. It panics unless both
// addresses are IPv4.
func transportChecksum(src, dst netip.Addr, protocol IPProtocol, segment []byte) uint16 {
	return combineChecksums(
		pseudoHeaderChecksum(src, dst, protocol, len(segment)),
		dataChecksum(segment),
	)
}
