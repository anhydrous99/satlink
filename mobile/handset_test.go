package mobile

import (
	"errors"
	"testing"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/sim"
)

func TestTheSMSFiltersAreTheHandsets(t *testing.T) {
	// ListSMS passes its filter on as it is, so each constant must be the
	// handset's own.
	cases := map[int]satlink.SMSFilter{
		SMSFilterAll:    satlink.SMSFilterAll,
		SMSFilterUnread: satlink.SMSFilterUnread,
		SMSFilterRead:   satlink.SMSFilterRead,
		SMSFilterUnsent: satlink.SMSFilterUnsent,
		SMSFilterSent:   satlink.SMSFilterSent,
	}
	for constant, filter := range cases {
		if satlink.SMSFilter(constant) != filter {
			t.Errorf("SMS filter %d is the handset's %v", constant, satlink.SMSFilter(constant))
		}
	}
}

func TestEveryChargeStatusHasItsConstant(t *testing.T) {
	cases := map[satlink.ChargeStatus]int{
		satlink.ChargeStatusOnBattery:     ChargeStatusOnBattery,
		satlink.ChargeStatusExternalPower: ChargeStatusExternalPower,
		satlink.ChargeStatusNoBattery:     ChargeStatusNoBattery,
		satlink.ChargeStatusPowerFault:    ChargeStatusPowerFault,
		satlink.ChargeStatus(7):           ChargeStatusUnknown,
	}
	for status, want := range cases {
		if got := chargeStatusOf(status); got != want {
			t.Errorf("chargeStatusOf(%v) = %d, want %d", status, got, want)
		}
	}
}

func TestEveryRegistrationStatusHasItsConstant(t *testing.T) {
	cases := map[satlink.RegistrationStatus]int{
		satlink.RegistrationStatusNotRegistered: RegistrationStatusNotRegistered,
		satlink.RegistrationStatusHome:          RegistrationStatusHome,
		satlink.RegistrationStatusSearching:     RegistrationStatusSearching,
		satlink.RegistrationStatusDenied:        RegistrationStatusDenied,
		satlink.RegistrationStatusUnknown:       RegistrationStatusUnknown,
		satlink.RegistrationStatusRoaming:       RegistrationStatusRoaming,
		satlink.RegistrationStatus(9):           RegistrationStatusUnknown,
	}
	for status, want := range cases {
		if got := registrationStatusOf(status); got != want {
			t.Errorf("registrationStatusOf(%v) = %d, want %d", status, got, want)
		}
	}
}

func TestAStoredMessageIsCopiedWhole(t *testing.T) {
	stored := satlink.SMSMessage{Index: 3, Status: "REC READ", Address: "+881600000000", Timestamp: "26/10/19,04:00:00+00", Text: "hi"}

	got := smsMessageOf(stored)

	want := SMSMessage{Index: 3, Status: "REC READ", Address: "+881600000000", Timestamp: "26/10/19,04:00:00+00", Text: "hi"}
	if got != want {
		t.Errorf("smsMessageOf() = %+v, want %+v", got, want)
	}
}

// assertCoreError fails the test unless err is a *satlink.Error equal to
// want.
func assertCoreError(t *testing.T, err error, want *satlink.Error) {
	t.Helper()
	var got *satlink.Error
	if !errors.As(err, &got) || *got != *want {
		t.Errorf("error is %#v, want %#v", err, want)
	}
}

func TestAFailureWhileACallMayStillBeUpSaysSo(t *testing.T) {
	leftUp := callMayStillBeUpError("ATH failed on a data call left up: transport error: USB write failed")
	cases := []struct {
		name string
		err  error
		want *satlink.Error
	}{
		{
			name: "the handset's own report is kept",
			err:  leftUp,
			want: leftUp,
		},
		{
			name: "another failure gives its text",
			err:  &satlink.Error{Kind: satlink.ErrTransport, Message: "unplugged"},
			want: callMayStillBeUpError("transport error: unplugged"),
		},
		{
			// The flag is Rust's CallFailure, not its Error::CallMayStillBeUp.
			name: "a failure of another kind with the flag gives its text",
			err:  &satlink.Error{Kind: satlink.ErrTimeout, Command: "+++", CallMayStillBeUp: true},
			want: callMayStillBeUpError(`timed out waiting for a response to "+++"`),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertCoreError(t, asCallMayStillBeUp(c.err), c.want)
		})
	}
}

func TestEndingAStrandedCallSaysTheCallMayStillBeUpWhateverFailed(t *testing.T) {
	cases := []struct {
		name  string
		fault func(*simtest.FaultyTransport)
		want  *satlink.Error
	}{
		{
			name:  "a handset that does not answer",
			fault: func(faulty *simtest.FaultyTransport) { faulty.Unplugged.Store(true) },
			want:  callMayStillBeUpError("transport error: unplugged"),
		},
		{
			name:  "an ATH that is lost",
			fault: func(faulty *simtest.FaultyTransport) { faulty.FailsNextHangUp.Store(true) },
			want:  callMayStillBeUpError("ATH failed: transport error: USB write failed"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			faulty := simtest.NewFaultyTransport(simtest.NewSimulator(t, sim.DefaultConfig()))
			c.fault(faulty)

			err := endStrandedCall(satlink.NewHandset(faulty))

			assertCoreError(t, err, c.want)
		})
	}
}

func TestEndingAStrandedCallThatIsOverSucceeds(t *testing.T) {
	t.Parallel()
	handset := satlink.NewHandset(simtest.NewSimulator(t, sim.DefaultConfig()))

	if err := endStrandedCall(handset); err != nil {
		t.Errorf("endStrandedCall: %v", err)
	}
}
