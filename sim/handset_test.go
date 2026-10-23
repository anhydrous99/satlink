package sim

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/anhydrous99/satlink/iridium"
)

// The handset as a transport, its Close, and I/O after Close.

// handsetPatience bounds the waits for what the simulated handset does at
// once; a test that reaches it has failed.
const handsetPatience = 5 * time.Second

// promisedPollInterval is how often a waiting ReadTimeout advances the
// simulation, as its doc promises: every 2 ms, which keeps simulated latency
// and pacing accurate to a few milliseconds.
const promisedPollInterval = 2 * time.Millisecond

// readUntilQuiet reads from handset until it has nothing more to say.
func readUntilQuiet(t *testing.T, handset *Handset) string {
	t.Helper()
	var received strings.Builder
	buffer := make([]byte, 512)
	for {
		count, err := handset.ReadTimeout(buffer, 20*time.Millisecond)
		if err != nil {
			t.Fatalf("ReadTimeout: %v", err)
		}
		if count == 0 {
			return received.String()
		}
		received.Write(buffer[:count])
	}
}

// readUntilContains reads from handset until want has arrived.
func readUntilContains(t *testing.T, handset *Handset, want string) {
	t.Helper()
	deadline := time.Now().Add(handsetPatience)
	var received strings.Builder
	buffer := make([]byte, 512)
	for !strings.Contains(received.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("never received %q, only %q", want, received.String())
		}
		count, err := handset.ReadTimeout(buffer, 5*time.Millisecond)
		if err != nil {
			t.Fatalf("ReadTimeout: %v", err)
		}
		received.Write(buffer[:count])
	}
}

// assertClosedError fails the test unless err is the closed handset's.
func assertClosedError(t *testing.T, err error) {
	t.Helper()
	var handsetErr *iridium.Error
	if !errors.As(err, &handsetErr) || handsetErr.Kind != iridium.ErrTransport || handsetErr.Message != closedMessage {
		t.Errorf("got %v; want a transport error %q", err, closedMessage)
	}
}

func TestTheHandsetAnswersCommandsWrittenToIt(t *testing.T) {
	t.Parallel()
	handset := New(DefaultConfig())

	if err := handset.WriteAll([]byte("AT\r")); err != nil {
		t.Fatal(err)
	}

	if got := readUntilQuiet(t, handset); got != "AT\r\r\nOK\r\n" {
		t.Errorf("the handset said %q", got)
	}
}

func TestReadTimeoutReturnsNothingOnceItsTimeoutHasPassed(t *testing.T) {
	t.Parallel()
	handset := New(DefaultConfig())
	start := time.Now()

	count, err := handset.ReadTimeout(make([]byte, 16), 30*time.Millisecond)

	if count != 0 || err != nil {
		t.Errorf("ReadTimeout = %d, %v; want 0, nil", count, err)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("gave up after %v; want at least the 30ms timeout", elapsed)
	}
}

func TestReadTimeoutWithoutRoomOrTimeReturnsAtOnce(t *testing.T) {
	t.Parallel()
	handset := New(DefaultConfig())
	start := time.Now()

	emptyCount, emptyErr := handset.ReadTimeout(nil, time.Hour)
	noTimeCount, noTimeErr := handset.ReadTimeout(make([]byte, 16), -time.Second)

	if emptyCount != 0 || emptyErr != nil || noTimeCount != 0 || noTimeErr != nil {
		t.Errorf("ReadTimeout = %d, %v and %d, %v; want nothing, at once", emptyCount, emptyErr, noTimeCount, noTimeErr)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v", elapsed)
	}
}

func TestIOFailsOnceTheHandsetIsClosed(t *testing.T) {
	t.Parallel()
	handset := New(DefaultConfig())
	if err := handset.Close(); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"WriteAll": func() error { return handset.WriteAll([]byte("AT\r")) },
		"ReadTimeout": func() error {
			_, err := handset.ReadTimeout(make([]byte, 8), time.Second)
			return err
		},
		"RaiseDTR": handset.RaiseDTR,
		"DropDTR":  handset.DropDTR,
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			assertClosedError(t, call())
		})
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	handset := New(DefaultConfig())

	first, second := handset.Close(), handset.Close()

	if first != nil || second != nil {
		t.Errorf("Close, Close = %v, %v; want nil, nil", first, second)
	}
}

// The read must be waiting when Close comes, or it would fail at its first
// check and the wake-up would go untested. In a synctest bubble, synctest.Wait returns once the read sleeps
// between two polls, and time passes only while every goroutine waits, so
// the read ends exactly one poll interval after Close.
func TestCloseEndsAReadInProgress(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		handset := New(DefaultConfig())
		results := make(chan error, 1)
		go func() {
			// Finite, so that a read that Close fails to end still
			// returns and the bubble can end.
			_, err := handset.ReadTimeout(make([]byte, 8), handsetPatience)
			results <- err
		}()
		synctest.Wait()

		closedAt := time.Now()
		if err := handset.Close(); err != nil {
			t.Error(err)
		}

		assertClosedError(t, <-results)
		if waited := time.Since(closedAt); waited > promisedPollInterval {
			t.Errorf("the read went on for %v after Close; want it to end at its next poll, within %v", waited, promisedPollInterval)
		}
	})
}

