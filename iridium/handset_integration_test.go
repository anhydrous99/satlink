package iridium_test

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/iridium"
)

// okReply is the handset's answer to a command that succeeded.
const okReply = "\r\nOK\r\n"

// The handset's answers to AT+CPAS: no call up, and a data call up.
const (
	readyStatusReply    = "\r\n+CPAS: 000\r\n\r\nOK\r\n"
	dataCallStatusReply = "\r\n+CPAS: 004\r\n\r\nOK\r\n"
)

// When late answers, to commands that timed out, arrive once the next
// exchange has begun: within the quiet the handset waits for before it
// takes anything for an answer.
const (
	// lateAnswerBeforeACall arrives within the 200 ms of quiet with which
	// late answers are dropped before a call is answered or dialled, and
	// before the handset is asked which call it is in.
	lateAnswerBeforeACall = 150 * time.Millisecond
	// lateAnswerAtInitialize arrives within the 300 ms of quiet that ends
	// Initialize's first drain, though after the 200 ms that do before a
	// call.
	lateAnswerAtInitialize = 250 * time.Millisecond
)

// A late answer that keeps coming, a line at a time: the listing of an
// AT+CMGL that timed out.
const (
	// lateListingPause is the pause before each of its stored messages:
	// shorter than any quiet that ends what the handset had to say.
	lateListingPause = 100 * time.Millisecond
	// lateListingLasts is how long it keeps coming: within the two seconds
	// the handset may keep talking before what it sends counts as a call's
	// data, with a second to spare for a slow machine.
	lateListingLasts = time.Second
)

// keepsListing queues on transport a late answer that keeps coming for
// lateListingLasts from now: the stored messages of an AT+CMGL that timed
// out, one every lateListingPause, then its OK.
func keepsListing(transport *scripted) *scripted {
	storedMessages := int(lateListingLasts/lateListingPause) - 1
	for index := 1; index <= storedMessages; index++ {
		message := fmt.Sprintf("\r\n+CMGL: %d,\"REC READ\",\"+881631111111\",,\r\nhello\r\n", index)
		transport.unsolicitedAfter(time.Duration(index)*lateListingPause, message)
	}
	return transport.unsolicitedAfter(lateListingLasts, okReply)
}

// configuration are the steps with which Initialize ends: echo off,
// verbose results, verbose +CME ERROR, SMS text mode.
func configuration() []step {
	return []step{
		{"ATE0\r", okReply},
		{"ATV1\r", okReply},
		{"AT+CMEE=2\r", okReply},
		{"AT+CMGF=1\r", okReply},
	}
}

func TestInitializeHandlesEcho(t *testing.T) {
	transport := newScripted(
		step{"AT\r", "AT\r\r\nOK\r\n"},
		step{"AT+CPAS\r", "AT+CPAS\r\r\n+CPAS: 000\r\n\r\nOK\r\n"},
		step{"ATE0\r", "ATE0\r\r\nOK\r\n"},
		step{"ATV1\r", okReply},
		step{"AT+CMEE=2\r", okReply},
		step{"AT+CMGF=1\r", okReply},
	)
	handset := newTestHandset(transport)

	if err := handset.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if unplayed := transport.unplayedSteps(); unplayed != 0 {
		t.Errorf("%d steps unplayed", unplayed)
	}
}

func TestDeviceInfo(t *testing.T) {
	handset := newTestHandset(newScripted(
		step{"AT+CGMI\r", "\r\nIridium\r\n\r\nOK\r\n"},
		step{"AT+CGMM\r", "\r\n9575\r\n\r\nOK\r\n"},
		step{"AT+CGMR\r", "\r\nCall Processor Version: HT11002\r\n\r\nOK\r\n"},
		step{"AT+CGSN\r", "\r\n300234010123450\r\n\r\nOK\r\n"},
	))

	info, err := handset.Info()

	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	want := iridium.DeviceInfo{
		Manufacturer: "Iridium",
		Model:        "9575",
		Revision:     "Call Processor Version: HT11002",
		IMEI:         "300234010123450",
	}
	if info != want {
		t.Errorf("Info = %+v, want %+v", info, want)
	}
}

// handsetSettle is how long a command line waits after the handset last
// sent something: a 9555A drops a command written straight after its
// previous answer.
const handsetSettle = 20 * time.Millisecond

