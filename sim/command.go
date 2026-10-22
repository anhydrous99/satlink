package sim

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anhydrous99/satlink/internal/asciicase"
)

// Turns a command line into one of the commands the simulated handset
// knows.
//
// The handset takes one command per line. Names and parameters are matched
// without regard to case, as on real handsets.

const (
	// attention is what every command line addressed to the handset starts
	// with.
	attention = "AT"
	// textMode is the AT+CMGF value of text mode, the only SMS format
	// simulated.
	textMode = "1"
	// bearerFields is how many fields AT+CBST takes: speed, name and
	// connection element.
	bearerFields = 3
)

// Bit sizes of the numbers commands carry.
const (
	bearerFieldBits = 8
	smsIndexBits    = 32
)

// commandKind says what a [command] is.
type commandKind int

const (
	// commandAttention is plain AT: is the handset there?
	commandAttention commandKind = iota
	// commandSetting changes how the handset behaves.
	commandSetting
	// commandQuery reports something.
	commandQuery
	// commandSMS is a text-mode SMS command.
	commandSMS
	// commandCall places, ends or resumes a call.
	commandCall
)

// command is a command the handset understands. It is comparable with ==.
type command struct {
	kind    commandKind
	setting setting     // commandSetting
	query   query       // commandQuery
	sms     smsCommand  // commandSMS
	call    callCommand // commandCall
}

// settingKind says what a [setting] changes.
type settingKind int

const (
	// settingIgnored is accepted for compatibility but not simulated: ATV1,
	// AT+CMEE, AT+CMGF=1, AT&C, AT&K, ATS<n>=<v> and AT+CR.
	settingIgnored settingKind = iota
	// settingReset is ATZ: back to the default settings.
	settingReset
	// settingEcho is ATE0 and ATE1: whether typed commands are echoed.
	settingEcho
	// settingBearer is AT+CBST=<speed>,<name>,<ce>.
	settingBearer
	// settingHangUpOnDTRDrop is AT&D<n>: whether a call ends when DTR
	// drops, as it does when the app closes the port. &D2 and &D3 end it;
	// &D0 and &D1 leave it up.
	settingHangUpOnDTRDrop
)

// setting is a command that changes how the handset behaves.
type setting struct {
	kind settingKind
	// on is the new state (settingEcho, settingHangUpOnDTRDrop).
	on bool
	// bearer is the new bearer service (settingBearer).
	bearer bearer
}

// query is a command that reports something.
type query int

const (
	queryBearer            query = iota // AT+CBST?
	queryManufacturer                   // AT+CGMI
	queryModel                          // AT+CGMM
	queryRevision                       // AT+CGMR
	queryIMEI                           // AT+CGSN
	queryIMSI                           // AT+CIMI
	querySignalQuality                  // AT+CSQ
	queryFastSignalQuality              // AT+CSQF, the instant variant of AT+CSQ
	queryBattery                        // AT+CBC
	queryRegistration                   // AT+CREG?
	queryClock                          // AT+CCLK?
	queryCallRelease                    // AT+CEER: why the last call ended
	queryActivityStatus                 // AT+CPAS: whether a data call is up
)

// extendedQueries are the queries by their name after the +.
var extendedQueries = map[string]query{
	"CBST?": queryBearer,
	"CGMI":  queryManufacturer,
	"CGMM":  queryModel,
	"CGMR":  queryRevision,
	"CGSN":  queryIMEI,
	"CIMI":  queryIMSI,
	"CSQ":   querySignalQuality,
	"CSQF":  queryFastSignalQuality,
	"CBC":   queryBattery,
	"CREG?": queryRegistration,
	"CCLK?": queryClock,
	"CEER":  queryCallRelease,
	"CPAS":  queryActivityStatus,
}

// smsCommandKind says what an [smsCommand] does.
type smsCommandKind int

const (
	// smsSend is AT+CMGS="<number>", followed by the text after a "> "
	// prompt.
	smsSend smsCommandKind = iota
	// smsList is AT+CMGL[="<status>"].
	smsList
	// smsRead is AT+CMGR=<index>.
	smsRead
	// smsDelete is AT+CMGD=<index>.
	smsDelete
)

// smsCommand is a text-mode SMS command (3GPP TS 27.005).
type smsCommand struct {
	kind   smsCommandKind
	number string    // smsSend
	filter smsFilter // smsList
	index  uint32    // smsRead, smsDelete
}

// callCommandKind says what a [callCommand] does.
type callCommandKind int

