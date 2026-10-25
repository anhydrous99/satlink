package iridium_test

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/debugtext"
	"github.com/anhydrous99/satlink/iridium"
)

// pppFlag is the byte that delimits PPP frames: a call's data.
const pppFlag = 0x7E

// testEscapeGuard is the escape guard of the tests: short, so that hanging
// up takes a fraction of a second.
const testEscapeGuard = 10 * time.Millisecond

// longEscapeGuard is an escape guard longer than DefaultEscapeGuard, as a
// handset with a long S12 guard time needs: a call ended with the default
// guard instead would show.
const longEscapeGuard = iridium.DefaultEscapeGuard + 500*time.Millisecond

// noDeadline is the longest timeout, which means no deadline.
const noDeadline = math.MaxInt64

// unendingWait bounds a wait that must end although nothing else bounds it,
// as it is put off for good otherwise.
const unendingWait = 30 * time.Second

// callSettings are the data call settings of the tests: short timeouts, no
// AT&D2 to script, and a cancellation of their own.
func callSettings() iridium.DataCallSettings {
	return iridium.DataCallSettings{
		Bearer:              nil,
		ConnectTimeout:      200 * time.Millisecond,
		EscapeGuard:         testEscapeGuard,
		EndCallWhenDTRDrops: false,
		Cancellation:        iridium.NewCancellation(),
	}
}

// connectReply is the handset's answer to a dial that connected.
const connectReply = "\r\nCONNECT 9600\r\n"

// testNumber is the number the tests dial.
const testNumber = "12025550100"

// dialStep is the dial of testNumber, answered by reply.
func dialStep(reply string) step {
	return step{"ATD" + testNumber + "\r", reply}
}

// mustDial dials testNumber over handset, failing the test if no call comes
// up.
func mustDial(t *testing.T, handset *iridium.Handset, settings iridium.DataCallSettings) *iridium.DataCall {
	t.Helper()
	call, err := handset.DialData(testNumber, settings)
	if err != nil {
		t.Fatalf("DialData: %v", err)
	}
	return call
}

// mustFailToDial dials testNumber over handset, failing the test if a call
// comes up, and returns the error.
func mustFailToDial(t *testing.T, handset *iridium.Handset, settings iridium.DataCallSettings) error {
	t.Helper()
	call, err := handset.DialData(testNumber, settings)
	if err == nil {
		t.Fatalf("DialData connected (%q), want an error", call.ConnectMessage())
	}
	return err
}

// readBytes reads from call until count bytes arrived or it went quiet.
func readBytes(t *testing.T, call *iridium.DataCall, count int) []byte {
	t.Helper()
	var received []byte
	buf := make([]byte, 64)
	for len(received) < count {
		n, err := call.ReadTimeout(buf, time.Millisecond)
		if err != nil {
			t.Fatalf("ReadTimeout: %v", err)
		}
		if n == 0 {
			break
		}
		received = append(received, buf[:n]...)
	}
	return received
}

// plusSigns counts the '+' written to transport: three per escape.
func plusSigns(transport *scripted) int {
	return bytes.Count(transport.history(), []byte("+"))
}

// assertCallMayStillBeUp fails the test unless err says whether the call
// may still be up as want does: through its sentinel, and in the field of
// its *iridium.Error, the CallMayStillBeUp field.
func assertCallMayStillBeUp(t *testing.T, err error, want bool) {
	t.Helper()
	if got := errors.Is(err, iridium.ErrCallMayStillBeUp); got != want {
		t.Errorf("errors.Is(%v, ErrCallMayStillBeUp) = %v, want %v", err, got, want)
	}
	var failure *iridium.Error
	if !errors.As(err, &failure) || failure.CallMayStillBeUp != want {
		t.Errorf("%#v: want an *iridium.Error with CallMayStillBeUp %v", err, want)
	}
}

// assertAllPlayed fails the test if transport has steps left unplayed.
func assertAllPlayed(t *testing.T, transport *scripted) {
	t.Helper()
	if unplayed := transport.unplayedSteps(); unplayed != 0 {
		t.Errorf("%d steps unplayed; written %q", unplayed, transport.history())
	}
}

// assertWaitedBetween fails the test unless later was written to transport
// at least atLeast after earlier, the first write of each in one piece.
func assertWaitedBetween(t *testing.T, transport *scripted, earlier, later string, atLeast time.Duration) {
	t.Helper()
	earlierAt, wroteEarlier := transport.writtenAt(earlier)
	laterAt, wroteLater := transport.writtenAt(later)
	if !wroteEarlier || !wroteLater {
		t.Fatalf("written %q, want %q and %q", transport.history(), earlier, later)
	}
	if waited := laterAt.Sub(earlierAt); waited < atLeast {
		t.Errorf("%q followed %q after %v, want at least %v", later, earlier, waited, atLeast)
	}
}

func TestDialPreparesTheHandsetAndKeepsDataThatArrivedWithConnect(t *testing.T) {
	transport := newScripted(
		step{"AT&D2\r", okReply},
		step{"AT+CBST=6,0,1\r", okReply},
		step{"ATD008816000022\r", "\r\nCONNECT 9600\r\n\x7e\xff\x7d\x23\xc0\x21"},
	)
	settings := callSettings()
	settings.Bearer = &iridium.BearerService{Speed: 6, Name: 0, ConnectionElement: 1}
	settings.EndCallWhenDTRDrops = true

	call, err := newTestHandset(transport).DialData("008816000022", settings)

	if err != nil {
		t.Fatalf("DialData: %v", err)
	}
	if message := call.ConnectMessage(); message != "9600" {
		t.Errorf("ConnectMessage = %q, want 9600", message)
	}
	want := []byte{pppFlag, 0xFF, 0x7D, 0x23, 0xC0, 0x21}
	if received := readBytes(t, call, len(want)); !bytes.Equal(received, want) {
		t.Errorf("read % x, want % x", received, want)
	}
}

func TestAFailedDialReturnsAUsableHandset(t *testing.T) {
	handset := newTestHandset(newScripted(dialStep("\r\nBUSY\r\n"), step{"AT\r", okReply}))

	err := mustFailToDial(t, handset, callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "BUSY"})
	assertCallMayStillBeUp(t, err, false)
	if _, err := handset.Command("AT"); err != nil {
		t.Errorf("the handset does not answer AT after the failed dial: %v", err)
	}
}

func TestAnUndialableNumberIsRefusedBeforeDialling(t *testing.T) {
	transport := newScripted()

	_, err := newTestHandset(transport).DialData("123;ATH", callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrInvalidArgument, Message: `"123;ATH" is not a dialable number`})
	if written := transport.history(); len(written) != 0 {
		t.Errorf("written %q, want nothing", written)
	}
}

func TestNoticesTheCarrierDroppingMidCall(t *testing.T) {
	transport := newScripted(dialStep(connectReply), step{"ping", "pong\r\nNO CARRIER\r\n"})
	call := mustDial(t, newTestHandset(transport), callSettings())

	if err := call.WriteAll([]byte("ping")); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	if call.CarrierLost() {
		t.Error("the carrier is lost before NO CARRIER came")
	}
	readBytes(t, call, 100)

	if !call.CarrierLost() {
		t.Error("the carrier is not lost after NO CARRIER")
	}
}

func TestHangUpEscapesToCommandModeThenHangsUp(t *testing.T) {
	transport := newScripted(
		dialStep(connectReply),
		step{"+++", "\x7e\xff\x03late data\r\nOK\r\n"},
		step{"ATH\r", okReply},
	)
	call := mustDial(t, newTestHandset(transport), callSettings())
	if err := call.WriteAll([]byte("payload")); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}

	if err := call.HangUp(); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	assertAllPlayed(t, transport)
	if written := transport.history(); !bytes.HasSuffix(written, []byte("payload+++ATH\r")) {
		t.Errorf("written %q, want it to end with payload+++ATH\\r", written)
	}
}

