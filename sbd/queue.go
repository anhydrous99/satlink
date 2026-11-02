// Package sbd provides durable handset-side Short Burst Data messaging.
// Gateway acceptance does not imply delivery to the destination application.
// Retries can duplicate messages; include an application ID in your payload
// when the receiver needs duplicate suppression.
package sbd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/anhydrous99/satlink/iridium"
	bolt "go.etcd.io/bbolt"
)

// State is a durable outbound message state.
type State string

const (
	Queued    State = "queued"
	InFlight  State = "in-flight"
	RetryWait State = "retry-wait"
	Accepted  State = "accepted"
	Failed    State = "failed"
	Cancelled State = "cancelled"
	Orphaned  State = "orphaned"
)

// Attempt records an actual transmission attempt. Uncertain attempts may
// have delivered and will be retried automatically by the manager.
type Attempt struct {
	Started   time.Time
	Finished  time.Time
	Uncertain bool
	Error     string
	Result    iridium.SBDSessionResult
}

// Message is a durable outbox record. Payload bytes are never modified.
type Message struct {
	ID          string
	Payload     []byte
	State       State
	Created     time.Time
	NextAttempt time.Time
	RetryStart  int
	Attempts    []Attempt
}

// Incoming is an inbox record. Ack removes it; reading does not.
type Incoming struct {
	ID, IMEI string
	Sequence int
	Payload  []byte
	Received time.Time
}

// Queue stores records in dir. It opens its database only during operations,
// so independent processes can enqueue while a worker is using the modem.
type Queue struct{ Dir string }

// Open creates or validates a queue. The directory must be private to the app.
func Open(dir string) (*Queue, error) {
	if dir == "" {
		return nil, errors.New("empty SBD queue directory")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	q := &Queue{Dir: dir}
	e := q.update(func(*bolt.Tx) error { return nil })
	return q, e
}
func (q *Queue) update(f func(*bolt.Tx) error) error {
	db, e := bolt.Open(filepath.Join(q.Dir, "queue.db"), 0600, &bolt.Options{Timeout: 5 * time.Second})
	if e != nil {
		return e
	}
	defer db.Close()
	return db.Update(func(tx *bolt.Tx) error {
		meta, e := tx.CreateBucketIfNotExists([]byte("meta"))
		if e != nil {
			return e
		}
		version := string(meta.Get([]byte("version")))
		if version != "" && version != "1" {
			return errors.New("unsupported SBD queue schema")
		}
		if e = meta.Put([]byte("version"), []byte("1")); e != nil {
			return e
		}
		for _, name := range []string{"outbox", "inbox"} {
			if _, e = tx.CreateBucketIfNotExists([]byte(name)); e != nil {
				return e
			}
		}
		return f(tx)
	})
}
func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func put(tx *bolt.Tx, bucket, key string, v any) error {
	p, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return tx.Bucket([]byte(bucket)).Put([]byte(key), p)
}
func readMessage(tx *bolt.Tx, id string) (Message, error) {
	var m Message
	p := tx.Bucket([]byte("outbox")).Get([]byte(id))
	if p == nil {
		return m, errors.New("SBD message not found")
	}
	e := json.Unmarshal(p, &m)
	return m, e
}
func records[T any](tx *bolt.Tx, bucket string) ([]T, error) {
	out := []T{}
	e := tx.Bucket([]byte(bucket)).ForEach(func(_, p []byte) error {
		var v T
		if e := json.Unmarshal(p, &v); e != nil {
			return e
		}
		out = append(out, v)
		return nil
	})
	return out, e
}
func capacity(tx *bolt.Tx, extra int) error {
	count, total := 0, extra
	for _, name := range []string{"outbox", "inbox"} {
		e := tx.Bucket([]byte(name)).ForEach(func(_, p []byte) error {
			count++
			var v struct{ Payload []byte }
			if e := json.Unmarshal(p, &v); e != nil {
				return e
			}
			total += len(v.Payload)
			return nil
		})
		if e != nil {
			return e
		}
	}
	if count >= 1000 || total > 16*1024*1024 {
		return errors.New("SBD queue capacity exceeded; acknowledge or purge records")
	}
	return nil
}

// Enqueue commits a non-empty payload before returning its stable local ID.
func (q *Queue) Enqueue(payload []byte) (string, error) { return q.enqueue(payload, Queued) }
func (q *Queue) enqueue(payload []byte, state State) (string, error) {
	if len(payload) < 1 || len(payload) > 1960 {
		return "", errors.New("SBD payload must contain 1 to 1960 bytes")
	}
	m := Message{ID: newID(), Payload: append([]byte(nil), payload...), State: state, Created: time.Now().UTC()}
	e := q.update(func(tx *bolt.Tx) error {
		if e := capacity(tx, len(payload)); e != nil {
			return e
		}
		return put(tx, "outbox", m.ID, m)
	})
	return m.ID, e
}

// Outbox lists all records, including terminal records, oldest first.
func (q *Queue) Outbox() (out []Message, err error) {
	err = q.view(func(tx *bolt.Tx) error { var e error; out, e = records[Message](tx, "outbox"); return e })
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return
}

// Get returns a message including its attempt history.
func (q *Queue) Get(id string) (m Message, err error) {
	err = q.view(func(tx *bolt.Tx) error { var e error; m, e = readMessage(tx, id); return e })
	return
}
func (q *Queue) change(id string, f func(*Message) error) error {
	return q.update(func(tx *bolt.Tx) error {
		m, e := readMessage(tx, id)
		if e != nil {
			return e
		}
		if e = f(&m); e != nil {
			return e
		}
		return put(tx, "outbox", id, m)
	})
}

// Cancel prevents future attempts. An active session cannot be unsent.
func (q *Queue) Cancel(id string) error {
	return q.change(id, func(m *Message) error {
		if m.State == InFlight || m.State == Accepted {
			return errors.New("active or accepted message cannot be cancelled")
		}
		m.State = Cancelled
		return nil
	})
}

// Retry explicitly requeues a terminal or orphaned message with a fresh retry
// budget. Attempt history remains available.
func (q *Queue) Retry(id string) error {
	return q.change(id, func(m *Message) error {
		if m.State == InFlight {
			return errors.New("message is in flight")
		}
		m.State = Queued
		m.RetryStart = len(m.Attempts)
		m.NextAttempt = time.Time{}
		return nil
	})
}

// Purge removes a terminal record. It never removes pending work.
func (q *Queue) Purge(id string) error {
	return q.update(func(tx *bolt.Tx) error {
		m, e := readMessage(tx, id)
		if e != nil {
			return e
		}
		if m.State != Accepted && m.State != Failed && m.State != Cancelled {
			return errors.New("only terminal records can be purged")
		}
		if string(tx.Bucket([]byte("meta")).Get([]byte("orphan"))) == id {
			return errors.New("restart the worker to reconcile this orphan before purging")
		}
		return tx.Bucket([]byte("outbox")).Delete([]byte(id))
	})
}

// Inbox lists received messages. Payloads remain until Ack.
func (q *Queue) Inbox() (out []Incoming, err error) {
	err = q.view(func(tx *bolt.Tx) error { var e error; out, e = records[Incoming](tx, "inbox"); return e })
	sort.Slice(out, func(i, j int) bool { return out[i].Received.Before(out[j].Received) })
	return
}

// Read returns one incoming message without acknowledging it.
func (q *Queue) Read(id string) (v Incoming, err error) {
	err = q.view(func(tx *bolt.Tx) error {
		p := tx.Bucket([]byte("inbox")).Get([]byte(id))
		if p == nil {
			return errors.New("incoming SBD message not found")
		}
		return json.Unmarshal(p, &v)
	})
	return
}

// Ack removes an inbox record after the application has consumed it.
func (q *Queue) Ack(id string) error {
	return q.update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("inbox")).Delete([]byte(id)) })
}

