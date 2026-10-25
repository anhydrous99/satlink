package satnet_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/ppp"
	"github.com/anhydrous99/satlink/satnet"
	"github.com/anhydrous99/satlink/sim"
)

// End-to-end tests: the whole stack — AT commands, the data call, PPP,
// TCP/IP, the policy and the limits — against the simulated handset.

// state returns the link state of kind for reason.
func state(kind satnet.LinkStateKind, reason satnet.CloseReasonKind) satnet.LinkState {
	return satnet.LinkState{Kind: kind, Reason: satnet.CloseReason{Kind: reason}}
}

// expectStateChange checks the watcher's next state change.
func expectStateChange(t *testing.T, watcher *satnet.Watcher, want satnet.LinkState) {
	t.Helper()
	if got, ok := watcher.NextStateChange(timeout); !ok || got != want {
		t.Errorf("NextStateChange() = %v, %t; want %v", got, ok, want)
	}
}

// expectClosed checks that the link closes, for reason.
func expectClosed(t *testing.T, watcher *satnet.Watcher, reason satnet.CloseReasonKind) {
	t.Helper()
	if got, ok := watcher.WaitClosed(timeout); !ok || got.Kind != reason {
		t.Errorf("WaitClosed() = %v, %t; want %v", got, ok, reason)
	}
}

func TestComesUpWithTheAddressesIPCPAssigned(t *testing.T) {
	t.Parallel()
	link, control := newLink(t)

	status := link.Status()

	if status.State != (satnet.LinkState{Kind: satnet.LinkStateUp}) || status.ConnectMessage != "19200" {
		t.Errorf("state %v, connect message %q", status.State, status.ConnectMessage)
	}
	if status.IPv4 == nil {
		t.Fatal("IPCP did not finish")
	}
	if status.IPv4.LocalAddress != netip.AddrFrom4([4]byte{10, 64, 0, 2}) ||
		!slices.Equal(status.IPv4.DNSServers, []netip.Addr{netip.AddrFrom4([4]byte{10, 64, 0, 1})}) {
		t.Errorf("IPv4 = %+v", *status.IPv4)
	}
	if !control.Stats().InDataCall {
		t.Error("no data call is up")
	}
}

func TestResolvesOnlyNamesThePolicyAllows(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7", "*.missing.test:*")

	addresses, err := link.Resolve("echo.test", timeout)

	if err != nil || !slices.Equal(addresses, []netip.Addr{echoAddress}) {
		t.Errorf("Resolve(echo.test) = %v, %v", addresses, err)
	}
	_, err = link.Resolve("web.test", timeout)
	expectKind(t, err, satnet.ErrPolicyDenied)
	_, err = link.Resolve("nothing.missing.test", timeout)
	expectKind(t, err, satnet.ErrNameNotFound)
}

func TestANameTheSecondDNSServerSaysDoesNotExistIsNotATimeout(t *testing.T) {
	t.Parallel()
	// IPCP offers first a server that never answers: the stack gives it ten
	// seconds before it asks the gateway, whose answer then comes late.
	silent := netip.AddrFrom4([4]byte{10, 64, 0, 99})
	simulator := simtest.SimulatorConfig()
	simulator.Network.DNSServers = []netip.Addr{silent, simulator.Network.GatewayAddress}
	simulator.Network.SilentAddresses = []netip.Addr{silent}
	handset, _ := simtest.NewHandset(t, simulator)
	config := linkConfig(t, "missing.test:*")
	// The shortest idle limit allowed outlasts the wait.
	config.Limits.IdleTimeout = 25 * time.Second
	link := establish(t, handset, config)
	started := time.Now()

	_, err := link.Resolve("missing.test", 60*time.Second)

	expectKind(t, err, satnet.ErrNameNotFound)
	if elapsed := time.Since(started); elapsed < 10*time.Second {
		t.Errorf("the lookup took %v: the silent server was not asked first", elapsed)
	}
	if got := link.Status().State; got != (satnet.LinkState{Kind: satnet.LinkStateUp}) {
		t.Errorf("state %v", got)
	}
}

func TestALookupNoServerAnswersTimesOut(t *testing.T) {
	t.Parallel()
	link := linkWithASilentDNSServer(t, "echo.test:7")

	_, err := link.Resolve("echo.test", 60*time.Second)

	expectTimedOut(t, err, "DNS lookup")
}

func TestLocalHostsNeedNoDNS(t *testing.T) {
	t.Parallel()
	config := linkConfig(t, "relay.example:7")
	config.Hosts["relay.example"] = echoAddress
	link, _ := linkWith(t, config)

	addresses, err := link.Resolve("Relay.Example", timeout)

	if err != nil || !slices.Equal(addresses, []netip.Addr{echoAddress}) {
		t.Errorf("Resolve(Relay.Example) = %v, %v", addresses, err)
	}
}

// TestAnAnswerIsReusedRatherThanAskedForAgain checks that an answer is
// reused. Each lookup costs a satellite round trip of airtime.
func TestAnAnswerIsReusedRatherThanAskedForAgain(t *testing.T) {
	t.Parallel()
	link, control := newLink(t, "echo.test:7")
	if _, err := link.Resolve("echo.test", timeout); err != nil {
		t.Fatal(err)
	}
	before := control.Stats().DataBytesFromApp

	addresses, err := link.Resolve("echo.test", timeout)

	if err != nil || !slices.Equal(addresses, []netip.Addr{echoAddress}) {
		t.Errorf("Resolve(echo.test) again = %v, %v", addresses, err)
	}
	// A lookup that asked again would have had its answer before returning.
	if sent := control.Stats().DataBytesFromApp - before; sent != 0 {
		t.Errorf("the second lookup sent %d bytes, want none", sent)
	}
}

func TestTCPRoundTripThroughTheEchoService(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)

	expectEcho(t, conn, []byte("hello from orbit"))

	if conn.PeerAddr().Addr() != echoAddress {
		t.Errorf("PeerAddr() = %v", conn.PeerAddr())
	}
}

// TestTheUsageIsCurrentWhileTheLinkIsUp checks that an app showing what the
// call costs reads it while the link is up, not only once it has closed.
func TestTheUsageIsCurrentWhileTheLinkIsUp(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)

	// The usage counts what the reply arrived with by the time Read returns
	// it.
	expectEcho(t, conn, []byte("first"))
	first := link.Status()
	expectEcho(t, conn, []byte("second"))
	second := link.Status()

	if first.State != (satnet.LinkState{Kind: satnet.LinkStateUp}) {
		t.Fatalf("state %v, want the link up", first.State)
	}
	if usage := first.Usage; usage.ConnectedFor <= 0 || usage.BytesSent == 0 || usage.BytesReceived == 0 {
		t.Errorf("usage while up = %+v, want the airtime and traffic so far", usage)
	}
	if was, is := first.Usage, second.Usage; is.ConnectedFor <= was.ConnectedFor || is.BytesSent <= was.BytesSent ||
		is.BytesReceived <= was.BytesReceived {
		t.Errorf("usage went from %+v to %+v over a round trip, want all of it grown", was, is)
	}
}

