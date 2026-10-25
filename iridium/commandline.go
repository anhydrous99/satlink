package iridium

import (
	"slices"
	"strings"

	"github.com/anhydrous99/satlink/internal/asciicase"
	"github.com/anhydrous99/satlink/internal/debugtext"
)

// Reading a raw command line as the handset reads it (V.250 §5.2-5.4, ISU
// AT Command Reference §3.5), to tell whether it can take the handset
// online.

// repeatLastCommand has the handset run its last command line again, which
// may have been a data dial. It needs no AT, and no CR (V.250).
const repeatLastCommand = "A/"

// extendedNames are the extended commands, prefix and name, that the ISU AT
// Command Reference (MAN0009 version 5) defines, and +CIMI, of 3GPP TS
// 27.007, which [Handset.IMSI] sends. Commands follow one another with no
// separator (§3.5), so the handset tells where a name ends by knowing them:
// a name is the longest of these that the text starts with, as +CSQF is,
// not +CSQ.
var extendedNames = []string{
	"+ADJANT", "+CAPBD", "+CAPBR", "+CAPBW", "+CAPPV", "+CAR", "+CBC", "+CBST",
	"+CBT", "+CCFC", "+CCLK", "+CCWA", "+CDSI", "+CEER", "+CGMI", "+CGMM",
	"+CGMR", "+CGSN", "+CHKIN", "+CHLD", "+CHUP", "+CICCID", "+CIER", "+CIMI",
	"+CKPD", "+CLCC", "+CLCK", "+CLIP", "+CLIR", "+CLPBK", "+CLVL", "+CMEE",
	"+CMGD", "+CMGF", "+CMGL", "+CMGR", "+CMGS", "+CMGW", "+CMOD", "+CMUT",
	"+CNMI", "+CNUM", "+COPS", "+CPAS", "+CPBF", "+CPBR", "+CPBS", "+CPBW",
	"+CPIN", "+CPMS", "+CPWD", "+CR", "+CRC", "+CREG", "+CRIS", "+CRISX",
	"+CSCA", "+CSCB", "+CSCS", "+CSDT", "+CSMS", "+CSQ", "+CSQF", "+CSSSC",
	"+CSTA", "+CULK", "+CVHU", "+CVMI", "+DEBUG", "+DPLCI", "+DR", "+DS",
	"+FEATURES", "+FWVER", "+GCAP", "+GDBCMB", "+GDBGSN", "+GDBMOD", "+GDBRD",
	"+GDBREF", "+GDBS", "+GDBSCLR", "+GDBSTAT", "+GDBSVC", "+GDBTMOUT", "+GDBV",
	"+GEMON", "+GMI", "+GMM", "+GMR", "+GPSPOS", "+GPSSTA", "+GPSUPD", "+GSN",
	"+HWVER", "+IPR", "+KEY", "+LBSECD", "+LBSECR", "+LBSEDM", "+LBSEMD",
	"+LBSEMR", "+LFWVER", "+MODE", "+PCDA", "+QGPS", "+REBOOT", "+SBDAREG",
	"+SBDC", "+SBDCC", "+SBDCP", "+SBDCR", "+SBDCW", "+SBDD", "+SBDDET",
	"+SBDDSC", "+SBDGW", "+SBDGWN", "+SBDI", "+SBDIX", "+SBDIXA", "+SBDLOE",
	"+SBDMTA", "+SBDRB", "+SBDREG", "+SBDRT", "+SBDS", "+SBDST", "+SBDSX",
	"+SBDTC", "+SBDWB", "+SBDWT", "+WANTST", "+WDLDM", "+WFRNG", "+WIRLP",
	"+WTM", "+XCSI",
	"-MSGEO", "-MSGEOS", "-MSSTM", "-MSVLS", "-MSVTR", "-MSVTS",
}

