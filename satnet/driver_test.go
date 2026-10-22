package satnet

import (
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
	"github.com/anhydrous99/satlink/ppp"
)

// cannotDescribeItself is an error whose Error method fails on a nil
// receiver, as a careless transport's might.
type cannotDescribeItself struct {
	text string
}

func (e *cannotDescribeItself) Error() string { return e.text }

func TestPanicsAreDescribedByWhatTheyCarry(t *testing.T) {
	for _, c := range []struct {
		recovered any
		message   string
	}{
		{"a transport bug", "a transport bug"},
		{errors.New("runtime trouble"), "runtime trouble"},
		{42, "the link driver panicked"},
		// Describing it would panic again, while a panic is recovered.
		{(*cannotDescribeItself)(nil), "the link driver panicked"},
	} {
		if got := panicMessage(c.recovered); got != c.message {
			t.Errorf("panicMessage(%v) = %q, want %q", c.recovered, got, c.message)
		}
	}
}

// clientEndpoint returns a PPP client endpoint that has not opened.
func clientEndpoint(t *testing.T) *ppp.Endpoint {
	t.Helper()
	endpoint, err := ppp.NewEndpoint(ppp.NewConfig(ppp.ClientRole(ppp.Credentials{})))
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

// maxPPPRounds bounds the rounds of frames that bring PPP up between two
// endpoints talking straight to each other.
const maxPPPRounds = 100

// openEndpoint returns a PPP client endpoint open for IPv4, with the
// default MTU: it came up with a server that talks straight to it and asks
// for no authentication.
func openEndpoint(t *testing.T) *ppp.Endpoint {
	t.Helper()
	now := time.Now()
	client := clientEndpoint(t)
	server, err := ppp.NewEndpoint(ppp.NewConfig(ppp.ServerRole(ppp.ServerConfig{
		LocalAddress: ipv4(10, 64, 0, 1),
		PeerAddress:  clientSettings().LocalAddress,
	})))
	if err != nil {
		t.Fatal(err)
	}
	client.Open(now)
	server.Open(now)
	for range maxPPPRounds {
		if client.Phase() == ppp.PhaseOpen {
			return client
		}
		toServer, toClient := client.TakeOutput(), server.TakeOutput()
		server.HandleInput(toServer, now)
		client.HandleInput(toClient, now)
	}
	t.Fatal("PPP did not come up")
	return nil
}

func TestADatagramPPPCannotSendIsDroppedAndLogged(t *testing.T) {
	records := recordLogs(t, slog.LevelDebug)
	d := &driver{ppp: clientEndpoint(t)}

	// PPP is not open: renegotiating, as far as the driver can tell.
	d.sendDatagram(segment(1, wire.TCPControlPSH, "data"))

	got := records()
	if len(got) != 1 || got[0].level != slog.LevelDebug || got[0].message != "datagram dropped: the link is not open for IPv4" {
		t.Errorf("records %v", got)
	}
}

func TestADatagramTooLargeForThePeerIsDroppedWithAWarning(t *testing.T) {
	records := recordLogs(t, slog.LevelDebug)
	d := &driver{ppp: openEndpoint(t)}

	// The stack sizes its packets to fit: one that does not is a bug worth
	// a warning.
	d.sendDatagram(make([]byte, ppp.DefaultMRU+1))

	want := logRecord{slog.LevelWarn, logPackage, "datagram dropped: a 1501-byte datagram exceeds the peer's MRU of 1500"}
	if got := records(); !slices.Equal(got, []logRecord{want}) {
		t.Errorf("records %v, want %v", got, want)
	}
}

func TestDataLeavesForPPPOnlyWhenThePacedLineCanTakeIt(t *testing.T) {
	now := time.Now()
	e := engineUpAt(t, now)
	handle, _, _ := connected(t, e.stack, now)
	if _, err := e.stack.sendTCP(handle, []byte("paced")); err != nil {
		t.Fatal(err)
	}
	endpoint := openEndpoint(t)
	d := &driver{ppp: endpoint, pacer: lineBusyForASecond(now), repeats: newRepeatFilter(DefaultTuning().RepeatSuppression)}
	before := endpoint.Stats().FramesSent

	d.exchangeDatagrams(e, now)
	whileBusy := endpoint.Stats().FramesSent
	d.exchangeDatagrams(e, now.Add(time.Second))
	onceReady := endpoint.Stats().FramesSent

	// The stack chooses what goes at the last moment: nothing waits in
	// front of the line to go stale.
	if whileBusy != before {
		t.Errorf("%d frames went while the line was still sending what it was given", whileBusy-before)
	}
	if onceReady == whileBusy {
		t.Error("the data did not go once the line could take it")
	}
}

func TestIPv4UpIsLoggedAsTheRustVersionLoggedIt(t *testing.T) {
	records := recordLogs(t, slog.LevelInfo)
	d := &driver{ppp: clientEndpoint(t), tuning: DefaultTuning()}
	now := time.Now()
	settings := IPv4Settings{
		LocalAddress: netip.MustParseAddr("10.64.0.2"),
		PeerAddress:  netip.MustParseAddr("10.64.0.1"),
		DNSServers:   []netip.Addr{netip.MustParseAddr("10.64.0.1")},
	}

	if reason, failed := d.ipv4Up(testEngine(now), settings, now); failed {
		t.Fatalf("ipv4Up failed: %v", reason)
	}

	want := logRecord{slog.LevelInfo, logPackage,
		"IPv4 up: Ipv4Settings { local_address: 10.64.0.2, peer_address: Some(10.64.0.1), dns_servers: [10.64.0.1] }"}
	if got := records(); !slices.Equal(got, []logRecord{want}) {
		t.Errorf("records %v, want %v", got, want)
	}
}

func TestAnAddressTheStackCannotUseFailsIPCP(t *testing.T) {
	// The "IPv4 up" line goes to the recorder rather than the test's output.
	recordLogs(t, slog.LevelInfo)
	now := time.Now()
	deadline := now.Add(time.Minute)
	d := &driver{ppp: clientEndpoint(t), tuning: DefaultTuning(), negotiationDeadline: deadline}

	reason, failed := d.ipv4Up(testEngine(now), IPv4Settings{LocalAddress: ipv4(255, 255, 255, 255)}, now)

	want := CloseReason{Kind: CloseReasonPPP, PPP: DownReason{
		Kind:     ppp.DownReasonNegotiationFailed,
		Protocol: "IPCP",
		Detail:   "invalid configuration: the peer assigned 255.255.255.255, which is not a host address",
	}}
	if !failed || reason != want {
		t.Errorf("ipv4Up = %v, %t; want %v", reason, failed, want)
	}
	if d.negotiationDeadline != deadline {
		t.Error("a link that did not come up stopped waiting for PPP")
	}
}

func TestALinkThatComesUpStopsWaitingForPPP(t *testing.T) {
	// The "IPv4 up" line goes to the recorder rather than the test's output.
	recordLogs(t, slog.LevelInfo)
	connected := time.Now()
	deadline := connected.Add(DefaultTuning().NegotiationTimeout)
	d := &driver{ppp: clientEndpoint(t), tuning: DefaultTuning(), connectedAt: connected, negotiationDeadline: deadline}
	e := testEngine(connected)

	if reason, failed := d.ipv4Up(e, clientSettings(), connected); failed {
		t.Fatalf("ipv4Up failed: %v", reason)
	}

	// The deadline itself is the tightest case: PPP is late from then on.
	if kind, reached := d.limitReached(e, deadline); reached {
		t.Errorf("a link that came up reached %v at the negotiation deadline: it would hang up mid-transfer", kind)
	}
}

// The limits of the limit tests: a call time just longer than the shortest
// allowed with the simulator's escape guard, and a hang-up of 2 × 60 ms + 1 s.
const (
	testCallTimeLimit = 21500 * time.Millisecond
	testHangUpTime    = 1120 * time.Millisecond
	testDataLimit     = 4000
	testIdleLimit     = 25 * time.Second
)

func TestTheCallTimeLimitStartsTheHangUpInTimeToEndTheCallByIt(t *testing.T) {
	for _, c := range []struct {
		name          string
		limits        Limits
		hangUpTime    time.Duration
		connectedFor  time.Duration
		mustHangUpNow bool
	}{
		{"just before the hang-up must start", Limits{MaxCallDuration: testCallTimeLimit}, testHangUpTime,
			testCallTimeLimit - testHangUpTime - time.Nanosecond, false},
		{"when it must start", Limits{MaxCallDuration: testCallTimeLimit}, testHangUpTime,
			testCallTimeLimit - testHangUpTime, true},
		{"a hang-up longer than the limit", Limits{MaxCallDuration: time.Second}, 2 * time.Second, 0, true},
		{"no limit", Limits{}, testHangUpTime, 1000 * time.Hour, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &driver{limits: c.limits, hangUpTime: c.hangUpTime}

			if got := d.callTimeLimitReached(Usage{ConnectedFor: c.connectedFor}); got != c.mustHangUpNow {
				t.Errorf("callTimeLimitReached after %v = %t, want %t", c.connectedFor, got, c.mustHangUpNow)
			}
		})
	}
}