func TestHTTPOverTCP(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "web.test:80")
	conn := dial(t, link, "web.test", webPort)
	send(t, conn, []byte("GET / HTTP/1.0\r\nHost: web.test\r\n\r\n"))
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}

	response, err := io.ReadAll(conn)

	if err != nil || !strings.HasPrefix(string(response), "HTTP/1.0 200 OK") ||
		!strings.HasSuffix(string(response), sim.DefaultNetworkConfig().WebPage) {
		t.Errorf("response %q, %v", response, err)
	}
}

func TestOneGoroutineCanReadWhileAnotherWrites(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)
	message := bytes.Repeat([]byte("full duplex"), 20)
	sent := make(chan error, 1)

	go func() {
		for chunk := range slices.Chunk(message, 11) {
			if _, err := conn.Write(chunk); err != nil {
				sent <- err
				return
			}
		}
		sent <- nil
	}()
	received := receiveExactly(t, conn, len(message))

	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, message) {
		t.Errorf("received %q", received)
	}
}

func TestThePolicyBlocksOtherPortsAndBareAddresses(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")

	_, err := link.Dial("echo.test", webPort, timeout)
	expectKind(t, err, satnet.ErrPolicyDenied)
	_, err = link.Dial("192.0.2.7", echoPort, timeout)
	expectKind(t, err, satnet.ErrPolicyDenied)
}

func TestAddressRulesAllowBareAddresses(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "192.0.2.0/24:7")
	conn := dial(t, link, "192.0.2.99", echoPort)

	expectEcho(t, conn, []byte("ping"))
}

func TestUDPRoundTripThroughTheEchoService(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	socket := dialUDP(t, link, "echo.test", echoPort)
	send(t, socket, []byte("datagram"))
	if err := socket.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}

	buffer := make([]byte, 64)
	count, err := socket.Read(buffer)

	if err != nil || string(buffer[:count]) != "datagram" {
		t.Errorf("Read = %q, %v", buffer[:count], err)
	}
	_, err = socket.Write(make([]byte, socket.MaxDatagramLen()+1))
	expectKind(t, err, satnet.ErrDatagramTooLarge)
}

func TestClosingReturnsTheHandsetInCommandMode(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, simtest.SimulatorConfig())
	link := establish(t, handset, linkConfig(t))

	if err := link.Close(); err != nil {
		t.Fatalf("the hang-up fails: %v", err)
	}

	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
	expectCommandMode(t, handset)
}

func TestStateChangesAreReportedInOrder(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t)
	watcher := link.Watcher()
	expectStateChange(t, watcher, satnet.LinkState{Kind: satnet.LinkStateUp})

	link.RequestClose()

	expectStateChange(t, watcher, state(satnet.LinkStateClosing, satnet.CloseReasonRequested))
	expectStateChange(t, watcher, state(satnet.LinkStateClosed, satnet.CloseReasonRequested))
}

func TestADroppedCallEndsTheLink(t *testing.T) {
	t.Parallel()
	link, control := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)

	control.DropCall()

	expectClosed(t, link.Watcher(), satnet.CloseReasonCarrierLost)
	_, err := readExactly(conn, 8)
	expectLinkDown(t, err, satnet.CloseReasonCarrierLost)
	_, err = link.Dial("echo.test", echoPort, timeout)
	expectKind(t, err, satnet.ErrLinkDown)
}

// TestDataHoldingTheHandsetsNoCarrierDoesNotEndTheLink checks that the link
// asks the gateway to escape CR, so that the handset's own result code
// never appears inside a frame: otherwise any server could end the call by
// sending it — or fake the handset's OK while it hangs up.
func TestDataHoldingTheHandsetsNoCarrierDoesNotEndTheLink(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)

	expectEcho(t, conn, []byte("\r\nNO CARRIER\r\n"))

	// Had the call ended for it, the link would carry nothing more.
	expectEcho(t, conn, []byte("still up"))
	if got := link.Status().State; got != (satnet.LinkState{Kind: satnet.LinkStateUp}) {
		t.Errorf("state %v, want the link still up", got)
	}
}

func TestAnIdleLinkHangsUpByItself(t *testing.T) {
	t.Parallel()
	config := linkConfig(t)
	// The shortest idle limit allowed.
	config.Limits.IdleTimeout = 25 * time.Second
	link, control := linkWith(t, config)

	if got, ok := link.Watcher().WaitClosed(timeout + config.Limits.IdleTimeout); !ok || got.Kind != satnet.CloseReasonIdleTimeout {
		t.Errorf("WaitClosed() = %v, %t; want the idle timeout", got, ok)
	}

	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
	// The limit did not act before its time.
	if connected := link.Status().Usage.ConnectedFor; connected < config.Limits.IdleTimeout {
		t.Errorf("the call lasted %v, less than the idle limit %v", connected, config.Limits.IdleTimeout)
	}
}

// setup is the outcome of establishing a link in the background.
type setup struct {
	link *satnet.Link
	err  error
}

// establishInBackground starts establishing a link with config over
// handset in another goroutine, and returns the channel its outcome
// arrives on.
func establishInBackground(handset *iridium.Handset, config satnet.Config) <-chan setup {
	outcome := make(chan setup, 1)
	go func() {
		link, err := satnet.Establish(handset, config)
		outcome <- setup{link: link, err: err}
	}()
	return outcome
}

// receiveSetup returns the outcome of a setup started by
// establishInBackground, failing the test if it does not end within
// timeout. A link that came up is closed at the test's end.
func receiveSetup(t *testing.T, outcome <-chan setup) setup {
	t.Helper()
	select {
	case result := <-outcome:
		if result.link != nil {
			t.Cleanup(func() {
				// What the test needed is checked; a call this hang-up
				// leaves up ends with the simulator's Close.
				_ = result.link.Close()
			})
		}
		return result
	case <-time.After(timeout):
		t.Fatal("the setup never ended")
		return setup{}
	}
}