func TestHangUpAfterNoCarrierStillEscapesThenHangsUp(t *testing.T) {
	// The NO CARRIER might have come from the remote end, so it proves
	// nothing.
	transport := newScripted(
		dialStep("\r\nCONNECT 9600\r\n\r\nNO CARRIER\r\n"),
		step{"ATH\r", okReply},
	)
	call := mustDial(t, newTestHandset(transport), callSettings())
	readBytes(t, call, 100)
	if !call.CarrierLost() {
		t.Fatal("the carrier is not lost after NO CARRIER")
	}

	if err := call.HangUp(); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	assertAllPlayed(t, transport)
	if plus := plusSigns(transport); plus != len("+++") {
		t.Errorf("%d '+' written, want %d: one escape is enough", plus, len("+++"))
	}
	if written := transport.history(); !bytes.HasSuffix(written, []byte("ATH\r")) {
		t.Errorf("written %q, want ATH last", written)
	}
}

func TestNothingIsWrittenAfterNoCarrier(t *testing.T) {
	transport := newScripted(dialStep(connectReply), step{"ping", "\r\nNO CARRIER\r\n"})
	call := mustDial(t, newTestHandset(transport), callSettings())
	if err := call.WriteAll([]byte("ping")); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	readBytes(t, call, 100)

	// In command mode, A/ would repeat the last command: the dial.
	err := call.WriteAll([]byte("A/"))

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "NO CARRIER"})
	if written := transport.history(); bytes.Contains(written, []byte("A/")) {
		t.Errorf("written %q after NO CARRIER", written)
	}
}

func TestADialThatTimesOutIsAborted(t *testing.T) {
	handset := newTestHandset(newScripted(
		dialStep(""),
		step{"\r", "\r\nNO CARRIER\r\n"},
		step{"AT\r", okReply},
	))

	err := mustFailToDial(t, handset, callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "ATD" + testNumber})
	if _, err := handset.Command("AT"); err != nil {
		t.Errorf("the attempt ended, yet commands fail: %v", err)
	}
}

func TestACallThatConnectsAfterTheTimeoutIsEndedAgain(t *testing.T) {
	transport := newScripted(
		dialStep(""),
		step{"\r", connectReply},
		step{"+++", okReply},
		step{"ATH\r", okReply},
	)

	err := mustFailToDial(t, newTestHandset(transport), callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "ATD" + testNumber})
	assertCallMayStillBeUp(t, err, false)
	assertAllPlayed(t, transport)
}

func TestStaleAnswersDoNotDecideTheDial(t *testing.T) {
	// A NO CARRIER left over from an earlier timed-out command.
	transport := newScripted(dialStep(connectReply)).unsolicited("\r\nNO CARRIER\r\n")

	call := mustDial(t, newTestHandset(transport), callSettings())

	if message := call.ConnectMessage(); message != "9600" {
		t.Errorf("ConnectMessage = %q, want 9600", message)
	}
}

func TestAnOKBeforeConnectDoesNotEndTheDial(t *testing.T) {
	transport := newScripted(dialStep("\r\nOK\r\n\r\n+CR: REL ASYNC\r\n\r\nCONNECT 19200\r\n"))

	call := mustDial(t, newTestHandset(transport), callSettings())

	if message := call.ConnectMessage(); message != "19200" {
		t.Errorf("ConnectMessage = %q, want 19200", message)
	}
}

func TestAnsweringAVoiceCallHangsItUp(t *testing.T) {
	// NO CARRIER to ATH means the voice call had just ended: no data call
	// is left to escape from.
	for name, hangUpReply := range map[string]string{"OK": okReply, "NO CARRIER": "\r\nNO CARRIER\r\n"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			transport := newScripted(step{"ATA\r", okReply}, step{"ATH\r", hangUpReply})

			_, err := newTestHandset(transport).AnswerData(callSettings())

			assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "ATA answered a voice call, not a data call"})
			if written := string(transport.history()); written != "ATA\rATH\r" {
				t.Errorf("written %q, want only ATA and ATH", written)
			}
		})
	}
}

func TestAHandsetDeafToTheEscapeIsHungUpByDroppingDTR(t *testing.T) {
	transport := newScripted(
		dialStep(connectReply),
		step{dtrDropped, "\r\nNO CARRIER\r\n"},
		step{"ATH\r", okReply},
	)
	call := mustDial(t, newTestHandset(transport), callSettings())

	if err := call.HangUp(); err != nil {
		t.Fatalf("HangUp: %v; the DTR drop should end the call", err)
	}

	assertHungUpByDroppingDTR(t, transport)
	assertAllPlayed(t, transport)
}

func TestTheFarEndsDataAfterADTRDropDoesNotPassForEvents(t *testing.T) {
	// What the handset relays as the drop takes effect is the far end's
	// data, a CONNECT and a RING of its own included: dropped, not queued.
	transport := newScripted(
		dialStep(connectReply),
		step{dtrDropped, "\r\nCONNECT 9600\r\n\r\nRING\r\n"},
		step{"ATH\r", okReply},
	)
	handset := newTestHandset(transport)
	call := mustDial(t, handset, callSettings())

	if err := call.HangUp(); err != nil {
		t.Fatalf("HangUp: %v; the DTR drop should end the call", err)
	}

	if urcs, err := handset.PollURCs(0); err != nil || len(urcs) != 0 {
		t.Errorf("PollURCs = %q, %v; want no event", urcs, err)
	}
	assertAllPlayed(t, transport)
}

func TestAHandsetThatNeverLeavesDataModeIsReported(t *testing.T) {
	transport := newScripted(dialStep(connectReply))
	call := mustDial(t, newTestHandset(transport), callSettings())

	err := call.HangUp()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "+++"})
	assertCallMayStillBeUp(t, err, true)
	if plus := plusSigns(transport); plus != 2*len("+++") {
		t.Errorf("%d '+' written, want %d: one retry after the first escape", plus, 2*len("+++"))
	}
	if written := transport.history(); !bytes.HasSuffix(written, []byte("ATH\r")) {
		t.Errorf("written %q, want ATH last: it goes out anyway", written)
	}
}

func TestAnOKLineInTheFarEndsDataDoesNotEndTheHangUp(t *testing.T) {
	// Deaf to the escape, the handset passes ATH on to the far end, whose
	// reply holds an OK line: only a DTR drop really ends the call.
	transport := newScripted(
		dialStep(connectReply),
		step{"ATH\r", "\nOK\n"},
		step{dtrDropped, "\r\nNO CARRIER\r\n"},
		step{"ATH\r", okReply},
	)
	call := mustDial(t, newTestHandset(transport), callSettings())

	if err := call.HangUp(); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	assertHungUpByDroppingDTR(t, transport)
	assertAllPlayed(t, transport)
}

func TestATHWaitsOutTheGuardAfterAnEarlyOK(t *testing.T) {
	guard := 300 * time.Millisecond
	transport := newScripted(
		dialStep(connectReply),
		// Too early to be the handset's: a byte from us now would cancel
		// the escape.
		step{"+++", okReply},
		step{"ATH\r", okReply},
	)
	settings := callSettings()
	settings.EscapeGuard = guard
	call := mustDial(t, newTestHandset(transport), settings)

	if err := call.HangUp(); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	assertWaitedBetween(t, transport, "+++", "ATH\r", guard)
}

func TestADialWhoseAbortGoesUnansweredIsEndedAnyway(t *testing.T) {
	// Slow — the ten-second abort grace — but timing-insensitive.
	t.Parallel()
	// Nothing proves the attempt ended, so it might still connect.
	transport := newScripted(
		dialStep(""),
		step{"\r", ""},
		step{"+++", okReply},
		step{"ATH\r", okReply},
	)

	err := mustFailToDial(t, newTestHandset(transport), callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "ATD" + testNumber})
	assertCallMayStillBeUp(t, err, false)
	assertAllPlayed(t, transport)
}