const (
	// callDial is ATD<number>: with a trailing semicolon a voice call,
	// without one a data call.
	callDial callCommandKind = iota
	// callAnswer is ATA.
	callAnswer
	// callHangUp is ATH, ATH0.
	callHangUp
	// callReturnOnline is ATO, ATO0: back to online-data mode.
	callReturnOnline
)

// callKind is what ATD places: a semicolon after the number asks for
// voice.
type callKind int

const (
	callVoice callKind = iota
	callData
)

// callCommand is a command that places, ends or resumes a call.
type callCommand struct {
	kind     callCommandKind
	number   string   // callDial
	callKind callKind // callDial
}

// bearer is an AT+CBST bearer service: data rate, modem type and
// connection element (3GPP TS 27.007 §6.7).
type bearer struct {
	speed             uint8
	name              uint8
	connectionElement uint8
}

// defaultBearer is 9600 bit/s V.32, asynchronous, non-transparent.
var defaultBearer = bearer{speed: 7, name: 0, connectionElement: 1}

// String returns the bearer as AT+CBST? reports it.
func (b bearer) String() string {
	return fmt.Sprintf("%d,%d,%d", b.speed, b.name, b.connectionElement)
}

// stripAttention returns what follows the AT prefix of line, in upper case
// and trimmed.
//
// It returns false for a line without the prefix: such a line is not meant
// for the handset (line noise, a stray ESC) and is ignored, as on real
// handsets. Anything before the prefix is ignored too.
func stripAttention(line string) (string, bool) {
	upper := asciicase.Upper(line)
	start := strings.Index(upper, attention)
	if start < 0 {
		return "", false
	}
	return strings.TrimSpace(upper[start+len(attention):]), true
}

// parseCommand returns the command body names, where body is what
// stripAttention returned, and false when the handset does not know it.
func parseCommand(body string) (command, bool) {
	if extended, ok := strings.CutPrefix(body, "+"); ok {
		return parseExtended(extended)
	}
	return parseBasic(body)
}

// basicCommands are the V.250 basic commands without parameters, and the
// fixed forms of those with one.
var basicCommands = map[string]command{
	"":    {kind: commandAttention},
	"Z":   settingCommand(setting{kind: settingReset}),
	"Z0":  settingCommand(setting{kind: settingReset}),
	"E":   settingCommand(setting{kind: settingEcho, on: false}),
	"E0":  settingCommand(setting{kind: settingEcho, on: false}),
	"E1":  settingCommand(setting{kind: settingEcho, on: true}),
	"V1":  settingCommand(setting{kind: settingIgnored}),
	"A":   callCommandOf(callCommand{kind: callAnswer}),
	"H":   callCommandOf(callCommand{kind: callHangUp}),
	"H0":  callCommandOf(callCommand{kind: callHangUp}),
	"O":   callCommandOf(callCommand{kind: callReturnOnline}),
	"O0":  callCommandOf(callCommand{kind: callReturnOnline}),
	"&D":  settingCommand(setting{kind: settingHangUpOnDTRDrop, on: false}),
	"&D0": settingCommand(setting{kind: settingHangUpOnDTRDrop, on: false}),
	"&D1": settingCommand(setting{kind: settingHangUpOnDTRDrop, on: false}),
	"&D2": settingCommand(setting{kind: settingHangUpOnDTRDrop, on: true}),
	"&D3": settingCommand(setting{kind: settingHangUpOnDTRDrop, on: true}),
}

// parseBasic parses a V.250 basic command: a letter, sometimes & and a
// letter, and a value.
func parseBasic(body string) (command, bool) {
	if known, ok := basicCommands[body]; ok {
		return known, true
	}
	if dial, ok := parseDial(body); ok {
		return dial, true
	}
	if isIgnoredBasicSetting(body) {
		return settingCommand(setting{kind: settingIgnored}), true
	}
	return command{}, false
}

func parseDial(body string) (command, bool) {
	dialString, ok := strings.CutPrefix(body, "D")
	if !ok {
		return command{}, false
	}
	number, voice := strings.CutSuffix(dialString, ";")
	kind := callData
	if voice {
		kind = callVoice
	}
	// T asks for tone dialling, which is what the handset does anyway.
	number = strings.TrimPrefix(number, "T")
	if !isDialable(number) {
		return command{}, false
	}
	return callCommandOf(callCommand{kind: callDial, number: number, callKind: kind}), true
}