func TestACommandWaitsForTheHandsetToSettleAfterItsLastAnswer(t *testing.T) {
	transport := newScripted(
		step{"AT+CGMI\r", "\r\nIridium\r\n\r\nOK\r\n"},
		// A 9555A's OK after AT+CGMM comes without a line break of its own.
		step{"AT+CGMM\r", "\r\n9555A\r\nOK\r\n"},
	)
	handset := newTestHandset(transport)

	_, firstErr := handset.Command("AT+CGMI")
	model, secondErr := handset.Command("AT+CGMM")

	if firstErr != nil || secondErr != nil {
		t.Fatalf("Command = %v, %v", firstErr, secondErr)
	}
	if !slices.Equal(model.Lines, []string{"9555A"}) {
		t.Errorf("AT+CGMM answered %q, want 9555A", model.Lines)
	}
	first, _ := transport.writtenAt("AT+CGMI\r")
	second, _ := transport.writtenAt("AT+CGMM\r")
	if gap := second.Sub(first); gap < handsetSettle {
		t.Errorf("AT+CGMM written %v after AT+CGMI, which was answered at once; want at least %v", gap, handsetSettle)
	}
}

func TestSignalBatteryRegistration(t *testing.T) {
	handset := newTestHandset(newScripted(
		step{"AT+CSQ\r", "\r\n+CSQ:4\r\n\r\nOK\r\n"},
		step{"AT+CBC\r", "\r\n+CBC: 1,080\r\n\r\nOK\r\n"},
		step{"AT+CREG?\r", "\r\n+CREG: 0,1\r\n\r\nOK\r\n"},
	))

	bars, err := handset.SignalQuality()
	if err != nil || bars != 4 {
		t.Errorf("SignalQuality = %d, %v; want 4", bars, err)
	}
	battery, err := handset.Battery()
	want := iridium.Battery{Status: iridium.ChargeStatusExternalPower, Level: 80}
	if err != nil || battery != want {
		t.Errorf("Battery = %+v, %v; want %+v", battery, err, want)
	}
	registration, err := handset.Registration()
	if err != nil || registration.Status != iridium.RegistrationStatusHome {
		t.Errorf("Registration = %+v, %v; want Home", registration, err)
	}
}

func TestSendSMSWaitsForPrompt(t *testing.T) {
	handset := newTestHandset(newScripted(
		step{"AT+CMGS=\"+881631234567\"\r", "\r\n> "},
		step{"hello\x1a", "\r\n+CMGS: 17\r\n\r\nOK\r\n"},
	))

	reference, err := handset.SendSMS("+881631234567", "hello")

	if err != nil || reference != 17 {
		t.Errorf("SendSMS = %d, %v; want 17", reference, err)
	}
}

func TestListSMSMultiline(t *testing.T) {
	handset := newTestHandset(newScripted(step{
		"AT+CMGL=\"ALL\"\r",
		"\r\n+CMGL: 1,\"REC READ\",\"+881631111111\",,\"24/10/18,12:00:00+00\"\r\n" +
			"first line\r\nsecond line\r\n" +
			"+CMGL: 4,\"REC UNREAD\",\"+12025550100\",,\"24/10/18,13:30:00+00\"\r\n" +
			"hi\r\n\r\nOK\r\n",
	}))

	messages, err := handset.ListSMS(iridium.SMSFilterAll)

	if err != nil {
		t.Fatalf("ListSMS: %v", err)
	}
	want := []iridium.SMSMessage{
		{Index: 1, Status: "REC READ", Address: "+881631111111", Timestamp: "24/10/18,12:00:00+00", Text: "first line\nsecond line"},
		{Index: 4, Status: "REC UNREAD", Address: "+12025550100", Timestamp: "24/10/18,13:30:00+00", Text: "hi"},
	}
	if !slices.Equal(messages, want) {
		t.Errorf("ListSMS = %+v, want %+v", messages, want)
	}
}

func TestURCDuringCommandIsQueued(t *testing.T) {
	handset := newTestHandset(newScripted(step{
		"AT+CSQ\r",
		"\r\n+CMTI: \"SM\",3\r\n\r\n+CSQ:2\r\n\r\nOK\r\n",
	}))

	bars, err := handset.SignalQuality()

	if err != nil || bars != 2 {
		t.Errorf("SignalQuality = %d, %v; want 2", bars, err)
	}
	if urcs := handset.TakeURCs(); !slices.Equal(urcs, []string{`+CMTI: "SM",3`}) {
		t.Errorf("TakeURCs = %q", urcs)
	}
}

func TestPollURCsReturnsRing(t *testing.T) {
	handset := iridium.NewHandset(newScripted().unsolicited("\r\nRING\r\n"))

	urcs, err := handset.PollURCs(50 * time.Millisecond)

	if err != nil || !slices.Equal(urcs, []string{"RING"}) {
		t.Errorf("PollURCs = %q, %v; want RING", urcs, err)
	}
}

