package tcp

import "errors"

// The errors a socket's operations return. Each operation documents which of
// them it returns.
var (
	// ErrInvalidState reports an operation the socket's state does not
	// allow: listening or connecting while open, sending while the transmit
	// half is closed, or receiving while the receive half is not open.
	ErrInvalidState = errors.New("invalid state")
	// ErrUnaddressable reports an endpoint that cannot be used: a zero port,
	// an unspecified address, an address that is not IPv4, or no address to
	// send from.
	ErrUnaddressable = errors.New("unaddressable destination")
	// ErrFinished reports that the peer closed its side gracefully (sent a
	// FIN) and every octet before it has been read: the end of the stream.
	ErrFinished = errors.New("operation finished")
)
