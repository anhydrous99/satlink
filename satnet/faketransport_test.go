package satnet

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
)

// fakeSendRoom is the fake TCP connection's send buffer: the default
// tuning's TCP send buffer.
const fakeSendRoom = 4096

// The ends of the fake TCP connection.
var (
	fakeClientAddress = netip.MustParseAddrPort("10.64.0.2:49152")
	fakeServerAddress = netip.MustParseAddrPort("192.0.2.43:443")
)

// errFakeReset is what the server's end reads once the client reset the
// connection.
var errFakeReset = errors.New("the client reset the connection")

// fakeTCP stands in for a TCP connection under a TLS session, in memory.
// Its client side is a tlsTransport; its server side, from server(), is a
// net.Conn for tls.Server. What the client sends waits in a send buffer of
// limited room until the server reads it, as in TCP's; what the server
// writes never waits. One lock guards both directions, and a change wakes
// whoever waits, as the link's engine does.
//
// As a TCPConn does, the client may not send once it finished sending or
// reset the connection, and a reset fails its later calls with
// ErrConnectionReset — a wait for acknowledgement too, when it dropped what
// the server had yet to read.
type fakeTCP struct {
	mu      sync.Mutex
	changed broadcast
	// toServer and toClient are the bytes on their way; mu guards them
	// and the flags below.
	toServer bytes.Buffer
	toClient bytes.Buffer
	sendRoom int
	// clientFinished and serverFinished are the ends' FINs: the other end
	// reads the end of the stream once it has read everything before.
	clientFinished bool
	serverFinished bool
	// clientClosed and serverClosed report that an end closed.
	clientClosed bool
	serverClosed bool
	// clientReset reports that the client reset the connection (Abort),
	// and lostOnReset that the reset dropped bytes on their way.
	clientReset bool
	lostOnReset bool
	// waitingCalls counts the calls waiting for a change, so that a test
	// can tell when a call has started to wait.
	waitingCalls int
	// sendAttempts counts the client's tries at sending, so that a test can
	// tell a call that waits from one that spins.
	sendAttempts int
	// The client's deadlines, and the server's read deadline.
	readDeadline       atomicTime
	writeDeadline      atomicTime
	serverReadDeadline atomicTime
}

// The fake is a TCP connection as a TLS session sees it.
var _ tlsTransport = (*fakeTCP)(nil)

// newFakeTCP returns an open connection whose send buffer holds sendRoom
// bytes.
func newFakeTCP(sendRoom int) *fakeTCP {
	f := &fakeTCP{sendRoom: sendRoom}
	f.changed.start()
	return f
}

// await checks ready, with the lock held, after every change until it is
// over, failing with ErrTimedOut for operation once until passes.
func (f *fakeTCP) await(operation string, until deadline, ready func() (bool, error)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for {
		if done, err := ready(); done || err != nil {
			return err
		}
		pause, ok := timeToWait(until())
		if !ok {
			return &Error{Kind: ErrTimedOut, Operation: operation}
		}
		changed := f.changed.wait()
		f.waitingCalls++
		f.mu.Unlock()
		timer := time.NewTimer(pause)
		select {
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
		f.mu.Lock()
		f.waitingCalls--
	}
}

// awaitWaitingCall waits until a call waits for a change, and fails the
// test if none does within waitBudget.
func (f *fakeTCP) awaitWaitingCall(t *testing.T) {
	t.Helper()
	simtest.Eventually(t, waitBudget, "a call waits on the connection", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.waitingCalls > 0
	})
}

// change runs update with the lock held, then wakes whoever waits.
func (f *fakeTCP) change(update func()) {
	defer f.changed.notify()
	f.mu.Lock()
	defer f.mu.Unlock()
	update()
}

func (f *fakeTCP) trySend(p []byte) (int, error) {
	sent := 0
	err := f.clientCall(func() error {
		f.sendAttempts++
		if err := f.sendingError(); err != nil {
			return err
		}
		sent = min(len(p), f.sendRoom-f.toServer.Len())
		f.toServer.Write(p[:sent])
		return nil
	})
	return sent, err
}

