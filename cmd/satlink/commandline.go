package main

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/anhydrous99/satlink/internal/utf8lossy"
)

// Reading the command line. The parser is the CLI's own, rather than
// cobra's or pflag's, so that help, error messages and option precedence
// stay under this package's control. The rules:
//
//   - Each command takes the tokens up to the name of its subcommand, which
//     takes the rest.
//   - Global options (-p, -b, --simulate, -v) may be given at any level. The
//     deepest level that gives one wins outright; counts add up only within
//     a level.
//   - A token that starts with '-' is an option, so a value that starts with
//     '-' must follow '=' or come after "--". An option waiting for a value
//     takes the next token, even one that names a subcommand. --simulate
//     takes a value only after '='.
//   - An option may be given once per level, except --allow and -v.
//   - -h shows the summary and --help the long help; -V exists on the root
//     only. Each acts once the value pending at its own level is checked, so
//     "-b x -h" refuses "x". Every group has a help subcommand.
//   - A group given nothing shows its summary on standard error, with status
//     2. A group given options but no subcommand needs a subcommand.
//   - An environment variable set to "" is set.
//
// A value attached to its option (--x=v, -xv) is checked at once. A value in
// a token of its own, an option's or a positional argument's, is checked only
// once the command line moves on: at the command's next argument, at the end
// of its tokens, or once its subcommand has been read in full. So a later
// problem may be reported first, help may be shown, and a later token that
// is itself a problem is reported instead.

// surroundings are what reading a command line needs of the world: its
// environment variables, and the palette of what it shows.
type surroundings struct {
	lookupEnv func(string) (string, bool)
	// palette styles help and the problems, whichever stream they go to.
	palette palette
}

// parseCommandLine reads args, the command line without the program's name,
// against root's tree, and returns the command it names. Every argument of
// the commands it names gets its value: from the command line, from its
// environment variable as world's lookupEnv finds it, or its default. When
// the command line asks for help or the version, or does not parse, it
// returns instead the exit that ends the run.
func parseCommandLine(root *command, args []string, world surroundings) (*command, *commandLineExit) {
	p := &parser{surroundings: world, explain: explainer{palette: world.palette}}
	named, exit := p.read(p.enter(root), args)
	if exit != nil {
		return nil, exit
	}
	if exit := p.giveAbsentValues(); exit != nil {
		return nil, exit
	}
	return named, nil
}

// parser is one reading of a command line.
type parser struct {
	surroundings
	// explain words the problems.
	explain explainer
	// levels are the commands that the command line names, root first.
	levels []*level
}

// level is what one command of the command line was given, and how far its
// reading has got.
type level struct {
	command *command
	// given are the arguments the command line gave, in the order they
	// are recorded (see note); counts says how often each came.
	given  []*argument
	counts map[*argument]int
	// awaiting is an option waiting for its value in the next token, and
	// pending a value given in a token of its own, which is checked only
	// once the command line moves on: at most one of them is set.
	awaiting *argument
	pending  *givenValue
	// positionalsTaken counts the positional arguments that have their value.
	positionalsTaken int
	// trailing is set after "--", when every token is a positional value.
	trailing bool
}

// givenValue is the text the command line gave an argument.
type givenValue struct {
	arg  *argument
	text string
}

// enter starts the level of cmd, which the command line names.
func (p *parser) enter(cmd *command) *level {
	lvl := &level{command: cmd, counts: map[*argument]int{}}
	p.levels = append(p.levels, lvl)
	return lvl
}

// read reads args for lvl's command and for the subcommand they name, and
// returns the command they name.
func (p *parser) read(lvl *level, args []string) (*command, *commandLineExit) {
	for i, token := range args {
		rest := args[i+1:]
		subcommand := lvl.subcommandNamed(token)
		switch {
		case subcommand != nil && subcommand.showsHelp:
			// The value left pending is never checked: help comes first.
			return nil, p.showHelpOf(lvl.command, rest)
		case subcommand != nil:
			return p.readSubcommand(lvl, subcommand, rest)
		}
		if exit := p.readToken(lvl, token, rest); exit != nil {
			return nil, exit
		}
	}
	if exit := p.finish(lvl); exit != nil {
		return nil, exit
	}
	return lvl.command, nil
}

