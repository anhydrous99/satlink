//go:build unix

package main_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/serial"
)

// The tests that need a process of their own: the interruptions — SIGINT,
// SIGTERM and SIGHUP from outside, and how the process then ends — and a
// standard error that blocks, which satlink's own log must not make the link
// wait for; and standard streams that nobody reads any more, which must not end
// the process before it has hung up. They build the satlink binary once and run
// it. netinprocess_test.go runs the others in-process.

// limit is the longest a run may take: dialling, PPP, the exchange and
// hanging up, all without the simulator's delays.
const limit = 60 * time.Second

// What satlink says once connected, when interrupted, and once it has hung
// up; and what it warns of when it may not have, or quits at once.
const (
	connectedText   = "type away"
	interruptedText = "Interrupted: hanging up once the call allows"
	hungUpText      = "Call lasted"
	warningText     = "warning"
	quittingText    = "(quitting at once)"
)

// The pipes that tests fill: chunks of fillChunk bytes until the pipe takes
// no more; a drain reads drainChunk bytes at
// a time and looks again every drainCheck while nothing has arrived.
const (
	fillChunk  = 64
	drainChunk = 4096
	drainCheck = 10 * time.Millisecond
)

// The scenarios' own times: the gap between the two taps
// of a double-tapped Ctrl-C; how long standard error stays blocked behind
// them, longer than the 2 s in which a repeat is ignored; how long the log
// stays blocked, longer than idleLimitSeconds and the hang-up take, which
// is why a call of longestCallSeconds or more waited for it; how long an
// ignored signal is given to show it was not.
const (
	doubleTapGap         = 200 * time.Millisecond
	stderrBlockedFor     = 3 * time.Second
	logBlockedFor        = 40 * time.Second
	idleLimitSeconds     = "25"
	longestCallSeconds   = 35
	ignoredSignalAllowed = time.Second
)

// lateRepeatGap is how long after the first interruption's message a late
// repeat comes: longer than the 2 s in which a repeat is ignored.
const lateRepeatGap = 3 * time.Second

// setupCancelLimit is how soon an interrupted call setup must end: well
// before the 20 s the call takes to connect.
const setupCancelLimit = 10 * time.Second

// interruptedStatus is the exit status of an interrupted run that SIGINT
// cannot end: 128 + SIGINT, as shells report it.
const interruptedStatus = 130

// callLasted reads how long the call lasted from the call summary.
var callLasted = regexp.MustCompile(`Call lasted (\d+) s`)

// built is the satlink binary, built once for all the tests in a directory
// that TestMain removes.
var built struct {
	once sync.Once
	dir  string
	path string
	err  error
}

func TestMain(m *testing.M) {
	status := m.Run()
	if built.dir != "" {
		// Best effort: a temporary directory left behind harms nothing.
		_ = os.RemoveAll(built.dir)
	}
	os.Exit(status)
}

// satlinkBinary returns the satlink binary built from this package, and
// skips the test where the go command is not to be found.
func satlinkBinary(t *testing.T) string {
	t.Helper()
	goCommand, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go command to build satlink with: %v", err)
	}
	built.once.Do(func() {
		built.dir, built.err = os.MkdirTemp("", "satlink-test-")
		if built.err != nil {
			return
		}
		built.path = filepath.Join(built.dir, "satlink")
		if output, err := exec.Command(goCommand, "build", "-o", built.path, ".").CombinedOutput(); err != nil {
			built.err = fmt.Errorf("%v: %s", err, output)
		}
	})
	if built.err != nil {
		t.Fatalf("cannot build satlink: %v", built.err)
	}
	return built.path
}

// skipUnlessDeliverable skips the test when this process ignores
// interruption, as one started under nohup or as a background job does:
// satlink, its child, would ignore it too.
func skipUnlessDeliverable(t *testing.T, interruption os.Signal) {
	t.Helper()
	if signal.Ignored(interruption) {
		t.Skipf("this process ignores %v, and so would satlink", interruption)
	}
}

