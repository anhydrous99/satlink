package iridium_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/iridium"
)

// Stopping a data call while it is being set up, with the Cancellation in
// its settings.

// patientConnectTimeout is a connect timeout long enough that only the
// cancellation can end the dial.
const patientConnectTimeout = time.Minute

// abortIgnoredWindow is how long after a command a handset may ignore what
// it receives (V.250 §5.6.1), so that an abort sent sooner could be lost.
const abortIgnoredWindow = 125 * time.Millisecond

// Cancelling a dial under way, as a cancel button would.
const (
	// cancelledAfter is how long the dial has waited when it is cancelled:
	// past the time a cancellation waits after the dial command.
	cancelledAfter = 300 * time.Millisecond
	// dialUnderWayWithin bounds how long the dial takes to get that far.
	dialUnderWayWithin = 5 * time.Second
	// abortedWithin bounds how long a cancelled dial takes to end: far less
	// than its connect timeout.
	abortedWithin = 10 * time.Second
)

// cancellationSeenWithin is how soon DialData promises to see a
// cancellation: within a fifth of a second.
const cancellationSeenWithin = 200 * time.Millisecond

// abortedDialSteps are a dial that waits, and its abort that ends it.
func abortedDialSteps() []step {
	return []step{dialStep(""), {"\r", "\r\nNO CARRIER\r\n"}}
}

// patientSettings are callSettings that wait a minute for CONNECT.
func patientSettings() iridium.DataCallSettings {
	settings := callSettings()
	settings.ConnectTimeout = patientConnectTimeout
	return settings
}

func TestACancelledDialIsAborted(t *testing.T) {
	transport := newScripted(abortedDialSteps()...)
	settings := patientSettings()
	dialEnded := make(chan error, 1)
	go func() { dialEnded <- errorOf(newTestHandset(transport).DialData(testNumber, settings)) }()
	waitUntil(t, dialUnderWayWithin, "the dial has waited a while", func() bool {
		dialled, ok := transport.writtenAt("ATD" + testNumber + "\r")
		return ok && time.Since(dialled) >= cancelledAfter
	})

	settings.Cancellation.Cancel()

	err := awaitDialEnd(t, dialEnded, abortedWithin)
	assertError(t, err, &iridium.Error{Kind: iridium.ErrCancelled})
	assertCallMayStillBeUp(t, err, false)
	assertAllPlayed(t, transport)
}

func TestACallThatConnectsDespiteItsCancellationIsEnded(t *testing.T) {
	transport := newScripted(dialStep(""), step{"\r", connectReply}, step{"+++", okReply}, step{"ATH\r", okReply})
	settings := patientSettings()
	handset := newTestHandset(cancelsOn{scripted: transport, trigger: "ATD", cancel: settings.Cancellation.Cancel})

	err := mustFailToDial(t, handset, settings)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCancelled})
	assertCallMayStillBeUp(t, err, false)
	assertAllPlayed(t, transport)
}

func TestNothingIsDialledOnceTheCallWasCancelled(t *testing.T) {
	transport := newScripted()
	settings := callSettings()
	settings.Cancellation.Cancel()

	err := mustFailToDial(t, newTestHandset(transport), settings)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCancelled})
	assertCallMayStillBeUp(t, err, false)
	if written := transport.history(); bytes.Contains(written, []byte("ATD")) {
		t.Errorf("written %q, want no dial", written)
	}
}

func TestACancellationJustAfterTheDialCommandWaitsToAbort(t *testing.T) {
	// Cancelled as ATD goes out: an abort within 125 ms of it could be
	// ignored (V.250 §5.6.1).
	transport := newScripted(abortedDialSteps()...)
	settings := patientSettings()
	handset := newTestHandset(cancelsOn{scripted: transport, trigger: "ATD", cancel: settings.Cancellation.Cancel})

	err := mustFailToDial(t, handset, settings)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCancelled})
	assertWaitedBetween(t, transport, "ATD"+testNumber+"\r", "\r", abortIgnoredWindow)
}

// The tests below cover an answer cancelled, a context driving the
// cancellation, and settings without one.

