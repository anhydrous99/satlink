package main

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Help pages, laid out as the Rust version's clap laid them out: the summary
// that -h shows, an argument a line with its help in a column, and the long
// help of --help and of the help subcommand, an argument's help on the lines
// below it.

// helpLength is which of a command's helps to show.
type helpLength int

const (
	// shortHelp is the summary that -h shows.
	shortHelp helpLength = iota
	// longHelp is what --help and the help subcommand show, when the command
	// has more to say than its summary.
	longHelp
)

// The layout of help pages, as clap's.
const (
	// helpIndent starts every entry.
	helpIndent = "  "
	// longHelpIndent starts every line of an argument's help in long help.
	longHelpIndent = "          "
	// helpGap separates the widest entry from its help in a summary.
	helpGap = 2
	// shortNameWidth is the room "-p, " takes before an option's long name,
	// which a long name without a short one is indented by.
	shortNameWidth = 4
	// shortestSubcommandWidth is the narrowest column of subcommand names:
	// clap's "the shortest an arg can legally be is 2".
	shortestSubcommandWidth = 2
)

// The help of -h, --help: in a summary, it says whether --help shows more.
const (
	helpOptionHelp        = "Print help"
	helpOptionSummaryHelp = "Print help (see more with '--help')"
	helpOptionLongHelp    = "Print help (see a summary with '-h')"
)

// helpPage is one command's help being written.
type helpPage struct {
	command *command
	// long is set for long help.
	long bool
	// lookupEnv finds the environment variables whose values help shows.
	lookupEnv func(string) (string, bool)
	// palette styles the page for its stream.
	palette palette
}

// renderHelp returns cmd's help, styled by p: the long one when asked for,
// if the command has more to show than its summary.
func renderHelp(cmd *command, length helpLength, lookupEnv func(string) (string, bool), p palette) string {
	page := helpPage{command: cmd, long: length == longHelp && cmd.hasLongHelp(), lookupEnv: lookupEnv, palette: p}
	var sections []string
	if cmd.about() != "" {
		sections = append(sections, cmd.about())
	}
	sections = append(sections, p.header("Usage:")+" "+fullUsage(cmd, p))
	if cmd.hasSubcommands() {
		sections = append(sections, p.header("Commands:")+"\n"+page.subcommands())
	}
	if positionals := cmd.positionals(); len(positionals) > 0 {
		sections = append(sections, p.header("Arguments:")+"\n"+page.entries(positionals))
	}
	if options := cmd.options(); len(options) > 0 {
		slices.SortStableFunc(options, compareForHelp)
		sections = append(sections, p.header("Options:")+"\n"+page.entries(options))
	}
	return strings.TrimRightFunc(strings.Join(sections, "\n\n"), unicode.IsSpace) + "\n"
}

// subcommands lists the command's subcommands, a line each.
func (p helpPage) subcommands() string {
	width := shortestSubcommandWidth
	for _, subcommand := range p.command.subcommands {
		width = max(width, displayWidth(subcommand.name()))
	}
	lines := make([]string, len(p.command.subcommands))
	for i, subcommand := range p.command.subcommands {
		lines[i] = helpIndent + padded(p.palette.literal(subcommand.name()), width+helpGap) + subcommand.about()
	}
	return strings.Join(lines, "\n")
}

// entries lists the arguments of one section, all positional or all
// options.
func (p helpPage) entries(args []*argument) string {
	if p.long {
		return p.longEntries(args)
	}
	column := labelColumn(args)
	// Where an argument's help starts, as do the lines of it that a line
	// break in a variable's value begins.
	helpColumn := strings.Repeat(" ", len(helpIndent)+column)
	lines := make([]string, len(args))
	for i, arg := range args {
		lines[i] = helpIndent + padded(arg.helpLabel(p.palette), column) + indented(p.helpText(arg), helpColumn)
	}
	return strings.Join(lines, "\n")
}

// longEntries lists arguments as long help does: each argument's help, and
// the possible values that say what they mean, on the lines below it, and a
// blank line between arguments.
func (p helpPage) longEntries(args []*argument) string {
	entries := make([]string, len(args))
	for i, arg := range args {
		text := p.helpText(arg)
		entry := helpIndent + arg.helpLabel(p.palette) + "\n" + longHelpIndent + indented(text, longHelpIndent)
		if p.listsPossibleValues(arg) {
			if text != "" {
				entry += "\n\n" + longHelpIndent
			}
			entry += p.possibleValuesList(arg)
		}
		entries[i] = entry
	}
	return strings.Join(entries, "\n\n")
}

