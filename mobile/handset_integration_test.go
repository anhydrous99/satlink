package mobile_test

import (
	"regexp"
	"slices"
	"testing"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/mobile"
	"github.com/anhydrous99/satlink/sim"
)

// An IridiumHandset as an app uses it, over the simulated handset: status,
// SMS, calls, a handset busy while a link has it, and one the app closed.

// eventTimeoutMillis bounds the wait for an unsolicited event.
const eventTimeoutMillis = 5_000

// recipient is the number the SMS and call tests dial.
const recipient = "+881631234567"

// simulatedHandset returns an IridiumHandset over a new simulated handset,
// and the simulator's control.
func simulatedHandset(t *testing.T) (*mobile.IridiumHandset, *sim.Control) {
	t.Helper()
	transport, control := simulatedUSB(t)
	return newHandset(t, transport), control
}

func TestAHandsetIdentifiesItself(t *testing.T) {
	t.Parallel()
	handset, _ := simulatedHandset(t)

	info, err := handset.Info()

	want := mobile.DeviceInfo{Manufacturer: "Iridium", Model: "9575", Revision: "Call Processor Version: SIM-1.0", IMEI: "300234010000000"}
	if err != nil || *info != want {
		t.Errorf("Info() = %+v, %v, want %+v", info, err, want)
	}
	if imsi, err := handset.IMSI(); err != nil || imsi != "901039999999999" {
		t.Errorf("IMSI() = %q, %v, want 901039999999999", imsi, err)
	}
}

func TestAHandsetReportsSignalBatteryRegistrationAndClock(t *testing.T) {
	t.Parallel()
	handset, _ := simulatedHandset(t)

	bars, barsErr := handset.SignalQuality()
	battery, batteryErr := handset.Battery()
	registration, registrationErr := handset.Registration()
	clock, clockErr := handset.Clock()

	if barsErr != nil || bars != 4 {
		t.Errorf("SignalQuality() = %d, %v, want 4", bars, barsErr)
	}
	if want := (mobile.Battery{Status: mobile.ChargeStatusExternalPower, Bars: 3, Charger: mobile.ChargerStatusCharging,
		Level: 80, LevelReported: true}); batteryErr != nil || *battery != want {
		t.Errorf("Battery() = %+v, %v, want %+v", battery, batteryErr, want)
	}
	if want := (mobile.Registration{Status: mobile.RegistrationStatusHome}); registrationErr != nil || *registration != want {
		t.Errorf("Registration() = %+v, %v, want %+v", registration, registrationErr, want)
	}
	if timestamp := regexp.MustCompile(`^\d\d/\d\d/\d\d,\d\d:\d\d:\d\d[+-]\d\d$`); clockErr != nil || !timestamp.MatchString(clock) {
		t.Errorf("Clock() = %q, %v, want yy/MM/dd,hh:mm:ss±zz", clock, clockErr)
	}
}

func TestARawCommandReturnsItsLines(t *testing.T) {
	t.Parallel()
	handset, _ := simulatedHandset(t)

	response, err := handset.Command("AT+CSQ", eventTimeoutMillis)

	if err != nil || response.Connected || response.Lines.Len() != 1 || response.Lines.Get(0) != "+CSQ:4" {
		t.Errorf("Command(AT+CSQ) = %+v, %v, want one line +CSQ:4", response, err)
	}
}

func TestAHandsetSendsSMS(t *testing.T) {
	t.Parallel()
	handset, control := simulatedHandset(t)

	reference, err := handset.SendSMS(recipient, "hello from the sky")

	if err != nil || reference < 1 {
		t.Fatalf("SendSMS() = %d, %v, want a message reference", reference, err)
	}
	if sent := control.Stats().SMSSent; !slices.Equal(sent, []sim.SentSMS{{Number: recipient, Text: "hello from the sky"}}) {
		t.Errorf("the network got %+v", sent)
	}
}

