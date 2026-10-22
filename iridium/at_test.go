package iridium

import (
	"errors"
	"slices"
	"testing"
)

func TestClassifiesFinalResults(t *testing.T) {
	tests := []struct {
		line    string
		want    FinalResult
		isFinal bool
	}{
		{"OK", FinalResult{Kind: FinalResultOK}, true},
		{"+CME ERROR: 10", FinalResult{Kind: FinalResultCME, Code: "10"}, true},
		{"CONNECT 2400", FinalResult{Kind: FinalResultConnect, ConnectMessage: "2400"}, true},
		{"+CSQ:5", FinalResult{}, false},
		// The remaining codes.
		{"ERROR", FinalResult{Kind: FinalResultError}, true},
		{"+CMS ERROR: 321", FinalResult{Kind: FinalResultCMS, Code: "321"}, true},
		{"CONNECT", FinalResult{Kind: FinalResultConnect}, true},
		{"NO CARRIER", FinalResult{Kind: FinalResultNoCarrier}, true},
		{"BUSY", FinalResult{Kind: FinalResultBusy}, true},
		{"NO ANSWER", FinalResult{Kind: FinalResultNoAnswer}, true},
		{"NO DIALTONE", FinalResult{Kind: FinalResultNoDialtone}, true},
		{"NO DIAL TONE", FinalResult{Kind: FinalResultNoDialtone}, true},
		{"CONNECTED", FinalResult{}, false},
	}
	for _, test := range tests {
		got, isFinal := classifyFinalResult(test.line)

		if got != test.want || isFinal != test.isFinal {
			t.Errorf("classifyFinalResult(%q) = %+v, %v; want %+v, %v", test.line, got, isFinal, test.want, test.isFinal)
		}
	}
}

