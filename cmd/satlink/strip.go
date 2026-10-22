package main

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// What a stream that shows no styles keeps of help and errors. They are
// styled whatever stream they go to, so for such a stream the stripper takes
// out the styles, and whatever of the kind a token or variable brought in:
//
//   - the sequences that ESC starts: CSI, OSC, DCS, SOS, PM and APC alike;
//   - the C0 control characters and DEL, but for tab, line feed, form feed
//     and carriage return.
//
// A C1 control, two bytes in UTF-8 (U+009B, CSI, and U+0085, NEL, among
// them), stays with what follows it, as every UTF-8 character is kept whole.
// The parser follows the state table of the VT500-series terminals that Paul
// Williams described ("A parser for DEC's ANSI-compatible video terminals"),
// reduced to what stripping can tell apart.
//
// Edge cases, where a terminal would differ:
//
//   - an unfinished sequence in a token ends at the CLI's own style, so
//     $'\e[' gives '';
//   - each run of kept bytes is judged from the ground state, so
//     $'\e[\n1m' keeps '\n1m', where a terminal takes the 1 and the m
//     into the CSI;
//   - a kept byte leaves the parser where it was, so $'\e[\n\x01mz' gives
//     '\nz', with the m ending the CSI;
//   - only a kept character of more than one byte returns the parser to the
//     ground: $'\e[\né\x01mz' gives '\némz'.

// vtState is a state of the terminal parser: the ground, where characters
// show, and the parts of the sequences that ESC starts. Those are an escape,
// a CSI, a DCS, and the strings of OSC and of SOS, PM and APC.
type vtState int

const (
	// vtAnywhere is no state: a byte that moves the parser to it leaves the
	// parser where it was.
	vtAnywhere vtState = iota
	vtGround
	vtEscape
	vtEscapeIntermediate
	vtCSIEntry
	vtCSIParam
	vtCSIIntermediate
	vtCSIIgnore
	vtDCSEntry
	vtDCSParam
	vtDCSIntermediate
	vtDCSIgnore
	vtDCSPassthrough
	vtOSCString
	vtSOSPMAPCString
)

// follow moves the parser to next, unless next is vtAnywhere.
func (s *vtState) follow(next vtState) {
	if next != vtAnywhere {
		*s = next
	}
}

// vtAction is what the parser does with a byte, as far as stripping cares.
// The parser's other actions (collecting a sequence, dispatching it, passing
// a string on, ignoring the byte) all drop it.
type vtAction int

const (
	// vtDrop takes the byte into a sequence, or ignores it.
	vtDrop vtAction = iota
	// vtPrint shows the byte as a character.
	vtPrint
	// vtBeginUTF8 starts a UTF-8 character, which is kept whole.
	vtBeginUTF8
	// vtExecute carries the byte out as a control character.
	vtExecute
)

// vtRule moves the parser to next, with action, on a byte from first to
// last.
type vtRule struct {
	first, last byte
	next        vtState
	action      vtAction
}

// vtTransitions are the parser's rules, state by state, but for those that
// leave the parser where it was and drop the byte, as every byte that no
// rule names does. vtAnywhere's rules come before every state's own. The
// parser stays on the ground through a UTF-8 character, which it follows to
// its end.
var vtTransitions = [...][]vtRule{
	vtAnywhere: {
		{0x18, 0x18, vtGround, vtExecute},
		{0x1a, 0x1a, vtGround, vtExecute},
		{0x1b, 0x1b, vtEscape, vtDrop},
	},
	vtGround: executingC0Controls(
		vtRule{0x20, 0x7f, vtAnywhere, vtPrint},
		vtRule{0xc2, 0xf4, vtGround, vtBeginUTF8},
	),
	vtEscape: executingC0Controls(
		vtRule{0x20, 0x2f, vtEscapeIntermediate, vtDrop},
		vtRule{0x30, 0x4f, vtGround, vtDrop},
		vtRule{0x51, 0x57, vtGround, vtDrop},
		vtRule{0x59, 0x5a, vtGround, vtDrop},
		vtRule{0x5c, 0x5c, vtGround, vtDrop},
		vtRule{0x60, 0x7e, vtGround, vtDrop},
		vtRule{0x5b, 0x5b, vtCSIEntry, vtDrop},
		vtRule{0x5d, 0x5d, vtOSCString, vtDrop},
		vtRule{0x50, 0x50, vtDCSEntry, vtDrop},
		vtRule{0x58, 0x58, vtSOSPMAPCString, vtDrop},
		vtRule{0x5e, 0x5f, vtSOSPMAPCString, vtDrop},
	),
	vtEscapeIntermediate: executingC0Controls(
		vtRule{0x30, 0x7e, vtGround, vtDrop},
	),
	vtCSIEntry: executingC0Controls(
		vtRule{0x20, 0x2f, vtCSIIntermediate, vtDrop},
		vtRule{0x30, 0x3f, vtCSIParam, vtDrop},
		vtRule{0x40, 0x7e, vtGround, vtDrop},
	),
	vtCSIParam: executingC0Controls(
		vtRule{0x3c, 0x3f, vtCSIIgnore, vtDrop},
		vtRule{0x20, 0x2f, vtCSIIntermediate, vtDrop},
		vtRule{0x40, 0x7e, vtGround, vtDrop},
	),
	vtCSIIntermediate: executingC0Controls(
		vtRule{0x30, 0x3f, vtCSIIgnore, vtDrop},
		vtRule{0x40, 0x7e, vtGround, vtDrop},
	),
	vtCSIIgnore: executingC0Controls(
		vtRule{0x40, 0x7e, vtGround, vtDrop},
	),
	vtDCSEntry: {
		{0x20, 0x2f, vtDCSIntermediate, vtDrop},
		{0x30, 0x3f, vtDCSParam, vtDrop},
		{0x40, 0x7e, vtDCSPassthrough, vtDrop},
	},
	vtDCSParam: {
		{0x3c, 0x3f, vtDCSIgnore, vtDrop},
		{0x20, 0x2f, vtDCSIntermediate, vtDrop},
		{0x40, 0x7e, vtDCSPassthrough, vtDrop},
	},
	vtDCSIntermediate: {
		{0x30, 0x3f, vtDCSIgnore, vtDrop},
		{0x40, 0x7e, vtDCSPassthrough, vtDrop},
	},
	vtDCSIgnore:      {{0x9c, 0x9c, vtGround, vtDrop}},
	vtDCSPassthrough: {{0x9c, 0x9c, vtGround, vtDrop}},
	vtSOSPMAPCString: {{0x9c, 0x9c, vtGround, vtDrop}},
	vtOSCString:      {{0x07, 0x07, vtGround, vtDrop}},
}