func TestAHandsetStillRelayingDataAfterDTRDropsIsReportedPromptly(t *testing.T) {
	if testing.Short() {
		t.Skip("takes seconds of real time")
	}
	transport := endlessCallFor(8 * time.Second)
	handset := newTestHandset(transport)
	started := time.Now()

	err := handset.EndDataCall(testEscapeGuard)

	if err == nil {
		t.Fatal("EndDataCall succeeded, yet the call is still up")
	}
	assertCallMayStillBeUp(t, err, true)
	if elapsed := time.Since(started); elapsed >= 4*time.Second {
		t.Errorf("EndDataCall took %v, want under 4s", elapsed)
	}
	// Two seconds of data after the drop show that it changed
	// nothing, and end the hang-up: no ATH goes out to wait for an answer
	// that a handset still online never gives.
	if !transport.dtrDropped {
		t.Error("DTR was never dropped")
	}
	if written := transport.writtenAfterDTRDrop; len(written) != 0 {
		t.Errorf("written %q after the DTR drop, want nothing: the data still flowing show it changed nothing", written)
	}
}

func TestAnEscapeGuardBeyondAnyHandsetsIsRefusedBeforeDialling(t *testing.T) {
	transport := newScripted()
	settings := callSettings()
	settings.EscapeGuard = math.MaxInt64

	_, err := newTestHandset(transport).DialData(testNumber, settings)

	assertError(t, err, &iridium.Error{
		Kind:    iridium.ErrInvalidArgument,
		Message: "an escape guard of 9223372036.854775807s is longer than 10s",
	})
	if written := transport.history(); len(written) != 0 {
		t.Errorf("written %q, want nothing", written)
	}
}

func TestACallLeftUpIsEndedBeforeDiallingAgain(t *testing.T) {
	// An earlier session left the handset online with a quiet far end:
	// AT&D2 goes into the call unanswered.
	transport := newScripted(step{"AT&D2\r", ""}, step{"+++", okReply}, step{"ATH\r", okReply})
	settings := callSettings()
	settings.EndCallWhenDTRDrops = true

	err := mustFailToDial(t, newTestHandset(transport), settings)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "AT&D2"})
	assertCallMayStillBeUp(t, err, false)
	assertAllPlayed(t, transport)
	if written := transport.history(); bytes.Contains(written, []byte("ATD")) {
		t.Errorf("written %q, want no dial: the handset was not ready", written)
	}
}

func TestAHandsetFoundRelayingACallItCannotEndIsReported(t *testing.T) {
	// Slow — two seconds of data before the escape and after the DTR drop —
	// but timing-insensitive.
	t.Parallel()
	// DialData fails with the hang-up's error, flagged; Answer with
	// ErrCallMayStillBeUp, the kind on which mobile strands the handset.
	tests := map[string]struct {
		method func(*testing.T, *iridium.Handset) error
		want   *iridium.Error
	}{
		"DialData": {
			method: func(t *testing.T, handset *iridium.Handset) error {
				return mustFailToDial(t, handset, callSettings())
			},
			want: &iridium.Error{Kind: iridium.ErrTimeout, Command: "+++"},
		},
		"Answer": {
			method: func(_ *testing.T, handset *iridium.Handset) error { return handset.Answer() },
			want: &iridium.Error{Kind: iridium.ErrCallMayStillBeUp,
				Message: `the handset keeps sending data, and the call could not be ended: timed out waiting for a response to "+++"`},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := test.method(t, newTestHandset(endlessCallFor(30*time.Second)))

			assertError(t, err, test.want)
			assertCallMayStillBeUp(t, err, true)
		})
	}
}

func TestAReadFailingWhileWaitingForConnectAbortsTheAttempt(t *testing.T) {
	// The attempt went on regardless, and connected.
	transport := newScripted(dialStep(""), step{"\r", connectReply}, step{"+++", okReply}, step{"ATH\r", okReply})
	handset := newTestHandset(hiccupAfter("ATD", transport))

	err := mustFailToDial(t, handset, callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Err: errUSBHiccup})
	assertCallMayStillBeUp(t, err, false)
	assertAllPlayed(t, transport)
}

func TestAHangUpWithoutACommandDeadlineStillReachesTheDTRDrop(t *testing.T) {
	// Slow — ten seconds for ATH's answer — but timing-insensitive.
	t.Parallel()
	// Deaf to the escape and to ATH: only the DTR drop ends the call.
	transport := newScripted(dialStep(connectReply), step{dtrDropped, "\r\nNO CARRIER\r\n"}, step{"ATH\r", okReply})
	handset := newTestHandset(transport)
	handset.SetTimeout(noDeadline)
	call := mustDial(t, handset, callSettings())

	var hangUpErr error
	finishesWithin(t, unendingWait, "the hang-up", func() { hangUpErr = call.HangUp() })

	if hangUpErr != nil {
		t.Fatalf("HangUp: %v; the DTR drop should end the call", hangUpErr)
	}
	assertHungUpByDroppingDTR(t, transport)
}

func TestAnUnansweredATHAfterAConfirmedEscapeStillReachesTheDTRDrop(t *testing.T) {
	// Slow — ten seconds for ATH's answer — but timing-insensitive.
	t.Parallel()
	// The escape is confirmed, but ATH goes unanswered: without a command
	// deadline, only the ten-second cap on ATH's wait lets the DTR drop end
	// the call.
	transport := newScripted(
		dialStep(connectReply),
		step{"+++", okReply},
		step{"ATH\r", ""},
		step{dtrDropped, "\r\nNO CARRIER\r\n"},
		step{"ATH\r", okReply},
	)
	handset := newTestHandset(transport)
	handset.SetTimeout(noDeadline)
	call := mustDial(t, handset, callSettings())

	var hangUpErr error
	finishesWithin(t, unendingWait, "the hang-up", func() { hangUpErr = call.HangUp() })

	if hangUpErr != nil {
		t.Fatalf("HangUp: %v; the DTR drop should end the call", hangUpErr)
	}
	assertHungUpByDroppingDTR(t, transport)
	assertAllPlayed(t, transport)
}

func TestAnErrorPassedOnKeepsACallThatMayStillBeUp(t *testing.T) {
	// An error passed on keeps saying that the call may still be up, even
	// wrapped.
	transport := newScripted(dialStep(connectReply))
	call := mustDial(t, newTestHandset(transport), callSettings())
	closeCall := func(call *iridium.DataCall) error {
		if err := call.HangUp(); err != nil {
			return fmt.Errorf("closing the call: %w", err)
		}
		return nil
	}

	err := closeCall(call)

	if !errors.Is(err, iridium.ErrCallMayStillBeUp) {
		t.Errorf("closeCall = %v, want an error that the call may still be up", err)
	}
}

func TestADataCallTheVoiceAPIAnsweredUnseenIsEnded(t *testing.T) {
	// Slow — an escape with the default guard — but timing-insensitive.
	t.Parallel()
	// The read after ATA fails, and the handset connects a data call.
	transport := newScripted(step{"ATA\r", ""}, step{"\r", connectReply}, step{"+++", okReply}, step{"ATH\r", okReply})
	handset := newTestHandset(hiccupAfter("ATA", transport))

	err := handset.Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Err: errUSBHiccup})
	assertAllPlayed(t, transport)
}

func TestAVoiceCallAnsweredWhileAReadFailedIsKept(t *testing.T) {
	transport := newScripted(step{"ATA\r", okReply}, step{"AT+CSQ\r", "\r\n+CSQ:4\r\n\r\nOK\r\n"})
	handset := newTestHandset(hiccupAfter("ATA", transport))

	if err := handset.Answer(); err != nil {
		t.Fatalf("Answer: %v; the voice call was answered", err)
	}

	if bars, err := handset.SignalQuality(); err != nil || bars != 4 {
		t.Errorf("SignalQuality = %d, %v; want 4", bars, err)
	}
	if written := transport.history(); bytes.Contains(written, []byte("ATH")) {
		t.Errorf("written %q, want no ATH: the voice call is kept", written)
	}
}

