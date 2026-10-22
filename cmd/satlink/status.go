package main

import (
	"fmt"
	"io"

	"github.com/anhydrous99/satlink"
	"github.com/spf13/cobra"
)

// fullSignalBars is the most bars an Iridium handset shows.
const fullSignalBars = 5

// statusCommand is `satlink status`: signal, battery and network
// registration.
func (c *cli) statusCommand() *command {
	return newCommand(&cobra.Command{
		Use:   "status",
		Short: "Signal strength, battery and network registration",
		RunE: func(*cobra.Command, []string) error {
			return c.withHandset(c.reportingEvents(func(handset *satlink.Handset) error {
				return printStatus(c.streams.stdout, handset)
			}))
		},
	})
}

// printStatus writes the handset's signal, battery and registration. Only
// the signal must be known; the others may be unavailable, and then say why.
// Each is asked for once the lines before it are written, as in the Rust
// version: output that nobody can read costs no further exchange.
func printStatus(w io.Writer, handset *satlink.Handset) error {
	bars, err := handset.SignalQuality()
	if err != nil {
		return err
	}
	if err := writef(w, "Signal:       %d/%d\n", bars, fullSignalBars); err != nil {
		return err
	}
	if err := writef(w, "Battery:      %s\n", describeBattery(handset.Battery())); err != nil {
		return err
	}
	return writef(w, "Registration: %s\n", describeRegistration(handset.Registration()))
}

// describeBattery is the charge level and status, or why they are
// unavailable.
func describeBattery(battery satlink.Battery, err error) string {
	if err != nil {
		return fmt.Sprintf("unavailable (%v)", err)
	}
	return fmt.Sprintf("%d%% (%s)", battery.Level, battery.Status)
}

// describeRegistration is the registration status, or why it is
// unavailable.
func describeRegistration(registration satlink.Registration, err error) string {
	if err != nil {
		return fmt.Sprintf("unavailable (%v)", err)
	}
	return registration.Status.String()
}