// awaitSetup returns the error of a setup started by establishInBackground,
// as receiveSetup does.
func awaitSetup(t *testing.T, outcome <-chan setup) error {
	t.Helper()
	return receiveSetup(t, outcome).err
}

// awaitLink returns the link a setup started by establishInBackground
// brought up, as receiveSetup does, failing the test if it did not.
func awaitLink(t *testing.T, outcome <-chan setup) *satnet.Link {
	t.Helper()
	result := receiveSetup(t, outcome)
	if result.err != nil {
		t.Fatalf("the link does not come up: %v", result.err)
	}
	return result.link
}

// expectCancelled checks that err says the call setup was cancelled, and
// that the call was hung up.
func expectCancelled(t *testing.T, err error) {
	t.Helper()
	var linkErr *satnet.Error
	if !errors.As(err, &linkErr) || linkErr.Kind != satnet.ErrHandset || !errors.Is(err, iridium.ErrCancelled) {
		t.Errorf("err = %v, want the handset's ErrCancelled", err)
	}
	if errors.Is(err, iridium.ErrCallMayStillBeUp) {
		t.Errorf("%v says the call may still be up", err)
	}
}

// TestALinkCancelledWhileItDialsNeverConnects cancels the setup once the dial
// is under way, rather than a second after it starts.
func TestALinkCancelledWhileItDialsNeverConnects(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, simulatorWith(sim.LinkProfile{CallSetup: 5 * time.Second}))
	config := linkConfig(t)
	started := time.Now()
	established := establishInBackground(handset, config)
	simtest.Eventually(t, timeout, "the dial is under way", func() bool {
		return control.Stats().CallsPlaced == 1
	})

	config.Call.Cancellation.Cancel()

	expectCancelled(t, awaitSetup(t, established))
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Errorf("the cancelled setup took %v, as long as the call setup", elapsed)
	}
	expectNotBusy(t, handset)
	if stats := control.Stats(); stats.CallsPlaced != 1 || stats.CallsConnected != 0 {
		t.Errorf("%d calls placed and %d connected, want 1 and 0", stats.CallsPlaced, stats.CallsConnected)
	}
}

// TestALinkCancelledWhilePPPComesUpIsHungUp cancels the setup once the call is
// up, rather than a second after it starts.
func TestALinkCancelledWhilePPPComesUpIsHungUp(t *testing.T) {
	t.Parallel()
	// Each of PPP's round trips takes two seconds.
	handset, control := simtest.NewHandset(t, delayedLink(time.Second))
	config := linkConfig(t)
	established := establishInBackground(handset, config)
	simtest.Eventually(t, timeout, "the call is up", func() bool {
		return control.Stats().CallsConnected == 1
	})

	config.Call.Cancellation.Cancel()

	expectCancelled(t, awaitSetup(t, established))
	expectCommandMode(t, handset)
	if stats := control.Stats(); stats.CallsConnected != 1 || stats.InDataCall {
		t.Errorf("%d calls connected, in a data call: %t; want 1, hung up", stats.CallsConnected, stats.InDataCall)
	}
}

func TestAContextCancelsTheSetupThroughAfterFunc(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, simulatorWith(sim.LinkProfile{CallSetup: 5 * time.Second}))
	config := linkConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(ctx, config.Call.Cancellation.Cancel)
	established := establishInBackground(handset, config)
	simtest.Eventually(t, timeout, "the dial is under way", func() bool {
		return control.Stats().CallsPlaced == 1
	})

	cancel()

	expectCancelled(t, awaitSetup(t, established))
	if stop() {
		t.Error("the context's end did not cancel the setup")
	}
}

func TestTheDataLimitHangsUp(t *testing.T) {
	if testing.Short() {
		t.Skip("moves 4000 bytes at the default 300 B/s")
	}
	config := linkConfig(t, "echo.test:7")
	config.Limits.MaxBytes = 4_000
	link, _ := linkWith(t, config)
	conn := dial(t, link, "echo.test", echoPort)
	deadline := time.Now().Add(timeout)
	chunk := make([]byte, 512)

	for link.Status().State.Kind == satnet.LinkStateUp && time.Now().Before(deadline) {
		// Errors are expected once the limit ends the link; the loop stops
		// then.
		_ = conn.SetWriteDeadline(deadline)
		_, _ = conn.Write(bytes.Repeat([]byte{0x42}, 256))
		_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, _ = conn.Read(chunk)
	}

	expectClosed(t, link.Watcher(), satnet.CloseReasonDataLimit)
	// The limit did not act before its time.
	if usage := link.Status().Usage; usage.BytesSent+usage.BytesReceived < config.Limits.MaxBytes {
		t.Errorf("%d bytes moved, short of the data limit %d", usage.BytesSent+usage.BytesReceived, config.Limits.MaxBytes)
	}
}

func TestAFailedDialGivesTheHandsetBack(t *testing.T) {
	t.Parallel()
	config := linkConfig(t)
	config.Number = "not a number"
	handset, _ := simtest.NewHandset(t, simtest.SimulatorConfig())

	_, err := satnet.Establish(handset, config)

	expectKind(t, err, satnet.ErrHandset)
	expectCommandMode(t, handset)
}

// slowLink is a link with a round trip and a rate that let a test see
// data still on its way.
func slowLink() sim.Config {
	return simulatorWith(sim.LinkProfile{OneWayDelay: 300 * time.Millisecond, BytesPerSecond: 3_000})
}

func TestClosingDeliversWhatWasWrittenButTakesNothingNew(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, slowLink())
	config := linkConfig(t, "echo.test:9")
	config.Tuning.LinkRate = 3_000
	link := establish(t, handset, config)
	watcher := link.Watcher()
	expectStateChange(t, watcher, satnet.LinkState{Kind: satnet.LinkStateUp})
	conn := dial(t, link, "echo.test", discardPort)
	message := bytes.Repeat([]byte{0x42}, 3_000)
	send(t, conn, message)

	link.RequestClose()

	expectStateChange(t, watcher, state(satnet.LinkStateClosing, satnet.CloseReasonRequested))
	_, err := conn.Write([]byte("more"))
	expectLinkDown(t, err, satnet.CloseReasonRequested)
	if err := link.Close(); err != nil {
		t.Fatalf("the hang-up fails: %v", err)
	}
	if discarded := control.Stats().DiscardedBytes; discarded != uint64(len(message)) {
		t.Errorf("the discard service received %d bytes, want %d", discarded, len(message))
	}
}

// The datagrams of TestClosingDeliversTheDatagramsAlreadyWritten, which the
// paced line takes one at a time.
const (
	datagramsBeforeTheClose = 3
	datagramLen             = 200
)

