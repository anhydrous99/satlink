package sim_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/sim"
)

// The AT command set through satlink.Handset, as an app uses it.

// assertHandsetError fails the test unless err is a *satlink.Error like
// want, in kind and texts.
func assertHandsetError(t *testing.T, err error, want satlink.Error) {
	t.Helper()
	var got *satlink.Error
	if !errors.As(err, &got) || got.Kind != want.Kind || got.Code != want.Code || got.Message != want.Message {
		t.Errorf("got %v; want %v", err, &want)
	}
}

// The handset and its control are used from several goroutines at once, for
// the race detector to see.
func TestTheHandsetAndItsControlCanBeUsedFromSeveralGoroutines(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())
	const senders = 4
	var wait sync.WaitGroup
	for range senders {
		wait.Go(func() {
			control.ReceiveSMS("+881631234567", "from another goroutine")
			_ = control.Stats() // Only the race detector looks at this.
		})
	}

	for range senders {
		if _, err := handset.SignalQuality(); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()

	if inbox, err := handset.ListSMS(satlink.SMSFilterAll); err != nil || len(inbox) != senders {
		t.Errorf("ListSMS = %d messages, %v; want %d", len(inbox), err, senders)
	}
}

func TestIdentifiesItselfAsConfigured(t *testing.T) {
	t.Parallel()
	config := fastConfig()
	config.Model = "9555"
	config.IMEI = "300234010999990"
	handset, _ := readyHandset(t, config)

	info, err := handset.Info()
	if err != nil {
		t.Fatal(err)
	}
	imsi, err := handset.IMSI()
	if err != nil {
		t.Fatal(err)
	}

	want := satlink.DeviceInfo{
		Manufacturer: "Iridium",
		Model:        "9555",
		Revision:     "Call Processor Version: SIM-1.0",
		IMEI:         "300234010999990",
	}
	if info != want || imsi != "901039999999999" {
		t.Errorf("Info() = %+v, IMSI() = %q; want %+v and 901039999999999", info, imsi, want)
	}
}

func TestReportsSignalBatteryRegistrationAndClock(t *testing.T) {
	t.Parallel()
	config := fastConfig()
	config.SignalBars = 2
	handset, _ := readyHandset(t, config)

	bars, err := handset.SignalQuality()
	battery, batteryErr := handset.Battery()
	registration, registrationErr := handset.Registration()
	clock, clockErr := handset.Clock()

	assertNoErrors(t, err, batteryErr, registrationErr, clockErr)
	if bars != 2 {
		t.Errorf("SignalQuality() = %d; want 2", bars)
	}
	assertLines(t, handset, "AT+CSQF", "+CSQF:2")
	if battery.Status != satlink.ChargeStatusExternalPower || battery.Level != 80 {
		t.Errorf("Battery() = %+v; want external power at 80", battery)
	}
	if registration.Status != satlink.RegistrationStatusHome {
		t.Errorf("Registration() = %+v; want Home", registration)
	}
	if shape := digitsAsNines(clock); shape != "99/99/99,99:99:99+99" {
		t.Errorf("Clock() = %q", clock)
	}
}

// digitsAsNines returns text with every digit replaced by 9: its shape.
func digitsAsNines(text string) string {
	return strings.Map(func(r rune) rune {
		if '0' <= r && r <= '9' {
			return '9'
		}
		return r
	}, text)
}

// assertNoErrors fails the test at once if any of errs is not nil.
func assertNoErrors(t *testing.T, errs ...error) {
	t.Helper()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

func TestSendsSMSAndRecordsThem(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())

	first, firstErr := handset.SendSMS("+881631234567", "first")
	second, secondErr := handset.SendSMS("+12025550100", "second")

	if first != 1 || firstErr != nil || second != 2 || secondErr != nil {
		t.Errorf("references %d (%v) and %d (%v); want 1 and 2", first, firstErr, second, secondErr)
	}
	want := []sim.SentSMS{{Number: "+881631234567", Text: "first"}, {Number: "+12025550100", Text: "second"}}
	if sent := control.Stats().SMSSent; !slices.Equal(sent, want) {
		t.Errorf("sent %+v; want %+v", sent, want)
	}
}