// output collects what a process writes on one of its streams. It is safe
// for concurrent use.
type output struct {
	mu   sync.Mutex
	text bytes.Buffer
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.text.Write(p)
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.text.String()
}

// await waits until the output says text.
func (o *output) await(t *testing.T, text string) {
	t.Helper()
	simtest.Eventually(t, limit, fmt.Sprintf("satlink says %q", text), func() bool {
		return strings.Contains(o.String(), text)
	})
}

// satlinkRun is a satlink process that a test runs, its standard input open
// until closed. stdout and stderr collect its output, unless the test sends
// that elsewhere.
type satlinkRun struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout, stderr *output
	exited         chan struct{}
}

// collecting returns cmd with its standard output and error collected by
// the run that start makes of it.
func collecting(cmd *exec.Cmd) *satlinkRun {
	r := &satlinkRun{cmd: cmd, stdout: &output{}, stderr: &output{}}
	cmd.Stdout, cmd.Stderr = r.stdout, r.stderr
	return r
}

// variablesThatChangeSatlink are the environment variables that change
// what satlink does, besides its own: those that decide its styles.
var variablesThatChangeSatlink = []string{"NO_COLOR", "CLICOLOR_FORCE", "CLICOLOR", "TERM", "CI"}

// satlinkEnvironment returns this process's environment without satlink's
// own variables and those that change what it does, so that satlink does
// the same on every machine, and with variables, each "NAME=value", added.
func satlinkEnvironment(variables ...string) []string {
	kept := slices.DeleteFunc(os.Environ(), func(variable string) bool {
		name, _, _ := strings.Cut(variable, "=")
		return strings.HasPrefix(name, "SATLINK_") || slices.Contains(variablesThatChangeSatlink, name)
	})
	return append(kept, variables...)
}

// start starts the process, its standard input piped and left open, in
// satlinkEnvironment unless the test gave it an environment. A process
// still running when the test ends is killed.
func (r *satlinkRun) start(t *testing.T) *satlinkRun {
	t.Helper()
	if r.cmd.Env == nil {
		r.cmd.Env = satlinkEnvironment()
	}
	stdin, err := r.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r.stdin, r.exited = stdin, make(chan struct{})
	go func() {
		defer close(r.exited)
		// Its error only says how the process ended, as ProcessState does.
		_ = r.cmd.Wait()
	}()
	t.Cleanup(func() {
		if !r.hasEnded() {
			// Best effort: it may have ended meanwhile.
			_ = r.cmd.Process.Kill()
			<-r.exited
		}
	})
	return r
}

// hasEnded reports whether the process has ended.
func (r *satlinkRun) hasEnded() bool {
	select {
	case <-r.exited:
		return true
	default:
		return false
	}
}

// relayTo starts `satlink --simulate=instant net tcp host port`, its output
// collected.
func relayTo(t *testing.T, host string, port int) *satlinkRun {
	t.Helper()
	return collecting(exec.Command(satlinkBinary(t), "--simulate=instant", "net", "tcp", host, strconv.Itoa(port))).start(t)
}

// send sends the process interruption: SIGINT as a terminal's Ctrl-C
// would, though to it alone.
func (r *satlinkRun) send(t *testing.T, interruption os.Signal) {
	t.Helper()
	if err := r.cmd.Process.Signal(interruption); err != nil {
		t.Fatal(err)
	}
}

// closeStdin ends the process's standard input, unless the process has
// ended already, when Wait has closed it.
func (r *satlinkRun) closeStdin(t *testing.T) {
	t.Helper()
	if err := r.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
}

// finish waits for the process to end, within limit, and returns how it
// ended.
func (r *satlinkRun) finish(t *testing.T) syscall.WaitStatus {
	t.Helper()
	select {
	case <-r.exited:
	case <-time.After(limit):
		t.Fatalf("satlink did not end within %v", limit)
	}
	return r.cmd.ProcessState.Sys().(syscall.WaitStatus)
}