// Check requests one mailbox collection pass, durable across worker restarts.
func (q *Queue) Check() error { return q.setMeta("check", newID()) }
func (q *Queue) setMeta(key, value string) error {
	return q.update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("meta")).Put([]byte(key), []byte(value)) })
}
func (q *Queue) meta(key string) (value string, err error) {
	err = q.view(func(tx *bolt.Tx) error { value = string(tx.Bucket([]byte("meta")).Get([]byte(key))); return nil })
	return
}
func (q *Queue) bind(imei string) error {
	if imei == "" {
		return errors.New("handset has no IMEI")
	}
	return q.update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("meta"))
		old := string(b.Get([]byte("imei")))
		if old != "" && old != imei {
			return errors.New("SBD queue is bound to another handset IMEI")
		}
		return b.Put([]byte("imei"), []byte(imei))
	})
}

// LockWorker excludes another manager or direct SBD command for this queue.
// Closing the returned handle releases the OS lock, including after a crash.
func (q *Queue) LockWorker() (*bolt.DB, error) {
	db, e := bolt.Open(filepath.Join(q.Dir, "worker.db"), 0600, &bolt.Options{Timeout: time.Millisecond})
	if e != nil {
		return nil, fmt.Errorf("SBD worker already active or lock unavailable: %w", e)
	}
	return db, nil
}

// SaveIncoming commits an incoming payload before its handset buffer is cleared.
// It suppresses recovery rereads of the latest message, including after Ack.
func (q *Queue) SaveIncoming(imei string, sequence int, p []byte) error {
	if imei == "" || sequence < 0 || sequence > 65535 || len(p) > 1960 {
		return errors.New("invalid incoming SBD record")
	}
	return q.update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("meta"))
		key := fmt.Sprintf("%s/%d", imei, sequence)
		last := string(b.Get([]byte("last-incoming")))
		if last == key && string(b.Get([]byte("last-payload"))) == string(p) {
			return nil
		}
		if e := capacity(tx, len(p)); e != nil {
			return e
		}
		v := Incoming{ID: newID(), IMEI: imei, Sequence: sequence, Payload: p, Received: time.Now().UTC()}
		if e := put(tx, "inbox", v.ID, v); e != nil {
			return e
		}
		if e := b.Put([]byte("last-incoming"), []byte(key)); e != nil {
			return e
		}
		return b.Put([]byte("last-payload"), p)
	})
}

// view opens a short read transaction without dirtying pages or syncing a
// write transaction on every idle worker poll.
func (q *Queue) view(f func(*bolt.Tx) error) error {
	db, e := bolt.Open(filepath.Join(q.Dir, "queue.db"), 0600, &bolt.Options{Timeout: 5 * time.Second})
	if e != nil {
		return e
	}
	defer db.Close()
	return db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("meta"))
		if b == nil || string(b.Get([]byte("version"))) != "1" {
			return errors.New("unsupported SBD queue schema")
		}
		return f(tx)
	})
}
func (q *Queue) saveOrphan(p []byte) (string, error) {
	m := Message{ID: newID(), Payload: p, State: Orphaned, Created: time.Now().UTC()}
	e := q.update(func(tx *bolt.Tx) error {
		if e := capacity(tx, len(p)); e != nil {
			return e
		}
		if e := put(tx, "outbox", m.ID, m); e != nil {
			return e
		}
		return tx.Bucket([]byte("meta")).Put([]byte("orphan"), []byte(m.ID))
	})
	return m.ID, e
}

// Bind associates the queue with a handset identity, refusing mismatches.
func (q *Queue) Bind(imei string) error { return q.bind(imei) }
