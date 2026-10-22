package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/ppp"
	"github.com/anhydrous99/satlink/serial"
	"github.com/anhydrous99/satlink/sim"
)

// Running the command in-process: the world it finds, the output it leaves,
// and the simulated handsets, well or misbehaving, that it talks to.

// lockedBuffer collects output that several goroutines may write at once.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// world is what a command run in-process finds around it. The zero world
// has empty standard input, no environment variables, no handsets on USB,
// plain simulated handsets, and no interruptions.
type world struct {
	// stdin is standard input; nil is an empty one.
	stdin io.Reader
	// stdinIsTerminal, stdoutIsTerminal and stderrIsTerminal make the
	// standard streams terminals.
	stdinIsTerminal, stdoutIsTerminal, stderrIsTerminal bool
	// variables are the environment variables that are set.
	variables map[string]string
	ports     []serial.Port
	// discoveryFails makes looking for handsets on USB fail.
	discoveryFails bool
	// handset makes the handset that --simulate asks for, from the
	// configuration the command chose; nil makes a plain simulated one.
	handset func(t *testing.T, config sim.Config) satlink.Transport
	// interruptions, if not nil, are the Ctrl-C, SIGTERM and SIGHUP the
	// command gets, relayed once it catches them, and quit what quitting
	// at once does instead of ending the process; a nil quit fails the test.
	interruptions *interruptionSource
}

// interruptionSource sends a command run in-process its interruptions, and
// counts how often it quit at once.
type interruptionSource struct {
	signals chan os.Signal
	quits   atomic.Int32
}

// sentInterruptions is how many interruptions a test may send before the
// command takes them: the two of a double tap.
const sentInterruptions = 2

func newInterruptionSource() *interruptionSource {
	return &interruptionSource{signals: make(chan os.Signal, sentInterruptions)}
}

// interrupt sends the command a Ctrl-C, which it gets once it catches them.
func (s *interruptionSource) interrupt() {
	s.signals <- os.Interrupt
}

// relay hands the interruptions to signals, as notifyInterruptions does
// for the process, until stopped.
func (s *interruptionSource) relay(signals chan<- os.Signal) (stop func()) {
	stopped := make(chan struct{})
	go func() {
		for {
			select {
			case interruption := <-s.signals:
				select {
				case signals <- interruption:
				case <-stopped:
					return
				}
			case <-stopped:
				return
			}
		}
	}()
	return func() { close(stopped) }
}

// outcome is what a run of the command left.
type outcome struct {
	status         int
	stdout, stderr string
}

// environment returns an environment for running args in-process in w, with
// output collected in the buffers it also returns. The run's own log handler
// is never installed, so the run logs nothing; the packages' records go to
// the test binary's default logger, and so to its standard error, as tests
// never replace slog.Default.
func (w world) environment(t *testing.T, args []string) (environment, *lockedBuffer, *lockedBuffer) {
	t.Helper()
	stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
	stdin := w.stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	makeHandset := w.handset
	if makeHandset == nil {
		makeHandset = plainSimulator
	}
	env := environment{
		args:             args,
		stdin:            stdin,
		stdout:           stdout,
		stderr:           stderr,
		stdinIsTerminal:  w.stdinIsTerminal,
		stdoutIsTerminal: w.stdoutIsTerminal,
		stderrIsTerminal: w.stderrIsTerminal,
		lookupEnv: func(key string) (string, bool) {
			value, set := w.variables[key]
			return value, set
		},
		installLogger: func(slog.Handler) {},
		discoverHandsets: func() ([]serial.Port, error) {
			if w.discoveryFails {
				return nil, &satlink.Error{Kind: satlink.ErrTransport, Message: "no USB here"}
			}
			return w.ports, nil
		},
		simulateHandset:     func(config sim.Config) satlink.Transport { return makeHandset(t, config) },
		notifyInterruptions: w.notifyInterruptions,
		quitInterrupted:     w.quitter(t),
	}
	return env, stdout, stderr
}

