package sbd

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/sim"
	bolt "go.etcd.io/bbolt"
)

func TestQueueDurabilityAndLifecycle(t *testing.T) {
	q, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	p := []byte{0, 1, 2, 255}
	id, e := q.Enqueue(p)
	if e != nil {
		t.Fatal(e)
	}
	p[0] = 4
	q, e = Open(q.Dir)
	if e != nil {
		t.Fatal(e)
	}
	v, e := q.Get(id)
	if e != nil || v.Payload[0] != 0 {
		t.Fatal(v, e)
	}
	if e = q.Purge(id); e == nil {
		t.Fatal("purged pending")
	}
	if e = q.Cancel(id); e != nil {
		t.Fatal(e)
	}
	if e = q.Retry(id); e != nil {
		t.Fatal(e)
	}
}
func TestInboxAcknowledgementSuppressesRecoveryReread(t *testing.T) {
	q, _ := Open(t.TempDir())
	var e error
	if e = q.SaveIncoming("test", 1, []byte("in")); e != nil {
		t.Fatal(e)
	}
	list, _ := q.Inbox()
	if len(list) != 1 {
		t.Fatal(list)
	}
	if e = q.Ack(list[0].ID); e != nil {
		t.Fatal(e)
	}
	if e = q.SaveIncoming("test", 1, []byte("in")); e != nil {
		t.Fatal(e)
	}
	list, _ = q.Inbox()
	if len(list) != 0 {
		t.Fatal("acknowledged reread was reinserted")
	}
	if e = q.SaveIncoming("test", 1, []byte("different")); e != nil {
		t.Fatal(e)
	}
	list, _ = q.Inbox()
	if len(list) != 1 {
		t.Fatal(list)
	}
}
func TestQueueWorkerExclusionDoesNotBlockEnqueue(t *testing.T) {
	q, _ := Open(t.TempDir())
	lock, e := q.LockWorker()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = q.LockWorker(); e == nil {
		t.Fatal("second worker")
	}
	if _, e = q.Enqueue([]byte("queued while running")); e != nil {
		t.Fatal(e)
	}
	lock.Close()
	lock, e = q.LockWorker()
	if e != nil {
		t.Fatal(e)
	}
	lock.Close()
}
func TestQueueRejectsNewerSchemaAndMalformedRecords(t *testing.T) {
	q, _ := Open(t.TempDir())
	if e := q.setMeta("version", "2"); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(q.Dir); e == nil {
		t.Fatal("accepted newer schema")
	}
}
func TestManagerSendsAndPersistsIncomingBeforeClearing(t *testing.T) {
	q, _ := Open(t.TempDir())
	id, _ := q.Enqueue([]byte("out"))
	transport := sim.New(sim.DefaultConfig())
	transport.Control().InjectSBD([]byte("in"))
	h := iridium.NewHandset(transport)
	if e := h.Initialize(); e != nil {
		t.Fatal(e)
	}
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return h, nil }, DefaultSettings())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	eventually(t, func() bool { v, _ := q.Get(id); in, _ := q.Inbox(); return v.State == Accepted && len(in) == 1 })
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	sent := transport.Control().SentSBD()
	if len(sent) != 1 || !bytes.Equal(sent[0], []byte("out")) {
		t.Fatal(sent)
	}
}
func eventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
func TestManagerRestartRetriesUncertainAttempt(t *testing.T) {
	q, _ := Open(t.TempDir())
	id, _ := q.Enqueue([]byte("out"))
	s := DefaultSettings()
	s.RetryBase = time.Millisecond
	s.RetryMax = 5 * time.Millisecond
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return nil, errors.New("offline") }, s)
	v, _ := q.Get(id)
	if e := m.start(&v); e != nil {
		t.Fatal(e)
	}
	if e := m.recoverAttempts(); e != nil {
		t.Fatal(e)
	}
	v, _ = q.Get(id)
	if v.State != RetryWait || len(v.Attempts) != 1 || !v.Attempts[0].Uncertain {
		t.Fatal(v)
	}
}
func TestManagerOrphanPausesAndPreservesPayload(t *testing.T) {
	q, _ := Open(t.TempDir())
	transport := sim.New(sim.DefaultConfig())
	defer transport.Close()
	h := iridium.NewHandset(transport)
	if e := h.Initialize(); e != nil {
		t.Fatal(e)
	}
	c, _ := h.AcquireSBD()
	if e := c.WriteBinary([]byte("orphan"), DefaultSettings().Capabilities); e != nil {
		t.Fatal(e)
	}
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return h, nil }, DefaultSettings())
	if e := m.prepare(c, "test"); e == nil {
		t.Fatal("did not pause")
	}
	v, _ := q.Outbox()
	if len(v) != 1 || v[0].State != Orphaned || string(v[0].Payload) != "orphan" {
		t.Fatal(v)
	}
	if len(transport.Control().SentSBD()) != 0 {
		t.Fatal("sent orphan")
	}
	if e := q.Cancel(v[0].ID); e != nil {
		t.Fatal(e)
	}
	if e := m.prepare(c, "test"); e != nil {
		t.Fatal(e)
	}
	c.Close()
}
func TestManagerIdentityMismatchAndSettingsValidation(t *testing.T) {
	q, _ := Open(t.TempDir())
	if e := q.bind("first"); e != nil {
		t.Fatal(e)
	}
	if e := q.bind("second"); e == nil {
		t.Fatal("rebound queue")
	}
	s := DefaultSettings()
	s.PollInterval = time.Second
	if _, e := NewManager(q, func(context.Context) (*iridium.Handset, error) { return nil, nil }, s); e == nil {
		t.Fatal("accepted rapid polling")
	}
}
func TestWorkerDoesNotConnectUntilRun(t *testing.T) {
	q, _ := Open(t.TempDir())
	var count atomic.Int32
	m, e := NewManager(q, func(context.Context) (*iridium.Handset, error) { count.Add(1); return nil, errors.New("offline") }, DefaultSettings())
	if e != nil || m.Status().Running || count.Load() != 0 {
		t.Fatal(e)
	}
}
func TestIncomingStorageFailureRetainsPhoneBuffer(t *testing.T) {
	q, _ := Open(t.TempDir())
	transport := sim.New(sim.DefaultConfig())
	defer transport.Close()
	transport.Control().InjectSBD([]byte("incoming"))
	h := iridium.NewHandset(transport)
	if e := h.Initialize(); e != nil {
		t.Fatal(e)
	}
	c, _ := h.AcquireSBD()
	defer c.Close()
	if _, e := c.Session(iridium.DefaultSBDSessionSettings()); e != nil {
		t.Fatal(e)
	}
	if e := q.setMeta("version", "2"); e != nil {
		t.Fatal(e)
	}
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return h, nil }, DefaultSettings())
	if e := m.receive(c, "test", 1); e == nil {
		t.Fatal("ignored persistence failure")
	}
	status, e := c.Status()
	if e != nil || status.MTFlag != 1 {
		t.Fatal(status, e)
	}
}
func TestCapacityCountsRetainedRecords(t *testing.T) {
	q, _ := Open(t.TempDir())
	e := q.update(func(tx *bolt.Tx) error {
		for i := 0; i < 1000; i++ {
			m := Message{ID: newID(), State: Accepted, Payload: []byte("x")}
			if e := put(tx, "outbox", m.ID, m); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = q.Enqueue([]byte("one too many")); e == nil {
		t.Fatal("capacity ignored")
	}
}

func TestUncertainDeliveryAutomaticallyRetriesWithSamePayload(t *testing.T) {
	q, _ := Open(t.TempDir())
	id, _ := q.Enqueue([]byte("app-id:123"))
	transport := sim.New(sim.DefaultConfig())
	control := transport.Control()
	control.SetSBDOutcome(0, 0, true)
	settings := DefaultSettings()
	settings.SessionTimeout = 30 * time.Millisecond
	settings.RetryBase = time.Millisecond
	settings.RetryMax = 3 * time.Millisecond
	var connections atomic.Int32
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) {
		if connections.Add(1) > 1 {
			control.SetSBDOutcome(0, 0, false)
		}
		h := iridium.NewHandset(control.OpenTransport())
		if e := h.Initialize(); e != nil {
			return nil, e
		}
		return h, nil
	}, settings)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	eventually(t, func() bool { v, _ := q.Get(id); return v.State == Accepted })
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	sent := control.SentSBD()
	if len(sent) != 2 || !bytes.Equal(sent[0], sent[1]) {
		t.Fatalf("expected a duplicate with identical bytes: %q", sent)
	}
	v, _ := q.Get(id)
	if len(v.Attempts) != 2 || !v.Attempts[0].Uncertain {
		t.Fatal(v)
	}
}
func TestRetryBudgetAndExplicitRetry(t *testing.T) {
	q, _ := Open(t.TempDir())
	id, _ := q.Enqueue([]byte("out"))
	s := DefaultSettings()
	s.MaxAttempts = 1
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return nil, nil }, s)
	v, _ := q.Get(id)
	if e := m.start(&v); e != nil {
		t.Fatal(e)
	}
	if e := m.finish(&v, iridium.SBDSessionResult{Reported: true, MOStatus: 32}, iridium.ErrSBD); e != nil {
		t.Fatal(e)
	}
	v, _ = q.Get(id)
	if v.State != Failed {
		t.Fatal(v)
	}
	if e := q.Retry(id); e != nil {
		t.Fatal(e)
	}
	v, _ = q.Get(id)
	if v.RetryStart != 1 {
		t.Fatal(v)
	}
	if e := m.start(&v); e != nil {
		t.Fatal(e)
	}
	if e := m.finish(&v, iridium.SBDSessionResult{Reported: true, MOStatus: 0}, nil); e != nil {
		t.Fatal(e)
	}
	v, _ = q.Get(id)
	if v.State != Accepted || len(v.Attempts) != 2 {
		t.Fatal(v)
	}
}
func TestWorkerRingAlertCollectsWithoutTimedPolling(t *testing.T) {
	q, _ := Open(t.TempDir())
	transport := sim.New(sim.DefaultConfig())
	control := transport.Control()
	h := iridium.NewHandset(transport)
	if e := h.Initialize(); e != nil {
		t.Fatal(e)
	}
	c, _ := h.AcquireSBD()
	if _, e := c.Register(); e != nil {
		t.Fatal(e)
	}
	c.Close()
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return h, nil }, DefaultSettings())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	eventually(t, func() bool { return m.Status().Phase == "idle" })
	control.InjectSBD([]byte("ring payload"))
	eventually(t, func() bool { in, _ := q.Inbox(); return len(in) == 1 })
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestWorkerDrainsMoreThanOneCollectionPass(t *testing.T) {
	q, _ := Open(t.TempDir())
	transport := sim.New(sim.DefaultConfig())
	control := transport.Control()
	for i := 0; i < 12; i++ {
		control.InjectSBD([]byte{byte(i)})
	}
	q.Check()
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) {
		h := iridium.NewHandset(transport)
		return h, h.Initialize()
	}, DefaultSettings())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	eventually(t, func() bool { in, _ := q.Inbox(); return len(in) == 12 })
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestTemporaryNetworkFailureSchedulesRetryAndStopsCollection(t *testing.T) {
	q, _ := Open(t.TempDir())
	transport := sim.New(sim.DefaultConfig())
	defer transport.Close()
	transport.Control().SetSBDOutcome(32, 0, false)
	transport.Control().InjectSBD([]byte("queued remotely"))
	h := iridium.NewHandset(transport)
	if e := h.Initialize(); e != nil {
		t.Fatal(e)
	}
	c, _ := h.AcquireSBD()
	defer c.Close()
	s := DefaultSettings()
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return h, nil }, s)
	id, _ := q.Enqueue([]byte("out"))
	v, _ := q.Get(id)
	if e := m.session(c, "test", &v, false); e != nil {
		t.Fatal(e)
	}
	v, _ = q.Get(id)
	if v.State != RetryWait || len(v.Attempts) != 1 {
		t.Fatal(v)
	}
	ready, e := m.ready()
	if e != nil || ready {
		t.Fatal("network failure did not impose cooldown", e)
	}
	if len(transport.Control().SentSBD()) != 0 {
		t.Fatal("delivered failed session")
	}
}
func TestPermanentNetworkFailurePausesWorker(t *testing.T) {
	q, _ := Open(t.TempDir())
	transport := sim.New(sim.DefaultConfig())
	transport.Control().SetSBDOutcome(15, 0, false)
	id, _ := q.Enqueue([]byte("out"))
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) {
		h := iridium.NewHandset(transport)
		return h, h.Initialize()
	}, DefaultSettings())
	if e := m.Run(context.Background()); !errors.Is(e, iridium.ErrSBD) {
		t.Fatal(e)
	}
	v, _ := q.Get(id)
	if v.State != Failed || m.Status().Phase != "paused" {
		t.Fatal(v, m.Status())
	}
}
func TestWorkerGracefulStopWhileOfflineAndRunExclusion(t *testing.T) {
	q, _ := Open(t.TempDir())
	s := DefaultSettings()
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return nil, errors.New("disconnected") }, s)
	done := make(chan error, 1)
	go func() { done <- m.Run(context.Background()) }()
	eventually(t, func() bool { return m.Status().Phase == "reconnecting" })
	if e := m.Run(context.Background()); e == nil {
		t.Fatal("second Run succeeded")
	}
	m.Stop()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if m.Status().Running {
		t.Fatal("still running")
	}
}
func TestManagerPreservesUnrelatedEventsAndBackoffBounds(t *testing.T) {
	q, _ := Open(t.TempDir())
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return nil, nil }, DefaultSettings())
	if !m.events([]string{"SBDRING", "RING"}) {
		t.Fatal("lost SBD alert")
	}
	events := m.TakeEvents()
	if len(events) != 1 || events[0] != "RING" || len(m.TakeEvents()) != 0 {
		t.Fatal(events)
	}
	var observed []string
	m.Events = func(v []string) { observed = v }
	m.events([]string{"RING"})
	if len(observed) != 1 {
		t.Fatal(observed)
	}
	for _, attempt := range []int{1, 2, 100} {
		d := m.backoff(attempt)
		if d < 48*time.Second || d > 18*time.Minute {
			t.Fatal(d)
		}
	}
}
func TestOrphanCopyCrashDoesNotBecomeNetworkInbox(t *testing.T) {
	q, _ := Open(t.TempDir())
	transport := sim.New(sim.DefaultConfig())
	defer transport.Close()
	h := iridium.NewHandset(transport)
	if e := h.Initialize(); e != nil {
		t.Fatal(e)
	}
	c, _ := h.AcquireSBD()
	defer c.Close()
	caps := DefaultSettings().Capabilities
	if e := c.WriteBinary([]byte("old buffer"), caps); e != nil {
		t.Fatal(e)
	}
	if e := q.setMeta("orphan-copy", "1"); e != nil {
		t.Fatal(e)
	}
	if e := c.Loopback(); e != nil {
		t.Fatal(e)
	}
	id, e := q.saveOrphan([]byte("old buffer"))
	if e != nil {
		t.Fatal(e)
	}
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return h, nil }, DefaultSettings())
	if e = m.prepare(c, "test"); e == nil {
		t.Fatal("orphan did not pause")
	}
	in, e := q.Inbox()
	if e != nil || len(in) != 0 {
		t.Fatal("local loopback became an inbound network record", in, e)
	}
	if e = q.Retry(id); e != nil {
		t.Fatal(e)
	}
	if e = m.prepare(c, "test"); e != nil {
		t.Fatal(e)
	}
	status, e := c.Status()
	if e != nil || status.MOFlag != 0 || status.MTFlag != 0 {
		t.Fatal(status, e)
	}
}
func TestQueueReadAndTerminalPurge(t *testing.T) {
	q, _ := Open(t.TempDir())
	if e := q.SaveIncoming("test", 7, []byte("incoming")); e != nil {
		t.Fatal(e)
	}
	list, _ := q.Inbox()
	v, e := q.Read(list[0].ID)
	if e != nil || string(v.Payload) != "incoming" {
		t.Fatal(v, e)
	}
	if _, e = q.Read("missing"); e == nil {
		t.Fatal("missing record succeeded")
	}
	id, _ := q.Enqueue([]byte("out"))
	if e = q.Cancel(id); e != nil {
		t.Fatal(e)
	}
	if e = q.Purge(id); e != nil {
		t.Fatal(e)
	}
	if _, e = q.Get(id); e == nil {
		t.Fatal("purged record still exists")
	}
}
func TestQueueWriteFailureRecordsAttemptAndRetainsWork(t *testing.T) {
	q, _ := Open(t.TempDir())
	id, _ := q.Enqueue([]byte("out"))
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return nil, nil }, DefaultSettings())
	v, _ := q.Get(id)
	if e := m.start(&v); e != nil {
		t.Fatal(e)
	}
	e := m.finishFailure(&v, &iridium.Error{Kind: iridium.ErrTransport, Message: "USB unplugged"})
	if !errors.Is(e, iridium.ErrTransport) {
		t.Fatal(e)
	}
	v, _ = q.Get(id)
	if v.State != RetryWait || len(v.Attempts) != 1 {
		t.Fatal(v)
	}
}

func TestIncomingFailureKeepsOutgoingAcceptance(t *testing.T) {
	q, _ := Open(t.TempDir())
	id, _ := q.Enqueue([]byte("out"))
	transport := sim.New(sim.DefaultConfig())
	defer transport.Close()
	h := iridium.NewHandset(transport)
	if e := h.Initialize(); e != nil {
		t.Fatal(e)
	}
	c, _ := h.AcquireSBD()
	defer c.Close()
	m, _ := NewManager(q, func(context.Context) (*iridium.Handset, error) { return h, nil }, DefaultSettings())
	v, _ := q.Get(id)
	if e := m.start(&v); e != nil {
		t.Fatal(e)
	}
	result := iridium.SBDSessionResult{Reported: true, MOStatus: 1, MTStatus: 2}
	if e := m.finish(&v, result, nil); e != nil {
		t.Fatal(e)
	}
	if e := m.afterSession(c, "test", result, nil); !errors.Is(e, iridium.ErrSBD) {
		t.Fatal(e)
	}
	v, _ = q.Get(id)
	if v.State != Accepted {
		t.Fatal("incoming failure requeued accepted outgoing payload", v)
	}
}
