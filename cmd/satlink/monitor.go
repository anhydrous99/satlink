package main

import (
	"context"
	"io"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/spf13/cobra"
)

// monitorPollTimeout is how long each wait for events lasts before the
// next; the loop looks at its context between
// waits. Ctrl-C ends the process as it does by default: monitor catches no
// interruptions.
const monitorPollTimeout = time.Second

// monitorCommand is `satlink monitor`.
func (c *cli) monitorCommand() *command {
	return newCommand(&cobra.Command{
		Use:   "monitor",
		Short: "Print unsolicited events (incoming SMS, RING, ...) until interrupted",
		RunE: func(command *cobra.Command, _ []string) error {
			return c.withHandset(func(handset *satlink.Handset) error {
				return monitor(command.Context(), handset, c.streams.stdout)
			})
		},
	})
}

// monitor prints the handset's unsolicited result codes as they arrive,
// until ctx ends; main's never does, so it runs until the process is
// interrupted.
func monitor(ctx context.Context, handset *satlink.Handset, stdout io.Writer) error {
	for ctx.Err() == nil {
		urcs, err := handset.PollURCs(monitorPollTimeout)
		if err != nil {
			return err
		}
		if err := printLines(stdout, urcs); err != nil {
			return err
		}
	}
	return nil
}
