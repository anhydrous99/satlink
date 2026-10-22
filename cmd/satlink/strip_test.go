package main

import (
	"io"
	"slices"
	"strings"
	"testing"
)

// What a stream that shows no styles gets: common cases first, then
// sequences and control characters that the command line can bring into the
// CLI's styles, the sequences finished or not. The parser's rules are
// checked one by one against the full parser table below; these cases check
// the two passes over the text that use them.
func TestStrippingKeepsWhatAStreamWithoutStylesShows(t *testing.T) {
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
		// Every UTF-8 character is kept whole, so a C1 control such as CSI or
		// NEL stays.
		{"C1 controls", "a\u009b31mb\u0085c", "a\u009b31mb\u0085c"},
		// Each run kept is judged from the ground, where a terminal would take
		// the 1 and the m into the CSI, or end it at the f.
		{"white space inside a CSI", "\x1b[\n1m", "\n1m"},
		{"white space inside a CSI before a final byte", "\x1b[\nfoo", "\nfoo"},
		// A byte kept, white space or not, leaves the parser in the CSI, which
		// the m after the control character ends; a character of more than
		// one byte, kept whole, returns the parser to the ground, where the m
		// shows.
		{"white space and a control character inside a CSI", "\x1b[\n\x01mz", "\nz"},
		{"a byte after white space inside a CSI", "\x1b[\na\x01mz", "\naz"},
		{"a character after white space inside a CSI", "\x1b[\né\x01mz", "\némz"},
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
func TestStrippingTheStylesLeavesWhatTheZeroPaletteWrites(t *testing.T) {
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

// The stripper's rules against the full parser table, for every state and
// every byte. The stripper leaves out the rules that stripping cannot tell
// from no rule at all, so the two are compared as stripping sees them: each
// byte must move the parser where the full table moves it, and be dropped or
// kept as referenceFate says.
func TestTheStrippersRulesMatchTheFullParserTable(t *testing.T) {
	for state := range referenceRules {
		for b := range 256 {
			got, want := vtStep(vtState(state), byte(b)), referenceStep(vtState(state), byte(b))
			if got != want {
				t.Errorf("state %d, byte %#02x: the stripper moves to state %d, %v; the full table moves to %d, %v",
					state, b, got.next, got.fate, want.next, want.fate)
				// The first difference in a state shows which rule to look at.
				break
			}
		}
	}
}

// strippingStep is what a byte does as stripping sees it: where it moves
// the parser — vtAnywhere when it leaves the parser where it was — and
// whether it is kept.
type strippingStep struct {
	next vtState
	fate byteFate
}

// byteFate is whether stripping keeps a byte.
type byteFate int

const (
	byteDropped byteFate = iota
	byteKept
	// characterKept is the fate of a byte that starts a UTF-8 character,
	// which is kept whole.
	characterKept
)

// String returns the fate as the test's messages name it.
func (f byteFate) String() string {
	return [...]string{byteDropped: "byte dropped", byteKept: "byte kept", characterKept: "character kept"}[f]
}

// vtStep is the step that b takes from state by the stripper's rules.
func vtStep(state vtState, b byte) strippingStep {
	next, action := vtTransition(state, b)
	switch {
	case action == vtBeginUTF8:
		return strippingStep{next, characterKept}
	case isPrintable(b, action):
		return strippingStep{next, byteKept}
	default:
		return strippingStep{next, byteDropped}
	}
}

// referenceStep is the step that b takes from state by the full table's
// rules, Anywhere's first, with referenceFate's judgement of the byte. The
// stripper stays on the ground through a UTF-8 character, which the full
// table follows in a state of its own.
func referenceStep(state vtState, b byte) strippingStep {
	for _, rules := range [][]referenceRule{referenceRules[vtAnywhere], referenceRules[state]} {
		for _, rule := range rules {
			if rule.first <= b && b <= rule.last {
				next := rule.next
				if next == vtUTF8 {
					next = vtGround
				}
				return strippingStep{next, referenceFate(b, rule.action)}
			}
		}
	}
	// No rule: (Anywhere, Nop).
	return strippingStep{vtAnywhere, byteDropped}
}

// referenceFate is whether a stream without styles keeps b, which the full
// table handles with action: a character to show but DEL, the start of a
// UTF-8 character, or a control character that is ASCII white space.
func referenceFate(b byte, action parserAction) byteFate {
	const del = 0x7f
	// The ASCII whitespace bytes: space, tab, line feed, form feed and carriage return.
	const asciiWhiteSpace = " \t\n\f\r"
	switch {
	case action == parserBeginUTF8:
		return characterKept
	case action == parserPrint && b != del,
		action == parserExecute && strings.IndexByte(asciiWhiteSpace, b) >= 0:
		return byteKept
	default:
		return byteDropped
	}
}

// parserAction is one of the full table's actions, of which stripping tells
// three apart: Print, Execute and BeginUTF8.
type parserAction int

const (
	parserNop parserAction = iota
	parserCollect
	parserCSIDispatch
	parserESCDispatch
	parserExecute
	parserIgnore
	parserOSCPut
	parserParam
	parserPrint
	parserPut
	parserBeginUTF8
)

// referenceRule is a line of the full table: a byte from first to last
// moves the parser to next, with action.
type referenceRule struct {
	first, last byte
	next        vtState
	action      parserAction
}

// vtUTF8 is the full table's state inside a UTF-8 character, which the
// stripper does without: a number that none of its states has.
const vtUTF8 = vtSOSPMAPCString + 1

// referenceRules are the full VT500-series parser table that Paul Williams
// described, state by state and line by line, with every action, extended
// with a state that follows UTF-8 characters. A byte that no rule of a state
// names leaves the parser where it was and does nothing (Anywhere, Nop).
var referenceRules = [...][]referenceRule{
	vtAnywhere: {
		{0x18, 0x18, vtGround, parserExecute},
		{0x1a, 0x1a, vtGround, parserExecute},
		{0x1b, 0x1b, vtEscape, parserNop},
	},
	vtGround: {
		{0x00, 0x17, vtAnywhere, parserExecute},
		{0x19, 0x19, vtAnywhere, parserExecute},
		{0x1c, 0x1f, vtAnywhere, parserExecute},
		{0x20, 0x7f, vtAnywhere, parserPrint},
		{0x80, 0x8f, vtAnywhere, parserExecute},
		{0x91, 0x9a, vtAnywhere, parserExecute},
		{0x9c, 0x9c, vtAnywhere, parserExecute},
		{0xc2, 0xdf, vtUTF8, parserBeginUTF8},
		{0xe0, 0xef, vtUTF8, parserBeginUTF8},
		{0xf0, 0xf4, vtUTF8, parserBeginUTF8},
	},
	vtEscape: {
		{0x00, 0x17, vtAnywhere, parserExecute},
		{0x19, 0x19, vtAnywhere, parserExecute},
		{0x1c, 0x1f, vtAnywhere, parserExecute},
		{0x7f, 0x7f, vtAnywhere, parserIgnore},
		{0x20, 0x2f, vtEscapeIntermediate, parserCollect},
		{0x30, 0x4f, vtGround, parserESCDispatch},
		{0x51, 0x57, vtGround, parserESCDispatch},
		{0x59, 0x59, vtGround, parserESCDispatch},
		{0x5a, 0x5a, vtGround, parserESCDispatch},
		{0x5c, 0x5c, vtGround, parserESCDispatch},
		{0x60, 0x7e, vtGround, parserESCDispatch},
		{0x5b, 0x5b, vtCSIEntry, parserNop},
		{0x5d, 0x5d, vtOSCString, parserNop},
		{0x50, 0x50, vtDCSEntry, parserNop},
		{0x58, 0x58, vtSOSPMAPCString, parserNop},
		{0x5e, 0x5e, vtSOSPMAPCString, parserNop},
		{0x5f, 0x5f, vtSOSPMAPCString, parserNop},
	},
	vtEscapeIntermediate: {
		{0x00, 0x17, vtAnywhere, parserExecute},
		{0x19, 0x19, vtAnywhere, parserExecute},
		{0x1c, 0x1f, vtAnywhere, parserExecute},
		{0x20, 0x2f, vtAnywhere, parserCollect},
		{0x7f, 0x7f, vtAnywhere, parserIgnore},
		{0x30, 0x7e, vtGround, parserESCDispatch},
	},
	vtCSIEntry: {
		{0x00, 0x17, vtAnywhere, parserExecute},
		{0x19, 0x19, vtAnywhere, parserExecute},
		{0x1c, 0x1f, vtAnywhere, parserExecute},
		{0x7f, 0x7f, vtAnywhere, parserIgnore},
		{0x20, 0x2f, vtCSIIntermediate, parserCollect},
		{0x30, 0x39, vtCSIParam, parserParam},
		{0x3a, 0x3b, vtCSIParam, parserParam},
		{0x3c, 0x3f, vtCSIParam, parserCollect},
		{0x40, 0x7e, vtGround, parserCSIDispatch},
	},
	vtCSIIgnore: {
		{0x00, 0x17, vtAnywhere, parserExecute},
		{0x19, 0x19, vtAnywhere, parserExecute},
		{0x1c, 0x1f, vtAnywhere, parserExecute},
		{0x20, 0x3f, vtAnywhere, parserIgnore},
		{0x7f, 0x7f, vtAnywhere, parserIgnore},
		{0x40, 0x7e, vtGround, parserNop},
	},
	vtCSIParam: {
		{0x00, 0x17, vtAnywhere, parserExecute},
		{0x19, 0x19, vtAnywhere, parserExecute},
		{0x1c, 0x1f, vtAnywhere, parserExecute},
		{0x30, 0x39, vtAnywhere, parserParam},
		{0x3a, 0x3b, vtAnywhere, parserParam},
		{0x7f, 0x7f, vtAnywhere, parserIgnore},
		{0x3c, 0x3f, vtCSIIgnore, parserNop},
		{0x20, 0x2f, vtCSIIntermediate, parserCollect},
		{0x40, 0x7e, vtGround, parserCSIDispatch},
	},
	vtCSIIntermediate: {
		{0x00, 0x17, vtAnywhere, parserExecute},
		{0x19, 0x19, vtAnywhere, parserExecute},
		{0x1c, 0x1f, vtAnywhere, parserExecute},
		{0x20, 0x2f, vtAnywhere, parserCollect},
		{0x7f, 0x7f, vtAnywhere, parserIgnore},
		{0x30, 0x3f, vtCSIIgnore, parserNop},
		{0x40, 0x7e, vtGround, parserCSIDispatch},
	},
	vtDCSEntry: {
		{0x00, 0x17, vtAnywhere, parserIgnore},
		{0x19, 0x19, vtAnywhere, parserIgnore},
		{0x1c, 0x1f, vtAnywhere, parserIgnore},
		{0x7f, 0x7f, vtAnywhere, parserIgnore},
		{0x20, 0x2f, vtDCSIntermediate, parserCollect},
		{0x30, 0x39, vtDCSParam, parserParam},
		{0x3a, 0x3b, vtDCSParam, parserParam},
		{0x3c, 0x3f, vtDCSParam, parserCollect},
		{0x40, 0x7e, vtDCSPassthrough, parserNop},
	},
	vtDCSIntermediate: {
		{0x00, 0x17, vtAnywhere, parserIgnore},
		{0x19, 0x19, vtAnywhere, parserIgnore},
		{0x1c, 0x1f, vtAnywhere, parserIgnore},
		{0x20, 0x2f, vtAnywhere, parserCollect},
		{0x7f, 0x7f, vtAnywhere, parserIgnore},
		{0x30, 0x3f, vtDCSIgnore, parserNop},
		{0x40, 0x7e, vtDCSPassthrough, parserNop},
	},
	vtDCSIgnore: {
		{0x00, 0x17, vtAnywhere, parserIgnore},
		{0x19, 0x19, vtAnywhere, parserIgnore},
		{0x1c, 0x1f, vtAnywhere, parserIgnore},
		{0x20, 0x7f, vtAnywhere, parserIgnore},
		{0x9c, 0x9c, vtGround, parserNop},
	},
	vtDCSParam: {
		{0x00, 0x17, vtAnywhere, parserIgnore},
		{0x19, 0x19, vtAnywhere, parserIgnore},
		{0x1c, 0x1f, vtAnywhere, parserIgnore},
		{0x30, 0x39, vtAnywhere, parserParam},
		{0x3a, 0x3b, vtAnywhere, parserParam},
		{0x7f, 0x7f, vtAnywhere, parserIgnore},
		{0x3c, 0x3f, vtDCSIgnore, parserNop},
		{0x20, 0x2f, vtDCSIntermediate, parserCollect},
		{0x40, 0x7e, vtDCSPassthrough, parserNop},
	},
	vtDCSPassthrough: {
		{0x00, 0x17, vtAnywhere, parserPut},
		{0x19, 0x19, vtAnywhere, parserPut},
		{0x1c, 0x1f, vtAnywhere, parserPut},
		{0x20, 0x7e, vtAnywhere, parserPut},
		{0x7f, 0x7f, vtAnywhere, parserIgnore},
		{0x9c, 0x9c, vtGround, parserNop},
	},
	vtSOSPMAPCString: {
		{0x00, 0x17, vtAnywhere, parserIgnore},
		{0x19, 0x19, vtAnywhere, parserIgnore},
		{0x1c, 0x1f, vtAnywhere, parserIgnore},
		{0x20, 0x7f, vtAnywhere, parserIgnore},
		{0x9c, 0x9c, vtGround, parserNop},
	},
	vtOSCString: {
		{0x00, 0x06, vtAnywhere, parserIgnore},
		{0x07, 0x07, vtGround, parserNop},
		{0x08, 0x17, vtAnywhere, parserIgnore},
		{0x19, 0x19, vtAnywhere, parserIgnore},
		{0x1c, 0x1f, vtAnywhere, parserIgnore},
		{0x20, 0xff, vtAnywhere, parserOSCPut},
	},
}
