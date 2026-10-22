package sim

import (
	"fmt"
	"strings"
)

// How the handset frames what it says: verbose result codes and
// information text, each between CR LF pairs (ITU-T V.250 §5.7).

const (
	crlf = "\r\n"
	// connectResult is CONNECT with the rate the handset reports on its
	// serial interface, which says nothing about the satellite link's
	// throughput.
	connectResult = "CONNECT 19200"
	// textPrompt asks for the text of an SMS.
	textPrompt = "> "
)

// replyKind says what a [reply] is.
type replyKind int

const (
	// replyOK is information lines, if any, then OK.
	replyOK replyKind = iota
	// replyError means the command is unknown or its parameters are
	// invalid.
	replyError
	// replySMSError is +CMS ERROR: <code>, an SMS service failure.
	replySMSError
	// replyNoCarrier means the call is over, or there never was one.
	replyNoCarrier
	// replyConnect means a data call is up and the handset is in
	// online-data mode.
	replyConnect
	// replyTextPrompt is the "> " prompt that asks for an SMS text.
	replyTextPrompt
)

// smsError is a +CMS ERROR code the handset reports (3GPP TS 27.005
// §3.2.5).
type smsError int

const (
	// smsErrorInvalidPDUModeParameter reports a PDU the handset cannot
	// send: not hexadecimal, too long, no SMS-SUBMIT, or of another length
	// than AT+CMGS was given.
	smsErrorInvalidPDUModeParameter smsError = 304
	// smsErrorInvalidMemoryIndex reports that no message is stored at that
	// index.
	smsErrorInvalidMemoryIndex smsError = 321
)

// reply is the handset's answer to a command.
type reply struct {
	kind replyKind
	// lines are the information lines before OK (replyOK).
	lines []string
	// code is the SMS error (replySMSError).
	code smsError
}

// okReply returns OK after lines, if any.
func okReply(lines ...string) reply {
	return reply{kind: replyOK, lines: lines}
}

// smsErrorReply returns +CMS ERROR with code.
func smsErrorReply(code smsError) reply {
	return reply{kind: replySMSError, code: code}
}

// encode returns the bytes the handset sends.
func (r reply) encode() string {
	switch r.kind {
	case replyOK:
		if len(r.lines) == 0 {
			return framed("OK")
		}
		return framed(strings.Join(r.lines, crlf)) + framed("OK")
	case replyError:
		return framed("ERROR")
	case replySMSError:
		return framed(fmt.Sprintf("+CMS ERROR: %d", r.code))
	case replyNoCarrier:
		return framed("NO CARRIER")
	case replyConnect:
		return framed(connectResult)
	case replyTextPrompt:
		return crlf + textPrompt
	default:
		panic(fmt.Sprintf("sim: a reply of unknown kind %d", r.kind))
	}
}

// unsolicited returns an unsolicited result code such as +CMTI: "SM",3,
// framed like any result.
func unsolicited(text string) string {
	return framed(text)
}

func framed(text string) string {
	return crlf + text + crlf
}
