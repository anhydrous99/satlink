package iridium

import (
	"fmt"

	"github.com/anhydrous99/satlink/internal/debugtext"
)

// noCarrier is the result code of a call that ended, or of hanging up when
// none was up.
const noCarrier = "NO CARRIER"

// ErrorKind classifies an [Error].
//
// Each kind is itself an error, a sentinel for errors.Is:
// errors.Is(err, iridium.ErrTimeout) reports whether err is, or wraps, an
// *Error of kind ErrTimeout. A kind's own text is the short label that also
// starts the message of every Error of that kind. [ErrCallMayStillBeUp]
// matches more than its own kind: every error that means the call may still
// be up.
type ErrorKind int

// The kinds of [Error], and the fields of Error each one fills.
const (
	// ErrTransport reports that reading from or writing to the transport
	// failed; Message and, when there is one, the cause in Err say how.
	ErrTransport ErrorKind = iota

	// ErrTimeout reports that no final result arrived in time; Command is
	// the command that was waiting.
	ErrTimeout

	// ErrCommandFailed reports that the handset answered ERROR; Command is
	// the command it refused.
	ErrCommandFailed

	// ErrEquipment reports "+CME ERROR: <code>", an equipment or network
	// error, numeric or verbose; Code is the code.
	ErrEquipment

	// ErrSMS reports "+CMS ERROR: <code>", an SMS service error; Code is the
	// code.
	ErrSMS

	// ErrCall reports that call setup ended with NO CARRIER, BUSY, NO ANSWER
	// or NO DIALTONE, or that a call misbehaved; Message is the result.
	ErrCall

	// ErrParse reports an answer the handset was not expected to give;
	// Message describes it.
	ErrParse

	// ErrInvalidArgument reports an argument refused before anything was
	// sent to the handset; Message says why.
	ErrInvalidArgument

	// ErrBusy reports that a data call holds the handset, from the start
	// of its dial or answer: hang it up (or close the satnet link using it)
	// first.
	ErrBusy

	// ErrCallMayStillBeUp reports that a data call could not be ended, so
	// that it may still be up and billing; Message says what failed.
	// [Handset.Initialize] and [Handset.Answer] report it. Retry with
	// [Handset.EndDataCall] once the handset answers again; power-cycling
	// it is the last resort.
	//
	// As a sentinel it matches every error that means the call may still be
	// up, through any wrapping: an *Error of this kind, an *Error of any
	// kind whose CallMayStillBeUp is set — a hang-up that could not be
	// confirmed — and the satnet errors that carry the same flag.
	ErrCallMayStillBeUp

	// ErrCancelled reports that the call setup was stopped with a
	// [Cancellation].
	ErrCancelled
)

// kindLabels are the kinds' labels, which begin each error's message.
var kindLabels = [...]string{
	ErrTransport:        "transport error",
	ErrTimeout:          "timed out",
	ErrCommandFailed:    "handset returned ERROR",
	ErrEquipment:        "+CME ERROR",
	ErrSMS:              "+CMS ERROR",
	ErrCall:             "call failed",
	ErrParse:            "unexpected response",
	ErrInvalidArgument:  "invalid argument",
	ErrBusy:             "the handset is in a data call",
	ErrCallMayStillBeUp: "the call may still be up",
	ErrCancelled:        "the call setup was cancelled",
}

// Error returns the kind's label, such as "transport error".
func (k ErrorKind) Error() string {
	if k < 0 || int(k) >= len(kindLabels) {
		return fmt.Sprintf("iridium.ErrorKind(%d)", int(k))
	}
	return kindLabels[k]
}

