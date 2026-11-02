package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/sbd"
)

// SBDSettings configures message sizes, session deadlines and managed retries.
// PollIntervalMillis zero disables potentially billable timed mailbox checks.
type SBDSettings struct {
	// ReadFinalResult selects the voice handset trailing-OK binary format.
	ReadFinalResult                                                           bool
	MaxMO, MaxMT, MaxAttempts                                                 int
	SessionTimeoutMillis, PollIntervalMillis, RetryBaseMillis, RetryMaxMillis int64
}

// NewSBDSettings returns voice-terminal defaults with timed polling disabled.
func NewSBDSettings() *SBDSettings {
	s := sbd.DefaultSettings()
	return &SBDSettings{true, s.Capabilities.MaxMO, s.Capabilities.MaxMT, s.MaxAttempts, s.SessionTimeout.Milliseconds(), 0, s.RetryBase.Milliseconds(), s.RetryMax.Milliseconds()}
}
func (s *SBDSettings) core() sbd.Settings {
	return sbd.Settings{Capabilities: iridium.SBDCapabilities{ReadFinalResult: s.ReadFinalResult, MaxMO: s.MaxMO, MaxMT: s.MaxMT, RingAlerts: true}, MaxAttempts: s.MaxAttempts, SessionTimeout: sbdMillis(s.SessionTimeoutMillis), PollInterval: sbdMillis(s.PollIntervalMillis), RetryBase: sbdMillis(s.RetryBaseMillis), RetryMax: sbdMillis(s.RetryMaxMillis)}
}
func sbdMillis(n int64) time.Duration {
	if n > int64((1<<63-1)/time.Millisecond) {
		return time.Duration(1<<63 - 1)
	}
	if n < 0 {
		return -1
	}
	return time.Duration(n) * time.Millisecond
}

// SBDResult contains a session outcome and an optional incoming payload.
// Accepted means gateway acceptance, not destination-application receipt.
type SBDResult struct {
	MOStatus, MOMSN, MTStatus, MTMSN, MTLength, Queued int
	Accepted                                           bool
	Payload                                            []byte
}

// ExchangeSBD conducts one atomic exchange. A nil payload requests receive only.
// Incoming bytes remain on the handset until ClearSBDBuffer is called.
func (h *IridiumHandset) ExchangeSBD(payload []byte, settings *SBDSettings, cancel *Cancellation) (result *SBDResult, err error) {
	defer recoverPanic(&err)
	if _, e := made(h.handset, "IridiumHandset"); e != nil {
		return nil, e
	}
	if settings == nil {
		return nil, nilArgument("settings")
	}
	return withHandset(h, func(core *iridium.Handset) (*SBDResult, error) {
		s := iridium.DefaultSBDSessionSettings()
		s.Timeout = sbdMillis(settings.SessionTimeoutMillis)
		if cancel != nil {
			s.Cancellation = &cancel.core
		}
		x, e := core.ExchangeSBD(payload, settings.core().Capabilities, s)
		r := x.Result
		return &SBDResult{r.MOStatus, r.MOMSN, r.MTStatus, r.MTMSN, r.MTLength, r.Queued, e == nil && r.Accepted(), x.Payload}, e
	})
}

// SBDStatusJSON reports handset buffer state without transmitting.
func (h *IridiumHandset) SBDStatusJSON() (result string, err error) {
	defer recoverPanic(&err)
	return withHandset(h, func(core *iridium.Handset) (string, error) {
		v, e := core.SBDStatus()
		if e != nil {
			return "", e
		}
		p, e := json.Marshal(v)
		return string(p), e
	})
}

// ClearSBDBuffer clears 0 outgoing, 1 incoming, or 2 both local buffers.
func (h *IridiumHandset) ClearSBDBuffer(which int) (err error) {
	defer recoverPanic(&err)
	return h.perform(func(core *iridium.Handset) error {
		c, e := core.AcquireSBD()
		if e != nil {
			return e
		}
		defer c.Close()
		return c.Clear(which)
	})
}

// SBDQueue stores messages in a directory supplied by the host application.
type SBDQueue struct{ core *sbd.Queue }

// OpenSBDQueue opens an application-private directory without opening USB.
func OpenSBDQueue(dir string) (queue *SBDQueue, err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	q, e := sbd.Open(dir)
	if e != nil {
		return nil, e
	}
	return &SBDQueue{q}, nil
}

// Enqueue commits a payload before returning its stable local ID.
func (q *SBDQueue) Enqueue(payload []byte) (id string, err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	return q.core.Enqueue(payload)
}

// OutboxJSON returns records and attempt history; payloads are base64 in JSON.
func (q *SBDQueue) OutboxJSON() (result string, err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	v, e := q.core.Outbox()
	if e != nil {
		return "", e
	}
	p, e := json.Marshal(v)
	return string(p), e
}

// InboxJSON returns received records; payloads are base64 in JSON.
func (q *SBDQueue) InboxJSON() (result string, err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	v, e := q.core.Inbox()
	if e != nil {
		return "", e
	}
	p, e := json.Marshal(v)
	return string(p), e
}

