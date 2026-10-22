package main

import (
	"io"
	"slices"
	"strings"
	"testing"
)

// What anstream 1.0.0's StripStream passed on to a stream that shows no
// styles: its own tests' cases first, then sequences and control characters
// that the command line can bring into clap's styles, the sequences finished
// or not. The parser's rules are checked one by one against anstyle-parse's
// below; these cases check the two passes over the text that use them.
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

// Go only: the port's rules against anstyle-parse 1.0.0's own, for every
// state and every byte. The port leaves out the rules that stripping cannot
// tell from no rule at all, so the two are compared as stripping sees them:
// each byte must move the parser where anstyle-parse moved it, and be
// dropped or kept as anstream dropped or kept it.
func TestTheParsersRulesAreAnstyleParsesAsStrippingSeesThem(t *testing.T) {
	for state := range anstyleParseRules {
		for b := range 256 {
			got, want := portStep(vtState(state), byte(b)), anstreamStep(vtState(state), byte(b))
			if got != want {
				t.Errorf("state %d, byte %#02x: the port moves to state %d, %v; anstyle-parse moved to %d, %v",
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

// portStep is the step that b takes from state by the port's rules.
func portStep(state vtState, b byte) strippingStep {
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

// anstreamStep is the step that b took from state by anstyle-parse's rules,
// Anywhere's first, as its state_change looks them up, with anstream's
// judgement of the byte. The port stays on the ground through a UTF-8
// character, which anstyle-parse follows in its Utf8 state.
func anstreamStep(state vtState, b byte) strippingStep {
	for _, rules := range [][]anstyleRule{anstyleParseRules[vtAnywhere], anstyleParseRules[state]} {
		for _, rule := range rules {
			if rule.first <= b && b <= rule.last {
				next := rule.next
				if next == vtUTF8 {
					next = vtGround
				}
				return strippingStep{next, anstreamFate(b, rule.action)}
			}
		}
	}
	// No rule: (Anywhere, Nop).
	return strippingStep{vtAnywhere, byteDropped}
}

// anstreamFate is whether anstream kept b, which anstyle-parse handled with
// action (adapter/strip.rs, is_printable_bytes): a character to show but
// DEL, the start of a UTF-8 character, or a control character that is ASCII
// white space.
func anstreamFate(b byte, action anstyleAction) byteFate {
	const del = 0x7f
	// The bytes of Rust's u8::is_ascii_whitespace.
	const asciiWhiteSpace = " \t\n\f\r"
	switch {
	case action == anstyleBeginUTF8:
		return characterKept
	case action == anstylePrint && b != del,
		action == anstyleExecute && strings.IndexByte(asciiWhiteSpace, b) >= 0:
		return byteKept
	default:
		return byteDropped
	}
}

// anstyleAction is one of anstyle-parse's actions (state/definitions.rs),
// of which stripping tells three apart: Print, Execute and BeginUtf8.
type anstyleAction int

const (
	anstyleNop anstyleAction = iota
	anstyleCollect
	anstyleCSIDispatch
	anstyleESCDispatch
	anstyleExecute
	anstyleIgnore
	anstyleOSCPut
	anstyleParam
	anstylePrint
	anstylePut
	anstyleBeginUTF8
)

// anstyleRule is a line of anstyle-parse's rules: a byte from first to last
// moves the parser to next, with action.
type anstyleRule struct {
	first, last byte
	next        vtState
	action      anstyleAction
}

// vtUTF8 is anstyle-parse's state inside a UTF-8 character, which the port
// does without: a number that none of the port's states has.
const vtUTF8 = vtSOSPMAPCString + 1

// anstyleParseRules are anstyle-parse 1.0.0's rules (state/codegen.rs),
// state by state and line by line, in its order and with its actions; only
// the states have the port's names. A byte that no rule of a state names
// leaves the parser where it was and does nothing (Anywhere, Nop).
var anstyleParseRules = [...][]anstyleRule{
	vtAnywhere: {
		{0x18, 0x18, vtGround, anstyleExecute},
		{0x1a, 0x1a, vtGround, anstyleExecute},
		{0x1b, 0x1b, vtEscape, anstyleNop},
	},
	vtGround: {
		{0x00, 0x17, vtAnywhere, anstyleExecute},
		{0x19, 0x19, vtAnywhere, anstyleExecute},
		{0x1c, 0x1f, vtAnywhere, anstyleExecute},
		{0x20, 0x7f, vtAnywhere, anstylePrint},
		{0x80, 0x8f, vtAnywhere, anstyleExecute},
		{0x91, 0x9a, vtAnywhere, anstyleExecute},
		{0x9c, 0x9c, vtAnywhere, anstyleExecute},
		{0xc2, 0xdf, vtUTF8, anstyleBeginUTF8},
		{0xe0, 0xef, vtUTF8, anstyleBeginUTF8},
		{0xf0, 0xf4, vtUTF8, anstyleBeginUTF8},
	},
	vtEscape: {
		{0x00, 0x17, vtAnywhere, anstyleExecute},
		{0x19, 0x19, vtAnywhere, anstyleExecute},
		{0x1c, 0x1f, vtAnywhere, anstyleExecute},
		{0x7f, 0x7f, vtAnywhere, anstyleIgnore},
		{0x20, 0x2f, vtEscapeIntermediate, anstyleCollect},
		{0x30, 0x4f, vtGround, anstyleESCDispatch},
		{0x51, 0x57, vtGround, anstyleESCDispatch},
		{0x59, 0x59, vtGround, anstyleESCDispatch},
		{0x5a, 0x5a, vtGround, anstyleESCDispatch},
		{0x5c, 0x5c, vtGround, anstyleESCDispatch},
		{0x60, 0x7e, vtGround, anstyleESCDispatch},
		{0x5b, 0x5b, vtCSIEntry, anstyleNop},
		{0x5d, 0x5d, vtOSCString, anstyleNop},
		{0x50, 0x50, vtDCSEntry, anstyleNop},
		{0x58, 0x58, vtSOSPMAPCString, anstyleNop},
		{0x5e, 0x5e, vtSOSPMAPCString, anstyleNop},
		{0x5f, 0x5f, vtSOSPMAPCString, anstyleNop},
	},
	vtEscapeIntermediate: {
		{0x00, 0x17, vtAnywhere, anstyleExecute},
		{0x19, 0x19, vtAnywhere, anstyleExecute},
		{0x1c, 0x1f, vtAnywhere, anstyleExecute},
		{0x20, 0x2f, vtAnywhere, anstyleCollect},
		{0x7f, 0x7f, vtAnywhere, anstyleIgnore},
		{0x30, 0x7e, vtGround, anstyleESCDispatch},
	},
	vtCSIEntry: {
		{0x00, 0x17, vtAnywhere, anstyleExecute},
		{0x19, 0x19, vtAnywhere, anstyleExecute},
		{0x1c, 0x1f, vtAnywhere, anstyleExecute},
		{0x7f, 0x7f, vtAnywhere, anstyleIgnore},
		{0x20, 0x2f, vtCSIIntermediate, anstyleCollect},
		{0x30, 0x39, vtCSIParam, anstyleParam},
		{0x3a, 0x3b, vtCSIParam, anstyleParam},
		{0x3c, 0x3f, vtCSIParam, anstyleCollect},
		{0x40, 0x7e, vtGround, anstyleCSIDispatch},
	},
	vtCSIIgnore: {
		{0x00, 0x17, vtAnywhere, anstyleExecute},
		{0x19, 0x19, vtAnywhere, anstyleExecute},
		{0x1c, 0x1f, vtAnywhere, anstyleExecute},
		{0x20, 0x3f, vtAnywhere, anstyleIgnore},
		{0x7f, 0x7f, vtAnywhere, anstyleIgnore},
		{0x40, 0x7e, vtGround, anstyleNop},
	},
	vtCSIParam: {
		{0x00, 0x17, vtAnywhere, anstyleExecute},
		{0x19, 0x19, vtAnywhere, anstyleExecute},
		{0x1c, 0x1f, vtAnywhere, anstyleExecute},
		{0x30, 0x39, vtAnywhere, anstyleParam},
		{0x3a, 0x3b, vtAnywhere, anstyleParam},
		{0x7f, 0x7f, vtAnywhere, anstyleIgnore},
		{0x3c, 0x3f, vtCSIIgnore, anstyleNop},
		{0x20, 0x2f, vtCSIIntermediate, anstyleCollect},
		{0x40, 0x7e, vtGround, anstyleCSIDispatch},
	},
	vtCSIIntermediate: {
		{0x00, 0x17, vtAnywhere, anstyleExecute},
		{0x19, 0x19, vtAnywhere, anstyleExecute},
		{0x1c, 0x1f, vtAnywhere, anstyleExecute},
		{0x20, 0x2f, vtAnywhere, anstyleCollect},
		{0x7f, 0x7f, vtAnywhere, anstyleIgnore},
		{0x30, 0x3f, vtCSIIgnore, anstyleNop},
		{0x40, 0x7e, vtGround, anstyleCSIDispatch},
	},
	vtDCSEntry: {
		{0x00, 0x17, vtAnywhere, anstyleIgnore},
		{0x19, 0x19, vtAnywhere, anstyleIgnore},
		{0x1c, 0x1f, vtAnywhere, anstyleIgnore},
		{0x7f, 0x7f, vtAnywhere, anstyleIgnore},
		{0x20, 0x2f, vtDCSIntermediate, anstyleCollect},
		{0x30, 0x39, vtDCSParam, anstyleParam},
		{0x3a, 0x3b, vtDCSParam, anstyleParam},
		{0x3c, 0x3f, vtDCSParam, anstyleCollect},
		{0x40, 0x7e, vtDCSPassthrough, anstyleNop},
	},
	vtDCSIntermediate: {
		{0x00, 0x17, vtAnywhere, anstyleIgnore},
		{0x19, 0x19, vtAnywhere, anstyleIgnore},
		{0x1c, 0x1f, vtAnywhere, anstyleIgnore},
		{0x20, 0x2f, vtAnywhere, anstyleCollect},
		{0x7f, 0x7f, vtAnywhere, anstyleIgnore},
		{0x30, 0x3f, vtDCSIgnore, anstyleNop},
		{0x40, 0x7e, vtDCSPassthrough, anstyleNop},
	},
	vtDCSIgnore: {
		{0x00, 0x17, vtAnywhere, anstyleIgnore},
		{0x19, 0x19, vtAnywhere, anstyleIgnore},
		{0x1c, 0x1f, vtAnywhere, anstyleIgnore},
		{0x20, 0x7f, vtAnywhere, anstyleIgnore},
		{0x9c, 0x9c, vtGround, anstyleNop},
	},
	vtDCSParam: {
		{0x00, 0x17, vtAnywhere, anstyleIgnore},
		{0x19, 0x19, vtAnywhere, anstyleIgnore},
		{0x1c, 0x1f, vtAnywhere, anstyleIgnore},
		{0x30, 0x39, vtAnywhere, anstyleParam},
		{0x3a, 0x3b, vtAnywhere, anstyleParam},
		{0x7f, 0x7f, vtAnywhere, anstyleIgnore},
		{0x3c, 0x3f, vtDCSIgnore, anstyleNop},
		{0x20, 0x2f, vtDCSIntermediate, anstyleCollect},
		{0x40, 0x7e, vtDCSPassthrough, anstyleNop},
	},
	vtDCSPassthrough: {
		{0x00, 0x17, vtAnywhere, anstylePut},
		{0x19, 0x19, vtAnywhere, anstylePut},
		{0x1c, 0x1f, vtAnywhere, anstylePut},
		{0x20, 0x7e, vtAnywhere, anstylePut},
		{0x7f, 0x7f, vtAnywhere, anstyleIgnore},
		{0x9c, 0x9c, vtGround, anstyleNop},
	},
	vtSOSPMAPCString: {
		{0x00, 0x17, vtAnywhere, anstyleIgnore},
		{0x19, 0x19, vtAnywhere, anstyleIgnore},
		{0x1c, 0x1f, vtAnywhere, anstyleIgnore},
		{0x20, 0x7f, vtAnywhere, anstyleIgnore},
		{0x9c, 0x9c, vtGround, anstyleNop},
	},
	vtOSCString: {
		{0x00, 0x06, vtAnywhere, anstyleIgnore},
		{0x07, 0x07, vtGround, anstyleNop},
		{0x08, 0x17, vtAnywhere, anstyleIgnore},
		{0x19, 0x19, vtAnywhere, anstyleIgnore},
		{0x1c, 0x1f, vtAnywhere, anstyleIgnore},
		{0x20, 0xff, vtAnywhere, anstyleOSCPut},
	},
}