func TestResponsePrefixes(t *testing.T) {
	tests := map[string]string{
		"AT+CSQ":       "+CSQ",
		"at+creg?":     "+CREG",
		`AT+CMGS="+1"`: "+CMGS",
		"ATE0":         "",
		// Too short to be a command.
		"A": "",
	}
	for command, want := range tests {
		if got := responsePrefix(command); got != want {
			t.Errorf("responsePrefix(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestURCDetectionExcludesOwnResponse(t *testing.T) {
	tests := []struct {
		line, ownPrefix string
		want            bool
	}{
		{`+CMTI: "SM",3`, "+CSQ", true},
		{"RING", "", true},
		{"+CREG: 0,1", "+CREG", false},
		{"+CMGS: 4", "+CMGS", false},
		// +CMT must not swallow +CMTI's own response or similar prefixes.
		{"+CMTX: 1", "", false},
	}
	for _, test := range tests {
		if got := isURC(test.line, test.ownPrefix); got != test.want {
			t.Errorf("isURC(%q, %q) = %v, want %v", test.line, test.ownPrefix, got, test.want)
		}
	}
}

func TestLineBufferAndPrompt(t *testing.T) {
	var buffer lineBuffer

	buffer.push([]byte("\r\n+CSQ:4\r\n\r\nOK\r"))
	assertNextLine(t, &buffer, "+CSQ:4")
	assertNoLine(t, &buffer)
	buffer.push([]byte("\n"))
	assertNextLine(t, &buffer, "OK")
	buffer.push([]byte("\r\n> "))
	assertNoLine(t, &buffer)

	if !buffer.takePrompt() {
		t.Error("the prompt was not taken")
	}
	if buffer.takePrompt() {
		t.Error("a second prompt was taken")
	}
}

func TestLineBufferMakesInvalidUTF8Valid(t *testing.T) {
	var buffer lineBuffer

	buffer.push([]byte("caf\xe9\x00\r\n"))

	assertNextLine(t, &buffer, "caf�")
}

func TestLineBufferReplacesEachIllFormedSubpart(t *testing.T) {
	var buffer lineBuffer

	// Two Latin-1 letters are two ill-formed subparts, each replaced by its
	// own U+FFFD; strings.ToValidUTF8 would replace them with one.
	buffer.push([]byte("caf\xe9\xe9 \xe2\x82!\r\n"))

	assertNextLine(t, &buffer, "caf\uFFFD\uFFFD \uFFFD!")
}

func TestLineBufferHandsOverWhatIsNotALine(t *testing.T) {
	var buffer lineBuffer
	buffer.push([]byte("CONNECT\r\n\x7e\xff"))
	assertNextLine(t, &buffer, "CONNECT")

	remaining := buffer.takeRemaining()

	if string(remaining) != "\x7e\xff" {
		t.Errorf("takeRemaining = %q", remaining)
	}
	assertNoLine(t, &buffer)
}

func TestCSVSplit(t *testing.T) {
	fields := SplitCSV(`1,"REC UNREAD","+881631234567",,"24/10/18,12:00:00+00"`)

	want := []string{"1", "REC UNREAD", "+881631234567", "", "24/10/18,12:00:00+00"}
	if !slices.Equal(fields, want) {
		t.Errorf("SplitCSV = %q, want %q", fields, want)
	}
}

func TestParseNumberAcceptsPlusSignAndChecksWidth(t *testing.T) {
	tests := []struct {
		field  string
		width  fieldWidth
		want   int
		parses bool
	}{
		{"4", byteField, 4, true},
		{"080", byteField, 80, true},
		{"+4", byteField, 4, true},
		{"-1", byteField, 0, false},
		{"300", byteField, 0, false},
		{"300", wordField, 300, true},
		{"4294967296", wordField, 0, false},
		{"++4", byteField, 0, false},
		{"", byteField, 0, false},
	}
	for _, test := range tests {
		got, err := parseNumber([]string{test.field}, 0, "+CSQ", test.width)

		if got != test.want || (err == nil) != test.parses {
			t.Errorf("parseNumber(%q, %d bits) = %d, %v; want %d, parses %v", test.field, test.width, got, err, test.want, test.parses)
		}
	}
}

func TestAMissingNumericFieldIsAParseError(t *testing.T) {
	_, err := parseNumber([]string{"1"}, 1, "+CBC", byteField)

	var parseErr *Error
	if !errors.As(err, &parseErr) || parseErr.Kind != ErrParse || parseErr.Message != `+CBC: bad field 1 in ["1"]` {
		t.Errorf("parseNumber = %v, want ErrParse naming the field", err)
	}
}

func TestDebugListQuotesEachItem(t *testing.T) {
	tests := []struct {
		items []string
		want  string
	}{
		{nil, "[]"},
		{[]string{"a"}, `["a"]`},
		{[]string{"a", `say "hi"`}, `["a", "say \"hi\""]`},
		// Control characters are escaped as \u{b} and \0, not as \v and \x00.
		{[]string{"a\vb", "\x00"}, `["a\u{b}b", "\0"]`},
	}
	for _, test := range tests {
		if got := debugList(test.items); got != test.want {
			t.Errorf("debugList(%q) = %s, want %s", test.items, got, test.want)
		}
	}
}

func TestFinalResultKindNames(t *testing.T) {
	names := map[FinalResultKind]string{
		FinalResultOK:         "OK",
		FinalResultError:      "Error",
		FinalResultCME:        "CME",
		FinalResultCMS:        "CMS",
		FinalResultConnect:    "Connect",
		FinalResultNoCarrier:  "NoCarrier",
		FinalResultBusy:       "Busy",
		FinalResultNoAnswer:   "NoAnswer",
		FinalResultNoDialtone: "NoDialtone",
		FinalResultKind(42):   "FinalResultKind(42)",
	}
	for kind, want := range names {
		if got := kind.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

// assertNextLine fails the test unless buffer's next line is want.
func assertNextLine(t *testing.T, buffer *lineBuffer, want string) {
	t.Helper()
	if line, ok := buffer.nextLine(); !ok || line != want {
		t.Errorf("nextLine = %q, %v; want %q", line, ok, want)
	}
}

// assertNoLine fails the test if buffer holds a complete line.
func assertNoLine(t *testing.T, buffer *lineBuffer) {
	t.Helper()
	if line, ok := buffer.nextLine(); ok {
		t.Errorf("nextLine = %q, want none", line)
	}
}