// Read returns incoming payload bytes without acknowledging the message.
func (q *SBDQueue) Read(id string) (payload []byte, err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	v, e := q.core.Read(id)
	return v.Payload, e
}

// Ack removes a consumed inbox record.
func (q *SBDQueue) Ack(id string) (err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	return q.core.Ack(id)
}

// Retry explicitly requeues an outbound message, permitting duplicates.
func (q *SBDQueue) Retry(id string) (err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	return q.core.Retry(id)
}

// Cancel prevents future attempts of a message not currently in flight.
func (q *SBDQueue) Cancel(id string) (err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	return q.core.Cancel(id)
}

// Purge deletes a terminal outbox record.
func (q *SBDQueue) Purge(id string) (err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	return q.core.Purge(id)
}

// Check requests one mailbox collection pass from the worker.
func (q *SBDQueue) Check() (err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	return q.core.Check()
}

// SBDTransportFactory supplies a fresh USB connection for each reconnect.
// The host controls USB permissions and must not lend the port elsewhere.
type SBDTransportFactory interface {
	Open() (ByteTransport, error)
	// Release closes a transport after the worker is finished with it.
	Release(ByteTransport) error
}

// SBDManager is a host-controlled foreground worker. Run blocks; call it
// off the UI thread. Stop is safe from another thread and waits no session.
type SBDManager struct{ core *sbd.Manager }

// NewSBDManager creates a worker using the host's USB transport factory.
func NewSBDManager(q *SBDQueue, factory SBDTransportFactory, settings *SBDSettings) (manager *SBDManager, err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	if factory == nil {
		return nil, nilArgument("factory")
	}
	return makeSBDManager(q, settings, func(context.Context) (*iridium.Handset, error) {
		t, e := factory.Open()
		if e != nil {
			return nil, e
		}
		if t == nil {
			return nil, nilArgument("transport")
		}
		h := iridium.NewHandset(&sbdHostTransport{foreignTransport: newForeignTransport(t), factory: factory})
		if e = h.Initialize(); e != nil {
			_ = h.Close()
			return nil, e
		}
		return h, nil
	})
}

func makeSBDManager(q *SBDQueue, s *SBDSettings, connect sbd.Connector) (*SBDManager, error) {
	if q == nil {
		return nil, nilArgument("queue")
	}
	if s == nil {
		return nil, nilArgument("settings")
	}
	m, e := sbd.NewManager(q.core, connect, s.core())
	if e != nil {
		return nil, e
	}
	return &SBDManager{m}, nil
}

// Run starts the worker until Stop or a permanent error. Uncertain sends retry.
func (m *SBDManager) Run() (err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	return m.core.Run(context.Background())
}

// Stop requests graceful shutdown after the active satellite session.
func (m *SBDManager) Stop() { defer survivePanic("SBDManager.Stop"); m.core.Stop() }

// StatusJSON returns worker state without payloads or subscriber identifiers.
func (m *SBDManager) StatusJSON() (result string, err error) {
	defer recoverPanic(&err)
	p, e := json.Marshal(m.core.Status())
	return string(p), e
}

// SBDMessage is an outbound queue snapshot. Payload is copied from storage.
type SBDMessage struct {
	ID, State, Created, NextAttempt string
	Payload                         []byte
	AttemptCount                    int
}

// SBDMessageList is a gomobile-compatible list of outbox snapshots.
type SBDMessageList struct{ items []SBDMessage }

func (l *SBDMessageList) Len() int { return len(l.items) }
func (l *SBDMessageList) Get(index int) *SBDMessage {
	if index < 0 || index >= len(l.items) {
		return nil
	}
	v := l.items[index]
	v.Payload = append([]byte(nil), v.Payload...)
	return &v
}

// Outbox lists outbound records without opening USB.
func (q *SBDQueue) Outbox() (list *SBDMessageList, err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	v, e := q.core.Outbox()
	if e != nil {
		return nil, e
	}
	list = &SBDMessageList{}
	for _, m := range v {
		list.items = append(list.items, SBDMessage{m.ID, string(m.State), m.Created.Format(time.RFC3339), m.NextAttempt.Format(time.RFC3339), m.Payload, len(m.Attempts)})
	}
	return list, nil
}

// SBDIncoming is a received message retained until acknowledged.
type SBDIncoming struct {
	ID, IMEI, Received string
	Sequence           int
	Payload            []byte
}

// SBDIncomingList is a gomobile-compatible list of incoming messages.
type SBDIncomingList struct{ items []SBDIncoming }

func (l *SBDIncomingList) Len() int { return len(l.items) }
func (l *SBDIncomingList) Get(index int) *SBDIncoming {
	if index < 0 || index >= len(l.items) {
		return nil
	}
	v := l.items[index]
	v.Payload = append([]byte(nil), v.Payload...)
	return &v
}

