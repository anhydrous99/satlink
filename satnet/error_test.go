package satnet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"syscall"
	"testing"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/ppp"
)

func TestErrorMessages(t *testing.T) {
	coreErr := &iridium.Error{Kind: iridium.ErrBusy}
	for _, c := range []struct {
		err  *Error
		text string
	}{
		{&Error{Kind: ErrHandset, Err: coreErr}, "handset: the handset is in a data call"},
		{&Error{Kind: ErrInvalidConfig, Message: "MTU too large"}, "invalid configuration: MTU too large"},
		{&Error{Kind: ErrPolicyDenied, Destination: "echo.test:80"}, "echo.test:80 is not allowed by the link policy"},
		{&Error{Kind: ErrInvalidHost, Host: "bad name"}, `"bad name" is not a valid host name`},
		// Control characters are escaped as \u{..}, where Go's %q would write
		// "a\x1bb".
		{&Error{Kind: ErrInvalidHost, Host: "a\x1bb"}, `"a\u{1b}b" is not a valid host name`},
		{&Error{Kind: ErrNameNotFound, Host: "missing.test"}, "no address found for missing.test"},
		{&Error{Kind: ErrNoDNSServer}, "the link has no DNS server"},
		{&Error{Kind: ErrTimedOut, Operation: operationTCPRead}, "TCP read timed out"},
		{&Error{Kind: ErrConnectionFailed, Addr: netip.MustParseAddrPort("192.0.2.7:7")}, "could not connect to 192.0.2.7:7"},
		{&Error{Kind: ErrConnectionReset}, "the connection was reset"},
		{&Error{Kind: ErrDatagramTooLarge, Len: 600, Max: 548}, "a 600-byte datagram exceeds the 548-byte limit"},
		{&Error{Kind: ErrTLS, Message: closedDuringHandshake}, "TLS: the server closed the connection during the handshake"},
		{&Error{Kind: ErrTLS, Message: invalidPEMMessage, Err: errMalformedPEMSection}, "TLS: invalid PEM: a section is malformed"},
		{&Error{Kind: ErrTLSTruncated}, "the server closed the connection without ending the TLS session"},
		{&Error{Kind: ErrLinkDown, Reason: CloseReason{Kind: CloseReasonCarrierLost}}, "the satellite link is down: the call dropped"},
		{&Error{Kind: ErrorKind(99)}, "satnet.ErrorKind(99)"},
	} {
		if got := c.err.Error(); got != c.text {
			t.Errorf("Error() = %q, want %q", got, c.text)
		}
	}
}

func TestErrorsMatchTheirKindsAndTheStandardEquivalents(t *testing.T) {
	for _, c := range []struct {
		kind       ErrorKind
		equivalent error
	}{
		{ErrTimedOut, os.ErrDeadlineExceeded},
		{ErrLinkDown, syscall.ENOTCONN},
		{ErrPolicyDenied, os.ErrPermission},
		{ErrInvalidConfig, os.ErrInvalid},
		{ErrInvalidHost, os.ErrInvalid},
		{ErrDatagramTooLarge, os.ErrInvalid},
		{ErrConnectionReset, syscall.ECONNRESET},
		{ErrConnectionFailed, syscall.ECONNREFUSED},
		{ErrTLSTruncated, io.ErrUnexpectedEOF},
	} {
		err := fmt.Errorf("context: %w", &Error{Kind: c.kind})
		if !errors.Is(err, c.kind) || !errors.Is(err, c.equivalent) {
			t.Errorf("%v does not match its kind and %v", c.kind, c.equivalent)
		}
		if errors.Is(err, ErrHandset) {
			t.Errorf("%v matches ErrHandset", c.kind)
		}
	}
}

