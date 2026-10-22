package satnet_test

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/satnet"
	"github.com/anhydrous99/satlink/sim"
)

// What the external tests share: links over the simulated handset, and
// reading and writing with deadlines.

// timeout bounds every operation the tests expect to succeed.
const timeout = 10 * time.Second

// The simulated network's services: every address serves echo and
// discard (RFC 862, RFC 863), which counts what it receives.
const (
	echoPort    = 7
	discardPort = 9
	webPort     = 80
)

// echoAddress is echo.test's address.
var echoAddress = netip.AddrFrom4([4]byte{192, 0, 2, 7})

// briefCloseGrace is the close grace of links whose tests end with data
// still queued that need not arrive. The tests' cleanup waits for the
// hang-up, which would otherwise wait for that data for the default 30 s;
// the tests dropped the link instead, which hangs up in the
// background.
const briefCloseGrace = time.Second

// linkClosingBriefly returns a link allowing rules, over a new simulated
// handset, whose close waits at most briefCloseGrace for queued data.
func linkClosingBriefly(t *testing.T, rules ...string) *satnet.Link {
	t.Helper()
	config := linkConfig(t, rules...)
	config.Tuning.CloseGrace = briefCloseGrace
	link, _ := linkWith(t, config)
	return link
}

// linkConfig returns a configuration allowing rules, with an escape guard
// above the simulator's and a connect timeout of timeout.
func linkConfig(t *testing.T, rules ...string) satnet.Config {
	t.Helper()
	policy, err := satnet.ParsePolicy(rules...)
	if err != nil {
		t.Fatalf("ParsePolicy(%q): %v", rules, err)
	}
	config := satnet.NewConfig(policy)
	config.Call.EscapeGuard = simtest.CallEscapeGuard
	config.Call.ConnectTimeout = timeout
	return config
}

// simulatorWith returns the tests' simulator configuration with link's
// timing.
func simulatorWith(link sim.LinkProfile) sim.Config {
	config := simtest.SimulatorConfig()
	config.Link = link
	return config
}

// delayedLink is a satellite round trip without Iridium's throughput
// limit.
func delayedLink(oneWayDelay time.Duration) sim.Config {
	return simulatorWith(sim.LinkProfile{OneWayDelay: oneWayDelay})
}

// establish brings a link up over handset, failing the test otherwise. The
// test's end closes it, before the simulator closes.
func establish(t *testing.T, handset *satlink.Handset, config satnet.Config) *satnet.Link {
	t.Helper()
	link, err := satnet.Establish(handset, config)
	if err != nil {
		t.Fatalf("the link does not come up: %v", err)
	}
	t.Cleanup(func() {
		// What the test needed is checked; a call this hang-up leaves up
		// ends with the simulator's Close.
		_ = link.Close()
	})
	return link
}

// linkWith returns a link with config over a new simulated handset, and the
// simulator's control.
func linkWith(t *testing.T, config satnet.Config) (*satnet.Link, *sim.Control) {
	t.Helper()
	handset, control := simtest.NewHandset(t, simtest.SimulatorConfig())
	return establish(t, handset, config), control
}

// newLink returns a link allowing rules over a new simulated handset, and
// the simulator's control.
func newLink(t *testing.T, rules ...string) (*satnet.Link, *sim.Control) {
	t.Helper()
	return linkWith(t, linkConfig(t, rules...))
}

// linkWithASilentDNSServer returns a link allowing rules over a new
// simulated handset whose network offers only a DNS server that never
// answers: the stack gives it ten seconds before a lookup times out, which
// is worth retrying.
func linkWithASilentDNSServer(t *testing.T, rules ...string) *satnet.Link {
	t.Helper()
	silent := netip.AddrFrom4([4]byte{10, 64, 0, 99})
	simulator := simtest.SimulatorConfig()
	simulator.Network.DNSServers = []netip.Addr{silent}
	simulator.Network.SilentAddresses = []netip.Addr{silent}
	handset, _ := simtest.NewHandset(t, simulator)
	return establish(t, handset, linkConfig(t, rules...))
}

