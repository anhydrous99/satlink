package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/sim"
)

// The Rust version's tests/net.rs, those that need no process boundary,
// run in-process: net tcp and net get end to end against the simulator —
// how a run ends when the remote end closes first, a page fetched for a
// slow reader — and, Go only, what interruptions and a panic do to a run.
// net_integration_test.go runs the binary for the tests that need a
// process of their own.

// runLimit is the longest a run may take: dialling, PPP, the exchange and
// hanging up, all without the simulator's delays.
const runLimit = 60 * time.Second

// webRequest asks the simulated web server for its page, which webPage
// starts.
const (
	webRequest = "GET / HTTP/1.0\r\n\r\n"
	webPage    = "HTTP/1.0 200 OK"
)

// callLasted reads how long the call lasted from the call summary.
var callLasted = regexp.MustCompile(`Call lasted (\d+) s`)

// relayTo runs `satlink --simulate=instant net tcp host port` in-process in
// w until it ends.
func relayTo(t *testing.T, w world, host string, port int) outcome {
	t.Helper()
	return runInProcess(t, w, instant, "net", "tcp", host, strconv.Itoa(port))
}

// stdinLeftOpen is a standard input that holds text and then stays open, as
// one someone is still typing does, until the test ends.
func stdinLeftOpen(t *testing.T, text string) io.Reader {
	stdin, typing := io.Pipe()
	t.Cleanup(func() {
		// Closing a pipe cannot fail.
		_ = typing.Close()
	})
	go func() {
		// Best effort: the command may be over before it reads it all.
		_, _ = io.WriteString(typing, text)
	}()
	return stdin
}

// checkNotAllSent checks that a relay to the web server printed the page
// but failed, as standard input could not all be sent.
func checkNotAllSent(t *testing.T, got outcome) {
	t.Helper()
	if got.status == exitSuccess || !strings.Contains(got.stderr, "not all of standard input was sent") {
		t.Errorf("exit status %d and standard error %q, want a failure to send standard input", got.status, got.stderr)
	}
	if !strings.HasPrefix(got.stdout, webPage) {
		t.Errorf("standard output %q, want the page", got.stdout)
	}
}

// Rust: a_request_the_remote_end_answered_and_closed_is_no_error.
func TestARequestTheRemoteEndAnsweredAndClosedIsNoError(t *testing.T) {
	t.Parallel()

	// The request is all of standard input.
	got := relayTo(t, world{stdin: strings.NewReader(webRequest)}, "web.test", 80)

	if got.status != exitSuccess || !strings.HasPrefix(got.stdout, webPage) {
		t.Errorf("exit status %d, output %q and %q; want success and the page", got.status, got.stdout, got.stderr)
	}
}

// Rust: an_upload_the_remote_end_cut_short_is_an_error.
func TestAnUploadTheRemoteEndCutShortIsAnError(t *testing.T) {
	t.Parallel()
	// The web server answers the request head and closes, long before the
	// rest has gone at Iridium's rate.
	upload := webRequest + strings.Repeat("x", 30_000)

	got := relayTo(t, world{stdin: strings.NewReader(upload)}, "web.test", 80)

	checkNotAllSent(t, got)
}

// Rust: an_upload_whose_end_the_remote_end_reset_is_an_error.
func TestAnUploadWhoseEndTheRemoteEndResetIsAnError(t *testing.T) {
	t.Parallel()
	// Small enough to be queued at once, but the web server has closed, and
	// resets it, by the time it arrives.
	upload := webRequest + strings.Repeat("x", 3_000)

	got := relayTo(t, world{stdin: strings.NewReader(upload)}, "web.test", 80)

	checkNotAllSent(t, got)
}

func TestStandardInputStillOpenWhenTheRemoteEndClosedIsAnError(t *testing.T) {
	t.Parallel()

	got := relayTo(t, world{stdin: stdinLeftOpen(t, webRequest)}, "web.test", 80)

	checkNotAllSent(t, got)
	if want := "not all of standard input was sent: the remote end closed the connection first"; !strings.Contains(got.stderr, want) {
		t.Errorf("standard error %q, want it to say %q", got.stderr, want)
	}
}

func TestSomeoneStillTypingWhenTheRemoteEndClosedIsNoError(t *testing.T) {
	t.Parallel()

	got := relayTo(t, world{stdin: stdinLeftOpen(t, webRequest), stdinIsTerminal: true}, "web.test", 80)

	if got.status != exitSuccess || !strings.HasPrefix(got.stdout, webPage) {
		t.Errorf("exit status %d, output %q and %q; want success and the page", got.status, got.stdout, got.stderr)
	}
}

