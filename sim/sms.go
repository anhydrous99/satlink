package sim

import (
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/anhydrous99/satlink/internal/smspdu"
)

// SMS in PDU mode (3GPP TS 27.005 §3), the only mode Iridium handsets
// have: the SIM's message storage behind AT+CMGL, AT+CMGR and AT+CMGD, and
// the PDU entry after AT+CMGS.

const (
	// ctrlZ ends the PDU and sends the message.
	ctrlZ = 0x1A
	// escapeKey abandons the message.
	escapeKey = 0x1B
	// maxEntryLen is the longest entry accepted, in bytes. A PDU in
	// hexadecimal takes at most 352; the cap only stops a runaway writer
	// from growing a draft without end.
	maxEntryLen = 1024
	// firstIndex is where storage indices start; freed ones are reused.
	firstIndex = 1
)

// serviceCentre is the SMS centre the simulated SIM names: Iridium's.
const serviceCentre = "+881662900005"

// internationalNumber is the type of address +CSCA reports for a number
// with a '+' (TS 24.008 §10.5.4.7).
const internationalNumber = 145

// The storage statuses of PDU mode, as +CMGL and +CMGR report them and
// +CMGL selects by.
const (
	statusUnread = 0
	statusRead   = 1
	statusUnsent = 2
	statusSent   = 3
	statusAll    = 4
)

// smsFilter is which stored messages AT+CMGL lists.
type smsFilter int

const (
	smsFilterAll smsFilter = iota
	smsFilterUnread
	smsFilterRead
	smsFilterUnsent
	smsFilterSent
)

// smsFilterStatuses are the filters by the storage status AT+CMGL names
// them with.
var smsFilterStatuses = map[string]smsFilter{
	fmt.Sprint(statusUnread): smsFilterUnread,
	fmt.Sprint(statusRead):   smsFilterRead,
	fmt.Sprint(statusUnsent): smsFilterUnsent,
	fmt.Sprint(statusSent):   smsFilterSent,
	fmt.Sprint(statusAll):    smsFilterAll,
}

// parseSMSFilter returns the filter AT+CMGL names by status.
func parseSMSFilter(status string) (smsFilter, bool) {
	filter, ok := smsFilterStatuses[status]
	return filter, ok
}

// admits reports whether the filter lists message.
func (f smsFilter) admits(message *storedMessage) bool {
	switch f {
	case smsFilterAll:
		return true
	case smsFilterUnread:
		return message.unread
	case smsFilterRead:
		return !message.unread
	default:
		// AT+CMGS sends straight away, so no outgoing message is stored.
		return false
	}
}

// storedMessage is a received message in the SIM's storage.
type storedMessage struct {
	// pdu is the message as +CMGL and +CMGR give it, in hexadecimal: the
	// service centre's address, then the SMS-DELIVER.
	pdu string
	// tpduLength is the length of the SMS-DELIVER, in octets.
	tpduLength int
	unread     bool
}

func (m *storedMessage) status() int {
	if m.unread {
		return statusUnread
	}
	return statusRead
}

// inbox is the SIM's message storage. The zero inbox is empty and ready.
type inbox struct {
	messages map[uint32]*storedMessage
}

// store stores a message from sender, received at moment, in the lowest
// free slot and returns its index. It fails, storing nothing, if sender is
// not a dialable number or text does not fit one message.
func (b *inbox) store(sender, text string, moment time.Time) (uint32, error) {
	centre, err := smspdu.EncodeServiceCentre(serviceCentre)
	if err != nil {
		return 0, err
	}
	// Received to the second, and never before the epoch, as the clock
	// reports it.
	tpdu, err := smspdu.EncodeDeliver(sender, text, time.Unix(max(moment.Unix(), 0), 0).UTC())
	if err != nil {
		return 0, err
	}
	if b.messages == nil {
		b.messages = map[uint32]*storedMessage{}
	}
	index := b.lowestFreeIndex()
	b.messages[index] = &storedMessage{
		pdu:        strings.ToUpper(hex.EncodeToString(append(centre, tpdu...))),
		tpduLength: len(tpdu),
		unread:     true,
	}
	return index, nil
}

