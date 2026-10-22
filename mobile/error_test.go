package mobile

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"testing"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/ppp"
	"github.com/anhydrous99/satlink/satnet"
)

// errCable is a transport's own failure, a cause the errors underneath wrap.
var errCable = errors.New("cable pulled")

// Close reasons that the link errors of these tests carry.
var (
	carrierLost          = satnet.CloseReason{Kind: satnet.CloseReasonCarrierLost}
	authenticationFailed = satnet.CloseReason{Kind: satnet.CloseReasonPPP, PPP: ppp.DownReason{Kind: ppp.DownReasonAuthenticationFailed}}
)

// ruleError is a rule that does not parse.
var ruleError = &satnet.ParseRuleError{Kind: satnet.ErrRuleMissingPort, Text: "not a rule"}

// assertSatlinkError fails the test unless err is want, field for field,
// with want's text.
func assertSatlinkError(t *testing.T, err error, want *SatlinkError, wantText string) {
	t.Helper()
	got := AsSatlinkError(err)
	if got == nil {
		t.Fatalf("error %v (%T) is not a *SatlinkError", err, err)
	}
	if *got != *want {
		t.Errorf("error is %+v, want %+v", *got, *want)
	}
	if got.Error() != wantText {
		t.Errorf("text is %q, want %q", got.Error(), wantText)
	}
}

// errorMapping is a case of the error mapping: an error of the packages
// underneath, and the SatlinkError, with its text, that the app sees.
type errorMapping struct {
	name     string
	err      error
	want     *SatlinkError
	wantText string
}

// assertErrorMappings checks every case as a subtest of t.
func assertErrorMappings(t *testing.T, cases []errorMapping) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertSatlinkError(t, fromError(c.err), c.want, c.wantText)
		})
	}
}

// handsetErrorMappings are the handset's errors, one of each kind.
var handsetErrorMappings = []errorMapping{
	{
		name:     "transport message",
		err:      &satlink.Error{Kind: satlink.ErrTransport, Message: "unplugged"},
		want:     &SatlinkError{Kind: SatlinkErrorTransport, Reason: "unplugged"},
		wantText: "transport: unplugged",
	},
	{
		name:     "transport cause",
		err:      &satlink.Error{Kind: satlink.ErrTransport, Err: errCable},
		want:     &SatlinkError{Kind: SatlinkErrorTransport, Reason: "cable pulled"},
		wantText: "transport: cable pulled",
	},
	{
		name:     "transport message and cause",
		err:      &satlink.Error{Kind: satlink.ErrTransport, Message: "writing", Err: errCable},
		want:     &SatlinkError{Kind: SatlinkErrorTransport, Reason: "writing: cable pulled"},
		wantText: "transport: writing: cable pulled",
	},
	{
		name:     "timeout",
		err:      &satlink.Error{Kind: satlink.ErrTimeout, Command: "AT+CSQ"},
		want:     &SatlinkError{Kind: SatlinkErrorTimeout, Command: "AT+CSQ"},
		wantText: "timed out waiting for AT+CSQ",
	},
	{
		name:     "command failed",
		err:      &satlink.Error{Kind: satlink.ErrCommandFailed, Command: "AT+CIMI"},
		want:     &SatlinkError{Kind: SatlinkErrorCommandFailed, Command: "AT+CIMI"},
		wantText: "command failed: AT+CIMI",
	},
	{
		name:     "equipment",
		err:      &satlink.Error{Kind: satlink.ErrEquipment, Code: "SIM not inserted"},
		want:     &SatlinkError{Kind: SatlinkErrorEquipment, Code: "SIM not inserted"},
		wantText: "+CME ERROR: SIM not inserted",
	},
	{
		name:     "SMS",
		err:      &satlink.Error{Kind: satlink.ErrSMS, Code: "321"},
		want:     &SatlinkError{Kind: SatlinkErrorSMS, Code: "321"},
		wantText: "+CMS ERROR: 321",
	},
	{
		name:     "call",
		err:      &satlink.Error{Kind: satlink.ErrCall, Message: "NO CARRIER"},
		want:     &SatlinkError{Kind: SatlinkErrorCall, Result: "NO CARRIER"},
		wantText: "call failed: NO CARRIER",
	},
	{
		name:     "parse",
		err:      &satlink.Error{Kind: satlink.ErrParse, Message: "no +CSQ line in []"},
		want:     &SatlinkError{Kind: SatlinkErrorParse, Reason: "no +CSQ line in []"},
		wantText: "unexpected response: no +CSQ line in []",
	},
	{
		name:     "invalid argument",
		err:      &satlink.Error{Kind: satlink.ErrInvalidArgument, Message: "9 is not an SMS filter"},
		want:     &SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "9 is not an SMS filter"},
		wantText: "invalid argument: 9 is not an SMS filter",
	},
	{
		name:     "busy",
		err:      &satlink.Error{Kind: satlink.ErrBusy},
		want:     &SatlinkError{Kind: SatlinkErrorHandsetBusy},
		wantText: "the handset is in use by a satellite link",
	},
	{
		name:     "a call that may still be up",
		err:      callMayStillBeUpError(answeredCallLeftUp),
		want:     &SatlinkError{Kind: SatlinkErrorHangUpFailed, Reason: answeredCallLeftUp},
		wantText: "the hang-up was not confirmed, so the call may still be up (" + answeredCallLeftUp + ")",
	},
	{
		name:     "cancelled",
		err:      &satlink.Error{Kind: satlink.ErrCancelled},
		want:     &SatlinkError{Kind: SatlinkErrorCancelled},
		wantText: "the call setup was cancelled",
	},
	{
		name:     "a kind without a constant",
		err:      &satlink.Error{Kind: satlink.ErrorKind(99)},
		want:     &SatlinkError{Kind: SatlinkErrorInternal, Reason: "satlink.ErrorKind(99)"},
		wantText: "internal error: satlink.ErrorKind(99)",
	},
}