// TestClosingDeliversTheDatagramsAlreadyWritten is the UDP side of
// TestClosingDeliversWhatWasWrittenButTakesNothingNew: the stack hands every
// datagram to the device at once, where they wait for the line.
func TestClosingDeliversTheDatagramsAlreadyWritten(t *testing.T) {
	t.Parallel()
	link, control := newLink(t, "echo.test:7")
	socket := dialUDP(t, link, "echo.test", echoPort)
	before := control.Stats().DataBytesFromApp
	for range datagramsBeforeTheClose {
		send(t, socket, make([]byte, datagramLen))
	}

	// All but the first still wait for the line.
	if err := link.Close(); err != nil {
		t.Fatalf("the hang-up fails: %v", err)
	}

	// The handset counts the datagrams with PPP's framing around them.
	written := uint64(datagramsBeforeTheClose * (datagramLen + wire.IPv4HeaderLen + wire.UDPHeaderLen))
	if sent := control.Stats().DataBytesFromApp - before; sent < written {
		t.Errorf("%d bytes went before the hang-up, fewer than the %d of the datagrams written", sent, written)
	}
}

func TestARequestedCloseLetsWaitsAlreadyUnderWayFinish(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, slowLink())
	config := linkConfig(t, "echo.test:7", "echo.test:9")
	config.Tuning.LinkRate = 3_000
	link := establish(t, handset, config)
	upload := dial(t, link, "echo.test", discardPort)
	echo := dial(t, link, "echo.test", echoPort)
	message := bytes.Repeat([]byte{0x42}, 3_000)
	send(t, upload, message)
	send(t, echo, []byte("reply, please"))
	reply := make(chan []byte, 1)
	go func() {
		received, err := readExactly(echo, 13)
		if err != nil {
			t.Error(err)
		}
		reply <- received
	}()
	// A fixed 50 ms sleep would have the read under way before the close;
	// AwaitWaitingCall waits for exactly that.
	satnet.AwaitWaitingCall(t, link, timeout)

	link.RequestClose()

	// Both were under way before the close: what was written arrives, and
	// the answer to it is read.
	if err := upload.WaitAcknowledged(time.Now().Add(timeout)); err != nil {
		t.Errorf("the close does not deliver what was written: %v", err)
	}
	if got := <-reply; string(got) != "reply, please" {
		t.Errorf("the reply is %q", got)
	}
	if err := link.Close(); err != nil {
		t.Fatalf("the hang-up fails: %v", err)
	}
	if discarded := control.Stats().DiscardedBytes; discarded != uint64(len(message)) {
		t.Errorf("the discard service received %d bytes, want %d", discarded, len(message))
	}
}

func TestABulkUploadDoesNotHoldUpALaterConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("measures a reply over a link busy for 16 s")
	}
	handset, control := simtest.NewHandset(t,
		simulatorWith(sim.LinkProfile{OneWayDelay: 300 * time.Millisecond, BytesPerSecond: 1_000}))
	config := linkConfig(t, "echo.test:7", "echo.test:9")
	config.Tuning.LinkRate = 1_000
	config.Tuning.CloseGrace = time.Second
	link := establish(t, handset, config)
	bulk := dial(t, link, "echo.test", discardPort)
	chat := dial(t, link, "echo.test", echoPort)
	before := control.Stats().DataBytesFromApp
	uploaded := make(chan struct{})
	// About 16 s of airtime, sent first: a connection opened earlier is
	// asked first, every time the line frees.
	go func() {
		defer close(uploaded)
		// The link closes under the upload; that error is expected.
		_, _ = bulk.Write(bytes.Repeat([]byte{0x42}, 16_000))
	}()
	simtest.Eventually(t, timeout, "the bulk upload is under way", func() bool {
		return control.Stats().DataBytesFromApp > before+1_000
	})

	started := time.Now()
	expectEcho(t, chat, []byte("still there?"))

	if elapsed := time.Since(started); elapsed >= 6*time.Second {
		t.Errorf("the reply took %v", elapsed)
	}
	if err := link.Close(); err != nil {
		t.Errorf("the hang-up fails: %v", err)
	}
	<-uploaded
}

// TestALookupDoesNotWaitBehindBusyConnections starts the lookup once the
// uploads have kept the line busy for three seconds, rather than three seconds
// after they start.
func TestALookupDoesNotWaitBehindBusyConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("times a lookup over a line busy with five uploads")
	}
	// Paced to Iridium's rate, five uploads keep about ten seconds of
	// packets waiting for the line.
	config := linkConfig(t, "192.0.2.7:9", "echo.test:7")
	config.Tuning.CloseGrace = briefCloseGrace
	link, control := linkWith(t, config)
	before := control.Stats().DataBytesFromApp
	const uploadCount = 5
	uploads := make(chan error, uploadCount)
	for range uploadCount {
		upload := dial(t, link, "192.0.2.7", discardPort)
		go func() {
			// The link closes under the upload; that error is expected.
			_, err := upload.Write(bytes.Repeat([]byte{0x42}, 20_000))
			uploads <- err
		}()
	}
	simtest.Eventually(t, timeout, "the line has been busy for three seconds", func() bool {
		return control.Stats().DataBytesFromApp >= before+3*uint64(config.Tuning.LinkRate)
	})
	started := time.Now()

	addresses, err := link.Resolve("echo.test", 30*time.Second)

	if err != nil || !slices.Equal(addresses, []netip.Addr{echoAddress}) {
		t.Errorf("Resolve(echo.test) = %v, %v", addresses, err)
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Errorf("the lookup took %v", elapsed)
	}
	if err := link.Close(); err != nil {
		t.Errorf("the hang-up fails: %v", err)
	}
	for range uploadCount {
		<-uploads
	}
}

func TestDataCutOffByANewAddressIsNotReportedDelivered(t *testing.T) {
	t.Parallel()
	link, control := newLink(t, "192.0.2.7:9")
	conn := dial(t, link, "192.0.2.7", discardPort)
	// Paced to Iridium's rate, this takes ten seconds to leave.
	send(t, conn, bytes.Repeat([]byte{0x42}, 3_000))
	newAddress := netip.AddrFrom4([4]byte{10, 64, 0, 3})

	if err := control.RestartPPP(newAddress); err != nil {
		t.Fatal(err)
	}
	err := conn.WaitAcknowledged(time.Now().Add(60 * time.Second))

	expectKind(t, err, satnet.ErrConnectionReset)
	if ipv4 := link.Status().IPv4; ipv4 == nil || ipv4.LocalAddress != newAddress {
		t.Errorf("IPv4 = %+v, want IPCP finished again with %v", ipv4, newAddress)
	}
}