func TestARelayWhoseOutputCannotBeWrittenFails(t *testing.T) {
	t.Parallel()
	env, _, stderr := world{stdin: strings.NewReader("hello\n")}.environment(t,
		[]string{instant, "net", "tcp", "echo.test", "7"})
	env.stdout = brokenWriter{}

	status := run(context.Background(), env)

	if status != exitFailure || !strings.HasSuffix(stderr.String(), "\nerror: broken pipe\n") {
		t.Errorf("exit status %d and standard error %q, want the output's failure", status, stderr.String())
	}
}

// panickingWriter is an output stream whose writes panic, as a bug might
// make them.
type panickingWriter struct{}

func (panickingWriter) Write([]byte) (int, error) {
	panic("a bug in writing")
}

// recordingHandset is a simulated handset that records its stats as the
// command closes it.
type recordingHandset struct {
	*sim.Handset
	// statsAtClose are the stats as the command closed the handset; nil
	// until then.
	statsAtClose *sim.Stats
}

func (h *recordingHandset) Close() error {
	stats := h.Control().Stats()
	h.statsAtClose = &stats
	return h.Handset.Close()
}

// Go only: Rust's tests make no net subcommand panic. As in the Rust
// version, a panic in the subcommand's work — a standard output whose writes
// panic stands in for a bug — is reported, and fails the command once it has
// hung up. Closing the handset would end the call too, by dropping DTR, but
// that is the last resort: that handsets honour DTR over USB is not yet
// verified.
func TestAPanicDuringTheCallHangsUpAndFailsTheCommand(t *testing.T) {
	t.Parallel()
	handset := &recordingHandset{}
	w := world{handset: func(t *testing.T, config sim.Config) satlink.Transport {
		handset.Handset = simtest.NewSimulator(t, config)
		return handset
	}}
	env, _, stderr := w.environment(t, []string{instant, "net", "resolve", "echo.test"})
	env.stdout = panickingWriter{}

	status := run(context.Background(), env)

	printed := stderr.String()
	if status != exitFailure || !strings.HasSuffix(printed, "\nerror: internal error: the command panicked\n") {
		t.Errorf("exit status %d and standard error %q, want the failure of a command that panicked", status, printed)
	}
	_, afterThePanic, found := strings.Cut(printed, "panic: a bug in writing\n")
	if !found || !strings.Contains(afterThePanic, "\nCall lasted ") {
		t.Errorf("standard error %q, want the panic reported, then the call summary", printed)
	}
	if stats := handset.statsAtClose; stats == nil || stats.InDataCall {
		t.Errorf("the handset's stats as it was closed: %+v; want it closed once the call was hung up", stats)
	}
}

// backgroundRun is a command run in-process in a goroutine of its own, so
// that the test can act while it runs.
type backgroundRun struct {
	stdout, stderr *lockedBuffer
	status         chan int
}

// startInProcess starts running the command that env holds; stderr is
// where env writes standard error.
func startInProcess(env environment, stdout, stderr *lockedBuffer) *backgroundRun {
	r := &backgroundRun{stdout: stdout, stderr: stderr, status: make(chan int, 1)}
	go func() { r.status <- run(context.Background(), env) }()
	return r
}

// awaitStderr waits until standard error says text.
func (r *backgroundRun) awaitStderr(t *testing.T, text string) {
	t.Helper()
	simtest.Eventually(t, runLimit, fmt.Sprintf("standard error says %q", text), func() bool {
		return strings.Contains(r.stderr.String(), text)
	})
}

// finish waits for the run to end, within runLimit, and returns what it
// left.
func (r *backgroundRun) finish(t *testing.T) outcome {
	t.Helper()
	select {
	case status := <-r.status:
		return outcome{status: status, stdout: r.stdout.String(), stderr: r.stderr.String()}
	case <-time.After(runLimit):
		t.Fatalf("the command did not end within %v", runLimit)
		return outcome{}
	}
}

// controlledWorld is a world of plain simulated handsets whose control it
// keeps, once the command has made its handset.
type controlledWorld struct {
	world
	control atomic.Pointer[sim.Control]
}

func newControlledWorld() *controlledWorld {
	w := &controlledWorld{}
	w.handset = func(t *testing.T, config sim.Config) satlink.Transport {
		simulator := simtest.NewSimulator(t, config)
		w.control.Store(simulator.Control())
		return simulator
	}
	return w
}

// awaitStats waits until the simulated handset's stats show what.
func (w *controlledWorld) awaitStats(t *testing.T, what string, shown func(sim.Stats) bool) {
	t.Helper()
	simtest.Eventually(t, runLimit, what, func() bool {
		control := w.control.Load()
		return control != nil && shown(control.Stats())
	})
}

