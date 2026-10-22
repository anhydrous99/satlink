package main

import "testing"

func TestStreamsAreStyledAsAnstreamChose(t *testing.T) {
	tests := []struct {
		name       string
		isTerminal bool
		variables  map[string]string
		styled     bool
	}{
		{"a terminal", true, map[string]string{termVariable: "xterm"}, true},
		{"a terminal with no TERM", true, nil, false},
		{"a dumb terminal", true, map[string]string{termVariable: dumbTerminal}, false},
		{"a terminal with an empty TERM", true, map[string]string{termVariable: ""}, true},
		{"a terminal with NO_COLOR", true, map[string]string{termVariable: "xterm", noColorVariable: "1"}, false},
		{"a terminal with an empty NO_COLOR", true, map[string]string{termVariable: "xterm", noColorVariable: ""}, true},
		{"a terminal with CLICOLOR 0", true, map[string]string{termVariable: "xterm", colorVariable: "0"}, false},
		{"a terminal with CLICOLOR and no TERM", true, map[string]string{colorVariable: "1"}, true},
		{"a terminal with an empty CLICOLOR", true, map[string]string{colorVariable: ""}, true},
		{"a terminal in CI", true, map[string]string{ciVariable: ""}, true},
		{"a pipe", false, map[string]string{termVariable: "xterm"}, false},
		{"a pipe with CLICOLOR_FORCE", false, map[string]string{forceColorVariable: "1"}, true},
		{"a pipe with CLICOLOR_FORCE 0", false, map[string]string{forceColorVariable: "0"}, true},
		{"a pipe with an empty CLICOLOR_FORCE", false, map[string]string{forceColorVariable: ""}, false},
		{"NO_COLOR over CLICOLOR_FORCE", false, map[string]string{forceColorVariable: "1", noColorVariable: "1"}, false},
		{"CLICOLOR_FORCE over CLICOLOR 0", false, map[string]string{forceColorVariable: "1", colorVariable: "0"}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookupEnv := func(key string) (string, bool) {
				value, set := test.variables[key]
				return value, set
			}

			if got := showsStyles(test.isTerminal, lookupEnv); got != test.styled {
				t.Errorf("styled %t, want %t", got, test.styled)
			}
		})
	}
}

func TestAPaletteStylesEvenEmptyTextOrNothingAtAll(t *testing.T) {
	styled := palette{styled: true}

	if got := styled.invalid(""); got != ansiYellow+ansiReset {
		t.Errorf("an empty text styled %q", got)
	}
	if got := styled.header("Usage:"); got != "\x1b[1m\x1b[4mUsage:\x1b[0m" {
		t.Errorf("a heading styled %q", got)
	}
	if got := (palette{}).failure("error:"); got != "error:" {
		t.Errorf("the zero palette styled %q", got)
	}
}

func TestStylesTakeNoRoomOnTheScreen(t *testing.T) {
	tests := map[string]int{
		"\x1b[1m-p\x1b[0m, \x1b[1m--port\x1b[0m <PORT>": len("-p, --port <PORT>"),
		"plain":             5,
		"\x1b[":             2,
		"\x1b[1x":           4,
		"\x1b[1;4mbold\x1b": 5,
	}
	for text, want := range tests {
		if got := displayWidth(text); got != want {
			t.Errorf("displayWidth(%q) = %d, want %d", text, got, want)
		}
	}
}
