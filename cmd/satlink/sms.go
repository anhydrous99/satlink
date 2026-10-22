package main

import (
	"cmp"
	"errors"
	"io"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/rustdebug"
	"github.com/spf13/cobra"
)

// `satlink sms`: sending, listing, reading and deleting text messages.

// smsFilters are the filters `sms list` takes, by the names the Rust
// version gave them, the default first.
var smsFilters = [...]struct {
	name   string
	filter satlink.SMSFilter
}{
	{name: "all", filter: satlink.SMSFilterAll},
	{name: "unread", filter: satlink.SMSFilterUnread},
	{name: "read", filter: satlink.SMSFilterRead},
	{name: "unsent", filter: satlink.SMSFilterUnsent},
	{name: "sent", filter: satlink.SMSFilterSent},
}

// smsCommand is `satlink sms` and its subcommands.
func (c *cli) smsCommand() *command {
	return newCommand(&cobra.Command{Use: "sms", Short: "SMS operations"}).withSubcommands(
		c.smsSendCommand(), c.smsListCommand(), c.smsReadCommand(), c.smsDeleteCommand())
}

// smsSendCommand is `satlink sms send`.
func (c *cli) smsSendCommand() *command {
	var number, text string
	return newCommand(&cobra.Command{
		Use:   "send",
		Short: "Send a text message",
		RunE: func(*cobra.Command, []string) error {
			return c.withHandset(c.reportingEvents(func(handset *satlink.Handset) error {
				reference, err := handset.SendSMS(number, text)
				if err != nil {
					return err
				}
				return writef(c.streams.stdout, "Sent (message reference %d)\n", reference)
			}))
		},
	}, requiredPositional("NUMBER", stringValue{target: &number}), requiredPositional("TEXT", stringValue{target: &text}))
}

// smsListCommand is `satlink sms list`.
func (c *cli) smsListCommand() *command {
	var filter satlink.SMSFilter
	return newCommand(&cobra.Command{
		Use:   "list",
		Short: "List stored messages",
		RunE: func(*cobra.Command, []string) error {
			return c.withHandset(c.reportingEvents(func(handset *satlink.Handset) error {
				messages, err := handset.ListSMS(filter)
				if err != nil {
					return err
				}
				return printMessages(c.streams.stdout, messages)
			}))
		},
	}, &argument{valueName: "FILTER", hasDefault: true, defaultValue: smsFilters[0].name,
		possibleValues: smsFilterValues(), value: smsFilterValue{target: &filter}})
}

// smsReadCommand is `satlink sms read`.
func (c *cli) smsReadCommand() *command {
	return c.storedMessageCommand("read", "Show one message", func(handset *satlink.Handset, index int) error {
		message, err := handset.ReadSMS(index)
		if err != nil {
			return err
		}
		return printMessage(c.streams.stdout, message)
	})
}

// smsDeleteCommand is `satlink sms delete`.
func (c *cli) smsDeleteCommand() *command {
	return c.storedMessageCommand("delete", "Delete one message", func(handset *satlink.Handset, index int) error {
		return handset.DeleteSMS(index)
	})
}

// storedMessageCommand is the `satlink sms` subcommand name, which acts on
// the message stored at the index it is given, a u32 in the Rust version.
func (c *cli) storedMessageCommand(name, short string, act func(*satlink.Handset, int) error) *command {
	var index uint32
	return newCommand(&cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(*cobra.Command, []string) error {
			return c.withHandset(c.reportingEvents(func(handset *satlink.Handset) error {
				return act(handset, int(index))
			}))
		},
	}, requiredPositional("INDEX", boundedValue[uint32]{target: &index}))
}

// smsFilterValues returns the values that `sms list` takes.
func smsFilterValues() []possibleValue {
	values := make([]possibleValue, len(smsFilters))
	for i, named := range smsFilters {
		values[i] = possibleValue{name: named.name}
	}
	return values
}

// smsFilterValue keeps the filter that `sms list` was given by name.
type smsFilterValue struct {
	target *satlink.SMSFilter
}

// Set chooses the filter called name. The command line gives it only the
// names smsFilterValues lists (parser.check): the refusal keeps the
// contract of argumentValue for any other caller.
func (v smsFilterValue) Set(name string) error {
	for _, named := range smsFilters {
		if named.name == name {
			*v.target = named.filter
			return nil
		}
	}
	return errors.New(rustdebug.Quote(name) + " is not an SMS filter")
}

// printMessages writes each message's header line — index, status, address
// and time — then its text, and a blank line.
func printMessages(w io.Writer, messages []satlink.SMSMessage) error {
	p := printer{w: w}
	for _, message := range messages {
		p.printf("[%d] %s %s %s\n%s\n\n", message.Index, message.Status, message.Address,
			message.Timestamp, message.Text)
	}
	return p.err
}

// printMessage writes a message's sender, time and status, a blank line,
// and its text.
func printMessage(w io.Writer, message satlink.SMSMessage) error {
	return writef(w, "From: %s\nTime: %s\nStatus: %s\n\n%s\n", message.Address,
		cmp.Or(message.Timestamp, "-"), message.Status, message.Text)
}
