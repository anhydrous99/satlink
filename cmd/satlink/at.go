package main

import (
	"context"
	"io"
	"strconv"
	"time"

	"github.com/anhydrous99/satlink/iridium"
)

// defaultATTimeoutSeconds is how long `satlink at` waits for the final
// result unless told otherwise: longer than the five seconds ordinary commands
// get, as a raw command may be a slow one.
const defaultATTimeoutSeconds = 10

// atCommand is `satlink at`: a command, raw, unless it can go online. A call
// that comes up during it all the same is hung up; when that fails, the
// command says how to end the call.
func (c *cli) atCommand() *command {
	var request string
	var timeoutSeconds uint64
	return newCommand("at", "Send a raw AT command, e.g. `"+programName+" at AT+CSQ`", func(context.Context) error {
		timeout := saturatingDuration(timeoutSeconds, time.Second)
		return c.withHandset(c.reportingEvents(func(handset *iridium.Handset) error {
			response, err := handset.CommandTimeout(request, timeout)
			if err != nil {
				return err
			}
			return printResponse(c.streams.stdout, response)
		}))
	},
		requiredPositional("COMMAND", "AT command to send, such as AT+CSQ", stringValue{target: &request}),
		&argument{long: "timeout", short: 't', valueName: "TIMEOUT", help: "Seconds to wait for the final result",
			hasDefault: true, defaultValue: strconv.Itoa(defaultATTimeoutSeconds),
			value: unsignedValue{target: &timeoutSeconds}},
	)
}

// printResponse writes the response's information lines, then its final
// result, OK.
func printResponse(w io.Writer, response iridium.Response) error {
	if err := printLines(w, response.Lines); err != nil {
		return err
	}
	return writef(w, "OK\n")
}
