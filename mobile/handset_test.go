package mobile

import (
	"errors"
	"testing"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/sim"
)

func TestTheSMSFiltersAreTheHandsets(t *testing.T) {
	// ListSMS passes its filter on as it is, so each constant must be the
	// handset's own.
	cases := map[int]iridium.SMSFilter{
		SMSFilterAll:    iridium.SMSFilterAll,
		SMSFilterUnread: iridium.SMSFilterUnread,
		SMSFilterRead:   iridium.SMSFilterRead,
		SMSFilterUnsent: iridium.SMSFilterUnsent,
		SMSFilterSent:   iridium.SMSFilterSent,
	}
	for constant, filter := range cases {
		if iridium.SMSFilter(constant) != filter {
			t.Errorf("SMS filter %d is the handset's %v", constant, iridium.SMSFilter(constant))
		}
	}
}

func TestEveryChargeStatusHasItsConstant(t *testing.T) {
	cases := map[iridium.ChargeStatus]int{
		iridium.ChargeStatusOnBattery:     ChargeStatusOnBattery,
		iridium.ChargeStatusExternalPower: ChargeStatusExternalPower,
		iridium.ChargeStatusNoBattery:     ChargeStatusNoBattery,
		iridium.ChargeStatusPowerFault:    ChargeStatusPowerFault,
		iridium.ChargeStatus(7):           ChargeStatusUnknown,
	}
	for status, want := range cases {
		if got := chargeStatusOf(status); got != want {
			t.Errorf("chargeStatusOf(%v) = %d, want %d", status, got, want)
		}
	}
}

func TestEveryChargerStatusHasItsConstant(t *testing.T) {
	cases := map[iridium.ChargerStatus]int{
		iridium.ChargerStatusUnreported:   ChargerStatusUnreported,
		iridium.ChargerStatusCharging:     ChargerStatusCharging,
		iridium.ChargerStatusNotCharging:  ChargerStatusNotCharging,
		iridium.ChargerStatusNotConnected: ChargerStatusNotConnected,
		iridium.ChargerStatus(7):          ChargerStatusUnknown,
	}
	for status, want := range cases {
		if got := chargerStatusOf(status); got != want {
			t.Errorf("chargerStatusOf(%v) = %d, want %d", status, got, want)
		}
	}
}

func TestEveryRegistrationStatusHasItsConstant(t *testing.T) {
	cases := map[iridium.RegistrationStatus]int{
		iridium.RegistrationStatusNotRegistered: RegistrationStatusNotRegistered,
		iridium.RegistrationStatusHome:          RegistrationStatusHome,
		iridium.RegistrationStatusSearching:     RegistrationStatusSearching,
		iridium.RegistrationStatusDenied:        RegistrationStatusDenied,
		iridium.RegistrationStatusUnknown:       RegistrationStatusUnknown,
		iridium.RegistrationStatusRoaming:       RegistrationStatusRoaming,
		iridium.RegistrationStatus(9):           RegistrationStatusUnknown,
	}
	for status, want := range cases {
		if got := registrationStatusOf(status); got != want {
			t.Errorf("registrationStatusOf(%v) = %d, want %d", status, got, want)
		}
	}
}

func TestAStoredMessageIsCopiedWhole(t *testing.T) {
	stored := iridium.SMSMessage{Index: 3, Status: "REC READ", Address: "+881600000000", Timestamp: "26/10/19,04:00:00+00", Text: "hi"}

	got := smsMessageOf(stored)

	want := SMSMessage{Index: 3, Status: "REC READ", Address: "+881600000000", Timestamp: "26/10/19,04:00:00+00", Text: "hi"}
	if got != want {
		t.Errorf("smsMessageOf() = %+v, want %+v", got, want)
	}
}

// assertCoreError fails the test unless err is an *iridium.Error equal to
// want.
func assertCoreError(t *testing.T, err error, want *iridium.Error) {
	t.Helper()
	var got *iridium.Error
	if !errors.As(err, &got) || *got != *want {
		t.Errorf("error is %#v, want %#v", err, want)
	}
}

func TestAFailureWhileACallMayStillBeUpSaysSo(t *testing.T) {
	leftUp := callMayStillBeUpError("ATH failed on a data call left up: transport error: USB write failed")
	cases := []struct {
		name string
		err  error
		want *iridium.Error
	}{
		{
			name: "the handset's own report is kept",
			err:  leftUp,
			want: leftUp,
		},
		{
			name: "another failure gives its text",
			err:  &iridium.Error{Kind: iridium.ErrTransport, Message: "unplugged"},
			want: callMayStillBeUpError("transport error: unplugged"),
		},
		{
			// The flag on an error of another kind does not make it a hang-up failure.
			name: "a failure of another kind with the flag gives its text",
			err:  &iridium.Error{Kind: iridium.ErrTimeout, Command: "+++", CallMayStillBeUp: true},
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
		want  *iridium.Error
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

			err := endStrandedCall(iridium.NewHandset(faulty))

			assertCoreError(t, err, c.want)
		})
	}
}

func TestEndingAStrandedCallThatIsOverSucceeds(t *testing.T) {
	t.Parallel()
	handset := iridium.NewHandset(simtest.NewSimulator(t, sim.DefaultConfig()))

	if err := endStrandedCall(handset); err != nil {
		t.Errorf("endStrandedCall: %v", err)
	}
}