func TestErrorsAreTyped(t *testing.T) {
	handset := newTestHandset(newScripted(
		step{"AT+CIMI\r", "\r\n+CME ERROR: SIM not inserted\r\n"},
		step{"AT+CMGD=9\r", "\r\n+CMS ERROR: 321\r\n"},
		step{"ATD+12025550100;\r", "\r\nNO CARRIER\r\n"},
		step{"AT+BOGUS\r", "\r\nERROR\r\n"},
	))

	_, imsiErr := handset.IMSI()
	deleteErr := handset.DeleteSMS(9)
	dialErr := handset.Dial("+12025550100")
	_, commandErr := handset.Command("AT+BOGUS")

	assertError(t, imsiErr, &iridium.Error{Kind: iridium.ErrEquipment, Code: "SIM not inserted"})
	assertError(t, deleteErr, &iridium.Error{Kind: iridium.ErrSMS, Code: "321"})
	assertError(t, dialErr, &iridium.Error{Kind: iridium.ErrCall, Message: "NO CARRIER"})
	assertError(t, commandErr, &iridium.Error{Kind: iridium.ErrCommandFailed, Command: "AT+BOGUS"})
}

func TestAnOKLineFromACallLeftUpDoesNotPassForTheHandsets(t *testing.T) {
	// Slow — three two-second synchronisations, then an escape with the
	// default guard — but timing-insensitive.
	t.Parallel()
	// A crashed session left a call up, and the far end's data holds OK
	// lines: only the handset's own answer shows it is listening.
	farEndOK := "\nOK\n"
	transport := newScripted(
		step{"AT\r", farEndOK},
		step{"AT\r", farEndOK},
		step{"AT\r", farEndOK},
		step{"+++", okReply},
		step{"ATH\r", okReply},
		step{"AT\r", okReply},
		step{"AT+CPAS\r", readyStatusReply},
		step{"ATE0\r", okReply},
		step{"ATV1\r", okReply},
		step{"AT+CMEE=2\r", okReply},
		step{"AT+CMGF=1\r", okReply},
	)
	handset := newTestHandset(transport)

	if err := handset.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if unplayed := transport.unplayedSteps(); unplayed != 0 {
		t.Errorf("%d steps unplayed: the call was not ended", unplayed)
	}
}

func TestADataCallHeldInOnlineCommandModeIsHungUp(t *testing.T) {
	// Escaped from, but never hung up: the handset answers while the call
	// goes on.
	transport := newScripted(slices.Concat([]step{
		{"AT\r", okReply},
		{"AT+CPAS\r", dataCallStatusReply},
		{"ATH\r", okReply},
	}, configuration())...)
	handset := newTestHandset(transport)

	if err := handset.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if unplayed := transport.unplayedSteps(); unplayed != 0 {
		t.Errorf("%d steps unplayed: the call was not ended", unplayed)
	}
}

func TestAHeldDataCallThatCannotBeHungUpIsReported(t *testing.T) {
	handset := newTestHandset(newScripted(
		step{"AT\r", okReply},
		step{"AT+CPAS\r", dataCallStatusReply},
		step{"ATH\r", "\r\nERROR\r\n"},
	))

	err := handset.Initialize()

	assertError(t, err, &iridium.Error{
		Kind:    iridium.ErrCallMayStillBeUp,
		Message: `ATH failed on a data call left up: handset returned ERROR for "ATH"`,
	})
	assertCallMayStillBeUp(t, err, true)
}

func TestACallLeftUpThatCannotBeEndedIsReported(t *testing.T) {
	// Slow — six seconds of AT twice, and two escapes with the default
	// guard — but timing-insensitive.
	t.Parallel()
	// Still online, and neither the escape nor a DTR drop gets through: the
	// handset never answers.
	handset := newTestHandset(newScripted())

	err := handset.Initialize()

	assertError(t, err, &iridium.Error{
		Kind: iridium.ErrCallMayStillBeUp,
		Message: `the handset does not answer (timed out waiting for a response to "AT"), ` +
			`and ending a data call it may be in failed: timed out waiting for a response to "+++"`,
	})
	assertCallMayStillBeUp(t, err, true)
}

func TestAHandsetThatEndedItsCallButStaysSilentHasTimedOut(t *testing.T) {
	// Slow — six seconds of AT twice, and an escape with the default guard —
	// but timing-insensitive.
	t.Parallel()
	handset := newTestHandset(newScripted(step{"+++", okReply}, step{"ATH\r", okReply}))

	err := handset.Initialize()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "AT"})
	assertCallMayStillBeUp(t, err, false)
}

func TestFirmwareWithoutCPASIsStillInitialised(t *testing.T) {
	transport := newScripted(slices.Concat([]step{
		{"AT\r", okReply},
		{"AT+CPAS\r", "\r\nERROR\r\n"},
	}, configuration())...)
	handset := newTestHandset(transport)

	if err := handset.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if unplayed := transport.unplayedSteps(); unplayed != 0 {
		t.Errorf("%d steps unplayed", unplayed)
	}
	if written := transport.history(); bytes.Contains(written, []byte("ATH")) {
		t.Errorf("written %q, want no ATH: nothing shows a call", written)
	}
}

