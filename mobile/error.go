package mobile

import (
	"errors"
	"fmt"

	"github.com/anhydrous99/satlink/internal/debugtext"
)

// The kinds of [SatlinkError], and the fields of SatlinkError each one
// fills.
const (
	// SatlinkErrorTransport reports that talking to the handset failed;
	// Reason says how.
	SatlinkErrorTransport = iota
	// SatlinkErrorTimeout reports that the handset or the link did not
	// answer in time; Command is the command or operation that waited.
	SatlinkErrorTimeout
	// SatlinkErrorCommandFailed reports that the handset answered ERROR to
	// Command.
	SatlinkErrorCommandFailed
	// SatlinkErrorEquipment reports +CME ERROR, an equipment or network
	// error; Code is its code.
	SatlinkErrorEquipment
	// SatlinkErrorSMS reports +CMS ERROR, an SMS service error; Code is its
	// code.
	SatlinkErrorSMS
	// SatlinkErrorCall reports that call setup failed; Result is the
	// handset's result, such as NO CARRIER or BUSY.
	SatlinkErrorCall
	// SatlinkErrorParse reports that the handset gave an unexpected
	// answer; Reason describes it.
	SatlinkErrorParse
	// SatlinkErrorInvalidArgument reports an argument or setting refused
	// before anything was sent; Reason says why.
	SatlinkErrorInvalidArgument
	// SatlinkErrorPolicyDenied reports that the link settings do not allow
	// Destination.
	SatlinkErrorPolicyDenied
	// SatlinkErrorInvalidHost reports that Host is not a valid host name.
	SatlinkErrorInvalidHost
	// SatlinkErrorNameNotFound reports that DNS has no address for Host.
	SatlinkErrorNameNotFound
	// SatlinkErrorNoDNSServer reports that the link has no DNS server.
	SatlinkErrorNoDNSServer
	// SatlinkErrorConnectionFailed reports that a connection to Address
	// could not be established.
	SatlinkErrorConnectionFailed
	// SatlinkErrorConnectionReset reports that the peer reset the
	// connection.
	SatlinkErrorConnectionReset
	// SatlinkErrorDatagramTooLarge reports a datagram of Len bytes, more
	// than the Max the link carries.
	SatlinkErrorDatagramTooLarge
	// SatlinkErrorTLS reports that the TLS handshake or session failed;
	// Reason says how.
	SatlinkErrorTLS
	// SatlinkErrorTLSTruncated reports that the server closed the
	// connection without ending the TLS session, so what was received may
	// be cut short.
	SatlinkErrorTLSTruncated
	// SatlinkErrorLinkDown reports that the satellite link is down; Reason
	// says why it ended.
	SatlinkErrorLinkDown
	// SatlinkErrorHandsetBusy reports that a satellite link has the handset
	// until its call ends.
	SatlinkErrorHandsetBusy
	// SatlinkErrorHangUpFailed reports that the hang-up was not confirmed,
	// so the call may still be up and billing. Reason says why: why the
	// link ended (SatelliteLink.HangUp), why it did not come up (the
	// EstablishLink functions), what failed in ending a data call
	// (IridiumHandset.Answer, Reinitialize), or that the handset's last data
	// call could not be hung up (a stranded IridiumHandset).
	// IridiumHandset.Reinitialize tries to end the call; power-cycling the
	// handset is the last resort. After EstablishLink, which leaves no
	// handset to reinitialize, make an IridiumHandset on the transport and
	// call its HangUp — after EstablishSerialLink, once the link has closed
	// its port, which only one may open: it does so a moment after the call
	// has ended. Making an IridiumHandset or establishing a link fails with
	// it too when the handset does not answer and a data call it may be
	// relaying could not be ended: try again later, or power-cycle the
	// handset.
	SatlinkErrorHangUpFailed
	// SatlinkErrorInternal reports a fault inside the library, recovered
	// rather than ending the app; Reason describes it. After one from
	// EstablishLink or EstablishLinkWithHandset, whether a call is up is
	// unknown: end any as after SatlinkErrorHangUpFailed, with Reinitialize
	// on the lent IridiumHandset, which is stranded, or with an
	// IridiumHandset made on EstablishLink's transport. EstablishSerialLink
	// closes its port, which drops DTR and so ends a call.
	SatlinkErrorInternal
	// SatlinkErrorCancelled reports that a Cancellation stopped the link
	// being set up; no call is left up.
	SatlinkErrorCancelled
)