// pppFlag begins every PPP frame (RFC 1662 §3.1), and so everything a link
// writes once its call is up; AT commands and the escape do not.
const pppFlag = 0x7E

// unpluggingLine is a line to the simulated handset that fails the next PPP
// frame it is given, as a cable pulled out while the frame is written does,
// and lets everything else through.
type unpluggingLine struct {
	*sim.Handset
	// whileFailing is what runs during the write of the frame that fails:
	// what the app does meanwhile in another goroutine. Nil until the line
	// is to fail.
	whileFailing atomic.Pointer[func()]
}

// unplugDuring makes the next PPP frame fail once during has run.
func (l *unpluggingLine) unplugDuring(during func()) {
	l.whileFailing.Store(&during)
}

func (l *unpluggingLine) WriteAll(p []byte) error {
	if len(p) > 0 && p[0] == pppFlag {
		if during := l.whileFailing.Swap(nil); during != nil {
			(*during)()
			return &iridium.Error{Kind: iridium.ErrTransport, Message: "unplugged"}
		}
	}
	return l.Handset.WriteAll(p)
}

func TestDataTheHangUpDroppedIsNotReportedDelivered(t *testing.T) {
	t.Parallel()
	simulator := simtest.NewSimulator(t, simtest.SimulatorConfig())
	line := &unpluggingLine{Handset: simulator}
	link := establish(t, simtest.InitializedHandset(t, line), linkConfig(t, "192.0.2.7:9"))
	idle := dial(t, link, "192.0.2.7", discardPort)
	other := dial(t, link, "192.0.2.7", discardPort)
	written := make(chan error, 1)
	// Taken while the link is up, after the driver last handed the stack
	// what the app wrote: it still waits in the outbox as the hang-up starts.
	line.unplugDuring(func() {
		_, err := idle.Write([]byte("lost on the line"))
		written <- err
	})

	// Its segment is a frame for the line to fail on. Should another frame
	// come first, the line fails on that one instead, and the link may then
	// refuse this write.
	if _, err := other.Write([]byte("go")); err != nil && !errors.Is(err, satnet.ErrLinkDown) {
		t.Fatal(err)
	}

	expectClosed(t, link.Watcher(), satnet.CloseReasonTransport)
	if err := awaitError(t, written); err != nil {
		t.Fatalf("the Write while the line failed = %v, want it taken", err)
	}
	expectLinkDown(t, idle.WaitAcknowledged(time.Now().Add(timeout)), satnet.CloseReasonTransport)
	if discarded := simulator.Control().Stats().DiscardedBytes; discarded != 0 {
		t.Errorf("the discard service received %d bytes, want none", discarded)
	}
}

func TestAReplyToAClosedUDPSocketCostsNoAirtime(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, delayedLink(300*time.Millisecond))
	link := establish(t, handset, linkConfig(t, "192.0.2.7:7"))
	start := control.Stats()
	socket := dialUDP(t, link, "192.0.2.7", echoPort)

	send(t, socket, bytes.Repeat([]byte{0x42}, 400))
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}

	simtest.Eventually(t, timeout, "the echo came back", func() bool {
		return control.Stats().DataBytesToApp >= start.DataBytesToApp+400
	})
	// The wait for an ICMP error to go out, were there one: a wait for
	// something not to happen, the one sleep the tests allow. Paced at
	// Iridium's rate, an error the stack let out would wait for the line
	// longer than this; TestTheStacksICMPErrorsNeverGo, an internal test,
	// is what holds the stack to dropping them.
	time.Sleep(300 * time.Millisecond)
	if sent := control.Stats().DataBytesFromApp - start.DataBytesFromApp; sent >= 600 {
		t.Errorf("%d bytes sent for one 428-byte datagram", sent)
	}
}

func TestNothingIsSentOnceTheLinkIsClosed(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	conn := dial(t, link, "echo.test", echoPort)
	socket := dialUDP(t, link, "echo.test", echoPort)

	link.RequestClose()

	expectClosed(t, link.Watcher(), satnet.CloseReasonRequested)
	_, err := conn.Write([]byte("tcp"))
	expectLinkDown(t, err, satnet.CloseReasonRequested)
	_, err = socket.Write([]byte("udp"))
	expectLinkDown(t, err, satnet.CloseReasonRequested)
	_, err = link.Dial("echo.test", echoPort, timeout)
	expectLinkDown(t, err, satnet.CloseReasonRequested)
}

func TestAConnectCutShortByTheCallDroppingSaysTheLinkIsDown(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, delayedLink(500*time.Millisecond))
	link := establish(t, handset, linkConfig(t, "192.0.2.7:7"))
	before := control.Stats().DataBytesFromApp
	dialled := make(chan error, 1)
	go func() {
		_, err := link.Dial("192.0.2.7", echoPort, timeout)
		dialled <- err
	}()

	simtest.Eventually(t, timeout, "the SYN is on the line", func() bool {
		return control.Stats().DataBytesFromApp > before
	})
	control.DropCall()

	expectLinkDown(t, <-dialled, satnet.CloseReasonCarrierLost)
}

func TestPacketsFitAGatewayThatTakesOnlySmallOnes(t *testing.T) {
	t.Parallel()
	simulator := simtest.SimulatorConfig()
	simulator.Network.GatewayMRU = 296
	handset, _ := simtest.NewHandset(t, simulator)
	link := establish(t, handset, linkConfig(t, "echo.test:7"))

	socket := dialUDP(t, link, "echo.test", echoPort)
	if got := socket.MaxDatagramLen(); got != 296-28 {
		t.Errorf("MaxDatagramLen() = %d, want %d", got, 296-28)
	}
	expectEcho(t, dial(t, link, "echo.test", echoPort), pattern(2_000))
}

func TestAnUnusableAddressFromTheGatewayEndsTheCall(t *testing.T) {
	t.Parallel()
	simulator := simtest.SimulatorConfig()
	simulator.Network.ClientAddress = netip.AddrFrom4([4]byte{255, 255, 255, 255})
	handset, control := simtest.NewHandset(t, simulator)

	_, err := satnet.Establish(handset, linkConfig(t))

	expectLinkDown(t, err, satnet.CloseReasonPPP)
	expectNotBusy(t, handset)
	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
}

