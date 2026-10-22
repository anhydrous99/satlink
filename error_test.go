package satlink

import (
	"errors"
	"fmt"
	"io"
	"testing"
)

// allKinds are the error kinds, each with its label.
var allKinds = map[ErrorKind]string{
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

func TestErrorKindLabels(t *testing.T) {
	for kind, want := range allKinds {
		if got := kind.Error(); got != want {
			t.Errorf("ErrorKind(%d).Error() = %q, want %q", int(kind), got, want)
		}
	}
	for _, unknown := range []ErrorKind{99, -1} {
		if got, want := unknown.Error(), fmt.Sprintf("satlink.ErrorKind(%d)", int(unknown)); got != want {
			t.Errorf("an unknown kind's label = %q, want %q", got, want)
		}
	}
}

func TestErrorTextsAreTheRustOnes(t *testing.T) {
	tests := []struct {
		err  *Error
		want string
	}{
		{&Error{Kind: ErrTransport, Message: "unplugged"}, "transport error: unplugged"},
		{&Error{Kind: ErrTransport, Err: io.EOF}, "transport error: EOF"},
		{&Error{Kind: ErrTransport, Message: "read", Err: io.EOF}, "transport error: read: EOF"},
		{&Error{Kind: ErrTimeout, Command: "AT+CSQ"}, `timed out waiting for a response to "AT+CSQ"`},
		{&Error{Kind: ErrCommandFailed, Command: "AT+BOGUS"}, `handset returned ERROR for "AT+BOGUS"`},
		// Rust's {:?} of a str, where Go's %q would write "AT\a" and "AT\x01".
		{&Error{Kind: ErrTimeout, Command: "AT\a"}, `timed out waiting for a response to "AT\u{7}"`},
		{&Error{Kind: ErrCommandFailed, Command: "AT\x01"}, `handset returned ERROR for "AT\u{1}"`},
		{&Error{Kind: ErrEquipment, Code: "SIM not inserted"}, "+CME ERROR: SIM not inserted"},
		{&Error{Kind: ErrSMS, Code: "321"}, "+CMS ERROR: 321"},
		{&Error{Kind: ErrCall, Message: "BUSY"}, "call failed: BUSY"},
		{&Error{Kind: ErrParse, Message: "AT+CGMI returned no data"}, "unexpected response: AT+CGMI returned no data"},
		{&Error{Kind: ErrInvalidArgument, Message: `"1;" is not a dialable number`}, `invalid argument: "1;" is not a dialable number`},
		{&Error{Kind: ErrBusy}, "the handset is in a data call"},
		{&Error{Kind: ErrCall, Message: "BUSY", CallMayStillBeUp: true}, "call failed: BUSY"},
		{&Error{Kind: ErrCallMayStillBeUp, Message: "ATH failed"}, "the call may still be up: ATH failed"},
		{&Error{Kind: ErrCancelled}, "the call setup was cancelled"},
	}
	for _, test := range tests {
		if got := test.err.Error(); got != test.want {
			t.Errorf("Error() = %q, want %q", got, test.want)
		}
	}
}

func TestErrorKindsAreSentinels(t *testing.T) {
	for kind := range allKinds {
		err := &Error{Kind: kind}
		wrapped := fmt.Errorf("context: %w", err)
		for other := range allKinds {
			if got, want := errors.Is(wrapped, other), other == kind; got != want {
				t.Errorf("errors.Is(%v error, %v) = %v, want %v", kind, other, got, want)
			}
		}
	}
}

func TestCallMayStillBeUpIsMatchedByItsSentinel(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"flag set", &Error{Kind: ErrTimeout, Command: "+++", CallMayStillBeUp: true}, true},
		{"flag not set", &Error{Kind: ErrTimeout, Command: "+++"}, false},
		{"flag set, wrapped", fmt.Errorf("hanging up: %w", &Error{Kind: ErrCall, CallMayStillBeUp: true}), true},
		{"flag not set, wrapped", fmt.Errorf("hanging up: %w", &Error{Kind: ErrCall}), false},
		{"its kind", callMayStillBeUpError("ATH failed"), true},
		{"its kind without the flag", &Error{Kind: ErrCallMayStillBeUp, Message: "ATH failed"}, true},
		{"its kind, wrapped", fmt.Errorf("initialising: %w", callMayStillBeUpError("ATH failed")), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := errors.Is(test.err, ErrCallMayStillBeUp); got != test.want {
				t.Errorf("errors.Is(%v, ErrCallMayStillBeUp) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestTheFlagLeavesTheKindMatching(t *testing.T) {
	err := &Error{Kind: ErrTimeout, Command: "+++", CallMayStillBeUp: true}

	if !errors.Is(err, ErrTimeout) {
		t.Error("a flagged error no longer matches its kind")
	}
}

func TestUnwrapReturnsTheCause(t *testing.T) {
	err := &Error{Kind: ErrTransport, Err: io.ErrUnexpectedEOF}

	if !errors.Is(err, io.ErrUnexpectedEOF) || err.Unwrap() != io.ErrUnexpectedEOF {
		t.Errorf("%v does not unwrap to its cause", err)
	}
	if (&Error{Kind: ErrCall}).Unwrap() != nil {
		t.Error("an error without a cause unwraps to one")
	}
}

func TestATransportsErrorIsWrappedOnceAsATransportError(t *testing.T) {
	own := &Error{Kind: ErrTransport, Message: "unplugged"}

	if wrapTransportError(nil) != nil {
		t.Error("no error became one")
	}
	if got := wrapTransportError(own); got != own {
		t.Errorf("a transport's *Error became %v", got)
	}
	wrapped := wrapTransportError(io.EOF)
	if !errors.Is(wrapped, ErrTransport) || !errors.Is(wrapped, io.EOF) {
		t.Errorf("io.EOF became %v, want an ErrTransport wrapping it", wrapped)
	}
}

func TestFlaggingAnErrorLeavesTheOriginalAlone(t *testing.T) {
	// A transport may hand out the same *Error again.
	original := &Error{Kind: ErrTransport, Message: "unplugged"}

	flagged := callMayStillBeUp(original)

	if original.CallMayStillBeUp {
		t.Error("the original error was flagged")
	}
	if !errors.Is(flagged, ErrCallMayStillBeUp) || flagged.Error() != original.Error() {
		t.Errorf("flagged = %#v, want the original's copy with the flag", flagged)
	}
	if !errors.Is(callMayStillBeUp(io.EOF), ErrTransport) {
		t.Error("a foreign error was not reported as a transport error")
	}
}
