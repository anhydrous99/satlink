package mobile_test

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/mobile"
	"github.com/anhydrous99/satlink/ppp"
	"github.com/anhydrous99/satlink/sim"
)

// The link API, driven from Go the way Kotlin or Swift would drive it:
// through a ByteTransport (here, over the simulator).

// hangUpTimeoutMillis bounds a hang-up the handset will not confirm: the
// escape, its retry, the DTR drop and the raw ATH each wait in turn.
const hangUpTimeoutMillis = 120_000

// echoMessage is what the TCP echo test sends over the satellite.
const echoMessage = "over the satellite"

func TestAForeignTransportCarriesAnAppPrivateLink(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	listener := &mobile.StateRecorder{}

	link := establish(t, transport, settingsAllowing("echo.test:7"), listener)
	status := link.Status()
	if mobile.DescribeState(status.State) != "Up" || status.LocalAddress != "10.64.0.2" {
		t.Errorf("status is %s at %q, want Up at 10.64.0.2", mobile.DescribeState(status.State), status.LocalAddress)
	}

	connection := openEcho(t, link)
	if err := connection.Send([]byte(echoMessage), timeoutMillis); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if echoed := receiveExactly(t, connection, len(echoMessage)); echoed != echoMessage {
		t.Errorf("echoed %q, want %q", echoed, echoMessage)
	}
	_, err := link.OpenTCP("echo.test", 80, timeoutMillis)
	assertKind(t, err, mobile.SatlinkErrorPolicyDenied)

	state, err := link.HangUp(timeoutMillis)
	if err != nil || mobile.DescribeState(state) != "Closed(Requested)" {
		t.Fatalf("HangUp = %s, %v, want Closed(Requested)", mobile.DescribeState(state), err)
	}
	mobile.AssertHeardEveryChange(t, listener)
}

// receiveExactly returns the next length bytes connection receives.
func receiveExactly(t *testing.T, connection *mobile.TCPConnection, length int) string {
	t.Helper()
	var received []byte
	for len(received) < length {
		chunk, err := connection.Receive(64, timeoutMillis)
		if err != nil || chunk.Finished {
			t.Fatalf("Receive after %q: %+v, %v", received, chunk, err)
		}
		received = append(received, chunk.Data...)
	}
	return string(received)
}

func TestSettingsAreValidatedBeforeDialling(t *testing.T) {
	t.Parallel()
	// A zero limit means no limit in Go: the invalid one is negative.
	negativeIdleTimeout := mobile.NewLinkSettings()
	negativeIdleTimeout.IdleTimeoutSeconds = -1
	negativeByteLimit := mobile.NewLinkSettings()
	negativeByteLimit.MaxBytes = -1
	notIPv4 := mobile.NewLinkSettings()
	notIPv4.AddHost("relay.test", "2001:db8::1")
	// Limits too small to be of use, each just below what LinkSettings says.
	tooShortACall := withMaxCallSeconds(23)
	tooShortAnIdleTime := mobile.NewLinkSettings()
	tooShortAnIdleTime.IdleTimeoutSeconds = 24
	tooSmallADataLimit := mobile.NewLinkSettings()
	tooSmallADataLimit.MaxBytes = 2047
	cases := map[string]*mobile.LinkSettings{
		"a rule that does not parse":                          settingsAllowing("not a rule"),
		"a negative idle timeout":                             negativeIdleTimeout,
		"a negative data limit":                               negativeByteLimit,
		"a host address that is not IPv4":                     notIPv4,
		"an absurd call limit":                                withMaxCallSeconds(1 << 62),
		"a call limit too short to bring PPP up and hang up":  tooShortACall,
		"an idle limit too short for a lookup to be answered": tooShortAnIdleTime,
		"a data limit below what PPP alone takes":             tooSmallADataLimit,
	}
	for name, settings := range cases {
		t.Run(name, func(t *testing.T) {
			transport, control := simulatedUSB(t)

			_, err := mobile.EstablishLink(transport, settings, nil, nil)

			assertKind(t, err, mobile.SatlinkErrorInvalidArgument)
			if placed := control.Stats().CallsPlaced; placed != 0 {
				t.Errorf("%d calls placed, want none", placed)
			}
		})
	}
}

