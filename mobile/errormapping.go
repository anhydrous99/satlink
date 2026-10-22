package mobile

import (
	"errors"
	"net"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/satnet"
)

// How the errors of the packages underneath reach the app.

// connectionClosed is the reason given for a connection used after Close,
// which the satnet connections report as net.ErrClosed.
const connectionClosed = "the connection is closed"

// strandedReason is the reason a stranded IridiumHandset gives: a data call
// it was in could not be ended, so it may still be up.
const strandedReason = "the last data call could not be hung up; reinitialize ends it"

// handsetClosed is the reason an IridiumHandset the app has closed gives
// for refusing to work: every call on it fails once it is closed.
const handsetClosed = "the handset is closed"

// fromError returns err as the error the app sees, a *SatlinkError; nil
// stays nil.
func fromError(err error) error {
	if err == nil {
		return nil
	}
	return satlinkErrorOf(err)
}

// satlinkErrorOf returns the SatlinkError for err, which is not nil. The
// order matters, as satnet's errors also match the standard library's: a
// *SatlinkError passes unchanged, then a *satnet.Error, a *satlink.Error
// and a *satnet.ParseRuleError are converted, and only then is net.ErrClosed
// a closed connection. Any other error is none this package expects from
// the libraries underneath, a fault inside the library.
//
// A handset error of kind satlink.ErrCallMayStillBeUp is a failed hang-up.
// The CallMayStillBeUp flag on an error of another kind is ignored here, as
// this conversion never sees it: establishError checks it, and
// SatelliteLink.HangUp reads the link's status instead.
func satlinkErrorOf(err error) *SatlinkError {
	var (
		own       *SatlinkError
		netError  *satnet.Error
		coreError *satlink.Error
		ruleError *satnet.ParseRuleError
	)
	switch {
	case errors.As(err, &own):
		return own
	case errors.As(err, &netError):
		return fromNetError(netError)
	case errors.As(err, &coreError):
		return fromCoreError(coreError)
	case errors.As(err, &ruleError):
		return invalidArgument(ruleError.Error())
	case errors.Is(err, net.ErrClosed):
		return invalidArgument(connectionClosed)
	default:
		return unexpectedError(err)
	}
}

// establishError reports why a link could not be established:
// SatlinkErrorHangUpFailed, with the
// error's text as Reason, when its call may still be up.
func establishError(err error) error {
	if errors.Is(err, satlink.ErrCallMayStillBeUp) {
		return hangUpFailed(err.Error())
	}
	return fromError(err)
}

// hangUpFailed reports a hang-up that could not be confirmed, so that the
// call may still be up; reason says why the link ended or did not come up.
func hangUpFailed(reason string) *SatlinkError {
	return &SatlinkError{Kind: SatlinkErrorHangUpFailed, Reason: reason}
}

// unexpectedError reports an error of a kind this package does not know,
// which only a fault inside the library would return.
func unexpectedError(err error) *SatlinkError {
	return &SatlinkError{Kind: SatlinkErrorInternal, Reason: err.Error()}
}

// fromCoreError converts a handset's error, with ErrBusy as SatlinkErrorHandsetBusy and
// ErrCallMayStillBeUp as SatlinkErrorHangUpFailed.
func fromCoreError(e *satlink.Error) *SatlinkError {
	if e.Kind < 0 || int(e.Kind) >= len(coreErrorConversions) || coreErrorConversions[e.Kind] == nil {
		return unexpectedError(e)
	}
	return coreErrorConversions[e.Kind](e)
}

// coreErrorConversions convert each kind of handset error.
var coreErrorConversions = [...]func(e *satlink.Error) *SatlinkError{
	satlink.ErrTransport: func(e *satlink.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorTransport, Reason: messageOrCause(e.Message, e.Err)}
	},
	satlink.ErrTimeout: func(e *satlink.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorTimeout, Command: e.Command}
	},
	satlink.ErrCommandFailed: func(e *satlink.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorCommandFailed, Command: e.Command}
	},
	satlink.ErrEquipment: func(e *satlink.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorEquipment, Code: e.Code}
	},
	satlink.ErrSMS: func(e *satlink.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorSMS, Code: e.Code}
	},
	satlink.ErrCall: func(e *satlink.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorCall, Result: messageOrCause(e.Message, e.Err)}
	},
	satlink.ErrParse: func(e *satlink.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorParse, Reason: messageOrCause(e.Message, e.Err)}
	},
	satlink.ErrInvalidArgument: func(e *satlink.Error) *SatlinkError {
		return invalidArgument(messageOrCause(e.Message, e.Err))
	},
	satlink.ErrBusy: func(*satlink.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorHandsetBusy}
	},
	satlink.ErrCallMayStillBeUp: func(e *satlink.Error) *SatlinkError {
		return hangUpFailed(messageOrCause(e.Message, e.Err))
	},
	satlink.ErrCancelled: func(*satlink.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorCancelled}
	},
}

// fromNetError converts a link's error.
func fromNetError(e *satnet.Error) *SatlinkError {
	if e.Kind < 0 || int(e.Kind) >= len(netErrorConversions) || netErrorConversions[e.Kind] == nil {
		return unexpectedError(e)
	}
	return netErrorConversions[e.Kind](e)
}

// netErrorConversions convert each kind of link error.
var netErrorConversions = [...]func(e *satnet.Error) *SatlinkError{
	satnet.ErrHandset: fromHandsetFailure,
	satnet.ErrInvalidConfig: func(e *satnet.Error) *SatlinkError {
		return invalidArgument(messageOrCause(e.Message, e.Err))
	},
	satnet.ErrPolicyDenied: func(e *satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorPolicyDenied, Destination: e.Destination}
	},
	satnet.ErrInvalidHost: func(e *satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorInvalidHost, Host: e.Host}
	},
	satnet.ErrNameNotFound: func(e *satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorNameNotFound, Host: e.Host}
	},
	satnet.ErrNoDNSServer: func(*satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorNoDNSServer}
	},
	satnet.ErrTimedOut: func(e *satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorTimeout, Command: e.Operation}
	},
	satnet.ErrConnectionFailed: func(e *satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorConnectionFailed, Address: e.Addr.String()}
	},
	satnet.ErrConnectionReset: func(*satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorConnectionReset}
	},
	satnet.ErrDatagramTooLarge: func(e *satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorDatagramTooLarge, Len: e.Len, Max: e.Max}
	},
	satnet.ErrTLS: func(e *satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorTLS, Reason: messageOrCause(e.Message, e.Err)}
	},
	satnet.ErrTLSTruncated: func(*satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorTLSTruncated}
	},
	satnet.ErrLinkDown: func(e *satnet.Error) *SatlinkError {
		return &SatlinkError{Kind: SatlinkErrorLinkDown, Reason: e.Reason.String()}
	},
}

// fromHandsetFailure converts a link's ErrHandset into the handset's own
// error, which it wraps.
func fromHandsetFailure(e *satnet.Error) *SatlinkError {
	var core *satlink.Error
	if !errors.As(e.Err, &core) {
		return unexpectedError(e)
	}
	return fromCoreError(core)
}

// messageOrCause is an error's free text, as the errors underneath word it:
// the message, followed by the cause when there is one.
func messageOrCause(message string, cause error) string {
	switch {
	case cause == nil:
		return message
	case message == "":
		return cause.Error()
	default:
		return message + ": " + cause.Error()
	}
}
