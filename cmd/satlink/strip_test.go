package main

import (
	"io"
	"slices"
	"testing"
)

// What anstream 1.0.0's StripStream passed on to a stream that shows no
// styles: its own tests' cases first, then sequences that the command line
// can leave unfinished inside clap's styles.
func TestStrippingKeepsWhatAnstreamKept(t *testing.T) {
	tests := []struct {
		name, text, kept string
	}{
		{"styles", "\x1b[32mfoo\x1b[m bar", "foo bar"},
		{"DEL", "\x7f", ""},
		{"a character in four bytes", "\U0001134B", "\U0001134B"},
		{"a sequence broken off by a character", "ö\x1b😀hello😀goodbye", "öello😀goodbye"},
		{"a CSI that the style after it ends", "'\x1b[33m\x1b[\x1b[0m'", "''"},
		{"an OSC that BEL ends", "a\x1b]0;title\x07b", "ab"},
		{"an OSC that ESC ends", "a\x1b]0;title\x1b[0mb", "ab"},
		{"a DCS that ST ends", "\x1bPq#0\x1b\\z", "z"},
		{"an SOS that a C1 ST ends in a character", "\x1bXĜz", "z"},
		{"a sequence that CAN cancels", "\x1b[1\x18x", "x"},
		{"a sequence that SUB cancels", "\x1b]0\x1ax", "x"},
		{"control characters, white space kept", "a\x01\x08b\tc\nd\x0be\x0cf\rg", "ab\tc\nde\x0cf\rg"},
		// Each run kept is judged from the ground, where a terminal would take
		// the 1 and the m into the CSI, or end it at the f.
		{"white space inside a CSI", "\x1b[\n1m", "\n1m"},
		{"white space inside a CSI before a final byte", "\x1b[\nfoo", "\nfoo"},
		{"a control character inside a CSI", "\x1b[\x07foo", "oo"},
		{"an escape before a character", "\x1b[éz", ""},
		{"nothing to strip", "Usage: satlink [OPTIONS] <COMMAND>", "Usage: satlink [OPTIONS] <COMMAND>"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stripStyles(test.text); got != test.kept {
				t.Errorf("stripStyles(%q) = %q, want %q", test.text, got, test.kept)
			}
		})
	}
}

// The tests read command lines with the zero palette, so that they can say
// what the text is: it must be what a stream that shows no styles gets.
func TestStrippingClapsStylesLeavesWhatTheZeroPaletteWrites(t *testing.T) {
	for _, test := range slices.Concat(helpCases(), usageCases(), environmentCases(), styleCases()) {
		t.Run(test.name, func(t *testing.T) {
			styled := commandLineExitText(t, test.world, test.args, palette{styled: true})

			if plain := commandLineExitText(t, test.world, test.args, palette{}); stripStyles(styled) != plain {
				t.Errorf("stripped of its styles,\n%q\nis\n%q\nwhere the zero palette writes\n%q",
					styled, stripStyles(styled), plain)
			}
		})
	}
}

// commandLineExitText reads args in w with styles from p, and returns what
// the exit that ends the run prints: the help, the version or the problem.
func commandLineExitText(t *testing.T, w world, args []string, p palette) string {
	t.Helper()
	env, _, _ := w.environment(t, args)
	root := newCLI(env, io.Discard).rootCommand()
	_, exit := parseCommandLine(root, args, surroundings{lookupEnv: env.lookupEnv, palette: p})
	if exit == nil {
		t.Fatalf("%q runs a command", args)
	}
	return exit.text
}