func TestACallLeftUpIsEndedBeforeDiallingWithoutACommandDeadline(t *testing.T) {
	// Slow — ten seconds for AT&D2's answer — but timing-insensitive.
	t.Parallel()
	transport := newScripted(step{"AT&D2\r", ""}, step{"+++", okReply}, step{"ATH\r", okReply})
	handset := newTestHandset(transport)
	handset.SetTimeout(noDeadline)
	settings := callSettings()
	settings.EndCallWhenDTRDrops = true

	var dialErr error
	finishesWithin(t, unendingWait, "the dial", func() { _, dialErr = handset.DialData(testNumber, settings) })

	if dialErr == nil {
		t.Fatal("DialData connected, yet the handset was not ready to dial")
	}
	assertAllPlayed(t, transport)
}

func TestAVoiceCallAnsweredByMistakeThatCannotBeHungUpIsReported(t *testing.T) {
	transport := newScripted(step{"ATA\r", okReply})

	_, err := newTestHandset(transport).AnswerData(callSettings())

	if err == nil {
		t.Fatal("AnswerData connected, yet a voice call is no data call")
	}
	assertCallMayStillBeUp(t, err, true)
}

func TestAnEarlyOKStillUnreadWhenAReadFailsDoesNotEndTheAbort(t *testing.T) {
	// The read after ATD fails before its early OK is taken; the abort is
	// too soon to count (V.250 §5.6.1), and the call connects.
	transport := newScripted(dialStep(okReply), step{"\r", connectReply}, step{"+++", okReply}, step{"ATH\r", okReply})
	handset := newTestHandset(hiccupAfter("ATD", transport))

	err := mustFailToDial(t, handset, callSettings())

	assertCallMayStillBeUp(t, err, false)
	assertAllPlayed(t, transport)
}

func TestAVoiceCallWhoseOKWasLostWithAFailedReadIsKept(t *testing.T) {
	// Slow — the ten-second abort grace — but timing-insensitive.
	t.Parallel()
	// The failed read took ATA's OK with it, and the abort, an empty line
	// to a handset in a voice call, goes unanswered. The handset still
	// answers commands, and shows a voice call up.
	transport := newScripted(
		step{"ATA\r", ""},
		step{"AT\r", okReply},
		step{"AT+CPAS\r", readyStatusReply},
		step{"AT+CLCC\r", "\r\n+CLCC: 000\r\n\r\nOK\r\n"},
	)
	handset := newTestHandset(hiccupAfter("ATA", transport))

	if err := handset.Answer(); err != nil {
		t.Fatalf("Answer: %v; the voice call was answered", err)
	}

	assertAllPlayed(t, transport)
	if written := transport.history(); bytes.Contains(written, []byte("ATH")) {
		t.Errorf("written %q, want no ATH: the voice call is kept", written)
	}
}

func TestADataCallWhoseConnectWasLostWithAFailedReadIsEnded(t *testing.T) {
	// Slow — the ten-second abort grace, six seconds of AT and an escape
	// with the default guard — but timing-insensitive.
	t.Parallel()
	// Online, with a quiet far end: nothing answers AT, so it is no voice
	// call.
	transport := newScripted(step{"ATA\r", ""}, step{"+++", okReply}, step{"ATH\r", okReply})
	handset := newTestHandset(hiccupAfter("ATA", transport))

	err := handset.Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Err: errUSBHiccup})
	assertAllPlayed(t, transport)
}

func TestADataCallWhoseConnectCameAfterAFailedAbortIsEnded(t *testing.T) {
	// Slow — an escape with the default guard — but timing-insensitive.
	t.Parallel()
	// The read after the abort failed, and the CONNECT came after it: the
	// handset is online, and AT would go to the far end as data.
	transport := newScripted(step{"ATA\r", ""}, step{"\r", connectReply}, step{"+++", okReply}, step{"ATH\r", okReply})
	handset := newTestHandset(hiccupAfter("ATA", transport).andAfter("\r"))

	err := handset.Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Err: errUSBHiccup})
	if written := string(transport.history()); written != "ATA\r\r+++ATH\r" {
		t.Errorf("written %q, want ATA, the abort, the escape and ATH", written)
	}
}

func TestALateAnswerToATimedOutCommandDoesNotPassForATAs(t *testing.T) {
	// Slow — an escape with the default guard — but timing-insensitive.
	t.Parallel()
	// AT+CSQ timed out; its answer came later, and is still unread when a
	// data call rings.
	transport := newScripted(step{"ATA\r", connectReply}, step{"+++", okReply}, step{"ATH\r", okReply}).
		unsolicited("\r\n+CSQ:4\r\n\r\nOK\r\n")
	handset := newTestHandset(transport)

	err := handset.Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "ATA answered a data call, which was ended again"})
	assertAllPlayed(t, transport)
}

// The tests below cover the escape's guard of silence.

func TestTheEscapeKeepsTheSettingsGuardOfSilence(t *testing.T) {
	// Slow — two guards longer than the default — but timing-insensitive.
	t.Parallel()
	// The handset takes +++ for the escape only after a guard of silence:
	// sooner, it passes +++ to the far end as data, and the hang-up waits
	// three guards for the escape to be tried again. A handset may need a
	// longer guard than the default, which the settings then give.
	transport := newScripted(slices.Concat([]step{dialStep(connectReply)}, confirmedHangUp())...)
	settings := callSettings()
	settings.EscapeGuard = longEscapeGuard
	call := mustDial(t, newTestHandset(transport), settings)
	if err := call.WriteAll([]byte("payload")); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}

	if err := call.HangUp(); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	assertWaitedBetween(t, transport, "payload", "+++", longEscapeGuard)
}

func TestTheEscapeCountsItsGuardOfSilenceFromTheLastWrite(t *testing.T) {
	// The call writes a guard after CONNECT: a silence counted from CONNECT
	// would be over by then, and +++ would follow the data at once, which
	// the handset passes to the far end as data.
	guard := 300 * time.Millisecond
	transport := newScripted(slices.Concat([]step{dialStep(connectReply)}, confirmedHangUp())...)
	settings := callSettings()
	settings.EscapeGuard = guard
	call := mustDial(t, newTestHandset(transport), settings)
	// The call's own quiet before it writes, not a wait for anything.
	time.Sleep(guard)
	if err := call.WriteAll([]byte("payload")); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}

	hangUp(t, call)

	assertWaitedBetween(t, transport, "payload", "+++", guard)
}

func TestTheEscapeKeepsAGuardOfSilenceAfterConnect(t *testing.T) {
	// A call hung up before it wrote anything keeps the guard of silence
	// before +++ all the same: it counts from CONNECT, when the handset went
	// online.
	guard := 300 * time.Millisecond
	transport := newScripted(slices.Concat([]step{dialStep(connectReply)}, confirmedHangUp())...)
	settings := callSettings()
	settings.EscapeGuard = guard
	call := mustDial(t, newTestHandset(transport), settings)

	hangUp(t, call)

	assertWaitedBetween(t, transport, "ATD"+testNumber+"\r", "+++", guard)
}

func TestACallThatConnectsAfterTheTimeoutIsEndedWithTheSettingsGuard(t *testing.T) {
	// Slow — two guards longer than the default — but timing-insensitive.
	t.Parallel()
	// The attempt given up on connected after all: it is ended with the
	// settings' guard too, which a handset may need longer than the default.
	transport := newScripted(slices.Concat([]step{dialStep(""), {"\r", connectReply}}, confirmedHangUp())...)
	settings := callSettings()
	settings.EscapeGuard = longEscapeGuard

	mustFailToDial(t, newTestHandset(transport), settings)

	assertWaitedBetween(t, transport, "\r", "+++", longEscapeGuard)
}

func TestAnAnswerWhoseAbortEndsInNoCarrierAnsweredNothing(t *testing.T) {
	transport := newScripted(step{"ATA\r", ""}, step{"\r", "\r\nNO CARRIER\r\n"})
	handset := newTestHandset(hiccupAfter("ATA", transport))

	err := handset.Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Err: errUSBHiccup})
	assertAllPlayed(t, transport)
	if written := transport.history(); bytes.Contains(written, []byte("+++")) {
		t.Errorf("written %q, want no escape: no call came up", written)
	}
}