// describe says how a process ended, as a shell would.
func describe(status syscall.WaitStatus) string {
	if status.Signaled() {
		return "killed by " + status.Signal().String()
	}
	return "exit status " + strconv.Itoa(status.ExitStatus())
}

// checkHungUpThenEndedBySIGINT checks that satlink, which printed printed,
// hung up without a warning, then ended by SIGINT — so that a shell loop
// around it stops too.
func checkHungUpThenEndedBySIGINT(t *testing.T, status syscall.WaitStatus, printed string) {
	t.Helper()
	if !strings.Contains(printed, hungUpText) || strings.Contains(printed, warningText) {
		t.Errorf("satlink printed %q, want it to hang up without a warning", printed)
	}
	if !status.Signaled() || status.Signal() != syscall.SIGINT {
		t.Errorf("satlink ended with %s, want it killed by SIGINT: %s", describe(status), printed)
	}
}

// The process's own variables stand in for their options, as clap's env
// reads them; the in-process tests make up the variables a run finds.
func TestTheProcessesVariablesStandInForTheirOptions(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "ttyACM9")
	// The serial library's error, as the open of a port that is not there
	// fails: "transport error: No such file or directory" on macOS and
	// Linux.
	_, openErr := serial.Open(missing, serial.DefaultBaud)
	tests := []struct {
		name, variable string
		args           []string
		status         int
		says           string
	}{
		{"the data number", "SATLINK_DATA_NUMBER=12345", []string{"--simulate=instant", "net", "status"}, 0,
			"Dialling 12345 — "},
		{"the port", "SATLINK_PORT=" + missing, []string{"info"}, 1, fmt.Sprintf("error: %v\n", openErr)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(satlinkBinary(t), test.args...)
			cmd.Env = satlinkEnvironment(test.variable)
			run := collecting(cmd).start(t)
			run.closeStdin(t)

			status := run.finish(t)
			if printed := run.stderr.String(); !strings.Contains(printed, test.says) || !status.Exited() ||
				status.ExitStatus() != test.status {
				t.Errorf("satlink ended with %s, having printed %q; want exit status %d after %q",
					describe(status), printed, test.status, test.says)
			}
		})
	}
}

// An interrupted run hangs up, then ends by SIGINT. SIGTERM and SIGHUP are
// tested too.
func TestAnInterruptedRunHangsUpThenEndsBySIGINT(t *testing.T) {
	tests := []struct {
		name         string
		interruption syscall.Signal
	}{
		{"SIGINT", syscall.SIGINT},
		{"SIGTERM", syscall.SIGTERM},
		{"SIGHUP", syscall.SIGHUP},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			skipUnlessDeliverable(t, test.interruption)
			// Standard input stays open: only the interruption ends the run.
			run := relayTo(t, "echo.test", 7)
			run.stderr.await(t, connectedText)

			run.send(t, test.interruption)

			status := run.finish(t)
			checkHungUpThenEndedBySIGINT(t, status, run.stderr.String())
		})
	}
}

// A second interruption right after the first still lets it hang up. The
// second follows as soon as the first has been taken in, rather than after a
// pause of 200 ms.
func TestASecondInterruptionRightAfterTheFirstStillLetsItHangUp(t *testing.T) {
	t.Parallel()
	skipUnlessDeliverable(t, syscall.SIGINT)
	run := relayTo(t, "echo.test", 7)
	run.stderr.await(t, connectedText)

	// A double-tapped Ctrl-C.
	run.send(t, syscall.SIGINT)
	run.stderr.await(t, interruptedText)
	run.send(t, syscall.SIGINT)

	status := run.finish(t)
	checkHungUpThenEndedBySIGINT(t, status, run.stderr.String())
}