// readSubcommand reads rest for subcommand, which lvl's command line names,
// and returns the command they name. The value that lvl's command line left
// pending is checked only once rest has been read in full, so a problem
// there comes first, and so does help. The value is stored before theirs all
// the same, as the deepest command to give a global option wins: with
// "--simulate=instant -b x info -b y", "y" is the one reported.
// A command with subcommands takes no positional arguments, and no option
// of it can be waiting for a value, which would have taken the name.
func (p *parser) readSubcommand(lvl *level, subcommand *command, rest []string) (*command, *commandLineExit) {
	refused := p.resolve(lvl)
	named, exit := p.read(p.enter(subcommand), rest)
	switch {
	case exit != nil:
		return nil, exit
	case refused != nil:
		return nil, refused
	}
	if exit := p.checkEnvironment(lvl); exit != nil {
		return nil, exit
	}
	return named, nil
}

// subcommandNamed returns the subcommand that token names, if a subcommand
// may come now.
func (l *level) subcommandNamed(token string) *command {
	if l.trailing || l.awaiting != nil {
		return nil
	}
	return l.command.subcommand(token)
}

// readToken reads one token that names no subcommand; rest are the tokens
// after it.
func (p *parser) readToken(lvl *level, token string, rest []string) *commandLineExit {
	switch {
	case lvl.trailing:
		return p.readPositional(lvl, token)
	case token == "--":
		lvl.trailing = true
		return nil
	case strings.HasPrefix(token, "--"):
		return p.readLong(lvl, token[len("--"):], rest)
	case strings.HasPrefix(token, "-") && token != "-":
		return p.readShorts(lvl, token[len("-"):])
	case lvl.awaiting != nil:
		lvl.pending = &givenValue{arg: lvl.awaiting, text: token}
		lvl.awaiting = nil
		return nil
	default:
		return p.readPositional(lvl, token)
	}
}

// readLong reads an option by its long name: --name or --name=value.
func (p *parser) readLong(lvl *level, body string, rest []string) *commandLineExit {
	name, attached, hasEquals := strings.Cut(body, "=")
	option := lvl.command.option(name)
	switch {
	case option == nil:
		lvl.settleBeforeAProblem()
		return p.explain.unknownLong(lvl, lossy(name), rest)
	case !option.takesValue() && hasEquals:
		// The usage line names what was given before the value left
		// pending, which is not recorded yet.
		return p.explain.unneededValue(lvl.command, option, lossy(attached), lvl.given)
	case !option.takesValue():
		return p.act(lvl, option, longHelp)
	case hasEquals:
		return p.take(lvl, option, attached)
	case option.missingValue != "":
		return p.take(lvl, option, option.missingValue)
	default:
		return p.await(lvl, option)
	}
}

// readShorts reads a cluster of options by their short names, such as -vv
// or -p/dev/ttyACM0: an option that takes a value takes the rest of the
// cluster, without a leading '=', or else the next token.
func (p *parser) readShorts(lvl *level, cluster string) *commandLineExit {
	for i, letter := range cluster {
		option := lvl.command.shortOption(letter)
		// Not RuneLen: a byte that is not UTF-8 comes as U+FFFD, a byte long.
		_, size := utf8.DecodeRuneInString(cluster[i:])
		rest := cluster[i+size:]
		switch {
		case option == nil:
			return p.explain.unknownShort(lvl, unknownShortToken(cluster[i:]))
		case !option.takesValue():
			if exit := p.act(lvl, option, shortHelp); exit != nil {
				return exit
			}
		case option.missingValue != "" && !strings.HasPrefix(rest, "="):
			// Its value must follow '=': what follows are more options.
			if exit := p.take(lvl, option, option.missingValue); exit != nil {
				return exit
			}
		case rest == "":
			return p.await(lvl, option)
		default:
			return p.take(lvl, option, strings.TrimPrefix(rest, "="))
		}
	}
	return nil
}

// lossy is text from the command line or the environment as errors and help
// show it: with U+FFFD for each part that is not UTF-8.
func lossy(text string) string {
	return utf8lossy.String([]byte(text))
}

// unknownShortToken names a short option that is not known at the start of
// rest, the remains of a cluster: -x. At a byte that is not UTF-8, letters
// can no longer be told apart, so it is the whole rest, read lossily.
func unknownShortToken(rest string) string {
	letter, size := utf8.DecodeRuneInString(rest)
	if letter == utf8.RuneError && size == 1 {
		return "-" + lossy(rest)
	}
	return "-" + string(letter)
}

