package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/sim"
)

// Each value is asked for once the lines before it are written: output that
// nobody can read costs no further exchange with the handset, which may be slow
// to answer.
func TestAStatusAsksTheHandsetNothingMoreOnceALineCannotBeWritten(t *testing.T) {
	tests := []struct {
		failingLine string
		// linesTaken is how many lines the output takes before it fails.
		linesTaken int
		// asked are the commands the status sends.
		asked []string
	}{
		{"Signal", 0, []string{"AT+CSQ"}},
		{"Battery", 1, []string{"AT+CSQ", "AT+CBC"}},
		{"Registration", 2, []string{"AT+CSQ", "AT+CBC", "AT+CREG?"}},
	}
	for _, test := range tests {
		t.Run(test.failingLine, func(t *testing.T) {
			t.Parallel()
			transport := &commandRecorder{Handset: simtest.NewSimulator(t, simtest.SimulatorConfig())}
			handset := simtest.InitializedHandset(t, transport)
			transport.commands = nil // forget the initialisation's

			err := printStatus(&breakingWriter{writesLeft: test.linesTaken}, handset)

			if err == nil || !slices.Equal(transport.commands, test.asked) {
				t.Errorf("%v after asking %q; want a failure after asking %q", err, transport.commands, test.asked)
			}
		})
	}
}

func TestAReportTheHandsetCannotGiveSaysWhy(t *testing.T) {
	refused := &iridium.Error{Kind: iridium.ErrCommandFailed, Command: "AT+CBC"}

	if got, want := describeBattery(iridium.Battery{}, refused), `unavailable (handset returned ERROR for "AT+CBC")`; got != want {
		t.Errorf("battery: %q, want %q", got, want)
	}
	if got, want := describeRegistration(iridium.Registration{}, errors.New("no answer")), "unavailable (no answer)"; got != want {
		t.Errorf("registration: %q, want %q", got, want)
	}
}

func TestAReportShowsTheLevelAndTheStatusByName(t *testing.T) {
	battery := iridium.Battery{Status: iridium.ChargeStatus(7), Level: 55}
	registration := iridium.Registration{Status: iridium.RegistrationStatusRoaming}

	if got, want := describeBattery(battery, nil), "55% (Unknown(7))"; got != want {
		t.Errorf("battery: %q, want %q", got, want)
	}
	if got, want := describeRegistration(registration, nil), "Roaming"; got != want {
		t.Errorf("registration: %q, want %q", got, want)
	}
}

// commandRecorder is a simulated handset that keeps the command lines
// written to it, without the CR that ends each.
type commandRecorder struct {
	*sim.Handset
	commands []string
}

func (r *commandRecorder) WriteAll(p []byte) error {
	r.commands = append(r.commands, strings.TrimSuffix(string(p), "\r"))
	return r.Handset.WriteAll(p)
}

// breakingWriter is an output stream that takes writesLeft writes and then
// fails as brokenWriter does, as a pipe whose reader leaves part way.
type breakingWriter struct {
	writesLeft int
}

func (w *breakingWriter) Write(p []byte) (int, error) {
	if w.writesLeft == 0 {
		return brokenWriter{}.Write(p)
	}
	w.writesLeft--
	return len(p), nil
}
