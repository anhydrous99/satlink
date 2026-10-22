package iridium

import (
	"fmt"
	"strings"
	"time"
)

// SMS in text mode (3GPP TS 27.005, AT+CMGF=1): submitting messages and
// listing, reading and deleting stored ones.

// smsTimeout bounds SMS submission, which goes over the satellite link and
// can take a while.
const smsTimeout = 120 * time.Second

// The control characters of SMS text entry, which a message's text must
// not contain.
const (
	// ctrlZ ends text entry: the message is submitted.
	ctrlZ = 0x1A
	// esc aborts text entry.
	esc = 0x1B
)

// sendSMS is SendSMS, with the handset's lock held.
func (h *Handset) sendSMS(number, text string) (int, error) {
	if err := validateNumber(number); err != nil {
		return 0, err
	}
	if strings.IndexByte(text, ctrlZ) >= 0 || strings.IndexByte(text, esc) >= 0 {
		return 0, invalidArgument("SMS text must not contain Ctrl-Z or ESC")
	}
	response, err := h.exec(request{
		command:    fmt.Sprintf(`AT+CMGS="%s"`, number),
		timeout:    smsTimeout,
		completion: untilFinalResult,
		textEntry:  append([]byte(text), ctrlZ),
	})
	if err != nil {
		return 0, err
	}
	fields, err := fieldsWithPrefix(response, "+CMGS")
	if err != nil {
		return 0, err
	}
	return parseNumber(fields, 0, "+CMGS", wordField)
}

// listSMS is ListSMS, with the handset's lock held.
func (h *Handset) listSMS(filter SMSFilter) ([]SMSMessage, error) {
	status, ok := filter.storageStatus()
	if !ok {
		return nil, invalidArgument(fmt.Sprintf("%d is not an SMS filter", int(filter)))
	}
	response, err := h.command(fmt.Sprintf(`AT+CMGL="%s"`, status))
	if err != nil {
		return nil, err
	}
	return parseSMSList(response.Lines)
}

// readSMS is ReadSMS, with the handset's lock held.
func (h *Handset) readSMS(index int) (SMSMessage, error) {
	if err := validateIndex(index); err != nil {
		return SMSMessage{}, err
	}
	response, err := h.command(fmt.Sprintf("AT+CMGR=%d", index))
	if err != nil {
		return SMSMessage{}, err
	}
	return parseReadSMS(index, response.Lines)
}

// deleteSMS is DeleteSMS, with the handset's lock held.
func (h *Handset) deleteSMS(index int) error {
	if err := validateIndex(index); err != nil {
		return err
	}
	_, err := h.command(fmt.Sprintf("AT+CMGD=%d", index))
	return err
}

// storageStatus returns the <stat> value text-mode +CMGL selects f with,
// and false for a filter without a constant of its own.
func (f SMSFilter) storageStatus() (string, bool) {
	switch f {
	case SMSFilterAll:
		return "ALL", true
	case SMSFilterUnread:
		return "REC UNREAD", true
	case SMSFilterRead:
		return "REC READ", true
	case SMSFilterUnsent:
		return "STO UNSENT", true
	case SMSFilterSent:
		return "STO SENT", true
	default:
		return "", false
	}
}

// validateIndex refuses a negative storage index, which no message has.
func validateIndex(index int) error {
	if index < 0 {
		return invalidArgument(fmt.Sprintf("%d is not a message index", index))
	}
	return nil
}

// parseSMSList parses text-mode +CMGL output: a header line per message
// followed by its body.
func parseSMSList(lines []string) ([]SMSMessage, error) {
	var messages []SMSMessage
	var body []string
	for _, line := range lines {
		header, isHeader := strings.CutPrefix(line, "+CMGL:")
		if !isHeader {
			body = append(body, line)
			continue
		}
		setLastText(messages, body)
		body = nil
		message, err := parseListedSMS(header)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	setLastText(messages, body)
	return messages, nil
}

// parseListedSMS parses a +CMGL header, without its prefix:
// <index>,<stat>,<oa/da>,[<alpha>],[<scts>].
func parseListedSMS(header string) (SMSMessage, error) {
	fields := SplitCSV(header)
	index, err := parseNumber(fields, 0, "+CMGL", wordField)
	if err != nil {
		return SMSMessage{}, err
	}
	return SMSMessage{
		Index:     index,
		Status:    fieldAt(fields, 1),
		Address:   fieldAt(fields, 2),
		Timestamp: fieldAt(fields, 4),
	}, nil
}

// setLastText gives the last of messages, if any, the body lines that
// followed its header.
func setLastText(messages []SMSMessage, body []string) {
	if len(messages) > 0 {
		messages[len(messages)-1].Text = strings.Join(body, "\n")
	}
}

// parseReadSMS parses text-mode +CMGR output for the message at index: the
// header +CMGR: <stat>,<oa>,[<alpha>],<scts>, then the text.
func parseReadSMS(index int, lines []string) (SMSMessage, error) {
	header, isHeader := "", false
	if len(lines) > 0 {
		header, isHeader = strings.CutPrefix(lines[0], "+CMGR:")
	}
	if !isHeader {
		return SMSMessage{}, parseError(fmt.Sprintf("no +CMGR header in %s", debugList(lines)))
	}
	fields := SplitCSV(header)
	return SMSMessage{
		Index:     index,
		Status:    fieldAt(fields, 0),
		Address:   fieldAt(fields, 1),
		Timestamp: fieldAt(fields, 3),
		Text:      strings.Join(lines[1:], "\n"),
	}, nil
}
