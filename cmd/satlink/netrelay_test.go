package main

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/satnet"
)

// panickingReader is a standard input whose reads panic, as a bug might make
// them.
type panickingReader struct{}

func (panickingReader) Read([]byte) (int, error) {
	panic("a bug in reading")
}

func TestASenderThatPanicsEndsAloneOnceReported(t *testing.T) {
	var stderr strings.Builder
	outcomes := make(chan error, 1)

	// The panic comes before anything is sent, so no connection is needed.
	sendStandardInput(nil, streams{stdin: panickingReader{}, stderr: &stderr}, outcomes)

	if !strings.HasPrefix(stderr.String(), "panic: a bug in reading\n") {
		t.Errorf("reported %q, want the panic", stderr.String())
	}
	if err, reported := <-outcomes; reported {
		t.Errorf("the sender reported %v, want no outcome", err)
	}
}

// recordingStream is an outgoing stream that records what the sender of
// standard input does with it: what it writes, the write deadline each
// write found set, and how it ends the stream. Its writes fail with
// failure, if set, and otherwise queue all they are given, as satnet's do;
// setting a write deadline fails with deadlineFailure, if set. Ending the
// stream, it checks that the sender has reported its outcome on outcomes:
// the relay must know why the stream ended by the time the remote end sees
// it end.
type recordingStream struct {
	t                        *testing.T
	outcomes                 chan error
	failure, deadlineFailure error
	written                  []byte
	// deadline is the write deadline set since the last write, and
	// deadlines those that each write found.
	deadline  time.Time
	deadlines []time.Time
	// aborts and writeCloses count the resets and the ends of the stream.
	aborts, writeCloses int
}

func (s *recordingStream) Write(p []byte) (int, error) {
	if s.failure != nil {
		return 0, s.failure
	}
	s.deadlines = append(s.deadlines, s.deadline)
	s.deadline = time.Time{}
	s.written = append(s.written, p...)
	return len(p), nil
}

func (s *recordingStream) SetWriteDeadline(deadline time.Time) error {
	if s.deadlineFailure != nil {
		return s.deadlineFailure
	}
	s.deadline = deadline
	return nil
}

func (s *recordingStream) CloseWrite() error {
	s.checkReported()
	s.writeCloses++
	return nil
}

func (s *recordingStream) Abort() error {
	s.checkReported()
	s.aborts++
	return nil
}

// checkReported fails the test unless the sender's outcome waits on
// outcomes.
func (s *recordingStream) checkReported() {
	if len(s.outcomes) == 0 {
		s.t.Error("the stream ended before the sender reported why")
	}
}

// Go only: Rust's tests leave the stream's end unchecked.
func TestTheSenderResetsTheStreamUnlessAllOfStandardInputWasSent(t *testing.T) {
	unsendable := errors.New("timed out")
	tests := []struct {
		name  string
		stdin io.Reader
		// stream is the connection's, which fails as it is told to.
		stream  *recordingStream
		outcome error
		written string
		reset   bool
	}{
		{"all sent", strings.NewReader("all of it"), &recordingStream{}, nil, "all of it", false},
		{"standard input cannot be read", io.MultiReader(strings.NewReader("part of it"), brokenReader{}),
			&recordingStream{}, errBrokenStdin, "part of it", true},
		{"standard input cannot be sent", strings.NewReader("all of it"), &recordingStream{failure: unsendable},
			unsendable, "", true},
		{"no time can be allowed for sending", strings.NewReader("all of it"),
			&recordingStream{deadlineFailure: unsendable}, unsendable, "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcomes := make(chan error, 1)
			stream := test.stream
			stream.t, stream.outcomes = t, outcomes

			sendStandardInput(stream, streams{stdin: test.stdin, stderr: io.Discard}, outcomes)

			if outcome := <-outcomes; outcome != test.outcome {
				t.Errorf("the sender reported %v, want %v", outcome, test.outcome)
			}
			wantAborts, wantWriteCloses := 0, 1
			if test.reset {
				wantAborts, wantWriteCloses = 1, 0
			}
			if string(stream.written) != test.written || stream.aborts != wantAborts || stream.writeCloses != wantWriteCloses {
				t.Errorf("wrote %q, then reset the stream %d times and ended it %d times; want %q, %d and %d",
					stream.written, stream.aborts, stream.writeCloses, test.written, wantAborts, wantWriteCloses)
			}
		})
	}
}

func TestEachChunkOfStandardInputGetsSendTimeoutToBeQueued(t *testing.T) {
	stream := &recordingStream{t: t}
	stdin := strings.NewReader(strings.Repeat("x", 2*chunkSize+1))
	before := time.Now()

	err := forwardStandardInput(stream, stdin)

	after := time.Now()
	if err != nil || len(stream.deadlines) != 3 {
		t.Fatalf("forwarding returned %v after %d writes, want nil after 3", err, len(stream.deadlines))
	}
	// The Rust version's SEND_TIMEOUT.
	const allowed = 2 * time.Minute
	for i, deadline := range stream.deadlines {
		if !isAllowedAfter(deadline, allowed, before, after) {
			t.Errorf("write %d found the deadline %v, want two minutes after the chunk was read", i, deadline)
		}
	}
}

