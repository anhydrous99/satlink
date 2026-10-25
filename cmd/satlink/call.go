package main

import (
	"context"

	"github.com/anhydrous99/satlink/iridium"
)

// Voice calls: placing, answering and ending them.

// dialCommand is `satlink dial`.
func (c *cli) dialCommand() *command {
	var number string
	return newCommand("dial", "Place a voice call", func(context.Context) error {
		return c.withHandset(c.reportingEvents(func(handset *iridium.Handset) error {
			if err := handset.Dial(number); err != nil {
				return err
			}
			return writef(c.streams.stdout, "Dialling %s…\n", number)
		}))
	}, requiredPositional("NUMBER", "Phone number to call, in international format", stringValue{target: &number}))
}

// answerCommand is `satlink answer`.
func (c *cli) answerCommand() *command {
	return newCommand("answer", "Answer an incoming call", func(context.Context) error {
		return c.withHandset(c.reportingEvents((*iridium.Handset).Answer))
	})
}

// hangUpCommand is `satlink hangup`. Initialising the handset first ends a
// data call that an earlier session left up, so it also ends the call of a
// net command that could not hang up, as that command's warning says.
func (c *cli) hangUpCommand() *command {
	return newCommand("hangup", "End the current call", func(context.Context) error {
		return c.withHandset(c.reportingEvents((*iridium.Handset).HangUp))
	})
}
