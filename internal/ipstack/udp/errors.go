package udp

import "errors"

// The errors a socket's operations return. smoltcp has one enum per
// operation (BindError, SendError, RecvError) with these variants and texts;
// each operation documents which of them it returns.
var (
	// ErrInvalidState reports a Bind on a socket that is already bound.
	ErrInvalidState = errors.New("invalid state")
	// ErrUnaddressable reports a zero port, an unbound socket, or an address
	// the stack cannot use: an unspecified destination, or an address that
	// is not IPv4.
	ErrUnaddressable = errors.New("unaddressable")
	// ErrBufferFull reports a datagram the transmit buffer has no room for,
	// now or, if it is larger than the buffer, ever.
	ErrBufferFull = errors.New("buffer full")
	// ErrExhausted reports that no datagram has been received.
	ErrExhausted = errors.New("exhausted")
	// ErrTruncated reports a datagram larger than the slice given to copy it
	// into.
	ErrTruncated = errors.New("truncated")
)
