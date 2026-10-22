package main

import (
	"slices"
	"strings"
	"testing"
)

// The command line as clap reads it: its help pages, its errors for command
// lines that do not parse, its environment variables, and its styles. The
// golden files in testdata/help, testdata/usage and testdata/style hold that
// output, byte for byte.

// helpCommands are the command paths whose help pages are compared.
var helpCommands = [][]string{
	nil, {"list"}, {"info"}, {"status"}, {"sms"}, {"sms", "send"}, {"sms", "list"}, {"sms", "read"},
	{"sms", "delete"}, {"dial"}, {"answer"}, {"hangup"}, {"at"}, {"monitor"}, {"net"}, {"net", "status"},
	{"net", "resolve"}, {"net", "get"}, {"net", "tcp"},
}

func TestTheCommandLineIsReadAsClapReadsIt(t *testing.T) {
	cases := append(helpCases(), usageCases()...)
	cases = append(cases, environmentCases()...)
	cases = append(cases, styleCases()...)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := runInProcess(t, test.world, test.args...)

			checkGolden(t, test.name, test.args, got)
		})
	}
}

// helpCases are the help pages: every command's summary (-h) and long help
// (--help), the help subcommand, and the version.
func helpCases() []goldenCase {
	var cases []goldenCase
	for _, path := range helpCommands {
		name := "help/" + strings.Join(append([]string{programName}, path...), "-")
		cases = append(cases,
			goldenCase{name: name + "-short", args: slices.Concat(path, []string{"-h"})},
			goldenCase{name: name + "-long", args: slices.Concat(path, []string{"--help"})})
	}
	return append(cases, []goldenCase{
		{name: "help/subcommand", args: []string{"help"}},
		{name: "help/subcommand-of-a-subcommand", args: []string{"help", "sms", "list"}},
		{name: "help/subcommand-in-a-group", args: []string{"net", "help", "tcp"}},
		{name: "help/subcommand-itself", args: []string{"help", "help"}},
		{name: "help/subcommand-itself-in-a-group", args: []string{"sms", "help", "help"}},
		{name: "help/after-options", args: []string{instant, "-v", "help", "info"}},
		{name: "help/before-a-subcommand", args: []string{"--help", "info"}},
		{name: "help/in-a-cluster", args: []string{"-vh"}},
		{name: "help/before-an-unknown-option", args: []string{"--help", "--frob"}},
		{name: "help/version", args: []string{"--version"}},
		{name: "help/version-short", args: []string{"-V"}},
		{name: "help/version-before-help", args: []string{"-V", "--help"}},
		{name: "help/version-before-a-subcommand", args: []string{"--version", "info"}},
	}...)
}

// usageCases are command lines that do not parse, one of each of clap's
// errors and of the rules behind them.
func usageCases() []goldenCase {
	return slices.Concat(subcommandUsageCases(), optionUsageCases(), valueUsageCases(), pendingValueUsageCases(),
		flagValueUsageCases())
}

// subcommandUsageCases are command lines that name no command to run.
func subcommandUsageCases() []goldenCase {
	return []goldenCase{
		{name: "usage/no-command", args: nil},
		{name: "usage/unknown-command", args: []string{"frobnicate"}},
		{name: "usage/similar-command", args: []string{"inf"}},
		{name: "usage/similar-commands", args: []string{"s"}},
		{name: "usage/similar-help", args: []string{"hel"}},
		{name: "usage/similar-subcommand", args: []string{"net", "stat"}},
		{name: "usage/group-without-anything", args: []string{instant, "sms"}},
		{name: "usage/group-without-a-subcommand", args: []string{instant, "net", "--allow", "x"}},
		{name: "usage/root-without-a-subcommand", args: []string{"-v"}},
		{name: "usage/group-after-double-dash", args: []string{instant, "sms", "--"}},
		{name: "usage/subcommand-after-double-dash", args: []string{instant, "--", "info"}},
		{name: "usage/help-of-an-unknown-command", args: []string{"help", "frobnicate"}},
		{name: "usage/help-of-an-unknown-subcommand", args: []string{"help", "sms", "frob"}},
		{name: "usage/help-of-too-much", args: []string{"help", "info", "extra"}},
		{name: "usage/help-of-the-help-of-help", args: []string{"help", "help", "help"}},
		{name: "usage/help-of-an-option", args: []string{"help", "-v"}},
		{name: "usage/extra-argument", args: []string{instant, "info", "extra"}},
		{name: "usage/extra-value", args: []string{instant, "dial", "1", "2"}},
		{name: "usage/extra-value-after-double-dash", args: []string{instant, "sms", "list", "all", "--", "read"}},
		{name: "usage/missing-argument", args: []string{instant, "dial"}},
		{name: "usage/missing-arguments", args: []string{instant, "net", "tcp"}},
		{name: "usage/missing-argument-after-one", args: []string{instant, "sms", "send", "+881631234567"}},
		{name: "usage/missing-argument-after-an-option", args: []string{instant, "dial", "--port", "5"}},
	}
}

