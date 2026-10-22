package sim

import "testing"

// parseLine parses a whole command line, as the modem does.
func parseLine(line string) (command, bool) {
	body, ok := stripAttention(line)
	if !ok {
		return command{}, false
	}
	return parseCommand(body)
}

// parseCase is a command line and what it should parse to; a zero want
// with ok false means the handset does not know it.
type parseCase struct {
	line string
	want command
	ok   bool
}

// known returns a case that parses to c; its line comes from forLine.
func known(c command) parseCase {
	return parseCase{want: c, ok: true}
}

// assertParses checks each case's line against what it should parse to.
func assertParses(t *testing.T, cases []parseCase) {
	t.Helper()
	for _, test := range cases {
		got, ok := parseLine(test.line)
		if got != test.want || ok != test.ok {
			t.Errorf("parseLine(%q) = %+v, %v; want %+v, %v", test.line, got, ok, test.want, test.ok)
		}
	}
}

// forLine returns the case for the command line line.
func (c parseCase) forLine(line string) parseCase {
	c.line = line
	return c
}

// unknown returns the case of a line the handset does not know.
func unknown(line string) parseCase {
	return parseCase{line: line}
}

func TestOnlyLinesWithTheAttentionPrefixAreCommands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line string
		body string
		ok   bool
	}{
		{line: "\x1b"},
		{line: "hello"},
		{line: "at+csq ", body: "+CSQ", ok: true},
		{line: "\x1bAT", body: "", ok: true},
	}

	for _, test := range cases {
		if body, ok := stripAttention(test.line); body != test.body || ok != test.ok {
			t.Errorf("stripAttention(%q) = %q, %v; want %q, %v", test.line, body, ok, test.body, test.ok)
		}
	}
}

func TestOnlyASCIILettersChangeCase(t *testing.T) {
	t.Parallel()

	// Unicode would upper-case "ſ" (long s) to "S" and accept a register
	// setting the handset never saw.
	if got, ok := parseLine("ATſ12=50"); ok {
		t.Errorf("parseLine(\"ATſ12=50\") = %+v; want an unknown command", got)
	}
}

func TestBasicCommands(t *testing.T) {
	t.Parallel()
	ignored := known(settingCommand(setting{kind: settingIgnored}))
	assertParses(t, []parseCase{
		known(command{kind: commandAttention}).forLine("AT"),
		known(settingCommand(setting{kind: settingEcho, on: false})).forLine("ATE0"),
		known(settingCommand(setting{kind: settingEcho, on: true})).forLine("ate1"),
		known(settingCommand(setting{kind: settingReset})).forLine("ATZ"),
		ignored.forLine("ATV1"),
		ignored.forLine("AT&K0"),
		ignored.forLine("AT&C"),
		ignored.forLine("ATS12=50"),
		known(settingCommand(setting{kind: settingHangUpOnDTRDrop, on: true})).forLine("AT&D2"),
		known(settingCommand(setting{kind: settingHangUpOnDTRDrop, on: false})).forLine("AT&D0"),
		unknown("AT&D4"),
		known(callCommandOf(callCommand{kind: callHangUp})).forLine("ATH0"),
		known(callCommandOf(callCommand{kind: callReturnOnline})).forLine("ATO"),
		known(callCommandOf(callCommand{kind: callAnswer})).forLine("ATA"),
	})
}

func TestDiallingVoiceAndData(t *testing.T) {
	t.Parallel()
	dial := func(number string, kind callKind) parseCase {
		return known(callCommandOf(callCommand{kind: callDial, number: number, callKind: kind}))
	}
	assertParses(t, []parseCase{
		dial("+12025550100", callVoice).forLine("ATD+12025550100;"),
		dial("008816000025", callData).forLine("ATD008816000025"),
		dial("*#100", callData).forLine("ATDT*#100"),
		unknown("ATD"),
		unknown("ATD+"),
		unknown("ATD12a"),
	})
}

func TestExtendedCommands(t *testing.T) {
	t.Parallel()
	ignored := known(settingCommand(setting{kind: settingIgnored}))
	iridiumBearer := bearer{speed: 71, name: 0, connectionElement: 1}
	assertParses(t, []parseCase{
		known(command{kind: commandQuery, query: queryFastSignalQuality}).forLine("AT+CSQF"),
		known(command{kind: commandQuery, query: queryRegistration}).forLine("AT+CREG?"),
		ignored.forLine("AT+CMEE=2"),
		ignored.forLine("AT+CMGF=1"),
		// PDU mode is not simulated.
		unknown("AT+CMGF=0"),
		known(settingCommand(setting{kind: settingBearer, bearer: iridiumBearer})).forLine("AT+CBST=71,0,1"),
		unknown("AT+CBST=71,0"),
		unknown("AT+CBST=71,0,1,2"),
		unknown("AT+BOGUS"),
	})
}

func TestSMSCommands(t *testing.T) {
	t.Parallel()
	assertParses(t, []parseCase{
		known(smsCommandOf(smsCommand{kind: smsSend, number: "+881631234567"})).forLine(`AT+CMGS="+881631234567"`),
		unknown(`AT+CMGS=""`),
		known(smsCommandOf(smsCommand{kind: smsList, filter: smsFilterUnread})).forLine(`AT+CMGL="REC UNREAD"`),
		known(smsCommandOf(smsCommand{kind: smsList, filter: smsFilterAll})).forLine(`at+cmgl="all"`),
		unknown(`AT+CMGL="NEW"`),
		known(smsCommandOf(smsCommand{kind: smsRead, index: 3})).forLine("AT+CMGR=3"),
		unknown("AT+CMGD=x"),
		// Without a status, +CMGL lists the unread messages.
		known(smsCommandOf(smsCommand{kind: smsList, filter: smsFilterUnread})).forLine("AT+CMGL"),
		unknown(`AT+CMGS="+1`),
	})
}

func TestNumbersReadWithinTheirBitWidths(t *testing.T) {
	t.Parallel()
	assertParses(t, []parseCase{
		known(smsCommandOf(smsCommand{kind: smsDelete, index: 4})).forLine("AT+CMGD=+4"),
		unknown("AT+CMGD=-1"),
		unknown("AT+CMGD=4294967296"),
		known(settingCommand(setting{kind: settingBearer, bearer: bearer{speed: 71, name: 0, connectionElement: 1}})).
			forLine("AT+CBST= 71 , 0 ,1"),
		unknown("AT+CBST=300,0,1"),
	})
}

func TestTheBearerReadsBackAsTheHandsetPrintsIt(t *testing.T) {
	t.Parallel()

	if got := defaultBearer.String(); got != "7,0,1" {
		t.Errorf("defaultBearer = %q; want \"7,0,1\"", got)
	}
}