func TestTheDataLimitCountsBothDirectionsToTheLastByte(t *testing.T) {
	for _, c := range []struct {
		name    string
		limits  Limits
		usage   Usage
		reached bool
	}{
		{"a byte short", Limits{MaxBytes: testDataLimit}, Usage{BytesSent: 1000, BytesReceived: 2999}, false},
		{"at the limit", Limits{MaxBytes: testDataLimit}, Usage{BytesSent: 1000, BytesReceived: 3000}, true},
		{"no limit", Limits{}, Usage{BytesSent: 1 << 40, BytesReceived: 1 << 40}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &driver{limits: c.limits}

			if got := d.dataLimitReached(c.usage); got != c.reached {
				t.Errorf("dataLimitReached(%+v) = %t, want %t", c.usage, got, c.reached)
			}
		})
	}
}

func TestOnlyALinkThatIsUpIdlesAndTrafficRestartsTheClock(t *testing.T) {
	connected := time.Now()
	idleLimit := &driver{limits: Limits{IdleTimeout: testIdleLimit}}
	up := engineUpAt(t, connected)
	draining := engineUpAt(t, connected)
	draining.beginDraining()
	busy := engineUpAt(t, connected)
	busy.recordActivity(connected.Add(20 * time.Second))
	for _, c := range []struct {
		name    string
		driver  *driver
		engine  *engine
		at      time.Duration
		reached bool
	}{
		{"still negotiating", idleLimit, testEngine(connected), testIdleLimit + 5*time.Second, false},
		{"up, just short of the limit", idleLimit, up, testIdleLimit - time.Nanosecond, false},
		{"up, at the limit", idleLimit, up, testIdleLimit, true},
		{"draining after a requested close", idleLimit, draining, testIdleLimit + 5*time.Second, false},
		{"just short of the limit after traffic", idleLimit, busy, 20*time.Second + testIdleLimit - time.Nanosecond, false},
		{"at the limit after traffic", idleLimit, busy, 20*time.Second + testIdleLimit, true},
		{"no limit", &driver{}, up, time.Hour, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.driver.idleTimeoutReached(c.engine, connected.Add(c.at)); got != c.reached {
				t.Errorf("idleTimeoutReached %v after connecting = %t, want %t", c.at, got, c.reached)
			}
		})
	}
}