// optionUsageCases are options in the wrong place, unknown, or given
// wrongly.
func optionUsageCases() []goldenCase {
	return []goldenCase{
		{name: "usage/unknown-option", args: []string{"--frob", "info"}},
		{name: "usage/unknown-option-of-a-subcommand", args: []string{instant, "sms", "--frob", "list"}},
		{name: "usage/unknown-option-before-a-value", args: []string{instant, "dial", "--frob"}},
		{name: "usage/unknown-option-after-options", args: []string{instant, "dial", "-v", "--frob"}},
		{name: "usage/unknown-option-after-a-value", args: []string{instant, "sms", "list", "all", "--frob"}},
		{name: "usage/unknown-short-option", args: []string{"-x", "info"}},
		{name: "usage/unknown-short-option-in-a-cluster", args: []string{"-vx", "info"}},
		{name: "usage/equals-after-a-flag", args: []string{"-v=", "info"}},
		{name: "usage/empty-long-option", args: []string{"--=x", "info"}},
		{name: "usage/similar-option", args: []string{"--verbos", "info"}},
		{name: "usage/similar-option-after-one", args: []string{instant, "net", "--max-kb", "5", "--max", "status"}},
		{name: "usage/similar-built-in-option", args: []string{"--ver", "info"}},
		{name: "usage/option-of-a-later-subcommand", args: []string{instant, "--idle-secs", "5", "net", "status"}},
		{name: "usage/option-of-the-group-after-a-subcommand", args: []string{instant, "net", "status", "--number", "5"}},
		{name: "usage/option-of-a-subcommand-before-it", args: []string{instant, "-t", "3", "at", "AT"}},
		{name: "usage/version-of-a-subcommand", args: []string{"info", "-V"}},
		{name: "usage/simulate-without-equals", args: []string{"--simulate", "instant", "info"}},
		{name: "usage/negative-number", args: []string{instant, "sms", "read", "-1"}},
		{name: "usage/negative-number-as-a-value", args: []string{instant, "at", "--timeout", "-1", "AT"}},
		{name: "usage/option-twice", args: []string{"-p", "a", "--port", "b", "info"}},
		{name: "usage/option-of-a-group-twice", args: []string{instant, "net", "--number", "1", "--number", "2", "status"}},
		{name: "usage/value-for-a-counted-flag", args: []string{"--verbose=2", instant, "info"}},
		{name: "usage/empty-value-for-a-counted-flag", args: []string{"--verbose="}},
		{name: "usage/value-for-help", args: []string{"--help=foo"}},
		{name: "usage/missing-value", args: []string{instant, "info", "--port"}},
		{name: "usage/missing-short-value", args: []string{instant, "-p"}},
		{name: "usage/missing-value-before-an-option", args: []string{"--port", "-v", "frobnicate"}},
		{name: "usage/missing-value-in-a-cluster", args: []string{"-vp"}},
		{name: "usage/missing-value-before-double-dash", args: []string{instant, "dial", "--port", "--", "5"}},
		{name: "usage/value-that-names-a-subcommand", args: []string{"--port", "info"}},
		{name: "usage/double-dash-after-an-option", args: []string{"--port", "--", "info"}},
	}
}

