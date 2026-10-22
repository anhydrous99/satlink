package satlink_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
)

// The handset is never moved into a data call, so while a call holds it,
// its own methods fail with ErrBusy, and every way out of the call releases
// it. A transport's panic out of a call that holds the handset pulses DTR
// first, which ends the call under AT&D2.

// promptly bounds what must not wait for a dial or a call: it returns at
// once, or it is blocked.
const promptly = time.Second

// confirmedHangUp are the steps of a hang-up that the handset confirms.
func confirmedHangUp() []step {
	return []step{{"+++", okReply}, {"ATH\r", okReply}}
}

// busyCalls returns the handset's methods that talk to it, each as a
// function returning its error.
func busyCalls() map[string]func(*satlink.Handset) error {
	return map[string]func(*satlink.Handset) error{
		"Initialize":     (*satlink.Handset).Initialize,
		"Command":        func(h *satlink.Handset) error { return errorOf(h.Command("AT")) },
		"CommandTimeout": func(h *satlink.Handset) error { return errorOf(h.CommandTimeout("AT", time.Second)) },
		"Info":           func(h *satlink.Handset) error { return errorOf(h.Info()) },
		"IMSI":           func(h *satlink.Handset) error { return errorOf(h.IMSI()) },
		"SignalQuality":  func(h *satlink.Handset) error { return errorOf(h.SignalQuality()) },
		"Battery":        func(h *satlink.Handset) error { return errorOf(h.Battery()) },
		"Registration":   func(h *satlink.Handset) error { return errorOf(h.Registration()) },
		"Clock":          func(h *satlink.Handset) error { return errorOf(h.Clock()) },
		"SendSMS":        func(h *satlink.Handset) error { return errorOf(h.SendSMS("+1", "hi")) },
		"ListSMS":        func(h *satlink.Handset) error { return errorOf(h.ListSMS(satlink.SMSFilterAll)) },
		"ReadSMS":        func(h *satlink.Handset) error { return errorOf(h.ReadSMS(1)) },
		"DeleteSMS":      func(h *satlink.Handset) error { return h.DeleteSMS(1) },
		"Dial":           func(h *satlink.Handset) error { return h.Dial("+1") },
		"Answer":         (*satlink.Handset).Answer,
		"HangUp":         (*satlink.Handset).HangUp,
		"PollURCs":       func(h *satlink.Handset) error { return errorOf(h.PollURCs(time.Second)) },
		"EndDataCall":    func(h *satlink.Handset) error { return h.EndDataCall(satlink.DefaultEscapeGuard) },
		"DialData":       func(h *satlink.Handset) error { return errorOf(h.DialData(testNumber, callSettings())) },
		"AnswerData":     func(h *satlink.Handset) error { return errorOf(h.AnswerData(callSettings())) },
	}
}

// errorOf returns the error of a call whose other result the test does not
// need.
func errorOf[T any](_ T, err error) error {
	return err
}

func TestAHandsetInADataCallIsBusy(t *testing.T) {
	transport := newScripted(slices.Concat([]step{dialStep(connectReply)}, confirmedHangUp())...)
	handset := newTestHandset(transport)
	call := mustDial(t, handset, callSettings())
	t.Cleanup(func() { hangUp(t, call) })
	written := transport.history()

	for method, talk := range busyCalls() {
		t.Run(method, func(t *testing.T) {
			err := talk(handset)

			assertBusy(t, err)
		})
	}

	if after := transport.history(); len(after) != len(written) {
		t.Errorf("written %q while busy", after[len(written):])
	}
}

func TestTakeURCsReturnsNothingWhileBusy(t *testing.T) {
	transport := newScripted(slices.Concat([]step{
		{"AT+CSQ\r", "\r\n+CMTI: \"SM\",3\r\n\r\n+CSQ:2\r\n\r\nOK\r\n"},
		dialStep(connectReply),
	}, confirmedHangUp())...)
	handset := newTestHandset(transport)
	if _, err := handset.SignalQuality(); err != nil {
		t.Fatalf("SignalQuality: %v", err)
	}
	call := mustDial(t, handset, callSettings())

	busyURCs := handset.TakeURCs()
	hangUp(t, call)
	laterURCs := handset.TakeURCs()

	if busyURCs != nil {
		t.Errorf("TakeURCs while busy = %q, want nothing", busyURCs)
	}
	if want := []string{`+CMTI: "SM",3`}; !slices.Equal(laterURCs, want) {
		t.Errorf("TakeURCs after the call = %q, want %q: the queue stays for later", laterURCs, want)
	}
}

