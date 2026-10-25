package iridium_test

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/iridium"
)

// Test support shared by the integration tests: a transport that plays a
// script, one that never answers, and helpers that wait with deadlines.

// dtrDropped stands in a script for DTR dropping, which is not a write.
const dtrDropped = "<DTR dropped>"

// replyChunk is the most a scripted read delivers, so that reassembling
// lines split across reads is exercised.
const replyChunk = 7

// longestNap is the most a scripted read with no reply on its way sleeps:
// a short nap spares the CPU in long waits, such as the ten-second abort
// grace, which would otherwise spin.
const longestNap = time.Millisecond

// errUnplugged is what a transport whose cable is pulled reports.
var errUnplugged = errors.New("unplugged")

// step is one step of a script: once write has been written, reply comes.
type step struct {
	write string
	reply string
}

// scriptStep is a step as the scripted transport plays it: its reply comes
// delay after its write.
type scriptStep struct {
	step
	delay time.Duration
}

// reply is what the handset sends, from a moment on.
type reply struct {
	// arrival is when the reply arrives: never before one sent ahead of it.
	arrival time.Time
	data    []byte
}

// timedWrite is one write and when it happened.
type timedWrite struct {
	at   time.Time
	data string
}

// dtrChange is one change of the DTR line and when it happened.
type dtrChange struct {
	at     time.Time
	raised bool
}

// scripted plays back a script of steps. It implements iridium.Transport
// and iridium.DTRController, and is safe for concurrent use: a test looks
// at it while a handset uses it from another goroutine.
//
// Replies come at once unless a test makes them late, as a real handset's
// can be (answersLate, unsolicitedAfter): a quiet a handset waits for
// shows only through a reply that arrives while it waits.
type scripted struct {
	mu     sync.Mutex
	script []scriptStep
	// replies are what the handset sent and nobody has read yet, oldest
	// first.
	replies []reply
	// written is what was written since the last step played.
	written []byte
	// allWritten is everything written, in order.
	allWritten []byte
	writes     []timedWrite
	dtrChanges []dtrChange
	// unplugsWhenPlayedOut makes every call fail once the script has played
	// and every reply has been read.
	unplugsWhenPlayedOut bool
	// readsWhole has a read deliver all that has arrived, not replyChunk
	// bytes at most.
	readsWhole bool
}

var (
	_ iridium.Transport     = (*scripted)(nil)
	_ iridium.DTRController = (*scripted)(nil)
)

// newScripted returns a transport that plays steps in order, each reply
// coming at once.
func newScripted(steps ...step) *scripted {
	script := make([]scriptStep, len(steps))
	for i, played := range steps {
		script[i] = scriptStep{step: played}
	}
	return &scripted{script: script}
}

// unsolicited queues data as if the handset had sent it unprompted.
func (s *scripted) unsolicited(data string) *scripted {
	return s.unsolicitedAfter(0, data)
}

// unsolicitedAfter queues data as if the handset sent it unprompted, delay
// from now: the late answer to a command that timed out, say.
func (s *scripted) unsolicitedAfter(delay time.Duration, data string) *scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.send(time.Now().Add(delay), data)
	return s
}

// answersLate makes the reply of the first step still to play whose write
// is write come delay after that write, as a slow handset's answer does. It
// panics when no such step is left: the test is wrong.
func (s *scripted) answersLate(write string, delay time.Duration) *scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.script {
		if s.script[i].write == write {
			s.script[i].delay = delay
			return s
		}
	}
	panic(fmt.Sprintf("no step of the script writes %q", write))
}

// unpluggedOncePlayedOut makes every call fail, as if the cable were
// pulled, once the script has played and every reply has been read.
func (s *scripted) unpluggedOncePlayedOut() *scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unplugsWhenPlayedOut = true
	return s
}

// inWholeReads has every read deliver all that has arrived, as one USB
// transfer can carry a reply and what the handset sent straight after it.
func (s *scripted) inWholeReads() *scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readsWhole = true
	return s
}

// history returns everything written so far, in order.
func (s *scripted) history() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.allWritten)
}

// unplayedSteps returns how many steps' expected writes never happened.
func (s *scripted) unplayedSteps() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.script)
}

// writtenAt returns when data was first written in one piece.
func (s *scripted) writtenAt(data string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, write := range s.writes {
		if write.data == data {
			return write.at, true
		}
	}
	return time.Time{}, false
}

// dtrChangesSoFar returns every DTR change and when it happened, in order.
func (s *scripted) dtrChangesSoFar() []dtrChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dtrChange(nil), s.dtrChanges...)
}

// WriteAll records p and plays the next step once p completes its write.
func (s *scripted) WriteAll(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isUnplugged() {
		return errUnplugged
	}
	s.allWritten = append(s.allWritten, p...)
	s.writes = append(s.writes, timedWrite{at: time.Now(), data: string(p)})
	s.advance(p)
	return nil
}