func TestAnUnconfirmedAnswerWhoseHandsetShowsADataCallEndsIt(t *testing.T) {
	// Slow — the ten-second abort grace and an escape with the default
	// guard — but timing-insensitive.
	t.Parallel()
	// The handset answers commands, but +CPAS shows a data call, held in
	// online-command mode: no voice call to keep.
	transport := newScripted(
		step{"ATA\r", ""},
		step{"AT\r", okReply},
		step{"AT+CPAS\r", dataCallStatusReply},
		step{"+++", okReply},
		step{"ATH\r", okReply},
	)
	handset := newTestHandset(hiccupAfter("ATA", transport))

	err := handset.Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Err: errUSBHiccup})
	assertAllPlayed(t, transport)
}

func TestAVoiceCallWhoseAbortWasAnsweredLateIsKept(t *testing.T) {
	// The reads after ATA and after the abort fail, and the abort's OK comes
	// late, once Answer asks the handset which call is up. Taken for AT's
	// answer, it would leave each command after it the answer of the one
	// before, and the voice call answered would be ended as a data call.
	transport := newScripted(
		step{"ATA\r", ""},
		step{"\r", okReply},
		step{"AT\r", okReply},
		step{"AT+CPAS\r", readyStatusReply},
		step{"AT+CLCC\r", "\r\n+CLCC: 000\r\n\r\nOK\r\n"},
	).answersLate("\r", lateAnswerBeforeACall)
	handset := newTestHandset(hiccupAfter("ATA", transport).andAfter("\r"))

	if err := handset.Answer(); err != nil {
		t.Fatalf("Answer: %v; the voice call was answered", err)
	}

	assertAllPlayed(t, transport)
	if written := transport.history(); bytes.Contains(written, []byte("+++")) || bytes.Contains(written, []byte("ATH")) {
		t.Errorf("written %q, want neither an escape nor ATH: the voice call is kept", written)
	}
}

func TestAnsweringADataCallThatIsNotThereFails(t *testing.T) {
	transport := newScripted(step{"ATA\r", "\r\nNO CARRIER\r\n"})

	_, err := newTestHandset(transport).AnswerData(callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "NO CARRIER"})
	assertCallMayStillBeUp(t, err, false)
	if written := transport.history(); bytes.Contains(written, []byte("+++")) {
		t.Errorf("written %q, want no escape: no call came up", written)
	}
}

func TestARefusedBearerFailsTheDialWithoutEndingACall(t *testing.T) {
	// ERROR is an answer: the handset listens, so it is in no call.
	transport := newScripted(step{"AT+CBST=6,0,1\r", "\r\nERROR\r\n"})
	settings := callSettings()
	settings.Bearer = &iridium.BearerService{Speed: 6, Name: 0, ConnectionElement: 1}

	err := mustFailToDial(t, newTestHandset(transport), settings)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCommandFailed, Command: "AT+CBST=6,0,1"})
	if written := transport.history(); bytes.Contains(written, []byte("+++")) || bytes.Contains(written, []byte("ATD")) {
		t.Errorf("written %q, want neither an escape nor a dial", written)
	}
}

func TestURCsDuringTheAbortAreQueuedAndOtherLinesDropped(t *testing.T) {
	// The intermediate result code is no unsolicited one.
	abortAnswer := "\r\n+CR: REL ASYNC\r\n\r\nRING\r\n\r\nNO CARRIER\r\n"
	handset := newTestHandset(newScripted(dialStep(""), step{"\r", abortAnswer}))

	err := mustFailToDial(t, handset, callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "ATD" + testNumber})
	if urcs := handset.TakeURCs(); !slices.Equal(urcs, []string{"RING"}) {
		t.Errorf("TakeURCs = %q, want only RING", urcs)
	}
}

func TestADialThatTimesOutAndCannotBeEndedMayStillBeUp(t *testing.T) {
	// The late CONNECT shows the call came up, and the handset then ignores
	// the escape, ATH and the DTR drop: the hang-up's error is reported.
	transport := newScripted(dialStep(""), step{"\r", connectReply})

	err := mustFailToDial(t, newTestHandset(transport), callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "+++"})
	assertCallMayStillBeUp(t, err, true)
}

func TestADialWhoseAbortCannotBeSentIsEndedAnyway(t *testing.T) {
	transport := refusingWrite{
		scripted: newScripted(dialStep(""), step{"+++", okReply}, step{"ATH\r", okReply}),
		refused:  "\r",
	}

	_, err := newTestHandset(transport).DialData(testNumber, callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Err: errUnplugged})
	assertCallMayStillBeUp(t, err, false)
	assertAllPlayed(t, transport.scripted)
}

func TestADialWhoseAbortGoesUnreadIsEndedAnyway(t *testing.T) {
	// The read after the abort fails, and so does every way to end the call.
	transport := newScripted(dialStep(""), step{"\r", ""}).unpluggedOncePlayedOut()

	err := mustFailToDial(t, newTestHandset(transport), callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Err: errUnplugged})
	assertCallMayStillBeUp(t, err, true)
}

func TestAnsweringADataCallConnects(t *testing.T) {
	transport := newScripted(step{"ATA\r", "\r\nCONNECT\r\n"}, step{"+++", okReply}, step{"ATH\r", okReply})

	call, err := newTestHandset(transport).AnswerData(callSettings())

	if err != nil {
		t.Fatalf("AnswerData: %v", err)
	}
	if message := call.ConnectMessage(); message != "" {
		t.Errorf("ConnectMessage = %q, want none", message)
	}
	if err := call.HangUp(); err != nil {
		t.Errorf("HangUp: %v", err)
	}
	assertAllPlayed(t, transport)
}

func TestAHungUpCallIsOver(t *testing.T) {
	transport := newScripted(dialStep(connectReply), step{"+++", okReply}, step{"ATH\r", okReply})
	call := mustDial(t, newTestHandset(transport), callSettings())
	if err := call.HangUp(); err != nil {
		t.Fatalf("HangUp: %v", err)
	}
	written := transport.history()

	errs := map[string]error{
		"WriteAll": call.WriteAll([]byte("data")),
		"RaiseDTR": call.RaiseDTR(),
		"DropDTR":  call.DropDTR(),
		"HangUp":   call.HangUp(),
	}
	_, errs["ReadTimeout"] = call.ReadTimeout(make([]byte, 8), time.Millisecond)

	for method, err := range errs {
		t.Run(method, func(t *testing.T) {
			assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "the data call is over"})
			assertCallMayStillBeUp(t, err, false)
		})
	}
	if after := transport.history(); !bytes.Equal(after, written) {
		t.Errorf("written %q after the hang-up", after[len(written):])
	}
	if call.ConnectMessage() != "9600" || call.CarrierLost() {
		t.Errorf("the call's last state is lost: %q, carrier lost %v", call.ConnectMessage(), call.CarrierLost())
	}
}

func TestDTRChangesDuringACallReachTheTransport(t *testing.T) {
	transport := newScripted(dialStep(connectReply))
	call := mustDial(t, newTestHandset(transport), callSettings())

	dropErr := call.DropDTR()
	raiseErr := call.RaiseDTR()

	if dropErr != nil || raiseErr != nil {
		t.Fatalf("DropDTR, RaiseDTR = %v, %v", dropErr, raiseErr)
	}
	assertDTRDroppedAndRaised(t, transport)
}

func TestATransportWithoutDTRSaysSo(t *testing.T) {
	transport := withoutDTR{newScripted(dialStep(connectReply))}
	call := mustDial(t, newTestHandset(transport), callSettings())

	err := call.DropDTR()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTransport, Message: "this transport has no DTR line"})
}

func TestEndDataCallEndsACallLeftUp(t *testing.T) {
	transport := newScripted(step{"+++", okReply}, step{"ATH\r", okReply})

	err := newTestHandset(transport).EndDataCall(testEscapeGuard)

	if err != nil {
		t.Errorf("EndDataCall: %v", err)
	}
	assertAllPlayed(t, transport)
}

func TestEndDataCallIsHarmlessWithoutACall(t *testing.T) {
	// In command mode the escape goes unanswered and ATH answers OK.
	transport := newScripted(step{"+++ATH\r", okReply})

	err := newTestHandset(transport).EndDataCall(testEscapeGuard)

	if err != nil {
		t.Errorf("EndDataCall: %v", err)
	}
	assertAllPlayed(t, transport)
}