// prefixedBasicCommands are the basic commands named by a prefix and a
// letter that the ISU AT Command Reference (MAN0009 version 5) defines.
// After a prefix, any other character is read as a command: a '*' may be a
// DTMF tone or part of a shortcode, as in "AT-MSVTS=* D123", whose D
// dials.
var prefixedBasicCommands = []string{
	"%C", "%E", "%R",
	"&C", "&D", "&F", "&G", "&J", "&K", "&L", "&M", "&P", "&Q", "&R", "&S", "&V", "&W", "&X", "&Y",
	"*F", "*P", "*R",
	`\A`, `\B`, `\G`, `\J`, `\K`, `\N`,
}

// textToEndOfLine are the extended commands whose value runs to the end of
// the line, ';' and all, and holds text written without quotes: the text of
// +SBDWT, and the values of commands that, the reference says, do not
// support command concatenation on the handsets that have them: a name in
// UCS-2 hex, or ALL (+CAPBW, +CAPBD, on the 9555(A) and 9575), and an email
// address, or ALL (+LBSEMR, +LBSEMD, on the 9575).
var textToEndOfLine = []string{"+SBDWT", "+CAPBD", "+CAPBW", "+LBSEMD", "+LBSEMR"}

// hexDigitValues are the extended commands whose value is hexadecimal
// digits written without quotes, by how many: +CULK's unlock key, 16 of
// them (ISU AT Command Reference, +CULK). Read as commands, a key's A or D
// would refuse it.
var hexDigitValues = map[string]int{"+CULK": 16}

// hexDigits are the hexadecimal digits, in upper case.
const hexDigits = "0123456789ABCDEF"

// The characters that shape a command line.
const (
	// extendedPrefixes begin an extended-syntax command: V.250's '+', and
	// the '-' of Iridium's own, such as -MSSTM.
	extendedPrefixes = "+-"
	// basicPrefixes begin a basic command named by two characters, such as
	// &D2, %R or *F: the prefix, a letter, then perhaps a number.
	basicPrefixes = "&%*\\"
	// commandSeparator ends an extended-syntax command, and the dial string
	// of a voice call.
	commandSeparator = ';'
	// extendedCommandEnds end an extended-syntax command: Iridium's handsets
	// separate commands by a space as by a ';'.
	extendedCommandEnds = "; "
	// nameEnds end an extended-syntax command's name: what follows is its
	// value or a query.
	nameEnds = "=?"
	// valueStart begins an extended-syntax command's value.
	valueStart = '='
	// quote begins and ends a string constant.
	quote = '"'
)

// S-register commands (ISU AT Command Reference, S-Register Commands).
const (
	// registerCommand begins one: Sr, Sr? or Sr=n.
	registerCommand = 'S'
	// registerWrite writes a register: Sr=n, or =n, which writes the one a
	// command last referred to.
	registerWrite = '='
	// autoAnswerRegister, S0, set to n above 0, has the handset answer the
	// next call on its own, after n rings (ISU AT Command Reference, S0=n):
	// the CONNECT of a data call it answers is awaited by no command.
	autoAnswerRegister = 0
	// lineEndRegister, S3, holds the character that ends a command line,
	// and backspaceRegister, S5, the one that deletes the character before
	// it (V.250 §6.2.1, §6.2.3). Changing either changes how the handset
	// reads the next lines: one read here as a voice dial, "ATD123;", could
	// reach it as a data dial.
	lineEndRegister   = 3
	backspaceRegister = 5
	// numberCeiling is where reading a number stops counting: above every
	// register's number and value, which are at most 255.
	numberCeiling = 1000
)

// checkPrintable refuses a raw command that holds anything but printable
// ASCII, of which a command line is made (ISU AT Command Reference §3.5).
// The handset would run another line than the one mayGoOnline reads: it
// deletes the character before a backspace or a delete, and reads only the
// low seven bits of a byte.
func checkPrintable(command string) error {
	for i := range len(command) {
		if command[i] < ' ' || command[i] > '~' {
			return invalidArgument(debugtext.Quote(command) +
				" holds a character other than printable ASCII, which can change the command the handset runs")
		}
	}
	return nil
}