func TestAnIncomingSMSIsAnnouncedReadAndDeleted(t *testing.T) {
	t.Parallel()
	handset, control := simulatedHandset(t)
	want := mobile.SMSMessage{Index: 1, Status: "REC UNREAD", Address: "+881600000000", Text: "are you there?"}

	control.ReceiveSMS(want.Address, want.Text)
	events, err := handset.PollEvents(eventTimeoutMillis)
	if err != nil {
		t.Fatalf("PollEvents: %v", err)
	}
	if events.Len() != 1 || events.Get(0) != `+CMTI: "SM",1` {
		t.Fatalf("PollEvents() has %d events, the first %q, want +CMTI for index 1", events.Len(), events.Get(0))
	}
	unread := listSMS(t, handset, mobile.SMSFilterUnread)
	if unread.Len() != 1 {
		t.Fatalf("ListSMS(unread) has %d messages, want one", unread.Len())
	}
	assertSMS(t, unread.Get(0), want)
	read, err := handset.ReadSMS(want.Index)
	if err != nil {
		t.Fatalf("ReadSMS: %v", err)
	}
	want.Status = "REC READ"
	assertSMS(t, read, want)
	if err := handset.DeleteSMS(want.Index); err != nil {
		t.Fatalf("DeleteSMS: %v", err)
	}
	if all := listSMS(t, handset, mobile.SMSFilterAll); all.Len() != 0 {
		t.Errorf("after deleting, ListSMS(all) has %d messages, want none", all.Len())
	}
}

// listSMS returns the messages filter selects.
func listSMS(t *testing.T, handset *mobile.IridiumHandset, filter int) *mobile.SMSMessageList {
	t.Helper()
	messages, err := handset.ListSMS(filter)
	if err != nil {
		t.Fatalf("ListSMS(%d): %v", filter, err)
	}
	return messages
}

// assertSMS fails the test unless got is want with a service-centre
// timestamp, which the simulated network sets.
func assertSMS(t *testing.T, got *mobile.SMSMessage, want mobile.SMSMessage) {
	t.Helper()
	if got.Timestamp == "" {
		t.Errorf("message %d has no timestamp", got.Index)
	}
	want.Timestamp = got.Timestamp
	if *got != want {
		t.Errorf("the message is %+v, want %+v", *got, want)
	}
}

func TestTakeEventsReturnsTheQueuedEventsOnce(t *testing.T) {
	t.Parallel()
	handset, control := simulatedHandset(t)
	control.ReceiveSMS("+881600000000", "hello")

	var events *mobile.StringList
	simtest.Eventually(t, patience, "the announcement is queued", func() bool {
		// A command reads what the handset said meanwhile, queueing the
		// announcement among the events.
		if _, err := handset.Command("AT", eventTimeoutMillis); err != nil {
			t.Fatalf("Command(AT): %v", err)
		}
		events = handset.TakeEvents()
		return events.Len() > 0
	})

	if events.Len() != 1 || events.Get(0) != `+CMTI: "SM",1` {
		t.Errorf("TakeEvents() has %d events, the first %q, want +CMTI for index 1", events.Len(), events.Get(0))
	}
	if again := handset.TakeEvents(); again.Len() != 0 {
		t.Errorf("TakeEvents() again has %d events, want none", again.Len())
	}
}

func TestAnUnknownSMSFilterIsRefused(t *testing.T) {
	t.Parallel()
	handset, _ := simulatedHandset(t)

	_, err := handset.ListSMS(99)

	if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorInvalidArgument || details.Reason != "99 is not an SMS filter" {
		t.Errorf("ListSMS(99) fails with %v, want \"99 is not an SMS filter\"", err)
	}
}

func TestAVoiceCallIsPlacedAndHungUp(t *testing.T) {
	t.Parallel()
	handset, control := simulatedHandset(t)

	if err := handset.Dial(recipient); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := handset.HangUp(); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	if stats := control.Stats(); stats.CallsPlaced != 1 || stats.CallsConnected != 1 {
		t.Errorf("%d calls placed and %d connected, want one each", stats.CallsPlaced, stats.CallsConnected)
	}
}

func TestAnsweringWithoutACallFails(t *testing.T) {
	t.Parallel()
	handset, _ := simulatedHandset(t)

	err := handset.Answer()

	if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorCall || details.Result != "NO CARRIER" {
		t.Errorf("Answer() fails with %v, want SatlinkErrorCall NO CARRIER", err)
	}
}