// withMaxCallSeconds returns the default settings with a call limit of
// seconds.
func withMaxCallSeconds(seconds int64) *mobile.LinkSettings {
	settings := mobile.NewLinkSettings()
	settings.MaxCallSeconds = seconds
	return settings
}

func TestALinkHasTheHandsetToItselfUntilItHangsUp(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	handset := newHandset(t, transport)

	link := lend(t, handset, mobile.NewLinkSettings())

	assertKind(t, signalQuality(handset)(), mobile.SatlinkErrorHandsetBusy)
	_, err := mobile.EstablishLinkWithHandset(handset, mobile.NewLinkSettings(), nil, nil)
	assertKind(t, err, mobile.SatlinkErrorHandsetBusy)
	state, err := link.HangUp(timeoutMillis)
	if err != nil || mobile.DescribeState(state) != "Closed(Requested)" {
		t.Fatalf("HangUp = %s, %v, want Closed(Requested)", mobile.DescribeState(state), err)
	}
	if err := signalQuality(handset)(); err != nil {
		t.Errorf("the handset is not back: %v", err)
	}
}

func TestADroppedLinkGivesTheHandsetBackOnceItsCallEnds(t *testing.T) {
	t.Parallel()
	transport, control := simulatedUSB(t)
	handset := newHandset(t, transport)
	link := lend(t, handset, mobile.NewLinkSettings())

	// Go drops nothing: Close is the drop, ending the call without waiting.
	if err := link.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	simtest.Eventually(t, patience, "the handset is back", func() bool {
		return kindOf(signalQuality(handset)()) != mobile.SatlinkErrorHandsetBusy
	})
	if err := signalQuality(handset)(); err != nil {
		t.Errorf("the handset is not back: %v", err)
	}
	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
}

func TestALinkThatFailsToComeUpGivesTheHandsetBack(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	handset := newHandset(t, transport)
	settings := mobile.NewLinkSettings()
	settings.Number = "not a number"

	_, err := mobile.EstablishLinkWithHandset(handset, settings, nil, nil)

	if err == nil {
		t.Fatal("a link to an undialable number came up")
	}
	if err := signalQuality(handset)(); err != nil {
		t.Errorf("the handset is not back: %v", err)
	}
}

// The setups that TestSetupsTakingTurnsWithAHandsetLeaveItUsable runs.
const (
	// competingSetups is how many threads set links up with one handset at
	// once.
	competingSetups = 2
	// setupAttempts is how many setups each thread tries. A setup that took
	// another's handset for its own did so within a few hundred.
	setupAttempts = 2000
)

// TestSetupsTakingTurnsWithAHandsetLeaveItUsable holds a setup that failed
// to handing the handset back once: the handset may go to another thread's
// setup the moment it is back, and that setup is not the failed one's to
// end. Each setup fails before it dials, as the number cannot be dialled,
// so no call is ever up and nothing may strand the handset.
func TestSetupsTakingTurnsWithAHandsetLeaveItUsable(t *testing.T) {
	t.Parallel()
	transport, control := simulatedUSB(t)
	handset := newHandset(t, transport)
	settings := mobile.NewLinkSettings()
	settings.Number = "not a number"

	var setups sync.WaitGroup
	for range competingSetups {
		setups.Go(func() {
			for range setupAttempts {
				_, err := mobile.EstablishLinkWithHandset(handset, settings, nil, nil)
				if kind := kindOf(err); kind != mobile.SatlinkErrorInvalidArgument && kind != mobile.SatlinkErrorHandsetBusy {
					t.Errorf("a setup fails with %v, want the number's SatlinkErrorInvalidArgument or SatlinkErrorHandsetBusy", err)
					return
				}
			}
		})
	}
	setups.Wait()

	if err := signalQuality(handset)(); err != nil {
		t.Errorf("the handset is not usable after the setups: %v", err)
	}
	if placed := control.Stats().CallsPlaced; placed != 0 {
		t.Errorf("%d calls placed, want none", placed)
	}
}

// gatewayWantingCredentials returns a simulator whose gateway demands PAP
// credentials that the default link settings do not have, so PPP fails.
func gatewayWantingCredentials() sim.Config {
	config := sim.DefaultConfig()
	config.Authentication = &sim.Authentication{
		Protocol:    ppp.AuthProtocolPAP,
		Credentials: sim.Credentials{Username: "app", Password: "secret"},
	}
	return config
}