func TestALinkThatFailsAndCannotHangUpSaysTheCallMayStillBeUp(t *testing.T) {
	t.Parallel()
	simulator := simtest.SimulatorConfig()
	simulator.Authentication = &sim.Authentication{
		Protocol:    ppp.AuthProtocolPAP,
		Credentials: ppp.Credentials{Username: "app", Password: "secret"},
	}
	handset, control, faulty := faultyHandset(t, simulator)
	faulty.DeafToEscape.Store(true)
	faulty.DeafToDTR.Store(true)

	_, err := satnet.Establish(handset, linkConfig(t))

	// The gateway refuses our credentials.
	expectLinkDown(t, err, satnet.CloseReasonPPP)
	var linkErr *satnet.Error
	if !errors.Is(err, iridium.ErrCallMayStillBeUp) || !errors.As(err, &linkErr) || !linkErr.CallMayStillBeUp {
		t.Errorf("%v does not say the call may still be up", err)
	}
	if !control.Stats().InDataCall {
		t.Error("the call is not up, as the failed hang-up says it may be")
	}
	expectNotBusy(t, handset)
}

// TestAPanicWhileHangingUpStillReportsTheLinkClosed checks that a panic while
// hanging up still reports the link closed. The handset is never moved into
// the link, so it cannot go down with the panic; instead it is released, its
// methods no longer failing with iridium.ErrBusy.
func TestAPanicWhileHangingUpStillReportsTheLinkClosed(t *testing.T) {
	t.Parallel()
	link, handset, control, faulty := faultyLink(t)
	watcher := link.Watcher()
	faulty.PanicsOnEscape.Store(true)

	err := link.Close()

	var linkErr *satnet.Error
	if !errors.As(err, &linkErr) || linkErr.Kind != satnet.ErrLinkDown || !linkErr.CallMayStillBeUp ||
		linkErr.Reason != (satnet.CloseReason{Kind: satnet.CloseReasonInternal, Detail: "the link's driver thread panicked"}) {
		t.Fatalf("Close() = %#v, want the panicked driver's error", err)
	}
	expectClosed(t, watcher, satnet.CloseReasonRequested)
	if !watcher.Status().CallMayStillBeUp {
		t.Error("the status does not say the call may still be up")
	}
	expectNotBusy(t, handset)
	// The core's hang-up pulses DTR as the panic unwinds, which ends the call
	// after AT&D2. The test counts those drops below.
	if faulty.DTRDrops() < 1 {
		t.Error("DTR was never dropped")
	}
	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
}

func TestAHandsetDeafToTheEscapeIsHungUpByDroppingDTR(t *testing.T) {
	t.Parallel()
	link, handset, control, faulty := faultyLink(t)
	faulty.DeafToEscape.Store(true)

	if err := link.Close(); err != nil {
		t.Fatalf("the DTR drop does not end the call: %v", err)
	}

	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
	expectCommandMode(t, handset)
	if faulty.DTRDrops() < 1 {
		t.Error("DTR was never dropped")
	}
}

func TestAHangUpThatCannotBeConfirmedIsReported(t *testing.T) {
	t.Parallel()
	link, handset, control, faulty := faultyLink(t)

	faulty.Unplugged.Store(true)

	expectClosed(t, link.Watcher(), satnet.CloseReasonTransport)
	if !link.Status().CallMayStillBeUp {
		t.Error("the status does not say the call may still be up")
	}
	if !control.Stats().InDataCall {
		t.Error("the simulated call is not still up")
	}
	if err := link.Close(); !errors.Is(err, iridium.ErrCallMayStillBeUp) {
		t.Errorf("Close() = %v, want the failed hang-up", err)
	}
	expectNotBusy(t, handset)
}

func TestEarlyRetransmissionsCostNoAirtime(t *testing.T) {
	if testing.Short() {
		t.Skip("uploads 2000 bytes at Iridium's 300 B/s")
	}
	// Iridium's throughput, with a round trip longer than the TCP stack's
	// first retransmission timeouts.
	handset, control := simtest.NewHandset(t,
		simulatorWith(sim.LinkProfile{OneWayDelay: 1_200 * time.Millisecond, BytesPerSecond: 300}))
	config := linkConfig(t, "relay.test:9")
	config.Hosts["relay.test"] = echoAddress
	link := establish(t, handset, config)
	before := control.Stats().DataBytesFromApp
	conn, err := link.Dial("relay.test", discardPort, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	upload := bytes.Repeat([]byte{0x42}, 2_000)

	if _, err := conn.Write(upload); err != nil {
		t.Fatal(err)
	}
	if err := conn.WaitAcknowledged(time.Now().Add(60 * time.Second)); err != nil {
		t.Fatal(err)
	}

	if discarded := control.Stats().DiscardedBytes; discarded != uint64(len(upload)) {
		t.Errorf("the discard service received %d bytes", discarded)
	}
	// Headers, framing and the handshake add about 16%; a single resent
	// full-size segment would add 29% more.
	sent := control.Stats().DataBytesFromApp - before
	if allowed := uint64(len(upload)) * 13 / 10; sent >= allowed {
		t.Errorf("%d bytes on the line for a 2000-byte upload", sent)
	}
}

func TestACallAnEarlierSessionLeftUpIsEndedBeforeDialling(t *testing.T) {
	t.Parallel()
	simulator := simtest.NewSimulator(t, simtest.SimulatorConfig())
	control := simulator.Control()
	earlier := simtest.InitializedHandset(t, simulator)
	// Online, then abandoned as by a crashed app.
	if _, err := earlier.DialData(satnet.IridiumInternetNumber, linkConfig(t).Call); err != nil {
		t.Fatal(err)
	}
	if !control.Stats().InDataCall {
		t.Fatal("the earlier session's call is not up")
	}

	handset := simtest.InitializedHandset(t, simulator)

	if control.Stats().InDataCall {
		t.Error("the earlier session's call is still up")
	}
	link := establish(t, handset, linkConfig(t))
	if got := link.Status().State; got != (satnet.LinkState{Kind: satnet.LinkStateUp}) {
		t.Errorf("state %v", got)
	}
}

func TestTheLongestTimeoutMeansNoDeadline(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")
	const longest = math.MaxInt64

	if addresses, err := link.Resolve("echo.test", longest); err != nil || !slices.Equal(addresses, []netip.Addr{echoAddress}) {
		t.Errorf("Resolve = %v, %v", addresses, err)
	}
	conn, err := link.Dial("echo.test", echoPort, longest)
	if err != nil {
		t.Fatal(err)
	}
	expectEchoWithin(t, conn, longest)
	socket, err := link.DialUDP("echo.test", echoPort, longest)
	if err != nil {
		t.Fatal(err)
	}
	expectEchoWithin(t, socket, longest)
}

// expectEchoWithin sends "ping" on conn and expects it back, all within
// timeout, and closes conn.
func expectEchoWithin(t *testing.T, conn net.Conn, timeout time.Duration) {
	t.Helper()
	// Closing a connection of the link cannot fail.
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, 4)
	if _, err := io.ReadFull(conn, echoed); err != nil || string(echoed) != "ping" {
		t.Errorf("echoed %q, %v", echoed, err)
	}
}

