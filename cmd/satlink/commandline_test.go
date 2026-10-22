package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// fixture is a small tree to read command lines against, shaped as
// satlink's: a root with global options, a group with options of its own,
// and a leaf with positional arguments. Its fields keep what the arguments
// are given.
type fixture struct {
	root        *command
	colour      givenText
	loudness    int
	mode        string
	tags        []string
	size        string
	first, rest string
}

// colourVariable is the fixture's environment variable for --colour.
const colourVariable = "FIXTURE_COLOUR"

// newFixture returns a fixture whose arguments have their zero values.
func newFixture() *fixture {
	f := &fixture{}
	leaf := newCommand(&cobra.Command{Use: "leaf", Short: "A leaf"},
		requiredPositional("FIRST", "", stringValue{target: &f.first}),
		&argument{valueName: "REST", hasDefault: true, defaultValue: "none", value: stringValue{target: &f.rest}})
	group := newCommand(&cobra.Command{Use: "group", Short: "A group"},
		&argument{long: "tag", valueName: "TAG", action: actionAppend, value: stringsValue{target: &f.tags}},
		&argument{long: "size", valueName: "SIZE", hasDefault: true, defaultValue: "medium",
			value: stringValue{target: &f.size}},
	).withSubcommands(leaf)
	f.root = newRoot(&cobra.Command{Use: "fixture", Short: "A fixture"},
		&argument{long: "colour", short: 'c', valueName: "COLOUR", env: colourVariable, global: true,
			value: &f.colour},
		&argument{long: "loud", short: 'l', action: actionCount, global: true, value: countValue{target: &f.loudness}},
		&argument{long: "mode", short: 'm', valueName: "MODE", missingValue: "fast", global: true,
			possibleValues: []possibleValue{{name: "fast"}, {name: "slow"}}, value: stringValue{target: &f.mode}},
	).withSubcommands(group)
	return f
}

// read reads the command line args against the fixture, in an environment
// where only variables are set, and returns the command it names.
func (f *fixture) read(args []string, variables map[string]string) (*command, *commandLineExit) {
	lookupEnv := func(key string) (string, bool) {
		value, set := variables[key]
		return value, set
	}
	return parseCommandLine(f.root, args, surroundings{lookupEnv: lookupEnv})
}

// mustRead is read without environment variables, failing the test if the
// command line does not parse.
func (f *fixture) mustRead(t *testing.T, args ...string) *command {
	t.Helper()
	named, exit := f.read(args, nil)
	if exit != nil {
		t.Fatalf("%q: %s", args, exit.text)
	}
	return named
}

// isUsageProblem reports whether exit ends a run whose command line does
// not parse.
func isUsageProblem(exit *commandLineExit) bool {
	return exit != nil && exit.status == exitUsage
}

func TestReadingReturnsTheCommandTheCommandLineNames(t *testing.T) {
	f := newFixture()

	named := f.mustRead(t, "group", "leaf", "x")

	if named.binName() != "fixture group leaf" {
		t.Errorf("named %q, want fixture group leaf", named.binName())
	}
}

func TestArgumentsKeepTheValuesTheyAreGiven(t *testing.T) {
	f := newFixture()

	f.mustRead(t, "-c", "red", "group", "--tag", "a", "--tag=b", "leaf", "x", "y")

	if f.colour != (givenText{text: "red", given: true}) || !slices.Equal(f.tags, []string{"a", "b"}) ||
		f.first != "x" || f.rest != "y" {
		t.Errorf("colour %+v, tags %q, values %q and %q; want red, [a b], x and y", f.colour, f.tags, f.first, f.rest)
	}
}

func TestArgumentsNotGivenTakeTheirDefaults(t *testing.T) {
	f := newFixture()

	f.mustRead(t, "group", "leaf", "x")

	if f.size != "medium" || f.rest != "none" || f.colour.given || f.mode != "" {
		t.Errorf("size %q, rest %q, colour %+v, mode %q; want the defaults, and no colour or mode",
			f.size, f.rest, f.colour, f.mode)
	}
}