// echoLink returns a link to the simulated network's echo service, over a
// new simulated handset, which the test's end hangs up, waiting at most a
// second for data still queued.
func echoLink(t *testing.T) *satnet.Link {
	t.Helper()
	handset, _ := simtest.NewHandset(t, simtest.SimulatorConfig())
	policy, err := satnet.ParsePolicy("echo.test:7")
	if err != nil {
		t.Fatal(err)
	}
	config := satnet.NewConfig(policy)
	config.Call.EscapeGuard = simtest.CallEscapeGuard
	config.Tuning.CloseGrace = time.Second
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

// echoConnection returns a TCP connection to the simulated network's echo
// service, over a link of echoLink's.
func echoConnection(t *testing.T) *satnet.TCPConn {
	t.Helper()
	conn, err := echoLink(t).Dial("echo.test", 7, connectTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Closing a connection the link has let go of only says so.
		_ = conn.Close()
	})
	return conn
}

// readToTheEnd reads conn until it ends, within runLimit, and returns what
// it read, and why it ended: nil for the end of the stream.
func readToTheEnd(t *testing.T, conn *satnet.TCPConn) ([]byte, error) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(runLimit)); err != nil {
		t.Fatal(err)
	}
	return io.ReadAll(conn)
}

// Go only: what the connection says once standard input could not be read.
func TestASenderWhoseStandardInputFailsResetsTheConnection(t *testing.T) {
	t.Parallel()
	conn := echoConnection(t)
	stdin := io.MultiReader(strings.NewReader("part of the upload"), brokenReader{})
	outcomes := make(chan error, 1)

	sendStandardInput(conn, streams{stdin: stdin, stderr: io.Discard}, outcomes)

	if outcome := <-outcomes; outcome != errBrokenStdin {
		t.Errorf("the sender reported %v, want %v", outcome, errBrokenStdin)
	}
	// Whether any of the echo comes back first depends on timing; the
	// stream must not end as if the part were the whole.
	if _, err := readToTheEnd(t, conn); !errors.Is(err, satnet.ErrConnectionReset) {
		t.Errorf("reading the connection ended with %v, want the reset", err)
	}
}

// Go only: what the connection says once all of standard input was sent.
func TestASenderWhoseStandardInputEndsFinishesTheStream(t *testing.T) {
	t.Parallel()
	conn := echoConnection(t)
	outcomes := make(chan error, 1)

	sendStandardInput(conn, streams{stdin: strings.NewReader("all of the upload"), stderr: io.Discard}, outcomes)

	if outcome := <-outcomes; outcome != nil {
		t.Errorf("the sender reported %v, want nothing wrong", outcome)
	}
	if echoed, err := readToTheEnd(t, conn); err != nil || string(echoed) != "all of the upload" {
		t.Errorf("read %q, then %v; want all of it echoed, then the end of the stream", echoed, err)
	}
}

// sendingThatEnded returns how sending standard input goes once the sender
// reported outcome.
func sendingThatEnded(outcome error) *stdinSending {
	outcomes := make(chan error, 1)
	outcomes <- outcome
	close(outcomes)
	return &stdinSending{outcomes: outcomes}
}

// sendingThatPanicked returns how sending standard input goes once the
// sender panicked, ending without an outcome.
func sendingThatPanicked() *stdinSending {
	outcomes := make(chan error)
	close(outcomes)
	return &stdinSending{outcomes: outcomes}
}

func TestTheSendersOutcomeIsTakenOnceItHasArrived(t *testing.T) {
	stillSending := &stdinSending{outcomes: make(chan error)}
	tests := []struct {
		name    string
		sending *stdinSending
		ended   bool
		failed  bool
	}{
		{"still sending", stillSending, false, false},
		{"all sent", sendingThatEnded(nil), true, false},
		{"failed", sendingThatEnded(errBrokenStdin), true, true},
		{"panicked", sendingThatPanicked(), false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.sending.check()

			if test.sending.ended != test.ended || test.sending.failed() != test.failed {
				t.Errorf("ended %t and failed %t, want %t and %t",
					test.sending.ended, test.sending.failed(), test.ended, test.failed)
			}
		})
	}
}

func TestAnOutcomeOnceTakenIsKept(t *testing.T) {
	sending := sendingThatEnded(errBrokenStdin)
	sending.check()

	sending.check()
	sending.await(stdinEndGrace)

	if !sending.failed() || !errors.Is(sending.err, errBrokenStdin) {
		t.Errorf("outcome %v, want %v", sending.err, errBrokenStdin)
	}
}