func TestEndDataCallRefusesAGuardBeyondAnyHandsets(t *testing.T) {
	transport := newScripted()

	err := newTestHandset(transport).EndDataCall(90 * time.Second)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrInvalidArgument, Message: "an escape guard of 90s is longer than 10s"})
	assertCallMayStillBeUp(t, err, false)
	if written := transport.history(); len(written) != 0 {
		t.Errorf("written %q, want nothing", written)
	}
}

func TestEndDataCallThatFailsSaysTheCallMayStillBeUp(t *testing.T) {
	// The flag says what the CallMayStillBeUp field's doc says of every
	// error. Deaf to the escape, to ATH and to DTR, the handset may still be
	// in the call.
	transport := newScripted()

	err := newTestHandset(transport).EndDataCall(testEscapeGuard)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "+++"})
	assertCallMayStillBeUp(t, err, true)
}

func TestACallLeftUpThatIsEndedFailsTheDialWithWhatShowedIt(t *testing.T) {
	// Slow — two seconds of data before giving up — but timing-insensitive.
	t.Parallel()
	// The handset relays a call a crashed session left up, until the escape:
	// the call is ended, and the dial fails with what showed it was up.
	transport := newScripted(step{"+++", okReply}, step{"ATH\r", okReply})
	handset := newTestHandset(callUntil("+++", transport))

	err := mustFailToDial(t, handset, callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "the handset keeps sending data: it is still online"})
	assertCallMayStillBeUp(t, err, false)
	assertAllPlayed(t, transport)
}

func TestACallLeftUpIsEndedInsteadOfAnsweredAndItsDataAreNoPartOfTheNextAnswer(t *testing.T) {
	// Slow — two seconds of data before Answer gives up — but
	// timing-insensitive.
	t.Parallel()
	// The handset relays a call left up, until the escape: ATA would go
	// into it as its data, so Answer ends it instead. What Answer took in
	// before that must not come back as the start of the next command's
	// answer.
	transport := newScripted(step{"+++", okReply}, step{"ATH\r", okReply},
		step{"AT+CGMI\r", "\r\nIridium\r\n\r\nOK\r\n"})
	handset := newTestHandset(callUntil("+++", transport))

	answerErr := handset.Answer()
	response, commandErr := handset.Command("AT+CGMI")

	assertError(t, answerErr, &iridium.Error{Kind: iridium.ErrCall, Message: "the handset keeps sending data: it is still online"})
	assertCallMayStillBeUp(t, answerErr, false)
	if written := string(transport.history()); written != "+++ATH\rAT+CGMI\r" {
		t.Errorf("written %q, want the call ended, then AT+CGMI: Answer sends no ATA into a call", written)
	}
	if commandErr != nil || !slices.Equal(response.Lines, []string{"Iridium"}) {
		t.Errorf("Command = %q, %v; want only Iridium", response.Lines, commandErr)
	}
	if urcs := handset.TakeURCs(); len(urcs) != 0 {
		t.Errorf("TakeURCs = %q, want nothing: a call's data are no unsolicited results", urcs)
	}
}

func TestDefaultDataCallSettings(t *testing.T) {
	got := iridium.DefaultDataCallSettings()

	want := iridium.DataCallSettings{
		Bearer:              nil,
		ConnectTimeout:      90 * time.Second,
		EscapeGuard:         1500 * time.Millisecond,
		EndCallWhenDTRDrops: true,
		Cancellation:        got.Cancellation,
	}
	if got != want {
		t.Errorf("DefaultDataCallSettings = %+v, want %+v", got, want)
	}
	if got.Cancellation == nil || got.Cancellation.Cancelled() {
		t.Errorf("Cancellation = %v, want one not cancelled", got.Cancellation)
	}
	if got.Cancellation == iridium.DefaultDataCallSettings().Cancellation {
		t.Error("the settings of two calls share one cancellation")
	}
}

func TestAnAnswerThatTimesOutIsAborted(t *testing.T) {
	handset := newTestHandset(newScripted(
		step{"ATA\r", ""},
		step{"\r", "\r\nNO CARRIER\r\n"},
		step{"AT\r", okReply},
	))

	_, err := handset.AnswerData(callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "ATA"})
	assertCallMayStillBeUp(t, err, false)
	if _, err := handset.Command("AT"); err != nil {
		t.Errorf("the attempt ended, yet commands fail: %v", err)
	}
}

func TestAVoiceCallAnsweredInsteadThatCannotBeHungUpIsEndedAsADataCall(t *testing.T) {
	// ATH refused: the call is still up, and the escape and ATH end it.
	transport := newScripted(step{"ATA\r", okReply}, step{"ATH\r", "\r\nERROR\r\n"}, step{"+++", okReply}, step{"ATH\r", okReply})

	_, err := newTestHandset(transport).AnswerData(callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCommandFailed, Command: "ATH"})
	assertCallMayStillBeUp(t, err, false)
	assertAllPlayed(t, transport)
}

func TestADialKeepsTheURCsThatCameBeforeIt(t *testing.T) {
	// Stale answers are dropped before dialling, unsolicited codes kept.
	transport := newScripted(slices.Concat([]step{dialStep(connectReply)}, confirmedHangUp())...).
		unsolicited("\r\nNO CARRIER\r\n\r\nRING\r\n")
	handset := newTestHandset(transport)

	hangUp(t, mustDial(t, handset, callSettings()))

	if urcs := handset.TakeURCs(); !slices.Equal(urcs, []string{"RING"}) {
		t.Errorf("TakeURCs = %q, want RING", urcs)
	}
}

func TestALateAnswerArrivingDuringAnswerDoesNotPassForATAs(t *testing.T) {
	// Slow — an escape with the default guard — but timing-insensitive.
	t.Parallel()
	// AT+CSQ timed out, and its answer arrives once Answer has begun,
	// within the 200 ms of quiet with which it drops late answers: ATA's
	// own answer is the CONNECT of a data call, which must be ended.
	transport := newScripted(step{"ATA\r", connectReply}, step{"+++", okReply}, step{"ATH\r", okReply}).
		unsolicitedAfter(lateAnswerBeforeACall, "\r\n+CSQ:4\r\n\r\nOK\r\n")
	handset := newTestHandset(transport)

	err := handset.Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "ATA answered a data call, which was ended again"})
	assertAllPlayed(t, transport)
}

func TestAPartialAnswerLeftBeforeAnswerDoesNotPassForATAs(t *testing.T) {
	// Slow — an escape with the default guard — but timing-insensitive.
	t.Parallel()
	// AT+CBC timed out before the line end of its answer's OK came: what it
	// left in the line buffer must not run into ATA's answer, where it would
	// pass for a voice call's OK and leave the data call ATA answered up.
	transport := newScripted(
		step{"AT+CBC\r", "\r\n+CBC: 1,3\r\n\r\nOK"},
		step{"ATA\r", connectReply},
		step{"+++", okReply},
		step{"ATH\r", okReply},
	)
	handset := newTestHandset(transport)
	if _, err := handset.Battery(); !errors.Is(err, iridium.ErrTimeout) {
		t.Fatalf("Battery = %v, want a timeout: its answer never ended", err)
	}

	err := handset.Answer()

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "ATA answered a data call, which was ended again"})
	assertAllPlayed(t, transport)
}

func TestACallThatCameUpAsADialIsPreparedIsEndedAndFailsIt(t *testing.T) {
	// The handset answered a call on its own (ATS0=1) as the dial began,
	// within the 200 ms of quiet with which late answers are dropped: the
	// dial's commands would go into that call as its data.
	transport := newScripted(step{"+++", okReply}, step{"ATH\r", okReply}).
		unsolicitedAfter(lateAnswerBeforeACall, "\r\nRING\r\n"+connectReply)
	handset := newTestHandset(transport)

	err := mustFailToDial(t, handset, callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCall,
		Message: "a CONNECT arrived before the call was set up: the handset went online"})
	assertCallMayStillBeUp(t, err, false)
	if written := string(transport.history()); written != "+++ATH\r" {
		t.Errorf("written %q, want only the escape and ATH", written)
	}
	if urcs := handset.TakeURCs(); !slices.Equal(urcs, []string{"RING"}) {
		t.Errorf("TakeURCs = %q, want the RING before the CONNECT", urcs)
	}
}