// assertStranded fails the test unless handset is stranded: every method
// but Reinitialize and Close fails with SatlinkErrorHangUpFailed, and it
// has no events.
func assertStranded(t *testing.T, handset *mobile.IridiumHandset) {
	t.Helper()
	const reason = "the last data call could not be hung up; reinitialize ends it"
	_, pollErr := handset.PollEvents(0)
	_, lendErr := mobile.EstablishLinkWithHandset(handset, mobile.NewLinkSettings(), nil, nil)
	for name, err := range map[string]error{"SignalQuality": signalQuality(handset)(), "PollEvents": pollErr, "EstablishLinkWithHandset": lendErr} {
		if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorHangUpFailed || details.Reason != reason {
			t.Errorf("%s fails with %v, want SatlinkErrorHangUpFailed: %s", name, err, reason)
		}
	}
	if events := handset.TakeEvents(); events.Len() != 0 {
		t.Errorf("a stranded handset has %d events, want none", events.Len())
	}
}

func TestALinkThatFailsAndCannotHangUpStrandsTheHandsetUntilItRecovers(t *testing.T) {
	t.Parallel()
	transport, faulty, control := stubbornUSB(t, gatewayWantingCredentials())
	handset := newHandset(t, transport)

	_, err := mobile.EstablishLinkWithHandset(handset, mobile.NewLinkSettings(), nil, nil)

	assertKind(t, err, mobile.SatlinkErrorHangUpFailed)
	if !control.Stats().InDataCall {
		t.Fatal("the call is not still up")
	}
	assertStranded(t, handset)
	faulty.DeafToDTR.Store(false)
	if err := handset.Reinitialize(); err != nil {
		t.Fatalf("Reinitialize did not end the call: %v", err)
	}
	if control.Stats().InDataCall {
		t.Error("the call is still up after Reinitialize")
	}
	if err := signalQuality(handset)(); err != nil {
		t.Errorf("the handset is not usable again: %v", err)
	}
}

func TestARecoveryThatCannotEndTheCallFailsWithHangUpFailedAgain(t *testing.T) {
	t.Parallel()
	transport, _, control := stubbornUSB(t, sim.DefaultConfig())
	handset := newHandset(t, transport)
	link := lend(t, handset, mobile.NewLinkSettings())
	_, err := link.HangUp(hangUpTimeoutMillis)
	assertKind(t, err, mobile.SatlinkErrorHangUpFailed)

	recovered := handset.Reinitialize()

	assertKind(t, recovered, mobile.SatlinkErrorHangUpFailed)
	if !control.Stats().InDataCall {
		t.Error("the call is not still up")
	}
	assertKind(t, signalQuality(handset)(), mobile.SatlinkErrorHangUpFailed)
}

func TestACallLeftUpThatCannotBeEndedIsReportedWhenTheHandsetIsOpened(t *testing.T) {
	t.Parallel()
	transport, faulty, control := stubbornUSB(t, sim.DefaultConfig())
	leaveADataCallUp(t, faulty)

	_, err := mobile.NewIridiumHandset(transport)

	assertKind(t, err, mobile.SatlinkErrorHangUpFailed)
	if !control.Stats().InDataCall {
		t.Error("the call is not still up")
	}
}

// setup is the outcome of establishing a link in the background.
type setup struct {
	link *mobile.SatelliteLink
	err  error
}

// establishInBackground starts establishing a link over transport, with
// settings and cancellation, in another goroutine, and returns the channel
// its outcome arrives on.
func establishInBackground(transport mobile.ByteTransport, settings *mobile.LinkSettings, cancellation *mobile.Cancellation) <-chan setup {
	outcome := make(chan setup, 1)
	go func() {
		link, err := mobile.EstablishLink(transport, settings, nil, cancellation)
		outcome <- setup{link: link, err: err}
	}()
	return outcome
}

// awaitSetup returns the error of a setup started by establishInBackground,
// failing the test if it does not end within patience. A link that came up
// all the same is hung up when the test ends.
func awaitSetup(t *testing.T, outcome <-chan setup) error {
	t.Helper()
	select {
	case result := <-outcome:
		if result.link != nil {
			mobile.HangUpAtCleanup(t, result.link)
		}
		return result.err
	case <-time.After(patience):
		t.Fatal("the setup never ended")
		return nil
	}
}