// list returns the +CMGL lines for the messages filter admits, each header
// followed by its PDU. Unread messages are marked read once listed, as TS
// 27.005 §3.4.2 specifies. The headers carry TS 27.005's <length>; a
// 9555A's are one more.
func (b *inbox) list(filter smsFilter) []string {
	var lines []string
	for _, index := range b.indices() {
		message := b.messages[index]
		if !filter.admits(message) {
			continue
		}
		lines = append(lines, fmt.Sprintf("+CMGL: %d,%d,,%d", index, message.status(), message.tpduLength), message.pdu)
		message.unread = false
	}
	return lines
}

// read returns the +CMGR lines for the message at index, which is marked
// read once its status has been reported, and false if there is none.
func (b *inbox) read(index uint32) ([]string, bool) {
	message, ok := b.messages[index]
	if !ok {
		return nil, false
	}
	header := fmt.Sprintf("+CMGR: %d,,%d", message.status(), message.tpduLength)
	message.unread = false
	return []string{header, message.pdu}, true
}

// remove deletes the message at index, and reports whether there was one.
func (b *inbox) remove(index uint32) bool {
	_, ok := b.messages[index]
	delete(b.messages, index)
	return ok
}

// indices returns the storage indices in use, in order.
func (b *inbox) indices() []uint32 {
	return slices.Sorted(maps.Keys(b.messages))
}

func (b *inbox) lowestFreeIndex() uint32 {
	candidate := uint32(firstIndex)
	for _, index := range b.indices() {
		if index != candidate {
			break
		}
		candidate++
	}
	return candidate
}

// outgoingSMS is a message submitted with AT+CMGS.
type outgoingSMS struct {
	number string
	text   string
}

// entryKind says what a byte typed after the "> " prompt did.
type entryKind int

const (
	// entryContinues means it became part of the PDU.
	entryContinues entryKind = iota
	// entrySubmitted means Ctrl-Z after a PDU the handset can send.
	entrySubmitted
	// entryCancelled means ESC: forget the message.
	entryCancelled
	// entryRefused means Ctrl-Z after a PDU the handset cannot send.
	entryRefused
)

// entry is what [smsDraft.typeByte] did.
type entry struct {
	kind entryKind
	// message is what to send (entrySubmitted).
	message outgoingSMS
}

// smsDraft is a PDU being typed after AT+CMGS=<length>.
type smsDraft struct {
	// length is the length of the TPDU AT+CMGS announced.
	length     int
	pdu        []byte
	overflowed bool
}

// newSMSDraft returns a draft of a TPDU of length octets, with nothing
// typed yet.
func newSMSDraft(length int) *smsDraft {
	return &smsDraft{length: length}
}

// typeByte takes one typed byte.
func (d *smsDraft) typeByte(b byte) entry {
	switch {
	case b == ctrlZ:
		return d.submitted()
	case b == escapeKey:
		return entry{kind: entryCancelled}
	case len(d.pdu) < maxEntryLen:
		d.pdu = append(d.pdu, b)
	default:
		d.overflowed = true
	}
	return entry{kind: entryContinues}
}

// submitted is what Ctrl-Z does: the message to send, or a refusal of a
// PDU that is no SMS-SUBMIT of the length announced. Real handsets do not
// check the length; the simulated one does, so that a wrong one shows in
// tests.
func (d *smsDraft) submitted() entry {
	refused := entry{kind: entryRefused}
	octets, err := hex.DecodeString(string(d.pdu))
	if d.overflowed || err != nil || len(octets) == 0 {
		return refused
	}
	if tpduLength := len(octets) - 1 - int(octets[0]); tpduLength != d.length {
		return refused
	}
	message, err := smspdu.Decode(octets)
	if err != nil || message.Kind != smspdu.KindSubmit {
		return refused
	}
	return entry{kind: entrySubmitted, message: outgoingSMS{number: message.Address, text: message.Text}}
}