func TestTheControlKeepsWorkingAfterClose(t *testing.T) {
	t.Parallel()
	handset := New(DefaultConfig())
	control := handset.Control()
	if err := handset.Close(); err != nil {
		t.Fatal(err)
	}

	if err := control.ReceiveSMS("+1", "after the port closed"); err != nil {
		t.Fatalf("ReceiveSMS: %v", err)
	}
	control.DropCall()
	stats := control.Stats()

	if stats.CallsPlaced != 0 || stats.InDataCall {
		t.Errorf("stats %+v; want a handset that did nothing", stats)
	}
}

func TestStatsAreACopyForTheCallerToKeep(t *testing.T) {
	t.Parallel()
	handset := New(DefaultConfig())
	control := handset.Control()
	if err := handset.WriteAll([]byte("ATE0\rAT+CMGS=11\r" + hiSubmitPDU + "\x1a")); err != nil {
		t.Fatal(err)
	}
	readUntilQuiet(t, handset)

	stats := control.Stats()
	stats.SMSSent[0].Text = "changed"

	if got := control.Stats().SMSSent; len(got) != 1 || got[0] != (SentSMS{Number: "+1", Text: "hi"}) {
		t.Errorf("the handset's record became %+v", got)
	}
}

func TestNewKeepsACopyOfTheConfiguration(t *testing.T) {
	t.Parallel()
	config := DefaultConfig()
	config.Network.SilentAddresses = []netip.Addr{netip.MustParseAddr("192.0.2.99")}
	config.Authentication = &Authentication{Credentials: Credentials{Username: "pilot"}}
	handset := New(config)

	config.Network.DNSServers[0] = netip.MustParseAddr("10.64.0.99")
	config.Network.SilentAddresses[0] = netip.MustParseAddr("192.0.2.98")
	config.Network.Hosts[0].Name = "changed.test"
	config.Authentication.Credentials.Username = "changed"

	kept := handset.shared.locked.modem.config
	if kept.Network.DNSServers[0] != defaultGatewayAddress || kept.Network.SilentAddresses[0] != netip.MustParseAddr("192.0.2.99") ||
		kept.Network.Hosts[0].Name != "echo.test" || kept.Authentication.Credentials.Username != "pilot" {
		t.Errorf("the handset's configuration changed with the caller's: %+v", kept)
	}
}

func TestClosingThePortEndsACallAfterAndD2(t *testing.T) {
	t.Parallel()
	handset := New(DefaultConfig())
	control := handset.Control()
	if err := handset.WriteAll([]byte("ATE0\rAT&D2\rATD" + iridiumInternet + "\r")); err != nil {
		t.Fatal(err)
	}
	readUntilContains(t, handset, "CONNECT 19200")

	if err := handset.Close(); err != nil {
		t.Fatal(err)
	}

	if control.Stats().InDataCall {
		t.Error("the call is still up after the port closed")
	}
}

func TestADataCallToAnUnusableNetworkEndsWithNoCarrier(t *testing.T) {
	t.Parallel()
	config := DefaultConfig()
	config.Network = NetworkConfig{}
	handset := New(config)
	if err := handset.WriteAll([]byte("ATE0\rATD" + iridiumInternet + "\r")); err != nil {
		t.Fatal(err)
	}

	readUntilContains(t, handset, "NO CARRIER")

	if handset.Control().Stats().CallsConnected != 0 {
		t.Error("the call connected")
	}
}

func TestDroppingDTREndsACallOnlyAfterAndD2(t *testing.T) {
	t.Parallel()
	for _, setting := range []string{"AT&D0", "AT&D2"} {
		handset := New(DefaultConfig())
		control := handset.Control()
		if err := handset.WriteAll([]byte("ATE0\r" + setting + "\rATD" + iridiumInternet + "\r")); err != nil {
			t.Fatal(err)
		}
		readUntilContains(t, handset, "CONNECT 19200")

		if err := handset.DropDTR(); err != nil {
			t.Fatal(err)
		}

		if inCall, want := control.Stats().InDataCall, setting == "AT&D0"; inCall != want {
			t.Errorf("after %s, the call is up: %v; want %v", setting, inCall, want)
		}
	}
}

func TestRaisingDTRChangesNothing(t *testing.T) {
	t.Parallel()
	handset := New(DefaultConfig())

	if err := handset.RaiseDTR(); err != nil {
		t.Fatal(err)
	}

	if err := handset.WriteAll([]byte("AT\r")); err != nil {
		t.Fatal(err)
	}
	if got := readUntilQuiet(t, handset); got != "AT\r\r\nOK\r\n" {
		t.Errorf("the handset said %q", got)
	}
}

// A Handset that nobody refers to any more drops DTR, once the garbage
// collector has noticed.
func TestAHandsetCollectedWithoutCloseDropsDTR(t *testing.T) {
	t.Parallel()

	control := callUpAndLetGoOfTheHandset(t)

	awaitAfterCollecting(t, func() bool { return !control.Stats().InDataCall }, func() string {
		return "the call is still up with the handset gone"
	})
}

// callUpAndLetGoOfTheHandset dials a data call that ends when DTR drops,
// and lets go of the handset: only its control is left.
func callUpAndLetGoOfTheHandset(t *testing.T) *Control {
	t.Helper()
	handset := New(DefaultConfig())
	if err := handset.WriteAll([]byte("ATE0\rAT&D2\rATD" + iridiumInternet + "\r")); err != nil {
		t.Fatal(err)
	}
	readUntilContains(t, handset, "CONNECT 19200")
	control := handset.Control()
	if !control.Stats().InDataCall {
		t.Fatal("the call is not up")
	}
	return control
}