// valueUsageCases are values that an argument does not take.
func valueUsageCases() []goldenCase {
	return []goldenCase{
		{name: "usage/unknown-simulation", args: []string{"--simulate=fast", "info"}},
		{name: "usage/similar-simulation", args: []string{"--simulate=instan", "info"}},
		{name: "usage/empty-simulation", args: []string{"--simulate=", "info"}},
		{name: "usage/simulation-in-capitals", args: []string{"--simulate=IRIDIUM", "info"}},
		{name: "usage/sms-filter", args: []string{instant, "sms", "list", "bogus"}},
		{name: "usage/similar-sms-filter", args: []string{instant, "sms", "list", "unred"}},
		{name: "usage/sms-filter-after-double-dash", args: []string{instant, "sms", "list", "--", "--frob"}},
		{name: "usage/sms-index", args: []string{instant, "sms", "read", "first"}},
		{name: "usage/empty-sms-index", args: []string{instant, "sms", "read", ""}},
		{name: "usage/sms-index-too-large", args: []string{instant, "sms", "read", "4294967296"}},
		{name: "usage/negative-sms-index", args: []string{instant, "sms", "read", "--", "-1"}},
		{name: "usage/port-too-large", args: []string{instant, "net", "tcp", "echo.test", "65536"}},
		{name: "usage/port-not-a-number", args: []string{instant, "net", "tcp", "echo.test", "seven"}},
		{name: "usage/baud-not-a-number", args: []string{"-b", "x", instant, "info"}},
		{name: "usage/empty-baud", args: []string{"-b=", "info"}},
		{name: "usage/negative-baud", args: []string{"--baud=-5", instant, "info"}},
		{name: "usage/baud-too-small", args: []string{"--baud=-99999999999999999999", instant, "info"}},
		{name: "usage/baud-too-large", args: []string{"--baud", "4294967296", instant, "info"}},
		{name: "usage/timeout-not-a-number", args: []string{instant, "at", "--timeout", "x", "AT"}},
		{name: "usage/negative-timeout", args: []string{instant, "at", "-t=-5", "AT"}},
		{name: "usage/timeout-too-large", args: []string{instant, "at", "--timeout", "99999999999999999999", "AT"}},
		{name: "usage/zero-idle-time", args: []string{instant, "net", "--idle-secs", "0", "status"}},
		{name: "usage/zero-data-limit", args: []string{instant, "net", "--max-kb", "0", "status"}},
		{name: "usage/negative-data-limit", args: []string{instant, "net", "--max-kb", "-5", "status"}},
		{name: "usage/not-an-http-url", args: []string{instant, "net", "get", "ftp://example.com/"}},
		{name: "usage/url-without-a-scheme", args: []string{instant, "net", "get", "example.com"}},
		{name: "usage/url-with-a-bad-port", args: []string{instant, "net", "get", "http://example.com:http/"}},
		{name: "usage/url-with-a-space", args: []string{instant, "net", "get", "http://web.test/a b"}},
	}
}

// pendingValueUsageCases are command lines with a value in a token of its
// own, which clap checked only once the command line had moved on: help, or
// a problem after it, comes first, and a token that is itself a problem is
// reported instead of it.
func pendingValueUsageCases() []goldenCase {
	return []goldenCase{
		{name: "usage/help-after-a-pending-value", args: []string{"-b", "x", "info", "-h"}},
		{name: "usage/help-subcommand-after-a-pending-value", args: []string{"-b", "x", "help"}},
		{name: "usage/pending-value-refused-once-its-subcommand-is-read", args: []string{instant, "-b", "x", "info"}},
		{name: "usage/missing-argument-after-a-pending-value", args: []string{instant, "-b", "x", "dial"}},
		{name: "usage/deeper-pending-value-refused-first", args: []string{instant, "-b", "x", "info", "-b", "y"}},
		{name: "usage/group-without-anything-after-an-option-twice", args: []string{"-p", "a", "-p", "b", "sms"}},
		{name: "usage/unknown-option-after-a-pending-value", args: []string{"--baud", "x", "--frob"}},
		{name: "usage/unknown-option-after-an-option-twice", args: []string{"-p", "a", "-p", "b", "--frob"}},
		{name: "usage/unknown-command-after-a-pending-value", args: []string{"-b", "x", "frob"}},
		{name: "usage/extra-value-after-a-pending-value", args: []string{instant, "sms", "read", "x", "y"}},
		{name: "usage/subcommand-after-double-dash-after-a-pending-value", args: []string{"--baud", "x", "--", "info"}},
		{name: "usage/value-for-help-after-a-pending-value", args: []string{instant, "at", "-t", "5", "--help=x", "AT"}},
	}
}

