package sbd

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/anhydrous99/satlink/iridium"
	bolt "go.etcd.io/bbolt"
)

// Connector opens a ready handset. Run closes each returned handset.
// Connection failures are retried without consuming transmission attempts.
type Connector func(context.Context) (*iridium.Handset, error)

// Settings controls managed messaging. PollInterval zero disables timed polls.
type Settings struct {
	Capabilities                 iridium.SBDCapabilities
	SessionTimeout, PollInterval time.Duration
	MaxAttempts                  int
	RetryBase, RetryMax          time.Duration
}

// DefaultSettings uses voice-terminal limits and ten attempts with jittered
// exponential backoff from one minute to fifteen minutes. Timed polls are off.
func DefaultSettings() Settings {
	return Settings{Capabilities: iridium.SBDCapabilities{ReadFinalResult: true, MaxMO: 1960, MaxMT: 1890, RingAlerts: true}, SessionTimeout: 120 * time.Second, MaxAttempts: 10, RetryBase: time.Minute, RetryMax: 15 * time.Minute}
}

// ManagerStatus is a snapshot without payloads or subscriber identifiers.
type ManagerStatus struct {
	Running            bool
	Phase, LastError   string
	Accepted, Received int
}

// Manager operates one handset and queue. Start it explicitly with Run.
// A manager is reusable after Run returns; concurrent Run calls are refused.
type Manager struct {
	pendingEvents []string
	Queue         *Queue
	Connect       Connector
	Settings      Settings
	Events        func([]string)
	mu            sync.Mutex
	status        ManagerStatus
	cancel        context.CancelFunc
}

// NewManager validates configuration without connecting or transmitting.
func NewManager(q *Queue, connect Connector, s Settings) (*Manager, error) {
	if q == nil || connect == nil {
		return nil, errors.New("queue and connector are required")
	}
	if s.SessionTimeout <= 0 || s.MaxAttempts < 1 || s.RetryBase <= 0 || s.RetryMax < s.RetryBase || s.PollInterval < 0 || (s.PollInterval > 0 && s.PollInterval < time.Minute) {
		return nil, errors.New("invalid SBD worker timing or retry settings")
	}
	if s.Capabilities.MaxMO < 1 || s.Capabilities.MaxMO > 1960 || s.Capabilities.MaxMT < 1 || s.Capabilities.MaxMT > 1960 {
		return nil, errors.New("invalid SBD worker limits")
	}
	return &Manager{Queue: q, Connect: connect, Settings: s}, nil
}

// Status returns the latest worker state.
func (m *Manager) Status() ManagerStatus { m.mu.Lock(); defer m.mu.Unlock(); return m.status }
func (m *Manager) phase(phase string, e error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Phase = phase
	if e != nil {
		m.status.LastError = e.Error()
	}
}

// Stop requests a graceful stop after any satellite session already underway.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}