// TestALinkBeingSetUpCanBeCancelled cancels the setup once the dial is under
// way, rather than a second after it starts. It runs alone, as it times the setup.
func TestALinkBeingSetUpCanBeCancelled(t *testing.T) {
	config := sim.DefaultConfig()
	config.Link.CallSetup = mobile.SlowCallSetup
	simulator := simtest.NewSimulator(t, config)
	control := simulator.Control()
	cancellation := mobile.NewCancellation()
	started := time.Now()
	established := establishInBackground(simtest.NewByteTransport(simulator), mobile.NewLinkSettings(), cancellation)
	simtest.Eventually(t, patience, "the dial is under way", func() bool {
		return control.Stats().CallsPlaced == 1
	})

	cancellation.Cancel()

	assertKind(t, awaitSetup(t, established), mobile.SatlinkErrorCancelled)
	if elapsed := time.Since(started); elapsed >= mobile.SlowCallSetup {
		t.Errorf("the cancelled setup took %v, as long as the call setup", elapsed)
	}
	if stats := control.Stats(); stats.CallsPlaced != 1 || stats.CallsConnected != 0 {
		t.Errorf("%d calls placed and %d connected, want 1 and 0", stats.CallsPlaced, stats.CallsConnected)
	}
}

// TestALinkThatLostTheHandsetWhileHangingUpSaysTheCallMayStillBeUp checks
// that a handset is never lost with its link: it comes back stranded, and
// Reinitialize reaches it as it is.
func TestALinkThatLostTheHandsetWhileHangingUpSaysTheCallMayStillBeUp(t *testing.T) {
	t.Parallel()
	transport, faulty, control := stubbornUSB(t, sim.DefaultConfig())
	handset := newHandset(t, transport)
	link := lend(t, handset, mobile.NewLinkSettings())
	faulty.PanicsOnNextEscape.Store(true)

	_, err := link.HangUp(hangUpTimeoutMillis)
	assertKind(t, err, mobile.SatlinkErrorHangUpFailed)

	assertKind(t, untilNotBusy(t, handset.Reinitialize), mobile.SatlinkErrorHangUpFailed)
	if !control.Stats().InDataCall {
		t.Fatal("the call is not still up")
	}
	faulty.DeafToDTR.Store(false)
	if err := handset.Reinitialize(); err != nil {
		t.Fatalf("Reinitialize did not end the call: %v", err)
	}
	if control.Stats().InDataCall {
		t.Error("the call is still up after Reinitialize")
	}
}

func TestADroppedLinkThatCannotHangUpStrandsTheHandset(t *testing.T) {
	t.Parallel()
	transport, _, control := stubbornUSB(t, sim.DefaultConfig())
	handset := newHandset(t, transport)
	link := lend(t, handset, mobile.NewLinkSettings())

	if err := link.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	assertKind(t, untilNotBusy(t, signalQuality(handset)), mobile.SatlinkErrorHangUpFailed)
	if !control.Stats().InDataCall {
		t.Error("the call is not still up")
	}
	assertStranded(t, handset)
}

// callHeldInOnlineCommandMode returns a handset over usb whose link's
// hang-up got OK to the escape, then lost its ATH: the call is held in
// online-command mode.
func callHeldInOnlineCommandMode(t *testing.T, usb *noDTRUSB) *mobile.IridiumHandset {
	t.Helper()
	handset := newHandset(t, usb)
	link := lend(t, handset, mobile.NewLinkSettings())
	usb.faulty.FailsNextHangUp.Store(true)

	_, err := link.HangUp(timeoutMillis)

	assertKind(t, err, mobile.SatlinkErrorHangUpFailed)
	if !usb.control.Stats().InDataCall {
		t.Fatal("the call is not still up")
	}
	return handset
}