func TestLimitsAreCheckedInTheRustVersionsOrder(t *testing.T) {
	connected := time.Now()
	endpoint := clientEndpoint(t)
	// The data limit counts every byte the line carries, frames or not.
	endpoint.HandleInput(make([]byte, testDataLimit), connected)
	e := engineUpAt(t, connected)
	// An hour on, every limit is reached, and the negotiation deadline too.
	now := connected.Add(time.Hour)
	every := Limits{MaxCallDuration: testCallTimeLimit, MaxBytes: testDataLimit, IdleTimeout: testIdleLimit}
	for _, c := range []struct {
		name                string
		limits              Limits
		negotiationDeadline time.Time
		want                CloseReasonKind
	}{
		{"the negotiation timeout first", every, connected.Add(DefaultTuning().NegotiationTimeout), CloseReasonNegotiationTimedOut},
		{"then the call time", every, time.Time{}, CloseReasonCallTimeLimit},
		{"then the data", Limits{MaxBytes: testDataLimit, IdleTimeout: testIdleLimit}, time.Time{}, CloseReasonDataLimit},
		{"then the idle time", Limits{IdleTimeout: testIdleLimit}, time.Time{}, CloseReasonIdleTimeout},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &driver{ppp: endpoint, limits: c.limits, connectedAt: connected,
				negotiationDeadline: c.negotiationDeadline, hangUpTime: testHangUpTime}

			if kind, reached := d.limitReached(e, now); !reached || kind != c.want {
				t.Errorf("limitReached = %v, %t; want %v", kind, reached, c.want)
			}
		})
	}
	unlimited := &driver{ppp: endpoint, connectedAt: connected}
	if kind, reached := unlimited.limitReached(e, now); reached {
		t.Errorf("a link without limits reached %v", kind)
	}
}