func TestAnInvalidConfigurationIsRefusedBeforeDialling(t *testing.T) {
	t.Parallel()
	config := linkConfig(t)
	config.Tuning.MTU = 10
	handset, control := simtest.NewHandset(t, simtest.SimulatorConfig())

	_, err := satnet.Establish(handset, config)

	expectKind(t, err, satnet.ErrInvalidConfig)
	if calls := control.Stats().CallsPlaced; calls != 0 {
		t.Errorf("%d calls placed", calls)
	}
}

// TestTheCallTimeLimitEndsTheCall asserts no timing: when the hang-up
// starts, to end the call in time, is a unit test's (driver_test.go).
func TestTheCallTimeLimitEndsTheCall(t *testing.T) {
	t.Parallel()
	config := linkConfig(t)
	// Just longer than bringing PPP up and hanging up take, 20 s and 2 × 60
	// ms + 1 s: the hang-up starts 20.38 s after CONNECT.
	config.Limits.MaxCallDuration = 21500 * time.Millisecond
	link, control := linkWith(t, config)

	if got, ok := link.Watcher().WaitClosed(timeout + config.Limits.MaxCallDuration); !ok || got.Kind != satnet.CloseReasonCallTimeLimit {
		t.Errorf("WaitClosed() = %v, %t; want the call time limit", got, ok)
	}

	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
	hangUpStart := config.Limits.MaxCallDuration - satnet.HangUpTime(config)
	if connected := link.Status().Usage.ConnectedFor; connected < hangUpStart {
		t.Errorf("the call lasted %v, but the hang-up was due only %v after CONNECT", connected, hangUpStart)
	}
}

func TestPPPThatDoesNotComeUpInTimeEndsTheCall(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, delayedLink(500*time.Millisecond))
	config := linkConfig(t)
	config.Tuning.NegotiationTimeout = 200 * time.Millisecond

	_, err := satnet.Establish(handset, config)

	expectLinkDown(t, err, satnet.CloseReasonNegotiationTimedOut)
	if control.Stats().InDataCall {
		t.Error("the call is still up")
	}
	expectCommandMode(t, handset)
}

func TestAPanicOfTheDriverEndsTheLinkAndStillHangsUp(t *testing.T) {
	t.Parallel()
	link, handset, control, faulty := faultyLink(t)

	faulty.PanicsOnNextRead.Store(true)

	reason, ok := link.Watcher().WaitClosed(timeout)
	if !ok || reason != (satnet.CloseReason{Kind: satnet.CloseReasonInternal, Detail: "a transport bug"}) {
		t.Errorf("WaitClosed() = %v, %t; want the transport's panic", reason, ok)
	}
	if link.Status().CallMayStillBeUp || control.Stats().InDataCall {
		t.Error("the call was not hung up")
	}
	if err := link.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	expectCommandMode(t, handset)
}

func TestALookupThatTimesOutIsAbandoned(t *testing.T) {
	t.Parallel()
	handset, _ := simtest.NewHandset(t, delayedLink(200*time.Millisecond))
	link := establish(t, handset, linkConfig(t, "echo.test:7"))

	_, err := link.Resolve("echo.test", 100*time.Millisecond)

	expectTimedOut(t, err, "DNS lookup")
	// Abandoned before Resolve returned: the stack no longer asks for it.
	if pending := satnet.PendingLookups(link); pending != 0 {
		t.Errorf("%d lookups are still under way after the timeout", pending)
	}
	if addresses, err := link.Resolve("echo.test", timeout); err != nil || !slices.Equal(addresses, []netip.Addr{echoAddress}) {
		t.Errorf("a later Resolve = %v, %v", addresses, err)
	}
}

func TestDialUDPFollowsThePolicyAndTheLink(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:7")

	_, err := link.DialUDP("echo.test", 53, timeout)
	expectKind(t, err, satnet.ErrPolicyDenied)
	_, err = link.DialUDP("bad name", echoPort, timeout)
	expectKind(t, err, satnet.ErrPolicyDenied)
	link.RequestClose()
	expectClosed(t, link.Watcher(), satnet.CloseReasonRequested)
	_, err = link.DialUDP("echo.test", echoPort, timeout)
	expectLinkDown(t, err, satnet.CloseReasonRequested)
}

func TestABareAddressIsNotDialledOnceTheLinkIsClosed(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "192.0.2.7:7")

	link.RequestClose()

	expectClosed(t, link.Watcher(), satnet.CloseReasonRequested)
	_, err := link.Dial("192.0.2.7", echoPort, timeout)
	expectLinkDown(t, err, satnet.CloseReasonRequested)
	_, err = link.DialUDP("192.0.2.7", echoPort, timeout)
	expectLinkDown(t, err, satnet.CloseReasonRequested)
}

func TestAnInvalidNameIsNeverLookedUp(t *testing.T) {
	t.Parallel()
	link, control := newLink(t, "echo.test:7")
	before := control.Stats().DataBytesFromApp

	_, err := link.Resolve("bad name", timeout)

	var linkErr *satnet.Error
	if !errors.As(err, &linkErr) || linkErr.Kind != satnet.ErrInvalidHost || linkErr.Host != "bad name" {
		t.Errorf("Resolve = %v, want the invalid name refused", err)
	}
	expectNothingSentSince(t, link, control, before)
}

// TestARefusedDestinationGetsNothingOnTheLine checks that the policy is
// checked before anything is sent, so that a refused name is never looked
// up — a name can carry data — and a refused address never gets a SYN. Each
// case has a link of its own: a lookup that leaked would leave its answer
// cached for the next.
func TestARefusedDestinationGetsNothingOnTheLine(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		refuse func(link *satnet.Link) error
	}{
		{"Resolve of a refused name", func(link *satnet.Link) error {
			_, err := link.Resolve("web.test", timeout)
			return err
		}},
		{"DialContext to a refused name", func(link *satnet.Link) error {
			_, err := link.DialContext(context.Background(), "tcp", "web.test:7")
			return err
		}},
		{"Dial to an allowed name on a refused port", func(link *satnet.Link) error {
			_, err := link.Dial("echo.test", webPort, timeout)
			return err
		}},
		{"DialUDP to an allowed name on a refused port", func(link *satnet.Link) error {
			_, err := link.DialUDP("echo.test", 53, timeout)
			return err
		}},
		{"Dial to a refused address", func(link *satnet.Link) error {
			_, err := link.Dial("192.0.2.7", echoPort, timeout)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			link, control := newLink(t, "echo.test:7")
			before := control.Stats().DataBytesFromApp

			err := c.refuse(link)

			expectKind(t, err, satnet.ErrPolicyDenied)
			expectNothingSentSince(t, link, control, before)
		})
	}
}