// An interruption during call setup keeps the call from connecting. With -v the
// simulator's log says when the call is being placed, so the test need not
// sleep two seconds.
func TestAnInterruptionDuringCallSetupKeepsTheCallFromConnecting(t *testing.T) {
	t.Parallel()
	skipUnlessDeliverable(t, syscall.SIGINT)
	// Iridium's timing: the call takes 20 s to connect.
	run := collecting(exec.Command(satlinkBinary(t), "--simulate", "-v", "net", "tcp", "echo.test", "7")).start(t)
	run.stderr.await(t, "simulated handset: dialling")
	interrupted := time.Now()

	run.send(t, syscall.SIGINT)

	status := run.finish(t)
	printed := run.stderr.String()
	if took := time.Since(interrupted); took >= setupCancelLimit {
		t.Errorf("satlink took %v to end once interrupted: %s", took, printed)
	}
	if strings.Contains(printed, "Connected") || strings.Contains(printed, warningText) {
		t.Errorf("satlink printed %q, want no call connected and no warning", printed)
	}
	if !status.Signaled() || status.Signal() != syscall.SIGINT {
		t.Errorf("satlink ended with %s, want it killed by SIGINT: %s", describe(status), printed)
	}
}

// A hang-up signal that satlink was started to ignore stays ignored, and so
// does an interrupt that a background job ignores.
func TestAnInterruptionItWasStartedToIgnoreStaysIgnored(t *testing.T) {
	tests := []struct {
		name         string
		launcher     []string
		interruption syscall.Signal
	}{
		// `nohup satlink net tcp echo.test 7`: SIGHUP ignored from the start.
		{"SIGHUP under nohup", []string{"nohup"}, syscall.SIGHUP},
		// As a shell's background job without job control is started.
		{"SIGINT in a background job", []string{"sh", "-c", `trap '' INT; exec "$0" "$@"`}, syscall.SIGINT},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			launcher, err := exec.LookPath(test.launcher[0])
			if err != nil {
				t.Skipf("no %s: %v", test.launcher[0], err)
			}
			args := slices.Concat(test.launcher[1:],
				[]string{satlinkBinary(t), "--simulate=instant", "net", "tcp", "echo.test", "7"})
			run := collecting(exec.Command(launcher, args...)).start(t)
			run.stderr.await(t, connectedText)

			// The terminal closes; standard input ends later. A signal taken
			// would end the run meanwhile.
			run.send(t, test.interruption)
			time.Sleep(ignoredSignalAllowed)
			run.closeStdin(t)

			if status := run.finish(t); !status.Exited() || status.ExitStatus() != 0 {
				t.Errorf("satlink ended with %s, want success: %s", describe(status), run.stderr.String())
			}
		})
	}
}

// Started with SIGINT ignored, as a background job without job control is,
// satlink cannot end by SIGINT: interrupted by another signal,
// it hangs up and then exits with status 130, so that a script around it
// stops too.
func TestARunInterruptedWhileSIGINTIsIgnoredExitsWithItsStatus(t *testing.T) {
	t.Parallel()
	skipUnlessDeliverable(t, syscall.SIGTERM)
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	run := collecting(exec.Command(shell, "-c", `trap '' INT; exec "$0" "$@"`, satlinkBinary(t),
		"--simulate=instant", "net", "tcp", "echo.test", "7")).start(t)
	run.stderr.await(t, connectedText)

	run.send(t, syscall.SIGTERM)

	status := run.finish(t)
	printed := run.stderr.String()
	if !strings.Contains(printed, hungUpText) || strings.Contains(printed, warningText) {
		t.Errorf("satlink printed %q, want it to hang up without a warning", printed)
	}
	if !status.Exited() || status.ExitStatus() != interruptedStatus {
		t.Errorf("satlink ended with %s, want exit status %d: %s", describe(status), interruptedStatus, printed)
	}
}

// slowPipe is a pipe that can be filled, as one to a pager still on its
// first page is: the end for satlink, the end to drain it from, and a
// filler to close before draining. All but satlink's end never block. They
// are a FIFO's, opened apart so that only the reader and the filler are
// non-blocking, and read and written with the system's calls rather than
// through Go's poller, which on macOS misses the end of a FIFO.
type slowPipe struct {
	satlinkEnd *os.File
	// reader and filler are descriptors, -1 once closed.
	reader, filler int
}

