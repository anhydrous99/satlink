package simtest

import (
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/sim"
)

// EscapeGuard is the escape guard (register S12) of the simulated handsets
// that [SimulatorConfig] describes: short, so that hanging up takes a
// fraction of a second.
const EscapeGuard = 20 * time.Millisecond

// CallEscapeGuard is the escape guard for data calls to a simulator with
// [EscapeGuard]: comfortably above it, as satlink.DefaultEscapeGuard is
// above a real handset's.
const CallEscapeGuard = 3 * EscapeGuard

// SimulatorConfig returns sim.DefaultConfig with the escape guard
// [EscapeGuard].
func SimulatorConfig() sim.Config {
	config := sim.DefaultConfig()
	config.EscapeGuard = EscapeGuard
	return config
}

// NewSimulator returns a simulated handset made with config and closes it
// when the test and its subtests have finished (t.Cleanup), which drops DTR
// and so ends any call left up. Make it before anything whose own cleanup
// needs the handset, such as a link to Close: cleanups run last in, first
// out.
func NewSimulator(t testing.TB, config sim.Config) *sim.Handset {
	t.Helper()
	simulator := sim.New(config)
	// Closing an in-memory simulator cannot fail: sim.Handset.Close always
	// returns nil.
	t.Cleanup(func() { _ = simulator.Close() })
	return simulator
}

// NewHandset returns a satlink handset over a new simulator made with
// config, as NewSimulator makes one, initialised as InitializedHandset
// does, and the simulator's control handle.
func NewHandset(t testing.TB, config sim.Config) (*satlink.Handset, *sim.Control) {
	t.Helper()
	simulator := NewSimulator(t, config)
	return InitializedHandset(t, simulator), simulator.Control()
}

// InitializedHandset returns a satlink handset over transport, initialised
// (satlink.Handset.Initialize). A failure to initialise fails the test at
// once. It closes nothing: the transport is the caller's.
func InitializedHandset(t testing.TB, transport satlink.Transport) *satlink.Handset {
	t.Helper()
	handset := satlink.NewHandset(transport)
	if err := handset.Initialize(); err != nil {
		t.Fatalf("initialising the handset: %v", err)
	}
	return handset
}
