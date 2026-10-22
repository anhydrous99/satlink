package mobile

import (
	"errors"
	"time"

	"github.com/anhydrous99/satlink/iridium"
)

// ByteTransport is the byte stream to the handset, implemented by the host
// app — on Android over UsbDeviceConnection (see AndroidUsbTransport.kt).
// Its methods are called from goroutines of this package, never two at
// once.
//
// An error a method returns reaches the caller as a SatlinkError of kind
// SatlinkErrorTransport carrying its message.
//
// In Objective-C and Swift, adopt the protocol SatlinkMobileByteTransport
// (Swift: SatlinkMobileByteTransportProtocol) in a class of your own. The
// class of that name stands for a Go implementation, and one made with the
// init it inherits from NSObject has none: passing it ends the app.
// scripts/build-bindings.sh marks that init unavailable.
type ByteTransport interface {
	// Write writes all of data, or fails. data is only valid during the
	// call: copy it to keep it. It always holds at least one byte, as an
	// empty array would reach Java as null and Swift as nil.
	Write(data []byte) error

	// Read returns up to maxLen bytes, or an empty array (or null) if
	// nothing arrived within timeoutMillis milliseconds. timeoutMillis is at
	// least 1 and at most 2147483647, so it fits the Java int that
	// UsbDeviceConnection.bulkTransfer takes.
	Read(maxLen int, timeoutMillis int64) ([]byte, error)

	// RaiseDTR raises the DTR line; over USB CDC-ACM, a
	// SET_CONTROL_LINE_STATE request. A transport without control lines
	// returns an error.
	RaiseDTR() error

	// DropDTR drops the DTR line, the last resort for ending a call the
	// handset will not hang up otherwise: with AT&D2, which data calls set,
	// the handset then ends the call. A transport without control lines
	// returns an error.
	DropDTR() error
}

// foreignTransport is the handset's iridium.Transport over the app's
// ByteTransport. It never closes the ByteTransport, which stays the app's.
type foreignTransport struct {
	transport ByteTransport
}

// A foreignTransport is a handset's port with a DTR line, as the app's
// transport is.
var (
	_ iridium.Transport     = (*foreignTransport)(nil)
	_ iridium.DTRController = (*foreignTransport)(nil)
)

// newForeignTransport returns the iridium.Transport over transport.
func newForeignTransport(transport ByteTransport) *foreignTransport {
	return &foreignTransport{transport: transport}
}

// WriteAll writes p through the app's transport; nothing at all is written
// for an empty p, which the app would receive as null.
func (f *foreignTransport) WriteAll(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	return hostError(f.transport.Write(p))
}

// ReadTimeout reads up to len(p) bytes through the app's transport, which
// waits whole milliseconds within the range a Java int holds, and copies at
// most len(p) of what it returns.
func (f *foreignTransport) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	data, err := f.transport.Read(len(p), transportWaitMillis(timeout))
	if err != nil {
		return 0, hostError(err)
	}
	return copy(p, data), nil
}

// RaiseDTR raises DTR through the app's transport.
func (f *foreignTransport) RaiseDTR() error {
	return hostError(f.transport.RaiseDTR())
}

// DropDTR drops DTR through the app's transport.
func (f *foreignTransport) DropDTR() error {
	return hostError(f.transport.DropDTR())
}

// hostError reports err, the app transport's failure, as the handset
// reports a transport's: ErrTransport with the failure's message. A
// SatlinkError of kind SatlinkErrorTransport gives its Reason, and a
// *iridium.Error of kind ErrTransport — from a
// transport written in Go — passes unchanged, as the handset passes it. A
// nil err stays nil.
func hostError(err error) error {
	if err == nil {
		return nil
	}
	var core *iridium.Error
	if errors.As(err, &core) && core.Kind == iridium.ErrTransport {
		return core
	}
	var own *SatlinkError
	if errors.As(err, &own) && own.Kind == SatlinkErrorTransport {
		return &iridium.Error{Kind: iridium.ErrTransport, Message: own.Reason}
	}
	return &iridium.Error{Kind: iridium.ErrTransport, Message: err.Error()}
}
