package main

import (
	"io"
	"slices"
	"testing"
)

// argumentNames names arguments by their forms in errors.
func argumentNames(arguments []*argument) []string {
	names := make([]string, len(arguments))
	for i, arg := range arguments {
		names[i] = arg.String()
	}
	return names
}

func TestACommandTakesItsOwnArgumentsThenTheGlobalOnesThenHelp(t *testing.T) {
	f := newFixture()
	leaf := f.root.subcommand("group").subcommand("leaf")
	tests := []struct {
		command *command
		want    []string
	}{
		{f.root, []string{"--colour <COLOUR>", "--loud...", "--mode[=<MODE>]", "--help", "--version"}},
		{leaf, []string{"<FIRST>", "[REST]", "--colour <COLOUR>", "--loud...", "--mode[=<MODE>]", "--help"}},
		{f.root.subcommand(helpSubcommandName), []string{"[COMMAND]..."}},
	}
	for _, test := range tests {
		if got := argumentNames(test.command.arguments()); !slices.Equal(got, test.want) {
			t.Errorf("%s takes %q, want %q", test.command.binName(), got, test.want)
		}
	}
}

func TestEveryCommandWithSubcommandsHasAHelpSubcommandLast(t *testing.T) {
	root := newCLI(environment{}, io.Discard).rootCommand()
	for _, group := range []*command{root, root.subcommand("sms"), root.subcommand("net")} {
		names := group.subcommandNames()
		if names[len(names)-1] != helpSubcommandName || group.subcommand(helpSubcommandName).help != nil {
			t.Errorf("%s has the subcommands %q, want help last, without -h", group.binName(), names)
		}
	}
}

// The command line names only commands without subcommands, and answers the
// help subcommand while it is read: every other command runs.
func TestEveryCommandButGroupsAndTheHelpSubcommandRuns(t *testing.T) {
	var check func(cmd *command)
	check = func(cmd *command) {
		if runs, want := cmd.run != nil, !cmd.hasSubcommands() && !cmd.showsHelp; runs != want {
			t.Errorf("%s runs: %t, want %t", cmd.binName(), runs, want)
		}
		for _, subcommand := range cmd.subcommands {
			check(subcommand)
		}
	}

	check(newCLI(environment{}, io.Discard).rootCommand())
}

func TestOnlyPossibleValuesThatSayWhatTheyMeanMakeLongHelp(t *testing.T) {
	satlink := newCLI(environment{}, io.Discard).rootCommand()
	f := newFixture()
	tests := []struct {
		command *command
		long    bool
	}{
		{satlink, true},
		{satlink.subcommand("net").subcommand("tcp"), true},
		{satlink.subcommand(helpSubcommandName), false},
		{f.root, false},
	}
	for _, test := range tests {
		if got := test.command.hasLongHelp(); got != test.long {
			t.Errorf("%s has long help: %t, want %t", test.command.binName(), got, test.long)
		}
	}
}

func TestOnlyCommandsWithOptionsOfTheirOwnSayOptions(t *testing.T) {
	f := newFixture()

	if !f.root.needsOptionsTag() || f.root.subcommand(helpSubcommandName).needsOptionsTag() {
		t.Error("[OPTIONS] is for the commands that take options other than -h and -V")
	}
}