func TestHangingUpJustAfterTheCallEndedIsNoFailure(t *testing.T) {
	handset := newTestHandset(newScripted(step{"ATH\r", "\r\nNO CARRIER\r\n"}))

	if err := handset.HangUp(); err != nil {
		t.Errorf("HangUp: %v", err)
	}
}

func TestAnsweringADataCallByMistakeEndsIt(t *testing.T) {
	// Slow — an escape with the default guard — but timing-insensitive.
	t.Parallel()
	transport := newScripted(
		step{"ATA\r", "\r\nCONNECT 9600\r\n"},
		step{"+++", okReply},
		step{"ATH\r", okReply},
	)
	handset := newTestHandset(transport)

	err := handset.Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "ATA answered a data call, which was ended again"})
	if errors.Is(err, iridium.ErrCallMayStillBeUp) {
		t.Error("the call was ended, yet the error says it may still be up")
	}
	if unplayed := transport.unplayedSteps(); unplayed != 0 {
		t.Errorf("%d steps unplayed", unplayed)
	}
}

func TestADataCallAnsweredByMistakeThatCannotBeEndedMayStillBeUp(t *testing.T) {
	transport := newScripted(step{"ATA\r", "\r\nCONNECT 9600\r\n"}).unpluggedOncePlayedOut()
	handset := newTestHandset(transport)

	err := handset.Answer()

	want := "the call may still be up: ATA answered a data call that could not be ended: transport error: unplugged"
	if err == nil || err.Error() != want {
		t.Fatalf("Answer = %v, want %q", err, want)
	}
	assertError(t, err, &iridium.Error{
		Kind:    iridium.ErrCallMayStillBeUp,
		Message: "ATA answered a data call that could not be ended: transport error: unplugged",
	})
	assertCallMayStillBeUp(t, err, true)
}

func TestAnsweringWithoutACallFails(t *testing.T) {
	handset := newTestHandset(newScripted(step{"ATA\r", "\r\nNO CARRIER\r\n"}))

	err := handset.Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "NO CARRIER"})
}

func TestAnAnswerThatCannotReadTheHandsetSendsNothing(t *testing.T) {
	// The late answers are dropped first, and that read fails.
	transport := newScripted().unpluggedOncePlayedOut()

	err := newTestHandset(transport).Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Err: errUnplugged})
	if written := transport.history(); len(written) != 0 {
		t.Errorf("written %q, want nothing", written)
	}
}

func TestATimeoutTooLongForTheClockMeansNoDeadline(t *testing.T) {
	handset := newTestHandset(newScripted(step{"AT\r", okReply}))
	handset.SetTimeout(math.MaxInt64)

	if _, err := handset.Command("AT"); err != nil {
		t.Errorf("Command: %v", err)
	}
}

func TestAnUnexpectedConnectIsAnError(t *testing.T) {
	// A late CONNECT from an abandoned dial must not pass as an answer.
	handset := newTestHandset(newScripted(step{"AT+CSQ\r", "\r\nCONNECT 9600\r\n"}))

	_, err := handset.SignalQuality()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "unexpected CONNECT after AT+CSQ: the handset is online"})
}

func TestTimeoutAndValidation(t *testing.T) {
	transport := newScripted()
	handset := newTestHandset(transport)

	_, commandErr := handset.Command("AT")
	dialErr := handset.Dial("123; ATH")
	_, smsErr := handset.SendSMS("+1", "bad\x1a")
	_, injectionErr := handset.Command("AT\rATH")

	assertError(t, commandErr, &iridium.Error{Kind: iridium.ErrTimeout, Command: "AT"})
	assertError(t, dialErr, &iridium.Error{Kind: iridium.ErrInvalidArgument, Message: `"123; ATH" is not a dialable number`})
	assertError(t, smsErr, &iridium.Error{Kind: iridium.ErrInvalidArgument, Message: "SMS text must not contain Ctrl-Z or ESC"})
	assertError(t, injectionErr, &iridium.Error{Kind: iridium.ErrInvalidArgument, Message: "command must not contain CR or LF"})
	if written := string(transport.history()); written != "AT\r" {
		t.Errorf("written %q, want only the AT that timed out", written)
	}
}

// The tests below cover further Handset methods.

func TestInitializeEndsACallLeftUpWhoseFarEndNeverPauses(t *testing.T) {
	// Slow — the first drain's two seconds, three two-second ATs and an
	// escape with the default guard — but timing-insensitive.
	t.Parallel()
	// A crashed session left a call up whose far end sends without a pause:
	// unless the first drain gives up, the escape that ends the call never
	// comes.
	transport := newScripted(slices.Concat([]step{
		{"+++", okReply},
		{"ATH\r", okReply},
		{"AT\r", okReply},
		{"AT+CPAS\r", readyStatusReply},
	}, configuration())...)
	handset := newTestHandset(callUntil("+++", transport))

	var err error
	finishesWithin(t, unendingWait, "Initialize", func() { err = handset.Initialize() })

	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	assertAllPlayed(t, transport)
}