func TestHangingUpReleasesTheHandset(t *testing.T) {
	transport := newScripted(slices.Concat(
		[]step{dialStep(connectReply)}, confirmedHangUp(), []step{{"AT\r", okReply}},
	)...)
	handset := newTestHandset(transport)
	call := mustDial(t, handset, callSettings())

	hangUp(t, call)

	if _, err := handset.Command("AT"); err != nil {
		t.Errorf("Command after the hang-up: %v", err)
	}
}

func TestAFailedHangUpReleasesTheHandsetToo(t *testing.T) {
	// Deaf to the escape, to ATH and to DTR.
	transport := newScripted(dialStep(connectReply), step{"AT\r", okReply})
	handset := newTestHandset(transport)
	call := mustDial(t, handset, callSettings())

	hangUpErr := call.HangUp()

	assertCallMayStillBeUp(t, hangUpErr, true)
	if _, err := handset.Command("AT"); err != nil {
		t.Errorf("Command after the failed hang-up: %v", err)
	}
}

func TestEveryFailedDialReleasesTheHandset(t *testing.T) {
	tooLongAGuard := callSettings()
	tooLongAGuard.EscapeGuard = 2 * satlink.MaxEscapeGuard
	tests := []struct {
		name     string
		script   []step
		settings satlink.DataCallSettings
	}{
		{"refused before dialling", nil, tooLongAGuard},
		{"answered BUSY", []step{dialStep("\r\nBUSY\r\n")}, callSettings()},
		{"connected too late and not ended", []step{dialStep(""), {"\r", connectReply}}, callSettings()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handset := newTestHandset(newScripted(test.script...))

			_, err := handset.DialData(testNumber, test.settings)

			if err == nil {
				t.Fatal("DialData connected, want an error")
			}
			assertNotBusy(t, handset)
		})
	}
}

func TestADialThatPanicsStillReleasesTheHandset(t *testing.T) {
	transport := panicsOnWrite{scripted: newScripted(), data: "ATD" + testNumber + "\r"}
	handset := newTestHandset(transport)

	panicked := recoverPanic(func() {
		_, err := handset.DialData(testNumber, callSettings())
		t.Errorf("DialData returned %v instead of passing the transport's panic on", err)
	})

	if panicked == nil {
		t.Fatal("DialData did not panic")
	}
	assertNotBusy(t, handset)
}

func TestAHangUpThatPanicsStillReleasesTheHandset(t *testing.T) {
	// satnet recovers such a panic in its driver and relies on the handset
	// being free afterwards.
	transport := panicsOnWrite{scripted: newScripted(dialStep(connectReply), step{"AT\r", okReply}), data: "+++"}
	handset := newTestHandset(transport)
	call := mustDial(t, handset, callSettings())

	panicked := recoverPanic(func() {
		err := call.HangUp()
		t.Errorf("HangUp returned %v instead of passing the transport's panic on", err)
	})

	if panicked == nil {
		t.Fatal("HangUp did not panic")
	}
	var commandErr error
	finishesWithin(t, promptly, "a command after the panic", func() {
		_, commandErr = handset.Command("AT")
	})
	if commandErr != nil {
		t.Errorf("Command after the panic: %v", commandErr)
	}
}

func TestADialThatPanicsPulsesDTRBeforePassingThePanicOn(t *testing.T) {
	// The call may have come up: after AT&D2 the pulse ends it.
	transport := panicsOnWrite{scripted: newScripted(), data: "ATD" + testNumber + "\r"}
	handset := newTestHandset(transport)

	panicked := recoverPanic(func() {
		_, _ = handset.DialData(testNumber, callSettings())
	})

	if panicked != transportBug {
		t.Fatalf("DialData panicked with %v, want the transport's %q", panicked, transportBug)
	}
	assertHungUpByDroppingDTR(t, transport.scripted)
}