// Run owns the handset until context cancellation or a permanent failure.
// Uncertain delivery is retried automatically, and can produce duplicates.
func (m *Manager) Run(ctx context.Context) error {
	m.mu.Lock()
	if m.status.Running {
		m.mu.Unlock()
		return errors.New("SBD manager already running")
	}
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.status = ManagerStatus{Running: true, Phase: "starting"}
	m.mu.Unlock()
	defer func() { cancel(); m.mu.Lock(); m.cancel = nil; m.status.Running = false; m.mu.Unlock() }()
	lock, e := m.Queue.LockWorker()
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = m.recoverAttempts(); e != nil {
		return e
	}
	for ctx.Err() == nil {
		m.phase("connecting", nil)
		h, e := m.Connect(ctx)
		if e != nil {
			m.phase("reconnecting", e)
			if !wait(ctx, m.Settings.RetryBase) {
				return nil
			}
			continue
		}
		e = m.connected(ctx, h)
		_ = h.Close()
		if ctx.Err() != nil {
			return nil
		}
		if e == nil {
			return nil
		}
		if errors.Is(e, iridium.ErrTransport) || errors.Is(e, iridium.ErrSBDUncertain) {
			m.phase("reconnecting", e)
			if !wait(ctx, m.Settings.RetryBase) {
				return nil
			}
			continue
		}
		m.phase("paused", e)
		return e
	}
	return nil
}
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func (m *Manager) recoverAttempts() error {
	return m.Queue.update(func(tx *bolt.Tx) error {
		messages, e := records[Message](tx, "outbox")
		if e != nil {
			return e
		}
		for _, v := range messages {
			if v.State != InFlight {
				continue
			}
			v.State = RetryWait
			if len(v.Attempts)-v.RetryStart >= m.Settings.MaxAttempts {
				v.State = Failed
			}
			v.NextAttempt = time.Now().Add(m.backoff(len(v.Attempts)))
			if len(v.Attempts) > 0 {
				a := &v.Attempts[len(v.Attempts)-1]
				a.Uncertain = true
				a.Error = "worker stopped before result was committed"
				a.Finished = time.Now().UTC()
			}
			if e = put(tx, "outbox", v.ID, v); e != nil {
				return e
			}
		}
		return nil
	})
}
func (m *Manager) connected(ctx context.Context, h *iridium.Handset) error {
	c, e := h.AcquireSBD()
	if e != nil {
		return e
	}
	defer c.Close()
	info, e := c.Info()
	if e != nil {
		return e
	}
	if _, e = iridium.SBDProfile(info.Model); e != nil {
		return e
	}
	if e = m.Queue.bind(info.IMEI); e != nil {
		return e
	}
	if e = m.prepare(c, info.IMEI); e != nil {
		return e
	}
	if m.Settings.Capabilities.RingAlerts {
		if e = c.ConfigureRingAlerts(true); e != nil {
			return e
		}
	}
	return m.work(ctx, c, info.IMEI)
}
func (m *Manager) work(ctx context.Context, c *iridium.SBDClient, imei string) error {
	initial, e := c.Status()
	if e != nil {
		return e
	}
	return m.schedule(ctx, c, imei, initial)
}
func (m *Manager) schedule(ctx context.Context, c *iridium.SBDClient, imei string, initial iridium.SBDStatus) error {
	nextPoll := time.Now().Add(m.Settings.PollInterval)
	alert := initial.RingAlert != 0
	collect := initial.Queued > 0
	for ctx.Err() == nil {
		m.phase("idle", nil)
		check, e := m.Queue.meta("check")
		if e != nil {
			return e
		}
		message, e := m.next()
		if e != nil {
			return e
		}
		poll := m.Settings.PollInterval > 0 && !time.Now().Before(nextPoll)
		ready, e := m.ready()
		if e != nil {
			return e
		}
		if ready && (message != nil || alert || collect || check != "" || poll) {
			collect, e = m.collection(ctx, c, imei, message, alert, check)
			if e != nil {
				return e
			}
			alert = false
			nextPoll = time.Now().Add(m.Settings.PollInterval)
			continue
		}
		events, e := c.PollEvents(200 * time.Millisecond)
		if e != nil {
			return e
		}
		alert = m.events(events) || alert
	}
	return nil
}
func (m *Manager) prepare(c *iridium.SBDClient, imei string) error {
	status, e := c.Status()
	if e != nil {
		return e
	}
	copying, e := m.Queue.meta("orphan-copy")
	if e != nil {
		return e
	}
	if status.MTFlag != 0 && copying != "1" {
		if e = m.receive(c, imei, status.MTMSN); e != nil {
			return e
		}
	}
	if status.MOFlag == 0 {
		return m.resetBufferOwnership()
	}
	owned, e := m.Queue.meta("owned")
	if e != nil {
		return e
	}
	if owned == "1" {
		return m.clearMO(c)
	}
	return m.preserveOrphan(c)
}
func (m *Manager) preserveOrphan(c *iridium.SBDClient) error {
	orphan, e := m.Queue.meta("orphan")
	if e != nil {
		return e
	}
	if orphan != "" {
		v, e := m.Queue.Get(orphan)
		if e != nil {
			return e
		}
		if e = m.finishOrphanCopy(c); e != nil {
			return e
		}
		if v.State == Orphaned {
			return errors.New("orphaned handset buffer: retry to adopt or cancel to discard " + orphan)
		}
		return m.clearMO(c)
	}
	if e = m.Queue.setMeta("orphan-copy", "1"); e != nil {
		return e
	}
	if e = c.Loopback(); e != nil {
		return e
	}
	caps := m.Settings.Capabilities
	caps.MaxMT = caps.MaxMO
	p, e := c.ReadBinary(caps)
	if e != nil {
		return e
	}
	id, e := m.Queue.saveOrphan(p)
	if e != nil {
		return e
	}
	if e = m.finishOrphanCopy(c); e != nil {
		return e
	}
	return errors.New("orphaned handset buffer saved; retry to adopt or cancel to discard " + id)
}
func (m *Manager) clearMO(c *iridium.SBDClient) error {
	if e := c.Clear(0); e != nil {
		return e
	}
	return m.resetBufferOwnership()
}
func (m *Manager) receive(c *iridium.SBDClient, imei string, sequence int) error {
	p, e := c.ReadBinary(m.Settings.Capabilities)
	if e != nil {
		return e
	}
	e = m.Queue.SaveIncoming(imei, sequence, p)
	if e != nil {
		return e
	}
	if e = c.Clear(1); e != nil {
		return e
	}
	m.mu.Lock()
	m.status.Received++
	m.mu.Unlock()
	return nil
}
func (m *Manager) next() (*Message, error) {
	list, e := m.Queue.Outbox()
	if e != nil {
		return nil, e
	}
	for _, v := range list {
		if (v.State == Queued || v.State == RetryWait) && !v.NextAttempt.After(time.Now()) {
			return &v, nil
		}
	}
	return nil, nil
}
func (m *Manager) pass(ctx context.Context, c *iridium.SBDClient, imei string, v *Message, alert bool) error {
	for n := 0; n < 10 && ctx.Err() == nil; n++ {
		if e := m.session(c, imei, v, alert); e != nil {
			return e
		}
		v = nil
		alert = false
		status, e := c.Status()
		if e != nil {
			return e
		}
		ready, e := m.ready()
		if e != nil {
			return e
		}
		if status.Queued == 0 || !ready {
			return nil
		}
	}
	return nil
}
func (m *Manager) session(c *iridium.SBDClient, imei string, v *Message, alert bool) error {
	m.phase("session", nil)
	if v != nil {
		if e := m.loadOutgoing(c, v); e != nil {
			if errors.Is(e, errSkipped) {
				return nil
			}
			return e
		}
	} else if e := c.Clear(0); e != nil {
		return e
	}
	settings := iridium.DefaultSBDSessionSettings()
	settings.Timeout = m.Settings.SessionTimeout
	settings.Alert = alert
	result, e := c.Session(settings)
	if v != nil {
		if err := m.finish(v, result, e); err != nil {
			return err
		}
	}
	if errors.Is(e, iridium.ErrSBDUncertain) {
		return e
	}
	if errors.Is(e, iridium.ErrTransport) {
		return e
	}
	return m.afterSession(c, imei, result, e)
}
func (m *Manager) afterSession(c *iridium.SBDClient, imei string, result iridium.SBDSessionResult, e error) error {
	if result.MTStatus == 1 {
		if err := m.receive(c, imei, result.MTMSN); err != nil {
			return err
		}
	}
	if err := m.clearMO(c); err != nil {
		return err
	}
	if result.MTStatus == 2 && e == nil {
		return &iridium.Error{Kind: iridium.ErrSBD, Code: "MT2", Message: "incoming transfer failed; outgoing acceptance is recorded"}
	}
	if e != nil && result.Retryable() {
		delay := m.Settings.RetryBase
		if result.MOStatus == 36 || result.MOStatus == 38 {
			delay = max(delay, 3*time.Minute)
		}
		if err := m.Queue.setMeta("network-after", time.Now().Add(delay).UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if e != nil && !result.Retryable() {
		return e
	}
	return nil
}
func (m *Manager) start(v *Message) error {
	return m.Queue.update(func(tx *bolt.Tx) error {
		current, e := readMessage(tx, v.ID)
		if e != nil {
			return e
		}
		if current.State != Queued && current.State != RetryWait {
			return errors.New("message changed before attempt")
		}
		if len(current.Payload) > m.Settings.Capabilities.MaxMO {
			current.State = Failed
			return put(tx, "outbox", current.ID, current)
		}
		current.State = InFlight
		current.Attempts = append(current.Attempts, Attempt{Started: time.Now().UTC()})
		*v = current
		if e = put(tx, "outbox", v.ID, current); e != nil {
			return e
		}
		return tx.Bucket([]byte("meta")).Put([]byte("owned"), []byte("1"))
	})
}
func (m *Manager) finishFailure(v *Message, e error) error {
	if err := m.finish(v, iridium.SBDSessionResult{}, e); err != nil {
		return err
	}
	return e
}
func (m *Manager) finish(v *Message, result iridium.SBDSessionResult, outcome error) error {
	return m.Queue.change(v.ID, func(current *Message) error {
		if len(current.Attempts) == 0 {
			return errors.New("missing SBD attempt")
		}
		a := &current.Attempts[len(current.Attempts)-1]
		a.Finished = time.Now().UTC()
		a.Result = result
		if outcome == nil {
			current.State = Accepted
			m.mu.Lock()
			m.status.Accepted++
			m.mu.Unlock()
			return nil
		}
		a.Error = outcome.Error()
		a.Uncertain = errors.Is(outcome, iridium.ErrSBDUncertain)
		current.State = Failed
		if (a.Uncertain || result.Retryable() || errors.Is(outcome, iridium.ErrTransport)) && len(current.Attempts)-current.RetryStart < m.Settings.MaxAttempts {
			current.State = RetryWait
			delay := m.backoff(len(current.Attempts) - current.RetryStart)
			if result.MOStatus == 36 || result.MOStatus == 38 {
				delay = max(delay, 3*time.Minute)
			}
			current.NextAttempt = time.Now().Add(delay)
		}
		return nil
	})
}
func (m *Manager) backoff(attempt int) time.Duration {
	d := m.Settings.RetryBase
	for i := 1; i < attempt && d < m.Settings.RetryMax; i++ {
		if d > m.Settings.RetryMax/2 {
			d = m.Settings.RetryMax
		} else {
			d *= 2
		}
	}
	spread := int64(d / 5)
	delta := time.Duration(rand.Int64N(2*spread+1) - spread)
	if delta > 0 && d > time.Duration(math.MaxInt64)-delta {
		return time.Duration(math.MaxInt64)
	}
	return d + delta
}

func (m *Manager) clearCheck(check string) error {
	return m.Queue.update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("meta"))
		if string(b.Get([]byte("check"))) == check {
			return b.Delete([]byte("check"))
		}
		return nil
	})
}
func (m *Manager) events(events []string) bool {
	alert := false
	var other []string
	for _, event := range events {
		if event == "SBDRING" {
			alert = true
		} else {
			other = append(other, event)
		}
	}
	if len(other) > 0 {
		if m.Events != nil {
			m.Events(other)
		} else {
			m.mu.Lock()
			m.pendingEvents = append(m.pendingEvents, other...)
			m.mu.Unlock()
		}
	}
	return alert
}