// newSlowPipe returns a slow pipe whose ends are closed when the test ends.
func newSlowPipe(t *testing.T) *slowPipe {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	p := &slowPipe{reader: -1, filler: -1}
	t.Cleanup(p.close)
	// The reader first: opening a FIFO's write end needs one.
	p.reader = openFIFO(t, path, syscall.O_RDONLY|syscall.O_NONBLOCK)
	p.satlinkEnd = os.NewFile(uintptr(openFIFO(t, path, syscall.O_WRONLY)), path)
	p.filler = openFIFO(t, path, syscall.O_WRONLY|syscall.O_NONBLOCK)
	return p
}

// openFIFO opens the FIFO at path with flags, never to be inherited by
// another test's process, and returns its descriptor.
func openFIFO(t *testing.T, path string, flags int) int {
	t.Helper()
	descriptor, err := syscall.Open(path, flags|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

// close closes what is still open of the pipe's ends.
func (p *slowPipe) close() {
	// Best effort, as for any file a test is done with.
	if p.satlinkEnd != nil {
		_ = p.satlinkEnd.Close()
	}
	for _, descriptor := range []*int{&p.reader, &p.filler} {
		if *descriptor >= 0 {
			_ = syscall.Close(*descriptor)
			*descriptor = -1
		}
	}
}

// handedOver closes this process's copy of satlink's end, once satlink has
// started with it: the reader sees the end of the pipe only once every
// writer has closed it.
func (p *slowPipe) handedOver(t *testing.T) {
	t.Helper()
	if err := p.satlinkEnd.Close(); err != nil {
		t.Fatal(err)
	}
}

// fill fills the pipe through the filler, until it takes no more.
func (p *slowPipe) fill() {
	chunk := bytes.Repeat([]byte("."), fillChunk)
	for {
		if _, err := syscall.Write(p.filler, chunk); err != nil {
			return
		}
	}
}

// closeFiller closes the filler, so that the pipe can end.
func (p *slowPipe) closeFiller(t *testing.T) {
	t.Helper()
	err := syscall.Close(p.filler)
	p.filler = -1
	if err != nil {
		t.Fatal(err)
	}
}

// drain returns what arrives at the reader until done holds, or until every
// writer has closed the pipe, within limit.
func (p *slowPipe) drain(t *testing.T, done func(received []byte) bool) []byte {
	t.Helper()
	var received []byte
	chunk := make([]byte, drainChunk)
	deadline := time.Now().Add(limit)
	for !done(received) {
		count, err := syscall.Read(p.reader, chunk)
		switch {
		case errors.Is(err, syscall.EAGAIN):
			if time.Now().After(deadline) {
				t.Fatalf("the pipe did not deliver what was awaited within %v: %q", limit, received)
			}
			time.Sleep(drainCheck)
		case err != nil:
			t.Fatal(err)
		case count == 0:
			return received
		default:
			received = append(received, chunk[:count]...)
		}
	}
	return received
}

// saying returns a drain's end: once text has arrived.
func saying(text string) func([]byte) bool {
	return func(received []byte) bool { return bytes.Contains(received, []byte(text)) }
}

// untilClosed is a drain's end: once every writer has closed the pipe.
func untilClosed([]byte) bool { return false }

// secondsTheCallLasted reads how long the call lasted from the call summary
// in printed, failing the test if there is none.
func secondsTheCallLasted(t *testing.T, printed string) int {
	t.Helper()
	lasted := callLasted.FindStringSubmatch(printed)
	if lasted == nil {
		t.Fatalf("no call summary: %s", printed)
	}
	seconds, err := strconv.Atoi(lasted[1])
	if err != nil {
		t.Fatal(err)
	}
	return seconds
}

// startWithStderr starts satlink with args, its standard error going to
// stderr's satlink end and its standard output nowhere.
func startWithStderr(t *testing.T, stderr *slowPipe, args ...string) *satlinkRun {
	t.Helper()
	cmd := exec.Command(satlinkBinary(t), args...)
	cmd.Stderr = stderr.satlinkEnd
	run := (&satlinkRun{cmd: cmd}).start(t)
	stderr.handedOver(t)
	return run
}

// pipeNobodyReads returns the end of a pipe for satlink to write to, whose
// reader is gone, as one to `head` that has had enough: writing to it fails
// with EPIPE, and raises SIGPIPE. The test's end closes it.
func pipeNobodyReads(t *testing.T) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Best effort, as for any file a test is done with.
		_ = writer.Close()
	})
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return writer
}