func TestAnnouncesIncomingSMSAndKeepsThemUntilDeleted(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())

	control.ReceiveSMS("+881631111111", "first line\nsecond line")
	if urcs, err := handset.PollURCs(2 * time.Second); err != nil || !slices.Equal(urcs, []string{`+CMTI: "SM",1`}) {
		t.Fatalf("PollURCs = %q, %v", urcs, err)
	}
	control.ReceiveSMS("+12025550100", "hi")

	unread := mustListSMS(t, handset, satlink.SMSFilterUnread, 2)
	assertMessage(t, unread[0], satlink.SMSMessage{Index: 1, Status: "REC UNREAD", Address: "+881631111111", Text: "first line\nsecond line"})
	if !strings.HasSuffix(unread[0].Timestamp, "+00") {
		t.Errorf("the first message was received at %q", unread[0].Timestamp)
	}
	if unread[1].Text != "hi" {
		t.Errorf("the second message is %+v", unread[1])
	}
	// Listing marks them read.
	mustListSMS(t, handset, satlink.SMSFilterUnread, 0)

	message, err := handset.ReadSMS(2)
	assertNoErrors(t, err)
	if message.Status != "REC READ" || message.Text != "hi" {
		t.Errorf("ReadSMS(2) = %+v", message)
	}
	assertNoErrors(t, handset.DeleteSMS(1))
	mustListSMS(t, handset, satlink.SMSFilterAll, 1)
	_, readErr := handset.ReadSMS(1)
	assertHandsetError(t, readErr, satlink.Error{Kind: satlink.ErrSMS, Code: "321"})
	assertHandsetError(t, handset.DeleteSMS(1), satlink.Error{Kind: satlink.ErrSMS, Code: "321"})
	mustListSMS(t, handset, satlink.SMSFilterSent, 0)
}

// mustListSMS returns the messages filter lists, failing the test unless
// there are count of them.
func mustListSMS(t *testing.T, handset *satlink.Handset, filter satlink.SMSFilter, count int) []satlink.SMSMessage {
	t.Helper()
	messages, err := handset.ListSMS(filter)
	if err != nil || len(messages) != count {
		t.Fatalf("ListSMS(%v) = %+v, %v; want %d messages", filter, messages, err, count)
	}
	return messages
}

// assertMessage fails the test unless got matches want in everything but
// the timestamp.
func assertMessage(t *testing.T, got, want satlink.SMSMessage) {
	t.Helper()
	want.Timestamp = got.Timestamp
	if got != want {
		t.Errorf("message %+v; want %+v", got, want)
	}
}

// The message is sent once the app waits for it. In a synctest bubble, synctest.Wait returns once every other
// goroutine waits: here once PollURCs sleeps between two reads of the
// simulated handset.
func TestAnSMSFromAnotherGoroutineReachesAWaitingApp(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		handset, control := readyHandset(t, fastConfig())
		type polled struct {
			urcs []string
			err  error
		}
		results := make(chan polled, 1)
		go func() {
			urcs, err := handset.PollURCs(5 * time.Second)
			results <- polled{urcs: urcs, err: err}
		}()
		synctest.Wait()

		control.ReceiveSMS("+881631234567", "ping")

		if result := <-results; result.err != nil || !slices.Equal(result.urcs, []string{`+CMTI: "SM",1`}) {
			t.Errorf("PollURCs = %q, %v", result.urcs, result.err)
		}
	})
}

func TestPlacesVoiceCalls(t *testing.T) {
	t.Parallel()
	handset, control := readyHandset(t, fastConfig())

	if err := handset.Dial("+12025550100"); err != nil {
		t.Fatal(err)
	}
	if err := handset.HangUp(); err != nil {
		t.Fatal(err)
	}

	if stats := control.Stats(); stats.CallsPlaced != 1 || stats.CallsConnected != 1 || stats.InDataCall {
		t.Errorf("stats %+v; want one voice call", stats)
	}
}

func TestAnsweringWithoutACallFails(t *testing.T) {
	t.Parallel()
	handset, _ := readyHandset(t, fastConfig())

	assertHandsetError(t, handset.Answer(), satlink.Error{Kind: satlink.ErrCall, Message: "NO CARRIER"})
}

func TestKeepsSettingsAndRefusesUnknownCommands(t *testing.T) {
	t.Parallel()
	handset, _ := readyHandset(t, fastConfig())

	assertLines(t, handset, "AT+CBST?", "+CBST: 7,0,1")
	if _, err := handset.Command("AT+CBST=71,0,1"); err != nil {
		t.Fatal(err)
	}
	assertLines(t, handset, "AT+CBST?", "+CBST: 71,0,1")
	for _, accepted := range []string{"AT&D2", "AT&K0", "AT&C1", "ATS12=50", "AT+CR=1"} {
		if _, err := handset.Command(accepted); err != nil {
			t.Errorf("%s: %v", accepted, err)
		}
	}
	if _, err := handset.Command("AT+BOGUS"); !errors.Is(err, satlink.ErrCommandFailed) {
		t.Errorf("AT+BOGUS: %v; want ErrCommandFailed", err)
	}
	assertLines(t, handset, "AT+CEER", "+CEER: No report available")
}

// assertLines fails the test unless command answers exactly the line want.
func assertLines(t *testing.T, handset *satlink.Handset, command, want string) {
	t.Helper()
	response, err := handset.Command(command)
	if err != nil || !slices.Equal(response.Lines, []string{want}) {
		t.Errorf("%s = %q, %v; want %q", command, response.Lines, err, want)
	}
}
