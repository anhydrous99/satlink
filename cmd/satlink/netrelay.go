package main

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/anhydrous99/satlink/satnet"
	"github.com/spf13/cobra"
)

// `satlink net tcp`: standard input to a TCP connection, and what comes back
// to standard output, like netcat.

// sendTimeout is the longest standard input waits for room in the
// connection's send buffer.
const sendTimeout = 120 * time.Second

// relayCheckInterval is how often the relay looks up from the connection to
// check on standard input.
const relayCheckInterval = time.Second

// stdinEndGrace is how long standard input may take to end once the remote
// end has closed.
const stdinEndGrace = 2 * time.Second

// deliveryGrace is how long the end of standard input may take to be
// acknowledged once the remote end has closed: as long as closing the link
// would wait for it.
const deliveryGrace = 30 * time.Second

// errRemoteClosedFirst is why standard input could not all be sent when the
// remote end closed the connection while there was more to send.
var errRemoteClosedFirst = errors.New("the remote end closed the connection first")

// tcpAction connects to host on port and relays standard input and output
// until the remote end closes the connection.
type tcpAction struct {
	needsACall
	host string
	port uint16
}

// tcpCommand is `satlink net tcp`. Its <PORT> is named apart from the
// global --port, the handset's serial port.
func tcpCommand(start func(netAction) error) *command {
	var action tcpAction
	return netSubcommand(&cobra.Command{
		Use:   "tcp",
		Short: "Connect to HOST PORT and relay standard input and output, like netcat",
	}, start, func() netAction { return action },
		requiredPositional("HOST", stringValue{target: &action.host}),
		requiredPositional("PORT", boundedValue[uint16]{target: &action.port}))
}

func (t tcpAction) target() (netTarget, bool) {
	return netTarget{host: t.host, port: t.port}, true
}

// execute sends standard input to the connection from a goroutine of its
// own, as reading it blocks, and prints what comes back until the remote end
// closes the connection or standard input cannot be sent. Standard input
// must all be delivered, unless someone is typing it at a terminal. The
// connection closes once the relay and the sender are both done with it.
func (t tcpAction) execute(link netLink, s streams) error {
	conn, err := link.Dial(t.host, t.port, connectTimeout)
	if err != nil {
		return err
	}
	users := shareConnection(conn)
	defer users.done()
	if err := writef(s.stderr, "Connected to %s; type away, Ctrl-D to finish.\n", conn.PeerAddr()); err != nil {
		return err
	}
	// The sender's outcome waits here when the relay is over already.
	outcomes := make(chan error, 1)
	users.add()
	go func() {
		defer users.done()
		sendStandardInput(conn, s, outcomes)
	}()
	sending := &stdinSending{outcomes: outcomes}
	if err := printReplies(conn, s.stdout, sending); err != nil {
		return err
	}
	return sending.afterRemoteClosed(conn.WaitAcknowledged, s.stdinIsTerminal)
}

// sharedConnection is a connection that the relay and the sender of
// standard input share. It closes once the last of them is done with it: the
// relay's as it returned, or the sender thread's once standard input had
// ended. So neither closes it under the other, whose calls Close
// would end with net.ErrClosed.
type sharedConnection struct {
	conn  io.Closer
	users atomic.Int32
}

// shareConnection returns conn with one user, the caller.
func shareConnection(conn io.Closer) *sharedConnection {
	shared := &sharedConnection{conn: conn}
	shared.users.Store(1)
	return shared
}

// add counts another user, who must call done once it is done. A user adds
// one before it is done itself, so the connection cannot have closed.
func (c *sharedConnection) add() {
	c.users.Add(1)
}

// done tells that a user is done with the connection, and the last one
// closes it.
func (c *sharedConnection) done() {
	if c.users.Add(-1) == 0 {
		// A TCPConn's Close always returns nil.
		_ = c.conn.Close()
	}
}

// printReplies writes what arrives on conn to stdout until the remote end
// closes the connection. It looks up from the connection every
// relayCheckInterval, so that a failure to send standard input, which ends
// the stream, says why it ended.
func printReplies(conn *satnet.TCPConn, stdout io.Writer, sending *stdinSending) error {
	chunk := make([]byte, chunkSize)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(relayCheckInterval)); err != nil {
			return err
		}
		count, err := conn.Read(chunk)
		if count > 0 {
			if _, writeErr := stdout.Write(chunk[:count]); writeErr != nil {
				return writeErr
			}
		}
		switch {
		case err == nil:
			continue
		case errors.Is(err, io.EOF):
			return nil
		}
		sending.check()
		// A failure to send standard input says why the stream ended.
		if sending.failed() {
			return notAllSent(sending.err)
		}
		if !errors.Is(err, satnet.ErrTimedOut) {
			return err
		}
	}
}