func TestInitializeLeavesTextEntryFirst(t *testing.T) {
	// ESC CR aborts an SMS a previous session left half-entered, whose
	// text AT would otherwise become.
	transport := newScripted(slices.Concat([]step{{"AT\r", okReply}, {"AT+CPAS\r", readyStatusReply}}, configuration())...)

	if err := newTestHandset(transport).Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if written := transport.history(); !bytes.HasPrefix(written, []byte("\x1b\rAT\r")) {
		t.Errorf("Initialize wrote %q, want ESC and CR first, then AT", written)
	}
}

func TestFirmwareRefusingTheOptionalSettingsIsStillInitialised(t *testing.T) {
	// Verbose +CME ERROR and SMS text mode are optional on some firmware:
	// their failure is ignored, and the second is asked for even when the
	// first is refused.
	transport := newScripted(
		step{"AT\r", okReply},
		step{"AT+CPAS\r", readyStatusReply},
		step{"ATE0\r", okReply},
		step{"ATV1\r", okReply},
		step{"AT+CMEE=2\r", "\r\nERROR\r\n"},
		step{"AT+CMGF=1\r", "\r\nERROR\r\n"},
	)

	if err := newTestHandset(transport).Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	assertAllPlayed(t, transport)
}

func TestFirmwareRefusingARequiredSettingIsNotInitialised(t *testing.T) {
	// Echo off and verbose results are not optional, as the two settings
	// after them are: a refusal fails Initialize, and nothing more is sent.
	refused := "\r\nERROR\r\n"
	settingSteps := map[string][]step{
		"ATE0": {{"ATE0\r", refused}},
		"ATV1": {{"ATE0\r", okReply}, {"ATV1\r", refused}},
	}
	for command, steps := range settingSteps {
		t.Run(command, func(t *testing.T) {
			transport := newScripted(slices.Concat([]step{{"AT\r", okReply}, {"AT+CPAS\r", readyStatusReply}}, steps)...)

			err := newTestHandset(transport).Initialize()

			assertError(t, err, &iridium.Error{Kind: iridium.ErrCommandFailed, Command: command})
			assertAllPlayed(t, transport)
			if written := transport.history(); !bytes.HasSuffix(written, []byte(command+"\r")) {
				t.Errorf("written %q, want nothing after %s", written, command)
			}
		})
	}
}

func TestALateAnswerArrivingDuringInitializeDoesNotHideAHeldCall(t *testing.T) {
	// The handset is still answering when Initialize begins — the opening
	// ESC, or a command of the session before — within the first drain's
	// 300 ms of quiet. Taken for AT's answer, it would leave AT's own for
	// AT+CPAS, and the call held in online-command mode unseen.
	transport := newScripted(slices.Concat([]step{
		{"AT\r", okReply},
		{"AT+CPAS\r", dataCallStatusReply},
		{"ATH\r", okReply},
	}, configuration())...).unsolicitedAfter(lateAnswerAtInitialize, okReply)

	if err := newTestHandset(transport).Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	assertAllPlayed(t, transport)
}

func TestAPartialAnswerLeftBeforeInitializeDoesNotHideAHeldCall(t *testing.T) {
	// AT+CSQ timed out before the line end of its answer's OK came: what it
	// left in the line buffer must not run into AT+CPAS's answer and pass
	// for it, leaving the call held in online-command mode unseen.
	transport := newScripted(slices.Concat([]step{
		{"AT+CSQ\r", "\r\n+CSQ:4\r\n\r\nOK"},
		{"AT\r", okReply},
		{"AT+CPAS\r", dataCallStatusReply},
		{"ATH\r", okReply},
	}, configuration())...)
	handset := newTestHandset(transport)
	if _, err := handset.SignalQuality(); !errors.Is(err, iridium.ErrTimeout) {
		t.Fatalf("SignalQuality = %v, want a timeout: its answer never ended", err)
	}

	if err := handset.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	assertAllPlayed(t, transport)
}

func TestALateAnswerThatKeepsComingDoesNotHideAHeldCall(t *testing.T) {
	// A listing that timed out is still coming, line by line, when
	// Initialize begins: the first drain lasts until it is over. Cut short,
	// it would leave the listing's OK for AT, AT's own for AT+CPAS, and the
	// call held in online-command mode unseen.
	transport := keepsListing(newScripted(slices.Concat([]step{
		{"AT\r", okReply},
		{"AT+CPAS\r", dataCallStatusReply},
		{"ATH\r", okReply},
	}, configuration())...))

	if err := newTestHandset(transport).Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	assertAllPlayed(t, transport)
}