// readPositional makes token the pending value of the next positional
// argument, or says why the command takes no more.
func (p *parser) readPositional(lvl *level, token string) *commandLineExit {
	positionals := lvl.command.positionals()
	if lvl.positionalsTaken == len(positionals) {
		lvl.settleBeforeAProblem()
		return p.explain.notAnArgument(lvl, lossy(token))
	}
	if exit := p.resolve(lvl); exit != nil {
		return exit
	}
	positional := positionals[lvl.positionalsTaken]
	if positional.action != actionAppend {
		lvl.positionalsTaken++
	}
	lvl.pending = &givenValue{arg: positional, text: token}
	return nil
}

// take gives option the value text, given in the same token. Such a value is
// checked at once, after the value left pending.
func (p *parser) take(lvl *level, option *argument, text string) *commandLineExit {
	if exit := p.resolve(lvl); exit != nil {
		return exit
	}
	return p.give(lvl, option, text)
}

// give gives arg the value text: once only, unless it takes many; one of its
// possible values, if it has them; and a value it accepts.
func (p *parser) give(lvl *level, arg *argument, text string) *commandLineExit {
	if arg.action == actionSet && lvl.counts[arg] > 0 {
		return p.explain.givenTwice(lvl.command, arg)
	}
	if exit := p.store(lvl.command, arg, text); exit != nil {
		return exit
	}
	lvl.note(arg)
	return nil
}

// store gives arg, an argument of cmd, the value text, if check lets it
// through and the argument accepts it.
func (p *parser) store(cmd *command, arg *argument, text string) *commandLineExit {
	if exit := p.check(cmd, arg, text); exit != nil {
		return exit
	}
	if err := arg.value.Set(text); err != nil {
		return p.explain.refusedValue(cmd, arg, text, err)
	}
	return nil
}

// check refuses text for arg, an argument of cmd, before its value reads
// it: the text must be one of arg's possible values, if it has them, and
// UTF-8 otherwise.
func (p *parser) check(cmd *command, arg *argument, text string) *commandLineExit {
	switch {
	case len(arg.possibleValues) > 0 && !slices.Contains(possibleValueNames(arg), text):
		return p.explain.impossibleValue(cmd, arg, lossy(text))
	case !utf8.ValidString(text):
		return p.explain.invalidUTF8(cmd)
	default:
		return nil
	}
}

// act carries out a flag, which takes no value, once the value left pending
// is checked: it shows help, of the length that the way it was given asks
// for, or the version, or counts once more.
func (p *parser) act(lvl *level, flag *argument, length helpLength) *commandLineExit {
	if exit := p.resolve(lvl); exit != nil {
		return exit
	}
	switch flag.action {
	case actionHelp:
		return showing(renderHelp(lvl.command, length, p.lookupEnv, p.palette))
	case actionVersion:
		return showing(versionText())
	default:
		lvl.note(flag)
		return p.store(lvl.command, flag, strconv.Itoa(lvl.counts[flag]))
	}
}

// await makes option wait for its value in the next token, once the value
// left pending is checked.
func (p *parser) await(lvl *level, option *argument) *commandLineExit {
	if exit := p.resolve(lvl); exit != nil {
		return exit
	}
	lvl.awaiting = option
	return nil
}

// resolve checks and stores the value that lvl's command line left pending,
// now that the command line has moved on. An option still waiting for its
// value has none.
func (p *parser) resolve(lvl *level) *commandLineExit {
	awaiting, pending := lvl.awaiting, lvl.pending
	lvl.awaiting, lvl.pending = nil, nil
	switch {
	case awaiting != nil:
		return p.explain.missingValue(lvl.command, awaiting)
	case pending != nil:
		return p.give(lvl, pending.arg, pending.text)
	default:
		return nil
	}
}

// settleBeforeAProblem drops the value left pending when the next token is
// itself a problem, which is reported instead of the value's: --baud x
// --frob reports --frob. The value still counts as given in the usage line,
// except for an option given twice, which is taken out of what was given,
// earlier occurrence and all.
func (l *level) settleBeforeAProblem() {
	pending := l.pending
	l.awaiting, l.pending = nil, nil
	switch {
	case pending == nil:
	case pending.arg.action == actionSet && l.counts[pending.arg] > 0:
		l.forget(pending.arg)
	default:
		l.note(pending.arg)
	}
}

// note records that arg was given once more. A flag that counts goes last
// among those given, as it is recorded afresh with every occurrence.
func (l *level) note(arg *argument) {
	switch {
	case arg.action == actionCount:
		l.given = append(slices.DeleteFunc(l.given, isArgument(arg)), arg)
	case l.counts[arg] == 0:
		l.given = append(l.given, arg)
	}
	l.counts[arg]++
}

