package satlink

import "time"

// Transport is a bidirectional byte stream to the handset's serial
// interface.
//
// Implementations exist for desktop serial ports (package serial), for the
// simulated handset (package sim) and for host apps through package mobile,
// for example over Android's UsbDeviceConnection. A transport that also
// implements [DTRController] lets the handset drop DTR to end a call it will
// not hang up otherwise; one that implements io.Closer is closed by
// [Handset.Close], which does not wait for a command or data call in
// progress. Its Close must therefore be safe while another goroutine is in
// ReadTimeout or WriteAll, and make them fail rather than act on what it
// closed.
//
// A Handset reports a transport's error to its caller as an [*Error] of kind
// [ErrTransport] wrapping it; an *Error is passed on unchanged, so a
// transport may return one to choose the message.
type Transport interface {
	// WriteAll writes every byte of p, or fails.
	WriteAll(p []byte) error

	// ReadTimeout reads up to len(p) bytes, waiting at most timeout for the
	// first one to arrive. It returns 0 and a nil error when nothing arrived
	// in time; a timeout of zero or less only takes what has already
	// arrived.
	ReadTimeout(p []byte, timeout time.Duration) (int, error)
}

// DTRController is implemented by transports that have a DTR control line.
//
// Dropping DTR briefly is the last resort for ending a call the handset will
// not hang up otherwise: with AT&D2, which data calls set by default, the
// handset ends the call when DTR drops. Transports without control lines do
// not implement it, and the handset then reports "this transport has no DTR
// line".
type DTRController interface {
	// RaiseDTR raises the DTR line: the terminal is ready.
	RaiseDTR() error

	// DropDTR drops the DTR line, as closing the port does.
	DropDTR() error
}

// The handset's own reads, writes and DTR changes. A data call uses them
// without the handset's lock while it holds the handset; every other caller
// holds the lock.

// noDTRLine is what a transport without a DTR line says to a DTR change.
const noDTRLine = "this transport has no DTR line"

// write sends p to the handset as it is.
func (h *Handset) write(p []byte) error {
	return wrapTransportError(h.transport.WriteAll(p))
}

// read reads what the handset sent, waiting at most timeout, as
// Transport.ReadTimeout does.
func (h *Handset) read(p []byte, timeout time.Duration) (int, error) {
	received, err := h.transport.ReadTimeout(p, timeout)
	if err != nil {
		return 0, wrapTransportError(err)
	}
	return received, nil
}

// raiseDTR raises the transport's DTR line, if it has one.
func (h *Handset) raiseDTR() error {
	line, err := h.dtrLine()
	if err != nil {
		return err
	}
	return wrapTransportError(line.RaiseDTR())
}

// dropDTR drops the transport's DTR line, if it has one.
func (h *Handset) dropDTR() error {
	line, err := h.dtrLine()
	if err != nil {
		return err
	}
	return wrapTransportError(line.DropDTR())
}

// dtrLine returns the transport's DTR line, failing for a transport
// without one.
func (h *Handset) dtrLine() (DTRController, error) {
	line, ok := h.transport.(DTRController)
	if !ok {
		return nil, &Error{Kind: ErrTransport, Message: noDTRLine}
	}
	return line, nil
}
