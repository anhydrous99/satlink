package main

import (
	"slices"
	"strings"
)

// The usage line, "Usage: satlink dial [OPTIONS] <NUMBER>", in two forms:
// the full one of help pages, and the one of errors, which names what the
// command line gave and what it still needs.

// subcommandPlaceholder stands for the subcommand in a usage line.
const subcommandPlaceholder = "<COMMAND>"

// usageLine is the usage line an error shows, styled by p: the full one when
// nothing was given (used is empty), and otherwise the smart one.
func usageLine(cmd *command, used []*argument, p palette) string {
	if len(used) == 0 {
		return p.header("Usage:") + " " + fullUsage(cmd, p)
	}
	return p.header("Usage:") + " " + smartUsage(cmd, used, nil, p)
}

// groupedUsageLine is the usage line of a value given to flag, a flag of
// cmd, after the command line gave given, styled by p. The arguments a
// command declares form a group (argumentGroup), and once one of them is
// given, this usage line names the group in place of them.
func groupedUsageLine(cmd *command, flag *argument, given []*argument, p palette) string {
	used := append(slices.Clone(given), flag)
	group := cmd.argumentGroup()
	if !slices.ContainsFunc(given, isAnyOf(group)) {
		return usageLine(cmd, used, p)
	}
	return p.header("Usage:") + " " + smartUsage(cmd, used, group, p)
}

// isAnyOf returns the test for being one of arguments.
func isAnyOf(arguments []*argument) func(*argument) bool {
	return func(arg *argument) bool { return slices.Contains(arguments, arg) }
}

// fullUsage is the usage of cmd's help page, styled by p: its name, [OPTIONS]
// if it takes options, its positional arguments, and <COMMAND> if it needs a
// subcommand.
func fullUsage(cmd *command, p palette) string {
	parts := []string{p.literal(cmd.binName())}
	if cmd.needsOptionsTag() {
		parts = append(parts, "[OPTIONS]")
	}
	for _, positional := range cmd.positionals() {
		parts = append(parts, positional.String())
	}
	return joinUsage(cmd, parts)
}

// smartUsage is the usage of an error, styled by p: the options that used
// gave, then the group, unless it is nil, in place of its members, then
// every positional argument — as a required one when it is required or was
// given — and <COMMAND> if the command needs a subcommand.
func smartUsage(cmd *command, used, group []*argument, p palette) string {
	named := append(requiredArguments(cmd), used...)
	inGroup := isAnyOf(group)
	parts := []string{p.literal(cmd.binName())}
	var options []*argument
	for _, arg := range named {
		if !arg.isPositional() && !slices.Contains(options, arg) && !inGroup(arg) {
			options = append(options, arg)
			parts = append(parts, arg.usageForm(p))
		}
	}
	if group != nil {
		parts = append(parts, groupForm(group))
	}
	for _, positional := range cmd.positionals() {
		switch {
		case inGroup(positional):
			// The group names it.
		case slices.Contains(named, positional):
			parts = append(parts, positional.requiredForm())
		default:
			parts = append(parts, positional.optionalForm())
		}
	}
	return joinUsage(cmd, parts)
}

// groupForm is how a usage line names a group of arguments: <a|b|…>,
// an option as errors write it, a positional argument by its value's name.
func groupForm(group []*argument) string {
	forms := make([]string, len(group))
	for i, arg := range group {
		forms[i] = arg.String()
		if arg.isPositional() {
			forms[i] = arg.valueName
		}
	}
	return "<" + strings.Join(forms, "|") + ">"
}

// joinUsage ends a usage line's parts with <COMMAND> for a command that
// needs a subcommand, and joins them.
func joinUsage(cmd *command, parts []string) string {
	if cmd.hasSubcommands() {
		parts = append(parts, subcommandPlaceholder)
	}
	return strings.Join(parts, " ")
}

// requiredArguments returns the arguments the command cannot do without.
func requiredArguments(cmd *command) []*argument {
	return slices.DeleteFunc(cmd.positionals(), func(positional *argument) bool {
		return !positional.required
	})
}
