package main

import (
	"io"

	"github.com/anhydrous99/satlink"
	"github.com/spf13/cobra"
)

// infoCommand is `satlink info`: who made the handset, and what it is.
func (c *cli) infoCommand() *command {
	return newCommand(&cobra.Command{
		Use:   "info",
		Short: "Manufacturer, model, firmware and IMEI",
		RunE: func(*cobra.Command, []string) error {
			return c.withHandset(c.reportingEvents(func(handset *satlink.Handset) error {
				info, err := handset.Info()
				if err != nil {
					return err
				}
				return printInfo(c.streams.stdout, info)
			}))
		},
	})
}

// printInfo writes the handset's identity, a field per line.
func printInfo(w io.Writer, info satlink.DeviceInfo) error {
	p := printer{w: w}
	p.printf("Manufacturer: %s\n", info.Manufacturer)
	p.printf("Model:        %s\n", info.Model)
	p.printf("Firmware:     %s\n", info.Revision)
	p.printf("IMEI:         %s\n", info.IMEI)
	return p.err
}