func TestStandardInputLeftUnsentWhenTheRemoteEndClosedIsAnErrorUnlessSomeoneIsTyping(t *testing.T) {
	tests := []struct {
		name            string
		sending         *stdinSending
		stdinIsTerminal bool
		want            string
	}{
		{"sending failed", sendingThatEnded(errBrokenStdin), true, "not all of standard input was sent: standard input is broken"},
		{"still to send", sendingThatPanicked(), false,
			"not all of standard input was sent: the remote end closed the connection first"},
		{"still to type", sendingThatPanicked(), true, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// No acknowledgement is awaited unless all of standard input was
			// queued.
			err := test.sending.afterRemoteClosed(nil, test.stdinIsTerminal)

			if got := errorText(err); got != test.want {
				t.Errorf("result %q, want %q", got, test.want)
			}
		})
	}
}

// Go only: Rust's tests leave the grace unchecked. Once all of standard
// input is queued, it must be acknowledged within 30 s of the remote end's
// close, as long as closing the link would wait for it.
func TestTheEndOfStandardInputMustBeAcknowledgedWithinThirtySeconds(t *testing.T) {
	tests := []struct {
		name            string
		acknowledgement error
		want            string
	}{
		{"acknowledged", nil, ""},
		{"reset instead", &satnet.Error{Kind: satnet.ErrConnectionReset},
			"not all of standard input was sent: the connection was reset"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var waitedUntil time.Time
			before := time.Now()

			err := sendingThatEnded(nil).afterRemoteClosed(func(deadline time.Time) error {
				waitedUntil = deadline
				return test.acknowledgement
			}, false)

			after := time.Now()
			if got := errorText(err); got != test.want {
				t.Errorf("result %q, want %q", got, test.want)
			}
			const grace = 30 * time.Second
			if !isAllowedAfter(waitedUntil, grace, before, after) {
				t.Errorf("waited for the acknowledgement until %v, want between %v and %v",
					waitedUntil, before.Add(grace), after.Add(grace))
			}
		})
	}
}

// errorText is err's text, "" for nil.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// isAllowedAfter reports whether deadline lies allowed after a moment
// between before and after, as a deadline set then to allow that would.
func isAllowedAfter(deadline time.Time, allowed time.Duration, before, after time.Time) bool {
	return !deadline.Before(before.Add(allowed)) && !deadline.After(after.Add(allowed))
}

// dialRecorder is a link that keeps the TCP connection it dials, so that a
// test can see whether the relay closed it.
type dialRecorder struct {
	*satnet.Link
	dialled *satnet.TCPConn
}

func (l *dialRecorder) Dial(host string, port uint16, timeout time.Duration) (*satnet.TCPConn, error) {
	conn, err := l.Link.Dial(host, port, timeout)
	l.dialled = conn
	return conn, err
}

// isConnectionClosed reports whether conn has been closed: its deadline
// setters then fail with net.ErrClosed.
func isConnectionClosed(conn *satnet.TCPConn) bool {
	return errors.Is(conn.SetReadDeadline(time.Time{}), net.ErrClosed)
}

// closeWait is how long a test waits for a connection to close once its
// last user is done with it, which takes no traffic.
const closeWait = 10 * time.Second

// Go only: Rust's tests leave the stream's drop unchecked. The relay's
// connection closes, gracefully, once the relay and the sender of standard
// input are both done with it, whichever is done last, as the Rust
// version's TcpStream closed once both had dropped the Arc that held it.
func TestARelaysConnectionClosesOnceTheRelayAndTheSenderAreDone(t *testing.T) {
	t.Run("the sender first", func(t *testing.T) {
		t.Parallel()
		link := &dialRecorder{Link: echoLink(t)}
		var stdout strings.Builder

		err := tcpAction{host: "echo.test", port: 7}.execute(link,
			streams{stdin: strings.NewReader("hello"), stdout: &stdout, stderr: io.Discard})

		if err != nil || stdout.String() != "hello" {
			t.Fatalf("the relay printed %q and returned %v, want the echo and nothing wrong", stdout.String(), err)
		}
		// The sender's last steps may follow the relay's end.
		simtest.Eventually(t, closeWait, "the connection is closed", func() bool { return isConnectionClosed(link.dialled) })
	})
	t.Run("the relay first", func(t *testing.T) {
		t.Parallel()
		link := &dialRecorder{Link: echoLink(t)}
		stdin, typing := io.Pipe()
		go func() {
			// The relay fails as the echo comes back, while standard input
			// stays open.
			_, _ = io.WriteString(typing, "hello")
		}()

		err := tcpAction{host: "echo.test", port: 7}.execute(link,
			streams{stdin: stdin, stdout: brokenWriter{}, stderr: io.Discard})

		if err == nil {
			t.Fatal("the relay wrote the echo to a broken standard output")
		}
		if isConnectionClosed(link.dialled) {
			t.Fatal("the relay closed the connection under the sender")
		}
		// Closing a pipe cannot fail.
		_ = typing.Close()
		simtest.Eventually(t, closeWait, "the connection is closed", func() bool { return isConnectionClosed(link.dialled) })
	})
}