// expectNothingSentSince checks that the app has sent nothing on the line
// since control's simulator counted before bytes from it, and that no lookup
// is under way that would send a query later.
func expectNothingSentSince(t *testing.T, link *satnet.Link, control *sim.Control, before uint64) {
	t.Helper()
	if sent := control.Stats().DataBytesFromApp - before; sent != 0 {
		t.Errorf("%d bytes went on the line, want none", sent)
	}
	if pending := satnet.PendingLookups(link); pending != 0 {
		t.Errorf("%d lookups are under way, want none", pending)
	}
}

// expectConnectionFailed checks that err says the connection to address
// failed.
func expectConnectionFailed(t *testing.T, err error, address netip.AddrPort) {
	t.Helper()
	var linkErr *satnet.Error
	if !errors.As(err, &linkErr) || linkErr.Kind != satnet.ErrConnectionFailed || linkErr.Addr != address {
		t.Errorf("err = %v, want the connection to %v failed", err, address)
	}
}

// refusedPort is a port where no service of the simulated network listens:
// a connection to it is refused.
const refusedPort = 8

func TestARefusedConnectionFailsWithItsAddress(t *testing.T) {
	t.Parallel()
	link, _ := newLink(t, "echo.test:8")

	_, err := link.Dial("echo.test", refusedPort, timeout)

	expectConnectionFailed(t, err, netip.AddrPortFrom(echoAddress, refusedPort))
}

// multiHost is a name the simulated network of simulatorWithTwoAddresses
// resolves to two addresses.
const multiHost = "multi.test"

// simulatorWithTwoAddresses returns the tests' simulator configuration, in
// which multiHost resolves to first, then second.
func simulatorWithTwoAddresses(first, second netip.Addr) sim.Config {
	config := simtest.SimulatorConfig()
	config.Network.Hosts = append(config.Network.Hosts,
		sim.HostAddress{Name: multiHost, Address: first},
		sim.HostAddress{Name: multiHost, Address: second})
	return config
}

func TestEveryAddressRefusingReportsTheLastOneTried(t *testing.T) {
	t.Parallel()
	first, last := netip.AddrFrom4([4]byte{192, 0, 2, 50}), netip.AddrFrom4([4]byte{192, 0, 2, 51})
	handset, _ := simtest.NewHandset(t, simulatorWithTwoAddresses(first, last))
	link := establish(t, handset, linkConfig(t, "multi.test:8"))

	_, err := link.Dial(multiHost, refusedPort, timeout)

	expectConnectionFailed(t, err, netip.AddrPortFrom(last, refusedPort))
}

func TestADialGoesOnToTheNextAddress(t *testing.T) {
	t.Parallel()
	silent := netip.AddrFrom4([4]byte{192, 0, 2, 99})
	simulator := simulatorWithTwoAddresses(silent, echoAddress)
	simulator.Network.SilentAddresses = []netip.Addr{silent}
	handset, _ := simtest.NewHandset(t, simulator)
	config := linkConfig(t, "multi.test:7")
	// The connection to the silent address times out by itself, as TCP
	// gives up on it — not the dial, whose timeout ends the attempts.
	config.Tuning.TCPTimeout = 2 * time.Second
	link := establish(t, handset, config)

	conn := dial(t, link, multiHost, echoPort)

	if conn.PeerAddr() != netip.AddrPortFrom(echoAddress, echoPort) {
		t.Errorf("PeerAddr() = %v, want the second address", conn.PeerAddr())
	}
	expectEcho(t, conn, []byte("from the second address"))
}

func TestTheLinkKeepsTheHostsItWasGiven(t *testing.T) {
	t.Parallel()
	handset, control := simtest.NewHandset(t, simulatorWith(sim.LinkProfile{CallSetup: time.Second}))
	config := linkConfig(t, "relay.example:7")
	config.Hosts["relay.example"] = echoAddress
	established := establishInBackground(handset, config)
	simtest.Eventually(t, timeout, "the dial is under way", func() bool {
		return control.Stats().CallsPlaced == 1
	})

	// The app's map, which its own copy of the configuration still shares,
	// changes while the call is set up.
	config.Hosts["relay.example"] = netip.AddrFrom4([4]byte{192, 0, 2, 99})

	link := awaitLink(t, established)
	if addresses, err := link.Resolve("relay.example", timeout); err != nil || !slices.Equal(addresses, []netip.Addr{echoAddress}) {
		t.Errorf("Resolve(relay.example) = %v, %v; want the address the link was given", addresses, err)
	}
}

// TestTheCloseGraceBoundsARequestedClose runs serially, as it times the
// close.
func TestTheCloseGraceBoundsARequestedClose(t *testing.T) {
	config := linkConfig(t, "echo.test:9")
	config.Tuning.CloseGrace = time.Second
	link, _ := linkWith(t, config)
	conn := dial(t, link, "echo.test", discardPort)
	// Over thirteen seconds of data at 300 B/s: far more than the grace.
	send(t, conn, make([]byte, 4_000))
	started := time.Now()

	link.RequestClose()

	if reason, ok := link.Watcher().WaitClosed(5 * time.Second); !ok || reason.Kind != satnet.CloseReasonRequested {
		t.Fatalf("WaitClosed() = %v, %t; want the link closed once the grace is over", reason, ok)
	}
	if elapsed := time.Since(started); elapsed < config.Tuning.CloseGrace {
		t.Errorf("the link closed %v after the request, sooner than its grace %v", elapsed, config.Tuning.CloseGrace)
	}
}

// TestTheCallsDurationCountsItsHangUp runs serially, as it times the call.
func TestTheCallsDurationCountsItsHangUp(t *testing.T) {
	config := linkConfig(t)
	config.Call.EscapeGuard = billingGuard
	link, _ := linkWith(t, config)
	established := time.Now()

	if err := link.Close(); err != nil {
		t.Fatal(err)
	}

	expectTheHangUpBilled(t, link, established)
}
