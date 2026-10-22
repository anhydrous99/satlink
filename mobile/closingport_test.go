package mobile

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/sim"
)

// portPatience is how long a test waits for a port to close once the call
// that needed it has ended.
const portPatience = 10 * time.Second

// closingPort stands in for a serial port that this package closes itself:
// a simulated handset, behind a FaultyTransport so that it can be
// unplugged, recording whether it was closed.
type closingPort struct {
	*simtest.FaultyTransport
	// control is the simulated handset's.
	control *sim.Control
	closed  atomic.Bool
}

// newClosingPort returns a closingPort over a new simulated handset with
// the default configuration.
func newClosingPort(t *testing.T) *closingPort {
	t.Helper()
	return newClosingPortWith(t, sim.DefaultConfig())
}

// newClosingPortWith returns a closingPort over a new simulated handset
// made with config.
func newClosingPortWith(t *testing.T, config sim.Config) *closingPort {
	t.Helper()
	simulator := simtest.NewSimulator(t, config)
	return &closingPort{FaultyTransport: simtest.NewFaultyTransport(simulator), control: simulator.Control()}
}

func (p *closingPort) Close() error {
	p.closed.Store(true)
	return p.FaultyTransport.Close()
}

// connectResult is the start of the handset's answer to a dial that brought
// the call up.
var connectResult = []byte("CONNECT")

// bugOnceConnected is what connectPanickingPort panics with.
const bugOnceConnected = "a transport bug once the call was up"

// connectPanickingPort is a closingPort whose read that brings CONNECT
// panics, as a buggy transport might once the call is up.
type connectPanickingPort struct {
	*closingPort
	// received is what the handset has read so far, from the goroutine
	// that dials, one read at a time.
	received []byte
	// panicked records the panic, which comes once.
	panicked bool
}

func (p *connectPanickingPort) ReadTimeout(buffer []byte, timeout time.Duration) (int, error) {
	n, err := p.closingPort.ReadTimeout(buffer, timeout)
	p.received = append(p.received, buffer[:n]...)
	if !p.panicked && bytes.Contains(p.received, connectResult) {
		p.panicked = true
		panic(bugOnceConnected)
	}
	return n, err
}

// bugWhileClosing is what closePanickingPort panics with.
const bugWhileClosing = "a port bug while closing"

// closePanickingPort is a closingPort whose Close panics once the port is
// closed, as a buggy port might.
type closePanickingPort struct {
	*closingPort
}

func (p *closePanickingPort) Close() error {
	// The port is closed, which ends any call, whatever closing it says:
	// the panic after it is the fault.
	_ = p.closingPort.Close()
	panic(bugWhileClosing)
}