// TestReinitializeEndsACallHeldInOnlineCommandMode checks that Reinitialize
// ends a call held in online command mode, with and without +CPAS: the
// cases differ only in the handset's firmware.
func TestReinitializeEndsACallHeldInOnlineCommandMode(t *testing.T) {
	t.Parallel()
	firmwareWithoutCPAS := map[string]bool{
		"with +CPAS": false,
		// Older firmware: +CPAS is not a command it knows.
		"even without +CPAS": true,
	}
	for name, withoutCPAS := range firmwareWithoutCPAS {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			usb := newNoDTRUSB(t)
			usb.faulty.WithoutCPAS.Store(withoutCPAS)
			handset := callHeldInOnlineCommandMode(t, usb)

			if err := handset.Reinitialize(); err != nil {
				t.Fatalf("Reinitialize did not end the call: %v", err)
			}

			if usb.control.Stats().InDataCall {
				t.Error("the call is still up after Reinitialize")
			}
		})
	}
}

func TestADataCallAnsweredByMistakeThatCannotBeEndedStrandsTheHandset(t *testing.T) {
	t.Parallel()
	handset := newHandset(t, &dataCallThatWillNotEnd{})

	err := handset.Answer()

	assertKind(t, err, mobile.SatlinkErrorHangUpFailed)
	assertKind(t, signalQuality(handset)(), mobile.SatlinkErrorHangUpFailed)
}

func TestADataCallAnswerFindsUpThatCannotBeEndedStrandsTheHandset(t *testing.T) {
	// Slow — two seconds of data, then an escape that goes unanswered — but
	// timing-insensitive.
	t.Parallel()
	transport := &dataCallThatWillNotEnd{}
	handset := newHandset(t, transport)
	transport.relayCall()

	err := handset.Answer()

	assertKind(t, err, mobile.SatlinkErrorHangUpFailed)
	assertKind(t, signalQuality(handset)(), mobile.SatlinkErrorHangUpFailed)
}

// openEcho opens a TCP connection to the simulator's echo service, which
// link must allow, and closes it at the test's end, before the link hangs
// up.
func openEcho(t *testing.T, link *mobile.SatelliteLink) *mobile.TCPConnection {
	t.Helper()
	connection, err := link.OpenTCP("echo.test", 7, timeoutMillis)
	if err != nil {
		t.Fatalf("OpenTCP: %v", err)
	}
	closeAtCleanup(t, connection.Close)
	return connection
}

func TestSendSomeSaysHowMuchItQueued(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, settingsAllowing("echo.test:7"), nil)
	connection := openEcho(t, link)

	queued, err := connection.SendSome([]byte(echoMessage), timeoutMillis)

	if err != nil || queued != int64(len(echoMessage)) {
		t.Fatalf("SendSome = %d, %v, want all %d bytes queued", queued, err, len(echoMessage))
	}
	if echoed := receiveExactly(t, connection, len(echoMessage)); echoed != echoMessage {
		t.Errorf("echoed %q, want %q", echoed, echoMessage)
	}
}

func TestAbortResetsTheConnection(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, settingsAllowing("echo.test:7"), nil)
	connection := openEcho(t, link)

	if err := connection.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	_, err := connection.Receive(64, timeoutMillis)
	assertKind(t, err, mobile.SatlinkErrorConnectionReset)
}

func TestAPanicWhileSettingUpALinkStrandsTheHandsetUntilItRecovers(t *testing.T) {
	t.Parallel()
	faulty := simtest.NewFaultyTransport(simtest.NewSimulator(t, sim.DefaultConfig()))
	handset := newHandset(t, simtest.NewByteTransport(faulty))
	faulty.PanicsOnNextRead.Store(true)

	_, err := mobile.EstablishLinkWithHandset(handset, mobile.NewLinkSettings(), nil, nil)

	if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorInternal || details.Reason != "a transport bug" {
		t.Fatalf("EstablishLinkWithHandset fails with %v, want SatlinkErrorInternal: a transport bug", err)
	}
	assertStranded(t, handset)
	if err := handset.Reinitialize(); err != nil {
		t.Fatalf("Reinitialize: %v", err)
	}
	if err := signalQuality(handset)(); err != nil {
		t.Errorf("the handset is not usable again: %v", err)
	}
}

