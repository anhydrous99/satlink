package simtest

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/iridium"
)

// errPortGone is the failure of a port that went away.
var errPortGone = errors.New("the port is gone")

// recordingPort is a transport with a DTR line that records what reaches
// it, guarded by a mutex as the race detector requires.
type recordingPort struct {
	mu         sync.Mutex
	written    []string
	dtrChanges []string
	toRead     []byte
	timeouts   []time.Duration
	closed     int
	failure    error
}

func (p *recordingPort) WriteAll(data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return p.failure
	}
	p.written = append(p.written, string(data))
	return nil
}

func (p *recordingPort) ReadTimeout(buffer []byte, timeout time.Duration) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.timeouts = append(p.timeouts, timeout)
	if p.failure != nil {
		return 0, p.failure
	}
	count := copy(buffer, p.toRead)
	p.toRead = p.toRead[count:]
	return count, nil
}

func (p *recordingPort) RaiseDTR() error { return p.recordDTR("raise") }

func (p *recordingPort) DropDTR() error { return p.recordDTR("drop") }

func (p *recordingPort) recordDTR(change string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return p.failure
	}
	p.dtrChanges = append(p.dtrChanges, change)
	return nil
}

func (p *recordingPort) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed++
	return nil
}

// seen returns copies of what reached the port.
func (p *recordingPort) seen() (written, dtrChanges []string, closed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.written...), append([]string(nil), p.dtrChanges...), p.closed
}

// bareTransport is a transport without a DTR line that cannot be closed,
// as a pipe would be.
type bareTransport struct{}

func (bareTransport) WriteAll([]byte) error { return nil }

func (bareTransport) ReadTimeout([]byte, time.Duration) (int, error) { return 0, nil }

// failureRecorder is a testing.TB whose Fatalf records the failure instead
// of ending the test, so that a test can check how a helper fails.
type failureRecorder struct {
	testing.TB
	failures []string
}

func (r *failureRecorder) Helper() {}

func (r *failureRecorder) Fatalf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// assertTransportError fails the test unless err is an *iridium.Error of kind
// ErrTransport with message.
func assertTransportError(t *testing.T, err error, message string) {
	t.Helper()
	var handsetErr *iridium.Error
	if !errors.As(err, &handsetErr) || handsetErr.Kind != iridium.ErrTransport || handsetErr.Message != message {
		t.Errorf("got %v; want a transport error %q", err, message)
	}
}

// panicValue returns what call panicked with; nil if it did not panic.
func panicValue(call func()) (value any) {
	defer func() { value = recover() }()
	call()
	return nil
}
