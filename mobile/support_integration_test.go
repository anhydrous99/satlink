package mobile_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/mobile"
	"github.com/anhydrous99/satlink/sim"
)

// What the external tests share: transports an app would hand over, over
// the simulated handset; links and handsets made for a test, the links
// hung up when it ends; and ways to look at their errors. What they share
// with the internal tests — a listener that records, the waits for what a
// link does by itself, and DescribeState — is in export_test.go.

// timeoutMillis bounds each call of these tests.
const timeoutMillis = 10_000

// patience is how long a test waits for what a link does by itself, such as
// a dial getting under way, a cancelled setup ending, or a dropped link
// giving its handset back.
const patience = 10 * time.Second

// busyPatience is how long untilNotBusy waits: a link that cannot hang up
// takes a while to give up.
const busyPatience = time.Minute

// simulatedUSB returns what a Kotlin ByteTransport would be, over a new
// simulated handset, and the simulator's control.
func simulatedUSB(t *testing.T) (*simtest.ByteTransport, *sim.Control) {
	t.Helper()
	simulator := simtest.NewSimulator(t, sim.DefaultConfig())
	return simtest.NewByteTransport(simulator), simulator.Control()
}

// stubbornUSB returns a ByteTransport over a handset that ignores the
// escape, and DTR until its FaultyTransport's DeafToDTR is switched off:
// nothing the library tries can end its calls. Only the simulator's own
// Close, when the test ends, ends a call left up. The FaultyTransport's
// PanicsOnNextEscape is the Rust StubbornUsb's panic_on_escape: whoever
// sends the next escape dies.
func stubbornUSB(t *testing.T, config sim.Config) (*simtest.ByteTransport, *simtest.FaultyTransport, *sim.Control) {
	t.Helper()
	simulator := simtest.NewSimulator(t, config)
	faulty := simtest.NewFaultyTransport(simulator)
	faulty.DeafToEscape.Store(true)
	faulty.DeafToDTR.Store(true)
	return simtest.NewByteTransport(faulty), faulty, simulator.Control()
}

// noDTRUSB is a transport without control lines, over a simulated handset,
// whose FaultyTransport can lose the next ATH and hide +CPAS: the escape
// works, so a call whose ATH is lost is held in online-command mode.
type noDTRUSB struct {
	*simtest.ByteTransport
	faulty  *simtest.FaultyTransport
	control *sim.Control
}

// newNoDTRUSB returns a noDTRUSB over a new simulated handset.
func newNoDTRUSB(t *testing.T) *noDTRUSB {
	t.Helper()
	simulator := simtest.NewSimulator(t, sim.DefaultConfig())
	// Behind a bare satlink.Transport, the simulator's DTR line is out of
	// reach: DTR changes fail, as on a transport without control lines.
	faulty := simtest.NewFaultyTransport(struct{ satlink.Transport }{simulator})
	return &noDTRUSB{ByteTransport: simtest.NewByteTransport(faulty), faulty: faulty, control: simulator.Control()}
}

// dataCallThatWillNotEnd is a handset that answers a data call to ATA, then
// ignores everything — the escape, ATH, DTR — as nothing ends the call. It
// answers each command line as it is written, OK to all but ATA and
// AT+CPAS, and has no control lines.
type dataCallThatWillNotEnd struct {
	mu sync.Mutex
	// online reports that ATA brought the call up: nothing is heard after.
	online bool
	// line is the command line written so far.
	line []byte
	// replies are the answers not read yet.
	replies []byte
}

// The answers dataCallThatWillNotEnd gives, by command.
const (
	// dataCallAnswer is ATA's: a data call, where a voice call was asked for.
	dataCallAnswer = "\r\nCONNECT 9600\r\n"
	// idleActivityStatus is AT+CPAS's: no call in progress.
	idleActivityStatus = "\r\n+CPAS: 000\r\n\r\nOK\r\n"
	// commandAccepted is every other command's.
	commandAccepted = "\r\nOK\r\n"
)

// quietReadNap is how long a read of dataCallThatWillNotEnd with nothing to
// give waits at most, as a handset that says nothing lets the time pass.
const quietReadNap = 10 * time.Millisecond