func TestAHandsetIsBusyWhileALinkHasIt(t *testing.T) {
	t.Parallel()
	handset, _ := simulatedHandset(t)
	link := lend(t, handset, mobile.NewLinkSettings())

	_, pollErr := handset.PollEvents(0)
	reinitializeErr := handset.Reinitialize()
	_, commandErr := handset.Command("AT", eventTimeoutMillis)

	for name, err := range map[string]error{"PollEvents": pollErr, "Reinitialize": reinitializeErr, "Command": commandErr} {
		if kind := kindOf(err); kind != mobile.SatlinkErrorHandsetBusy {
			t.Errorf("%s fails with %v, want SatlinkErrorHandsetBusy", name, err)
		}
	}
	if events := handset.TakeEvents(); events.Len() != 0 {
		t.Errorf("a busy handset has %d events, want none", events.Len())
	}
	if _, err := link.HangUp(timeoutMillis); err != nil {
		t.Fatalf("HangUp: %v", err)
	}
	if err := handset.Reinitialize(); err != nil {
		t.Errorf("Reinitialize after the call: %v", err)
	}
}

func TestClosingAHandsetLeavesTheAppsTransportAlone(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	handset := newHandset(t, transport)

	for range 2 {
		if err := handset.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	if err := signalQuality(newHandset(t, transport))(); err != nil {
		t.Errorf("a new handset on the app's transport does not answer: %v", err)
	}
}

// assertClosed fails the test unless handset refuses to work as a closed
// one does: every method but Close fails with SatlinkErrorInvalidArgument,
// "the handset is closed", and it has no events.
func assertClosed(t *testing.T, handset *mobile.IridiumHandset) {
	t.Helper()
	const reason = "the handset is closed"
	_, pollErr := handset.PollEvents(0)
	_, commandErr := handset.Command("AT", eventTimeoutMillis)
	_, lendErr := mobile.EstablishLinkWithHandset(handset, mobile.NewLinkSettings(), nil, nil)
	calls := map[string]error{
		"SignalQuality":            signalQuality(handset)(),
		"PollEvents":               pollErr,
		"Command":                  commandErr,
		"Reinitialize":             handset.Reinitialize(),
		"EstablishLinkWithHandset": lendErr,
	}
	for name, err := range calls {
		if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorInvalidArgument || details.Reason != reason {
			t.Errorf("%s fails with %v, want SatlinkErrorInvalidArgument: %s", name, err, reason)
		}
	}
	if events := handset.TakeEvents(); events.Len() != 0 {
		t.Errorf("a closed handset has %d events, want none", events.Len())
	}
}

func TestAClosedHandsetRefusesToWork(t *testing.T) {
	t.Parallel()
	handset, control := simulatedHandset(t)

	if err := handset.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	assertClosed(t, handset)
	if placed := control.Stats().CallsPlaced; placed != 0 {
		t.Errorf("the closed handset placed %d calls, want none", placed)
	}
}

func TestAHandsetClosedWhileALinkHasItStaysClosedOnceTheCallEnds(t *testing.T) {
	t.Parallel()
	handset, _ := simulatedHandset(t)
	link := lend(t, handset, mobile.NewLinkSettings())
	if err := handset.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := link.HangUp(timeoutMillis); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	assertClosed(t, handset)
}

func TestReinitializeReportsAHandsetThatDoesNotAnswer(t *testing.T) {
	t.Parallel()
	faulty := simtest.NewFaultyTransport(simtest.NewSimulator(t, sim.DefaultConfig()))
	handset := newHandset(t, simtest.NewByteTransport(faulty))
	faulty.Unplugged.Store(true)

	err := handset.Reinitialize()

	if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorTransport || details.Reason != "unplugged" {
		t.Errorf("Reinitialize fails with %v, want SatlinkErrorTransport: unplugged", err)
	}
	faulty.Unplugged.Store(false)
	if err := handset.Reinitialize(); err != nil {
		t.Errorf("Reinitialize once plugged in again: %v", err)
	}
}