// notifyInterruptions relays the world's interruptions to signals, if it
// has any, until stopped.
func (w world) notifyInterruptions(signals chan<- os.Signal) (stop func()) {
	if w.interruptions == nil {
		return func() {}
	}
	return w.interruptions.relay(signals)
}

// quitter returns what quitting at once does in the world: it counts the
// quits of its interruptions, and fails a test without any.
func (w world) quitter(t *testing.T) func() {
	return func() {
		if w.interruptions == nil {
			t.Error("the command quit at once without being interrupted")
			return
		}
		w.interruptions.quits.Add(1)
	}
}

// runInProcess runs satlink with args in w until it ends.
func runInProcess(t *testing.T, w world, args ...string) outcome {
	t.Helper()
	env, stdout, stderr := w.environment(t, args)
	status := run(context.Background(), env)
	return outcome{status: status, stdout: stdout.String(), stderr: stderr.String()}
}

// plainSimulator is a simulated handset made with config, closed when the
// test ends if the command left it open.
func plainSimulator(t *testing.T, config sim.Config) satlink.Transport {
	return simtest.NewSimulator(t, config)
}

// inboxHolding returns simulated handsets whose inbox holds a message from
// each sender, with the text "Hello from <sender>", the first at index 1.
func inboxHolding(senders ...string) func(*testing.T, sim.Config) satlink.Transport {
	return func(t *testing.T, config sim.Config) satlink.Transport {
		simulator := simtest.NewSimulator(t, config)
		for _, sender := range senders {
			simulator.Control().ReceiveSMS(sender, "Hello from "+sender)
		}
		return simulator
	}
}

// smsArrivingOn returns simulated handsets that receive an SMS as command
// is written to them, so that its announcement comes during the command.
func smsArrivingOn(command string) func(*testing.T, sim.Config) satlink.Transport {
	return func(t *testing.T, config sim.Config) satlink.Transport {
		return &smsOnCommand{Handset: simtest.NewSimulator(t, config), command: command}
	}
}

// smsOnCommand is a simulated handset that receives an SMS when command is
// first written to it.
type smsOnCommand struct {
	*sim.Handset
	command string
	once    sync.Once
}

func (h *smsOnCommand) WriteAll(p []byte) error {
	if bytes.HasPrefix(p, []byte(h.command)) {
		h.once.Do(func() { h.Control().ReceiveSMS("+881631234567", "Hello from orbit") })
	}
	return h.Handset.WriteAll(p)
}

// unplugged is a simulated handset whose cable has been pulled.
func unplugged(t *testing.T, config sim.Config) satlink.Transport {
	faulty := simtest.NewFaultyTransport(simtest.NewSimulator(t, config))
	faulty.Unplugged.Store(true)
	return faulty
}

// errBrokenStdin is why a brokenReader cannot be read.
var errBrokenStdin = errors.New("standard input is broken")

// brokenReader is a standard input that cannot be read.
type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) {
	return 0, errBrokenStdin
}

// panickingOnEscape is a simulated handset behind a transport that panics on
// the +++ escape, so that hanging up a data call fails. A handset deaf to the
// escape and to DTR would fail it too, but only after some twenty seconds
// with the default escape guard; the panic gives the same warning at once.
func panickingOnEscape(t *testing.T, config sim.Config) satlink.Transport {
	faulty := simtest.NewFaultyTransport(simtest.NewSimulator(t, config))
	faulty.PanicsOnEscape.Store(true)
	return faulty
}

// panickingAfter returns simulated handsets behind a transport whose next
// read panics with "a transport bug", as a buggy transport might, once a
// command starting with trigger is written — the first command, for "".
// Each simulator made is handed to keep, so that the test can see what
// became of it.
func panickingAfter(trigger string, keep func(*sim.Handset)) func(*testing.T, sim.Config) satlink.Transport {
	return func(t *testing.T, config sim.Config) satlink.Transport {
		simulator := simtest.NewSimulator(t, config)
		keep(simulator)
		return &panicTrap{FaultyTransport: simtest.NewFaultyTransport(simulator), trigger: []byte(trigger)}
	}
}

