package satnet

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"syscall"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/debugtext"
)

// ErrorKind classifies an [Error].
//
// Each kind is itself an error, a sentinel for errors.Is:
// errors.Is(err, satnet.ErrPolicyDenied) reports whether err is, or wraps,
// an *Error of kind ErrPolicyDenied. A kind's own text is a short label.
type ErrorKind int

// The kinds of [Error], and the fields of Error each one fills.
const (
	// ErrHandset reports that the handset refused a command or the call
	// failed; Err is the *satlink.Error.
	ErrHandset ErrorKind = iota

	// ErrInvalidConfig reports a configuration that cannot work; Message
	// says why, or, when PPP refuses the credentials as longer than 255
	// bytes, Err is the *ppp.ConfigError.
	ErrInvalidConfig

	// ErrPolicyDenied reports a destination the link policy does not allow;
	// Destination is the refused host or address, with the port where one
	// applies.
	ErrPolicyDenied

	// ErrInvalidHost reports something that is not a valid host name; Host
	// is what was given.
	ErrInvalidHost

	// ErrNameNotFound reports that DNS has no address for Host.
	ErrNameNotFound

	// ErrNoDNSServer reports that the network offered no DNS server, and
	// the name is not among the local hosts.
	ErrNoDNSServer

	// ErrTimedOut reports that Operation, such as "TCP read", did not
	// finish in time.
	ErrTimedOut

	// ErrConnectionFailed reports that a connection to Addr could not be
	// established: refused, unreachable or timed out.
	ErrConnectionFailed

	// ErrConnectionReset reports that the peer reset an established
	// connection.
	ErrConnectionReset

	// ErrDatagramTooLarge reports a datagram of Len bytes, more than the Max
	// the link carries.
	ErrDatagramTooLarge

	// ErrTLS reports that the TLS handshake or session failed (certificate,
	// protocol, …); Message says how, and Err is the crypto/tls or
	// crypto/x509 error when there is one.
	ErrTLS

	// ErrTLSTruncated reports that the server closed the connection without
	// ending the TLS session first, so what was received may be cut short.
	ErrTLSTruncated

	// ErrLinkDown reports that the satellite link is going or gone, so
	// nothing more can be sent; Reason says why it ended.
	ErrLinkDown
)

// errorKindLabels are the kinds' own texts, indexed by kind.
var errorKindLabels = [...]string{
	ErrHandset:          "handset error",
	ErrInvalidConfig:    "invalid configuration",
	ErrPolicyDenied:     "not allowed by the link policy",
	ErrInvalidHost:      "invalid host name",
	ErrNameNotFound:     "no address found",
	ErrNoDNSServer:      "the link has no DNS server",
	ErrTimedOut:         "timed out",
	ErrConnectionFailed: "could not connect",
	ErrConnectionReset:  "the connection was reset",
	ErrDatagramTooLarge: "datagram too large",
	ErrTLS:              "TLS error",
	ErrTLSTruncated:     "the server closed the connection without ending the TLS session",
	ErrLinkDown:         "the satellite link is down",
}

// Error returns the kind's label, such as "not allowed by the link policy".
func (k ErrorKind) Error() string {
	if k < 0 || int(k) >= len(errorKindLabels) {
		return fmt.Sprintf("satnet.ErrorKind(%d)", int(k))
	}
	return errorKindLabels[k]
}

// Error is what the satellite link and its connections report. Kind says
// what went wrong and which of the other fields carry the details.
type Error struct {
	// Kind classifies the error.
	Kind ErrorKind
	// Destination is the refused destination (ErrPolicyDenied).
	Destination string
	// Host is the name concerned (ErrInvalidHost, ErrNameNotFound).
	Host string
	// Operation is what timed out, such as "TCP connect" (ErrTimedOut).
	Operation string
	// Addr is the address that could not be reached (ErrConnectionFailed).
	Addr netip.AddrPort
	// Len is the size of the refused datagram (ErrDatagramTooLarge).
	Len int
	// Max is the largest datagram the link carries (ErrDatagramTooLarge).
	Max int
	// Reason is why the link ended (ErrLinkDown).
	Reason CloseReason
	// Message describes the failure (ErrInvalidConfig, ErrTLS).
	Message string
	// Err is the underlying cause: the *satlink.Error (ErrHandset), the
	// *ppp.ConfigError of credentials PPP refuses (ErrInvalidConfig), or the
	// crypto/tls or crypto/x509 error (ErrTLS).
	Err error
	// CallMayStillBeUp reports that the link's hang-up could not be
	// confirmed — neither ATH after the escape nor a DTR drop was — so that
	// the call may still be up and billing. It is
	// set on the error of [Link.Close] when the hang-up failed, and on that
	// of [Establish] when the link did not come up and hanging up failed
	// too. It does not change the error's text; errors.Is(err,
	// satlink.ErrCallMayStillBeUp) tests it.
	CallMayStillBeUp bool
}