// flagValueUsageCases are values given to flags, whose usage line names the
// arguments a command declares as a group once one of them is given, and an
// unknown option after a flag counted again, which clap moved last.
func flagValueUsageCases() []goldenCase {
	return []goldenCase{
		{name: "usage/value-for-help-after-a-global-option", args: []string{"-v", "--help=x"}},
		{name: "usage/value-for-help-after-positional-values", args: []string{instant, "net", "tcp", "h", "7", "--help=x"}},
		{name: "usage/value-for-help-after-an-attached-value", args: []string{instant, "at", "-t5", "--help=x", "AT"}},
		{name: "usage/unknown-option-after-a-flag-counted-again", args: []string{"-v", "-p", "7", "-v", "--frob"}},
	}
}

// notUTF8Case is a command line holding text that is not UTF-8, and what
// clap wrote on standard error for it.
type notUTF8Case struct {
	name   string
	world  world
	args   []string
	stderr string
}

// notUTF8Cases are text that is not UTF-8 given as each kind of argument,
// as a token clap did not know, and in a variable.
func notUTF8Cases() []notUTF8Case {
	const notUTF8 = "\xff"
	refused := func(usage string) string {
		return "error: invalid UTF-8 was detected in one or more arguments\n\nUsage: " + usage +
			"\n\nFor more information, try '--help'.\n"
	}
	unknown := func(problem string) string {
		return "error: " + problem + "\n\nUsage: satlink [OPTIONS] <COMMAND>\n\nFor more information, try '--help'.\n"
	}
	port := world{variables: map[string]string{portVariable: notUTF8}}
	return []notUTF8Case{
		{"text", world{}, []string{instant, "net", "--username", notUTF8, "status"}, refused("satlink net [OPTIONS] <COMMAND>")},
		{"a URL", world{}, []string{instant, "net", "get", "http://web.test/" + notUTF8},
			refused("satlink net get [OPTIONS] <URL>")},
		{"a number", world{}, []string{instant, "sms", "read", "1" + notUTF8}, refused("satlink sms read [OPTIONS] <INDEX>")},
		{"a possible value", world{}, []string{instant, "sms", "list", notUTF8},
			"error: invalid value '\uFFFD' for '[FILTER]'\n  [possible values: all, unread, read, unsent, sent]\n\n" +
				"For more information, try '--help'.\n"},
		{"a command", world{}, []string{notUTF8}, unknown("unrecognized subcommand '\uFFFD'")},
		{"an option", world{}, []string{"--" + notUTF8}, unknown("unexpected argument '--\uFFFD' found")},
		{"a cluster of options", world{}, []string{"-" + notUTF8 + "ab"}, unknown("unexpected argument '-\uFFFDab' found")},
		{"a variable the deepest command reads", port, []string{"info"}, refused("satlink info [OPTIONS]")},
		{"a variable a command above reads", port, []string{"info", "-p", "/x"}, refused("satlink [OPTIONS] <COMMAND>")},
	}
}

// clap's value parsers refused text that is not UTF-8 before anything ran,
// but for possible values, and clap showed tokens and variables lossily. Golden
// files would hold the bytes themselves, so the output is written out here.
func TestTextThatIsNotUTF8IsRefusedOrShownLossilyAsClapDid(t *testing.T) {
	for _, test := range notUTF8Cases() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := runInProcess(t, test.world, test.args...)

			if got.status != exitUsage || got.stdout != "" || got.stderr != test.stderr {
				t.Errorf("exit status %d, output %q and %q; want %d and %q", got.status, got.stdout, got.stderr,
					exitUsage, test.stderr)
			}
		})
	}
}

func TestHelpShowsAVariableThatIsNotUTF8Lossily(t *testing.T) {
	got := runInProcess(t, world{variables: map[string]string{portVariable: "a\xffb"}}, "info", "-h")

	if want := "[env: SATLINK_PORT=a�b]"; got.status != exitSuccess || !strings.Contains(got.stdout, want) {
		t.Errorf("exit status %d and help %q, want it to show %q", got.status, got.stdout, want)
	}
}