// panicTrap is a faulty transport that turns PanicsOnNextRead on once a
// command starting with trigger is written to it.
type panicTrap struct {
	*simtest.FaultyTransport
	trigger []byte
}

func (p *panicTrap) WriteAll(data []byte) error {
	if bytes.HasPrefix(data, p.trigger) {
		p.PanicsOnNextRead.Store(true)
	}
	return p.FaultyTransport.WriteAll(data)
}

// isClosed reports whether the command closed the simulated handset:
// raising DTR, which changes nothing on it, fails once it is closed.
func isClosed(simulator *sim.Handset) bool {
	return simulator.RaiseDTR() != nil
}

// refusingTheCredentials is a simulated handset whose gateway demands PAP
// credentials that the command does not give, behind a transport that
// panics on the +++ escape: the link does not come up, and its hang-up
// fails.
func refusingTheCredentials(t *testing.T, config sim.Config) satlink.Transport {
	config.Authentication = &sim.Authentication{
		Protocol:    ppp.AuthProtocolPAP,
		Credentials: ppp.Credentials{Username: "app", Password: "secret"},
	}
	return panickingOnEscape(t, config)
}

// interruptingBuffer collects output and interrupts the command as soon as
// any arrives.
type interruptingBuffer struct {
	lockedBuffer
	interrupt context.CancelFunc
}

func (b *interruptingBuffer) Write(p []byte) (int, error) {
	defer b.interrupt()
	return b.lockedBuffer.Write(p)
}

// blockedWriter is an output stream that nobody reads until it is
// unblocked, as a pipe to a pager still on its first page is: its writes
// wait until then.
type blockedWriter struct {
	lockedBuffer
	// writing is closed once a write has begun, and unblocked lets writes
	// through.
	writing      chan struct{}
	firstWriting sync.Once
	unblocked    chan struct{}
}

func newBlockedWriter() *blockedWriter {
	return &blockedWriter{writing: make(chan struct{}), unblocked: make(chan struct{})}
}

func (w *blockedWriter) Write(p []byte) (int, error) {
	w.firstWriting.Do(func() { close(w.writing) })
	<-w.unblocked
	return w.lockedBuffer.Write(p)
}

// unblock lets the writes that wait, and those after them, through.
func (w *blockedWriter) unblock() {
	close(w.unblocked)
}

// awaitWrite waits until a write has begun, failing the test after a
// minute.
func (w *blockedWriter) awaitWrite(t *testing.T) {
	t.Helper()
	select {
	case <-w.writing:
	case <-time.After(time.Minute):
		t.Fatal("nothing was written within a minute")
	}
}

// gatedWriter is an output stream whose writes go through until it is
// shut, as a pipe's do until the pager reading it has filled its first
// page; then they wait until it is opened again.
type gatedWriter struct {
	lockedBuffer
	mu sync.Mutex
	// opened is closed while the writer is open.
	opened chan struct{}
}

func newGatedWriter() *gatedWriter {
	opened := make(chan struct{})
	close(opened)
	return &gatedWriter{opened: opened}
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	opened := w.opened
	w.mu.Unlock()
	<-opened
	return w.lockedBuffer.Write(p)
}

// shut makes the writes from now on wait until open.
func (w *gatedWriter) shut() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.opened = make(chan struct{})
}

// open lets the writes that wait, and those after them, through. It does
// nothing while the writer is open.
func (w *gatedWriter) open() {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.opened:
	default:
		close(w.opened)
	}
}

// errBrokenOutput is why a brokenWriter cannot be written to.
var errBrokenOutput = errors.New("broken pipe")

// brokenWriter is an output stream that cannot be written to, such as a
// closed pipe.
type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) {
	return 0, errBrokenOutput
}