// ReadTimeout delivers the replies that have arrived, in small chunks. A
// reply on its way is waited for if it arrives within timeout; otherwise
// the read waits out timeout and delivers nothing. The read decides which
// at its start, so that a slow machine can make a reply arrive sooner than
// the handset expects, never later: a test of a wait long enough for a late
// reply cannot fail for being slow. With no reply on its way, the read only
// naps.
func (s *scripted) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	wait, arrives := s.nextArrival(timeout)
	time.Sleep(wait)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isUnplugged() {
		return 0, errUnplugged
	}
	if !arrives {
		return 0, nil
	}
	return s.deliver(p), nil
}

// nextArrival returns how long a read with timeout sleeps, and whether a
// reply has arrived by then.
func (s *scripted) nextArrival(timeout time.Duration) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.replies) == 0 {
		// Nothing will come: a short nap spares the CPU in long waits.
		return min(timeout, longestNap), false
	}
	untilArrival := max(time.Until(s.replies[0].arrival), 0)
	if untilArrival > max(timeout, 0) {
		return max(timeout, 0), false
	}
	return untilArrival, true
}

// deliver moves into p what has arrived of the oldest replies, at most
// replyChunk bytes unless the transport reads whole, and returns how much
// that was.
func (s *scripted) deliver(p []byte) int {
	chunk := p
	if !s.readsWhole {
		chunk = p[:min(len(p), replyChunk)]
	}
	delivered := 0
	for delivered < len(chunk) && len(s.replies) > 0 && !s.replies[0].arrival.After(time.Now()) {
		oldest := &s.replies[0]
		taken := copy(chunk[delivered:], oldest.data)
		oldest.data = oldest.data[taken:]
		delivered += taken
		if len(oldest.data) == 0 {
			s.replies = s.replies[1:]
		}
	}
	return delivered
}

// RaiseDTR records a raised DTR line.
func (s *scripted) RaiseDTR() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isUnplugged() {
		return errUnplugged
	}
	s.dtrChanges = append(s.dtrChanges, dtrChange{at: time.Now(), raised: true})
	return nil
}

// DropDTR records a dropped DTR line, which plays a dtrDropped step.
func (s *scripted) DropDTR() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isUnplugged() {
		return errUnplugged
	}
	s.dtrChanges = append(s.dtrChanges, dtrChange{at: time.Now(), raised: false})
	s.advance([]byte(dtrDropped))
	return nil
}

// isUnplugged reports whether the transport fails every call by now.
func (s *scripted) isUnplugged() bool {
	return s.unplugsWhenPlayedOut && len(s.script) == 0 && len(s.replies) == 0
}

// advance plays the next step once data completes its expected write.
func (s *scripted) advance(data []byte) {
	s.written = append(s.written, data...)
	if len(s.script) == 0 || !bytes.HasSuffix(s.written, []byte(s.script[0].write)) {
		return
	}
	played := s.script[0]
	s.script = s.script[1:]
	s.written = nil
	s.send(time.Now().Add(played.delay), played.reply)
}

// send queues data, arriving at arrival, or with the reply sent ahead of it
// if that one arrives later: the handset says one thing after another.
// Nothing comes of empty data.
func (s *scripted) send(arrival time.Time, data string) {
	if data == "" {
		return
	}
	if sent := len(s.replies); sent > 0 && s.replies[sent-1].arrival.After(arrival) {
		arrival = s.replies[sent-1].arrival
	}
	s.replies = append(s.replies, reply{arrival: arrival, data: []byte(data)})
}

// blockingTransport never answers: a read waits out its timeout, or fails
// once the transport is closed. It records writes and implements io.Closer,
// for the tests of what may happen while a dial waits.
type blockingTransport struct {
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	written   []byte
}

// newBlockingTransport returns an open blockingTransport.
func newBlockingTransport() *blockingTransport {
	return &blockingTransport{closed: make(chan struct{})}
}

// WriteAll records p.
func (b *blockingTransport) WriteAll(p []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.written = append(b.written, p...)
	return nil
}

// ReadTimeout waits out timeout, or fails once the transport is closed.
func (b *blockingTransport) ReadTimeout(_ []byte, timeout time.Duration) (int, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-b.closed:
		return 0, errors.New("the blocking transport is closed")
	case <-timer.C:
		return 0, nil
	}
}

// Close makes reads fail, the one in progress included.
func (b *blockingTransport) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

// hasWritten reports whether data has been written.
func (b *blockingTransport) hasWritten(data string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.written, []byte(data))
}

// commandTimeout is the handset timeout of the tests: short, as nothing
// they script ever takes long to answer.
const commandTimeout = 50 * time.Millisecond

// newTestHandset returns a handset over transport with commandTimeout.
func newTestHandset(transport iridium.Transport) *iridium.Handset {
	handset := iridium.NewHandset(transport)
	handset.SetTimeout(commandTimeout)
	return handset
}

// pollInterval is how often waitUntil looks at its condition.
const pollInterval = time.Millisecond

// waitUntil polls condition until it holds, failing the test if it does not
// within timeout.
func waitUntil(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting until %s", timeout, what)
		}
		time.Sleep(pollInterval)
	}
}

// finishesWithin runs action in another goroutine and fails the test if it
// does not finish within timeout: the action is blocked.
func finishesWithin(t *testing.T, timeout time.Duration, what string, action func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		action()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("%s did not finish within %v", what, timeout)
	}
}