func TestAHandsetThatDoesNotAnswerFailsTheLinkBeforeDialling(t *testing.T) {
	t.Parallel()
	simulator := simtest.NewSimulator(t, sim.DefaultConfig())
	faulty := simtest.NewFaultyTransport(simulator)
	faulty.Unplugged.Store(true)

	_, err := mobile.EstablishLink(simtest.NewByteTransport(faulty), mobile.NewLinkSettings(), nil, nil)

	if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorTransport || details.Reason != "unplugged" {
		t.Errorf("EstablishLink fails with %v, want SatlinkErrorTransport: unplugged", err)
	}
	if placed := simulator.Control().Stats().CallsPlaced; placed != 0 {
		t.Errorf("%d calls placed, want none", placed)
	}
}

func TestRefusedSettingsLeaveALentHandsetUsable(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	handset := newHandset(t, transport)

	_, err := mobile.EstablishLinkWithHandset(handset, settingsAllowing("not a rule"), nil, nil)

	assertKind(t, err, mobile.SatlinkErrorInvalidArgument)
	if err := signalQuality(handset)(); err != nil {
		t.Errorf("the handset is not usable: %v", err)
	}
}

func TestALinkThatCannotHangUpSaysSo(t *testing.T) {
	t.Parallel()
	transport, _, control := stubbornUSB(t, sim.DefaultConfig())
	link := establish(t, transport, mobile.NewLinkSettings(), nil)

	_, err := link.HangUp(hangUpTimeoutMillis)

	if details := mobile.AsSatlinkError(err); details == nil || details.Kind != mobile.SatlinkErrorHangUpFailed || details.Reason != "closed by the app" {
		t.Fatalf("HangUp fails with %v, want SatlinkErrorHangUpFailed: closed by the app", err)
	}
	if status := link.Status(); !status.CallMayStillBeUp || mobile.DescribeState(status.State) != "Closed(Requested)" {
		t.Errorf("status is %s, CallMayStillBeUp %t, want Closed(Requested) with the call perhaps still up",
			mobile.DescribeState(status.State), status.CallMayStillBeUp)
	}
	if !control.Stats().InDataCall {
		t.Error("the call is not still up")
	}
}

func TestALinkOfItsOwnThatFailsAndCannotHangUpSaysWhyItDidNotComeUp(t *testing.T) {
	t.Parallel()
	transport, _, _ := stubbornUSB(t, gatewayWantingCredentials())

	_, err := mobile.EstablishLink(transport, mobile.NewLinkSettings(), nil, nil)

	details := mobile.AsSatlinkError(err)
	if details == nil || details.Kind != mobile.SatlinkErrorHangUpFailed || !strings.HasPrefix(details.Reason, "the satellite link is down: ") {
		t.Errorf("EstablishLink fails with %v, want SatlinkErrorHangUpFailed saying why the link is down", err)
	}
}

func TestHangingUpWithoutWaitingReturnsTheStateMeanwhile(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, mobile.NewLinkSettings(), nil)

	meanwhile, err := link.HangUp(0)
	if err != nil {
		t.Fatalf("HangUp(0): %v", err)
	}
	finally, err := link.HangUp(timeoutMillis)

	if name := mobile.DescribeState(meanwhile); name != "Up" && name != "Closing(Requested)" {
		t.Errorf("HangUp(0) = %s, want Up or Closing(Requested)", name)
	}
	if err != nil || mobile.DescribeState(finally) != "Closed(Requested)" {
		t.Errorf("HangUp = %s, %v, want Closed(Requested)", mobile.DescribeState(finally), err)
	}
}

func TestAClosedLinksStatusReportsItsAddressesAndUsage(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, mobile.NewLinkSettings(), nil)
	if _, err := link.HangUp(timeoutMillis); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	status := link.Status()

	if mobile.DescribeState(status.State) != "Closed(Requested)" || status.CallMayStillBeUp {
		t.Errorf("status is %s, CallMayStillBeUp %t, want Closed(Requested), hung up", mobile.DescribeState(status.State), status.CallMayStillBeUp)
	}
	if status.LocalAddress != "10.64.0.2" || status.DNSServers.Len() != 1 || status.DNSServers.Get(0) != "10.64.0.1" {
		t.Errorf("addresses are %q with DNS %d servers, want 10.64.0.2 with 10.64.0.1", status.LocalAddress, status.DNSServers.Len())
	}
	if status.BytesSent <= 0 || status.BytesReceived <= 0 || status.ConnectedSeconds < 0 {
		t.Errorf("usage is %d s, %d bytes sent, %d received", status.ConnectedSeconds, status.BytesSent, status.BytesReceived)
	}
}