func TestLookAlikesOfTheNetPackageAreNotMatched(t *testing.T) {
	for _, err := range []error{
		&Error{Kind: ErrLinkDown, Reason: CloseReason{Kind: CloseReasonRequested}},
		&Error{Kind: ErrTimedOut, Operation: operationTCPRead},
	} {
		if errors.Is(err, net.ErrClosed) || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%v matches a look-alike of the net package", err)
		}
	}
	if errors.Is(&Error{Kind: ErrNameNotFound}, os.ErrInvalid) || errors.Is(&Error{Kind: ErrTLS}, os.ErrInvalid) {
		t.Error("an error without a standard equivalent matches one")
	}
	// A truncated session is no clean end: whoever stops reading at io.EOF
	// must not take what arrived for the whole (mobile's Receive relies on
	// it).
	if errors.Is(&Error{Kind: ErrTLSTruncated}, io.EOF) {
		t.Error("a truncated TLS session matches io.EOF")
	}
}

func TestTimeoutsAreTemporaryNetErrors(t *testing.T) {
	timeout := &Error{Kind: ErrTimedOut}
	var netErr net.Error
	if !errors.As(error(timeout), &netErr) || !netErr.Timeout() || !timeout.Temporary() {
		t.Error("a timeout is not a temporary net.Error timeout")
	}
	reset := &Error{Kind: ErrConnectionReset}
	if reset.Timeout() || reset.Temporary() {
		t.Error("a reset is a temporary timeout")
	}
}

func TestErrorsUnwrapToTheirCause(t *testing.T) {
	cause := &iridium.Error{Kind: iridium.ErrTimeout, Command: "ATD"}

	err := &Error{Kind: ErrHandset, Err: cause}

	var coreErr *iridium.Error
	if !errors.As(err, &coreErr) || coreErr != cause || !errors.Is(err, iridium.ErrTimeout) {
		t.Errorf("%v does not unwrap to %v", err, cause)
	}
	if got := (&Error{Kind: ErrInvalidConfig, Message: "bad", Err: ppp.ErrMRUTooSmall}).Error(); got != "invalid configuration: bad: MRU too small" {
		t.Errorf("Error() = %q", got)
	}
}

func TestAFailedHangUpMatchesCallMayStillBeUp(t *testing.T) {
	coreFailure := &iridium.Error{Kind: iridium.ErrTimeout, Command: "+++", CallMayStillBeUp: true}
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"flag set":              {&Error{Kind: ErrLinkDown, CallMayStillBeUp: true}, true},
		"flag unset":            {&Error{Kind: ErrLinkDown}, false},
		"wrapped by the app":    {fmt.Errorf("closing: %w", &Error{Kind: ErrHandset, CallMayStillBeUp: true}), true},
		"in the handset's flag": {&Error{Kind: ErrHandset, Err: coreFailure}, true},
		"handsetError":          {handsetError(coreFailure), true},
		"a clean handset error": {handsetError(&iridium.Error{Kind: iridium.ErrBusy}), false},
	} {
		if got := errors.Is(c.err, iridium.ErrCallMayStillBeUp); got != c.want {
			t.Errorf("%s: errors.Is(%v, ErrCallMayStillBeUp) = %t, want %t", name, c.err, got, c.want)
		}
	}
	if got := (&Error{Kind: ErrLinkDown, CallMayStillBeUp: true}).Error(); got != "the satellite link is down: closed by the app" {
		t.Errorf("the flag changes the text: %q", got)
	}
}

func TestHandsetErrorsCarryTheHandsetsFlag(t *testing.T) {
	err := handsetError(&iridium.Error{Kind: iridium.ErrTimeout, Command: "+++", CallMayStillBeUp: true})

	if !err.CallMayStillBeUp || err.Kind != ErrHandset {
		t.Errorf("handsetError = %+v", err)
	}
}

func TestErrorKindsHaveLabels(t *testing.T) {
	if ErrPolicyDenied.Error() != "not allowed by the link policy" || ErrorKind(-1).Error() != "satnet.ErrorKind(-1)" {
		t.Errorf("labels: %q, %q", ErrPolicyDenied.Error(), ErrorKind(-1).Error())
	}
}