// Inbox lists received records without acknowledging them.
func (q *SBDQueue) Inbox() (list *SBDIncomingList, err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	v, e := q.core.Inbox()
	if e != nil {
		return nil, e
	}
	list = &SBDIncomingList{}
	for _, m := range v {
		list.items = append(list.items, SBDIncoming{m.ID, m.IMEI, m.Received.Format(time.RFC3339), m.Sequence, m.Payload})
	}
	return list, nil
}

// WriteSBDBinary loads a local outgoing buffer without a satellite session.
func (h *IridiumHandset) WriteSBDBinary(payload []byte, settings *SBDSettings) (err error) {
	defer recoverPanic(&err)
	if _, e := made(h.handset, "IridiumHandset"); e != nil {
		return e
	}
	if settings == nil {
		return nilArgument("settings")
	}
	return h.perform(func(core *iridium.Handset) error {
		c, e := core.AcquireSBD()
		if e != nil {
			return e
		}
		defer c.Close()
		return c.WriteBinary(payload, settings.core().Capabilities)
	})
}

// ReadSBDBinary reads and validates the incoming buffer without clearing it.
func (h *IridiumHandset) ReadSBDBinary(settings *SBDSettings) (payload []byte, err error) {
	defer recoverPanic(&err)
	if _, e := made(h.handset, "IridiumHandset"); e != nil {
		return nil, e
	}
	if settings == nil {
		return nil, nilArgument("settings")
	}
	return withHandset(h, func(core *iridium.Handset) ([]byte, error) {
		c, e := core.AcquireSBD()
		if e != nil {
			return nil, e
		}
		defer c.Close()
		return c.ReadBinary(settings.core().Capabilities)
	})
}

// LoopbackSBD copies the outgoing buffer to incoming without radio use.
func (h *IridiumHandset) LoopbackSBD() (err error) {
	defer recoverPanic(&err)
	return h.perform(func(core *iridium.Handset) error {
		c, e := core.AcquireSBD()
		if e != nil {
			return e
		}
		defer c.Close()
		return c.Loopback()
	})
}

// Managed host transports are released between reconnect attempts, unlike a
// borrowed ByteTransport passed to NewIridiumHandset.
type sbdHostTransport struct {
	*foreignTransport
	factory SBDTransportFactory
	once    sync.Once
	err     error
}

func (t *sbdHostTransport) Close() error {
	t.once.Do(func() { t.err = t.factory.Release(t.transport) })
	return t.err
}

// TakeEvents returns unrelated unsolicited events retained by the worker.
func (m *SBDManager) TakeEvents() *StringList {
	defer survivePanic("SBDManager.TakeEvents")
	return &StringList{items: m.core.TakeEvents()}
}

// WriteSBDText loads at most 120 printable bytes without transmitting them.
func (h *IridiumHandset) WriteSBDText(text string) (err error) {
	defer recoverPanic(&err)
	return h.perform(func(core *iridium.Handset) error {
		c, e := core.AcquireSBD()
		if e != nil {
			return e
		}
		defer c.Close()
		return c.WriteText(text)
	})
}

// SetSBDRingAlerts controls local ring reporting without network registration.
func (h *IridiumHandset) SetSBDRingAlerts(enabled bool) (err error) {
	defer recoverPanic(&err)
	return h.perform(func(core *iridium.Handset) error {
		c, e := core.AcquireSBD()
		if e != nil {
			return e
		}
		defer c.Close()
		return c.ConfigureRingAlerts(enabled)
	})
}

// SBDRegistration reports local registration state without a satellite session.
func (h *IridiumHandset) SBDRegistration() (status int, err error) {
	defer recoverPanic(&err)
	return withHandset(h, func(core *iridium.Handset) (int, error) {
		c, e := core.AcquireSBD()
		if e != nil {
			return 0, e
		}
		defer c.Close()
		return c.Registration()
	})
}

// RegisterSBD explicitly registers on the network and returns response JSON.
func (h *IridiumHandset) RegisterSBD() (result string, err error) {
	defer recoverPanic(&err)
	return h.sbdRegistrationCommand(true)
}

// DetachSBD explicitly detaches and returns the response as JSON.
func (h *IridiumHandset) DetachSBD() (result string, err error) {
	defer recoverPanic(&err)
	return h.sbdRegistrationCommand(false)
}
func (h *IridiumHandset) sbdRegistrationCommand(register bool) (string, error) {
	return withHandset(h, func(core *iridium.Handset) (string, error) {
		c, e := core.AcquireSBD()
		if e != nil {
			return "", e
		}
		defer c.Close()
		var r iridium.Response
		if register {
			r, e = c.Register()
		} else {
			r, e = c.Detach()
		}
		if e != nil {
			return "", e
		}
		p, e := json.Marshal(r)
		return string(p), e
	})
}

func sbdBoundaryError(err *error) {
	if *err == nil {
		return
	}
	var core *iridium.Error
	var host *SatlinkError
	if errors.As(*err, &core) || errors.As(*err, &host) {
		*err = fromError(*err)
		return
	}
	*err = &SatlinkError{Kind: SatlinkErrorSBD, Reason: (*err).Error()}
}