func TestACancelledAnswerIsAborted(t *testing.T) {
	// Cancelled as ATA goes out, the answer is aborted as a dial is, and a
	// call that connects all the same is ended.
	scripts := map[string][]step{
		"the abort ends the attempt": {{"ATA\r", ""}, {"\r", "\r\nNO CARRIER\r\n"}},
		"the call connects anyway":   slices.Concat([]step{{"ATA\r", ""}, {"\r", connectReply}}, confirmedHangUp()),
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			transport := newScripted(script...)
			settings := patientSettings()
			handset := newTestHandset(cancelsOn{scripted: transport, trigger: "ATA", cancel: settings.Cancellation.Cancel})

			_, err := handset.AnswerData(settings)

			assertError(t, err, &iridium.Error{Kind: iridium.ErrCancelled})
			assertCallMayStillBeUp(t, err, false)
			assertAllPlayed(t, transport)
		})
	}
}

func TestAContextCanCancelADial(t *testing.T) {
	transport := newScripted(abortedDialSteps()...)
	settings := patientSettings()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := context.AfterFunc(ctx, settings.Cancellation.Cancel)
	defer stop()
	handset := newTestHandset(cancelsOn{scripted: transport, trigger: "ATD", cancel: cancel})

	err := mustFailToDial(t, handset, settings)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCancelled})
	assertAllPlayed(t, transport)
}

func TestSettingsWithoutACancellationStillDial(t *testing.T) {
	settings := callSettings()
	settings.Cancellation = nil

	call := mustDial(t, newTestHandset(newScripted(dialStep(connectReply))), settings)

	if message := call.ConnectMessage(); message != "9600" {
		t.Errorf("ConnectMessage = %q, want 9600", message)
	}
}

func TestADialLooksAtItsCancellationEveryFifthOfASecond(t *testing.T) {
	// However long the dial may still wait for CONNECT, none of its reads
	// may wait longer than a fifth of a second, after which it looks at its
	// cancellation again.
	settings := patientSettings()
	transport := &readTimeouts{
		Transport: cancelsOn{scripted: newScripted(abortedDialSteps()...), trigger: "ATD", cancel: settings.Cancellation.Cancel},
		waitOf:    "ATD" + testNumber + "\r",
	}

	err := mustFailToDial(t, newTestHandset(transport), settings)

	assertError(t, err, &iridium.Error{Kind: iridium.ErrCancelled})
	if len(transport.timeouts) == 0 {
		t.Fatal("the dial never waited for its answer")
	}
	if longest := slices.Max(transport.timeouts); longest > cancellationSeenWithin {
		t.Errorf("a read of the dial waited up to %v, longer than the %v within which it is to see a cancellation",
			longest, cancellationSeenWithin)
	}
}

// awaitDialEnd returns the error a dial ended with, failing the test if it
// does not end within timeout.
func awaitDialEnd(t *testing.T, dialEnded <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-dialEnded:
		return err
	case <-time.After(timeout):
		t.Fatalf("the dial did not end within %v", timeout)
		return nil
	}
}

// cancelsOn is a scripted transport that calls cancel the moment trigger
// is written, as a cancel button pressed just then would.
type cancelsOn struct {
	*scripted
	trigger string
	cancel  func()
}

// WriteAll cancels for a write that starts with trigger, and writes p to
// the script.
func (c cancelsOn) WriteAll(p []byte) error {
	if bytes.HasPrefix(p, []byte(c.trigger)) {
		c.cancel()
	}
	return c.scripted.WriteAll(p)
}

// readTimeouts is a transport that records the timeout of every read while
// a command waits for its answer: from the write of the command, waitOf,
// until the next write. It is used by one goroutine at a time.
type readTimeouts struct {
	iridium.Transport
	waitOf string
	// waiting is set from the write of waitOf until the next write.
	waiting  bool
	timeouts []time.Duration
}

// WriteAll notes whether p starts the wait that is recorded, and writes it.
func (r *readTimeouts) WriteAll(p []byte) error {
	r.waiting = string(p) == r.waitOf
	return r.Transport.WriteAll(p)
}

// ReadTimeout records timeout during the wait, and reads.
func (r *readTimeouts) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	if r.waiting {
		r.timeouts = append(r.timeouts, timeout)
	}
	return r.Transport.ReadTimeout(p, timeout)
}