func TestWhatTheHandsetSaidBeforeInitializeIsDropped(t *testing.T) {
	// A late answer to the session before is no unsolicited result code.
	transport := newScripted(slices.Concat([]step{{"AT\r", okReply}, {"AT+CPAS\r", readyStatusReply}}, configuration())...).
		unsolicited("\r\n+CSQ:4\r\n\r\nOK\r\n")
	handset := newTestHandset(transport)

	if err := handset.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if urcs := handset.TakeURCs(); len(urcs) != 0 {
		t.Errorf("TakeURCs = %q, want nothing: what came before Initialize is dropped", urcs)
	}
}

func TestAnATAnsweredASecondLateStillSynchronises(t *testing.T) {
	// Slow — a second for AT's answer — but timing-insensitive.
	t.Parallel()
	// Each AT is given two seconds: a slow answer is no sign of a call left
	// up, which an escape would end.
	transport := newScripted(slices.Concat([]step{{"AT\r", okReply}, {"AT+CPAS\r", readyStatusReply}}, configuration())...).
		answersLate("AT\r", time.Second)

	if err := newTestHandset(transport).Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if written := transport.history(); bytes.Count(written, []byte("AT\r")) != 1 || bytes.Contains(written, []byte("+++")) {
		t.Errorf("written %q, want one AT and no escape", written)
	}
}

func TestAZeroTimeoutFailsACommandWithoutWaitingForItsAnswer(t *testing.T) {
	// As SetTimeout says: the command is sent, and nothing
	// is read for it, not even an answer that has arrived.
	handset := newTestHandset(newScripted(step{"AT+CSQ\r", "\r\n+CSQ:4\r\n\r\nOK\r\n"}))
	handset.SetTimeout(0)

	_, err := handset.SignalQuality()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "AT+CSQ"})
}

// longestPromisedRead is the longest a read of a handset waits for data at a
// time, a second: then it looks at its deadline
// again, so that a transport is never handed an absurd timeout.
const longestPromisedRead = time.Second

func TestACommandWithoutADeadlineReadsASecondAtATime(t *testing.T) {
	transport := &readTimeouts{Transport: newScripted(step{"AT\r", okReply}), waitOf: "AT\r"}
	handset := newTestHandset(transport)
	handset.SetTimeout(noDeadline)

	if _, err := handset.Command("AT"); err != nil {
		t.Fatalf("Command: %v", err)
	}

	if len(transport.timeouts) == 0 {
		t.Fatal("the command never waited for its answer")
	}
	if longest := slices.Max(transport.timeouts); longest > longestPromisedRead {
		t.Errorf("a read was handed %v, want at most %v", longest, longestPromisedRead)
	}
}

func TestTheEchoIsNoInformationLine(t *testing.T) {
	// Handsets ship with echo on: what is sent before ATE0 comes back.
	handset := newTestHandset(newScripted(step{"AT+CGMI\r", "AT+CGMI\r\r\nIridium\r\n\r\nOK\r\n"}))

	response, err := handset.Command("AT+CGMI")

	if err != nil || !slices.Equal(response.Lines, []string{"Iridium"}) {
		t.Errorf("Command = %q, %v; want the lines without the echo", response.Lines, err)
	}
}

func TestALineOnlyUnicodeFoldsToTheCommandIsNoEcho(t *testing.T) {
	// The echo is the command but for the case of ASCII letters, which are the
	// only letters compared without regard to case: Unicode would fold ſ into s.
	handset := newTestHandset(newScripted(step{"AT+CSQ\r", "\r\nAT+C\u017fQ\r\n\r\n+CSQ:4\r\n\r\nOK\r\n"}))

	response, err := handset.Command("AT+CSQ")

	if want := []string{"AT+C\u017fQ", "+CSQ:4"}; err != nil || !slices.Equal(response.Lines, want) {
		t.Errorf("Command = %q, %v; want %q", response.Lines, err, want)
	}
}

func TestALineLeftAfterAnAnswerIsNotTheNextCommands(t *testing.T) {
	// The stray line comes in the same read as the OK — the reply is exactly
	// replyChunk long — so that it waits in the line buffer, not in the
	// transport, when the next command is written.
	handset := newTestHandset(newScripted(
		step{"AT\r", "OK\r\n1\r\n"},
		step{"AT+CIMI\r", "\r\n310170123456789\r\n\r\nOK\r\n"},
	))
	if _, err := handset.Command("AT"); err != nil {
		t.Fatalf("Command: %v", err)
	}

	imsi, err := handset.IMSI()

	if err != nil || imsi != "310170123456789" {
		t.Errorf("IMSI = %q, %v; want the handset's answer to AT+CIMI", imsi, err)
	}
	if urcs := handset.TakeURCs(); !slices.Equal(urcs, []string{"1"}) {
		t.Errorf("TakeURCs = %q, want the stray line, queued before AT+CIMI was sent", urcs)
	}
}