func TestAHangUpThatPanicsPulsesDTRBeforePassingThePanicOn(t *testing.T) {
	// satnet's driver recovers such a panic and cannot end the call itself:
	// the pulse is what ends it after AT&D2.
	transport := panicsOnWrite{scripted: newScripted(dialStep(connectReply)), data: "+++"}
	handset := newTestHandset(transport)
	call := mustDial(t, handset, callSettings())

	panicked := recoverPanic(func() {
		_ = call.HangUp()
	})

	if panicked != transportBug {
		t.Fatalf("HangUp panicked with %v, want the transport's %q", panicked, transportBug)
	}
	assertHungUpByDroppingDTR(t, transport.scripted)
}

func TestADialThatPanicsPulsesDTRBeforeReleasingTheHandset(t *testing.T) {
	// Pulsed once the handset was free, DTR would be down under the app's
	// next command or call, and after AT&D2 the drop could end a new call.
	// AnswerData sets its call up the same way.
	handset, witness := newWitnessedHandset(newScripted(), "ATD"+testNumber+"\r")

	panicked := recoverPanic(func() {
		_, _ = handset.DialData(testNumber, callSettings())
	})

	if panicked != transportBug {
		t.Fatalf("DialData panicked with %v, want the transport's %q", panicked, transportBug)
	}
	assertBusyWhileDTRWasDown(t, witness)
}

func TestAHangUpThatPanicsPulsesDTRBeforeReleasingTheHandset(t *testing.T) {
	// satnet's driver recovers such a panic and lets the app go on: pulsed
	// once the handset was free, DTR would be down under its next command.
	handset, witness := newWitnessedHandset(newScripted(dialStep(connectReply)), "+++")
	call := mustDial(t, handset, callSettings())

	panicked := recoverPanic(func() {
		_ = call.HangUp()
	})

	if panicked != transportBug {
		t.Fatalf("HangUp panicked with %v, want the transport's %q", panicked, transportBug)
	}
	assertBusyWhileDTRWasDown(t, witness)
}

func TestAPulseThatPanicsTooLeavesTheFirstPanicToGoOn(t *testing.T) {
	transport := panicsOnDTRToo{panicsOnWrite{scripted: newScripted(dialStep(connectReply)), data: "+++"}}
	handset := newTestHandset(transport)
	call := mustDial(t, handset, callSettings())

	panicked := recoverPanic(func() {
		_ = call.HangUp()
	})

	if panicked != transportBug {
		t.Errorf("HangUp panicked with %v, want the first panic, %q", panicked, transportBug)
	}
	assertNotBusy(t, handset)
}

func TestAHangUpThatPanicsOverATransportWithoutDTRPassesThePanicOn(t *testing.T) {
	transport := withoutDTR{panicsOnWrite{scripted: newScripted(dialStep(connectReply)), data: "+++"}}
	handset := newTestHandset(transport)
	call := mustDial(t, handset, callSettings())

	panicked := recoverPanic(func() {
		_ = call.HangUp()
	})

	if panicked != transportBug {
		t.Errorf("HangUp panicked with %v, want the transport's %q", panicked, transportBug)
	}
	assertNotBusy(t, handset)
}

func TestASecondCallIsRefusedWhileOneIsUp(t *testing.T) {
	transport := newScripted(slices.Concat([]step{dialStep(connectReply)}, confirmedHangUp())...)
	handset := newTestHandset(transport)
	call := mustDial(t, handset, callSettings())
	t.Cleanup(func() { hangUp(t, call) })

	_, dialErr := handset.DialData(testNumber, callSettings())
	_, answerErr := handset.AnswerData(callSettings())

	assertBusy(t, dialErr)
	assertBusy(t, answerErr)
}

func TestMethodsFailAtOnceWhileADialWaits(t *testing.T) {
	transport := newBlockingTransport()
	handset := satlink.NewHandset(transport)
	dialEnded := startEndlessDial(t, handset, transport)

	var commandErr error
	var urcs []string
	finishesWithin(t, promptly, "a command and TakeURCs during the dial", func() {
		_, commandErr = handset.Command("AT")
		urcs = handset.TakeURCs()
	})

	assertBusy(t, commandErr)
	if urcs != nil {
		t.Errorf("TakeURCs during the dial = %q, want nothing", urcs)
	}
	closeAndAwaitDial(t, handset, dialEnded)
}

