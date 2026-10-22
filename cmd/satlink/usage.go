package main

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/anhydrous99/satlink/internal/rustdebug"
)

// Command lines that do not parse, explained as the Rust version's clap
// explained them: "error: …", tips on what may have been meant, the usage
// line, and where to learn more — in clap's styles where it used them.

// listIndent starts each tip, list of values and missing argument, as clap's
// TAB.
const listIndent = "  "

// commandLineExit ends a run before any command runs: with a help page or the
// version on standard output (status exitSuccess), or with a command line
// that does not parse, explained on standard error (status exitUsage). It is
// not an error, so that nothing else can come out of reading a command line:
// the parser's functions return an exit, or nil.
type commandLineExit struct {
	// text is what to print, on the stream that status calls for, which
	// keeps its styles only if it shows them.
	text   string
	status int
}

// showing returns the exit that prints text, a help page or the version, on
// standard output.
func showing(text string) *commandLineExit {
	return &commandLineExit{text: text, status: exitSuccess}
}

// explainer words the problems of a command line as clap did, styled by
// its palette.
type explainer struct {
	palette palette
}

// usageProblem is a command line that does not parse, as clap explained it.
type usageProblem struct {
	// command is the command whose arguments do not parse.
	command *command
	// problem says what is wrong, after "error: ".
	problem string
	// tips suggest what may have been meant.
	tips []string
	// usage is the usage line to show, "" for none.
	usage string
}

// exit returns the exit that explains u on standard error.
func (e explainer) exit(u usageProblem) *commandLineExit {
	return &commandLineExit{text: e.text(u), status: exitUsage}
}

// text is the problem u, formatted as clap formatted it.
func (e explainer) text(u usageProblem) string {
	var text strings.Builder
	text.WriteString(e.palette.failure("error:") + " " + u.problem)
	if len(u.tips) > 0 {
		text.WriteString("\n")
	}
	for _, tip := range u.tips {
		text.WriteString("\n" + listIndent + e.palette.valid("tip:") + " " + tip)
	}
	if u.usage != "" {
		text.WriteString("\n\n" + u.usage)
	}
	if flag := helpFlagOf(u.command); flag != "" {
		text.WriteString("\n\nFor more information, try '" + e.palette.literal(flag) + "'.")
	}
	text.WriteString("\n")
	return text.String()
}

// helpFlagOf is what an error suggests trying for more information: --help,
// `satlink … help` for a command without it, or "" when there is nothing to
// try.
func helpFlagOf(cmd *command) string {
	switch {
	case cmd.help != nil:
		return cmd.help.optionName()
	case cmd.hasSubcommands():
		return cmd.binName() + " " + helpSubcommandName
	default:
		return ""
	}
}

// unexpectedArgument is a token the command does not take, with tips, and
// the usage of what used gave.
func (e explainer) unexpectedArgument(cmd *command, token string, tips []string, used []*argument) *commandLineExit {
	return e.exit(usageProblem{command: cmd, problem: "unexpected argument '" + e.palette.invalid(token) + "' found",
		tips: tips, usage: usageLine(cmd, used, e.palette)})
}

// unrecognizedSubcommand is a name that is not one of the command's
// subcommands, with tips.
func (e explainer) unrecognizedSubcommand(cmd *command, name string, tips []string) *commandLineExit {
	return e.exit(usageProblem{command: cmd, problem: "unrecognized subcommand '" + e.palette.invalid(name) + "'",
		tips: tips, usage: usageLine(cmd, nil, e.palette)})
}

// missingSubcommand is a command that only groups others, given without one.
func (e explainer) missingSubcommand(cmd *command) *commandLineExit {
	problem := "'" + e.palette.invalid(cmd.binName()) + "' requires a subcommand but one was not provided" +
		e.valueList("subcommands", cmd.subcommandNames())
	return e.exit(usageProblem{command: cmd, problem: problem, usage: usageLine(cmd, nil, e.palette)})
}

// missingArguments is a command given without the required arguments in
// missing, after those used gave.
func (e explainer) missingArguments(cmd *command, missing, used []*argument) *commandLineExit {
	problem := "the following required arguments were not provided:"
	for _, arg := range missing {
		problem += "\n" + listIndent + e.palette.valid(arg.String())
	}
	return e.exit(usageProblem{command: cmd, problem: problem,
		usage: usageLine(cmd, append(used, missing...), e.palette)})
}

