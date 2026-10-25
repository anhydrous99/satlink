package main

import (
	"context"
	"io"

	"github.com/anhydrous99/satlink/iridium"
)

// infoCommand is `satlink info`: who made the handset, and what it is.
func (c *cli) infoCommand() *command {
	return newCommand("info", "Manufacturer, model, firmware and IMEI", func(context.Context) error {
		return c.withHandset(c.reportingEvents(func(handset *iridium.Handset) error {
			info, err := handset.Info()
			if err != nil {
				return err
			}
			return printInfo(c.streams.stdout, info)
		}))
	})
}

// printInfo writes the handset's identity, a field per line.
func printInfo(w io.Writer, info iridium.DeviceInfo) error {
	p := printer{w: w}
	p.printf("Manufacturer: %s\n", info.Manufacturer)
	p.printf("Model:        %s\n", info.Model)
	p.printf("Firmware:     %s\n", info.Revision)
	p.printf("IMEI:         %s\n", info.IMEI)
	return p.err
}
