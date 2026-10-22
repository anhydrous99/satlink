package sim

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"
)

// receivedAtTheEpoch is the reception time of the stored messages in these
// tests.
func receivedAtTheEpoch() timestamp {
	return timestampAt(time.Unix(0, 0))
}

func TestMessagesAreStoredInTheLowestFreeSlot(t *testing.T) {
	t.Parallel()
	var box inbox
	stored := []uint32{
		box.store("+1", "a", receivedAtTheEpoch()),
		box.store("+1", "b", receivedAtTheEpoch()),
		box.store("+1", "c", receivedAtTheEpoch()),
	}

	removed := box.remove(2)
	stored = append(stored, box.store("+1", "d", receivedAtTheEpoch()), box.store("+1", "e", receivedAtTheEpoch()))

	if !removed || !slices.Equal(stored, []uint32{1, 2, 3, 2, 4}) {
		t.Errorf("stored at %v (removal %v); want 1, 2, 3, then 2 and 4", stored, removed)
	}
}

func TestListingSelectsByStatusAndMarksListedMessagesRead(t *testing.T) {
	t.Parallel()
	var box inbox
	box.store("+881631111111", "first\nsecond", receivedAtTheEpoch())

	unread := box.list(smsFilterUnread)

	want := []string{
		`+CMGL: 1,"REC UNREAD","+881631111111",,"70/01/01,00:00:00+00"`,
		"first",
		"second",
	}
	if !slices.Equal(unread, want) {
		t.Errorf("unread %q; want %q", unread, want)
	}
	if lines := box.list(smsFilterUnread); len(lines) != 0 {
		t.Errorf("still unread: %q", lines)
	}
	if read, all := box.list(smsFilterRead), box.list(smsFilterAll); len(read) != 3 || len(all) != 3 {
		t.Errorf("read %q, all %q; want three lines each", read, all)
	}
	if sent := box.list(smsFilterSent); len(sent) != 0 {
		t.Errorf("sent %q; want none", sent)
	}
}

func TestReadingMarksReadAndUnknownIndicesAreAbsent(t *testing.T) {
	t.Parallel()
	var box inbox
	index := box.store("+1", "hello", receivedAtTheEpoch())

	lines, found := box.read(index)

	want := []string{`+CMGR: "REC READ","+1",,"70/01/01,00:00:00+00"`, "hello"}
	if !found || !slices.Equal(lines, want) {
		t.Errorf("read %q, %v; want %q", lines, found, want)
	}
	if unread := box.list(smsFilterUnread); len(unread) != 0 {
		t.Errorf("still unread: %q", unread)
	}
	if _, found := box.read(99); found {
		t.Error("read a message at 99")
	}
	if box.remove(99) {
		t.Error("removed a message at 99")
	}
}

func TestASenderCannotBreakTheHeader(t *testing.T) {
	t.Parallel()
	var box inbox
	box.store("+1\"\r\n2", "x", receivedAtTheEpoch())

	if header := box.list(smsFilterAll)[0]; !strings.Contains(header, `"+12"`) {
		t.Errorf("header %q; want the sender as \"+12\"", header)
	}
}

func TestADraftIsSubmittedOnCtrlZAndCancelledOnEscape(t *testing.T) {
	t.Parallel()
	draft := newSMSDraft("+1")

	typed := []entry{draft.typeByte('h'), draft.typeByte('i')}
	submitted := draft.typeByte(ctrlZ)
	cancelled := newSMSDraft("+1").typeByte(escapeKey)

	for _, entry := range typed {
		if entry.kind != entryContinues {
			t.Errorf("typing gave %v; want entryContinues", entry.kind)
		}
	}
	want := entry{kind: entrySubmitted, message: outgoingSMS{number: "+1", text: "hi"}}
	if submitted != want {
		t.Errorf("Ctrl-Z gave %+v; want %+v", submitted, want)
	}
	if cancelled.kind != entryCancelled {
		t.Errorf("ESC gave %v; want entryCancelled", cancelled.kind)
	}
}

func TestAnOverlongDraftIsRefused(t *testing.T) {
	t.Parallel()
	draft := newSMSDraft("+1")
	for _, b := range bytes.Repeat([]byte("x"), maxTextLen+1) {
		draft.typeByte(b)
	}

	if got := draft.typeByte(ctrlZ); got.kind != entryTooLong {
		t.Errorf("Ctrl-Z gave %v; want entryTooLong", got.kind)
	}
}