// answeredCallLeftUp is what the handset's Answer says of a data call it
// took by mistake and could not end.
const answeredCallLeftUp = "ATA answered a data call that could not be ended: transport error: this transport has no DTR line"

func TestHandsetErrorsMapOneToOne(t *testing.T) {
	assertErrorMappings(t, handsetErrorMappings)
}

// linkErrorMappings are the link's errors, one of each kind.
var linkErrorMappings = []errorMapping{
	{
		name:     "handset",
		err:      &satnet.Error{Kind: satnet.ErrHandset, Err: &satlink.Error{Kind: satlink.ErrCall, Message: "BUSY"}},
		want:     &SatlinkError{Kind: SatlinkErrorCall, Result: "BUSY"},
		wantText: "call failed: BUSY",
	},
	{
		name:     "handset without a handset error",
		err:      &satnet.Error{Kind: satnet.ErrHandset, Err: errCable},
		want:     &SatlinkError{Kind: SatlinkErrorInternal, Reason: "handset: cable pulled"},
		wantText: "internal error: handset: cable pulled",
	},
	{
		name:     "a cancelled setup",
		err:      &satnet.Error{Kind: satnet.ErrHandset, Err: &satlink.Error{Kind: satlink.ErrCancelled}},
		want:     &SatlinkError{Kind: SatlinkErrorCancelled},
		wantText: "the call setup was cancelled",
	},
	{
		name:     "invalid configuration",
		err:      &satnet.Error{Kind: satnet.ErrInvalidConfig, Message: "durations must not be negative"},
		want:     &SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "durations must not be negative"},
		wantText: "invalid argument: durations must not be negative",
	},
	{
		name:     "policy denied",
		err:      &satnet.Error{Kind: satnet.ErrPolicyDenied, Destination: "echo.test:80"},
		want:     &SatlinkError{Kind: SatlinkErrorPolicyDenied, Destination: "echo.test:80"},
		wantText: "echo.test:80 is not allowed by the link settings",
	},
	{
		name:     "invalid host",
		err:      &satnet.Error{Kind: satnet.ErrInvalidHost, Host: "bad name"},
		want:     &SatlinkError{Kind: SatlinkErrorInvalidHost, Host: "bad name"},
		wantText: `"bad name" is not a valid host name`,
	},
	{
		name:     "name not found",
		err:      &satnet.Error{Kind: satnet.ErrNameNotFound, Host: "nowhere.test"},
		want:     &SatlinkError{Kind: SatlinkErrorNameNotFound, Host: "nowhere.test"},
		wantText: "no address found for nowhere.test",
	},
	{
		name:     "no DNS server",
		err:      &satnet.Error{Kind: satnet.ErrNoDNSServer},
		want:     &SatlinkError{Kind: SatlinkErrorNoDNSServer},
		wantText: "the link has no DNS server",
	},
	{
		name:     "timed out",
		err:      &satnet.Error{Kind: satnet.ErrTimedOut, Operation: "TCP read"},
		want:     &SatlinkError{Kind: SatlinkErrorTimeout, Command: "TCP read"},
		wantText: "timed out waiting for TCP read",
	},
	{
		name:     "connection failed",
		err:      &satnet.Error{Kind: satnet.ErrConnectionFailed, Addr: netip.MustParseAddrPort("192.0.2.7:9")},
		want:     &SatlinkError{Kind: SatlinkErrorConnectionFailed, Address: "192.0.2.7:9"},
		wantText: "could not connect to 192.0.2.7:9",
	},
	{
		name:     "connection reset",
		err:      &satnet.Error{Kind: satnet.ErrConnectionReset},
		want:     &SatlinkError{Kind: SatlinkErrorConnectionReset},
		wantText: "the connection was reset",
	},
	{
		name:     "datagram too large",
		err:      &satnet.Error{Kind: satnet.ErrDatagramTooLarge, Len: 600, Max: 548},
		want:     &SatlinkError{Kind: SatlinkErrorDatagramTooLarge, Len: 600, Max: 548},
		wantText: "a 600-byte datagram exceeds the 548-byte limit",
	},
	{
		name:     "TLS",
		err:      &satnet.Error{Kind: satnet.ErrTLS, Message: "invalid peer certificate", Err: errCable},
		want:     &SatlinkError{Kind: SatlinkErrorTLS, Reason: "invalid peer certificate: cable pulled"},
		wantText: "TLS: invalid peer certificate: cable pulled",
	},
	{
		name:     "TLS truncated",
		err:      &satnet.Error{Kind: satnet.ErrTLSTruncated},
		want:     &SatlinkError{Kind: SatlinkErrorTLSTruncated},
		wantText: "the server closed the connection without ending the TLS session",
	},
	{
		name:     "a connection's link down keeps its reason",
		err:      &satnet.Error{Kind: satnet.ErrLinkDown, Reason: carrierLost},
		want:     &SatlinkError{Kind: SatlinkErrorLinkDown, Reason: "the call dropped"},
		wantText: "the satellite link is down: the call dropped",
	},
	{
		name:     "link down for PPP",
		err:      &satnet.Error{Kind: satnet.ErrLinkDown, Reason: authenticationFailed},
		want:     &SatlinkError{Kind: SatlinkErrorLinkDown, Reason: authenticationFailed.String()},
		wantText: "the satellite link is down: " + authenticationFailed.String(),
	},
	{
		name:     "a kind without a constant",
		err:      &satnet.Error{Kind: satnet.ErrorKind(99)},
		want:     &SatlinkError{Kind: SatlinkErrorInternal, Reason: "satnet.ErrorKind(99)"},
		wantText: "internal error: satnet.ErrorKind(99)",
	},
}

