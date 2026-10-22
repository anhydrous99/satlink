package satlink

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/anhydrous99/satlink/internal/asciicase"
	"github.com/anhydrous99/satlink/internal/rustdebug"
	"github.com/anhydrous99/satlink/internal/utf8lossy"
)

// Low-level AT framing: line splitting, final result codes and unsolicited
// result codes. Only the pieces callers can use are exported; the line
// buffer and the URC rules are internal.

// FinalResult is a final result code that terminates an AT command.
type FinalResult struct {
	// Kind says which result code it is.
	Kind FinalResultKind
	// Code is the code after "+CME ERROR:" or "+CMS ERROR:", trimmed.
	Code string
	// ConnectMessage is the text after CONNECT, usually the data rate
	// (data calls); empty when CONNECT stood alone.
	ConnectMessage string
}

// FinalResultKind says which final result code a [FinalResult] is.
type FinalResultKind int

// The final result codes that end a command.
const (
	// FinalResultOK is "OK".
	FinalResultOK FinalResultKind = iota
	// FinalResultError is "ERROR".
	FinalResultError
	// FinalResultCME is "+CME ERROR: <code>".
	FinalResultCME
	// FinalResultCMS is "+CMS ERROR: <code>".
	FinalResultCMS
	// FinalResultConnect is "CONNECT", optionally followed by the rate.
	FinalResultConnect
	// FinalResultNoCarrier is "NO CARRIER".
	FinalResultNoCarrier
	// FinalResultBusy is "BUSY".
	FinalResultBusy
	// FinalResultNoAnswer is "NO ANSWER".
	FinalResultNoAnswer
	// FinalResultNoDialtone is "NO DIALTONE", or "NO DIAL TONE".
	FinalResultNoDialtone
)

// String returns the kind's name without its prefix, such as "NoCarrier".
func (k FinalResultKind) String() string {
	switch k {
	case FinalResultOK:
		return "OK"
	case FinalResultError:
		return "Error"
	case FinalResultCME:
		return "CME"
	case FinalResultCMS:
		return "CMS"
	case FinalResultConnect:
		return "Connect"
	case FinalResultNoCarrier:
		return "NoCarrier"
	case FinalResultBusy:
		return "Busy"
	case FinalResultNoAnswer:
		return "NoAnswer"
	case FinalResultNoDialtone:
		return "NoDialtone"
	default:
		return fmt.Sprintf("FinalResultKind(%d)", int(k))
	}
}

// The prefixes of the final result codes that carry a value.
const (
	cmeErrorPrefix = "+CME ERROR:"
	cmsErrorPrefix = "+CMS ERROR:"
	connectPrefix  = "CONNECT "
)

// classifyFinalResult reports which final result code line is, if it is
// one.
func classifyFinalResult(line string) (FinalResult, bool) {
	if kind, ok := bareFinalResult(line); ok {
		return FinalResult{Kind: kind}, true
	}
	if code, ok := strings.CutPrefix(line, cmeErrorPrefix); ok {
		return FinalResult{Kind: FinalResultCME, Code: strings.TrimSpace(code)}, true
	}
	if code, ok := strings.CutPrefix(line, cmsErrorPrefix); ok {
		return FinalResult{Kind: FinalResultCMS, Code: strings.TrimSpace(code)}, true
	}
	if rate, ok := strings.CutPrefix(line, connectPrefix); ok {
		return FinalResult{Kind: FinalResultConnect, ConnectMessage: strings.TrimSpace(rate)}, true
	}
	return FinalResult{}, false
}

// bareFinalResult reports which final result code line is, if it is one
// that carries no value.
func bareFinalResult(line string) (FinalResultKind, bool) {
	switch line {
	case "OK":
		return FinalResultOK, true
	case "ERROR":
		return FinalResultError, true
	case "NO CARRIER":
		return FinalResultNoCarrier, true
	case "BUSY":
		return FinalResultBusy, true
	case "NO ANSWER":
		return FinalResultNoAnswer, true
	case "NO DIALTONE", "NO DIAL TONE":
		return FinalResultNoDialtone, true
	case "CONNECT":
		return FinalResultConnect, true
	default:
		return 0, false
	}
}

// urcPrefixes are the prefixes of unsolicited result codes the handset may
// emit at any time.
var urcPrefixes = [...]string{
	"RING", "+CRING", "+CLIP", "+CCWA", "+CMTI", "+CDSI", "+CMT", "+CDS", "+CREG", "+CIEV", "+CUSD",
}

// responsePrefix returns the "+XXX" prefix a command's information response
// uses, such as "+CSQ" for AT+CSQ; "" when the command has none.
func responsePrefix(command string) string {
	if len(command) < 2 || !asciicase.EqualFold(command[:2], "AT") {
		return ""
	}
	rest := command[2:]
	if !strings.HasPrefix(rest, "+") {
		return ""
	}
	end := strings.IndexFunc(rest, func(c rune) bool { return c != '+' && !isASCIIAlphanumeric(c) })
	if end < 0 {
		end = len(rest)
	}
	return asciicase.Upper(rest[:end])
}

