package iridium

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// The names of the statuses, and the rules the handset applies to commands,
// numbers and the waits of a hang-up.

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
		{ChargerStatusUnreported, "Unreported"},
		{ChargerStatusCharging, "Charging"},
		{ChargerStatusNotCharging, "NotCharging"},
		{ChargerStatusNotConnected, "NotConnected"},
		{ChargerStatus(7), "Unknown(7)"},
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
		"ATD12025550100":     true,
		"atd12025550100":     true,
		"ATDT12025550100":    true,
		"AT D12025550100":    true,
		" ATD12025550100":    true,
		"ATD12025550100;":    false,
		"AT D12025550100 ; ": false,
		"ATDL":               true,
		"ATDL;":              false,
		"ATA":                true,
		"ata":                true,
		"ATA ":               true,
		"ATO":                true,
		"ATO0":               true,
		"A/":                 true,
		" a/":                true,
		"xa/":                true,
		"AT+CSQ":             false,
		"AT+CPAS":            false,
		"AT+CMGD=1":          false,
		"AT+CBST=71,0,1":     false,
		"AT&D2":              false,
		"ATH":                false,
		"ATE0V1Q0":           false,
		"ATS0?":              false,
		"ATS7=60":            false,
		"AT%R":               false,
		"AT*R1":              false,
		"AT-MSGEO":           false,
		"D12025550100":       false,
		// Wherever they stand among the commands, as the handset reads them.
		"ATE0D12025550100":         true,
		"ATE0d12025550100":         true,
		"ATA0":                     true,
		"ATAE0":                    true,
		"ATE0A":                    true,
		"ATE0O":                    true,
		"ATS0=1A":                  true,
		"ATS0A":                    true,
		"AT&D2D12025550100":        true,
		"ATE0;D12025550100":        true,
		"AT+CSQ;D12025550100":      true,
		"AT+CSQ;ATD12025550100":    true,
		"AT-MSSTM;O":               true,
		"ATD12025550100;D12025550": true,
		"ATD>\"A;B\"":              true,
		// From the first AT as written, which hides no command after it.
		"A T+ ATD12025550100": true,
		// A space ends an extended command, as does the next command once
		// past its name; so text written unquoted reads as commands.
		"AT+CSQ D12025550100":           true,
		"AT+CSQ ATD12025550100":         true,
		"AT+CBST=71,0,1 D12025550100":   true,
		"AT-MSSTM D12025550100":         true,
		"AT+CREG? A":                    true,
		"AT+CSQ O":                      true,
		"AT+CBST=71,0,1D12025550100":    true,
		"AT+CMGD=1D12025550100":         true,
		"AT+CREG?A":                     true,
		"AT+CPBW=1,12025550100,129,Ann": true,
		// A voice dial goes on to the commands after it, and a string's
		// separators separate nothing.
		"ATD12025550100;+CSQ":                        false,
		"AT+CSQ;D12025550100;":                       false,
		"ATD>\"A\";+CSQ":                             false,
		"AT+CPBW=1,\"D;A\",129":                      false,
		"AT+CPBW=1,\"+12025550100\",145,\"Ann Dee\"": false,
		// A dial string ends at its first ';', which must be outside quotes
		// for a voice call: a handset that ignores the quotes would run what
		// follows it.
		"ATD>\"A;B\";":    true,
		"ATD\"1;S0=1\";":  true,
		"ATD\"1\";S0=1":   true,
		"ATD\"1;O\";+CSQ": true,
		// Commands separated by spaces, or by nothing (ISU AT Command
		// Reference §3.5), and spaces inside a basic command.
		"AT X 0 &V +GSN +CBC=? +CPBR=1,12 -MSVLS?": false,
		"ATX&V+GSN+CBC=?+CPBR=1,12-MSVLS?":         false,
		"AT & D2":                                  false,
		"AT+CBST=71,0,1&D2":                        false,
		"AT&":                                      false,
		// A prefix whose next letter names no basic command the reference
		// defines, as the '*' of a DTMF tone or a shortcode, leaves that
		// letter to be read as a command.
		"AT-MSVTS=* +CPAS":    false,
		"AT-MSVTS=*+CPAS":     false,
		"AT-MSVTS=* -MSVLS=1": false,
		"AT-MSVTS=*;+CPAS":    false,
		"AT-MSVTS=* D123":     true,
		"AT-MSVTS=*D123":      true,
		"AT-MSVTS=1,* D123":   true,
		"AT-MSVTS=* A":        true,
		"AT-MSVTS=* O":        true,
		"AT+CSSSC=*21* D123":  true,
		"AT&A":                true,
		"AT\\A0":              false,
		// +SBDWT='s text runs to the end of the line, ';' and all.
		"AT+SBDWT=on;off":               false,
		"AT+SBDWT=Status: ok; all fine": false,
		// So do the values, unquoted, of commands that a 9555(A) or a 9575
		// runs alone: ALL, a name in UCS-2 hex ("Mom"), an email address.
		"AT+CAPBD=ALL":                false,
		"at+capbd=all":                false,
		"AT+CAPBW=004D006F006D":       false,
		"AT+LBSEMD=ALL":               false,
		"AT+LBSEMR=0,ops@example.org": false,
		// A name ends where the longest the reference defines does, and a
		// command may follow it with no separator.
		"AT+CSQD12025550100":    true,
		"AT+CPASA":              true,
		"AT-MSSTMD12025550100":  true,
		"AT+CGSND12025550100":   true,
		"AT+CEERO":              true,
		"AT+CSQ&D2D12025550100": true,
		"AT+GSN&VD12025550100":  true,
		"AT+CPAS\\N0O":          true,
		"AT+GSN+CGMI&VA":        true,
		"AT+CSQF":               false,
		"AT+SBDIXA":             false,
		"AT-MSGEOS":             false,
		"ATX&V+GSN+CMEE-MSVTR?": false,
		// One the reference does not define is read letter by letter.
		"AT+BOGUS": true,
		"AT+XYZZY": false,
		// A space after a value's comma ends the command, so the strings
		// that follow are read as commands.
		"AT+CPBW=1, \"+12025550100\";D12025550100":    true,
		"AT+CPBW=1, \"+12025550100\", 145, \"Ann\"":   true,
		"AT+CPBW=1,\"+12025550100\",145, \"Dad\"":     true,
		"AT+CPBW=1,\"+12025550100\",145,\"Ann\";+CSQ": false,
		"AT+CKPD=160":   false,
		"AT+CKPD = 160": true,
		// A key press, even one that may answer a ringing call, is sent.
		"AT+CKPD=140": false,
		// A value or a query ends before =n, which writes the register
		// last referred to: S0 here, or one an earlier line referred to.
		"ATS0+CREG?=1":      true,
		"ATS0;+CREG?=1":     true,
		"ATS0+CREG?=1&W0":   true,
		"AT+CREG?=1":        true,
		"AT+CMEE=2=1":       true,
		"AT+CBST=71,0,1=1":  true,
		"AT+CREG=?":         false,
		"AT+CPBW=1,\"A=B\"": false,
		// +CULK's unlock key is 16 hexadecimal digits, A and D among them.
		"AT+CULK=0123456789ABCDEF":              false,
		"at+culk=0123456789abcdef":              false,
		"AT+CULK=0123456789ABCDEF;+CULK?":       false,
		"AT+CULK=0123456789ABCDEF+CULK?":        false,
		"AT+CULK=0123456789ABCDEFD12025550100":  true,
		"AT+CULK=0123456789ABCDEF D12025550100": true,
		"AT+CULK=0123456789ABCDEF0D":            true,
		"AT+CULK=0123456789ABCDE":               true,
		"AT+CULK=D12025550100":                  true,
		"AT+CULK?0123456789ABCDEF":              true,
		"AT+CULK=S0=1ABCDEF012345":              true,
		"AT+CULK=O123456789ABCDEF":              true,
		// Auto-answer, and the registers that shape a line, written
		// directly or as the register last referred to.
		"ATS0=1":    true,
		"ATS 0 = 2": true,
		"ATS000=1":  true,
		"ATS0=0":    false,
		"ATS0":      false,
		"AT=1":      true,
		"ATS3=59":   true,
		"ATS5=32":   true,
		"ATS10=14":  false,
		"ATS12=50":  false,
		// A value's digits count across spaces, as a handset that drops
		// them reads them, and however many there are.
		"ATS0=0 1":   true,
		"ATS0 = 0 1": true,
		"ATS0=0 E1":  false,
		// A register's number ends at a space: should the handset read
		// on, the =n after it is refused all the same.
		"ATS0 1=1":                  true,
		"ATS3 0=0":                  true,
		"ATS0=18446744073709551616": true,
		// A prefix in mixed case may be skipped, for a later AT.
		"At+SBDWT=ATD12025550100":    true,
		"aT+CSQATD12025550100":       true,
		"At+CPBW=1,\"ATD12025550100": true,
		"At+CSQ":                     false,
		"AT+SBDWT=meet at dawn":      false,
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

func TestARefusedNumberQuotesControlCharacters(t *testing.T) {
	err := validateNumber("1\v2")

	// Control characters are escaped as \u{b}, not as \v.
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