// clap's help and errors went to a stream that shows no styles
// through anstream, which took out the sequences that ESC starts, and the
// C0 control characters and DEL but for tab, line feed, form feed and
// carriage return: clap's own styles, and those that tokens and variables
// brought in. A stream that shows styles got them all. Golden files would
// hold the control characters themselves, so the output is written out
// here.
func TestControlCharactersAreTakenOutOfErrorsAsClapsStreamTookThemOut(t *testing.T) {
	refused := func(problem, usage string) string {
		return "error: " + problem + "\n\nUsage: " + usage + "\n\nFor more information, try '--help'.\n"
	}
	unrecognized := func(name string) string {
		return refused("unrecognized subcommand '"+name+"'", "satlink [OPTIONS] <COMMAND>")
	}
	styled := world{variables: map[string]string{forceColorVariable: "1"}}
	tests := []struct {
		name   string
		world  world
		args   []string
		stderr string
	}{
		{"a style", world{}, []string{"\x1b[31mfoo"}, unrecognized("foo")},
		{"a sequence in a value", world{}, []string{"-b", "\x1b[2Jx", "info"},
			"error: invalid value 'x' for '--baud <BAUD>': invalid digit found in string\n\n" +
				"For more information, try '--help'.\n"},
		{"a backspace", world{}, []string{"info", "a\bb"},
			refused("unexpected argument 'ab' found", "satlink info [OPTIONS]")},
		{"DEL", world{}, []string{instant, "sms", "list", "a\x7f"},
			"error: invalid value 'a' for '[FILTER]'\n  [possible values: all, unread, read, unsent, sent]\n\n" +
				"For more information, try '--help'.\n"},
		// clap's style after the token ends the sequence.
		{"an unfinished sequence", world{}, []string{"\x1b["}, unrecognized("")},
		{"a line break inside a sequence", world{}, []string{"\x1b[\n1m"}, unrecognized("\n1m")},
		// The message quotes the URL and escapes the control character in it.
		{"a control character in a URL", world{}, []string{instant, "net", "get", "http://web.test/a\vb"},
			"error: invalid value 'http://web.test/ab' for '<URL>': \"http://web.test/a\\u{b}b\" " +
				"is not a valid http:// or https:// URL\n\nFor more information, try '--help'.\n"},
		{"NO_COLOR over CLICOLOR_FORCE", world{variables: map[string]string{noColorVariable: "1", forceColorVariable: "1"}},
			[]string{"info", "a\x01b"}, refused("unexpected argument 'ab' found", "satlink info [OPTIONS]")},
		{"a stream that shows styles", styled, []string{"\x1b[31mfoo"},
			"\x1b[1m\x1b[31merror:\x1b[0m unrecognized subcommand '\x1b[33m\x1b[31mfoo\x1b[0m'\n\n" +
				"\x1b[1m\x1b[4mUsage:\x1b[0m \x1b[1msatlink\x1b[0m [OPTIONS] <COMMAND>\n\n" +
				"For more information, try '\x1b[1m--help\x1b[0m'.\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := runInProcess(t, test.world, test.args...)

			if got.status != exitUsage || got.stdout != "" || got.stderr != test.stderr {
				t.Errorf("exit status %d, output %q and %q; want %d and %q", got.status, got.stdout, got.stderr,
					exitUsage, test.stderr)
			}
		})
	}
}

// Like the test above, this covers the variables whose values help shows. A
// line break in one starts the next line where the argument's help starts,
// as clap indented the lines of an argument's help.
func TestHelpShowsVariablesHoldingControlCharactersAsClapDid(t *testing.T) {
	port := func(value string) world { return world{variables: map[string]string{portVariable: value}} }
	tests := []struct {
		name  string
		world world
		args  []string
		shows string
	}{
		{"an OSC that BEL ends", port("/dev/\x1b]0;title\ax"), []string{"info", "-h"},
			"[env: SATLINK_PORT=/dev/x]\n  -b, --baud"},
		// The bracket after the value ends the sequence.
		{"an unfinished sequence", port("/dev/\x1b["), []string{"info", "-h"}, "[env: SATLINK_PORT=/dev/\n  -b, --baud"},
		{"a line break", port("a\nb"), []string{"info", "-h"},
			"[env: SATLINK_PORT=a\n                               b]\n  -b, --baud"},
		{"a line break in long help", world{variables: map[string]string{dataNumberVariable: "1\n2"}},
			[]string{"net", "--help"}, "[env: SATLINK_DATA_NUMBER=1\n          2]\n          [default: 008816000025]\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := runInProcess(t, test.world, test.args...)

			if got.status != exitSuccess || got.stderr != "" || !strings.Contains(got.stdout, test.shows) {
				t.Errorf("exit status %d, help %q and errors %q; want help showing %q", got.status, got.stdout,
					got.stderr, test.shows)
			}
		})
	}
}

