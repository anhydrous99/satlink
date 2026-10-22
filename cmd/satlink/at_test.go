package main

import (
	"strings"
	"testing"

	"github.com/anhydrous99/satlink/iridium"
)

func TestAResponseEndsWithItsFinalResult(t *testing.T) {
	tests := []struct {
		name     string
		response iridium.Response
		want     string
	}{
		{"OK", iridium.Response{Lines: []string{"+CSQ:4"}}, "+CSQ:4\nOK\n"},
		{"CONNECT and its rate", iridium.Response{Connected: true, ConnectMessage: "19200"}, "CONNECT 19200\n"},
		// The CONNECT line with no rate after it.
		{"a bare CONNECT", iridium.Response{Connected: true}, "CONNECT \n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout strings.Builder

			if err := printResponse(&stdout, test.response); err != nil || stdout.String() != test.want {
				t.Errorf("printResponse = %v, wrote %q; want %q", err, stdout.String(), test.want)
			}
		})
	}
}

func TestAResponseThatCannotBeWrittenFailsTheCommand(t *testing.T) {
	if err := printResponse(brokenWriter{}, iridium.Response{Lines: []string{"Iridium"}}); err == nil {
		t.Error("printResponse succeeded on a broken standard output")
	}
}