// missingValue is an option that takes a value, given without one.
func (e explainer) missingValue(cmd *command, option *argument) *commandLineExit {
	problem := "a value is required for '" + e.palette.invalid(option.String()) + "' but none was supplied" +
		e.valueList("possible values", possibleValueNames(option))
	return e.exit(usageProblem{command: cmd, problem: problem})
}

// impossibleValue is a value that is not one of the argument's possible
// values, with the value most like it as a tip.
func (e explainer) impossibleValue(cmd *command, arg *argument, text string) *commandLineExit {
	if text == "" {
		return e.missingValue(cmd, arg)
	}
	names := possibleValueNames(arg)
	problem := e.invalidValue(arg, text) + e.valueList("possible values", names)
	var tips []string
	if similar, found := mostSimilarName(text, names); found {
		tips = append(tips, e.similarTip("value", []string{similar}))
	}
	return e.exit(usageProblem{command: cmd, problem: problem, tips: tips})
}

// refusedValue is a value that the argument refuses for reason, such as a
// number that is not one.
func (e explainer) refusedValue(cmd *command, arg *argument, text string, reason error) *commandLineExit {
	return e.exit(usageProblem{command: cmd, problem: e.invalidValue(arg, text) + fmt.Sprintf(": %v", reason)})
}

// invalidValue says that text is not a value for arg.
func (e explainer) invalidValue(arg *argument, text string) string {
	return "invalid value '" + e.palette.invalid(text) + "' for '" + e.palette.literal(arg.String()) + "'"
}

// invalidUTF8 is a value that is not UTF-8, given to an argument of cmd
// that takes text or a number: clap's value parsers for those refused it
// before reading it.
func (e explainer) invalidUTF8(cmd *command) *commandLineExit {
	return e.exit(usageProblem{command: cmd, problem: "invalid UTF-8 was detected in one or more arguments",
		usage: usageLine(cmd, nil, e.palette)})
}

// unneededValue is a value given to a flag that takes none, after the
// command line gave given, with the usage line of a value given to a flag
// (groupedUsageLine).
func (e explainer) unneededValue(cmd *command, flag *argument, text string, given []*argument) *commandLineExit {
	problem := "unexpected value '" + e.palette.invalid(text) + "' for '" + e.palette.literal(flag.String()) +
		"' found; no more were expected"
	return e.exit(usageProblem{command: cmd, problem: problem, usage: groupedUsageLine(cmd, flag, given, e.palette)})
}

// givenTwice is an option given again, which takes one value only.
func (e explainer) givenTwice(cmd *command, option *argument) *commandLineExit {
	problem := "the argument '" + e.palette.invalid(option.String()) + "' cannot be used multiple times"
	return e.exit(usageProblem{command: cmd, problem: problem, usage: usageLine(cmd, nil, e.palette)})
}

// similarTip suggests names, of a kind such as "subcommand", for what was
// given.
func (e explainer) similarTip(kind string, names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = "'" + e.palette.valid(name) + "'"
	}
	if len(names) == 1 {
		return fmt.Sprintf("a similar %s exists: %s", kind, quoted[0])
	}
	return fmt.Sprintf("some similar %ss exist: %s", kind, strings.Join(quoted, ", "))
}

// valueList lists values under name, as "\n  [possible values: all, read]";
// it is "" without values.
func (e explainer) valueList(name string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	listed := make([]string, len(values))
	for i, value := range values {
		listed[i] = e.palette.valid(quotedIfBlank(value))
	}
	return "\n" + listIndent + "[" + name + ": " + strings.Join(listed, ", ") + "]"
}

// possibleValueNames returns the names of the argument's possible values.
func possibleValueNames(arg *argument) []string {
	names := make([]string, len(arg.possibleValues))
	for i, value := range arg.possibleValues {
		names[i] = value.name
	}
	return names
}

// quotedIfBlank is text as clap showed a value: quoted when it is empty or
// holds whitespace, so that its ends can be seen.
func quotedIfBlank(text string) string {
	if text == "" || strings.ContainsFunc(text, unicode.IsSpace) {
		return rustdebug.Quote(text)
	}
	return text
}
