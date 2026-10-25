package main

import (
	"cmp"
	"context"
	"io"

	"github.com/anhydrous99/satlink/serial"
)

// listCommand is `satlink list`: the handsets on USB. It opens none of them,
// so --simulate changes nothing.
func (c *cli) listCommand() *command {
	return newCommand("list", "List connected handsets", func(context.Context) error {
		ports, err := c.env.discoverHandsets()
		if err != nil {
			return err
		}
		return printPorts(c.streams.stdout, ports)
	})
}

// printPorts writes a line per port — path, USB product ID, model and serial
// number, separated by tabs — or that there is none.
func printPorts(w io.Writer, ports []serial.Port) error {
	p := printer{w: w}
	if len(ports) == 0 {
		p.printf("No Iridium handsets found.\n")
	}
	for _, port := range ports {
		p.printf("%s\t%04x\t%s\t%s\n", port.Path, port.ProductID,
			cmp.Or(port.Model, "unknown model"), cmp.Or(port.SerialNumber, "-"))
	}
	return p.err
}
