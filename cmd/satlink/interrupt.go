package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"
)

// Ctrl-C, SIGTERM and SIGHUP during a net call hang up properly instead of
// killing the CLI, which would leave ending the call to a DTR drop — and
// during call setup they cancel it, rather than let the call connect and
// bill first. Another one, once the first is a few seconds old, quits at
// once. One the CLI was started with ignored stays ignored, as shells do:
// nohup, or a background job, means the call to carry on. Once the call is
// over, the CLI ends by SIGINT (interrupt_unix.go).

// exitInterrupted is the exit status of an interrupted run that SIGINT
// cannot end: 128 + SIGINT, as shells report it. run returns it for every
// run an interruption asked to stop, and main then ends by SIGINT instead,
// where it can.
const exitInterrupted = 130

// repeatsIgnoredFor is how long a repeat after the first signal is part of
// the same request: a double-tapped Ctrl-C, or the second SIGHUP a closing
// terminal sends.
const repeatsIgnoredFor = 2 * time.Second

// signalBacklog is how many signals wait while one is handled: the next
// one, as any more are repeats of it.
const signalBacklog = 1

// interruptedMessage says what the first interruption does.
const interruptedMessage = "Interrupted: hanging up once the call allows (again to quit at once)…"

// errInterrupted is the outcome of a net subcommand that an interruption
// cut short.
var errInterrupted = errors.New("interrupted")

// notifyInterruptions relays the process's Ctrl-C, SIGTERM and SIGHUP to
// signals, instead of letting them end it, and returns the function that
// stops relaying them. Those the process was started with ignored stay
// ignored: Go's runtime keeps an inherited SIG_IGN for SIGHUP and SIGINT,
// which signal.Ignored then reports. It replaces one for SIGTERM with its
// own handler, so a SIGTERM ignored on entry is caught like any other.
func notifyInterruptions(signals chan<- os.Signal) (stop func()) {
	var caught []os.Signal
	for _, interruption := range interruptionSignals {
		if !signal.Ignored(interruption) {
			caught = append(caught, interruption)
		}
	}
	// Notify given no signal at all would relay every signal.
	if len(caught) > 0 {
		signal.Notify(signals, caught...)
	}
	return func() { signal.Stop(signals) }
}

// reaction is what an interruption does.
type reaction int

const (
	// reactionStop asks the run to stop: the first interruption's.
	reactionStop reaction = iota
	// reactionNone ignores a repeat that is part of the same request.
	reactionNone
	// reactionQuit quits at once: a repeat once the first is a few seconds
	// old.
	reactionQuit
)

// reactionTo is what an interruption handled at now does, the first having
// been handled at firstHandled, which is the zero time before.
func reactionTo(firstHandled, now time.Time) reaction {
	switch {
	case firstHandled.IsZero():
		return reactionStop
	case now.Sub(firstHandled) < repeatsIgnoredFor:
		return reactionNone
	default:
		return reactionQuit
	}
}

// interruptionHandler does what each interruption of a run calls for. The
// first asks the run to stop: interrupt ends the context that the run's
// net call watches. A repeat within repeatsIgnoredFor is part of the same
// request, and a later one quits at once. Both say so on stderr. Only one
// goroutine handles interruptions; askedToStop may be called from any.
type interruptionHandler struct {
	// stopRequested is set by the first interruption.
	stopRequested atomic.Bool
	interrupt     context.CancelFunc
	// quit ends the process at once.
	quit   func()
	stderr io.Writer
	// firstHandled is when the first interruption had been handled; the
	// zero time before.
	firstHandled time.Time
}

// newInterruptionHandler returns the handler whose first interruption calls
// interrupt, and whose late repeat calls quit; both say so on stderr.
func newInterruptionHandler(interrupt context.CancelFunc, quit func(), stderr io.Writer) *interruptionHandler {
	return &interruptionHandler{interrupt: interrupt, quit: quit, stderr: stderr}
}

// handle does what an interruption handled at now calls for.
func (h *interruptionHandler) handle(now time.Time) {
	switch reactionTo(h.firstHandled, now) {
	case reactionStop:
		h.requestStop()
	case reactionQuit:
		warnCallMayStillBeUp(h.stderr, " (quitting at once)")
		h.quit()
	}
}

// requestStop asks the run to stop: it cancels the call's setup, or hangs
// the call up, and says so.
func (h *interruptionHandler) requestStop() {
	h.stopRequested.Store(true)
	h.interrupt()
	// Best effort, and only once the hang-up is under way: standard error
	// can block.
	_, _ = fmt.Fprintln(h.stderr, interruptedMessage)
	// A repeat that waited behind the message is no later one.
	h.firstHandled = time.Now()
}

// askedToStop reports whether an interruption asked the run to stop.
func (h *interruptionHandler) askedToStop() bool {
	return h.stopRequested.Load()
}

// interruptions are a run's Ctrl-C, SIGTERM and SIGHUP, once its net call
// catches them: relayed to a goroutine of their own, which hands each in
// turn to the run's handler. The zero interruptions catches none; catch
// starts catching them.
type interruptions struct {
	// handler is the run's, from catch on.
	handler *interruptionHandler
	signals chan os.Signal
	// stopRelaying stops the signals' relay, and stopReacting the goroutine
	// that reacts to them, once.
	stopRelaying func()
	stopReacting chan struct{}
	stopOnce     sync.Once
}

// catch starts catching interruptions and reacting to them: notify relays
// them to a channel until the function it returns is called, as
// notifyInterruptions does for the process. The first asks the run to stop,
// a later repeat quits at once through quit, and both say so on stderr. The
// context catch returns is done from the first interruption on.
func (i *interruptions) catch(notify func(chan<- os.Signal) func(), quit func(), stderr io.Writer) context.Context {
	interrupted, interrupt := context.WithCancel(context.Background())
	i.handler = newInterruptionHandler(interrupt, quit, stderr)
	i.signals = make(chan os.Signal, signalBacklog)
	i.stopReacting = make(chan struct{})
	i.stopRelaying = notify(i.signals)
	go i.react()
	return interrupted
}

// react hands each interruption in turn to the handler, until stopped.
func (i *interruptions) react() {
	for {
		select {
		case <-i.signals:
			i.handler.handle(time.Now())
		case <-i.stopReacting:
			return
		}
	}
}

// stop stops catching interruptions, which end the process again as they
// do by default. It does nothing for the zero interruptions, nor once
// stopped.
func (i *interruptions) stop() {
	if i.stopReacting == nil {
		return
	}
	i.stopOnce.Do(func() {
		i.stopRelaying()
		close(i.stopReacting)
	})
}

// askedToStop reports whether an interruption asked the run to stop.
func (i *interruptions) askedToStop() bool {
	return i.handler != nil && i.handler.askedToStop()
}