// lineBusyForASecond returns a pacer for 300 bytes per second, given 300
// bytes to send at now.
func lineBusyForASecond(now time.Time) *pacer {
	line := newPacer(300, now)
	line.sent(300, now)
	return line
}

func TestALinkIsDrainedOnceWhatWasWrittenArrivedAndTheLineSentAll(t *testing.T) {
	now := time.Now()
	idle := engineUpAt(t, now)
	writing := engineUpAt(t, now)
	handle, _, _ := connected(t, writing.stack, now)
	if _, err := writing.stack.sendTCP(handle, []byte("undelivered")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		engine  *engine
		at      time.Duration
		drained bool
	}{
		{"while the line still sends what it was given", idle, 0, false},
		{"once the line has sent it", idle, time.Second, true},
		{"with data still undelivered", writing, time.Second, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &driver{pacer: lineBusyForASecond(now)}

			if got := d.isDrained(c.engine, now.Add(c.at)); got != c.drained {
				t.Errorf("isDrained = %t, want %t", got, c.drained)
			}
		})
	}
}

func TestARequestedCloseWaitsForTheDrainAtMostTheCloseGrace(t *testing.T) {
	now := time.Now()
	e := engineUpAt(t, now)
	handle, _, _ := connected(t, e.stack, now)
	if _, err := e.stack.sendTCP(handle, []byte("undelivered")); err != nil {
		t.Fatal(err)
	}
	tuning := DefaultTuning()
	d := &driver{pacer: newPacer(tuning.LinkRate, now), tuning: tuning}
	grace := tuning.CloseGrace

	e.requestShutdown()

	for _, c := range []struct {
		after time.Duration
		due   bool
	}{
		{0, false},
		{grace - time.Nanosecond, false},
		{grace, true},
	} {
		if got := d.requestedCloseIsDue(e, now.Add(c.after)); got != c.due {
			t.Errorf("%v after the request: due %t, want %t", c.after, got, c.due)
		}
	}
	if e.state != closingState(CloseReasonRequested) {
		t.Errorf("state %v, want the link draining", e.state)
	}
}

func TestACloseRequestedWhilePPPNegotiatesIsDueAtOnce(t *testing.T) {
	now := time.Now()
	e := testEngine(now)
	// PPP's own frames still keep the line busy; nothing the app wrote is
	// left to deliver.
	d := &driver{pacer: lineBusyForASecond(now), tuning: DefaultTuning()}

	e.requestShutdown()

	if !d.requestedCloseIsDue(e, now) {
		t.Error("a close requested while PPP negotiates waits for the line")
	}
}

func TestACancellationStopsOnlyALinkStillComingUp(t *testing.T) {
	now := time.Now()
	negotiating := testEngine(now)
	up := engineUpAt(t, now)
	d := &driver{cancellation: satlink.NewCancellation()}
	if d.setupCancelled(negotiating) {
		t.Fatal("a setup nobody cancelled counts as cancelled")
	}

	d.cancellation.Cancel()

	if !d.setupCancelled(negotiating) {
		t.Error("a cancelled setup goes on")
	}
	if d.setupCancelled(up) {
		t.Error("a cancellation stops a link that is up")
	}
	if (&driver{}).setupCancelled(negotiating) {
		t.Error("settings without a cancellation count as cancelled")
	}
}

func TestHangUpErrorsCarryTheHandsetsFailure(t *testing.T) {
	if err := hangUpError(nil); err != nil {
		t.Errorf("hangUpError(nil) = %v", err)
	}
	if err := hangUpError(errors.New("ATH timed out")); !errors.Is(err, ErrHandset) {
		t.Errorf("hangUpError = %v, want ErrHandset", err)
	}
}