// environmentCases are command lines that environment variables stand in
// for: shown in help with their values, counted as given, and set even when
// empty.
func environmentCases() []goldenCase {
	port := func(value string) world { return world{variables: map[string]string{portVariable: value}} }
	number := world{variables: map[string]string{dataNumberVariable: "12345"}}
	return []goldenCase{
		{name: "usage/port-variable-in-help", args: []string{"info", "-h"}, world: port("/dev/ttyACM7")},
		{name: "usage/empty-port-variable-in-help", args: []string{"info", "-h"}, world: port("")},
		{name: "usage/port-variable-with-a-space-in-help", args: []string{"info", "-h"}, world: port("a b")},
		{name: "usage/data-number-variable-in-help", args: []string{"net", "-h"}, world: number},
		{name: "usage/data-number-variable-in-long-help", args: []string{"net", "--help"}, world: number},
		{name: "usage/port-variable-without-a-command", args: nil, world: port("/dev/ttyACM7")},
		{name: "usage/port-variable-without-a-subcommand", args: []string{"sms"}, world: port("/dev/ttyACM7")},
		{name: "usage/data-number-variable-without-a-subcommand", args: []string{instant, "net"}, world: number},
		{name: "usage/port-variable-as-an-argument", args: []string{instant, "dial", "-v"}, world: port("/dev/ttyACM7")},
		{name: "usage/port-variable-unknown-while-reading", args: []string{"info", "--verbos"},
			world: port("/dev/ttyACM7")},
	}
}

// styleCases are help pages and problems in clap's styles, which
// CLICOLOR_FORCE asks for on a stream that is no terminal.
func styleCases() []goldenCase {
	styled := world{variables: map[string]string{forceColorVariable: "1"}}
	cases := []goldenCase{
		{name: "style/help", args: []string{"-h"}},
		{name: "style/long-help", args: []string{"--help"}},
		{name: "style/help-with-arguments", args: []string{"sms", "list", "-h"}},
		{name: "style/help-instead-of-a-subcommand", args: []string{"net"}},
		{name: "style/help-of-help", args: []string{"help", "help"}},
		{name: "style/version", args: []string{"--version"}},
		{name: "style/unknown-option", args: []string{instant, "dial", "--frob"}},
		{name: "style/similar-option", args: []string{"--verbos", "info"}},
		{name: "style/option-of-a-later-subcommand", args: []string{instant, "--idle-secs", "5", "net", "status"}},
		{name: "style/similar-commands", args: []string{"s"}},
		{name: "style/subcommand-after-double-dash", args: []string{instant, "--", "info"}},
		{name: "style/missing-subcommand", args: []string{"-v"}},
		{name: "style/missing-arguments", args: []string{instant, "net", "tcp"}},
		{name: "style/missing-value", args: []string{"--simulate="}},
		{name: "style/impossible-value", args: []string{instant, "sms", "list", "unred"}},
		{name: "style/refused-value", args: []string{"-b=", "info"}},
		{name: "style/unneeded-value", args: []string{"--help=foo"}},
		{name: "style/unneeded-value-after-a-global-option", args: []string{"-v", "--help=foo"}},
		{name: "style/option-twice", args: []string{"-p", "a", "-p", "b", "info"}},
		{name: "style/help-of-the-help-of-help", args: []string{"help", "help", "help"}},
	}
	for i := range cases {
		cases[i].world = styled
	}
	return cases
}

// Help and errors take clap's styles on a terminal: each on the one it goes
// to, help on standard output and errors on standard error.
func TestHelpAndErrorsAreStyledOnTheTerminalTheyGoTo(t *testing.T) {
	tests := []struct {
		name                               string
		args                               []string
		stdoutIsTerminal, stderrIsTerminal bool
		styled                             bool
	}{
		{"help on a terminal", []string{"-h"}, true, false, true},
		{"help beside a terminal", []string{"-h"}, false, true, false},
		{"an error on a terminal", []string{"--frob"}, false, true, true},
		{"an error beside a terminal", []string{"--frob"}, true, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			w := world{stdoutIsTerminal: test.stdoutIsTerminal, stderrIsTerminal: test.stderrIsTerminal,
				variables: map[string]string{termVariable: "xterm"}}

			got := runInProcess(t, w, test.args...)

			// Every style ends with a reset.
			printed := got.stdout + got.stderr
			if styled := strings.Contains(printed, ansiReset); styled != test.styled {
				t.Errorf("printed %q, styled %t; want styled %t", printed, styled, test.styled)
			}
		})
	}
}
