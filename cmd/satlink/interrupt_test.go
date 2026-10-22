package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWhatAnInterruptionDoesDependsOnHowLongAgoTheFirstWasHandled(t *testing.T) {
	firstHandled := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		firstHandled time.Time
		now          time.Time
		want         reaction
	}{
		{"the first", time.Time{}, firstHandled, reactionStop},
		{"a repeat at once", firstHandled, firstHandled, reactionNone},
		{"a repeat just within the window", firstHandled, firstHandled.Add(repeatsIgnoredFor - time.Nanosecond), reactionNone},
		{"a repeat once the window is over", firstHandled, firstHandled.Add(repeatsIgnoredFor), reactionQuit},
		{"a much later repeat", firstHandled, firstHandled.Add(time.Hour), reactionQuit},
	}
	for _, test := range tests {
		if got := reactionTo(test.firstHandled, test.now); got != test.want {
			t.Errorf("%s: reaction %d, want %d", test.name, got, test.want)
		}
	}
}

// stopRequestWait is how long a test lets an interruption take to ask the
// run to stop.
const stopRequestWait = 10 * time.Second

// handledInterruptions are interruptions whose reactions a test drives by
// calling handle itself: nothing is caught. interrupted is the context the
// first ends, said is what they wrote on standard error, and quits how
// often they quit at once.
type handledInterruptions struct {
	*interruptionHandler
	interrupted context.Context
	said        lockedBuffer
	quits       int
}

func newHandledInterruptions() *handledInterruptions {
	h := &handledInterruptions{}
	interrupted, interrupt := context.WithCancel(context.Background())
	h.interrupted = interrupted
	h.interruptionHandler = newInterruptionHandler(interrupt, func() { h.quits++ }, &h.said)
	return h
}

// The stop is asked for before the message is written, so a blocked standard
// error cannot hold it up.
func TestTheFirstInterruptionAsksTheRunToStopBeforeSayingSo(t *testing.T) {
	interrupted, interrupt := context.WithCancel(context.Background())
	stderr := newBlockedWriter()
	t.Cleanup(stderr.unblock)
	handler := newInterruptionHandler(interrupt, func() { t.Error("quit at once") }, stderr)

	// Standard error may be a pipe nobody reads: saying so waits.
	go handler.handle(time.Now())

	select {
	case <-interrupted.Done():
	case <-time.After(stopRequestWait):
		t.Fatalf("the run was not asked to stop within %v while standard error was blocked", stopRequestWait)
	}
}

func TestTheFirstInterruptionAsksTheRunToStopAndSaysSo(t *testing.T) {
	h := newHandledInterruptions()

	h.handle(time.Now())

	if !h.askedToStop() || h.interrupted.Err() == nil || h.quits != 0 {
		t.Errorf("requested %t, interrupted %v, quit %d times; want a stop requested and no quit",
			h.askedToStop(), h.interrupted.Err(), h.quits)
	}
	if got := h.said.String(); got != interruptedMessage+"\n" {
		t.Errorf("said %q, want %q", got, interruptedMessage)
	}
}

func TestARepeatRightAfterTheFirstInterruptionIsPartOfTheSameRequest(t *testing.T) {
	h := newHandledInterruptions()
	h.handle(time.Now())

	h.handle(time.Now())

	if got := h.said.String(); got != interruptedMessage+"\n" || h.quits != 0 {
		t.Errorf("said %q and quit %d times, want only the first message", got, h.quits)
	}
}

func TestARepeatOnceTheFirstIsAFewSecondsOldQuitsAtOnce(t *testing.T) {
	h := newHandledInterruptions()
	h.handle(time.Now())

	h.handle(time.Now().Add(repeatsIgnoredFor))

	if h.quits != 1 {
		t.Errorf("quit %d times, want once", h.quits)
	}
	if !strings.Contains(h.said.String(), "\nwarning: the hang-up was not confirmed (quitting at once), so the call may still be up") {
		t.Errorf("said %q, want the warning", h.said.String())
	}
}

func TestInterruptionsNeverCaughtNeverAskToStop(t *testing.T) {
	var never interruptions

	never.stop()

	if never.askedToStop() {
		t.Error("an interruption was requested where none was caught")
	}
}

// recordedRelay stands in for notifyInterruptions: it keeps the channel
// that interruptions are relayed to, and whether they still are.
type recordedRelay struct {
	signals  chan<- os.Signal
	relaying bool
}

func (r *recordedRelay) notify(signals chan<- os.Signal) (stop func()) {
	r.signals, r.relaying = signals, true
	return func() { r.relaying = false }
}

func TestCaughtInterruptionsAreRelayedUntilStopped(t *testing.T) {
	var relay recordedRelay
	var stderr lockedBuffer
	var i interruptions
	interrupted := i.catch(relay.notify, func() { t.Error("quit at once") }, &stderr)

	relay.signals <- os.Interrupt
	<-interrupted.Done()
	i.stop()
	i.stop()

	if !i.askedToStop() || relay.relaying {
		t.Errorf("requested %t and still relaying %t, want a stop requested and no more relaying",
			i.askedToStop(), relay.relaying)
	}
}
