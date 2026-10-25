package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// Golden files: what a command line left, kept in testdata and compared
// with what it leaves now.

var update = flag.Bool("update", false, "rewrite the golden files in testdata with the commands' output")

// variableOutput is output that differs from run to run, and what it is
// replaced by in golden files: the time an SMS arrived, how long a call
// lasted, and how many bytes PPP's random magic numbers made it carry.
var variableOutput = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`\d\d/\d\d/\d\d,\d\d:\d\d:\d\d[+-]\d\d`), "<time>"},
	{regexp.MustCompile(`lasted \d+ s`), "lasted <n> s"},
	{regexp.MustCompile(`\d+ bytes sent, \d+ received`), "<n> bytes sent, <n> received"},
}

// goldenText is what a golden file holds: the command line, then the exit
// status and output it left, with variableOutput replaced.
func goldenText(args []string, got outcome) string {
	commandLine := strings.Join(append([]string{"$", programName}, shellWords(args)...), " ")
	text := fmt.Sprintf("%s\nexit status %d\n--- stdout\n%s--- stderr\n%s",
		commandLine, got.status, withFinalNewline(got.stdout), withFinalNewline(got.stderr))
	for _, variable := range variableOutput {
		text = variable.pattern.ReplaceAllString(text, variable.replacement)
	}
	return text
}

// shellSpecial are the characters a shell takes for more than themselves.
const shellSpecial = " '\"*?;&|<>()$`\\"

// shellWords quotes args as a shell would take them back: in single quotes
// when they hold a character a shell takes for more than itself, or a
// control character.
func shellWords(args []string) []string {
	words := make([]string, len(args))
	for i, arg := range args {
		words[i] = arg
		if arg == "" || strings.ContainsAny(arg, shellSpecial) || strings.ContainsFunc(arg, unicode.IsControl) {
			words[i] = "'" + arg + "'"
		}
	}
	return words
}

// withFinalNewline ends output with a newline, saying so when it had none.
func withFinalNewline(output string) string {
	if output == "" || strings.HasSuffix(output, "\n") {
		return output
	}
	return output + "\n[no newline at the end]\n"
}

// checkGolden compares what running args left with testdata/name.golden,
// or with -update writes that file.
func checkGolden(t *testing.T, name string, args []string, got outcome) {
	t.Helper()
	text := goldenText(args, got)
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; go test -update writes it", err)
	}
	if text != string(want) {
		t.Errorf("the command line left\n%s\nwhere %s holds\n%s", text, path, want)
	}
}
