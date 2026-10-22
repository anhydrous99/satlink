package main

import (
	"slices"

	"github.com/spf13/cobra"
)

// The tree of commands. Where an option may stand, which token is a value,
// the help pages and the errors are what users of satlink rely on, and
// cobra and its flag library would decide all of these differently. So the
// CLI reads, documents and checks the command line itself (commandline.go,
// help.go, usage.go), and cobra only runs the command it names.

// helpSubcommandName is the name of the subcommand that every command with
// subcommands gets, which shows their help.
const helpSubcommandName = "help"

// command is one command of the command line: its name and what it does
// (its runner's Use and Short), the arguments it takes, its subcommands,
// and what running it does (its runner's RunE). The zero command is not
// usable; newCommand makes one.
type command struct {
	// runner is the cobra command that runs the command once the command
	// line names it.
	runner *cobra.Command
	// own are the arguments the command declares, in order; arguments adds
	// those every command takes.
	own []*argument
	// help is the command's -h, --help, and nil for the help subcommand,
	// which has none.
	help *argument
	// version is the root's -V, --version, and nil for any other command.
	version     *argument
	subcommands []*command
	parent      *command
	// showsHelp marks the help subcommand, which shows the help of the
	// commands it is given instead of running.
	showsHelp bool
}

// newCommand returns the command that runner names, describes and runs,
// taking arguments, its options in the order of their help.
func newCommand(runner *cobra.Command, arguments ...*argument) *command {
	order := 0
	for _, arg := range arguments {
		if !arg.isPositional() {
			arg.displayOrder = order
			order++
		}
	}
	return &command{runner: runner, own: arguments, help: newHelpOption()}
}

// newRoot returns the root of the tree, which runner runs: the program
// itself, taking arguments, with a version to show.
func newRoot(runner *cobra.Command, arguments ...*argument) *command {
	root := newCommand(runner, arguments...)
	root.version = &argument{long: "version", short: 'V', help: "Print version", action: actionVersion,
		displayOrder: builtInDisplayOrder}
	return root
}

// newHelpOption returns a command's -h, --help, whose help depends on the
// command (help.go).
func newHelpOption() *argument {
	return &argument{long: "help", short: 'h', action: actionHelp, displayOrder: builtInDisplayOrder}
}

// newHelpSubcommand returns the help subcommand: `help [COMMAND]...` shows
// the help of the command that the names lead to.
func newHelpSubcommand() *command {
	return &command{
		runner: &cobra.Command{
			Use:   helpSubcommandName,
			Short: "Print this message or the help of the given subcommand(s)",
		},
		own: []*argument{{valueName: "COMMAND", help: "Print help for the subcommand(s)",
			action: actionAppend}},
		showsHelp: true,
	}
}

// withSubcommands gives the command subcommands — and, after them, the help
// subcommand — and returns it.
func (c *command) withSubcommands(subcommands ...*command) *command {
	for _, subcommand := range append(subcommands, newHelpSubcommand()) {
		subcommand.parent = c
		c.subcommands = append(c.subcommands, subcommand)
		// The help subcommand is answered while reading the command line;
		// cobra never runs it.
		if !subcommand.showsHelp {
			c.runner.AddCommand(subcommand.runner)
		}
	}
	return c
}

// arguments returns everything the command takes, in the order help lists
// them: its own arguments, the global options of the commands above it, then
// -h and -V.
func (c *command) arguments() []*argument {
	all := slices.Clone(c.own)
	if c.parent != nil && !c.showsHelp {
		all = append(all, c.parent.globals()...)
	}
	for _, builtIn := range []*argument{c.help, c.version} {
		if builtIn != nil {
			all = append(all, builtIn)
		}
	}
	return all
}

// globals returns the global options that the command passes on to its
// subcommands: its own, and those it has from above.
func (c *command) globals() []*argument {
	var globals []*argument
	for _, arg := range c.own {
		if arg.global {
			globals = append(globals, arg)
		}
	}
	if c.parent != nil {
		globals = append(globals, c.parent.globals()...)
	}
	return globals
}

// argumentGroup returns the arguments grouped as the command's own: those it
// declares. Only the usage line of a value given to a flag shows the group.
// (The help subcommand, which groups nothing, is never read as a command: it
// answers at once.)
func (c *command) argumentGroup() []*argument {
	return c.own
}

// options returns the options the command takes.
func (c *command) options() []*argument {
	return slices.DeleteFunc(c.arguments(), (*argument).isPositional)
}

// positionals returns the positional arguments the command takes, in order.
func (c *command) positionals() []*argument {
	var positionals []*argument
	for _, arg := range c.own {
		if arg.isPositional() {
			positionals = append(positionals, arg)
		}
	}
	return positionals
}

// option returns the option named --long, or nil when the command takes
// none.
func (c *command) option(long string) *argument {
	for _, arg := range c.options() {
		if arg.long == long {
			return arg
		}
	}
	return nil
}

// shortOption returns the option named -short, or nil when the command takes
// none.
func (c *command) shortOption(short rune) *argument {
	for _, arg := range c.options() {
		if arg.short == short {
			return arg
		}
	}
	return nil
}

// longNames returns the long names of the options the command takes.
func (c *command) longNames() []string {
	var names []string
	for _, arg := range c.options() {
		if arg.long != "" {
			names = append(names, arg.long)
		}
	}
	return names
}

// name is the command's name: the first word of its runner's Use.
func (c *command) name() string {
	return c.runner.Name()
}

// about says what the command does, in a line: its runner's Short.
func (c *command) about() string {
	return c.runner.Short
}

// subcommand returns the subcommand called name, or nil when there is none.
func (c *command) subcommand(name string) *command {
	for _, subcommand := range c.subcommands {
		if subcommand.name() == name {
			return subcommand
		}
	}
	return nil
}

// subcommandNames returns the names of the command's subcommands, help last.
func (c *command) subcommandNames() []string {
	names := make([]string, len(c.subcommands))
	for i, subcommand := range c.subcommands {
		names[i] = subcommand.name()
	}
	return names
}

// hasSubcommands reports whether the command only groups others, one of
// which must be given.
func (c *command) hasSubcommands() bool {
	return len(c.subcommands) > 0
}

// binName is the command as a command line names it: satlink sms list.
func (c *command) binName() string {
	if c.parent == nil {
		return c.name()
	}
	return c.parent.binName() + " " + c.name()
}

// path returns the names of the subcommands from the root down to this
// command, as cobra finds the command by them.
func (c *command) path() []string {
	if c.parent == nil {
		return nil
	}
	return append(c.parent.path(), c.name())
}

// hasLongHelp reports whether --help shows more than -h: whether any argument
// has possible values that say what they mean.
func (c *command) hasLongHelp() bool {
	return slices.ContainsFunc(c.arguments(), (*argument).explainsPossibleValues)
}

// needsOptionsTag reports whether the command's usage line mentions
// [OPTIONS]: whether it takes an option other than -h and -V.
func (c *command) needsOptionsTag() bool {
	return slices.ContainsFunc(c.options(), func(option *argument) bool {
		return !option.isBuiltIn()
	})
}