// readUntil reads from file until what it has read says text, within
// limit.
func readUntil(t *testing.T, file *os.File, text string) {
	t.Helper()
	if err := file.SetReadDeadline(time.Now().Add(limit)); err != nil {
		t.Fatal(err)
	}
	var read []byte
	chunk := make([]byte, drainChunk)
	for !bytes.Contains(read, []byte(text)) {
		count, err := file.Read(chunk)
		read = append(read, chunk[:count]...)
		if err != nil {
			t.Fatalf("satlink did not say %q: %v, after %q", text, err, read)
		}
	}
}

// A reader of standard output that has had enough — `satlink net get … | head`
// — fails the command once it has hung up, and does not end satlink before
// then. SIGPIPE is ignored, so the failing write is an error, not a signal.
func TestAStandardOutputNobodyReadsFailsTheCommandOnceHungUp(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"a fetched page", []string{"--simulate=instant", "net", "get", "http://web.test/"}, ""},
		{"a relay", []string{"--simulate=instant", "net", "tcp", "echo.test", "7"}, "hello\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(satlinkBinary(t), test.args...)
			stderr := &output{}
			cmd.Stdout, cmd.Stderr = pipeNobodyReads(t), stderr
			run := (&satlinkRun{cmd: cmd, stderr: stderr}).start(t)

			if _, err := io.WriteString(run.stdin, test.stdin); err != nil {
				t.Fatal(err)
			}

			status := run.finish(t)
			printed := stderr.String()
			if !status.Exited() || status.ExitStatus() != 1 {
				t.Errorf("satlink ended with %s, want exit status 1: %s", describe(status), printed)
			}
			if !strings.Contains(printed, hungUpText) || strings.Contains(printed, warningText) ||
				!strings.HasSuffix(printed, "\nerror: Broken pipe (os error 32)\n") {
				t.Errorf("satlink printed %q, want it to hang up without a warning, then fail", printed)
			}
		})
	}
}

// A log (-vvv) that nobody reads any more neither ends satlink mid-call nor
// fails the relay: SIGPIPE is ignored, so the relay relays everything and hangs
// up.
func TestAStandardErrorNobodyReadsStopsNeitherTheRelayNorTheHangUp(t *testing.T) {
	t.Parallel()
	stderr, satlinksEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Best effort, as for any file a test is done with.
		_ = stderr.Close()
	})
	cmd := exec.Command(satlinkBinary(t), "--simulate=instant", "-vvv", "net", "tcp", "echo.test", "7")
	stdout := &output{}
	cmd.Stdout, cmd.Stderr = stdout, satlinksEnd
	run := (&satlinkRun{cmd: cmd, stdout: stdout}).start(t)
	if err := satlinksEnd.Close(); err != nil {
		t.Fatal(err)
	}
	readUntil(t, stderr, connectedText)

	// The log goes on: satlink logs every segment at -vvv.
	if err := stderr.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(run.stdin, "hello\n"); err != nil {
		t.Fatal(err)
	}
	simtest.Eventually(t, limit, "satlink relays the echo or ends", func() bool {
		return strings.Contains(stdout.String(), "hello\n") || run.hasEnded()
	})
	run.closeStdin(t)

	status := run.finish(t)
	if !status.Exited() || status.ExitStatus() != 0 || stdout.String() != "hello\n" {
		t.Errorf("satlink ended with %s after relaying %q, want success after the echo", describe(status), stdout.String())
	}
}