// forget records that arg was not given at all.
func (l *level) forget(arg *argument) {
	l.given = slices.DeleteFunc(l.given, isArgument(arg))
	delete(l.counts, arg)
}

// isArgument returns the test for arg itself.
func isArgument(arg *argument) func(*argument) bool {
	return func(other *argument) bool { return other == arg }
}

// finish checks what the command was given, once the command line has no
// more for it, once the value left pending is checked, and the environment
// variables of the options not given: no
// option may be waiting for a value; a command that only groups others needs
// one of them, and shows its help if it was given nothing at all; every
// required positional argument needs its value. Options whose environment
// variables are set count as given here.
func (p *parser) finish(lvl *level) *commandLineExit {
	if exit := p.resolve(lvl); exit != nil {
		return exit
	}
	if exit := p.checkEnvironment(lvl); exit != nil {
		return exit
	}
	given := append(slices.Clone(lvl.given), p.fromEnvironment(lvl)...)
	switch {
	case lvl.command.hasSubcommands() && len(given) == 0:
		return &commandLineExit{text: renderHelp(lvl.command, shortHelp, p.lookupEnv, p.palette), status: exitUsage}
	case lvl.command.hasSubcommands():
		return p.explain.missingSubcommand(lvl.command)
	}
	var missing []*argument
	for _, positional := range requiredArguments(lvl.command) {
		if lvl.counts[positional] == 0 {
			missing = append(missing, positional)
		}
	}
	if len(missing) > 0 {
		return p.explain.missingArguments(lvl.command, missing, given)
	}
	return nil
}

// checkEnvironment checks the environment variables that stand in for the
// options lvl's command line did not give. Every command that takes the
// option checks them, the deepest first, before it checks what the command
// was given; giveAbsentValues stores the values once every command has been
// read. Those variables stand in for text, so being UTF-8 is all their
// values are checked for.
func (p *parser) checkEnvironment(lvl *level) *commandLineExit {
	for _, arg := range p.fromEnvironment(lvl) {
		text, _ := p.environmentValue(arg)
		if exit := p.check(lvl.command, arg, text); exit != nil {
			return exit
		}
	}
	return nil
}

// fromEnvironment returns the options of lvl's command that the command line
// did not give, but whose environment variables are set.
func (p *parser) fromEnvironment(lvl *level) []*argument {
	var set []*argument
	for _, arg := range lvl.command.arguments() {
		if _, found := p.environmentValue(arg); found && lvl.counts[arg] == 0 {
			set = append(set, arg)
		}
	}
	return set
}

// environmentValue returns the value of arg's environment variable, and false
// when it has none or the variable is not set.
func (p *parser) environmentValue(arg *argument) (string, bool) {
	if arg.env == "" {
		return "", false
	}
	return p.lookupEnv(arg.env)
}

// showHelpOf shows the long help of the command that names lead to from cmd:
// the help subcommand's arguments.
func (p *parser) showHelpOf(cmd *command, names []string) *commandLineExit {
	for _, name := range names {
		subcommand := cmd.subcommand(name)
		if subcommand == nil {
			return p.explain.unrecognizedSubcommand(cmd, lossy(name), nil)
		}
		cmd = subcommand
	}
	return showing(renderHelp(cmd, longHelp, p.lookupEnv, p.palette))
}

// giveAbsentValues gives every argument of the commands named that the
// command line gave nowhere the value of its environment variable, if that
// is set, or else its default. (A global option given to several commands
// keeps the deepest one's value, which reading stored last.)
func (p *parser) giveAbsentValues() *commandLineExit {
	settled := map[*argument]bool{}
	for _, lvl := range p.levels {
		for _, arg := range lvl.given {
			settled[arg] = true
		}
	}
	for _, lvl := range p.levels {
		for _, arg := range lvl.command.arguments() {
			if settled[arg] || arg.isBuiltIn() {
				continue
			}
			settled[arg] = true
			if exit := p.giveAbsentValue(lvl.command, arg); exit != nil {
				return exit
			}
		}
	}
	return nil
}

// giveAbsentValue gives arg, an argument of cmd that the command line did
// not give, its environment variable's value or its default, if it has
// either.
func (p *parser) giveAbsentValue(cmd *command, arg *argument) *commandLineExit {
	text, found := p.environmentValue(arg)
	if !found && arg.hasDefault {
		text, found = arg.defaultValue, true
	}
	if !found {
		return nil
	}
	return p.store(cmd, arg, text)
}