func TestLinkErrorsMapToTheBindingsKinds(t *testing.T) {
	assertErrorMappings(t, linkErrorMappings)
}

// otherErrorMappings are the other errors the mapping knows, and one it
// does not, in the order the mapping tries them.
var otherErrorMappings = []errorMapping{
	{
		name:     "a rule that does not parse",
		err:      ruleError,
		want:     &SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: ruleError.Error()},
		wantText: "invalid argument: " + ruleError.Error(),
	},
	{
		name:     "a closed connection",
		err:      net.ErrClosed,
		want:     &SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "the connection is closed"},
		wantText: "invalid argument: the connection is closed",
	},
	{
		name:     "a closed connection, wrapped",
		err:      fmt.Errorf("read: %w", net.ErrClosed),
		want:     &SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "the connection is closed"},
		wantText: "invalid argument: the connection is closed",
	},
	{
		name:     "a link error before the standard error it also matches",
		err:      fmt.Errorf("dial: %w", &satnet.Error{Kind: satnet.ErrTimedOut, Operation: "TCP connect"}),
		want:     &SatlinkError{Kind: SatlinkErrorTimeout, Command: "TCP connect"},
		wantText: "timed out waiting for TCP connect",
	},
	{
		name:     "an error of no known kind",
		err:      errCable,
		want:     &SatlinkError{Kind: SatlinkErrorInternal, Reason: "cable pulled"},
		wantText: "internal error: cable pulled",
	},
}

