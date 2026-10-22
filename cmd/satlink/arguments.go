package main

import (
	"slices"
	"strings"
)

// The arguments a command takes — options such as --port and positional
// arguments such as <NUMBER> — as clap declares them, and how clap writes them
// in help, errors and usage lines.

// argumentAction is what giving an argument does, as clap's ArgAction.
type argumentAction int

const (
	// actionSet takes a value, and may be given once.
	actionSet argumentAction = iota
	// actionAppend takes a value each time it is given.
	actionAppend
	// actionCount takes no value, and counts how often it is given.
	actionCount
	// actionHelp shows the command's help.
	actionHelp
	// actionVersion shows the program's version.
	actionVersion
)

// builtInDisplayOrder places -h and -V after every declared option in help:
// clap's order for the arguments it adds itself.
const builtInDisplayOrder = 999

// argument is an option or a positional argument, as clap described it. The
// zero argument is not usable: one that the command line can give needs a
// value at least.
type argument struct {
	// long and short name an option: --long, -s. A positional argument has
	// neither.
	long  string
	short rune
	// valueName stands for the value in help, errors and usage, such as
	// PORT; it is "" for an option that takes no value.
	valueName string
	// help says what the argument is for; "" says nothing.
	help string
	// action is what giving the argument does.
	action argumentAction
	// required is set for a positional argument that must be given.
	required bool
	// missingValue is the value of an option given without one. With it, a
	// value must follow '=', as in --simulate=instant.
	missingValue string
	// env names an environment variable that stands in for an option not
	// given, even when it is set to "".
	env string
	// defaultValue is the value of an argument not given, when hasDefault.
	defaultValue string
	hasDefault   bool
	// possibleValues are the only values the argument takes, when there are
	// any.
	possibleValues []possibleValue
	// global is set for an option that the command's subcommands take too,
	// as clap's global arguments: the root's, which every command takes.
	global bool
	// displayOrder places an option in help: where its command declared it
	// among its options.
	displayOrder int
	// value keeps what the argument is given. It is nil for those that the
	// reading of the command line acts on itself: -h, -V, and the names that
	// the help subcommand is given.
	value argumentValue
}

// possibleValue is one of the values an argument takes, and what it means.
type possibleValue struct {
	name string
	// help is "" for a value that needs no explaining.
	help string
}

// requiredPositional returns a positional argument that must be given, such
// as <NUMBER>, whose value is kept by value.
func requiredPositional(valueName string, value argumentValue) *argument {
	return &argument{valueName: valueName, required: true, value: value}
}

// isPositional reports whether the argument is taken by position rather
// than by name.
func (a *argument) isPositional() bool {
	return a.long == "" && a.short == 0
}

// takesValue reports whether giving the argument gives it a value.
func (a *argument) takesValue() bool {
	return a.action == actionSet || a.action == actionAppend
}

// isBuiltIn reports whether the argument is one that clap adds itself: -h
// or -V.
func (a *argument) isBuiltIn() bool {
	return a.action == actionHelp || a.action == actionVersion
}

// explainsPossibleValues reports whether the argument has possible values
// that say what they mean, which long help lists one per line.
func (a *argument) explainsPossibleValues() bool {
	return slices.ContainsFunc(a.possibleValues, func(value possibleValue) bool {
		return value.help != ""
	})
}

// String is how clap wrote the argument in errors: --port <PORT>,
// --simulate[=<SIMULATE>], --verbose..., <NUMBER> or [FILTER].
func (a *argument) String() string {
	if a.isPositional() && !a.required {
		return a.optionalForm()
	}
	return a.requiredForm()
}

// requiredForm is how a usage line writes an argument that is required or
// was given: a positional argument as <NAME>.
func (a *argument) requiredForm() string {
	if a.isPositional() {
		return "<" + a.valueName + ">" + a.repetitionMark()
	}
	return a.optionName() + a.valueSuffix()
}

// optionalForm is how a usage line writes a positional argument that need
// not be given: [NAME].
func (a *argument) optionalForm() string {
	return "[" + a.valueName + "]" + a.repetitionMark()
}

// helpLabel is what a help page lists the argument as, its names styled by
// p: -p, --port <PORT>, the long name alone indented as if it had a short
// one, or the positional argument as in errors.
func (a *argument) helpLabel(p palette) string {
	switch {
	case a.isPositional():
		return a.String()
	case a.short == 0:
		return strings.Repeat(" ", shortNameWidth) + p.literal("--"+a.long) + a.valueSuffix()
	case a.long == "":
		return p.literal("-"+string(a.short)) + a.valueSuffix()
	default:
		return p.literal("-"+string(a.short)) + ", " + p.literal("--"+a.long) + a.valueSuffix()
	}
}

// usageForm is how an error's usage line writes an option, its name styled
// by p: --port <PORT>.
func (a *argument) usageForm(p palette) string {
	return p.literal(a.optionName()) + a.valueSuffix()
}

// optionName is --long, or -s for an option without a long name.
func (a *argument) optionName() string {
	if a.long == "" {
		return "-" + string(a.short)
	}
	return "--" + a.long
}

// valueSuffix is what follows an option's name: its value, which may have to
// follow '=', or "..." for a flag that counts.
func (a *argument) valueSuffix() string {
	switch {
	case a.takesValue() && a.missingValue != "":
		return "[=<" + a.valueName + ">]"
	case a.takesValue():
		return " <" + a.valueName + ">"
	case a.action == actionCount:
		return "..."
	default:
		return ""
	}
}

// repetitionMark is "..." for a positional argument that takes every value
// left, and otherwise "".
func (a *argument) repetitionMark() string {
	if a.action == actionAppend {
		return "..."
	}
	return ""
}
