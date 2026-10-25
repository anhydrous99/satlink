package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/sim"
)

// The log is flushed before the run returns.
func TestTheLastLogLinesOfARunReachStandardErrorBeforeItEnds(t *testing.T) {
	stderr := newFirstWriteHolder()
	c := newCLI(environment{}, stderr)
	t.Cleanup(c.log.close)
	c.log.hand("the last log line\n")
	<-stderr.holding

	ended := make(chan int, 1)
	go func() { ended <- c.finish(nil) }()
	select {
	case status := <-ended:
		t.Fatalf("the run ended with status %d while its last log line was held", status)
	case <-time.After(heldLineWait):
	}
	close(stderr.released)

	if status := <-ended; status != exitSuccess || stderr.String() != "the last log line\n" {
		t.Errorf("exit status %d after standard error got %q, want success after the log line", status, stderr.String())
	}
}

// errorFromAServer is the error of a TLS connection to a server whose
// certificate is for other names. The names are checked before the chain
// of trust, so any server can cause it, and the error repeats them as they
// come: a certificate's names may hold any ASCII. These write to the
// clipboard and clear the screen. errorFromAServerShown is what a terminal
// gets of it.
var (
	errorFromAServer = errors.New("TLS: tls: failed to verify certificate: x509: certificate is valid for " +
		"\x1b]52;c;cm0gLXJmIH4K\x07\x1b[2Jevil.test, not web.test")
	errorFromAServerShown = "TLS: tls: failed to verify certificate: x509: certificate is valid for " +
		"evil.test, not web.test"
)

func TestAnErrorShowsWithoutTheControlCharactersAServerPutInIt(t *testing.T) {
	stderr := &lockedBuffer{}
	c := newCLI(environment{}, stderr)
	t.Cleanup(c.log.close)

	if status := c.finish(errorFromAServer); status != exitFailure {
		t.Errorf("exit status %d, want %d", status, exitFailure)
	}

	if want := "error: " + errorFromAServerShown + "\n"; stderr.String() != want {
		t.Errorf("reported %q, want %q", stderr.String(), want)
	}
}

// A panic ends the run with status 101, once unwinding has dropped — and so
// closed — the handset. A bug must not pass for a command line that does not
// parse, which is what Go's runtime would make of it, with status 2. No error
// follows the report. (A panic while the link is up hangs up and fails the
// command instead: TestAPanicDuringTheCallHangsUpAndFailsTheCommand.)
func TestAPanicEndsTheRunWithStatus101OnceTheHandsetIsClosed(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		trigger string
		// during is the handset's method that the panic came in, as the
		// stack reported shows.
		during string
	}{
		{"while the handset is readied", []string{instant, "info"}, "", "Initialize"},
		{"during a handset command", []string{instant, "status"}, "AT+CSQF", "LastSignalQuality"},
		{"while a data call is set up", []string{instant, "net", "status"}, "ATD", "DialData"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var simulator *sim.Handset
			trapped := world{handset: panickingAfter(test.trigger, func(made *sim.Handset) { simulator = made })}

			got := runInProcess(t, trapped, test.args...)

			checkEndedByThePanic(t, got)
			if frame := "iridium.(*Handset)." + test.during + "("; !strings.Contains(got.stderr, frame) {
				t.Errorf("the stack reported does not pass through %s: %s", frame, got.stderr)
			}
			if simulator == nil || !isClosed(simulator) {
				t.Error("the handset was left open")
			}
		})
	}
}

// checkEndedByThePanic checks that a run whose handset panicked with "a
// transport bug" reported the panic and its stack, and ended there, with
// exitPanicked: no error followed, and nothing was output.
func checkEndedByThePanic(t *testing.T, got outcome) {
	t.Helper()
	if got.status != exitPanicked || !strings.Contains(got.stderr, "panic: a transport bug\n\ngoroutine ") {
		t.Errorf("exit status %d after %q, want %d after the panic and its stack", got.status, got.stderr, exitPanicked)
	}
	if strings.HasPrefix(got.stderr, "error: ") || strings.Contains(got.stderr, "\nerror: ") || got.stdout != "" {
		t.Errorf("standard output %q and error %q, want nothing but the panic", got.stdout, got.stderr)
	}
}

// A panic that leaves main before the run can end as interrupted. The run
// stops catching interruptions, as every run does at its end.
func TestAPanicOutranksAnInterruption(t *testing.T) {
	var relay recordedRelay
	var stderr lockedBuffer
	c := newCLI(environment{}, &stderr)
	t.Cleanup(c.log.close)
	interrupted := c.interruptions.catch(relay.notify, func() { t.Error("quit at once") }, &stderr)
	relay.signals <- os.Interrupt
	<-interrupted.Done()

	status := c.panicked("a bug")

	if status != exitPanicked || relay.relaying {
		t.Errorf("exit status %d, still relaying interruptions %t; want %d, and no more relaying",
			status, relay.relaying, exitPanicked)
	}
	if !strings.Contains(stderr.String(), "panic: a bug\n\ngoroutine ") {
		t.Errorf("reported %q, want the panic and its stack", stderr.String())
	}
}
