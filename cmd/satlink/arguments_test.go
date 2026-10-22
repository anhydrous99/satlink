package main

import "testing"

// argumentForms are the ways clap wrote an argument: in errors, in help, in
// a usage line as given or required, and as optional (positional ones only).
type argumentForms struct {
	inErrors, inHelp, asGiven, asOptional string
}

// formsOf returns the ways arg is written.
func formsOf(arg *argument) argumentForms {
	forms := argumentForms{inErrors: arg.String(), inHelp: arg.helpLabel(palette{}), asGiven: arg.requiredForm()}
	if arg.isPositional() {
		forms.asOptional = arg.optionalForm()
	}
	return forms
}

func TestArgumentsAreWrittenAsClapWroteThem(t *testing.T) {
	tests := []struct {
		name     string
		argument argument
		want     argumentForms
	}{
		{"option with a value", argument{long: "port", short: 'p', valueName: "PORT"},
			argumentForms{"--port <PORT>", "-p, --port <PORT>", "--port <PORT>", ""}},
		{"long option alone", argument{long: "number", valueName: "NUMBER"},
			argumentForms{"--number <NUMBER>", "    --number <NUMBER>", "--number <NUMBER>", ""}},
		{"short option alone", argument{short: 'x', valueName: "X"}, argumentForms{"-x <X>", "-x <X>", "-x <X>", ""}},
		{"option whose value must follow '='", argument{long: "simulate", valueName: "SIMULATE", missingValue: "iridium"},
			argumentForms{"--simulate[=<SIMULATE>]", "    --simulate[=<SIMULATE>]", "--simulate[=<SIMULATE>]", ""}},
		{"counted flag", argument{long: "verbose", short: 'v', action: actionCount},
			argumentForms{"--verbose...", "-v, --verbose...", "--verbose...", ""}},
		{"flag", argument{long: "help", short: 'h', action: actionHelp},
			argumentForms{"--help", "-h, --help", "--help", ""}},
		{"required positional", argument{valueName: "NUMBER", required: true},
			argumentForms{"<NUMBER>", "<NUMBER>", "<NUMBER>", "[NUMBER]"}},
		{"optional positional", argument{valueName: "FILTER"},
			argumentForms{"[FILTER]", "[FILTER]", "<FILTER>", "[FILTER]"}},
		{"positional taking the rest", argument{valueName: "COMMAND", action: actionAppend},
			argumentForms{"[COMMAND]...", "[COMMAND]...", "<COMMAND>...", "[COMMAND]..."}},
	}
	for _, test := range tests {
		if got := formsOf(&test.argument); got != test.want {
			t.Errorf("%s: written %+v, want %+v", test.name, got, test.want)
		}
	}
}

func TestOnlyOptionsThatSetOrAddTakeValues(t *testing.T) {
	takes := map[argumentAction]bool{actionSet: true, actionAppend: true, actionCount: false, actionHelp: false,
		actionVersion: false}
	for action, want := range takes {
		if got := (&argument{long: "x", action: action}).takesValue(); got != want {
			t.Errorf("action %d takes a value: %t, want %t", action, got, want)
		}
	}
}