// SatlinkError is what every failing call of this package reports. Kind is
// one of the SatlinkError constants and says which other fields carry the
// details.
type SatlinkError struct {
	// Kind classifies the error.
	Kind int
	// Reason describes the failure (Transport, Parse, InvalidArgument, TLS,
	// LinkDown, HangUpFailed, Internal).
	Reason string
	// Command is the command or operation that timed out or was refused
	// (Timeout, CommandFailed).
	Command string
	// Code is the error code the handset reported (Equipment, SMS).
	Code string
	// Result is the call's result, such as "NO CARRIER" (Call).
	Result string
	// Destination is the refused destination (PolicyDenied).
	Destination string
	// Host is the name concerned (InvalidHost, NameNotFound).
	Host string
	// Address is the address that could not be reached (ConnectionFailed).
	Address string
	// Len is the size of the refused datagram (DatagramTooLarge).
	Len int
	// Max is the largest datagram the link carries (DatagramTooLarge).
	Max int
}

// Error returns the message, such as "echo.test:80 is not allowed by the
// link settings".
func (e *SatlinkError) Error() string {
	if e.Kind < 0 || e.Kind >= len(errorMessages) {
		return fmt.Sprintf("SatlinkError(kind %d): %s", e.Kind, e.Reason)
	}
	return errorMessages[e.Kind](e)
}

// errorMessages words each kind's message from the error's fields.
var errorMessages = [...]func(e *SatlinkError) string{
	SatlinkErrorTransport:        func(e *SatlinkError) string { return "transport: " + e.Reason },
	SatlinkErrorTimeout:          func(e *SatlinkError) string { return "timed out waiting for " + e.Command },
	SatlinkErrorCommandFailed:    func(e *SatlinkError) string { return "command failed: " + e.Command },
	SatlinkErrorEquipment:        func(e *SatlinkError) string { return "+CME ERROR: " + e.Code },
	SatlinkErrorSMS:              func(e *SatlinkError) string { return "+CMS ERROR: " + e.Code },
	SatlinkErrorCall:             func(e *SatlinkError) string { return "call failed: " + e.Result },
	SatlinkErrorParse:            func(e *SatlinkError) string { return "unexpected response: " + e.Reason },
	SatlinkErrorInvalidArgument:  func(e *SatlinkError) string { return "invalid argument: " + e.Reason },
	SatlinkErrorPolicyDenied:     func(e *SatlinkError) string { return e.Destination + " is not allowed by the link settings" },
	SatlinkErrorInvalidHost:      func(e *SatlinkError) string { return debugtext.Quote(e.Host) + " is not a valid host name" },
	SatlinkErrorNameNotFound:     func(e *SatlinkError) string { return "no address found for " + e.Host },
	SatlinkErrorNoDNSServer:      func(*SatlinkError) string { return "the link has no DNS server" },
	SatlinkErrorConnectionFailed: func(e *SatlinkError) string { return "could not connect to " + e.Address },
	SatlinkErrorConnectionReset:  func(*SatlinkError) string { return "the connection was reset" },
	SatlinkErrorDatagramTooLarge: func(e *SatlinkError) string {
		return fmt.Sprintf("a %d-byte datagram exceeds the %d-byte limit", e.Len, e.Max)
	},
	SatlinkErrorTLS:          func(e *SatlinkError) string { return "TLS: " + e.Reason },
	SatlinkErrorTLSTruncated: func(*SatlinkError) string { return "the server closed the connection without ending the TLS session" },
	SatlinkErrorLinkDown:     func(e *SatlinkError) string { return "the satellite link is down: " + e.Reason },
	SatlinkErrorHandsetBusy:  func(*SatlinkError) string { return "the handset is in use by a satellite link" },
	SatlinkErrorHangUpFailed: func(e *SatlinkError) string {
		return "the hang-up was not confirmed, so the call may still be up (" + e.Reason + ")"
	},
	SatlinkErrorInternal:  func(e *SatlinkError) string { return "internal error: " + e.Reason },
	SatlinkErrorCancelled: func(*SatlinkError) string { return "the call setup was cancelled" },
}

// AsSatlinkError returns the details of err, an error one of this package's
// calls returned — in Java, the caught exception, which is a go.error — and
// nil for any other error.
func AsSatlinkError(err error) *SatlinkError {
	var satlinkError *SatlinkError
	if errors.As(err, &satlinkError) {
		return satlinkError
	}
	return nil
}