func TestACallThatCameUpDuringADataCallsSetUpIsEndedOnceWithTheSettingsGuard(t *testing.T) {
	// A call the handset answered on its own (ATS0=1) came up during one of
	// the setup's own commands, or straight after it, in the same read: the
	// setup ends it once, with the guard its settings give, which a handset
	// may need longer than the default.
	dial := func(handset *iridium.Handset, settings iridium.DataCallSettings) error {
		settings.EndCallWhenDTRDrops = true
		_, err := handset.DialData(testNumber, settings)
		return err
	}
	answer := func(handset *iridium.Handset, settings iridium.DataCallSettings) error {
		_, err := handset.AnswerData(settings)
		return err
	}
	setUps := []struct {
		name string
		// steps lead to command, which a CONNECT answers, or follows.
		steps   []step
		command string
		// arrived says when the CONNECT came: "during" or "after" command.
		arrived string
		setUp   func(*iridium.Handset, iridium.DataCallSettings) error
	}{
		{"a dial's AT&D2", []step{{"AT&D2\r", connectReply}}, "AT&D2", "during", dial},
		{"after a dial's AT&D2", []step{{"AT&D2\r", okReply + connectReply}}, "AT&D2", "after", dial},
		{"the ATH of a voice call answered", []step{{"ATA\r", okReply}, {"ATH\r", connectReply}}, "ATH", "during", answer},
		{"after the ATH of a voice call answered", []step{{"ATA\r", okReply}, {"ATH\r", okReply + connectReply}},
			"ATH", "after", answer},
	}
	for _, setUp := range setUps {
		t.Run(setUp.name, func(t *testing.T) {
			// Slow — two guards longer than the default — but
			// timing-insensitive.
			t.Parallel()
			transport := newScripted(slices.Concat(setUp.steps, []step{{"+++", okReply}, {"ATH\r", okReply}})...).
				inWholeReads()
			settings := callSettings()
			settings.EscapeGuard = longEscapeGuard

			err := setUp.setUp(newTestHandset(transport), settings)

			assertError(t, err, &iridium.Error{Kind: iridium.ErrCall, Message: "a CONNECT arrived " + setUp.arrived +
				" " + debugtext.Quote(setUp.command) + ": the handset went online"})
			assertCallMayStillBeUp(t, err, false)
			if escapes := plusSigns(transport) / len("+++"); escapes != 1 {
				t.Errorf("%d escapes written (%q), want one", escapes, transport.history())
			}
			assertWaitedBetween(t, transport, setUp.command+"\r", "+++", longEscapeGuard)
			assertAllPlayed(t, transport)
		})
	}
}

func TestALateAnswerArrivingWhileADialIsPreparedDoesNotDecideIt(t *testing.T) {
	// A NO CARRIER left over from an earlier timed-out command arrives once
	// the dial has begun, within the 200 ms of quiet with which late answers
	// are dropped before dialling.
	transport := newScripted(slices.Concat([]step{dialStep(connectReply)}, confirmedHangUp())...).
		unsolicitedAfter(lateAnswerBeforeACall, "\r\nNO CARRIER\r\n")

	call := mustDial(t, newTestHandset(transport), callSettings())

	hangUp(t, call)
	assertAllPlayed(t, transport)
}

func TestALateAnswerThatKeepsComingIsNoCallLeftUp(t *testing.T) {
	// A listing that timed out is still coming, line by line, when a dial is
	// prepared: within the two seconds a handset may keep talking, it is a
	// late answer to drop, not a call's data, and no call is to be ended.
	transport := keepsListing(newScripted(slices.Concat([]step{dialStep(connectReply)}, confirmedHangUp())...))

	call := mustDial(t, newTestHandset(transport), callSettings())

	hangUp(t, call)
	assertAllPlayed(t, transport)
}

func TestAPartialAnswerLeftBeforeADialDoesNotDecideIt(t *testing.T) {
	// ATH timed out before the line end of its answer's NO CARRIER came: what
	// it left in the line buffer must not run into the dial's answer, where
	// it would fail the dial and leave the call that connected up.
	transport := newScripted(slices.Concat([]step{
		{"ATH\r", "\r\nNO CARRIER"},
		dialStep(connectReply),
	}, confirmedHangUp())...)
	handset := newTestHandset(transport)
	if err := handset.HangUp(); !errors.Is(err, iridium.ErrTimeout) {
		t.Fatalf("HangUp = %v, want a timeout: its answer never ended", err)
	}

	call := mustDial(t, handset, callSettings())

	hangUp(t, call)
	assertAllPlayed(t, transport)
}

func TestAnEscapeConfirmedLateButWithinThreeGuardsIsNotRepeated(t *testing.T) {
	// The handset confirms the escape a guard after +++, and is given
	// three: a confirmation later than usual still counts, and the escape is
	// not tried again.
	guard := 200 * time.Millisecond
	confirmedAfter := guard * 5 / 2 // two guards and a half
	transport := newScripted(confirmedHangUp()...).answersLate("+++", confirmedAfter)

	if err := newTestHandset(transport).EndDataCall(guard); err != nil {
		t.Fatalf("EndDataCall: %v", err)
	}

	if plus := plusSigns(transport); plus != len("+++") {
		t.Errorf("%d '+' written, want %d: one escape", plus, len("+++"))
	}
}

func TestNoCarrierConfirmsTheEscape(t *testing.T) {
	// The far end hung up as +++ went out: the handset's own NO CARRIER
	// says it is back in command mode, as its OK would, and the escape is
	// not tried again.
	transport := newScripted(dialStep(connectReply), step{"+++", "\r\nNO CARRIER\r\n"}, step{"ATH\r", okReply})
	settings := callSettings()
	// Long enough that a slow machine still reads the NO CARRIER within
	// the three guards the handset is given.
	settings.EscapeGuard = 100 * time.Millisecond
	call := mustDial(t, newTestHandset(transport), settings)

	if err := call.HangUp(); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	assertAllPlayed(t, transport)
	if plus := plusSigns(transport); plus != len("+++") {
		t.Errorf("%d '+' written, want %d: one escape, confirmed", plus, len("+++"))
	}
}

func TestNoCarrierConfirmsTheATHAfterAnUnconfirmedEscape(t *testing.T) {
	// The escape's OK was lost, so ATH goes out while the handset may still
	// be online: its own NO CARRIER, the call having just ended, confirms
	// the hang-up as its OK would, and DTR is left alone.
	transport := newScripted(dialStep(connectReply), step{"ATH\r", "\r\nNO CARRIER\r\n"})
	call := mustDial(t, newTestHandset(transport), callSettings())

	if err := call.HangUp(); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	assertAllPlayed(t, transport)
	if changes := transport.dtrChangesSoFar(); len(changes) != 0 {
		t.Errorf("DTR changes %+v, want none: the handset confirmed the hang-up", changes)
	}
}

func TestTheAnswerToADTRDropDoesNotPassForATHs(t *testing.T) {
	// Deaf to the escape, the handset answers the DTR drop before ATH goes
	// out. Taken for ATH's answer, it would leave ATH's own OK to answer the
	// next command, and each command after it the answer of the one before.
	// ATH's OK comes half a command timeout late: in time, but never read
	// together with the drop's answer.
	dropAnswers := map[string]string{
		"AT&D2 ends the call":                          "\r\nNO CARRIER\r\n",
		"AT&D1 takes the handset back to command mode": okReply,
	}
	for name, dropAnswer := range dropAnswers {
		t.Run(name, func(t *testing.T) {
			transport := newScripted(
				dialStep(connectReply),
				step{dtrDropped, dropAnswer},
				step{"ATH\r", okReply},
				step{"AT+CSQ\r", "\r\n+CSQ:4\r\n\r\nOK\r\n"},
			).answersLate("ATH\r", commandTimeout/2)
			handset := newTestHandset(transport)
			call := mustDial(t, handset, callSettings())

			if err := call.HangUp(); err != nil {
				t.Fatalf("HangUp: %v; the DTR drop should end the call", err)
			}

			if bars, err := handset.SignalQuality(); err != nil || bars != 4 {
				t.Errorf("SignalQuality = %d, %v; want 4: the command got the hang-up's answer", bars, err)
			}
			assertAllPlayed(t, transport)
		})
	}
}

