package satlink

import (
	"bytes"
	"testing"
	"time"
)

// silentTransport takes every write and never answers. It is used by one
// goroutine at a time.
type silentTransport struct {
	written []byte
}

// WriteAll records p.
func (s *silentTransport) WriteAll(p []byte) error {
	s.written = append(s.written, p...)
	return nil
}

// ReadTimeout waits out timeout, briefly: nothing ever arrives.
func (s *silentTransport) ReadTimeout(_ []byte, timeout time.Duration) (int, error) {
	time.Sleep(min(timeout, time.Millisecond))
	return 0, nil
}

func TestTextEntryThatTimesOutIsLeftWithESC(t *testing.T) {
	// SendSMS waits two minutes; exec itself takes any timeout.
	transport := &silentTransport{}
	handset := NewHandset(transport)

	_, err := handset.exec(request{
		command:    `AT+CMGS="+1"`,
		timeout:    20 * time.Millisecond,
		completion: untilFinalResult,
		textEntry:  []byte("hi\x1a"),
	})

	if timeout, ok := err.(*Error); !ok || timeout.Kind != ErrTimeout || timeout.Command != `AT+CMGS="+1"` {
		t.Errorf("exec = %v, want ErrTimeout", err)
	}
	if !bytes.Equal(transport.written, []byte("AT+CMGS=\"+1\"\r\x1b")) {
		t.Errorf("written %q, want the command, then ESC to leave text entry", transport.written)
	}
}

func TestACommandThatTimesOutWithoutTextEntryIsNotSentESC(t *testing.T) {
	transport := &silentTransport{}
	handset := NewHandset(transport)

	_, err := handset.exec(request{command: "AT", timeout: 20 * time.Millisecond, completion: untilFinalResult})

	if err == nil {
		t.Fatal("exec answered without an answer")
	}
	if !bytes.Equal(transport.written, []byte("AT\r")) {
		t.Errorf("written %q, want only the command", transport.written)
	}
}