func (f *fakeTCP) tryReceive(p []byte) (int, bool, error) {
	var (
		received int
		finished bool
	)
	err := f.clientCall(func() error {
		if f.clientReset {
			return &Error{Kind: ErrConnectionReset}
		}
		received = takeFrom(&f.toClient, p)
		finished = received == 0 && f.serverFinished
		return nil
	})
	return received, finished, err
}

func (f *fakeTCP) checkSendable() error {
	return f.clientCall(f.sendingError)
}

func (f *fakeTCP) waitWritable(until deadline) error {
	return f.await(operationTCPWrite, until, func() (bool, error) {
		return f.clientClosed || !f.maySend() || f.toServer.Len() < f.sendRoom, f.closedError()
	})
}

func (f *fakeTCP) waitReadable(until deadline, changed func() bool) error {
	return f.await(operationTCPRead, until, func() (bool, error) {
		return f.clientClosed || f.isReadable() || changed(), f.closedError()
	})
}

func (f *fakeTCP) waitReadableOrWritable(until deadline, changed func() bool) error {
	return f.await(operationTCPRead, until, func() (bool, error) {
		writable := !f.maySend() || f.toServer.Len() < f.sendRoom
		return f.clientClosed || f.isReadable() || writable || changed(), f.closedError()
	})
}

func (f *fakeTCP) announce(change func()) {
	f.change(change)
}

// isReadable reports whether a client's read would not wait, with the lock
// held: data arrived, or the stream ended.
func (f *fakeTCP) isReadable() bool {
	return f.toClient.Len() > 0 || f.serverFinished || f.clientReset
}

// maySend reports whether the client may still send, with the lock held.
func (f *fakeTCP) maySend() bool {
	return !f.clientFinished && !f.clientReset
}

// sendingError is why the client may not send, with the lock held: as a
// TCPConn reports it, a reset connection, or our half's end.
func (f *fakeTCP) sendingError() error {
	if !f.maySend() {
		return &Error{Kind: ErrConnectionReset}
	}
	return nil
}

func (f *fakeTCP) readUntil() time.Time  { return f.readDeadline.Load() }
func (f *fakeTCP) writeUntil() time.Time { return f.writeDeadline.Load() }

// WaitAcknowledged waits until the server has read everything sent, and
// fails with ErrConnectionReset once a reset dropped some of it.
func (f *fakeTCP) WaitAcknowledged(deadline time.Time) error {
	return f.await(operationTCPFlush, fixedDeadline(deadline), func() (bool, error) {
		if err := f.closedError(); err != nil {
			return true, err
		}
		if f.lostOnReset {
			return true, &Error{Kind: ErrConnectionReset}
		}
		return f.toServer.Len() == 0, nil
	})
}

// CloseWrite sends the client's FIN.
func (f *fakeTCP) CloseWrite() error {
	return f.clientCall(func() error {
		f.clientFinished = true
		return nil
	})
}

// Abort resets the connection, dropping what waits for the server.
func (f *fakeTCP) Abort() error {
	return f.clientCall(func() error {
		f.clientReset = true
		if f.toServer.Len() > 0 {
			f.lostOnReset = true
		}
		f.toServer.Reset()
		return nil
	})
}

// Close closes the client's end, which sends its FIN.
func (f *fakeTCP) Close() error {
	f.change(func() {
		f.clientClosed = true
		f.clientFinished = true
	})
	return nil
}

func (f *fakeTCP) LocalAddr() net.Addr      { return net.TCPAddrFromAddrPort(fakeClientAddress) }
func (f *fakeTCP) RemoteAddr() net.Addr     { return net.TCPAddrFromAddrPort(fakeServerAddress) }
func (f *fakeTCP) PeerAddr() netip.AddrPort { return fakeServerAddress }

func (f *fakeTCP) SetDeadline(deadline time.Time) error {
	return f.setDeadlines(deadline, &f.readDeadline, &f.writeDeadline)
}

func (f *fakeTCP) SetReadDeadline(deadline time.Time) error {
	return f.setDeadlines(deadline, &f.readDeadline)
}

