package smspdu

import (
	"fmt"
	"time"
	"unicode/utf16"
)

// SMS-DELIVER and SMS-SUBMIT TPDUs (TS 23.040 §9.2.2.1 and §9.2.2.2).

// Kind says which TPDU a [Message] came from.
type Kind int

// The TPDUs a handset stores.
const (
	// KindDeliver is an SMS-DELIVER: a message received.
	KindDeliver Kind = iota
	// KindSubmit is an SMS-SUBMIT: a message to send, or sent.
	KindSubmit
)

// String returns the kind's name, such as "Deliver".
func (k Kind) String() string {
	switch k {
	case KindDeliver:
		return "Deliver"
	case KindSubmit:
		return "Submit"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}

// Message is what a TPDU says to people.
type Message struct {
	// Kind is the TPDU it came from.
	Kind Kind
	// Address is a DELIVER's originator, or a SUBMIT's destination.
	Address string
	// Timestamp is a DELIVER's service-centre timestamp,
	// yy/MM/dd,hh:mm:ss±zz with zz in quarter hours; "" for a SUBMIT.
	Timestamp string
	// Text is the user data without its header.
	Text string
}

// The first octet of a TPDU.
const (
	// messageTypeMask is TP-MTI, which says which TPDU it is.
	messageTypeMask = 0x03
	// headerIndicator, TP-UDHI, says that the user data starts with a
	// header.
	headerIndicator = 0x40
	// validityFormatShift and validityFormatMask are TP-VPF, a SUBMIT's
	// validity period format.
	validityFormatShift = 3
	validityFormatMask  = 0x03
)

// The values of TP-MTI for a message stored on a handset.
const (
	messageTypeDeliver = 0x00
	messageTypeSubmit  = 0x01
)

// How long a SUBMIT's validity period is, by TP-VPF: none, enhanced,
// relative and absolute.
var validityPeriodOctets = [4]int{0, 7, 1, 7}

// What satlink writes into a TPDU.
const (
	// submitFirstOctet is an SMS-SUBMIT with a relative validity period,
	// as a 9555A writes its own.
	submitFirstOctet = messageTypeSubmit | 0x02<<validityFormatShift
	// deliverFirstOctet is an SMS-DELIVER that says no more messages are
	// waiting (TP-MMS).
	deliverFirstOctet = messageTypeDeliver | 0x04
	// handsetReference lets the handset choose the message reference.
	handsetReference = 0x00
	// plainProtocol, TP-PID 0, is a message between people's phones.
	plainProtocol = 0x00
	// aWeek is the relative validity period a 9555A gives its own
	// messages: (0xAD − 166) days (TS 23.040 §9.2.3.12.1).
	aWeek = 0xAD
)

// Decode decodes a PDU as +CMGL and +CMGR give it: the service centre's
// address, then an SMS-DELIVER or SMS-SUBMIT.
func Decode(pdu []byte) (Message, error) {
	r := &reader{data: pdu}
	skipServiceCentre(r)
	firstOctet := r.octet()
	message, coding, err := decodeHeader(r, firstOctet)
	if err != nil {
		return Message{}, err
	}
	text, err := decodeUserData(r, firstOctet, coding)
	if err != nil {
		return Message{}, err
	}
	message.Text = text
	return message, nil
}

// decodeHeader reads a TPDU's fields between its first octet and its user
// data, and returns the message they describe and its coding scheme.
func decodeHeader(r *reader, firstOctet byte) (Message, byte, error) {
	var message Message
	var coding byte
	switch messageType := firstOctet & messageTypeMask; {
	case r.err != nil:
		return Message{}, 0, r.err
	case messageType == messageTypeDeliver:
		message, coding = decodeDeliverHeader(r)
	case messageType == messageTypeSubmit:
		message, coding = decodeSubmitHeader(r, firstOctet)
	default:
		return Message{}, 0, fmt.Errorf("TP-MTI %d is neither an SMS-DELIVER nor an SMS-SUBMIT", messageType)
	}
	return message, coding, r.err
}

// decodeDeliverHeader reads an SMS-DELIVER's TP-OA, TP-PID, TP-DCS and
// TP-SCTS, and returns the message and its coding scheme.
func decodeDeliverHeader(r *reader) (Message, byte) {
	originator := decodeAddress(r)
	r.octet() // TP-PID
	coding := r.octet()
	timestamp := decodeTimestamp(r.take(timestampOctets))
	return Message{Kind: KindDeliver, Address: originator, Timestamp: timestamp}, coding
}

// decodeSubmitHeader reads an SMS-SUBMIT's TP-MR, TP-DA, TP-PID, TP-DCS
// and TP-VP, and returns the message and its coding scheme.
func decodeSubmitHeader(r *reader, firstOctet byte) (Message, byte) {
	r.octet() // TP-MR
	destination := decodeAddress(r)
	r.octet() // TP-PID
	coding := r.octet()
	r.take(validityPeriodOctets[firstOctet>>validityFormatShift&validityFormatMask])
	return Message{Kind: KindSubmit, Address: destination}, coding
}

// EncodeSubmit returns the SMS-SUBMIT TPDU that sends text to number. A
// PDU is the service centre's address ([EncodeServiceCentre]) followed by
// it, and +CMGS is given the TPDU's length. It fails if number is not
// dialable, or text does not fit one message.
func EncodeSubmit(number, text string) ([]byte, error) {
	destination, err := encodeAddress(number)
	if err != nil {
		return nil, err
	}
	coding, length, userData, err := encodeUserData(text)
	if err != nil {
		return nil, err
	}
	tpdu := []byte{submitFirstOctet, handsetReference}
	tpdu = append(tpdu, destination...)
	tpdu = append(tpdu, plainProtocol, coding, aWeek, byte(length))
	return append(tpdu, userData...), nil
}

// EncodeDeliver returns the SMS-DELIVER TPDU of text received from
// originator at moment. A PDU is the service centre's address
// ([EncodeServiceCentre]) followed by it, as +CMGL and +CMGR give it. It
// fails if originator is not dialable, or text does not fit one message.
func EncodeDeliver(originator, text string, moment time.Time) ([]byte, error) {
	sender, err := encodeAddress(originator)
	if err != nil {
		return nil, err
	}
	coding, length, userData, err := encodeUserData(text)
	if err != nil {
		return nil, err
	}
	tpdu := append([]byte{deliverFirstOctet}, sender...)
	tpdu = append(tpdu, plainProtocol, coding)
	tpdu = append(tpdu, encodeTimestamp(moment)...)
	tpdu = append(tpdu, byte(length))
	return append(tpdu, userData...), nil
}

// The most one message holds.
const (
	// maxSeptets is the most GSM 7-bit characters, an extension table's
	// counting two.
	maxSeptets = 160
	// maxUCS2Units is the most UCS-2 characters, a character outside the
	// Basic Multilingual Plane counting two.
	maxUCS2Units = 70
)

// The data coding schemes satlink writes (TS 23.038 §4).
const (
	codingGSM7 = 0x00
	codingUCS2 = 0x08
)

// encodeUserData returns text as user data, in the GSM 7-bit alphabet when
// it can be, and in UCS-2 otherwise: the coding scheme, TP-UDL in septets
// or octets, and the octets. It fails if text does not fit one message.
func encodeUserData(text string) (coding byte, length int, data []byte, err error) {
	if septets, ok := gsm7Septets(text); ok {
		if len(septets) > maxSeptets {
			return 0, 0, nil, fmt.Errorf("the text is too long for one SMS: %d GSM characters, of %d", len(septets), maxSeptets)
		}
		return codingGSM7, len(septets), packSeptets(septets), nil
	}
	units := utf16.Encode([]rune(text))
	if len(units) > maxUCS2Units {
		return 0, 0, nil, fmt.Errorf("the text is too long for one SMS: %d UCS-2 characters, of %d", len(units), maxUCS2Units)
	}
	data = make([]byte, 0, 2*len(units))
	for _, unit := range units {
		data = append(data, byte(unit>>8), byte(unit))
	}
	return codingUCS2, len(data), data, nil
}
