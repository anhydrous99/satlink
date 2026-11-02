package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/sbd"
)

type sbdFlags struct {
	dir                string
	maxMO, maxMT, poll uint64
}

func (f *sbdFlags) queue() (*sbd.Queue, error) {
	dir := f.dir
	if dir == "" {
		root, e := os.UserConfigDir()
		if e != nil {
			return nil, e
		}
		dir = filepath.Join(root, "satlink", "sbd")
	}
	return sbd.Open(dir)
}
func (f *sbdFlags) caps() iridium.SBDCapabilities {
	return iridium.SBDCapabilities{ReadFinalResult: true, MaxMO: int(f.maxMO), MaxMT: int(f.maxMT), RingAlerts: true}
}
func (c *cli) sbdCommand() *command {
	f := &sbdFlags{}
	group := newCommand("sbd", "Send and manage Short Burst Data messages", nil,
		&argument{long: "queue-dir", valueName: "DIR", help: "Private durable queue directory (default: user config/satlink/sbd)", value: stringValue{target: &f.dir}},
		&argument{long: "max-mo", valueName: "BYTES", help: "Outbound provider limit", hasDefault: true, defaultValue: "1960", value: unsignedValue{target: &f.maxMO, minimum: 1}},
		&argument{long: "max-mt", valueName: "BYTES", help: "Inbound provider limit", hasDefault: true, defaultValue: "1890", value: unsignedValue{target: &f.maxMT, minimum: 1}})
	commands := []*command{c.sbdStatusCommand(f), c.sbdPayloadCommand(f, "send"), c.sbdPayloadCommand(f, "enqueue"), c.sbdPayloadCommand(f, "loopback"), c.sbdReceiveCommand(f), c.sbdRunCommand(f)}
	for _, name := range []string{"outbox", "inbox", "check"} {
		commands = append(commands, c.sbdQueueCommand(f, name))
	}
	for _, name := range []string{"read", "ack", "retry", "cancel", "purge"} {
		commands = append(commands, c.sbdRecordCommand(f, name))
	}
	return group.withSubcommands(commands...)
}
func (c *cli) sbdJSON(v any) error { return json.NewEncoder(c.streams.stdout).Encode(v) }
func (c *cli) withSBD(f *sbdFlags, act func(*iridium.SBDClient) error) error {
	if f.maxMO > 1960 || f.maxMT > 1960 {
		return errors.New("SBD limits cannot exceed 1960 bytes")
	}
	q, e := f.queue()
	if e != nil {
		return e
	}
	lock, e := q.LockWorker()
	if e != nil {
		return e
	}
	defer lock.Close()
	return c.withHandset(func(h *iridium.Handset) error {
		client, e := h.AcquireSBD()
		if e != nil {
			return e
		}
		defer client.Close()
		info, e := client.Info()
		if e != nil {
			return e
		}
		if _, e = iridium.SBDProfile(info.Model); e != nil {
			return e
		}
		if e = q.Bind(info.IMEI); e != nil {
			return e
		}
		return act(client)
	})
}
func (c *cli) sbdStatusCommand(f *sbdFlags) *command {
	return newCommand("status", "Show SBD buffer status as JSON without transmitting", func(context.Context) error {
		return c.withSBD(f, func(client *iridium.SBDClient) error {
			s, e := client.Status()
			if e != nil {
				return e
			}
			return c.sbdJSON(s)
		})
	})
}
func (c *cli) sbdPayloadCommand(f *sbdFlags, name string) *command {
	var text, file givenText
	return newCommand(name, map[string]string{"send": "Send one payload; gateway acceptance is reported as JSON", "enqueue": "Durably queue a payload without opening USB", "loopback": "Test USB buffer transfers without satellite airtime"}[name], func(context.Context) error {
		p, e := c.sbdPayload(text, file, int(f.maxMO))
		if e != nil {
			return e
		}
		if name == "enqueue" {
			q, e := f.queue()
			if e != nil {
				return e
			}
			id, e := q.Enqueue(p)
			if e != nil {
				return e
			}
			return writef(c.streams.stdout, "%s\n", id)
		}
		return c.sbdSendOrLoopback(f, name, p)
	}, &argument{long: "text", valueName: "TEXT", help: "UTF-8 payload bytes (exactly one of --text or --file)", value: &text}, &argument{long: "file", valueName: "PATH", help: "Binary payload file, or - for stdin", value: &file})
}
func (c *cli) sbdPayload(text, file givenText, limit int) ([]byte, error) {
	if text.given == file.given {
		return nil, errors.New("supply exactly one of --text or --file")
	}
	var p []byte
	var e error
	if text.given {
		p = []byte(text.text)
	} else {
		r := c.streams.stdin
		if file.text != "-" {
			f, err := os.Open(file.text)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			r = f
		}
		p, e = io.ReadAll(io.LimitReader(r, 1961))
		if e != nil {
			return nil, e
		}
	}
	if len(p) < 1 || len(p) > limit || len(p) > 1960 {
		return nil, errors.New("payload is empty or exceeds outbound SBD limit")
	}
	return p, nil
}
func (c *cli) sbdReceiveCommand(f *sbdFlags) *command {
	var output string
	return newCommand("receive", "Receive one payload; bytes to stdout and metadata to stderr", func(context.Context) error {
		return c.withSBD(f, func(client *iridium.SBDClient) error {
			status, e := client.Status()
			if e != nil {
				return e
			}
			if status.MOFlag != 0 {
				return errors.New("outgoing SBD buffer is occupied")
			}
			var p []byte
			sequence := status.MTMSN
			if status.MTFlag != 0 {
				p, e = client.ReadBinary(f.caps())
			} else {
				x, err := client.Exchange(nil, f.caps(), iridium.DefaultSBDSessionSettings())
				e = err
				if e == nil {
					if err = json.NewEncoder(c.streams.stderr).Encode(x.Result); err != nil {
						return err
					}
					if x.Result.MTStatus != 1 {
						return nil
					}
					p = x.Payload
					sequence = x.Result.MTMSN
				}
			}
			if e != nil {
				return e
			}
			q, e := f.queue()
			if e != nil {
				return e
			}
			info, e := client.Info()
			if e != nil {
				return e
			}
			if e = q.SaveIncoming(info.IMEI, sequence, p); e != nil {
				return e
			}
			if output != "" {
				e = os.WriteFile(output, p, 0600)
			} else {
				_, e = c.streams.stdout.Write(p)
			}
			if e != nil {
				return e
			}
			return client.Clear(1)
		})
	}, &argument{long: "output", valueName: "PATH", help: "Write binary payload to a file instead of stdout", value: stringValue{target: &output}})
}
func (c *cli) sbdQueueCommand(f *sbdFlags, name string) *command {
	return newCommand(name, "Access durable SBD "+name+" (JSON)", func(context.Context) error {
		q, e := f.queue()
		if e != nil {
			return e
		}
		switch name {
		case "outbox":
			v, e := q.Outbox()
			if e != nil {
				return e
			}
			return c.sbdJSON(v)
		case "inbox":
			v, e := q.Inbox()
			if e != nil {
				return e
			}
			return c.sbdJSON(v)
		default:
			return q.Check()
		}
	})
}
func (c *cli) sbdRecordCommand(f *sbdFlags, name string) *command {
	var id string
	return newCommand(name, "Apply "+name+" to a durable SBD record", func(context.Context) error {
		q, e := f.queue()
		if e != nil {
			return e
		}
		switch name {
		case "read":
			v, e := q.Read(id)
			if e != nil {
				return e
			}
			_, e = c.streams.stdout.Write(v.Payload)
			return e
		case "ack":
			return q.Ack(id)
		case "retry":
			return q.Retry(id)
		case "cancel":
			return q.Cancel(id)
		default:
			return q.Purge(id)
		}
	}, requiredPositional("ID", "Local message identifier", stringValue{target: &id}))
}
func (c *cli) sbdRunCommand(f *sbdFlags) *command {
	return newCommand("run", "Run the foreground worker; retries can deliver duplicates", func(ctx context.Context) error {
		q, e := f.queue()
		if e != nil {
			return e
		}
		settings := sbd.DefaultSettings()
		settings.Capabilities = f.caps()
		settings.PollInterval = saturatingDuration(f.poll, time.Second)
		connector := func(context.Context) (*iridium.Handset, error) {
			t, e := c.openTransport()
			if e != nil {
				return nil, e
			}
			h := iridium.NewHandset(t)
			if e = h.Initialize(); e != nil {
				_ = h.Close()
				return nil, e
			}
			return h, nil
		}
		manager, e := sbd.NewManager(q, connector, settings)
		if e != nil {
			return e
		}
		manager.Events = func(events []string) { _ = printEvents(c.streams.stderr, "(event) ", events) }
		interrupted := c.interruptions.catchMode(c.env.notifyInterruptions, c.env.quitInterrupted, c.streams.stderr, true)
		work, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-interrupted.Done():
				cancel()
			case <-work.Done():
			}
		}()
		return manager.Run(work)
	}, &argument{long: "poll-secs", valueName: "SECONDS", help: "Timed mailbox checks (0 disables; otherwise at least 60)", hasDefault: true, defaultValue: "0", value: unsignedValue{target: &f.poll}})
}

