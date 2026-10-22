package main

// The styles that the Rust version's clap gave its help and errors — the
// ANSI rendering of its default styles — and the streams that showed them.

// The ANSI sequences of clap's default styles.
const (
	ansiReset     = "\x1b[0m"
	ansiBold      = "\x1b[1m"
	ansiUnderline = "\x1b[4m"
	ansiRed       = "\x1b[31m"
	ansiGreen     = "\x1b[32m"
	ansiYellow    = "\x1b[33m"
)

// The environment variables that decide whether a stream is styled, as the
// anstream crate that clap wrote through read them.
const (
	// noColorVariable, set to anything but "", styles nothing
	// (no-color.org).
	noColorVariable = "NO_COLOR"
	// forceColorVariable, set to anything but "", styles any stream,
	// terminal or not (bixense.com/clicolors).
	forceColorVariable = "CLICOLOR_FORCE"
	// colorVariable styles nothing when set to 0, and a terminal otherwise.
	colorVariable = "CLICOLOR"
	// ciVariable, set at all, says a CI run, whose log styles show.
	ciVariable = "CI"
	// termVariable names the terminal; one called dumb has no styles.
	termVariable = "TERM"
)

// dumbTerminal is the TERM of a terminal without styles.
const dumbTerminal = "dumb"

// palette styles help and errors. The CLI builds them in clap's styles,
// whatever stream they go to, as clap did, and a stream that shows no
// styles takes them out again (stripStyles). The zero palette, clap's plain
// styles, styles nothing.
type palette struct {
	styled bool
}

// showsStyles reports whether a stream, which isTerminal says is a
// terminal, shows styles in the environment that lookupEnv finds: anstream's
// choice, which kept clap's styles for the stream or took them out.
func showsStyles(isTerminal bool, lookupEnv func(string) (string, bool)) bool {
	isSet := func(variable string) bool {
		_, set := lookupEnv(variable)
		return set
	}
	isSetToSomething := func(variable string) bool {
		value, _ := lookupEnv(variable)
		return value != ""
	}
	color, colorSet := lookupEnv(colorVariable)
	term, termSet := lookupEnv(termVariable)
	switch {
	case isSetToSomething(noColorVariable):
		return false
	case isSetToSomething(forceColorVariable):
		return true
	case colorSet && color == "0":
		return false
	}
	knowsStyles := termSet && term != dumbTerminal || colorSet || isSet(ciVariable)
	return isTerminal && knowsStyles
}

// header styles a heading: "Usage:", "Options:".
func (p palette) header(text string) string {
	return p.paint(ansiBold+ansiUnderline, text)
}

// literal styles what is typed as it is: the name of a command or option.
func (p palette) literal(text string) string {
	return p.paint(ansiBold, text)
}

// failure styles "error:".
func (p palette) failure(text string) string {
	return p.paint(ansiBold+ansiRed, text)
}

// valid styles what would be right: a suggestion, a value that is taken.
func (p palette) valid(text string) string {
	return p.paint(ansiGreen, text)
}

// invalid styles what is wrong: the token or value refused.
func (p palette) invalid(text string) string {
	return p.paint(ansiYellow, text)
}

// paint gives text a style, even an empty text, as clap did.
func (p palette) paint(style, text string) string {
	if !p.styled {
		return text
	}
	return style + text + ansiReset
}
