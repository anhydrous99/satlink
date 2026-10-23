package sim

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/internal/smspdu"
)

// PDUs as the app types them after AT+CMGS, in hexadecimal.
const (
	// helloSubmitPDU sends "hello" to +881631234567 through Iridium's
	// centre: a TPDU of 19 octets after the centre's eight.
	helloSubmitPDU = "0791886126090050" + "11000C918861133254760000AD05E8329BFD06"
	// hiSubmitPDU sends "hi" to +1, leaving the centre to the handset: a
	// TPDU of 11 octets after the empty centre's one.
	hiSubmitPDU = "00" + "11000191F10000AD02E834"
)

// theEpoch is the reception time of the stored messages in these tests.
func theEpoch() time.Time {
	return time.Unix(0, 0)
}

// storeAll stores a message from +1 for each text, and returns their
// indices.
func storeAll(t *testing.T, box *inbox, texts ...string) []uint32 {
	t.Helper()
	var indices []uint32
	for _, text := range texts {
		index, err := box.store("+1", text, theEpoch())
		if err != nil {
			t.Fatalf("store %q: %v", text, err)
		}
		indices = append(indices, index)
	}
	return indices
}

// receivedSMS has m receive a message from from, failing the test if it
// cannot.
func receivedSMS(t *testing.T, m *modem, from, text string, now time.Time) {
	t.Helper()
	if err := m.receiveSMS(from, text, now); err != nil {
		t.Fatalf("receiveSMS(%q, %q): %v", from, text, err)
	}
}

// decodedPDU decodes a PDU line of +CMGL or +CMGR.
func decodedPDU(t *testing.T, line string) smspdu.Message {
	t.Helper()
	octets, err := hex.DecodeString(line)
	if err != nil {
		t.Fatalf("PDU %q: %v", line, err)
	}
	message, err := smspdu.Decode(octets)
	if err != nil {
		t.Fatalf("PDU %s: %v", line, err)
	}
	return message
}

func TestMessagesAreStoredInTheLowestFreeSlot(t *testing.T) {
	t.Parallel()
	var box inbox
	stored := storeAll(t, &box, "a", "b", "c")

	removed := box.remove(2)
	stored = append(stored, storeAll(t, &box, "d", "e")...)

	if !removed || !slices.Equal(stored, []uint32{1, 2, 3, 2, 4}) {
		t.Errorf("stored at %v (removal %v); want 1, 2, 3, then 2 and 4", stored, removed)
	}
}

func TestAMessageIsStoredAsTheDeliverPDUOfIridiumsCentre(t *testing.T) {
	t.Parallel()
	var box inbox
	if _, err := box.store("+881631111111", "first\nsecond", theEpoch()); err != nil {
		t.Fatalf("store: %v", err)
	}

	unread := box.list(smsFilterUnread)

	if len(unread) != 2 || !strings.HasPrefix(unread[1], "0791886126090050") {
		t.Fatalf("unread %q; want a header, then a PDU through +881662900005", unread)
	}
	// The header's <length> is the TPDU's: all but the centre's 8 octets.
	if want := fmt.Sprintf("+CMGL: 1,0,,%d", len(unread[1])/2-8); unread[0] != want {
		t.Errorf("header %q; want %q", unread[0], want)
	}
	want := smspdu.Message{Kind: smspdu.KindDeliver, Address: "+881631111111", Timestamp: "70/01/01,00:00:00+00", Text: "first\nsecond"}
	if message := decodedPDU(t, unread[1]); message != want {
		t.Errorf("PDU says %+v; want %+v", message, want)
	}
}

