package main

import (
	"strings"
	"testing"

	"github.com/anhydrous99/satlink/iridium"
)

func TestAResponseEndsWithItsFinalResult(t *testing.T) {
	var stdout strings.Builder

	err := printResponse(&stdout, iridium.Response{Lines: []string{"+CSQ:4"}})

	if want := "+CSQ:4\nOK\n"; err != nil || stdout.String() != want {
		t.Errorf("printResponse = %v, wrote %q; want %q", err, stdout.String(), want)
	}
}

func TestAResponseThatCannotBeWrittenFailsTheCommand(t *testing.T) {
	if err := printResponse(brokenWriter{}, iridium.Response{Lines: []string{"Iridium"}}); err == nil {
		t.Error("printResponse succeeded on a broken standard output")
	}
}