func TestAnEnvironmentVariableStandsInForAnOptionNotGiven(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		variables map[string]string
		colour    givenText
	}{
		{"set", []string{"group", "leaf", "x"}, map[string]string{colourVariable: "blue"}, givenText{text: "blue", given: true}},
		// As clap's: a variable set to "" is set.
		{"set to nothing", []string{"group", "leaf", "x"}, map[string]string{colourVariable: ""}, givenText{given: true}},
		{"overridden", []string{"group", "leaf", "-c", "red", "x"}, map[string]string{colourVariable: "blue"},
			givenText{text: "red", given: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture()

			if _, exit := f.read(test.args, test.variables); exit != nil || f.colour != test.colour {
				t.Errorf("colour %+v (%v), want %+v", f.colour, exit, test.colour)
			}
		})
	}
}

func TestAValueMayBeAttachedToItsOptionOrFollowIt(t *testing.T) {
	for _, given := range [][]string{{"-cred"}, {"-c=red"}, {"-c", "red"}, {"--colour=red"}, {"--colour", "red"}, {"-lcred"}} {
		f := newFixture()

		f.mustRead(t, append(given, "group", "leaf", "x")...)

		if f.colour.text != "red" {
			t.Errorf("%q gave the colour %q, want red", given, f.colour.text)
		}
	}
}

func TestAnOptionWaitingForAValueTakesTheNextTokenWhateverItNames(t *testing.T) {
	f := newFixture()

	f.mustRead(t, "-c", "group", "group", "leaf", "x")

	if f.colour.text != "group" {
		t.Errorf("colour %q, want group", f.colour.text)
	}
}

func TestEveryTokenAfterDoubleDashIsAValue(t *testing.T) {
	f := newFixture()

	f.mustRead(t, "group", "leaf", "--", "-c", "--loud")

	if f.first != "-c" || f.rest != "--loud" || f.colour.given || f.loudness != 0 {
		t.Errorf("values %q and %q, colour %+v, loudness %d; want two values and no options",
			f.first, f.rest, f.colour, f.loudness)
	}
}

func TestAnOptionalValueMustFollowEquals(t *testing.T) {
	tests := []struct {
		args []string
		mode string
	}{
		{[]string{"--mode", "group", "leaf", "x"}, "fast"},
		{[]string{"--mode=slow", "group", "leaf", "x"}, "slow"},
	}
	for _, test := range tests {
		f := newFixture()

		f.mustRead(t, test.args...)

		if f.mode != test.mode {
			t.Errorf("%q: mode %q, want %s", test.args, f.mode, test.mode)
		}
	}
}

func TestAShortOptionWhoseValueMustFollowEqualsLeavesTheRestOfItsClusterToOthers(t *testing.T) {
	tests := []struct {
		args     []string
		mode     string
		loudness int
	}{
		{[]string{"-ml", "group", "leaf", "x"}, "fast", 1},
		{[]string{"-lm", "group", "leaf", "x"}, "fast", 1},
		{[]string{"-m=slow", "group", "leaf", "x"}, "slow", 0},
	}
	for _, test := range tests {
		f := newFixture()

		f.mustRead(t, test.args...)

		if f.mode != test.mode || f.loudness != test.loudness {
			t.Errorf("%q: mode %q and loudness %d, want %s and %d", test.args, f.mode, f.loudness, test.mode, test.loudness)
		}
	}
}

func TestAValueLeftPendingIsCheckedOnceTheCommandLineMovesOn(t *testing.T) {
	// Text that is not UTF-8 is the colour's one refusal.
	refused := "invalid UTF-8 was detected in one or more arguments"
	tests := []struct {
		name string
		next []string
		want string
	}{
		{"a flag", []string{"-l", "group", "leaf", "x"}, refused},
		{"an option whose value must follow '='", []string{"-m", "group", "leaf", "x"}, refused},
		{"an option waiting for its value", []string{"-c", "red", "group", "leaf", "x"}, refused},
		{"a subcommand read in full", []string{"group", "leaf", "x"}, refused},
		{"a subcommand that fails first", []string{"group", "leaf"}, "the following required arguments were not provided"},
		{"an unknown option, reported instead", []string{"--frob"}, "unexpected argument '--frob' found"},
	}
	for _, test := range tests {
		_, exit := newFixture().read(append([]string{"-c", "\xff"}, test.next...), nil)

		if !isUsageProblem(exit) || !strings.Contains(exit.text, test.want) {
			t.Errorf("%s: %v, want an error saying %q", test.name, exit, test.want)
		}
	}
}