// stdinSending is how sending standard input goes, as the relay learns it:
// the sending goroutine reports its outcome on outcomes, then closes it —
// with no outcome at all if it panicked.
type stdinSending struct {
	outcomes <-chan error
	// ended is set once the outcome has arrived, and err is the outcome: nil
	// once all of standard input was queued.
	ended bool
	err   error
}

// check takes the outcome, if it has arrived, without waiting.
func (s *stdinSending) check() {
	if s.ended {
		return
	}
	select {
	case err, ok := <-s.outcomes:
		s.ended, s.err = ok, err
	default:
	}
}

// await waits up to grace for the outcome.
func (s *stdinSending) await(grace time.Duration) {
	if s.ended {
		return
	}
	select {
	case err, ok := <-s.outcomes:
		s.ended, s.err = ok, err
	case <-time.After(grace):
	}
}

// failed reports whether sending standard input failed.
func (s *stdinSending) failed() bool {
	return s.ended && s.err != nil
}

// afterRemoteClosed is the relay's result once the remote end has closed
// the connection. What standard input still holds then cannot be sent,
// which only someone typing at a terminal does not mind, and what was sent
// must have arrived: waitAcknowledged, the connection's WaitAcknowledged,
// waits for that until a deadline.
func (s *stdinSending) afterRemoteClosed(waitAcknowledged func(deadline time.Time) error, stdinIsTerminal bool) error {
	s.await(stdinEndGrace)
	switch {
	case s.failed():
		return notAllSent(s.err)
	case s.ended:
		// Queued is not sent: a remote end that closed first resets what
		// arrives after.
		if err := waitAcknowledged(time.Now().Add(deliveryGrace)); err != nil {
			return notAllSent(err)
		}
		return nil
	case stdinIsTerminal:
		return nil
	default:
		return notAllSent(errRemoteClosedFirst)
	}
}

// notAllSent says that standard input could not all be sent, and why.
func notAllSent(cause error) error {
	return fmt.Errorf("not all of standard input was sent: %w", cause)
}

// outgoingStream is the connection as the sender of standard input uses it:
// writing to it, each chunk with a deadline, and ending what it sends.
// *satnet.TCPConn is one.
type outgoingStream interface {
	Write(p []byte) (int, error)
	SetWriteDeadline(deadline time.Time) error
	// CloseWrite ends the stream after what was written; Abort resets it.
	CloseWrite() error
	Abort() error
}

// sendStandardInput sends standard input to conn and reports the outcome on
// outcomes, which it then closes. It ends the stream after reporting: with
// its end once all of standard input is queued, and with a reset after a
// failure. A panic ends this goroutine alone, without an outcome, rather than
// the whole program before it has hung up.
func sendStandardInput(conn outgoingStream, s streams, outcomes chan<- error) {
	defer close(outcomes)
	defer func() {
		if value := recover(); value != nil {
			reportPanic(s.stderr, value)
		}
	}()
	err := forwardStandardInput(conn, s.stdin)
	outcomes <- err
	endStream(conn, err)
}

// endStream ends conn's stream once sending standard input has ended with
// err. Only the end of standard input ends the stream: the other end must
// not take a part for the whole.
func endStream(conn outgoingStream, err error) {
	// The abort and the write shutdown cannot fail; these fail only once the
	// connection is closed, which leaves nothing to end.
	if err != nil {
		_ = conn.Abort()
		return
	}
	_ = conn.CloseWrite()
}

// forwardStandardInput sends what stdin holds to conn, a chunk at a time,
// allowing each chunk sendTimeout to be queued.
func forwardStandardInput(conn outgoingStream, stdin io.Reader) error {
	chunk := make([]byte, chunkSize)
	for {
		count, readErr := stdin.Read(chunk)
		if count > 0 {
			if err := conn.SetWriteDeadline(time.Now().Add(sendTimeout)); err != nil {
				return err
			}
			if _, err := conn.Write(chunk[:count]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