func (f *fakeTCP) SetWriteDeadline(deadline time.Time) error {
	return f.setDeadlines(deadline, &f.writeDeadline)
}

// setDeadlines sets the client's deadlines, as a TCPConn's setters do.
func (f *fakeTCP) setDeadlines(deadline time.Time, deadlines ...*atomicTime) error {
	return f.clientCall(func() error {
		for _, stored := range deadlines {
			stored.Store(deadline)
		}
		return nil
	})
}

// clientCall runs call with the lock held unless the client's end is
// closed, and wakes whoever waits.
func (f *fakeTCP) clientCall(call func() error) error {
	var err error
	f.change(func() {
		if err = f.closedError(); err == nil {
			err = call()
		}
	})
	return err
}

// closedError is net.ErrClosed once the client's end is closed.
func (f *fakeTCP) closedError() error {
	if f.clientClosed {
		return net.ErrClosed
	}
	return nil
}

// finishFromServer sends the server's FIN without a TLS goodbye: a
// truncation.
func (f *fakeTCP) finishFromServer() {
	f.change(func() { f.serverFinished = true })
}

// awaitSentToClient waits until at least n bytes wait for the client.
func (f *fakeTCP) awaitSentToClient(n int) error {
	return f.await("delivery", fixedDeadline(time.Now().Add(waitBudget)), func() (bool, error) {
		return f.toClient.Len() >= n, nil
	})
}

// sendAttemptsSoFar returns how many times the client tried to send.
func (f *fakeTCP) sendAttemptsSoFar() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendAttempts
}

// waitingForClient returns how many bytes wait for the client.
func (f *fakeTCP) waitingForClient() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.toClient.Len()
}

// waitingForServer returns how many bytes wait for the server.
func (f *fakeTCP) waitingForServer() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.toServer.Len()
}

// isClientClosed reports whether the client closed its end.
func (f *fakeTCP) isClientClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clientClosed
}

// server returns the server's end, for tls.Server.
func (f *fakeTCP) server() net.Conn {
	return fakeServerEnd{tcp: f}
}

// fakeServerEnd is the server's end of a fakeTCP.
type fakeServerEnd struct {
	tcp *fakeTCP
}

func (s fakeServerEnd) Read(p []byte) (int, error) {
	f := s.tcp
	read := 0
	err := f.await("server read", f.serverReadDeadline.Load, func() (bool, error) {
		switch {
		case f.serverClosed:
			return true, net.ErrClosed
		case f.clientReset:
			return true, errFakeReset
		case f.toServer.Len() > 0:
			read = takeFrom(&f.toServer, p)
			return true, nil
		case f.clientFinished:
			return true, io.EOF
		default:
			return false, nil
		}
	})
	// What the server read made room in the client's send buffer.
	f.changed.notify()
	return read, err
}

func (s fakeServerEnd) Write(p []byte) (int, error) {
	var err error
	s.tcp.change(func() {
		if s.tcp.serverClosed {
			err = net.ErrClosed
			return
		}
		s.tcp.toClient.Write(p)
	})
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close closes the server's end, which sends its FIN.
func (s fakeServerEnd) Close() error {
	s.tcp.change(func() {
		s.tcp.serverClosed = true
		s.tcp.serverFinished = true
	})
	return nil
}

func (s fakeServerEnd) LocalAddr() net.Addr  { return net.TCPAddrFromAddrPort(fakeServerAddress) }
func (s fakeServerEnd) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(fakeClientAddress) }

func (s fakeServerEnd) SetDeadline(deadline time.Time) error { return s.SetReadDeadline(deadline) }

func (s fakeServerEnd) SetReadDeadline(deadline time.Time) error {
	s.tcp.change(func() { s.tcp.serverReadDeadline.Store(deadline) })
	return nil
}

// SetWriteDeadline does nothing: the server's writes never wait.
func (s fakeServerEnd) SetWriteDeadline(time.Time) error { return nil }

// takeFrom moves what fits of buffer into p and returns how much that was.
func takeFrom(buffer *bytes.Buffer, p []byte) int {
	taken := copy(p, buffer.Bytes())
	buffer.Next(taken)
	return taken
}