// faultyHandset returns a handset over a simulator made with config,
// behind a transport that fails on cue, with a short timeout so that a
// hang-up the handset ignores fails fast.
func faultyHandset(t *testing.T, config sim.Config) (*satlink.Handset, *sim.Control, *simtest.FaultyTransport) {
	t.Helper()
	simulator := simtest.NewSimulator(t, config)
	faulty := simtest.NewFaultyTransport(simulator)
	handset := simtest.InitializedHandset(t, faulty)
	handset.SetTimeout(500 * time.Millisecond)
	return handset, simulator.Control(), faulty
}

// faultyLink returns a link over a faulty handset.
func faultyLink(t *testing.T) (*satnet.Link, *satlink.Handset, *sim.Control, *simtest.FaultyTransport) {
	t.Helper()
	handset, control, faulty := faultyHandset(t, simtest.SimulatorConfig())
	return establish(t, handset, linkConfig(t)), handset, control, faulty
}

// dial opens a TCP connection to host on port, failing the test otherwise;
// the test's end closes it.
func dial(t *testing.T, link *satnet.Link, host string, port uint16) *satnet.TCPConn {
	t.Helper()
	conn, err := link.Dial(host, port, timeout)
	if err != nil {
		t.Fatalf("Dial(%s:%d): %v", host, port, err)
	}
	t.Cleanup(func() {
		// Closing a TCPConn cannot fail.
		_ = conn.Close()
	})
	return conn
}

// dialUDP opens a UDP socket for host on port, failing the test otherwise;
// the test's end closes it.
func dialUDP(t *testing.T, link *satnet.Link, host string, port uint16) *satnet.UDPConn {
	t.Helper()
	conn, err := link.DialUDP(host, port, timeout)
	if err != nil {
		t.Fatalf("DialUDP(%s:%d): %v", host, port, err)
	}
	t.Cleanup(func() {
		// Closing a UDPConn cannot fail.
		_ = conn.Close()
	})
	return conn
}

// send writes all of data within timeout, failing the test otherwise.
func send(t *testing.T, conn net.Conn, data []byte) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("writing %d bytes: %v", len(data), err)
	}
}

// receiveExactly reads n bytes, waiting at most timeout for each read,
// failing the test otherwise.
func receiveExactly(t *testing.T, conn net.Conn, n int) []byte {
	t.Helper()
	received, err := readExactly(conn, n)
	if err != nil {
		t.Fatal(err)
	}
	return received
}

// readExactly reads n bytes, waiting at most timeout for each read; it may
// run outside the test's goroutine.
func readExactly(conn net.Conn, n int) ([]byte, error) {
	received := make([]byte, n)
	for filled := 0; filled < n; {
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return nil, err
		}
		count, err := conn.Read(received[filled:])
		if err != nil {
			return nil, fmt.Errorf("the stream ended after %d bytes: %w", filled, err)
		}
		filled += count
	}
	return received, nil
}

// expectEcho sends message and expects it back.
func expectEcho(t *testing.T, conn net.Conn, message []byte) {
	t.Helper()
	send(t, conn, message)
	if echoed := receiveExactly(t, conn, len(message)); !bytes.Equal(echoed, message) {
		t.Errorf("echoed %q, want %q", echoed, message)
	}
}

// billingGuard is the escape guard of the links whose tests check that
// their hang-up is billed: the hang-up waits a guard after the escape, far
// longer than Close takes to return once the call has ended.
const billingGuard = 500 * time.Millisecond

