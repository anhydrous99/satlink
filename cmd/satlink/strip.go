package main

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// What a stream that shows no styles kept of clap's help and errors. clap
// built them in its styles whatever stream they went to, and wrote them
// through anstream, which took out of the text for such a stream the
// sequences that ESC starts, and the C0 control characters and DEL but for
// tab, line feed, form feed and carriage return: the styles, and whatever
// of the kind the command line or the environment brought in. A C1
// control, two bytes in UTF-8, stayed, as anstream kept every UTF-8
// character whole. This is anstream 1.0.0's StripBytes (adapter/strip.rs)
// over the state table of anstyle-parse 1.0.0 (state/codegen.rs), the
// parser of DEC's terminals that Paul Williams described.

// vtState is a state of the terminal parser. They are named as in
// anstyle-parse: the ground, where characters show, and the parts of the
// sequences that ESC starts — an escape, a CSI, a DCS, and the strings of
// OSC and of SOS, PM and APC.
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

// vtAction is what the parser does with a byte, as far as stripping cares:
// anstyle-parse's other actions — collecting a sequence, dispatching it,
// passing a string on, ignoring the byte — all drop it.
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

// vtTransitions are anstyle-parse's rules, state by state, but for those
// that leave the parser where it was and drop the byte, as every byte that
// no rule names does. vtAnywhere's rules come before every state's own. The
// parser stays on the ground through a UTF-8 character, which
// anstyle-parse's Utf8 state follows to its end.
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

// stripStyles returns text as anstream's StripStream passed it on to a
// stream that shows no styles: without the sequences that ESC starts —
// CSI, OSC, DCS, SOS, PM and APC alike — and without its C0 control
// characters and DEL but for tab, line feed, form feed and carriage return.
// A C1 control, two bytes in UTF-8, stays, as anstream kept every UTF-8
// character whole. text is UTF-8, as clap's always was.
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

// droppedLength returns how many bytes at the start of text StripStream
// dropped, moving the parser from state through them, and through the byte
// it kept after them.
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

// printableLength returns how many bytes at the start of text StripStream
// kept: it judged each as if the parser were on the ground, kept a UTF-8
// character whole, and moved the parser only as those judgements did, so
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

// isPrintable reports whether StripStream kept b, which the parser handled
// with action: a character to show but DEL, the start of a UTF-8
// character, or a control character that is ASCII white space.
func isPrintable(b byte, action vtAction) bool {
	// DEL, which the parser shows as VT320s did, and anstream drops as the
	// control character it is in UTF-8.
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