// mayGoOnline reports whether command, printable ASCII, can take the
// handset online: whether, read as the handset reads a line, it holds a
// data dial (D whose dial string does not end in ';'), A (answer) or O
// (return online) anywhere among its commands, as "ATD123", "ATE0D123",
// "ATA0", "AT+CSQ D123" and "AT+CSQD123" do, or A/, which may repeat a data
// dial. So does a write that has the handset answer calls on its own (S0=n,
// n above 0), or that changes how it reads the lines after (S3=n, S5=n),
// and so does =n, which writes the register last referred to, whichever it
// is.
//
// The handset reads a line from its first AT, in either case: whatever
// comes before counts for nothing. A prefix that mixes the cases, such as
// "At", may be no prefix to the handset (§3.5), so the line is read from
// the next AT as well. Spaces count for nothing in basic commands and dial
// strings. A dial string ends at its first ';', and counts as a data dial
// when that ';' is inside quotes. Commands follow one another after a
// space, a ';' or nothing at all (§3.5): an extended command's name is the
// longest of extendedNames that the text starts with, and its value or
// query ends at a space or a ';', or before a letter, a prefix or the '='
// of =n, all outside strings.
// So a string written without quotes reads as commands, except where
// textToEndOfLine says that a value runs to the end of the line, or
// hexDigitValues that it begins with so many hexadecimal digits. A
// character the reading cannot place counts as a basic command of its own,
// so that no letter after it goes unread: a prefix whose next letter names
// none of prefixedBasicCommands with it, and the characters of an extended
// name the reference does not define, too, since where such a name ends is
// unknown.
func mayGoOnline(command string) bool {
	line := asciicase.Upper(command)
	beforeAttention, commands, found := strings.Cut(line, attentionCommand)
	switch {
	case strings.Contains(beforeAttention, repeatLastCommand):
		return true
	case !found:
		// The handset ignores a line without AT.
		return false
	case mixesCase(command[len(beforeAttention):][:len(attentionCommand)]) &&
		mayGoOnline(command[len(beforeAttention)+len(attentionCommand):]):
		return true
	}
	for commands != "" {
		var online bool
		if online, commands = readCommand(commands); online {
			return true
		}
	}
	return false
}

// mixesCase reports whether prefix, an AT in some case, is neither "AT" nor
// "at".
func mixesCase(prefix string) bool {
	return prefix != asciicase.Upper(prefix) && prefix != asciicase.Lower(prefix)
}

// readCommand reads the first of commands, a command line's body after AT,
// and reports whether it can take the handset online, and what follows it.
func readCommand(commands string) (online bool, rest string) {
	name, rest := commands[0], commands[1:]
	switch {
	case name == 'A' || name == 'O':
		// Whatever follows A is ignored (V.250 §6.3.5).
		return true, ""
	case name == 'D':
		// A dial string ends at its first ';' (V.250 §6.3.1). A handset that
		// reads strings in it may read on past one inside quotes, so only a
		// ';' outside them makes the dial a voice call for every reading.
		dialString, rest, isVoiceCall := strings.Cut(rest, string(commandSeparator))
		return !isVoiceCall || strings.Count(dialString, string(quote))%2 == 1, rest
	case name == registerCommand:
		return readRegisterCommand(rest)
	case name == registerWrite:
		// The register last referred to may be S0, S3 or S5.
		return true, rest
	case strings.IndexByte(extendedPrefixes, name) >= 0:
		return false, skipExtendedCommand(commands)
	case strings.IndexByte(basicPrefixes, name) >= 0:
		// The letter that completes the name, such as the D of &D2.
		rest = strings.TrimLeft(rest, " ")
		if rest == "" || !slices.Contains(prefixedBasicCommands, string(name)+rest[:1]) {
			return false, rest
		}
		return false, rest[1:]
	default:
		return false, rest
	}
}