func TestAValuePendingAtASubcommandGivesWayToTheDeeperCommandsOwn(t *testing.T) {
	f := newFixture()

	// The root's red is checked only once the group's command line is read,
	// but stored before the group's blue.
	f.mustRead(t, "-c", "red", "group", "-c", "blue", "leaf", "x")

	if f.colour.text != "blue" {
		t.Errorf("colour %q, want blue", f.colour.text)
	}
}

func TestAFlagCountsWithinACommandAndTheDeepestCommandsCountWins(t *testing.T) {
	tests := []struct {
		args     []string
		loudness int
	}{
		{[]string{"-ll", "--loud", "group", "leaf", "x"}, 3},
		{[]string{"-lll", "group", "-l", "leaf", "x"}, 1},
		{[]string{"-l", "group", "leaf", "x", "-ll"}, 2},
	}
	for _, test := range tests {
		f := newFixture()

		f.mustRead(t, test.args...)

		if f.loudness != test.loudness {
			t.Errorf("%q: loudness %d, want %d", test.args, f.loudness, test.loudness)
		}
	}
}

func TestHelpAndTheVersionEndTheReadingWithoutAnError(t *testing.T) {
	tests := []struct {
		args  []string
		start string
	}{
		{[]string{"-h", "--frob"}, "A fixture\n"},
		{[]string{"group", "--help"}, "A group\n"},
		{[]string{"help", "group", "leaf"}, "A leaf\n"},
		{[]string{"-V"}, versionText()},
	}
	for _, test := range tests {
		_, exit := newFixture().read(test.args, nil)

		if exit == nil || exit.status != exitSuccess || !strings.HasPrefix(exit.text, test.start) {
			t.Errorf("%q: %v, want a successful exit showing %q", test.args, exit, test.start)
		}
	}
}

func TestCommandLinesThatDoNotParseEndInAUsageExit(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"group"},
		{"group", "leaf"},
		{"group", "leaf", "x", "y", "z"},
		{"--frob"},
		{"-x", "group"},
		{"group", "--size", "a", "--size", "b", "leaf", "x"},
		{"--mode=medium", "group", "leaf", "x"},
		{"group", "leaf", "x", "--colour"},
		{"--loud=2", "group", "leaf", "x"},
		{"--", "group"},
		{"help", "nowhere"},
	} {
		if _, exit := newFixture().read(args, nil); !isUsageProblem(exit) {
			t.Errorf("%q: %v, want a usage error", args, exit)
		}
	}
}

func TestAShortOptionThatIsNotUTF8IsUnknown(t *testing.T) {
	_, exit := newFixture().read([]string{"-l\xff"}, nil)

	if !isUsageProblem(exit) || !strings.Contains(exit.text, "unexpected argument '-\uFFFD' found") {
		t.Errorf("error %v, want the byte refused", exit)
	}
}

func TestARefusedDefaultIsAUsageProblem(t *testing.T) {
	f := newFixture()
	group := f.root.subcommand("group")
	group.own = append(group.own, &argument{long: "count", valueName: "COUNT", hasDefault: true,
		defaultValue: "many", value: unsignedValue{target: new(uint64)}})

	_, exit := f.read([]string{"group", "leaf", "x"}, nil)

	if !isUsageProblem(exit) || !strings.Contains(exit.text, "invalid value 'many' for '--count <COUNT>'") {
		t.Errorf("error %v, want the default refused", exit)
	}
}
