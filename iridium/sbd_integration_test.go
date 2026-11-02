package iridium_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/iridium"
)

var voiceSBD = iridium.SBDCapabilities{MaxMO: 1960, MaxMT: 1890, RingAlerts: true}

func sbdFrame(p []byte) []byte {
	wire := make([]byte, len(p)+4)
	binary.BigEndian.PutUint16(wire, uint16(len(p)))
	copy(wire[2:], p)
	var sum uint16
	for _, b := range p {
		sum += uint16(b)
	}
	binary.BigEndian.PutUint16(wire[len(wire)-2:], sum)
	return wire
}
func TestSBDBinaryFrameIsOpaqueAcrossEveryReadBoundary(t *testing.T) {
	payload := append([]byte("\r\nCONNECT\r\nOK\r\nERROR\r\nSBDRING\r\n"), make([]byte, 256)...)
	for i := 0; i < 256; i++ {
		payload[len(payload)-256+i] = byte(i)
	}
	transport := newScripted(step{"AT+SBDRB\r", string(sbdFrame(payload))}, step{"AT+CGMM\r", "\r\n9575\r\nOK\r\n"})
	h := newTestHandset(transport)
	c, e := h.AcquireSBD()
	if e != nil {
		t.Fatal(e)
	}
	got, e := c.ReadBinary(voiceSBD)
	if e != nil || !bytes.Equal(got, payload) {
		t.Fatalf("read: %v, %x", e, got)
	}
	c.Close()
	r, e := h.Command("AT+CGMM")
	if e != nil || len(r.Lines) != 1 || r.Lines[0] != "9575" {
		t.Fatalf("next command: %+v %v", r, e)
	}
	if len(h.TakeURCs()) != 0 {
		t.Fatal("payload became URC")
	}
}
func TestSBDWriteChecksNumericResultEvenWithOK(t *testing.T) {
	for _, code := range []string{"0", "2", "3"} {
		t.Run(code, func(t *testing.T) {
			p := []byte("hello")
			wire := sbdFrame(p)[2:]
			transport := newScripted(step{"AT+SBDWB=5\r", "\r\nREADY\r\n"}, step{string(wire), "\r\n" + code + "\r\nOK\r\n"})
			h := newTestHandset(transport)
			c, _ := h.AcquireSBD()
			defer c.Close()
			e := c.WriteBinary(p, voiceSBD)
			if (e == nil) != (code == "0") {
				t.Fatalf("code %s: %v", code, e)
			}
		})
	}
}
func TestSBDTruncationQuarantinesAllCommands(t *testing.T) {
	h := newTestHandset(newScripted(step{"AT+SBDRB\r", "\x00\x05h"}))
	c, _ := h.AcquireSBD()
	_, e := c.ReadBinary(voiceSBD)
	if !errors.Is(e, iridium.ErrTimeout) {
		t.Fatal(e)
	}
	c.Close()
	_, e = h.Command("AT")
	if !errors.Is(e, iridium.ErrSBDUnsynchronised) {
		t.Fatal(e)
	}
	if e = h.Initialize(); !errors.Is(e, iridium.ErrSBDUnsynchronised) {
		t.Fatal(e)
	}
}
func TestSBDBadChecksumDoesNotQuarantineFramedRead(t *testing.T) {
	wire := sbdFrame([]byte("hello"))
	wire[len(wire)-1]++
	h := newTestHandset(newScripted(step{"AT+SBDRB\r", string(wire)}, step{"AT\r", "\r\nOK\r\n"}))
	c, _ := h.AcquireSBD()
	_, e := c.ReadBinary(voiceSBD)
	if !errors.Is(e, iridium.ErrSBD) {
		t.Fatal(e)
	}
	c.Close()
	if _, e = h.Command("AT"); e != nil {
		t.Fatal(e)
	}
}
func TestSBDSessionPreservesNetworkStatus(t *testing.T) {
	for _, status := range []int{0, 1, 2, 3, 4, 10, 15, 32, 36, 38, 99} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			reply := fmt.Sprintf("\r\nSBDRING\r\n+SBDIX: %d,42,0,-1,0,0\r\nOK\r\n", status)
			h := newTestHandset(newScripted(step{"AT+SBDIX\r", reply}))
			c, _ := h.AcquireSBD()
			r, e := c.Session(iridium.DefaultSBDSessionSettings())
			if r.MOStatus != status || r.MOMSN != 42 || (e == nil) != (status <= 4) {
				t.Fatalf("%+v %v", r, e)
			}
			c.Close()
			if len(h.TakeURCs()) != 1 {
				t.Fatal("lost ring")
			}
		})
	}
}
func TestSBDCancellationBeforeSessionWritesNothing(t *testing.T) {
	transport := newScripted()
	h := newTestHandset(transport)
	c, _ := h.AcquireSBD()
	defer c.Close()
	s := iridium.DefaultSBDSessionSettings()
	s.Cancellation = iridium.NewCancellation()
	s.Cancellation.Cancel()
	_, e := c.Session(s)
	if !errors.Is(e, iridium.ErrCancelled) {
		t.Fatal(e)
	}
	if len(transport.allWritten) != 0 {
		t.Fatal("wrote after cancellation")
	}
}
func TestSBDLostSessionResultIsUncertainAndExclusive(t *testing.T) {
	h := newTestHandset(newScripted(step{"AT+SBDIX\r", ""}))
	c, _ := h.AcquireSBD()
	if _, e := h.Command("AT"); !errors.Is(e, iridium.ErrBusy) {
		t.Fatal(e)
	}
	s := iridium.DefaultSBDSessionSettings()
	s.Timeout = time.Millisecond
	_, e := c.Session(s)
	if !errors.Is(e, iridium.ErrSBDUncertain) {
		t.Fatal(e)
	}
	c.Close()
}
func TestSBDRawDataCommandsAreRefused(t *testing.T) {
	for _, command := range []string{"AT+SBDWB=4", "AT+SBDRB", "AT+SBDRT", "AT+SBDWT", "AT+SBDREG?+SBDREG", "AT+SBDIX"} {
		h := newTestHandset(newScripted())
		if _, e := h.Command(command); !errors.Is(e, iridium.ErrInvalidArgument) {
			t.Fatalf("%s: %v", command, e)
		}
	}
}
