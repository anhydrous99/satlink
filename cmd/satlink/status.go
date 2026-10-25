package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/anhydrous99/satlink/iridium"
)

// fullSignalBars is the most bars an Iridium handset shows.
const fullSignalBars = 5

// statusCommand is `satlink status`: signal, battery and network
// registration.
func (c *cli) statusCommand() *command {
	return newCommand("status", "Signal strength, battery and network registration", func(context.Context) error {
		return c.withHandset(c.reportingEvents(func(handset *iridium.Handset) error {
			return printStatus(c.streams.stdout, handset)
		}))
	})
}

// printStatus writes the handset's signal, battery and registration. Only
// the signal must be known; the others may be unavailable, and then say why.
// Each is asked for once the lines before it are written: output that nobody
// can read costs no further exchange.
func printStatus(w io.Writer, handset *iridium.Handset) error {
	// The last reading: a new one may take the handset fifty seconds.
	bars, err := handset.LastSignalQuality()
	if err != nil {
		return err
	}
	if err := writef(w, "Signal:       %d/%d\n", bars, fullSignalBars); err != nil {
		return err
	}
	battery, batteryErr := handset.Battery()
	if err := printReading(w, "Battery:      %s\n", describeBattery(battery, batteryErr), batteryErr); err != nil {
		return err
	}
	registration, registrationErr := handset.Registration()
	return printReading(w, "Registration: %s\n", describeRegistration(registration, registrationErr), registrationErr)
}

// printReading writes a reading's line, format with its description. When
// reading it failed with readErr and left a call that may still be up, it
// then fails with readErr, even if the line could not be written: the
// command must not succeed, and it says how to end the call
// (reportingEvents).
func printReading(w io.Writer, format, description string, readErr error) error {
	writeErr := writef(w, format, description)
	if errors.Is(readErr, iridium.ErrCallMayStillBeUp) {
		return readErr
	}
	return writeErr
}

// describeBattery is the charge in percent and the status, with the
// charger's, or the bars and the status from a handset that reports no
// percentage; or why they are unavailable.
func describeBattery(battery iridium.Battery, err error) string {
	if err != nil {
		return fmt.Sprintf("unavailable (%v)", err)
	}
	if !battery.LevelReported {
		return fmt.Sprintf("%d bars (%s)", battery.Bars, battery.Status)
	}
	return fmt.Sprintf("%d%% (%s, charger %s)", battery.Level, battery.Status, battery.Charger)
}

// describeRegistration is the registration status, or why it is
// unavailable.
func describeRegistration(registration iridium.Registration, err error) string {
	if err != nil {
		return fmt.Sprintf("unavailable (%v)", err)
	}
	return registration.Status.String()
}
