package sim

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/anhydrous99/satlink/internal/utf8lossy"
)

// Text-mode SMS (3GPP TS 27.005): the SIM's message storage behind
// AT+CMGL, AT+CMGR and AT+CMGD, and the text entry after AT+CMGS.

const (
	// ctrlZ ends the text and sends the message.
	ctrlZ = 0x1A
	// escapeKey abandons the message.
	escapeKey = 0x1B
	// maxTextLen is the longest text accepted, in bytes. An SMS holds 160
	// characters; the cap only stops a runaway writer from growing a draft
	// without end.
	maxTextLen = 1024
	// firstIndex is where storage indices start; freed ones are reused.
	firstIndex = 1
)

// headerBreakingRemover drops the characters that would break the quoted,
// line-based +CMGL and +CMGR headers if a sender's number contained them.
var headerBreakingRemover = strings.NewReplacer(`"`, "", "\r", "", "\n", "")

// The statuses of stored messages, as +CMGL and +CMGR name them.
const (
	statusUnread = "REC UNREAD"
	statusRead   = "REC READ"
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

// smsFilterStatuses are the status texts AT+CMGL names its filters by, in
// upper case.
var smsFilterStatuses = map[string]smsFilter{
	"ALL":        smsFilterAll,
	statusUnread: smsFilterUnread,
	statusRead:   smsFilterRead,
	"STO UNSENT": smsFilterUnsent,
	"STO SENT":   smsFilterSent,
}

// parseSMSFilter returns the filter AT+CMGL names by status, in upper case.
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
	sender   string
	text     string
	received timestamp
	unread   bool
}

func (m *storedMessage) status() string {
	if m.unread {
		return statusUnread
	}
	return statusRead
}

// inbox is the SIM's message storage. The zero inbox is empty and ready.
type inbox struct {
	messages map[uint32]*storedMessage
}

// store stores a received message in the lowest free slot and returns its
// index.
func (b *inbox) store(sender, text string, received timestamp) uint32 {
	if b.messages == nil {
		b.messages = map[uint32]*storedMessage{}
	}
	index := b.lowestFreeIndex()
	b.messages[index] = &storedMessage{
		sender:   headerBreakingRemover.Replace(sender),
		text:     text,
		received: received,
		unread:   true,
	}
	return index
}

// list returns the +CMGL lines for the messages filter admits, each header
// followed by its text. Unread messages are marked read once listed, as TS
// 27.005 §3.4.2 specifies.
func (b *inbox) list(filter smsFilter) []string {
	var lines []string
	for _, index := range b.indices() {
		message := b.messages[index]
		if !filter.admits(message) {
			continue
		}
		lines = append(lines, fmt.Sprintf(`+CMGL: %d,"%s","%s",,"%s"`,
			index, message.status(), message.sender, message.received))
		lines = append(lines, textLines(message.text)...)
		message.unread = false
	}
	return lines
}

// read returns the +CMGR lines for the message at index, which is marked
// read first, and false if there is none.
func (b *inbox) read(index uint32) ([]string, bool) {
	message, ok := b.messages[index]
	if !ok {
		return nil, false
	}
	message.unread = false
	header := fmt.Sprintf(`+CMGR: "%s","%s",,"%s"`, message.status(), message.sender, message.received)
	return append([]string{header}, textLines(message.text)...), true
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

// textLines returns a message text as response lines; the client joins
// them with "\n" again.
func textLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, "\r")
	}
	return lines
}

// outgoingSMS is a message submitted with AT+CMGS.
type outgoingSMS struct {
	number string
	text   string
}

// entryKind says what a byte typed after the "> " prompt did.
type entryKind int

const (
	// entryContinues means it became part of the text.
	entryContinues entryKind = iota
	// entrySubmitted means Ctrl-Z: send the message.
	entrySubmitted
	// entryCancelled means ESC: forget the message.
	entryCancelled
	// entryTooLong means Ctrl-Z after more text than the handset accepts.
	entryTooLong
)

// entry is what [smsDraft.typeByte] did.
type entry struct {
	kind entryKind
	// message is what to send (entrySubmitted).
	message outgoingSMS
}

// smsDraft is a message being typed after AT+CMGS.
type smsDraft struct {
	number     string
	text       []byte
	overflowed bool
}

// newSMSDraft returns a draft to number, with no text yet.
func newSMSDraft(number string) *smsDraft {
	return &smsDraft{number: number}
}

// typeByte takes one typed byte.
func (d *smsDraft) typeByte(b byte) entry {
	switch {
	case b == ctrlZ && d.overflowed:
		return entry{kind: entryTooLong}
	case b == ctrlZ:
		return entry{kind: entrySubmitted, message: outgoingSMS{number: d.number, text: utf8lossy.String(d.text)}}
	case b == escapeKey:
		return entry{kind: entryCancelled}
	case len(d.text) < maxTextLen:
		d.text = append(d.text, b)
	default:
		d.overflowed = true
	}
	return entry{kind: entryContinues}
}
