package main

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/anhydrous99/satlink/internal/debugtext"
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/satnet"
	"github.com/spf13/cobra"
)

// `satlink net`: the handset's data call as an IP link that only this
// program can use. This file holds what the call is given — its settings,
// the subcommand, and the link configuration they make; netcall.go runs the
// subcommand during the call.

// connectTimeout is the time allowed for a DNS lookup, TCP connect or TLS
// handshake.
const connectTimeout = 120 * time.Second

// dataNumberVariable is the environment variable that stands in for
// --number.
const dataNumberVariable = "SATLINK_DATA_NUMBER"

// leastLimit is the smallest value --idle-secs, --max-minutes and --max-kb
// take: a limit reached as soon as the call connects would bill a call for
// nothing.
const leastLimit = 1

// bytesPerKilobyte converts --max-kb to bytes.
const bytesPerKilobyte = 1024

// netFlags are the call's settings, the options of `net` itself.
type netFlags struct {
	number   string
	allow    []string
	username string
	password string
	// idleSeconds, maxMinutes and maxKilobytes are the limits, from
	// leastLimit up; maxKilobytes is 0 when --max-kb is not given.
	idleSeconds  uint64
	maxMinutes   uint64
	maxKilobytes uint64
}

// arguments declares the settings. Their defaults are the number that
// SATLINK_DATA_NUMBER names, or else Iridium's internet gateway; nothing
// else allowed; no credentials; the link's own default idle and call time
// limits, and no data limit.
func (f *netFlags) arguments() []*argument {
	limits := satnet.DefaultLimits()
	return []*argument{
		{long: "number", valueName: "NUMBER", help: "Number to dial for the data call", env: dataNumberVariable,
			hasDefault: true, defaultValue: satnet.IridiumInternetNumber, value: stringValue{target: &f.number}},
		{long: "allow", valueName: "RULE", action: actionAppend, value: stringsValue{target: &f.allow},
			help: "Also allow a destination such as `example.com:443` (repeatable); " +
				"the command's own target is always allowed"},
		{long: "username", valueName: "USERNAME", help: "PPP user name (Iridium's gateway accepts any)",
			hasDefault: true, value: stringValue{target: &f.username}},
		{long: "password", valueName: "PASSWORD", help: "PPP password (Iridium's gateway accepts any)",
			hasDefault: true, value: stringValue{target: &f.password}},
		limitArgument("idle-secs", "Hang up after this many seconds without traffic",
			&f.idleSeconds, wholeUnits(limits.IdleTimeout, time.Second)),
		limitArgument("max-minutes", "Longest the call may last, in minutes, hanging up included",
			&f.maxMinutes, wholeUnits(limits.MaxCallDuration, time.Minute)),
		{long: "max-kb", valueName: "MAX_KB", help: "Hang up after this many kilobytes of traffic",
			value: unsignedValue{target: &f.maxKilobytes, minimum: leastLimit}},
	}
}

// limitArgument declares --long, a limit of at least leastLimit that
// defaults to defaultValue.
func limitArgument(long, help string, target *uint64, defaultValue string) *argument {
	return &argument{long: long, valueName: valueNameOf(long), help: help, hasDefault: true,
		defaultValue: defaultValue, value: unsignedValue{target: target, minimum: leastLimit}}
}

// valueNameOf is the name of an option's value when it has none of its own:
// the option's, in capitals, with underscores: IDLE_SECS.
func valueNameOf(long string) string {
	name := []byte(long)
	for i, letter := range name {
		switch {
		case letter == '-':
			name[i] = '_'
		case 'a' <= letter && letter <= 'z':
			name[i] = letter - 'a' + 'A'
		}
	}
	return string(name)
}

// wholeUnits is duration as a count of whole units, in decimal.
func wholeUnits(duration, unit time.Duration) string {
	return strconv.FormatInt(int64(duration/unit), 10)
}

// limits are the link's limits the settings ask for, saturating where a
// value is too large to count, so that the link refuses it as over a year.
func (f *netFlags) limits() satnet.Limits {
	return satnet.Limits{
		IdleTimeout:     saturatingDuration(f.idleSeconds, time.Second),
		MaxCallDuration: saturatingDuration(f.maxMinutes, time.Minute),
		MaxBytes:        saturatingProduct(f.maxKilobytes, bytesPerKilobyte),
	}
}

// netArguments are what `satlink net` was given: the call's settings, and
// what the subcommand does during the call.
type netArguments struct {
	netFlags
	action netAction
}

// linkConfig is the link configuration the arguments ask for, with their
// policy. A configuration the link would refuse — limits too small to be of
// use among them — is refused too.
func (a netArguments) linkConfig() (satnet.Config, error) {
	policy, err := a.policy()
	if err != nil {
		return satnet.Config{}, err
	}
	config := satnet.NewConfig(policy)
	config.Number = a.number
	config.Credentials = satnet.Credentials{Username: a.username, Password: a.password}
	config.Limits = a.limits()
	// Checked again before dialling, but a limit too small to be of use
	// should not be refused after "Dialling" was announced.
	if err := config.Validate(); err != nil {
		return satnet.Config{}, err
	}
	return config, nil
}