// isIgnoredBasicSetting reports whether body is &C or &K, with an optional
// number, or S<n>=<v>, which sets a register.
func isIgnoredBasicSetting(body string) bool {
	if ampersand, ok := strings.CutPrefix(body, "&"); ok {
		value, named := cutAnyPrefix(ampersand, "C", "K")
		return named && (value == "" || isNumber(value))
	}
	register, ok := strings.CutPrefix(body, "S")
	if !ok {
		return false
	}
	number, value, assigned := strings.Cut(register, "=")
	return assigned && isNumber(number) && isNumber(value)
}

// parseExtended parses a 3GPP extended command, after its +.
func parseExtended(text string) (command, bool) {
	if name, value, assigned := strings.Cut(text, "="); assigned {
		return parseExtendedAssignment(name, value)
	}
	if text == "CMGL" {
		// Without a status, +CMGL lists the unread messages (TS 27.005
		// §3.4.2).
		return smsCommandOf(smsCommand{kind: smsList, filter: smsFilterUnread}), true
	}
	known, ok := extendedQueries[text]
	if !ok {
		return command{}, false
	}
	return command{kind: commandQuery, query: known}, true
}

func parseExtendedAssignment(name, value string) (command, bool) {
	ignored := settingCommand(setting{kind: settingIgnored})
	switch name {
	case "CMEE", "CR":
		return acceptedIf(ignored, isNumber(value))
	case "CMGF":
		return acceptedIf(ignored, value == textMode)
	case "CBST":
		service, ok := parseBearer(value)
		return acceptedIf(settingCommand(setting{kind: settingBearer, bearer: service}), ok)
	case "CMGS":
		number := unquote(value)
		return acceptedIf(smsCommandOf(smsCommand{kind: smsSend, number: number}), isDialable(number))
	case "CMGL":
		filter, ok := parseSMSFilter(unquote(value))
		return acceptedIf(smsCommandOf(smsCommand{kind: smsList, filter: filter}), ok)
	case "CMGR":
		return parseSMSIndexCommand(smsRead, value)
	case "CMGD":
		return parseSMSIndexCommand(smsDelete, value)
	default:
		return command{}, false
	}
}

// parseSMSIndexCommand parses the index of AT+CMGR=<index> or
// AT+CMGD=<index>.
func parseSMSIndexCommand(kind smsCommandKind, value string) (command, bool) {
	index, ok := parseUnsigned(value, smsIndexBits)
	return acceptedIf(smsCommandOf(smsCommand{kind: kind, index: uint32(index)}), ok)
}

func parseBearer(value string) (bearer, bool) {
	fields := strings.Split(value, ",")
	if len(fields) != bearerFields {
		return bearer{}, false
	}
	var parsed [bearerFields]uint8
	for i, field := range fields {
		number, ok := parseUnsigned(strings.TrimSpace(field), bearerFieldBits)
		if !ok {
			return bearer{}, false
		}
		parsed[i] = uint8(number)
	}
	return bearer{speed: parsed[0], name: parsed[1], connectionElement: parsed[2]}, true
}

// parseUnsigned parses a decimal number of at most bits bits: digits after
// an optional '+', nothing else.
func parseUnsigned(text string, bits int) (uint64, bool) {
	number, err := strconv.ParseUint(strings.TrimPrefix(text, "+"), 10, bits)
	return number, err == nil
}

// unquote returns value without the double quotes around it, if it has
// both.
func unquote(value string) string {
	if inner, ok := strings.CutPrefix(value, `"`); ok {
		if unquoted, ok := strings.CutSuffix(inner, `"`); ok {
			return unquoted
		}
	}
	return value
}

// cutAnyPrefix returns text without the first of prefixes it starts with,
// and whether there was one.
func cutAnyPrefix(text string, prefixes ...string) (string, bool) {
	for _, prefix := range prefixes {
		if rest, ok := strings.CutPrefix(text, prefix); ok {
			return rest, true
		}
	}
	return text, false
}

func isNumber(text string) bool {
	return text != "" && strings.Trim(text, "0123456789") == ""
}

// isDialable reports whether number is digits, '*' and '#', after an
// optional international '+'.
func isDialable(number string) bool {
	digits := strings.TrimPrefix(number, "+")
	return digits != "" && strings.Trim(digits, "0123456789*#") == ""
}

// acceptedIf returns parsed when ok, and no command otherwise.
func acceptedIf(parsed command, ok bool) (command, bool) {
	if !ok {
		return command{}, false
	}
	return parsed, true
}

func settingCommand(s setting) command {
	return command{kind: commandSetting, setting: s}
}

func smsCommandOf(s smsCommand) command {
	return command{kind: commandSMS, sms: s}
}

func callCommandOf(c callCommand) command {
	return command{kind: commandCall, call: c}
}