// isASCIIAlphanumeric reports whether c is an ASCII letter or digit.
func isASCIIAlphanumeric(c rune) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9'
}

// isURC reports whether line is an unsolicited result rather than part of
// the response of a command whose own prefix is ownPrefix ("" for none).
func isURC(line, ownPrefix string) bool {
	for _, prefix := range urcPrefixes {
		if prefix != ownPrefix && startsWithCode(line, prefix) {
			return true
		}
	}
	return false
}

// startsWithCode reports whether line starts with code as a whole word:
// followed by nothing, ':' or ' ', so that +CMT does not take +CMTI's lines.
func startsWithCode(line, code string) bool {
	rest, found := strings.CutPrefix(line, code)
	return found && (rest == "" || rest[0] == ':' || rest[0] == ' ')
}

// lineBuffer accumulates raw bytes and yields complete, non-empty lines.
// The zero lineBuffer is empty and ready to use.
type lineBuffer struct {
	buf []byte
}

// push appends data received from the handset.
func (b *lineBuffer) push(data []byte) {
	b.buf = append(b.buf, data...)
}

// nextLine takes the next complete, non-empty line, without its CR, LF and
// NUL padding, and reports whether there was one. Bytes that are not UTF-8
// become U+FFFD, one for each maximal subpart of an ill-formed sequence, as
// Rust's String::from_utf8_lossy makes them.
func (b *lineBuffer) nextLine() (string, bool) {
	for {
		end := bytes.IndexByte(b.buf, '\n')
		if end < 0 {
			return "", false
		}
		raw := b.buf[:end+1]
		b.buf = b.buf[end+1:]
		line := strings.Trim(utf8lossy.String(raw), "\r\n\x00")
		if line != "" {
			return line, true
		}
	}
}

// takePrompt consumes an SMS text-entry prompt ("> ") if one is waiting,
// and reports whether one was.
func (b *lineBuffer) takePrompt() bool {
	start := 0
	for start < len(b.buf) && (b.buf[start] == '\r' || b.buf[start] == '\n') {
		start++
	}
	if start == len(b.buf) || b.buf[start] != '>' {
		return false
	}
	end := start + 1
	if end < len(b.buf) && b.buf[end] == ' ' {
		end++
	}
	b.buf = b.buf[end:]
	return true
}

// clear forgets everything not yet consumed.
func (b *lineBuffer) clear() {
	b.buf = nil
}

// takeRemaining takes everything not yet consumed as a line, such as data
// that followed CONNECT.
func (b *lineBuffer) takeRemaining() []byte {
	remaining := b.buf
	b.buf = nil
	return remaining
}

// SplitCSV splits an AT parameter list on commas, honouring double quotes.
// Quotes are removed and fields are trimmed, so
//
//	1,"REC UNREAD","+881631234567",,"24/10/18,12:00:00+00"
//
// gives the five fields 1, REC UNREAD, +881631234567, an empty one, and
// 24/10/18,12:00:00+00.
func SplitCSV(s string) []string {
	var fields []string
	var field strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			quoted = !quoted
		case c == ',' && !quoted:
			fields = append(fields, strings.TrimSpace(field.String()))
			field.Reset()
		default:
			field.WriteByte(c)
		}
	}
	return append(fields, strings.TrimSpace(field.String()))
}

// fieldWidth is the size in bits of a numeric field, as the Rust version's
// u8 and u32 parse it.
type fieldWidth int

// The widths of the numeric fields the handset reports.
const (
	// byteField is a u8: the +CSQ bars, the +CBC status and level, the
	// +CREG stat.
	byteField fieldWidth = 8
	// wordField is a u32: the +CMGL index, the +CMGS reference.
	wordField fieldWidth = 32
)

// parseNumber parses fields[index], a decimal number of width bits, as
// Rust's unsigned FromStr does: a leading '+' is accepted, while '-', other
// signs and values out of range are refused, giving ErrParse with what (the
// response's prefix) in its text.
func parseNumber(fields []string, index int, what string, width fieldWidth) (int, error) {
	if index < len(fields) {
		value, err := strconv.ParseUint(strings.TrimPrefix(fields[index], "+"), 10, int(width))
		// An int holds every u32 on 64-bit platforms, but not on the 32-bit
		// ones gomobile builds for.
		if err == nil && value <= math.MaxInt {
			return int(value), nil
		}
	}
	return 0, parseError(fmt.Sprintf("%s: bad field %d in %s", what, index, debugList(fields)))
}

// fieldAt returns fields[index], or "" when there are fewer fields.
func fieldAt(fields []string, index int) string {
	if index < len(fields) {
		return fields[index]
	}
	return ""
}

// debugList writes items as the Rust version's {:?} of a Vec<String> does:
// ["a", "b"].
func debugList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = rustdebug.Quote(item)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