// Error is what a [Handset], a [DataCall] or a transport reports. Kind says
// what went wrong and which of the other fields carry the details.
type Error struct {
	// Kind classifies the error.
	Kind ErrorKind

	// Command is the command that timed out or was refused (ErrTimeout,
	// ErrCommandFailed).
	Command string

	// Code is the error code the handset reported (ErrEquipment, ErrSMS).
	Code string

	// Message describes the failure (ErrTransport, ErrCall, ErrParse,
	// ErrInvalidArgument, ErrCallMayStillBeUp).
	Message string

	// Err is the underlying cause, if any; usually the error a transport
	// returned (ErrTransport).
	Err error

	// CallMayStillBeUp reports that the data call may still be up and
	// billing: always on an error of kind [ErrCallMayStillBeUp], and on an
	// error of another kind when the operation that returned it ended with
	// a hang-up that could not be confirmed — neither ATH after the escape
	// nor a DTR drop was. It is set on the errors of
	// [DataCall.HangUp] and [Handset.EndDataCall] once they have tried to
	// hang up, and on those of a [Handset.DialData] or [Handset.AnswerData]
	// whose abandoned attempt might have connected and could not be ended.
	// It does not change the error's text; errors.Is(err,
	// ErrCallMayStillBeUp) tests it.
	CallMayStillBeUp bool
}

// Error returns the message, for example
// `timed out waiting for a response to "AT+CSQ"`.
func (e *Error) Error() string {
	switch e.Kind {
	case ErrTransport, ErrCall, ErrParse, ErrInvalidArgument, ErrCallMayStillBeUp:
		return e.Kind.Error() + ": " + e.messageOrCause()
	case ErrTimeout:
		return "timed out waiting for a response to " + debugtext.Quote(e.Command)
	case ErrCommandFailed:
		return "handset returned ERROR for " + debugtext.Quote(e.Command)
	case ErrEquipment, ErrSMS:
		return e.Kind.Error() + ": " + e.Code
	default:
		return e.Kind.Error()
	}
}

// Is reports whether target is e's kind, so that errors.Is(err, ErrTimeout)
// matches every *Error of kind ErrTimeout, or [ErrCallMayStillBeUp] when
// e.CallMayStillBeUp is set, whatever e's kind.
func (e *Error) Is(target error) bool {
	if target == ErrCallMayStillBeUp && e.CallMayStillBeUp {
		return true
	}
	kind, ok := target.(ErrorKind)
	return ok && kind == e.Kind
}

// Unwrap returns the underlying cause, if any.
func (e *Error) Unwrap() error {
	return e.Err
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

// wrapTransportError reports err, a transport's failure, as an *Error: an
// *Error passes unchanged, anything else becomes ErrTransport wrapping it.
// A nil err stays nil.
func wrapTransportError(err error) error {
	if err == nil {
		return nil
	}
	return asError(err)
}

// asError returns err, which is not nil, as an *Error, wrapping it as a
// transport error if it is not one.
func asError(err error) *Error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{Kind: ErrTransport, Err: err}
}

// callMayStillBeUp returns err, the error of a hang-up that failed, with
// CallMayStillBeUp set. It flags a copy: the *Error may be a transport's
// own, which it may hand out again.
func callMayStillBeUp(err error) error {
	flagged := *asError(err)
	flagged.CallMayStillBeUp = true
	return &flagged
}

// invalidArgument reports an argument refused before anything was sent.
func invalidArgument(message string) error {
	return &Error{Kind: ErrInvalidArgument, Message: message}
}

// parseError reports an answer the handset was not expected to give.
func parseError(message string) error {
	return &Error{Kind: ErrParse, Message: message}
}

// callError reports a call that failed or misbehaved.
func callError(message string) error {
	return &Error{Kind: ErrCall, Message: message}
}

// noCarrierError reports NO CARRIER: the call is over.
func noCarrierError() error {
	return callError(noCarrier)
}

// isNoCarrier reports whether err is a command's NO CARRIER.
func isNoCarrier(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Kind == ErrCall && e.Message == noCarrier
}

// callMayStillBeUpError reports a data call that could not be ended, so
// that it may still be up; message says what failed.
func callMayStillBeUpError(message string) error {
	return &Error{Kind: ErrCallMayStillBeUp, Message: message, CallMayStillBeUp: true}
}

// cancelledError reports a call setup stopped with a Cancellation.
func cancelledError() error {
	return &Error{Kind: ErrCancelled}
}

// timeoutError reports that command got no final result in time.
func timeoutError(command string) error {
	return &Error{Kind: ErrTimeout, Command: command}
}

// busyError reports that a data call holds the handset.
func busyError() error {
	return &Error{Kind: ErrBusy}
}
