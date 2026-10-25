package main

import (
	"context"
	"io"
	"time"

	"github.com/anhydrous99/satlink/iridium"
)

// monitorPollTimeout is how long each wait for events lasts before the
// next; the loop looks at its context between
// waits. Ctrl-C ends the process as it does by default: monitor catches no
// interruptions.
const monitorPollTimeout = time.Second

// monitorCommand is `satlink monitor`. A call whose CONNECT it reads, one
// the handset answered on its own, is hung up; when that fails, the command
// says how to end the call.
func (c *cli) monitorCommand() *command {
	return newCommand("monitor", "Print unsolicited events (incoming SMS, RING, ...) until interrupted",
		func(ctx context.Context) error {
			return c.withHandset(func(handset *iridium.Handset) error {
				err := monitor(ctx, handset, c.streams.stdout)
				warnIfCallMayStillBeUp(c.streams.stderr, err)
				return err
			})
		})
}

// monitor prints the handset's unsolicited result codes as they arrive
// (printEvents), until ctx ends; main's never does, so it runs until the
// process is interrupted.
func monitor(ctx context.Context, handset *iridium.Handset, stdout io.Writer) error {
	for ctx.Err() == nil {
		urcs, err := handset.PollURCs(monitorPollTimeout)
		if err != nil {
			return err
		}
		if err := printEvents(stdout, "", urcs); err != nil {
			return err
		}
	}
	return nil
}

// printEvents writes each of urcs on a line of its own, after prefix. They
// carry what the network passes on, such as a caller's number, so they show
// without their control characters (stripControls).
func printEvents(w io.Writer, prefix string, urcs []string) error {
	p := printer{w: w}
	for _, urc := range urcs {
		p.printf("%s%s\n", prefix, stripControls(urc))
	}
	return p.err
}