func TestAnAbortsResultArrivingSecondsLaterStillEndsTheAttempt(t *testing.T) {
	// Slow — three seconds for the abort's result — but timing-insensitive.
	t.Parallel()
	// The final result that follows the abort may take seconds, and it
	// proves that the attempt ended: there is no call to end.
	transport := newScripted(dialStep(""), step{"\r", "\r\nNO CARRIER\r\n"}).answersLate("\r", 3*time.Second)

	err := mustFailToDial(t, newTestHandset(transport), callSettings())

	assertError(t, err, &iridium.Error{Kind: iridium.ErrTimeout, Command: "ATD" + testNumber})
	assertCallMayStillBeUp(t, err, false)
	if plus := plusSigns(transport); plus != 0 {
		t.Errorf("%d '+' written, want none: no call came up", plus)
	}
}

func TestTheDataOfACallEndedAgainAreNoURCs(t *testing.T) {
	// The attempt connected after all, and the far end's first bytes, a line
	// feed between PPP flags, came with the CONNECT: once the call is ended,
	// they mean nothing.
	farEndData := "\x7e\n\x7e"
	transport := newScripted(slices.Concat(
		[]step{dialStep(""), {"\r", connectReply + farEndData}}, confirmedHangUp(),
	)...)
	handset := newTestHandset(transport)

	mustFailToDial(t, handset, callSettings())

	assertAllPlayed(t, transport)
	if urcs := handset.TakeURCs(); len(urcs) != 0 {
		t.Errorf("TakeURCs = %q, want nothing: the call's data are no unsolicited results", urcs)
	}
}

// assertDTRDroppedAndRaised fails the test unless transport saw DTR drop
// and come back up, in that order and nothing else.
func assertDTRDroppedAndRaised(t *testing.T, transport *scripted) {
	t.Helper()
	changes := transport.dtrChangesSoFar()
	if len(changes) != 2 || changes[0].raised || !changes[1].raised {
		t.Errorf("DTR changes %+v, want a drop, then a raise: DTR comes back up", changes)
	}
}

// hangUpDTRDrop is how long a hang-up keeps DTR down, as HangUp promises:
// the handset needs 50 ms (its S25 debounce) to notice.
const hangUpDTRDrop = 150 * time.Millisecond

// assertHungUpByDroppingDTR fails the test unless transport saw DTR drop
// for at least hangUpDTRDrop and come back up, and nothing else.
func assertHungUpByDroppingDTR(t *testing.T, transport *scripted) {
	t.Helper()
	assertDTRDroppedAndRaised(t, transport)
	if changes := transport.dtrChangesSoFar(); len(changes) == 2 {
		if down := changes[1].at.Sub(changes[0].at); down < hangUpDTRDrop {
			t.Errorf("DTR was down for %v, want at least %v for the handset to notice", down, hangUpDTRDrop)
		}
	}
}

// refusingWrite is a scripted transport whose writes of refused fail, as a
// port unplugged at the wrong moment would.
type refusingWrite struct {
	*scripted
	refused string
}

// WriteAll fails for refused and writes anything else.
func (r refusingWrite) WriteAll(p []byte) error {
	if string(p) == r.refused {
		return errUnplugged
	}
	return r.scripted.WriteAll(p)
}

// withoutDTR is the transport it holds without its DTR line: only the
// methods of an iridium.Transport show.
type withoutDTR struct {
	iridium.Transport
}

// endlessCallPause is how long the far end of an endless call takes per
// byte it sends.
const endlessCallPause = 10 * time.Millisecond

// endlessCall is a handset deaf to the escape and to DTR, relaying a far
// end that sends without a pause until a moment comes. It notes what is
// written once DTR has dropped, and is used by one goroutine at a time.
type endlessCall struct {
	until time.Time
	// dtrDropped is set by the first DTR drop.
	dtrDropped bool
	// writtenAfterDTRDrop is what was written since.
	writtenAfterDTRDrop []byte
}

// endlessCallFor returns an endlessCall whose far end sends for duration.
func endlessCallFor(duration time.Duration) *endlessCall {
	return &endlessCall{until: time.Now().Add(duration)}
}

// WriteAll takes p, which changes nothing, noting it once DTR has dropped.
func (e *endlessCall) WriteAll(p []byte) error {
	if e.dtrDropped {
		e.writtenAfterDTRDrop = append(e.writtenAfterDTRDrop, p...)
	}
	return nil
}

// RaiseDTR changes nothing.
func (*endlessCall) RaiseDTR() error {
	return nil
}

// DropDTR changes nothing, but is noted.
func (e *endlessCall) DropDTR() error {
	e.dtrDropped = true
	return nil
}

// ReadTimeout returns one PPP flag per pause until the far end stops, then
// nothing.
func (e *endlessCall) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	if !time.Now().Before(e.until) {
		time.Sleep(min(timeout, endlessCallPause))
		return 0, nil
	}
	return relayFarEnd(p)
}

// relayFarEnd returns one PPP flag after a pause, as a far end that sends
// without a pause does.
func relayFarEnd(p []byte) (int, error) {
	time.Sleep(endlessCallPause)
	p[0] = pppFlag
	return 1, nil
}

// callLeftUp is a scripted handset relaying a call left up, whose far end
// sends without a pause until a write holds stop — the escape, or the next
// command, by which the far end has fallen silent; from then on the script
// plays.
type callLeftUp struct {
	*scripted
	stop string
	// stopped is set once stop has been written.
	stopped atomic.Bool
}

// callUntil returns a callLeftUp over transport whose far end sends until a
// write holds stop.
func callUntil(stop string, transport *scripted) *callLeftUp {
	return &callLeftUp{scripted: transport, stop: stop}
}

// WriteAll notes a write that holds stop, and writes p to the script.
func (c *callLeftUp) WriteAll(p []byte) error {
	if bytes.Contains(p, []byte(c.stop)) {
		c.stopped.Store(true)
	}
	return c.scripted.WriteAll(p)
}

// ReadTimeout relays the far end until stop was written, then reads from
// the script.
func (c *callLeftUp) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	if c.stopped.Load() {
		return c.scripted.ReadTimeout(p, timeout)
	}
	return relayFarEnd(p)
}

// errUSBHiccup is the read failure of a hiccup.
var errUSBHiccup = errors.New("USB hiccup")

// hiccup is a scripted transport that fails one read each time a trigger
// has been written, as a USB hiccup would.
type hiccup struct {
	*scripted
	triggers []string
	// armed is set when a trigger is written, and cleared by the read it
	// fails.
	armed atomic.Bool
}

// hiccupAfter returns a hiccup over transport that fails a read after
// trigger.
func hiccupAfter(trigger string, transport *scripted) *hiccup {
	return &hiccup{scripted: transport, triggers: []string{trigger}}
}

// andAfter makes h fail a read after trigger too, and returns h.
func (h *hiccup) andAfter(trigger string) *hiccup {
	h.triggers = append(h.triggers, trigger)
	return h
}

// WriteAll arms the hiccup for a write that starts with a trigger, and
// writes p to the script.
func (h *hiccup) WriteAll(p []byte) error {
	if slices.ContainsFunc(h.triggers, func(trigger string) bool { return bytes.HasPrefix(p, []byte(trigger)) }) {
		h.armed.Store(true)
	}
	return h.scripted.WriteAll(p)
}

// ReadTimeout fails once armed, and otherwise reads from the script.
func (h *hiccup) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	if h.armed.Swap(false) {
		return 0, errUSBHiccup
	}
	return h.scripted.ReadTimeout(p, timeout)
}
