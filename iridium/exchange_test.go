package iridium

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

// okTransport answers every write with OK at once, as a handset does to the
// escape and to ATH. It is used by one goroutine at a time.
type okTransport struct {
	written []byte
	unread  []byte
}

// WriteAll records p, and has OK answer it.
func (o *okTransport) WriteAll(p []byte) error {
	o.written = append(o.written, p...)
	o.unread = append(o.unread, "\r\nOK\r\n"...)
	return nil
}

// ReadTimeout delivers the OKs not yet read, or waits out timeout, briefly.
func (o *okTransport) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	if len(o.unread) == 0 {
		time.Sleep(min(timeout, time.Millisecond))
		return 0, nil
	}
	n := copy(p, o.unread)
	o.unread = o.unread[n:]
	return n, nil
}

// connectReceived is what a handset had sent, unread, when a data call came
// up between commands, as one set to answer on its own (ATS0=1) may: a URC,
// then the CONNECT, read together.
const connectReceived = "\r\nRING\r\n\r\nCONNECT 9600\r\n"

func TestACONNECTAlreadyReceivedHasItsCallEndedInsteadOfTheNextCommand(t *testing.T) {
	// Slow — an escape with the default guard — but timing-insensitive.
	t.Parallel()
	transport := &okTransport{}
	handset := NewHandset(transport)
	handset.rx.push([]byte(connectReceived))

	_, err := handset.Battery()

	want := `call failed: a CONNECT arrived before "AT+CBC": the handset went online, and the call was ended`
	if e, ok := err.(*Error); !ok || e.Kind != ErrCall || err.Error() != want {
		t.Errorf("Battery = %v, want %s", err, want)
	}
	assertEscapedAndHungUpKeepingTheRING(t, handset, transport)
}

func TestACONNECTThatPollURCsReadsHasItsCallEnded(t *testing.T) {
	// Slow — an escape with the default guard — but timing-insensitive.
	t.Parallel()
	transport := &okTransport{}
	handset := NewHandset(transport)
	handset.rx.push([]byte(connectReceived))

	_, err := handset.PollURCs(time.Second)

	want := "call failed: a CONNECT arrived while waiting for unsolicited result codes: " +
		"the handset went online, and the call was ended"
	if e, ok := err.(*Error); !ok || e.Kind != ErrCall || err.Error() != want {
		t.Errorf("PollURCs = %v, want %s", err, want)
	}
	assertEscapedAndHungUpKeepingTheRING(t, handset, transport)
}

func TestACONNECTAlreadyReceivedHasItsCallEndedInsteadOfAnsweredInto(t *testing.T) {
	// ATA would go into the call as its data, and only its timeout, a
	// minute and a half later, would have the call ended. Slow — an escape
	// with the default guard — but timing-insensitive.
	t.Parallel()
	transport := &okTransport{}
	handset := NewHandset(transport)
	handset.rx.push([]byte(connectReceived))

	err := handset.Answer()

	want := `call failed: a CONNECT arrived before "ATA": the handset went online, and the call was ended`
	if e, ok := err.(*Error); !ok || e.Kind != ErrCall || err.Error() != want {
		t.Errorf("Answer = %v, want %s", err, want)
	}
	assertEscapedAndHungUpKeepingTheRING(t, handset, transport)
}

// assertEscapedAndHungUpKeepingTheRING checks that only the escape and ATH
// were written, and that the RING of connectReceived is queued.
func assertEscapedAndHungUpKeepingTheRING(t *testing.T, handset *Handset, transport *okTransport) {
	t.Helper()
	if written := string(transport.written); written != escapeSequence+hangUpCommand+commandEnd {
		t.Errorf("written %q, want only the escape and ATH", written)
	}
	if urcs := handset.TakeURCs(); len(urcs) != 1 || urcs[0] != "RING" {
		t.Errorf("TakeURCs = %q, want the RING before the CONNECT", urcs)
	}
}

func TestTextEntryThatTimesOutIsLeftWithESC(t *testing.T) {
	// SendSMS waits two minutes; exec itself takes any timeout.
	transport := &silentTransport{}
	handset := NewHandset(transport)

	_, err := handset.exec(request{
		command:    "AT+CMGS=19",
		timeout:    20 * time.Millisecond,
		completion: untilFinalResult,
		textEntry:  []byte("00\x1a"),
	})

	if timeout, ok := err.(*Error); !ok || timeout.Kind != ErrTimeout || timeout.Command != "AT+CMGS=19" {
		t.Errorf("exec = %v, want ErrTimeout", err)
	}
	if !bytes.Equal(transport.written, []byte("AT+CMGS=19\r\x1b")) {
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
