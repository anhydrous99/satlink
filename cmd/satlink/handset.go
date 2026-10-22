package main

import (
	"strconv"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/serial"
)

// The handset that the global options name — a simulator, or the serial
// port given or discovered — opened for a command and closed after it.

// portVariable is the environment variable that stands in for --port.
const portVariable = "SATLINK_PORT"

// globalOptions are the options that every command takes: which handset to
// use, and how much to log.
type globalOptions struct {
	// port is the serial device, if given; without one, the handset is
	// found by its USB vendor ID.
	port givenText
	// baud is the serial port's baud rate.
	baud uint32
	// simulate asks for a simulated handset instead of a real one.
	simulate simulation
	// verbosity counts the -v flags.
	verbosity int
}

// arguments declares the options, which every command takes, as clap's
// global arguments.
func (o *globalOptions) arguments() []*argument {
	return []*argument{
		{long: "port", short: 'p', valueName: "PORT", global: true, env: portVariable,
			help: "Serial device (auto-detected by USB vendor ID when omitted)", value: &o.port},
		{long: "baud", short: 'b', valueName: "BAUD", global: true,
			hasDefault: true, defaultValue: strconv.Itoa(serial.DefaultBaud), value: boundedValue[uint32]{target: &o.baud}},
		{long: "simulate", valueName: "SIMULATE", global: true, missingValue: defaultLinkProfile,
			possibleValues: linkProfileValues(), value: &o.simulate,
			help: "Use a simulated handset instead of a real one, with Iridium's real timing (the default) or instant responses"},
		{long: "verbose", short: 'v', action: actionCount, global: true, value: countValue{target: &o.verbosity},
			help: "Log what the link is doing (-v), in more detail (-vv, -vvv)"},
	}
}

// withHandset opens the simulated handset, or the serial port given or
// discovered, readies it for commands and lets use have it. It closes the
// handset again whatever happens — a panic included, as the panic unwinds.
func (c *cli) withHandset(use func(*iridium.Handset) error) error {
	transport, err := c.openTransport()
	if err != nil {
		return err
	}
	handset := iridium.NewHandset(transport)
	defer closeHandset(handset)
	if err := handset.Initialize(); err != nil {
		return err
	}
	return use(handset)
}

// reportingEvents returns use followed, when it succeeds, by the unsolicited
// result codes the handset sent meanwhile, on standard error.
func (c *cli) reportingEvents(use func(*iridium.Handset) error) func(*iridium.Handset) error {
	return func(handset *iridium.Handset) error {
		if err := use(handset); err != nil {
			return err
		}
		p := printer{w: c.streams.stderr}
		for _, urc := range handset.TakeURCs() {
			p.printf("(event) %s\n", urc)
		}
		return p.err
	}
}

// openTransport returns a simulated handset when --simulate asks for one,
// and otherwise the serial port given or discovered.
func (c *cli) openTransport() (iridium.Transport, error) {
	if config, simulated := c.options.simulate.config(); simulated {
		return c.env.simulateHandset(config), nil
	}
	path, err := c.portPath()
	if err != nil {
		return nil, err
	}
	port, err := serial.Open(path, int(c.options.baud))
	if err != nil {
		return nil, err
	}
	return port, nil
}

// portPath is the serial device given — even an empty one, which then fails
// to open — or else the only handset discovered, as serial.ChoosePort chooses
// it.
func (c *cli) portPath() (string, error) {
	if c.options.port.given {
		return c.options.port.text, nil
	}
	ports, err := c.env.discoverHandsets()
	if err != nil {
		return "", err
	}
	return serial.ChoosePortFrom(ports)
}

// closeHandset closes the handset's transport, which drops DTR, once the
// command is done with it.
func closeHandset(handset *iridium.Handset) {
	// Closing the port is the last thing done with the handset, and a failure
	// has no remedy: it is not reported.
	_ = handset.Close()
}