func (c *cli) sbdSendOrLoopback(f *sbdFlags, name string, p []byte) error {
	return c.withSBD(f, func(client *iridium.SBDClient) error {
		status, e := client.Status()
		if e != nil {
			return e
		}
		if status.MOFlag != 0 || status.MTFlag != 0 {
			return errors.New("existing SBD buffer: recover it before send or loopback")
		}
		if name == "loopback" {
			return c.sbdLoopback(client, p, f.caps())
		}
		x, e := client.Exchange(p, f.caps(), iridium.DefaultSBDSessionSettings())
		if x.Result.Reported {
			if outputErr := c.sbdJSON(x.Result); outputErr != nil {
				return outputErr
			}
		}
		if e != nil {
			return e
		}
		if x.Result.MTStatus == 1 {
			q, e := f.queue()
			if e != nil {
				return e
			}
			info, e := client.Info()
			if e != nil {
				return e
			}
			if e = q.SaveIncoming(info.IMEI, x.Result.MTMSN, x.Payload); e != nil {
				return e
			}
			if e = client.Clear(1); e != nil {
				return e
			}
		}
		if e = client.Clear(0); e != nil {
			return e
		}
		return nil
	})
}

func (c *cli) sbdLoopback(client *iridium.SBDClient, p []byte, caps iridium.SBDCapabilities) error {
	if len(p) > caps.MaxMT {
		return errors.New("loopback payload exceeds incoming buffer limit")
	}
	if e := client.WriteBinary(p, caps); e != nil {
		return e
	}
	if e := client.Loopback(); e != nil {
		return e
	}
	got, e := client.ReadBinary(caps)
	if e != nil {
		return e
	}
	if string(got) != string(p) {
		return errors.New("SBD loopback mismatch")
	}
	if e = client.Clear(2); e != nil {
		return e
	}
	return c.sbdJSON(map[string]any{"bytes": len(got), "matched": true})
}