// panickingListener panics on the first state change and records the
// others.
type panickingListener struct {
	mobile.StateRecorder
	once sync.Once
}

func (l *panickingListener) OnStateChanged(state *mobile.LinkState) error {
	l.once.Do(func() { panic("a listener bug") })
	return l.StateRecorder.OnStateChanged(state)
}

func TestAListenerThatPanicsStillHearsTheLaterChanges(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	listener := &panickingListener{}
	link := establish(t, transport, mobile.NewLinkSettings(), listener)

	if _, err := link.HangUp(timeoutMillis); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	simtest.Eventually(t, mobile.ListenerPatience, "the listener hears the link close", func() bool {
		return slices.Contains(listener.Heard(), "Closed(Requested)")
	})
	if heard, want := listener.Heard(), []string{"Closing(Requested)", "Closed(Requested)"}; !slices.Equal(heard, want) {
		t.Errorf("the listener heard %q after its panic, want %q", heard, want)
	}
}

func TestLocalHostsAndNamesResolve(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	settings := settingsAllowing("echo.test:7", "relay.test:7")
	settings.AddHost("relay.test", "192.0.2.7")
	link := establish(t, transport, settings, nil)
	cases := map[string]string{"echo.test": "192.0.2.7", "relay.test": "192.0.2.7", "203.0.113.9": "203.0.113.9"}
	for host, want := range cases {
		addresses, err := link.Resolve(host, timeoutMillis)

		if err != nil {
			t.Errorf("Resolve(%q): %v, want %s", host, err, want)
			continue
		}
		if addresses.Len() != 1 || addresses.Get(0) != want {
			t.Errorf("Resolve(%q) has %d addresses, the first %q, want %s", host, addresses.Len(), addresses.Get(0), want)
		}
	}
	_, err := link.Resolve("web.test", timeoutMillis)
	assertKind(t, err, mobile.SatlinkErrorPolicyDenied)
}

func TestPortsOutsideTheirRangeAreRefusedAndPortZeroIsDenied(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	link := establish(t, transport, settingsAllowing("echo.test:*"), nil)
	connector, err := mobile.NewTLSConnector(mobile.NewTLSSettings())
	if err != nil {
		t.Fatalf("NewTLSConnector: %v", err)
	}
	for _, port := range []int{-1, 65536} {
		_, tcpErr := link.OpenTCP("echo.test", port, timeoutMillis)
		_, tlsErr := link.OpenTLS("echo.test", port, connector, timeoutMillis)
		_, udpErr := link.OpenUDP("echo.test", port, timeoutMillis)
		for _, err := range []error{tcpErr, tlsErr, udpErr} {
			if details := mobile.AsSatlinkError(err); details == nil || details.Reason != "port must be between 0 and 65535" {
				t.Errorf("port %d: error %v, want \"port must be between 0 and 65535\"", port, err)
			}
		}
	}

	_, tcpErr := link.OpenTCP("echo.test", 0, timeoutMillis)
	_, udpErr := link.OpenUDP("echo.test", 0, timeoutMillis)

	for _, err := range []error{tcpErr, udpErr} {
		if details := mobile.AsSatlinkError(err); details == nil || details.Error() != "echo.test:0 is not allowed by the link settings" {
			t.Errorf("port 0: error %v, want it not allowed by the link settings", err)
		}
	}
}

func TestASetupCancelledBeforeItDialsGivesTheHandsetBack(t *testing.T) {
	t.Parallel()
	transport, control := simulatedUSB(t)
	handset := newHandset(t, transport)
	cancellation := mobile.NewCancellation()
	cancellation.Cancel()

	_, err := mobile.EstablishLinkWithHandset(handset, mobile.NewLinkSettings(), nil, cancellation)

	assertKind(t, err, mobile.SatlinkErrorCancelled)
	if placed := control.Stats().CallsPlaced; placed != 0 {
		t.Errorf("%d calls placed, want none", placed)
	}
	if err := signalQuality(handset)(); err != nil {
		t.Errorf("the handset is not back: %v", err)
	}
}

