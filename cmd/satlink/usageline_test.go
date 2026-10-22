package main

import "testing"

func TestTheUsageLineOfHelpNamesWhatTheCommandTakes(t *testing.T) {
	f := newFixture()
	group := f.root.subcommand("group")
	tests := []struct {
		command *command
		want    string
	}{
		{f.root, "fixture [OPTIONS] <COMMAND>"},
		{group, "fixture group [OPTIONS] <COMMAND>"},
		{group.subcommand("leaf"), "fixture group leaf [OPTIONS] <FIRST> [REST]"},
		{f.root.subcommand(helpSubcommandName), "fixture help [COMMAND]..."},
	}
	for _, test := range tests {
		if got := fullUsage(test.command, palette{}); got != test.want {
			t.Errorf("usage %q, want %q", got, test.want)
		}
	}
}

func TestTheUsageLineOfAnErrorNamesWhatWasGivenAndWhatIsNeeded(t *testing.T) {
	f := newFixture()
	leaf := f.root.subcommand("group").subcommand("leaf")
	loud, rest := leaf.option("loud"), leaf.positionals()[1]
	tests := []struct {
		name string
		used []*argument
		want string
	}{
		{"nothing", nil, "Usage: fixture group leaf [OPTIONS] <FIRST> [REST]"},
		{"an option", []*argument{loud}, "Usage: fixture group leaf --loud... <FIRST> [REST]"},
		{"an option twice", []*argument{loud, loud}, "Usage: fixture group leaf --loud... <FIRST> [REST]"},
		{"an optional value", []*argument{rest}, "Usage: fixture group leaf <FIRST> <REST>"},
	}
	for _, test := range tests {
		if got := usageLine(leaf, test.used, palette{}); got != test.want {
			t.Errorf("%s: %q, want %q", test.name, got, test.want)
		}
	}
}