// expectTheHangUpBilled checks that the airtime of link, whose escape guard
// is billingGuard, counts its hang-up: it runs from CONNECT, before
// Establish returned at established, to the end of the hang-up, which Close
// returned after. Half a guard leaves Close time enough to return.
func expectTheHangUpBilled(t *testing.T, link *satnet.Link, established time.Time) {
	t.Helper()
	billed := link.Status().Usage.ConnectedFor
	if closedAfter := time.Since(established); billed < closedAfter-billingGuard/2 {
		t.Errorf("the call lasted %v, though Close returned %v after Establish: the hang-up is left out",
			billed, closedAfter)
	}
}

// expectLinkDown checks that err is ErrLinkDown for reason.
func expectLinkDown(t *testing.T, err error, reason satnet.CloseReasonKind) {
	t.Helper()
	var linkErr *satnet.Error
	if !errors.As(err, &linkErr) || linkErr.Kind != satnet.ErrLinkDown || linkErr.Reason.Kind != reason {
		t.Errorf("err = %v, want the link down for %v", err, reason)
	}
}

// expectTimedOut checks that err is the ErrTimedOut of operation.
func expectTimedOut(t *testing.T, err error, operation string) {
	t.Helper()
	var timedOut *satnet.Error
	if !errors.As(err, &timedOut) || timedOut.Kind != satnet.ErrTimedOut || timedOut.Operation != operation {
		t.Errorf("err = %v, want the %s timed out", err, operation)
	}
}

// expectKind checks that err is a satnet error of kind.
func expectKind(t *testing.T, err error, kind satnet.ErrorKind) {
	t.Helper()
	var linkErr *satnet.Error
	if !errors.As(err, &linkErr) || linkErr.Kind != kind {
		t.Errorf("err = %v, want %v", err, kind)
	}
}

// expectNotBusy checks that the handset no longer serves a data call: its
// methods may fail, but not with satlink.ErrBusy.
func expectNotBusy(t *testing.T, handset *satlink.Handset) {
	t.Helper()
	if _, err := handset.SignalQuality(); errors.Is(err, satlink.ErrBusy) {
		t.Errorf("the handset is still busy: %v", err)
	}
}

// expectCommandMode checks that the handset answers AT again.
func expectCommandMode(t *testing.T, handset *satlink.Handset) {
	t.Helper()
	if _, err := handset.Command("AT"); err != nil {
		t.Errorf("AT commands fail: %v", err)
	}
}

// heartbeat is a short message written while a bulk write waits, as an
// app's heartbeat goroutine might.
var heartbeat = []byte("<heartbeat>")

// bulkUnderWay is how much of a bulk write has left the app when the
// heartbeat is written: twice the default TCP send buffer, so that the bulk
// has had to wait for room, and room has come since.
const bulkUnderWay = 8 * 1024

// expectAHeartbeatAfterTheBulk writes heartbeat on conn while a write of
// bulk, more than conn takes at once, keeps the line of control's simulator
// busy — room keeps coming as it does, which a second write could take —
// and checks that the echo carries the whole bulk, then the heartbeat: each
// write goes out whole, as a net.TCPConn's does.
func expectAHeartbeatAfterTheBulk(t *testing.T, control *sim.Control, conn net.Conn, bulk []byte) {
	t.Helper()
	before := control.Stats().DataBytesFromApp
	written := inBackground(func() error {
		_, err := conn.Write(bulk)
		return err
	})
	simtest.Eventually(t, timeout, "the bulk keeps the line busy", func() bool {
		return control.Stats().DataBytesFromApp >= before+bulkUnderWay
	})
	beat := inBackground(func() error {
		_, err := conn.Write(heartbeat)
		return err
	})

	echoed := receiveExactly(t, conn, len(bulk)+len(heartbeat))

	for _, err := range []error{awaitError(t, written), awaitError(t, beat)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(echoed, slices.Concat(bulk, heartbeat)) {
		t.Errorf("the heartbeat came back at byte %d, inside the bulk of %d", bytes.Index(echoed, heartbeat), len(bulk))
	}
}

// pattern returns n bytes counting up modulo 251, so that a lost or
// repeated chunk shows.
func pattern(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}