// A double tap while standard error is blocked still lets it hang up.
func TestADoubleTapWhileStandardErrorIsBlockedStillLetsItHangUp(t *testing.T) {
	if testing.Short() {
		t.Skip("leaves standard error blocked for seconds, behind the double tap")
	}
	skipUnlessDeliverable(t, syscall.SIGINT)
	stderr := newSlowPipe(t)
	run := startWithStderr(t, stderr, "--simulate=instant", "net", "tcp", "echo.test", "7")
	printed := stderr.drain(t, saying(connectedText))
	// Nobody reads standard error for a while.
	stderr.fill()

	// A double tap, the first message stuck behind the full pipe; the pipe
	// stays full for longer than a repeat is ignored, which nothing outside
	// the process could show sooner.
	run.send(t, syscall.SIGINT)
	time.Sleep(doubleTapGap)
	run.send(t, syscall.SIGINT)
	time.Sleep(stderrBlockedFor)
	stderr.closeFiller(t)

	printed = append(printed, stderr.drain(t, untilClosed)...)
	status := run.finish(t)
	checkHungUpThenEndedBySIGINT(t, status, string(printed))
}

// A repeat once the first is older than the 2 s in which repeats are ignored
// quits at once, leaving the call to the DTR drop: the warning, then death by
// SIGINT. A standard output nobody reads holds the run in writing the page out,
// so that it cannot end first.
func TestASecondInterruptionSecondsAfterTheFirstQuitsAtOnce(t *testing.T) {
	t.Parallel()
	skipUnlessDeliverable(t, syscall.SIGINT)
	stdout := newSlowPipe(t)
	stdout.fill()
	cmd := exec.Command(satlinkBinary(t), "--simulate=instant", "net", "get", "http://web.test/")
	stderr := &output{}
	cmd.Stdout, cmd.Stderr = stdout.satlinkEnd, stderr
	run := (&satlinkRun{cmd: cmd, stderr: stderr}).start(t)
	stdout.handedOver(t)
	// The page's head is out: its body waits for standard output.
	stderr.await(t, "HTTP/1.0 200 OK")

	run.send(t, syscall.SIGINT)
	stderr.await(t, interruptedText)
	// The scenario's own gap: nothing outside the process could show sooner
	// that the repeat counts as a late one.
	time.Sleep(lateRepeatGap)
	run.send(t, syscall.SIGINT)

	status := run.finish(t)
	printed := stderr.String()
	if !strings.Contains(printed, quittingText) || strings.Contains(printed, hungUpText) {
		t.Errorf("satlink printed %q, want it to quit at once, before the call summary", printed)
	}
	if !status.Signaled() || status.Signal() != syscall.SIGINT {
		t.Errorf("satlink ended with %s, want it killed by SIGINT: %s", describe(status), printed)
	}
}

// A blocked standard error does not hold the link up.
func TestABlockedStandardErrorDoesNotHoldTheLinkUp(t *testing.T) {
	if testing.Short() {
		t.Skip("leaves standard error blocked for 40 s")
	}
	stderr := newSlowPipe(t)
	run := startWithStderr(t, stderr, "--simulate=instant", "-vvv", "net", "--idle-secs", idleLimitSeconds,
		"tcp", "echo.test", "7")
	printed := stderr.drain(t, saying(connectedText))

	// Nobody reads the log for longer than the idle limit and the hang-up
	// take, which nothing outside the process could show sooner.
	stderr.fill()
	time.Sleep(logBlockedFor)
	stderr.closeFiller(t)
	printed = append(printed, stderr.drain(t, untilClosed)...)

	run.closeStdin(t)
	run.finish(t)
	if lasted := secondsTheCallLasted(t, string(printed)); lasted >= longestCallSeconds {
		t.Errorf("the idle limit waited for the log: the call lasted %d s", lasted)
	}
}
