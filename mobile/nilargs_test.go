package mobile

import (
	"testing"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/sim"
)

// The app's language can pass null for any object or interface argument,
// which reaches Go as nil. Every function refuses that with
// SatlinkErrorInvalidArgument, naming the parameter, but for a link's
// listener, which may be nil.

// linkTimeoutMillis bounds each link operation of these tests.
const linkTimeoutMillis = 10_000

// nilArgumentCall is a call with one nil argument, named parameter.
type nilArgumentCall struct {
	name      string
	call      func() error
	parameter string
}

// nilArgumentCalls are the calls of every function with a nil argument;
// transport is the one usable argument the calls hand over.
func nilArgumentCalls(transport ByteTransport) []nilArgumentCall {
	return []nilArgumentCall{
		{
			name:      "NewIridiumHandset",
			call:      func() error { _, err := NewIridiumHandset(nil); return err },
			parameter: "transport",
		},
		{
			name:      "EstablishLink without a transport",
			call:      func() error { _, err := EstablishLink(nil, NewLinkSettings(), nil, nil); return err },
			parameter: "transport",
		},
		{
			name:      "EstablishLink without settings",
			call:      func() error { _, err := EstablishLink(transport, nil, nil, nil); return err },
			parameter: "settings",
		},
		{
			name:      "EstablishLinkWithHandset without a handset",
			call:      func() error { _, err := EstablishLinkWithHandset(nil, NewLinkSettings(), nil, nil); return err },
			parameter: "handset",
		},
		{
			name:      "EstablishLinkWithHandset without settings",
			call:      func() error { _, err := EstablishLinkWithHandset(&IridiumHandset{}, nil, nil, nil); return err },
			parameter: "settings",
		},
		{
			name:      "NewTLSConnector",
			call:      func() error { _, err := NewTLSConnector(nil); return err },
			parameter: "settings",
		},
		{
			name:      "SetLogSink",
			call:      func() error { return SetLogSink(nil, LogLevelInfo) },
			parameter: "sink",
		},
	}
}

func TestFunctionsRefuseNilArgumentsByName(t *testing.T) {
	transport := &recordingTransport{}
	for _, c := range nilArgumentCalls(transport) {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()

			reason := c.parameter + " must not be nil"
			assertSatlinkError(t, err, &SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: reason}, "invalid argument: "+reason)
		})
	}
	if len(transport.written) != 0 {
		t.Errorf("a refused call wrote %q to the transport", transport.written)
	}
}

// simulatedLink returns a link over a simulated handset, hung up when the
// test ends.
func simulatedLink(t *testing.T) *SatelliteLink {
	t.Helper()
	transport := simtest.NewByteTransport(simtest.NewSimulator(t, sim.DefaultConfig()))
	link, err := EstablishLink(transport, NewLinkSettings(), nil, nil)
	if err != nil {
		t.Fatalf("EstablishLink: %v", err)
	}
	HangUpAtCleanup(t, link)
	return link
}

func TestOpenTLSRefusesANilOrZeroConnector(t *testing.T) {
	link := simulatedLink(t)
	cases := []struct {
		name      string
		connector *TLSConnector
		reason    string
	}{
		{name: "nil", connector: nil, reason: "connector must not be nil"},
		{name: "zero", connector: &TLSConnector{}, reason: "the TLSConnector was not made by this library"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := link.OpenTLS("secure.test", 443, c.connector, linkTimeoutMillis)

			assertSatlinkError(t, err, &SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: c.reason}, "invalid argument: "+c.reason)
		})
	}
}