// policy is the link's policy that the arguments ask for: the --allow rules
// and the subcommand's own target, which must be a host the link can reach,
// as the link would refuse anything else after the call had cost airtime.
func (a netArguments) policy() (satnet.Policy, error) {
	target, hasTarget := a.action.target()
	if !hasTarget {
		return satnet.ParsePolicy(a.allow...)
	}
	policy, err := satnet.ParsePolicy(append(slices.Clone(a.allow), target.rule())...)
	if err != nil {
		return satnet.Policy{}, err
	}
	if err := target.check(policy); err != nil {
		return satnet.Policy{}, err
	}
	return policy, nil
}

// netAction is what a net subcommand does: its work during the call, the
// destination it needs, and for resolve an answer that needs no call.
type netAction interface {
	// target is the destination the command needs, and false when it needs
	// none.
	target() (netTarget, bool)
	// literalAddress is the command's whole answer when it needs no call,
	// and false when it needs one.
	literalAddress() (netip.Addr, bool)
	// execute does the command's work over link.
	execute(link netLink, s streams) error
}

// netLink is the link as the net subcommands use it: they look names up,
// open connections, secured with TLS or not, and ask for the hang-up.
// *satnet.Link is one; tests stand in for it to see what the subcommands
// ask of it.
type netLink interface {
	dialer
	Resolve(host string, timeout time.Duration) ([]netip.Addr, error)
	RequestClose()
}

// netTarget is the destination of a net subcommand: a port of a host, or,
// for a lookup, the host's name, on any port.
type netTarget struct {
	host string
	port uint16
	// lookup is set for a host that is only looked up.
	lookup bool
}

// rule is the allow rule the link needs for the target.
func (t netTarget) rule() string {
	if t.lookup {
		return t.host + ":*"
	}
	return fmt.Sprintf("%s:%d", t.host, t.port)
}

// check refuses a target the link would refuse anyway, before it costs a
// call: a rule such as *.example.com or 10.0.0.0/8 is not a host.
func (t netTarget) check(policy satnet.Policy) error {
	if !t.isReachable(policy) {
		return errors.New(debugtext.Quote(t.host) + " is not a host name or IPv4 address")
	}
	return nil
}

// isReachable reports whether policy lets the link reach the target. An IPv4
// address needs no lookup.
func (t netTarget) isReachable(policy satnet.Policy) bool {
	if t.lookup {
		_, isAddress := ipv4Literal(t.host)
		return isAddress || policy.AllowsLookup(t.host)
	}
	return policy.AllowsHost(t.host, t.port)
}

// ipv4Literal returns the IPv4 address that host writes, and false when it
// writes none.
func ipv4Literal(host string) (netip.Addr, bool) {
	address, err := netip.ParseAddr(host)
	return address, err == nil && address.Is4()
}

// needsACall gives the net subcommands whose answer always takes a call
// their literalAddress.
type needsACall struct{}

func (needsACall) literalAddress() (netip.Addr, bool) { return netip.Addr{}, false }

// statusAction does nothing during the call: the link brought up, what was
// negotiated shown, and the call hung up are what every net subcommand does
// around its own work.
type statusAction struct {
	needsACall
}

func (statusAction) target() (netTarget, bool)      { return netTarget{}, false }
func (statusAction) execute(netLink, streams) error { return nil }

// resolveAction looks a host name up over the link. An IPv4 address needs
// no lookup, and so no call.
type resolveAction struct {
	host string
}

func (r resolveAction) target() (netTarget, bool) {
	return netTarget{host: r.host, lookup: true}, true
}

func (r resolveAction) literalAddress() (netip.Addr, bool) {
	return ipv4Literal(r.host)
}

func (r resolveAction) execute(link netLink, s streams) error {
	addresses, err := link.Resolve(r.host, connectTimeout)
	if err != nil {
		return err
	}
	p := printer{w: s.stdout}
	for _, address := range addresses {
		p.printf("%s\n", address)
	}
	return p.err
}

// netCommand is `satlink net`, whose subcommands open the handset and run
// during a call.
func (c *cli) netCommand() *command {
	return newNetCommand(func(arguments netArguments) error {
		return c.withHandset(func(handset *iridium.Handset) error {
			return c.runNet(handset, arguments)
		})
	})
}

// newNetCommand returns `net` and its subcommands, each of which hands what
// it was given to execute.
func newNetCommand(execute func(netArguments) error) *command {
	flags := &netFlags{}
	start := func(action netAction) error {
		return execute(netArguments{netFlags: *flags, action: action})
	}
	return newCommand(&cobra.Command{
		Use:   "net",
		Short: "Use a data call as an IP link that only this program can use",
	}, flags.arguments()...).withSubcommands(
		netSubcommand(&cobra.Command{Use: "status", Short: "Bring the link up, show what was negotiated, and hang up"},
			start, func() netAction { return statusAction{} }),
		resolveCommand(start), getCommand(start), tcpCommand(start),
	)
}

// netSubcommand returns a net subcommand, which cobraCommand names and
// describes: running it starts the action that newAction makes from what
// the command line gave arguments.
func netSubcommand(cobraCommand *cobra.Command, start func(netAction) error, newAction func() netAction,
	arguments ...*argument) *command {
	cobraCommand.RunE = func(*cobra.Command, []string) error {
		return start(newAction())
	}
	return newCommand(cobraCommand, arguments...)
}

// resolveCommand is `satlink net resolve`.
func resolveCommand(start func(netAction) error) *command {
	var action resolveAction
	return netSubcommand(&cobra.Command{Use: "resolve", Short: "Look up a host name over the link"},
		start, func() netAction { return action }, requiredPositional("HOST", "Host name to look up", stringValue{target: &action.host}))
}