func TestOtherErrorsMapInOrder(t *testing.T) {
	assertErrorMappings(t, otherErrorMappings)
}

func TestASatlinkErrorPassesUnchanged(t *testing.T) {
	own := &SatlinkError{Kind: SatlinkErrorTLS, Reason: "bad record"}

	got := fromError(fmt.Errorf("wrapped: %w", own))

	if AsSatlinkError(got) != own {
		t.Errorf("fromError returned %v, want the SatlinkError itself", got)
	}
}

func TestNoErrorMapsToNoError(t *testing.T) {
	if err := fromError(nil); err != nil {
		t.Errorf("fromError(nil) = %v, want nil", err)
	}
}

func TestAFailedHangUpReportsWhyTheLinkEnded(t *testing.T) {
	err := hangUpFailed(satnet.CloseReason{Kind: satnet.CloseReasonRequested}.String())

	assertSatlinkError(t, err,
		&SatlinkError{Kind: SatlinkErrorHangUpFailed, Reason: "closed by the app"},
		"the hang-up was not confirmed, so the call may still be up (closed by the app)")
}

func TestAnEstablishingErrorWhoseCallMayStillBeUpIsAFailedHangUp(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		generalKind int
	}{
		{
			name:        "a link that did not come up",
			err:         &satnet.Error{Kind: satnet.ErrLinkDown, Reason: authenticationFailed, CallMayStillBeUp: true},
			generalKind: SatlinkErrorLinkDown,
		},
		{
			name: "a dial whose attempt could not be ended",
			err: &satnet.Error{Kind: satnet.ErrHandset, CallMayStillBeUp: true,
				Err: &satlink.Error{Kind: satlink.ErrTimeout, Command: "+++", CallMayStillBeUp: true}},
			generalKind: SatlinkErrorTimeout,
		},
		{
			name:        "wrapped by the caller",
			err:         fmt.Errorf("establishing: %w", &satnet.Error{Kind: satnet.ErrLinkDown, Reason: authenticationFailed, CallMayStillBeUp: true}),
			generalKind: SatlinkErrorLinkDown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			established := establishError(c.err)
			general := fromError(c.err)

			assertSatlinkError(t, established,
				&SatlinkError{Kind: SatlinkErrorHangUpFailed, Reason: c.err.Error()},
				"the hang-up was not confirmed, so the call may still be up ("+c.err.Error()+")")
			if kind := AsSatlinkError(general).Kind; kind != c.generalKind {
				t.Errorf("the general mapping gives kind %d, want the error's own %d", kind, c.generalKind)
			}
		})
	}
}

func TestAnEstablishingErrorWithoutTheFlagMapsAsUsual(t *testing.T) {
	err := &satnet.Error{Kind: satnet.ErrInvalidConfig, Message: "limits must not be negative; use 0 for no limit"}

	assertSatlinkError(t, establishError(err),
		&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "limits must not be negative; use 0 for no limit"},
		"invalid argument: limits must not be negative; use 0 for no limit")
}

func TestACancelledSetupWhoseHangUpFailedIsAFailedHangUp(t *testing.T) {
	err := &satnet.Error{Kind: satnet.ErrHandset, CallMayStillBeUp: true, Err: &satlink.Error{Kind: satlink.ErrCancelled}}

	assertSatlinkError(t, establishError(err),
		&SatlinkError{Kind: SatlinkErrorHangUpFailed, Reason: "handset: the call setup was cancelled"},
		"the hang-up was not confirmed, so the call may still be up (handset: the call setup was cancelled)")
}