func (d *dataCallThatWillNotEnd) Write(data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, b := range data {
		if d.online {
			break
		}
		if b != '\r' {
			d.line = append(d.line, b)
			continue
		}
		d.replies = append(d.replies, d.respondTo(string(d.line))...)
		d.line = d.line[:0]
	}
	return nil
}

// respondTo carries out command and returns the handset's answer to it.
func (d *dataCallThatWillNotEnd) respondTo(command string) string {
	switch command {
	case "ATA":
		d.online = true
		return dataCallAnswer
	case "AT+CPAS":
		return idleActivityStatus
	default:
		return commandAccepted
	}
}

func (d *dataCallThatWillNotEnd) Read(maxLen int, timeoutMillis int64) ([]byte, error) {
	if read := d.takeReplies(maxLen); len(read) > 0 {
		return read, nil
	}
	// Only a write brings an answer, and the handset never writes while it
	// reads: let the read's time pass, a little of it at a time.
	time.Sleep(min(time.Duration(timeoutMillis)*time.Millisecond, quietReadNap))
	return nil, nil
}

// takeReplies returns up to maxLen bytes of the answers not read yet.
func (d *dataCallThatWillNotEnd) takeReplies(maxLen int) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	count := min(len(d.replies), maxLen)
	read := slices.Clone(d.replies[:count])
	d.replies = d.replies[count:]
	return read
}

func (d *dataCallThatWillNotEnd) RaiseDTR() error {
	return errNoDTRLine
}

func (d *dataCallThatWillNotEnd) DropDTR() error {
	return errNoDTRLine
}

// errNoDTRLine is what a transport without control lines says to a DTR
// change.
var errNoDTRLine = errors.New("no DTR line")

// settingsAllowing returns the default settings, allowing rules.
func settingsAllowing(rules ...string) *mobile.LinkSettings {
	settings := mobile.NewLinkSettings()
	for _, rule := range rules {
		settings.Allow(rule)
	}
	return settings
}

// establish brings a link up over transport, hung up when the test ends.
func establish(t *testing.T, transport mobile.ByteTransport, settings *mobile.LinkSettings, listener mobile.LinkListener) *mobile.SatelliteLink {
	t.Helper()
	link, err := mobile.EstablishLink(transport, settings, listener, nil)
	if err != nil {
		t.Fatalf("EstablishLink: %v", err)
	}
	mobile.HangUpAtCleanup(t, link)
	return link
}

// newHandset returns an IridiumHandset over transport.
func newHandset(t *testing.T, transport mobile.ByteTransport) *mobile.IridiumHandset {
	t.Helper()
	handset, err := mobile.NewIridiumHandset(transport)
	if err != nil {
		t.Fatalf("NewIridiumHandset: %v", err)
	}
	return handset
}

// lend brings a link up with handset, hung up when the test ends.
func lend(t *testing.T, handset *mobile.IridiumHandset, settings *mobile.LinkSettings) *mobile.SatelliteLink {
	t.Helper()
	link, err := mobile.EstablishLinkWithHandset(handset, settings, nil, nil)
	if err != nil {
		t.Fatalf("EstablishLinkWithHandset: %v", err)
	}
	mobile.HangUpAtCleanup(t, link)
	return link
}

// kindOf returns the SatlinkError kind of err, or -1 when err is not one.
func kindOf(err error) int {
	if details := mobile.AsSatlinkError(err); details != nil {
		return details.Kind
	}
	return -1
}

// assertKind fails the test unless err is a SatlinkError of kind.
func assertKind(t *testing.T, err error, kind int) {
	t.Helper()
	if got := kindOf(err); got != kind {
		t.Fatalf("error %v has kind %d, want %d", err, got, kind)
	}
}

// untilNotBusy calls call until it stops failing with
// SatlinkErrorHandsetBusy, within busyPatience, and returns its error.
func untilNotBusy(t *testing.T, call func() error) error {
	t.Helper()
	var err error
	simtest.Eventually(t, busyPatience, "the handset is no longer busy", func() bool {
		err = call()
		return kindOf(err) != mobile.SatlinkErrorHandsetBusy
	})
	return err
}

// signalQuality is the handset call the tests use to see whether the
// handset can be used.
func signalQuality(handset *mobile.IridiumHandset) func() error {
	return func() error {
		_, err := handset.SignalQuality()
		return err
	}
}