// executingC0Controls returns the rules of a state that carries out the C0
// control characters — all but CAN, SUB and ESC, which vtAnywhere's rules
// take — followed by rules.
func executingC0Controls(rules ...vtRule) []vtRule {
	return slices.Concat([]vtRule{
		{0x00, 0x17, vtAnywhere, vtExecute},
		{0x19, 0x19, vtAnywhere, vtExecute},
		{0x1c, 0x1f, vtAnywhere, vtExecute},
	}, rules)
}

// vtTransition returns the state that b moves the parser to from state —
// vtAnywhere when it stays where it was — and what the parser does with b.
func vtTransition(state vtState, b byte) (vtState, vtAction) {
	for _, rules := range [][]vtRule{vtTransitions[vtAnywhere], vtTransitions[state]} {
		for _, rule := range rules {
			if rule.first <= b && b <= rule.last {
				return rule.next, rule.action
			}
		}
	}
	return vtAnywhere, vtDrop
}

// stripStyles returns text for a stream that shows no styles: without the
// sequences that ESC starts (CSI, OSC, DCS, SOS, PM and APC alike), and
// without its C0 control characters and DEL but for tab, line feed, form
// feed and carriage return. A C1 control, two bytes in UTF-8, stays, as
// every UTF-8 character is kept whole. text is UTF-8, as help and errors
// always are.
func stripStyles(text string) string {
	var kept strings.Builder
	state := vtGround
	for text != "" {
		text = text[droppedLength(text, &state):]
		printable := printableLength(text, &state)
		kept.WriteString(text[:printable])
		text = text[printable:]
	}
	return kept.String()
}

// droppedLength returns how many bytes at the start of text are dropped,
// moving the parser from state through them, and through the byte kept
// after them.
func droppedLength(text string, state *vtState) int {
	for i := range len(text) {
		next, action := vtTransition(*state, text[i])
		state.follow(next)
		if isPrintable(text[i], action) {
			return i
		}
	}
	return len(text)
}

// printableLength returns how many bytes at the start of text are kept:
// each is judged as if the parser were on the ground, a UTF-8 character is
// kept whole, and the parser moves only as those judgements do, so
// that a byte kept leaves the parser where it was, even inside a sequence,
// and a character of more than one byte returns it to the ground.
func printableLength(text string, state *vtState) int {
	for i := 0; i < len(text); {
		next, action := vtTransition(vtGround, text[i])
		state.follow(next)
		switch {
		case action == vtBeginUTF8:
			_, size := utf8.DecodeRuneInString(text[i:])
			i += size
		case isPrintable(text[i], action):
			i++
		default:
			return i
		}
	}
	return len(text)
}

// isPrintable reports whether b is kept, which the parser handled with
// action: a character to show but DEL, the start of a UTF-8
// character, or a control character that is ASCII white space.
func isPrintable(b byte, action vtAction) bool {
	// DEL, which the parser shows as VT320s did, but which is dropped as
	// the control character it is in UTF-8.
	const del = 0x7f
	switch action {
	case vtPrint:
		return b != del
	case vtBeginUTF8:
		return true
	case vtExecute:
		return b == '\t' || b == '\n' || b == '\f' || b == '\r'
	default:
		return false
	}
}
