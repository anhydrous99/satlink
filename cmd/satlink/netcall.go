package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/satnet"
)

// A net subcommand runs during a data call, which is hung up whatever
// happens: a forgotten data call keeps airtime billing by the minute.

// errCommandPanicked is the outcome of a subcommand that panicked, after
// which the call is hung up all the same.
var errCommandPanicked = errors.New("internal error: the command panicked")

// runNet answers the subcommand without a call when it can; otherwise it
// dials, runs the subcommand during the call, and hangs up. From the dial
// on, it catches interruptions (interrupt.go).
func (c *cli) runNet(handset *iridium.Handset, arguments netArguments) error {
	if address, ok := arguments.action.literalAddress(); ok {
		// An address needs no lookup, so no call either.
		return writef(c.streams.stdout, "%s\n", address)
	}
	config, err := arguments.linkConfig()
	if err != nil {
		return err
	}
	interrupted := c.interruptions.catch(c.env.notifyInterruptions, c.env.quitInterrupted, c.streams.stderr)
	link, err := c.establish(handset, config, interrupted)
	if err != nil {
		return err
	}
	return c.duringCall(link, arguments.action, interrupted)
}

// establish announces the dial and brings the link up, unless interrupted
// ends first, which cancels the setup. When the link does not come up and
// its call may still be up, it says what to do.
func (c *cli) establish(handset *iridium.Handset, config satnet.Config, interrupted context.Context) (*satnet.Link, error) {
	stopCancelling := context.AfterFunc(interrupted, config.Call.Cancellation.Cancel)
	defer stopCancelling()
	err := writef(c.streams.stderr, "Dialling %s — call setup over Iridium takes about half a minute…\n", config.Number)
	if err != nil {
		return nil, err
	}
	link, err := satnet.Establish(handset, config)
	warnIfCallMayStillBeUp(c.streams.stderr, err)
	return link, err
}

// duringCall shows what the link negotiated and does the action over it,
// then hangs up and says what the call carried. The result is the action's
// outcome when the hang-up succeeded — errInterrupted when interrupted
// ended first — and otherwise the hang-up's failure.
func (c *cli) duringCall(link *satnet.Link, action netAction, interrupted context.Context) error {
	watcher := link.Watcher()
	// Nothing may skip the hang-up below: an error becomes the outcome, and
	// so do a panic and an interruption.
	stopClosing := context.AfterFunc(interrupted, link.RequestClose)
	outcome := outcomeOf(c.streams.stderr, func() error {
		if err := printLink(c.streams.stderr, link.Status()); err != nil {
			return err
		}
		return action.execute(link, c.streams)
	})
	if !stopClosing() {
		outcome = errInterrupted
	}
	hungUp := link.Close()
	printCallSummary(c.streams.stderr, watcher.Status())
	return netResult(c.streams.stderr, outcome, hungUp)
}

// outcomeOf returns what do returns, or errCommandPanicked should it panic,
// once the panic is reported on stderr.
func outcomeOf(stderr io.Writer, do func() error) (outcome error) {
	defer func() {
		if value := recover(); value != nil {
			reportPanic(stderr, value)
			outcome = errCommandPanicked
		}
	}()
	return do()
}

// netResult combines the subcommand's outcome and the hang-up's result: the
// outcome when the hang-up succeeded, and otherwise the hang-up's failure,
// after the outcome's error, if any, on stderr.
func netResult(stderr io.Writer, outcome, hangUp error) error {
	if hangUp == nil {
		return outcome
	}
	if outcome != nil {
		reportError(stderr, outcome)
	}
	return hangUp
}

// printCallSummary writes callSummary to stderr.
func printCallSummary(stderr io.Writer, status satnet.Status) {
	// Best effort: the link is closed, so a write error changes nothing.
	_, _ = fmt.Fprintln(stderr, callSummary(status))
}

// callSummary is what the call carried, or a warning when it may not have
// ended.
func callSummary(status satnet.Status) string {
	usage := status.Usage
	traffic := fmt.Sprintf("%d bytes sent, %d received", usage.BytesSent, usage.BytesReceived)
	if status.CallMayStillBeUp {
		return callMayStillBeUpWarning(" (" + traffic + ")")
	}
	return fmt.Sprintf("Call lasted %d s: %s.", int64(usage.ConnectedFor/time.Second), traffic)
}

// warnIfCallMayStillBeUp writes callMayStillBeUpWarning to stderr if err
// says that the call may still be up.
func warnIfCallMayStillBeUp(stderr io.Writer, err error) {
	if errors.Is(err, iridium.ErrCallMayStillBeUp) {
		warnCallMayStillBeUp(stderr, "")
	}
}

// warnCallMayStillBeUp writes callMayStillBeUpWarning to stderr.
func warnCallMayStillBeUp(stderr io.Writer, details string) {
	// Best effort: the error that follows says what failed.
	_, _ = fmt.Fprintln(stderr, callMayStillBeUpWarning(details))
}

// callMayStillBeUpWarning tells how to end a call whose hang-up was not
// confirmed; details follow "not confirmed".
func callMayStillBeUpWarning(details string) string {
	return fmt.Sprintf("warning: the hang-up was not confirmed%s, so the call may still be up and billing. "+
		"End it with `%s hangup`, or power-cycle the handset.", details, programName)
}

// printLink writes what the call negotiated to stderr: the connect message,
// and the addresses PPP agreed on, once it has.
func printLink(stderr io.Writer, status satnet.Status) error {
	p := printer{w: stderr}
	p.printf("Connected (%s).\n", status.ConnectMessage)
	if settings := status.IPv4; settings != nil {
		p.printf("  address:     %s\n", settings.LocalAddress)
		if settings.PeerAddress.IsValid() {
			p.printf("  gateway:     %s\n", settings.PeerAddress)
		}
		p.printf("  DNS servers: %s\n", joinAddresses(settings.DNSServers))
	}
	return p.err
}

// joinAddresses lists addresses, separated by commas.
func joinAddresses(addresses []netip.Addr) string {
	texts := make([]string, len(addresses))
	for i, address := range addresses {
		texts[i] = address.String()
	}
	return strings.Join(texts, ", ")
}