func TestPollURCsReturnsAsSoonAsOneArrives(t *testing.T) {
	// The poll does not wait out its timeout: an app polling for minutes on
	// end hears of an incoming call when it rings.
	ringsAfter := 100 * time.Millisecond
	pollTimeout := 10 * time.Second
	handset := iridium.NewHandset(newScripted().unsolicitedAfter(ringsAfter, "\r\nRING\r\n"))
	started := time.Now()

	urcs, err := handset.PollURCs(pollTimeout)

	if err != nil || !slices.Equal(urcs, []string{"RING"}) {
		t.Errorf("PollURCs = %q, %v; want RING", urcs, err)
	}
	if elapsed := time.Since(started); elapsed > ringsAfter+promptly {
		t.Errorf("PollURCs returned after %v, want as soon as RING arrived, %v in", elapsed, ringsAfter)
	}
}

func TestReadSMSReadsTheHeaderAndTheText(t *testing.T) {
	handset := newTestHandset(newScripted(step{
		"AT+CMGR=3\r",
		"\r\n+CMGR: \"REC UNREAD\",\"+881631111111\",,\"24/10/18,12:00:00+00\"\r\nline one\r\nline two\r\n\r\nOK\r\n",
	}))

	message, err := handset.ReadSMS(3)

	if err != nil {
		t.Fatalf("ReadSMS: %v", err)
	}
	want := iridium.SMSMessage{
		Index: 3, Status: "REC UNREAD", Address: "+881631111111",
		Timestamp: "24/10/18,12:00:00+00", Text: "line one\nline two",
	}
	if message != want {
		t.Errorf("ReadSMS = %+v, want %+v", message, want)
	}
}

func TestReadSMSWithoutAHeaderIsAParseError(t *testing.T) {
	handset := newTestHandset(newScripted(step{"AT+CMGR=3\r", "\r\nhello\r\n\r\nOK\r\n"}))

	_, err := handset.ReadSMS(3)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrParse, Message: `no +CMGR header in ["hello"]`})
}

func TestMessageIndicesAndFiltersAreCheckedBeforeAnythingIsSent(t *testing.T) {
	transport := newScripted()
	handset := newTestHandset(transport)

	_, readErr := handset.ReadSMS(-1)
	deleteErr := handset.DeleteSMS(-2)
	_, listErr := handset.ListSMS(iridium.SMSFilter(9))

	assertError(t, readErr, &iridium.Error{Kind: iridium.ErrInvalidArgument, Message: "-1 is not a message index"})
	assertError(t, deleteErr, &iridium.Error{Kind: iridium.ErrInvalidArgument, Message: "-2 is not a message index"})
	assertError(t, listErr, &iridium.Error{Kind: iridium.ErrInvalidArgument, Message: "9 is not an SMS filter"})
	if written := transport.history(); len(written) != 0 {
		t.Errorf("written %q, want nothing", written)
	}
}

func TestListSMSSelectsWithTheFiltersStorageStatus(t *testing.T) {
	filters := map[iridium.SMSFilter]string{
		iridium.SMSFilterUnread: "REC UNREAD",
		iridium.SMSFilterRead:   "REC READ",
		iridium.SMSFilterUnsent: "STO UNSENT",
		iridium.SMSFilterSent:   "STO SENT",
	}
	for filter, status := range filters {
		t.Run(filter.String(), func(t *testing.T) {
			handset := newTestHandset(newScripted(step{"AT+CMGL=\"" + status + "\"\r", okReply}))

			messages, err := handset.ListSMS(filter)

			if err != nil || len(messages) != 0 {
				t.Errorf("ListSMS = %+v, %v; want no messages", messages, err)
			}
		})
	}
}

func TestClockStripsTheQuotes(t *testing.T) {
	handset := newTestHandset(newScripted(step{"AT+CCLK?\r", "\r\n+CCLK: \"24/10/18,12:00:00+00\"\r\n\r\nOK\r\n"}))

	clock, err := handset.Clock()

	if err != nil || clock != "24/10/18,12:00:00+00" {
		t.Errorf("Clock = %q, %v", clock, err)
	}
}

func TestRegistrationReportsTheCell(t *testing.T) {
	handset := newTestHandset(newScripted(step{"AT+CREG?\r", "\r\n+CREG: 2,5,\"1A2B\",\"00C3\"\r\n\r\nOK\r\n"}))

	registration, err := handset.Registration()

	want := iridium.Registration{Status: iridium.RegistrationStatusRoaming, LocationArea: "1A2B", CellID: "00C3"}
	if err != nil || registration != want {
		t.Errorf("Registration = %+v, %v; want %+v", registration, err, want)
	}
}

