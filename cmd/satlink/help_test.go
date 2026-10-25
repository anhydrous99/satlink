package main

import (
	"slices"
	"strings"
	"testing"
)

func TestOptionsAreListedInTheOrderDeclaredThenByName(t *testing.T) {
	options := []*argument{
		{long: "zebra", displayOrder: 1},
		{long: "help", short: 'h', displayOrder: builtInDisplayOrder},
		{long: "Upper", short: 'U', displayOrder: 0},
		{long: "apple", displayOrder: 0},
		{long: "upper", short: 'u', displayOrder: 0},
	}

	slices.SortStableFunc(options, compareForHelp)

	var order []string
	for _, option := range options {
		order = append(order, option.long)
	}
	if want := []string{"apple", "upper", "Upper", "zebra", "help"}; !slices.Equal(order, want) {
		t.Errorf("order %q, want %q", order, want)
	}
}

func TestOptionsWithAndWithoutShortNamesShareTheirColumn(t *testing.T) {
	cmd := newCommand("fixture", "A fixture", nil,
		&argument{short: 'x', valueName: "X", help: "Short only", value: stringValue{target: new(string)}},
		&argument{long: "long", help: "Long only", action: actionCount, value: countValue{target: new(int)}})

	got := renderHelp(cmd, shortHelp, noVariables, palette{})

	want := "Options:\n" +
		"  -x <X>         Short only\n" +
		"      --long...  Long only\n" +
		"  -h, --help     Print help\n"
	if !strings.HasSuffix(got, want) {
		t.Errorf("help =\n%s\nwant it to end with\n%s", got, want)
	}
}

func TestACommandWithoutADescriptionStartsWithItsUsage(t *testing.T) {
	cmd := newCommand("fixture", "", nil)

	if got := renderHelp(cmd, longHelp, noVariables, palette{}); !strings.HasPrefix(got, "Usage: fixture\n") {
		t.Errorf("help starts %q", got)
	}
}

func TestLongHelpIsTheSummaryWhereThereIsNoMoreToSay(t *testing.T) {
	cmd := newFixture().root.subcommand("group")

	long, short := renderHelp(cmd, longHelp, noVariables, palette{}), renderHelp(cmd, shortHelp, noVariables, palette{})

	if long != short {
		t.Errorf("long help\n%s\ndiffers from the summary\n%s", long, short)
	}
}

func TestTextIsPaddedToAWidth(t *testing.T) {
	if got := padded("ab", 4); got != "ab  " {
		t.Errorf("padded = %q", got)
	}
	if got := padded("abcdef", 4); got != "abcdef" {
		t.Errorf("padded too long = %q", got)
	}
}