func TestSetTimeoutAndCloseDoNotWaitForADial(t *testing.T) {
	transport := newBlockingTransport()
	handset := satlink.NewHandset(transport)
	dialEnded := startEndlessDial(t, handset, transport)

	finishesWithin(t, promptly, "SetTimeout during the dial", func() {
		handset.SetTimeout(time.Minute)
	})
	dialErr := closeAndAwaitDial(t, handset, dialEnded)

	if !errors.Is(dialErr, satlink.ErrTransport) {
		t.Errorf("the dial ended with %v, want the closed transport's error", dialErr)
	}
	assertNotBusy(t, handset)
}

// Callers that take turns at the handset.
const (
	// concurrentCallers is how many goroutines share one handset.
	concurrentCallers = 8
	// callsPerCaller is how many queries each of them makes.
	callsPerCaller = 5
)

func TestConcurrentCallsTakeTurns(t *testing.T) {
	// Run with -race. Each answer comes a byte per read, so that callers
	// that did not take turns would read each other's.
	transport := newAnswering(map[string]string{
		"AT+CSQ\r": "\r\n+CSQ:4\r\n\r\nOK\r\n",
		"AT+CBC\r": "\r\n+CBC: 1,080\r\n\r\nOK\r\n",
	})
	handset := satlink.NewHandset(transport)
	wantBattery := satlink.Battery{Status: satlink.ChargeStatusExternalPower, Level: 80}

	var callers sync.WaitGroup
	for range concurrentCallers {
		callers.Go(func() {
			for range callsPerCaller {
				if bars, err := handset.SignalQuality(); err != nil || bars != 4 {
					t.Errorf("SignalQuality = %d, %v; want 4", bars, err)
				}
				if battery, err := handset.Battery(); err != nil || battery != wantBattery {
					t.Errorf("Battery = %+v, %v; want %+v", battery, err, wantBattery)
				}
			}
		})
	}
	callers.Wait()
}

// answering is a handset that answers each command it knows whenever it is
// written, a byte per read. It is safe for concurrent use.
type answering struct {
	mu      sync.Mutex
	answers map[string]string
	// command is what has been written of the command line under way.
	command []byte
	pending []byte
}

// newAnswering returns a handset that answers each command line of answers
// with what answers holds for it.
func newAnswering(answers map[string]string) *answering {
	return &answering{answers: answers}
}

// WriteAll queues the answer to each command line p completes.
func (a *answering) WriteAll(p []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range p {
		a.command = append(a.command, c)
		if c == '\r' {
			a.pending = append(a.pending, a.answers[string(a.command)]...)
			a.command = nil
		}
	}
	return nil
}

// ReadTimeout delivers a byte of the pending answers, if there is one.
func (a *answering) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	if !a.hasPending() {
		// Nothing will come: a short nap spares the CPU.
		time.Sleep(min(timeout, longestNap))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delivered := copy(p[:min(len(p), 1)], a.pending)
	a.pending = a.pending[delivered:]
	return delivered, nil
}

// hasPending reports whether an answer is waiting to be read.
func (a *answering) hasPending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending) > 0
}

// startEndlessDial starts a dial over handset that would wait an hour for
// CONNECT, and returns once transport has seen the dial command. The
// channel delivers the dial's error once closing the handset has ended it
// (closeAndAwaitDial); a test that fails first has it closed at cleanup.
func startEndlessDial(t *testing.T, handset *satlink.Handset, transport *blockingTransport) <-chan error {
	t.Helper()
	settings := callSettings()
	settings.ConnectTimeout = time.Hour
	dialEnded := make(chan error, 1)
	go func() {
		call, err := handset.DialData(testNumber, settings)
		if err == nil {
			err = errors.New("the dial connected: " + call.ConnectMessage())
		}
		dialEnded <- err
	}()
	// Closing a blocking transport cannot fail, and closing twice is
	// harmless: this only ends a dial a failed test left waiting.
	t.Cleanup(func() { _ = handset.Close() })
	waitUntil(t, 5*time.Second, "the dial is under way", func() bool {
		return transport.hasWritten("ATD" + testNumber + "\r")
	})
	return dialEnded
}

// closeAndAwaitDial closes handset, which must not wait for the dial, and
// returns the error the dial then ends with.
func closeAndAwaitDial(t *testing.T, handset *satlink.Handset, dialEnded <-chan error) error {
	t.Helper()
	var closeErr error
	finishesWithin(t, promptly, "Close during the dial", func() {
		closeErr = handset.Close()
	})
	if closeErr != nil {
		t.Errorf("Close: %v", closeErr)
	}
	select {
	case err := <-dialEnded:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the dial did not end when the transport closed")
		return nil
	}
}