func TestCancellingALinkThatIsUpChangesNothing(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	cancellation := mobile.NewCancellation()
	link, err := mobile.EstablishLink(transport, settingsAllowing("echo.test:7"), nil, cancellation)
	if err != nil {
		t.Fatalf("EstablishLink: %v", err)
	}
	mobile.HangUpAtCleanup(t, link)

	cancellation.Cancel()

	connection := openEcho(t, link)
	if err := connection.Send([]byte(echoMessage), timeoutMillis); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if echoed := receiveExactly(t, connection, len(echoMessage)); echoed != echoMessage {
		t.Errorf("echoed %q, want %q", echoed, echoMessage)
	}
	if state := mobile.DescribeState(link.Status().State); state != "Up" {
		t.Errorf("the link is %s after the cancellation, want Up", state)
	}
}

func TestReinitializeStrandsAReadyHandsetWhoseCallCannotBeEnded(t *testing.T) {
	t.Parallel()
	transport, faulty, control := stubbornUSB(t, sim.DefaultConfig())
	handset := newHandset(t, transport)
	leaveADataCallUp(t, faulty)

	err := handset.Reinitialize()

	assertKind(t, err, mobile.SatlinkErrorHangUpFailed)
	if !control.Stats().InDataCall {
		t.Fatal("the call is not still up")
	}
	assertStranded(t, handset)
}

// The tests of a dropped link check that the handset is given back once its
// call ends, or stranded when the call cannot be hung up. They call Close,
// which is how a Go app drops a link. A link that the app's language merely
// lets go of is
// collected instead, and only its runtime cleanup ends the call: these
// tests hold the bindings to keeping nothing that keeps the link reachable,
// neither the listener's goroutine nor the IridiumHandset that lent its
// handset. (serial_test.go holds a serial link's port closer to the same.)

// establishAndDrop brings a link up over transport, with listener, and
// lets go of it. It is not inlined, so that nothing of it stays on the
// caller's stack.
//
//go:noinline
func establishAndDrop(t *testing.T, transport mobile.ByteTransport, listener mobile.LinkListener) {
	t.Helper()
	if _, err := mobile.EstablishLink(transport, mobile.NewLinkSettings(), listener, nil); err != nil {
		t.Fatalf("EstablishLink: %v", err)
	}
}

// lendAndDrop brings a link up with handset and lets go of it, as
// establishAndDrop does.
//
//go:noinline
func lendAndDrop(t *testing.T, handset *mobile.IridiumHandset) {
	t.Helper()
	if _, err := mobile.EstablishLinkWithHandset(handset, mobile.NewLinkSettings(), nil, nil); err != nil {
		t.Fatalf("EstablishLinkWithHandset: %v", err)
	}
}

func TestACollectedLinkStillHangsUpAndItsListenerHearsItClose(t *testing.T) {
	t.Parallel()
	transport, control := simulatedUSB(t)
	listener := &mobile.StateRecorder{}

	establishAndDrop(t, transport, listener)

	mobile.CollectGarbageUntil(t, "the dropped link has closed", func() bool {
		return slices.Contains(listener.Heard(), "Closed(Requested)")
	})
	mobile.AssertHeardEveryChange(t, listener)
	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
}

func TestACollectedLentLinkGivesTheHandsetBackOnceItsCallEnds(t *testing.T) {
	t.Parallel()
	transport, control := simulatedUSB(t)
	handset := newHandset(t, transport)

	lendAndDrop(t, handset)

	mobile.CollectGarbageUntil(t, "the handset is back", func() bool {
		return kindOf(signalQuality(handset)()) != mobile.SatlinkErrorHandsetBusy
	})
	if err := signalQuality(handset)(); err != nil {
		t.Errorf("the handset is not back: %v", err)
	}
	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
}

func TestALentLinksListenerHearsEveryChange(t *testing.T) {
	t.Parallel()
	transport, _ := simulatedUSB(t)
	handset := newHandset(t, transport)
	listener := &mobile.StateRecorder{}
	link, err := mobile.EstablishLinkWithHandset(handset, mobile.NewLinkSettings(), listener, nil)
	if err != nil {
		t.Fatalf("EstablishLinkWithHandset: %v", err)
	}
	mobile.HangUpAtCleanup(t, link)

	if _, err := link.HangUp(timeoutMillis); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	mobile.AssertHeardEveryChange(t, listener)
}
