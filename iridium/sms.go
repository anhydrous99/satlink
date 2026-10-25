package iridium

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/anhydrous99/satlink/internal/debugtext"
	"github.com/anhydrous99/satlink/internal/smspdu"
)

// SMS in PDU mode (3GPP TS 27.005 §3, AT+CMGF=0), the only mode Iridium
// handsets have (ISU AT Command Reference, +CMGF): submitting messages,
// and listing, reading and deleting stored ones. Package smspdu encodes
// and decodes the PDUs.

// How long the SMS commands that take a while wait for their final result.
const (
	// smsTimeout bounds SMS submission, which goes over the satellite link.
	smsTimeout = 120 * time.Second
	// smsListTimeout bounds a listing: a 9555A takes about a third of a
	// second for each message it lists, so a SIM full of fifty takes
	// seventeen.
	smsListTimeout = 60 * time.Second
)

// The control characters of SMS text entry.
const (
	// ctrlZ ends text entry: the message is submitted.
	ctrlZ = 0x1A
	// esc aborts text entry.
	esc = 0x1B
)

// iridiumServiceCentre is Iridium's SMS centre, which a 9555A names in its
// own messages. Messages go through it unless the SIM names another
// (AT+CSCA): a 9555A whose SIM names none refuses a PDU that leaves the
// choice to it, with +CMS ERROR: 303.
const iridiumServiceCentre = "+881662900005"

// smsStatusNames are the storage statuses as text mode names them, by
// their codes in PDU mode.
var smsStatusNames = [...]string{"REC UNREAD", "REC READ", "STO UNSENT", "STO SENT"}

// sendSMS is SendSMS, with the handset's lock held.
func (h *Handset) sendSMS(number, text string) (int, error) {
	if err := validateNumber(number); err != nil {
		return 0, err
	}
	tpdu, err := smspdu.EncodeSubmit(number, text)
	if err != nil {
		return 0, invalidArgument(err.Error())
	}
	address, err := h.serviceCentreAddress()
	if err != nil {
		return 0, err
	}
	pdu := append(address, tpdu...)
	response, err := h.exec(request{
		command:    fmt.Sprintf("AT+CMGS=%d", len(tpdu)),
		timeout:    smsTimeout,
		completion: untilFinalResult,
		textEntry:  append([]byte(strings.ToUpper(hex.EncodeToString(pdu))), ctrlZ),
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

// serviceCentreAddress returns the address of the SMS centre the SIM
// names (AT+CSCA?), or of Iridium's when it names none. A handset that
// cannot say gets Iridium's too: what fails then fails again at AT+CMGS,
// which reports it. It fails only with the errors a method returns all the
// same (callTrouble).
func (h *Handset) serviceCentreAddress() ([]byte, error) {
	fields, err := h.queryFields("AT+CSCA?", "+CSCA")
	if trouble := callTrouble(err); trouble != nil {
		return nil, trouble
	}
	if err == nil && fieldAt(fields, 0) != "" {
		if address, err := smspdu.EncodeServiceCentre(fields[0]); err == nil {
			return address, nil
		}
	}
	// Encoding a constant number cannot fail.
	address, _ := smspdu.EncodeServiceCentre(iridiumServiceCentre)
	return address, nil
}

// listSMS is ListSMS, with the handset's lock held.
func (h *Handset) listSMS(filter SMSFilter) ([]SMSMessage, error) {
	status, ok := filter.storageStatus()
	if !ok {
		return nil, invalidArgument(fmt.Sprintf("%d is not an SMS filter", int(filter)))
	}
	response, err := h.commandWithTimeout(fmt.Sprintf("AT+CMGL=%d", status), smsListTimeout)
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

// storageStatus returns the <stat> value PDU-mode +CMGL selects f with,
// and false for a filter without a constant of its own.
func (f SMSFilter) storageStatus() (int, bool) {
	switch f {
	case SMSFilterAll:
		return 4, true
	case SMSFilterUnread:
		return 0, true
	case SMSFilterRead:
		return 1, true
	case SMSFilterUnsent:
		return 2, true
	case SMSFilterSent:
		return 3, true
	default:
		return 0, false
	}
}

// validateIndex refuses a negative storage index, which no message has.
func validateIndex(index int) error {
	if index < 0 {
		return invalidArgument(fmt.Sprintf("%d is not a message index", index))
	}
	return nil
}

// parseSMSList parses PDU-mode +CMGL output: for each message, the header
// +CMGL: <index>,<stat>,[<alpha>],<length>, then its PDU on a line of its
// own.
func parseSMSList(lines []string) ([]SMSMessage, error) {
	var messages []SMSMessage
	for i := 0; i < len(lines); i += 2 {
		header, isHeader := strings.CutPrefix(lines[i], "+CMGL:")
		if !isHeader {
			return nil, parseError(fmt.Sprintf("no +CMGL header in %s", debugtext.Quote(lines[i])))
		}
		fields := SplitCSV(header)
		index, err := parseNumber(fields, 0, "+CMGL", wordField)
		if err != nil {
			return nil, err
		}
		if i+1 == len(lines) {
			return nil, parseError(fmt.Sprintf("+CMGL: message %d has no PDU", index))
		}
		message, err := decodeStoredSMS(index, fields[1:], lines[i+1], "+CMGL")
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// parseReadSMS parses PDU-mode +CMGR output for the message at index: the
// header +CMGR: <stat>,[<alpha>],<length>, then its PDU.
func parseReadSMS(index int, lines []string) (SMSMessage, error) {
	header, isHeader := "", false
	if len(lines) > 0 {
		header, isHeader = strings.CutPrefix(lines[0], "+CMGR:")
	}
	if !isHeader || len(lines) < 2 {
		return SMSMessage{}, parseError(fmt.Sprintf("no +CMGR header and PDU in %s", debugList(lines)))
	}
	return decodeStoredSMS(index, SplitCSV(header), lines[1], "+CMGR")
}

// decodeStoredSMS returns the message at index from the header fields
// that follow its index, <stat>,[<alpha>],<length>, and its PDU in
// hexadecimal. The handset's <length> goes unread: a 9555A's is one more
// than TS 27.005 says, and the PDU holds its own lengths.
func decodeStoredSMS(index int, fields []string, pdu, prefix string) (SMSMessage, error) {
	status, err := parseNumber(fields, 0, prefix, byteField)
	if err != nil {
		return SMSMessage{}, err
	}
	octets, err := hex.DecodeString(strings.TrimSpace(pdu))
	if err != nil {
		return SMSMessage{}, parseError(fmt.Sprintf("%s: message %d: the PDU is not hexadecimal", prefix, index))
	}
	message, err := smspdu.Decode(octets)
	if err != nil {
		return SMSMessage{}, parseError(fmt.Sprintf("%s: message %d: %v", prefix, index, err))
	}
	return SMSMessage{
		Index:     index,
		Status:    smsStatusName(status),
		Address:   message.Address,
		Timestamp: message.Timestamp,
		Text:      message.Text,
	}, nil
}

// smsStatusName returns the text-mode name of a PDU-mode storage status,
// such as "REC UNREAD", or "Unknown(7)" for a code without one.
func smsStatusName(code int) string {
	if code < len(smsStatusNames) {
		return smsStatusNames[code]
	}
	return fmt.Sprintf("Unknown(%d)", code)
}