func (m *Manager) resetBufferOwnership() error {
	return m.Queue.update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("meta"))
		for _, key := range []string{"owned", "orphan", "orphan-copy"} {
			if e := b.Delete([]byte(key)); e != nil {
				return e
			}
		}
		return nil
	})
}

func (m *Manager) ready() (bool, error) {
	s, e := m.Queue.meta("network-after")
	if e != nil || s == "" {
		return s == "", e
	}
	at, e := time.Parse(time.RFC3339Nano, s)
	return !time.Now().Before(at), e
}

// TakeEvents returns unrelated unsolicited events retained when Events is nil.
func (m *Manager) TakeEvents() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.pendingEvents
	m.pendingEvents = nil
	return v
}

func (m *Manager) collection(ctx context.Context, c *iridium.SBDClient, imei string, message *Message, alert bool, check string) (bool, error) {
	if e := m.pass(ctx, c, imei, message, alert); e != nil {
		return false, e
	}
	pending, e := c.Status()
	if e != nil {
		return false, e
	}
	ready, e := m.ready()
	if e != nil {
		return false, e
	}
	collect := pending.Queued > 0 || !ready
	if check != "" && !collect {
		if e = m.clearCheck(check); e != nil {
			return false, e
		}
	}
	return collect, nil
}

var errSkipped = errors.New("message skipped")

func (m *Manager) loadOutgoing(c *iridium.SBDClient, v *Message) error {
	if len(v.Payload) > m.Settings.Capabilities.MaxMO {
		if e := m.Queue.change(v.ID, func(v *Message) error { v.State = Failed; return nil }); e != nil {
			return e
		}
		return errSkipped
	}
	if e := m.start(v); e != nil {
		return e
	}
	if e := c.WriteBinary(v.Payload, m.Settings.Capabilities); e != nil {
		return m.finishFailure(v, e)
	}
	return nil
}

func (m *Manager) finishOrphanCopy(c *iridium.SBDClient) error {
	copying, e := m.Queue.meta("orphan-copy")
	if e != nil {
		return e
	}
	if copying == "1" {
		if e = c.Clear(1); e != nil {
			return e
		}
		return m.Queue.setMeta("orphan-copy", "")
	}
	return nil
}
