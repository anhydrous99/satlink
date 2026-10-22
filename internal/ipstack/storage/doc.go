// Package storage provides the containers the sockets keep their data in: a
// generic RingBuffer (a TCP socket's byte stream), a PacketBuffer of
// datagrams with headers (a UDP socket's queues), and the Assembler that
// tracks which parts of a TCP receive window have arrived.
//
// Unlike smoltcp's storage module, which lets callers supply the memory, each
// container allocates its own, with the capacity given to
// its constructor, and never grows. The containers hand out slices of their
// storage to avoid copying; such a slice is valid only until the container
// is next changed, and its capacity is its length, so appending to it
// reallocates instead of overwriting the storage that follows.
//
// Derived from smoltcp (https://github.com/smoltcp-rs/smoltcp), 0BSD licence;
// see ../LICENSE-smoltcp.
package storage