// readRegisterCommand reads an S-register command after its S, and reports
// whether it can take the handset online — a write of n above 0 to S0, or
// of anything to S3 or S5 — and what follows it.
func readRegisterCommand(command string) (online bool, rest string) {
	// A number ends at a space: should the handset read on past it, the =n
	// that follows is refused all the same.
	register, rest := cutNumber(command, false)
	if rest == "" || rest[0] != registerWrite {
		return false, rest
	}
	// Read on past spaces: a handset that drops them reads "S0=0 1" as
	// S0=01, and auto-answers.
	value, rest := cutNumber(rest[1:], true)
	switch register {
	case autoAnswerRegister:
		return value != 0, rest
	case lineEndRegister, backspaceRegister:
		return true, rest
	default:
		return false, rest
	}
}

// cutNumber reads the decimal number that s begins with, after any spaces,
// and returns it, at most numberCeiling, and what follows it, after any
// spaces. A number left out is 0, as the handset takes it (§3.5). It ends
// at a space unless acrossSpaces says to read on past the spaces between
// its digits.
func cutNumber(s string, acrossSpaces bool) (number int, rest string) {
	s = strings.TrimLeft(s, " ")
	end := 0
	for ; end < len(s) && (isDigit(s[end]) || acrossSpaces && s[end] == ' '); end++ {
		if s[end] != ' ' {
			number = min(number*10+int(s[end]-'0'), numberCeiling)
		}
	}
	return number, strings.TrimLeft(s[end:], " ")
}

// isDigit reports whether c is a decimal digit.
func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

// skipExtendedCommand returns what follows the extended command that
// command, from its prefix, begins with. Its name is the longest of
// extendedNames that command starts with. A value or query after it ends
// at a space or a ';', or before the start of another command, all outside
// strings; one of textToEndOfLine's values runs to the end of the line, and
// one of hexDigitValues' begins with digits that are no commands. A name
// that starts with none of them leaves all but its prefix to be read as
// commands.
func skipExtendedCommand(command string) string {
	name := extendedName(command)
	if name == "" {
		return command[1:]
	}
	rest := command[len(name):]
	switch {
	case rest == "" || strings.IndexByte(nameEnds, rest[0]) < 0:
		return rest
	case rest[0] != valueStart:
		return skipParameters(rest[1:])
	case slices.Contains(textToEndOfLine, name):
		return ""
	default:
		return skipParameters(skipHexDigits(rest[1:], hexDigitValues[name]))
	}
}

// skipHexDigits returns what follows the count hexadecimal digits that s
// begins with, or s if it does not begin with that many.
func skipHexDigits(s string, count int) string {
	if len(s) < count || strings.Trim(s[:count], hexDigits) != "" {
		return s
	}
	return s[count:]
}

// extendedName returns the longest of extendedNames that command starts
// with, or "" if it starts with none.
func extendedName(command string) string {
	longest := ""
	for _, name := range extendedNames {
		if len(name) > len(longest) && strings.HasPrefix(command, name) {
			longest = name
		}
	}
	return longest
}

// skipParameters returns what follows an extended command's value or query,
// which parameters, past its '=' or '?', begins with: it ends at a space or
// a ';', or before the start of another command, all outside strings.
func skipParameters(parameters string) string {
	quoted := false
	for i := range len(parameters) {
		c := parameters[i]
		switch {
		case c == quote:
			quoted = !quoted
		case quoted:
		case strings.IndexByte(extendedCommandEnds, c) >= 0:
			return parameters[i+1:]
		case startsCommand(c):
			return parameters[i:]
		}
	}
	return ""
}

// startsCommand reports whether c, upper case, can begin a command: a
// letter, a prefix, or the '=' of =n, which writes the register last
// referred to.
func startsCommand(c byte) bool {
	return 'A' <= c && c <= 'Z' || c == registerWrite || strings.IndexByte(extendedPrefixes+basicPrefixes, c) >= 0
}
