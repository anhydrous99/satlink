package main

import (
	"strings"
	"testing"

	"github.com/anhydrous99/satlink/iridium"
)

// A message whose every field carries control characters, as a sender's
// name may (GSM 7-bit has carriage return and line feed) and as UCS-2 and
// 8-bit text may. The text keeps its lines; a header field keeps its line.
var messageWithControls = iridium.SMSMessage{
	Index:     1,
	Status:    "REC UNREAD\x1b[8m",
	Address:   "\x1b[2K\rMy\nBank",
	Timestamp: "26/10/22,10:00:00+00\a",
	Text:      "Pay\rnow\x1b]52;c;cm0gLXJmIH4K\a\n\u009b2Jhere",
}

func TestAListedMessageShowsWithoutItsControlCharacters(t *testing.T) {
	var stdout strings.Builder

	if err := printMessages(&stdout, []iridium.SMSMessage{messageWithControls}); err != nil {
		t.Fatal(err)
	}

	const want = "[1] REC UNREAD My Bank 26/10/22,10:00:00+00\nPaynow\n2Jhere\n\n"
	if stdout.String() != want {
		t.Errorf("printed %q, want %q", stdout.String(), want)
	}
}

func TestAMessageReadShowsWithoutItsControlCharacters(t *testing.T) {
	var stdout strings.Builder

	if err := printMessage(&stdout, messageWithControls); err != nil {
		t.Fatal(err)
	}

	const want = "From: My Bank\nTime: 26/10/22,10:00:00+00\nStatus: REC UNREAD\n\nPaynow\n2Jhere\n"
	if stdout.String() != want {
		t.Errorf("printed %q, want %q", stdout.String(), want)
	}
}