// transportBug is what the panicking transports below panic with.
const transportBug = "a transport bug"

// panicsOnWrite is a scripted transport with a bug: writing data panics.
type panicsOnWrite struct {
	*scripted
	data string
}

// WriteAll panics for data and writes anything else.
func (p panicsOnWrite) WriteAll(written []byte) error {
	if string(written) == p.data {
		panic(transportBug)
	}
	return p.scripted.WriteAll(written)
}

// panicsOnDTRToo is a panicsOnWrite whose DTR line panics too, as a
// transport with bugs everywhere might.
type panicsOnDTRToo struct {
	panicsOnWrite
}

// DropDTR panics.
func (panicsOnDTRToo) DropDTR() error {
	panic("a DTR bug")
}

// pulseWitness is a panicsOnWrite that, the moment DTR drops, has another
// goroutine send its handset a command, as an app might while the pulse
// that a panic sets off runs: the pulse holds the handset, so the command
// must fail with ErrBusy, writing nothing.
type pulseWitness struct {
	panicsOnWrite
	// handset is the handset over the witness.
	handset *satlink.Handset
	// commandErr is the error of the command sent while DTR was down.
	commandErr error
	// writtenWhileDown is what was written while DTR was down.
	writtenWhileDown []byte
}

// newWitnessedHandset returns a handset over a pulseWitness that plays
// script but panics when data is written, and the witness.
func newWitnessedHandset(script *scripted, data string) (*satlink.Handset, *pulseWitness) {
	witness := &pulseWitness{panicsOnWrite: panicsOnWrite{scripted: script, data: data}}
	witness.handset = newTestHandset(witness)
	return witness.handset, witness
}

// DropDTR drops DTR, then sends a command from another goroutine and notes
// how that went. Run while a panic unwinds, it cannot fail the test itself,
// nor wait for good: a command that waits for the pulse to end is noted
// once promptly has passed.
func (w *pulseWitness) DropDTR() error {
	if err := w.scripted.DropDTR(); err != nil {
		return err
	}
	writtenBefore := len(w.history())
	commandEnded := make(chan error, 1)
	go func() {
		_, err := w.handset.Command("AT")
		commandEnded <- err
	}()
	select {
	case w.commandErr = <-commandEnded:
	case <-time.After(promptly):
		w.commandErr = errors.New("the command waited for the pulse to end")
	}
	w.writtenWhileDown = w.history()[writtenBefore:]
	return nil
}

// recoverPanic runs action and returns what it panicked with, nil if it did
// not panic.
func recoverPanic(action func()) (panicked any) {
	defer func() { panicked = recover() }()
	action()
	return nil
}

// hangUp hangs call up, failing the test if the hang-up fails.
func hangUp(t *testing.T, call *satlink.DataCall) {
	t.Helper()
	if err := call.HangUp(); err != nil {
		t.Errorf("HangUp: %v", err)
	}
}

// assertBusy fails the test unless err is ErrBusy's.
func assertBusy(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, satlink.ErrBusy) || err.Error() != "the handset is in a data call" {
		t.Errorf("error = %v, want ErrBusy", err)
	}
}

// assertBusyWhileDTRWasDown fails the test unless DTR was pulsed, and the
// command that witness sent while DTR was down failed with ErrBusy,
// writing nothing.
func assertBusyWhileDTRWasDown(t *testing.T, witness *pulseWitness) {
	t.Helper()
	assertDTRDroppedAndRaised(t, witness.scripted)
	assertBusy(t, witness.commandErr)
	if len(witness.writtenWhileDown) != 0 {
		t.Errorf("written %q while DTR was down, want nothing: the pulse holds the handset", witness.writtenWhileDown)
	}
}

// assertNotBusy fails the test if handset is still busy, or still locked.
func assertNotBusy(t *testing.T, handset *satlink.Handset) {
	t.Helper()
	var err error
	finishesWithin(t, promptly, "a look at the handset's URCs", func() {
		_, err = handset.PollURCs(0)
	})
	if errors.Is(err, satlink.ErrBusy) {
		t.Error("the handset is still busy")
	}
}