// Rust: a_fetched_page_is_hung_up_on_before_a_slow_reader_takes_it. A
// standard output that blocks stands in for the Rust test's full pipe, and
// the test waits for the hang-up instead of sleeping ten seconds.
func TestAFetchedPageIsHungUpOnBeforeASlowReaderTakesIt(t *testing.T) {
	t.Parallel()
	w := newControlledWorld()
	env, _, stderr := w.environment(t, []string{instant, "net", "get", "http://web.test/"})
	stdout := newBlockedWriter()
	env.stdout = stdout
	r := startInProcess(env, &stdout.lockedBuffer, stderr)

	// The reader comes back to it only once the call is over.
	stdout.awaitWrite(t)
	w.awaitStats(t, "the call is hung up", func(stats sim.Stats) bool { return stats.CallsConnected == 1 && !stats.InDataCall })
	stdout.unblock()
	got := r.finish(t)

	if got.status != exitSuccess || got.stdout != "Hello from the simulated Iridium network!\n" {
		t.Fatalf("exit status %d, output %q and %q; want success and the page's body", got.status, got.stdout, got.stderr)
	}
	if lasted := secondsTheCallLasted(t, got.stderr); lasted >= 8 {
		t.Errorf("the call lasted %d s, want under 8: %s", lasted, got.stderr)
	}
}

// secondsTheCallLasted reads how long the call lasted from the call summary
// in stderr, failing the test if there is none.
func secondsTheCallLasted(t *testing.T, stderr string) int {
	t.Helper()
	lasted := callLasted.FindStringSubmatch(stderr)
	if lasted == nil {
		t.Fatalf("no call summary: %s", stderr)
	}
	seconds, err := strconv.Atoi(lasted[1])
	if err != nil {
		t.Fatal(err)
	}
	return seconds
}

func TestAnInterruptedRelayHangsUpAndEndsAsInterrupted(t *testing.T) {
	t.Parallel()
	interruptions := newInterruptionSource()
	w := world{stdin: stdinLeftOpen(t, "hello\n"), interruptions: interruptions}
	env, stdout, stderr := w.environment(t, []string{instant, "net", "tcp", "echo.test", "7"})
	r := startInProcess(env, stdout, stderr)
	r.awaitStderr(t, "type away")

	interruptions.interrupt()
	got := r.finish(t)

	if got.status != exitInterrupted || interruptions.quits.Load() != 0 {
		t.Errorf("exit status %d after %d quits, want %d and none", got.status, interruptions.quits.Load(), exitInterrupted)
	}
	for _, said := range []string{interruptedMessage + "\n", "\nCall lasted ", "\nerror: interrupted\n"} {
		if !strings.Contains(got.stderr, said) {
			t.Errorf("standard error %q, want it to say %q", got.stderr, said)
		}
	}
}

// Go only. Nothing between an interruption and the hang-up waits for
// standard error, which may be a pipe nobody reads: neither the message
// that the interruption writes, nor anything else.
func TestAnInterruptionHangsUpWhileStandardErrorIsBlocked(t *testing.T) {
	t.Parallel()
	interruptions := newInterruptionSource()
	w := newControlledWorld()
	w.stdin, w.interruptions = stdinLeftOpen(t, "hello\n"), interruptions
	env, stdout, _ := w.environment(t, []string{instant, "net", "tcp", "echo.test", "7"})
	stderr := newGatedWriter()
	t.Cleanup(stderr.open)
	env.stderr = stderr
	r := startInProcess(env, stdout, &stderr.lockedBuffer)
	r.awaitStderr(t, "type away")

	stderr.shut()
	interruptions.interrupt()
	w.awaitStats(t, "the call is hung up while standard error is blocked", func(stats sim.Stats) bool {
		return stats.CallsConnected == 1 && !stats.InDataCall
	})
	stderr.open()
	got := r.finish(t)

	if got.status != exitInterrupted || !strings.Contains(got.stderr, interruptedMessage+"\n") {
		t.Errorf("exit status %d and standard error %q, want %d and the interruption's message",
			got.status, got.stderr, exitInterrupted)
	}
}

func TestAnInterruptionWhileTheCallIsPlacedCancelsIt(t *testing.T) {
	t.Parallel()
	interruptions := newInterruptionSource()
	w := newControlledWorld()
	w.interruptions = interruptions
	// Iridium's timing: the call takes 20 s to connect.
	env, stdout, stderr := w.environment(t, []string{"--simulate", "net", "tcp", "echo.test", "7"})
	r := startInProcess(env, stdout, stderr)
	w.awaitStats(t, "the call is being placed", func(stats sim.Stats) bool { return stats.CallsPlaced == 1 })

	interruptions.interrupt()
	got := r.finish(t)

	if got.status != exitInterrupted || w.control.Load().Stats().CallsConnected != 0 {
		t.Errorf("exit status %d with %d calls connected, want %d and none",
			got.status, w.control.Load().Stats().CallsConnected, exitInterrupted)
	}
	if !strings.HasSuffix(got.stderr, "\nerror: handset: the call setup was cancelled\n") || strings.Contains(got.stderr, "Connected") {
		t.Errorf("standard error %q, want the setup cancelled before anything connected", got.stderr)
	}
}
