package mobile_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anhydrous99/satlink/mobile"
)

func TestMobileSBDLocalOperationsAndExchange(t *testing.T) {
	h, control := simulatedHandset(t)
	defer h.Close()
	s := mobile.NewSBDSettings()
	if e := h.WriteSBDText("hello"); e != nil {
		t.Fatal(e)
	}
	if e := h.LoopbackSBD(); e != nil {
		t.Fatal(e)
	}
	p, e := h.ReadSBDBinary(s)
	if e != nil || string(p) != "hello" {
		t.Fatal(p, e)
	}
	if e = h.ClearSBDBuffer(2); e != nil {
		t.Fatal(e)
	}
	if _, e = h.SBDStatusJSON(); e != nil {
		t.Fatal(e)
	}
	control.InjectSBD([]byte{0, 255, 10})
	r, e := h.ExchangeSBD([]byte("out"), s, nil)
	if e != nil || !r.Accepted || !bytes.Equal(r.Payload, []byte{0, 255, 10}) {
		t.Fatal(r, e)
	}
	if e = h.ClearSBDBuffer(2); e != nil {
		t.Fatal(e)
	}
	if e = h.WriteSBDBinary([]byte{0, 255}, s); e != nil {
		t.Fatal(e)
	}
	if e = h.ClearSBDBuffer(0); e != nil {
		t.Fatal(e)
	}
}
func TestMobileSBDRegistrationOperations(t *testing.T) {
	h, _ := simulatedHandset(t)
	defer h.Close()
	if e := h.SetSBDRingAlerts(true); e != nil {
		t.Fatal(e)
	}
	if _, e := h.RegisterSBD(); e != nil {
		t.Fatal(e)
	}
	status, e := h.SBDRegistration()
	if e != nil || status != 2 {
		t.Fatal(status, e)
	}
	if _, e = h.DetachSBD(); e != nil {
		t.Fatal(e)
	}
}
func TestMobileSBDQueueListsAndHistory(t *testing.T) {
	q, e := mobile.OpenSBDQueue(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	id, e := q.Enqueue([]byte("hello"))
	if e != nil {
		t.Fatal(e)
	}
	list, e := q.Outbox()
	if e != nil || list.Len() != 1 || list.Get(0).ID != id || list.Get(-1) != nil {
		t.Fatal(list, e)
	}
	copy := list.Get(0)
	copy.Payload[0] = 'x'
	if string(list.Get(0).Payload) != "hello" {
		t.Fatal("shared payload")
	}
	text, e := q.OutboxJSON()
	if e != nil || !strings.Contains(text, id) {
		t.Fatal(text, e)
	}
}
func TestMobileSBDQueueActions(t *testing.T) {
	q, _ := mobile.OpenSBDQueue(t.TempDir())
	id, _ := q.Enqueue([]byte("hello"))
	var e error
	if e = q.Cancel(id); e != nil {
		t.Fatal(e)
	}
	if e = q.Retry(id); e != nil {
		t.Fatal(e)
	}
	if e = q.Cancel(id); e != nil {
		t.Fatal(e)
	}
	if e = q.Purge(id); e != nil {
		t.Fatal(e)
	}
	in, e := q.Inbox()
	if e != nil || in.Len() != 0 || in.Get(0) != nil {
		t.Fatal(in, e)
	}
	if _, e = q.InboxJSON(); e != nil {
		t.Fatal(e)
	}
	if e = q.Check(); e != nil {
		t.Fatal(e)
	}
}