func TestEveryKindHasItsMessage(t *testing.T) {
	cases := []struct {
		err  SatlinkError
		want string
	}{
		{SatlinkError{Kind: SatlinkErrorTransport, Reason: "unplugged"}, "transport: unplugged"},
		{SatlinkError{Kind: SatlinkErrorTimeout, Command: "ATD"}, "timed out waiting for ATD"},
		{SatlinkError{Kind: SatlinkErrorCommandFailed, Command: "AT+CIMI"}, "command failed: AT+CIMI"},
		{SatlinkError{Kind: SatlinkErrorEquipment, Code: "30"}, "+CME ERROR: 30"},
		{SatlinkError{Kind: SatlinkErrorSMS, Code: "321"}, "+CMS ERROR: 321"},
		{SatlinkError{Kind: SatlinkErrorCall, Result: "NO CARRIER"}, "call failed: NO CARRIER"},
		{SatlinkError{Kind: SatlinkErrorParse, Reason: "no +CSQ line"}, "unexpected response: no +CSQ line"},
		{SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "maxLen must be at least 1"}, "invalid argument: maxLen must be at least 1"},
		{SatlinkError{Kind: SatlinkErrorPolicyDenied, Destination: "echo.test:0"}, "echo.test:0 is not allowed by the link settings"},
		{SatlinkError{Kind: SatlinkErrorInvalidHost, Host: "1.2.3"}, `"1.2.3" is not a valid host name`},
		// A form feed is escaped as \u{c}, where Go's %q would write "a\fb".
		{SatlinkError{Kind: SatlinkErrorInvalidHost, Host: "a\fb"}, `"a\u{c}b" is not a valid host name`},
		{SatlinkError{Kind: SatlinkErrorNameNotFound, Host: "nowhere.test"}, "no address found for nowhere.test"},
		{SatlinkError{Kind: SatlinkErrorNoDNSServer}, "the link has no DNS server"},
		{SatlinkError{Kind: SatlinkErrorConnectionFailed, Address: "192.0.2.7:9"}, "could not connect to 192.0.2.7:9"},
		{SatlinkError{Kind: SatlinkErrorConnectionReset}, "the connection was reset"},
		{SatlinkError{Kind: SatlinkErrorDatagramTooLarge, Len: 9000, Max: 548}, "a 9000-byte datagram exceeds the 548-byte limit"},
		{SatlinkError{Kind: SatlinkErrorTLS, Reason: "bad record"}, "TLS: bad record"},
		{SatlinkError{Kind: SatlinkErrorTLSTruncated}, "the server closed the connection without ending the TLS session"},
		{SatlinkError{Kind: SatlinkErrorLinkDown, Reason: "the call dropped"}, "the satellite link is down: the call dropped"},
		{SatlinkError{Kind: SatlinkErrorHandsetBusy}, "the handset is in use by a satellite link"},
		{SatlinkError{Kind: SatlinkErrorHangUpFailed, Reason: "closed by the app"}, "the hang-up was not confirmed, so the call may still be up (closed by the app)"},
		{SatlinkError{Kind: SatlinkErrorInternal, Reason: "a transport bug"}, "internal error: a transport bug"},
		{SatlinkError{Kind: SatlinkErrorCancelled}, "the call setup was cancelled"},
		{SatlinkError{Kind: 99, Reason: "made up"}, "SatlinkError(kind 99): made up"},
		{SatlinkError{Kind: -1}, "SatlinkError(kind -1): "},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("kind %d: Error() = %q, want %q", c.err.Kind, got, c.want)
		}
	}
}

func TestAsSatlinkErrorRecoversTheDetails(t *testing.T) {
	own := &SatlinkError{Kind: SatlinkErrorPolicyDenied, Destination: "echo.test:80"}
	cases := []struct {
		name string
		err  error
		want *SatlinkError
	}{
		{"the error itself", own, own},
		{"wrapped", fmt.Errorf("opening: %w", own), own},
		{"another error", errCable, nil},
		{"no error", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AsSatlinkError(c.err); got != c.want {
				t.Errorf("AsSatlinkError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