// labelColumn is how wide the entries of a summary's section are, gap
// included: the widest argument as errors write it, after the room for a
// short name in a section of options.
func labelColumn(args []*argument) int {
	widest := 0
	for _, arg := range args {
		widest = max(widest, displayWidth(arg.String()))
	}
	if !args[0].isPositional() {
		widest += shortNameWidth
	}
	return widest + helpGap
}

// helpText is what the page says of arg: its help, then what else gives it
// values — its environment variable, its default, its possible values.
func (p helpPage) helpText(arg *argument) string {
	help, values := p.argumentHelp(arg), p.valueSources(arg)
	switch {
	case help == "":
		return values
	case values == "":
		return help
	case p.long:
		return help + "\n\n" + values
	default:
		return help + " " + values
	}
}

// argumentHelp is arg's own help; that of -h, --help says whether --help
// shows more.
func (p helpPage) argumentHelp(arg *argument) string {
	switch {
	case arg.action != actionHelp:
		return arg.help
	case p.long:
		return helpOptionLongHelp
	case p.command.hasLongHelp():
		return helpOptionSummaryHelp
	default:
		return helpOptionHelp
	}
}

// valueSources says where else arg's value may come from: "[env: X=value]"
// with the variable's current value, which clap read lossily, "[default:
// value]", and "[possible values: a, b]" unless long help lists them one per
// line.
func (p helpPage) valueSources(arg *argument) string {
	var sources []string
	if arg.env != "" {
		text, _ := p.lookupEnv(arg.env)
		sources = append(sources, "[env: "+arg.env+"="+lossy(text)+"]")
	}
	if arg.takesValue() && arg.hasDefault {
		sources = append(sources, "[default: "+quotedIfBlank(arg.defaultValue)+"]")
	}
	if len(arg.possibleValues) > 0 && !p.listsPossibleValues(arg) {
		names := make([]string, len(arg.possibleValues))
		for i, name := range possibleValueNames(arg) {
			names[i] = quotedIfBlank(name)
		}
		sources = append(sources, "[possible values: "+strings.Join(names, ", ")+"]")
	}
	if p.long {
		return strings.Join(sources, "\n")
	}
	return strings.Join(sources, " ")
}

// listsPossibleValues reports whether the page lists arg's possible values
// one per line, with what each means.
func (p helpPage) listsPossibleValues(arg *argument) bool {
	return p.long && arg.explainsPossibleValues()
}

// possibleValuesList lists arg's possible values with what they mean, a
// line each, their meanings aligned.
func (p helpPage) possibleValuesList(arg *argument) string {
	width := 0
	for _, value := range arg.possibleValues {
		width = max(width, displayWidth(value.name))
	}
	lines := []string{"Possible values:"}
	for _, value := range arg.possibleValues {
		line := "- " + p.palette.literal(value.name)
		if value.help != "" {
			line += ": " + strings.Repeat(" ", width-displayWidth(value.name)) + value.help
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"+longHelpIndent)
}

// compareForHelp orders options as clap's help did: by the order their
// commands declared them, the built-in ones last, and then by name.
func compareForHelp(a, b *argument) int {
	return cmp.Or(cmp.Compare(a.displayOrder, b.displayOrder), strings.Compare(helpSortKey(a), helpSortKey(b)))
}

// helpSortKey orders options declared in the same place: by short name,
// lower case before upper, or else by long name.
func helpSortKey(option *argument) string {
	if option.short == 0 {
		return option.long
	}
	key := string(unicode.ToLower(option.short))
	if unicode.IsLower(option.short) {
		return key + "0"
	}
	return key + "1"
}

// indented indents every line of text after the first by indent, blank
// lines included, as clap indented the lines of an argument's help.
func indented(text, indent string) string {
	return strings.ReplaceAll(text, "\n", "\n"+indent)
}

// padded is text followed by spaces up to width.
func padded(text string, width int) string {
	return text + strings.Repeat(" ", max(width-displayWidth(text), 0))
}

// displayWidth is how many columns text takes in a terminal: one for each
// character of the ASCII that help pages are written in, and none for the
// sequences that style it.
func displayWidth(text string) int {
	width := 0
	for len(text) > 0 {
		if style, found := styleSequenceAt(text); found {
			text = text[len(style):]
			continue
		}
		_, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		width++
	}
	return width
}

// styleSequenceAt returns the ANSI style sequence that text starts with, such
// as "\x1b[1m", and false when it starts with none.
func styleSequenceAt(text string) (string, bool) {
	body, isSequence := strings.CutPrefix(text, "\x1b[")
	if !isSequence {
		return "", false
	}
	end := strings.IndexFunc(body, func(r rune) bool { return (r < '0' || r > '9') && r != ';' })
	if end < 0 || body[end] != 'm' {
		return "", false
	}
	return text[:len("\x1b[")+end+len("m")], true
}