// Error returns the message, for example "echo.test:80 is not allowed by
// the link policy".
func (e *Error) Error() string {
	if e.Kind < 0 || int(e.Kind) >= len(errorMessages) || errorMessages[e.Kind] == nil {
		return e.Kind.Error()
	}
	return errorMessages[e.Kind](e)
}

// errorMessages words the message of each kind that has details; the
// message of any other kind is its label.
var errorMessages = [...]func(e *Error) string{
	ErrHandset:          func(e *Error) string { return "handset: " + e.messageOrCause() },
	ErrInvalidConfig:    func(e *Error) string { return "invalid configuration: " + e.messageOrCause() },
	ErrPolicyDenied:     func(e *Error) string { return e.Destination + " is not allowed by the link policy" },
	ErrInvalidHost:      func(e *Error) string { return debugtext.Quote(e.Host) + " is not a valid host name" },
	ErrNameNotFound:     func(e *Error) string { return "no address found for " + e.Host },
	ErrTimedOut:         func(e *Error) string { return e.Operation + " timed out" },
	ErrConnectionFailed: func(e *Error) string { return "could not connect to " + e.Addr.String() },
	ErrDatagramTooLarge: func(e *Error) string {
		return fmt.Sprintf("a %d-byte datagram exceeds the %d-byte limit", e.Len, e.Max)
	},
	ErrTLS:      func(e *Error) string { return "TLS: " + e.messageOrCause() },
	ErrLinkDown: func(e *Error) string { return "the satellite link is down: " + e.Reason.String() },
}

// Is reports whether target is e's kind, or the standard library's
// equivalent of it listed in the package documentation, such as
// os.ErrDeadlineExceeded for ErrTimedOut, or satlink.ErrCallMayStillBeUp
// when e.CallMayStillBeUp is set.
func (e *Error) Is(target error) bool {
	if target == satlink.ErrCallMayStillBeUp {
		return e.CallMayStillBeUp
	}
	if kind, ok := target.(ErrorKind); ok {
		return kind == e.Kind
	}
	return isStandardEquivalent(e.Kind, target)
}

// Unwrap returns the underlying cause, if any.
func (e *Error) Unwrap() error {
	return e.Err
}

// Timeout reports whether the error is a timeout (ErrTimedOut), as
// net.Error asks.
func (e *Error) Timeout() bool {
	return e.Kind == ErrTimedOut
}

// Temporary reports whether retrying may succeed, which is true for
// timeouts only. net.Error still declares it, and crypto/tls keeps a session
// usable after a read error only when it is temporary.
func (e *Error) Temporary() bool {
	return e.Kind == ErrTimedOut
}

// messageOrCause is the error's free text: the message, followed by the
// cause when there is one.
func (e *Error) messageOrCause() string {
	switch {
	case e.Err == nil:
		return e.Message
	case e.Message == "":
		return e.Err.Error()
	default:
		return e.Message + ": " + e.Err.Error()
	}
}

// isStandardEquivalent reports whether target is the standard library's
// error for kind. A link that is down is not net.ErrClosed, which means
// that this side closed the connection, and a deadline is not
// context.DeadlineExceeded, which the net package keeps for contexts.
func isStandardEquivalent(kind ErrorKind, target error) bool {
	switch kind {
	case ErrTimedOut:
		return target == os.ErrDeadlineExceeded
	case ErrLinkDown:
		return target == syscall.ENOTCONN
	case ErrPolicyDenied:
		return target == os.ErrPermission
	case ErrInvalidConfig, ErrInvalidHost, ErrDatagramTooLarge:
		return target == os.ErrInvalid
	case ErrConnectionReset:
		return target == syscall.ECONNRESET
	case ErrConnectionFailed:
		return target == syscall.ECONNREFUSED
	case ErrTLSTruncated:
		return target == io.ErrUnexpectedEOF
	default:
		return false
	}
}

// The operations an ErrTimedOut names.
const (
	operationLinkUp     = "bringing the link up"
	operationDNSLookup  = "DNS lookup"
	operationTCPConnect = "TCP connect"
	operationTCPRead    = "TCP read"
	operationTCPWrite   = "TCP write"
	operationTCPFlush   = "TCP flush"
	operationUDPSend    = "UDP send"
	operationUDPReceive = "UDP receive"
)

// handsetError is the error of the handset's failure err, which carries
// over whether the call may still be up.
func handsetError(err error) *Error {
	return &Error{Kind: ErrHandset, Err: err, CallMayStillBeUp: errors.Is(err, satlink.ErrCallMayStillBeUp)}
}