func TestCodesWithoutAConstantAreUnknown(t *testing.T) {
	handset := newTestHandset(newScripted(
		step{"AT+CREG?\r", "\r\n+CREG: 0,7\r\n\r\nOK\r\n"},
		step{"AT+CBC\r", "\r\n+CBC: 9,50\r\n\r\nOK\r\n"},
	))

	registration, registrationErr := handset.Registration()
	battery, batteryErr := handset.Battery()

	if registrationErr != nil || registration.Status != iridium.RegistrationStatusUnknown {
		t.Errorf("Registration = %+v, %v; want Unknown", registration, registrationErr)
	}
	if batteryErr != nil || battery.Status.String() != "Unknown(9)" {
		t.Errorf("Battery = %+v, %v; want status Unknown(9)", battery, batteryErr)
	}
}

func TestAMalformedAnswerIsAParseError(t *testing.T) {
	handset := newTestHandset(newScripted(
		step{"AT+CSQ\r", "\r\n+CSQ:-1\r\n\r\nOK\r\n"},
		step{"AT+CBC\r", "\r\nOK\r\n"},
		step{"AT+CIMI\r", okReply},
	))

	_, signalErr := handset.SignalQuality()
	_, batteryErr := handset.Battery()
	_, imsiErr := handset.IMSI()

	assertError(t, signalErr, &iridium.Error{Kind: iridium.ErrParse, Message: `+CSQ: bad field 0 in ["-1"]`})
	assertError(t, batteryErr, &iridium.Error{Kind: iridium.ErrParse, Message: "no +CBC line in []"})
	assertError(t, imsiErr, &iridium.Error{Kind: iridium.ErrParse, Message: "AT+CIMI returned no data"})
}

func TestVoiceCallsDialAnswerAndHangUp(t *testing.T) {
	transport := newScripted(
		step{"ATD*#12;\r", okReply},
		step{"ATH\r", okReply},
		step{"ATA\r", okReply},
	)
	handset := newTestHandset(transport)

	if err := handset.Dial("*#12"); err != nil {
		t.Errorf("Dial: %v", err)
	}
	if err := handset.HangUp(); err != nil {
		t.Errorf("HangUp: %v", err)
	}
	if err := handset.Answer(); err != nil {
		t.Errorf("Answer: %v", err)
	}
	if unplayed := transport.unplayedSteps(); unplayed != 0 {
		t.Errorf("%d steps unplayed", unplayed)
	}
}

func TestCommandTimeoutWaitsItsOwnTimeout(t *testing.T) {
	handset := newTestHandset(newScripted())
	ownTimeout := 4 * commandTimeout
	started := time.Now()

	_, err := handset.CommandTimeout("AT", ownTimeout)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "AT"})
	if elapsed := time.Since(started); elapsed < ownTimeout {
		t.Errorf("CommandTimeout gave up after %v, before its own timeout of %v", elapsed, ownTimeout)
	}
}

func TestATransportErrorIsReportedAsATransportError(t *testing.T) {
	handset := newTestHandset(newScripted().unpluggedOncePlayedOut())

	_, err := handset.Command("AT")

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Err: errUnplugged})
	if !errors.Is(err, errUnplugged) {
		t.Errorf("%v does not wrap the transport's error", err)
	}
}

func TestTransportReturnsTheTransport(t *testing.T) {
	transport := newScripted()

	handset := iridium.NewHandset(transport)

	if handset.Transport() != transport {
		t.Error("Transport is not the handset's transport")
	}
}

func TestNewHandsetRefusesANilTransport(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewHandset(nil) did not panic")
		}
	}()

	iridium.NewHandset(nil)
}

func TestCloseClosesTheTransportOnce(t *testing.T) {
	transport := &countingCloser{scripted: newScripted()}
	handset := iridium.NewHandset(transport)

	first := handset.Close()
	second := handset.Close()

	assertError(t, first, &iridium.Error{Kind: iridium.ErrTransport, Err: errCloseFailed})
	if second != nil || transport.closes != 1 {
		t.Errorf("second Close = %v after %d closes; want nil after 1", second, transport.closes)
	}
}

func TestCloseLeavesATransportWithoutCloseAlone(t *testing.T) {
	handset := iridium.NewHandset(newScripted())

	if err := handset.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// errCloseFailed is what countingCloser's Close reports.
var errCloseFailed = errors.New("close failed")

// countingCloser is a scripted transport that counts its closes, each of
// which fails. Its counter is only read once Close has returned.
type countingCloser struct {
	*scripted
	closes int
}

// Close counts the close and fails.
func (c *countingCloser) Close() error {
	c.closes++
	return errCloseFailed
}

// assertError fails the test unless err is an *iridium.Error equal to want,
// the CallMayStillBeUp flag aside.
func assertError(t *testing.T, err error, want *iridium.Error) {
	t.Helper()
	var got *iridium.Error
	if !errors.As(err, &got) {
		t.Errorf("error = %v, want %#v", err, want)
		return
	}
	if got.Kind != want.Kind || got.Command != want.Command || got.Code != want.Code ||
		got.Message != want.Message || !errors.Is(got.Err, want.Err) {
		t.Errorf("error = %#v (%v), want %#v (%v)", got, got, want, want)
	}
}