func TestListingSelectsByStatusAndMarksListedMessagesRead(t *testing.T) {
	t.Parallel()
	var box inbox
	storeAll(t, &box, "hello")

	if unread := box.list(smsFilterUnread); len(unread) != 2 {
		t.Fatalf("unread %q; want one message", unread)
	}

	if lines := box.list(smsFilterUnread); len(lines) != 0 {
		t.Errorf("still unread: %q", lines)
	}
	read, all := box.list(smsFilterRead), box.list(smsFilterAll)
	if len(read) != 2 || len(all) != 2 || !strings.HasPrefix(read[0], "+CMGL: 1,1,,") {
		t.Errorf("read %q, all %q; want the message, now read", read, all)
	}
	if sent := box.list(smsFilterSent); len(sent) != 0 {
		t.Errorf("sent %q; want none", sent)
	}
}

func TestReadingReportsUnreadOnceThenMarksReadAndUnknownIndicesAreAbsent(t *testing.T) {
	t.Parallel()
	var box inbox
	index := storeAll(t, &box, "hello")[0]

	first, found := box.read(index)
	again, _ := box.read(index)

	// The ISU AT Command Reference, +CMGR: status 0 on the first read, 1
	// on the next.
	if !found || len(first) != 2 || !strings.HasPrefix(first[0], "+CMGR: 0,,") || !strings.HasPrefix(again[0], "+CMGR: 1,,") {
		t.Errorf("read %q, then %q; want unread, then read", first, again)
	}
	if message := decodedPDU(t, first[1]); message.Text != "hello" {
		t.Errorf("PDU says %+v; want hello", message)
	}
	if _, found := box.read(99); found {
		t.Error("read a message at 99")
	}
	if box.remove(99) {
		t.Error("removed a message at 99")
	}
}

func TestAMessageWithoutAPDUIsNotStored(t *testing.T) {
	t.Parallel()
	var box inbox

	_, senderErr := box.store("+1\"\r\n2", "x", theEpoch())
	_, textErr := box.store("+1", strings.Repeat("x", 161), theEpoch())

	if senderErr == nil || textErr == nil {
		t.Errorf("stored with errors %v and %v; want both refused", senderErr, textErr)
	}
	if all := box.list(smsFilterAll); len(all) != 0 {
		t.Errorf("stored %q", all)
	}
}

// typed types text into draft, and returns what the last byte did.
func typed(draft *smsDraft, text string) entry {
	var last entry
	for i := range len(text) {
		last = draft.typeByte(text[i])
	}
	return last
}

func TestADraftIsSubmittedOnCtrlZAndCancelledOnEscape(t *testing.T) {
	t.Parallel()
	draft := newSMSDraft(11)

	typing := typed(draft, hiSubmitPDU)
	submitted := draft.typeByte(ctrlZ)
	cancelled := newSMSDraft(11).typeByte(escapeKey)

	if typing.kind != entryContinues {
		t.Errorf("typing gave %v; want entryContinues", typing.kind)
	}
	want := entry{kind: entrySubmitted, message: outgoingSMS{number: "+1", text: "hi"}}
	if submitted != want {
		t.Errorf("Ctrl-Z gave %+v; want %+v", submitted, want)
	}
	if cancelled.kind != entryCancelled {
		t.Errorf("ESC gave %v; want entryCancelled", cancelled.kind)
	}
}

func TestADraftTheHandsetCannotSendIsRefused(t *testing.T) {
	t.Parallel()
	// A DELIVER: what a handset stores, not what it sends.
	deliver := "00" + "040191F10000700101000000000002E834"
	tests := map[string]struct {
		length int
		pdu    string
	}{
		"too long":                  {11, string(bytes.Repeat([]byte("0"), maxEntryLen+1))},
		"not hexadecimal":           {11, "hello"},
		"empty":                     {0, ""},
		"of another length":         {12, hiSubmitPDU},
		"an SMS-DELIVER":            {len(deliver)/2 - 1, deliver},
		"a TPDU that does not hold": {2, "001100"},
	}
	for name, test := range tests {
		draft := newSMSDraft(test.length)
		typed(draft, test.pdu)

		if got := draft.typeByte(ctrlZ); got.kind != entryRefused {
			t.Errorf("%s: Ctrl-Z gave %v; want entryRefused", name, got.kind)
		}
	}
}
