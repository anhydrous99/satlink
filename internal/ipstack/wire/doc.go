// Package wire reads and writes the packets the stack speaks, IPv4, TCP, UDP
// and DNS, and writes ICMPv4 errors. It follows the design of smoltcp's wire
// module (see the ipstack package documentation).
//
// It works on two levels.
//
// The packet level gives structured access to the octets of a packet without
// judging their content: IPv4Packet, TCPPacket, UDPPacket and DNSPacket are
// byte slices with an accessor for each header field the stack reads and a
// setter for each one it writes. ICMPv4Packet has only setters, as the stack
// writes ICMP errors but reads no ICMP message.
// Converting a slice, as in IPv4Packet(buffer), does not check the buffer;
// the New… constructors do, and validate the length fields.
// When parsing untrusted input, always use a New… constructor (or CheckLen):
// once it has succeeded, no accessor panics as long as the buffer is not
// changed. When emitting, use the conversion on a buffer of exactly the length
// the Repr reports; a length check could pass on a zeroed buffer and fail on a
// reused one.
//
// The representation level sheds everything the stack does not support, such
// as impossible flag combinations, bad checksums and unknown options.
// ParseIPv4Repr, ParseTCPRepr and ParseUDPRepr read an IPv4Repr, TCPRepr or
// UDPRepr from a packet, and the Repr's Emit writes it back. ICMPv4Repr, an
// ICMP error, is only emitted. DNS is one-sided: DNSRepr is the query the
// stack sends, which it only emits, and ParseDNSQuestion and ParseDNSRecord
// read a response's question and records.
// The Parse… functions check the packet's length themselves and never panic,
// whatever its content, and Emit never panics as long as the buffer is
// exactly as long as the Repr says and its addresses are IPv4. Slices in a
// parsed Repr, such as a payload or a name, share the packet's storage.
//
// Addresses are netip.Addr values holding plain IPv4 addresses (Is4); the
// stack compares them with ==, so IPv4-mapped IPv6 forms must be unmapped
// first. Functions that write an address into a packet or include one in a
// checksum (the address setters, the Emit methods, FillChecksum,
// VerifyChecksum, and ParseTCPRepr and ParseUDPRepr when they verify a
// checksum) panic unless it is IPv4. The sockets refuse other addresses
// where they enter the stack, so nothing the stack builds reaches that panic.
// Endpoints are netip.AddrPort values, and ListenEndpoint is an endpoint
// whose address may be left open. Header field names follow the RFCs
// (SrcAddr, DstPort, HopLimit…).
//
// Optional header fields whose zero value is meaningful, such as an
// acknowledgement number or a window scale of 0, are Optional values.
//
// IPv4Repr and TCPRepr have HeaderLen (the header) and BufferLen (header and
// payload), so a segment's whole datagram is ip.HeaderLen() +
// segment.BufferLen(). UDPRepr has only HeaderLen, as its payload travels
// beside it. ICMPv4Repr, DNSRepr and DNSQuestion have only BufferLen, the
// length of everything they emit.
//
// Parsing logs a TCP window scale above 14 and a SACK
// option with more than three blocks, at debug level with the "pkg"
// attribute "ipstack".
//
// Derived from smoltcp (https://github.com/smoltcp-rs/smoltcp), 0BSD licence;
// see ../LICENSE-smoltcp.
package wire
