package satlink

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// Go only: the names the Rust CLI printed with {:?}, and the rules the
// handset applies to commands, numbers and the waits of a hang-up.

func TestStatusNames(t *testing.T) {
	tests := []struct {
		status fmt.Stringer
		want   string
	}{
		{ChargeStatusOnBattery, "OnBattery"},
		{ChargeStatusExternalPower, "ExternalPower"},
		{ChargeStatusNoBattery, "NoBattery"},
		{ChargeStatusPowerFault, "PowerFault"},
		{ChargeStatus(7), "Unknown(7)"},
		{RegistrationStatusNotRegistered, "NotRegistered"},
		{RegistrationStatusHome, "Home"},
		{RegistrationStatusSearching, "Searching"},
		{RegistrationStatusDenied, "Denied"},
		{RegistrationStatusUnknown, "Unknown"},
		{RegistrationStatusRoaming, "Roaming"},
		{RegistrationStatus(9), "RegistrationStatus(9)"},
		{SMSFilterAll, "All"},
		{SMSFilterUnread, "Unread"},
		{SMSFilterRead, "Read"},
		{SMSFilterUnsent, "Unsent"},
		{SMSFilterSent, "Sent"},
		{SMSFilter(-1), "SMSFilter(-1)"},
	}
	for _, test := range tests {
		if got := test.status.String(); got != test.want {
			t.Errorf("String() = %q, want %q", got, test.want)
		}
	}
}

func TestOnlyCommandsThatCanGoOnlineMayEndInConnect(t *testing.T) {
	tests := map[string]bool{
		"ATD12025550100":  true,
		"atd12025550100":  true,
		"ATD12025550100;": false,
		"ATA":             true,
		"ata":             true,
		"ATO":             true,
		"ATO0":            true,
		"AT+CSQ":          false,
		"ATH":             false,
	}
	for command, want := range tests {
		if got := mayGoOnline(command); got != want {
			t.Errorf("mayGoOnline(%q) = %v, want %v", command, got, want)
		}
	}
}

func TestDialableNumbers(t *testing.T) {
	tests := map[string]bool{
		"+12025550100": true,
		"008816000022": true,
		"*#06#":        true,
		"":             false,
		"+":            false,
		"123;ATH":      false,
		"12 34":        false,
		"++1":          false,
		"１２３":          false,
	}
	for number, dialable := range tests {
		if err := validateNumber(number); (err == nil) != dialable {
			t.Errorf("validateNumber(%q) = %v, want dialable %v", number, err, dialable)
		}
	}
}

func TestARefusedNumberIsQuotedAsTheRustVersionQuotedIt(t *testing.T) {
	err := validateNumber("1\v2")

	// Rust's {:?} of a str, where Go's %q would write "1\v2".
	if want := `invalid argument: "1\u{b}2" is not a dialable number`; err == nil || err.Error() != want {
		t.Errorf("validateNumber = %v, want %s", err, want)
	}
}

func TestAHangUpWaitsForTheCommandTimeoutButAtMostTenSeconds(t *testing.T) {
	// The ten seconds SetTimeout and HangUp promise, written as a literal:
	// ATH's wait, the wait that ends a data call, and those of the commands
	// that prepare a dial.
	tests := map[time.Duration]time.Duration{
		time.Second:      time.Second,
		10 * time.Second: 10 * time.Second,
		time.Minute:      10 * time.Second,
		math.MaxInt64:    10 * time.Second,
	}
	handset := NewHandset(&silentTransport{})
	for timeout, want := range tests {
		handset.SetTimeout(timeout)

		if got := handset.hangUpTimeout(); got != want {
			t.Errorf("with a timeout of %v, a hang-up waits %v; want %v", timeout, got, want)
		}
	}
}

func TestRegistrationCodesWithoutAConstantAreUnknown(t *testing.T) {
	tests := map[int]RegistrationStatus{
		0: RegistrationStatusNotRegistered,
		1: RegistrationStatusHome,
		2: RegistrationStatusSearching,
		3: RegistrationStatusDenied,
		4: RegistrationStatusUnknown,
		5: RegistrationStatusRoaming,
		6: RegistrationStatusUnknown,
	}
	for code, want := range tests {
		if got := registrationStatus(code); got != want {
			t.Errorf("registrationStatus(%d) = %v, want %v", code, got, want)
		}
	}
}
