package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestAUsageProblemIsLaidOutAsClapsErrors(t *testing.T) {
	f := newFixture()
	helpSubcommand := f.root.subcommand(helpSubcommandName)
	tests := []struct {
		name    string
		problem usageProblem
		want    string
	}{
		{"alone", usageProblem{command: f.root, problem: "it broke"},
			"error: it broke\n\nFor more information, try '--help'.\n"},
		{"with tips and usage", usageProblem{command: f.root, problem: "it broke", tips: []string{"a", "b"},
			usage: "Usage: fixture"},
			"error: it broke\n\n  tip: a\n  tip: b\n\nUsage: fixture\n\nFor more information, try '--help'.\n"},
		{"of the help subcommand, which has no help", usageProblem{command: helpSubcommand, problem: "it broke",
			usage: "Usage: fixture help"},
			"error: it broke\n\nUsage: fixture help\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := (explainer{}).text(test.problem); got != test.want {
				t.Errorf("text =\n%q\nwant\n%q", got, test.want)
			}
		})
	}
}

func TestAGroupWithoutAHelpOptionSuggestsItsHelpSubcommand(t *testing.T) {
	group := newCommand(&cobra.Command{Use: "group"}).withSubcommands(newCommand(&cobra.Command{Use: "leaf"}))
	group.help = nil
	newRoot(&cobra.Command{Use: "fixture"}).withSubcommands(group)

	if got := helpFlagOf(group); got != "fixture group help" {
		t.Errorf("helpFlagOf = %q, want fixture group help", got)
	}
}

func TestSimilarNamesAreSuggestedOneOrSeveral(t *testing.T) {
	if got := (explainer{}).similarTip("value", []string{"all"}); got != "a similar value exists: 'all'" {
		t.Errorf("one: %q", got)
	}
	if got := (explainer{}).similarTip("subcommand", []string{"status", "sms"}); got != "some similar subcommands exist: 'status', 'sms'" {
		t.Errorf("several: %q", got)
	}
}

func TestValuesAreQuotedOnlyWhenTheirEndsWouldNotShow(t *testing.T) {
	tests := map[string]string{
		"":          `""`,
		"a b":       `"a b"`,
		"tab\there": `"tab\there"`,
		"plain":     "plain",
		// Rust's {:?} of a str, as clap's Escape writes it, where Go's %q
		// would write "\v".
		"vertical\vtab": `"vertical\u{b}tab"`,
	}
	for text, want := range tests {
		if got := quotedIfBlank(text); got != want {
			t.Errorf("quotedIfBlank(%q) = %s, want %s", text, got, want)
		}
	}
	if got := (explainer{}).valueList("possible values", nil); got != "" {
		t.Errorf("an empty list is %q", got)
	}
}
