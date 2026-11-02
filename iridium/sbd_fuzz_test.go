package iridium

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

type sbdWire struct {
	reply     []byte
	remaining []byte
	chunk     int
}

func (w *sbdWire) WriteAll([]byte) error { w.remaining = append([]byte(nil), w.reply...); return nil }
func (w *sbdWire) ReadTimeout(p []byte, _ time.Duration) (int, error) {
	n := min(len(p), len(w.remaining), w.chunk)
	copy(p, w.remaining[:n])
	w.remaining = w.remaining[n:]
	return n, nil
}
func FuzzSBDBinaryRead(f *testing.F) {
	f.Add([]byte("\r\nOK\r\nCONNECT\r\nSBDRING\r\n"), uint8(7), true)
	f.Add([]byte{0, 255, 13, 10}, uint8(1), false)
	f.Fuzz(func(t *testing.T, p []byte, chunk uint8, final bool) {
		if len(p) > 1960 {
			t.Skip()
		}
		frame := make([]byte, len(p)+4)
		binary.BigEndian.PutUint16(frame, uint16(len(p)))
		copy(frame[2:], p)
		var sum uint16
		for _, b := range p {
			sum += uint16(b)
		}
		binary.BigEndian.PutUint16(frame[len(frame)-2:], sum)
		if final {
			frame = append(frame, []byte("\r\nOK\r\n")...)
		}
		h := NewHandset(&sbdWire{reply: frame, chunk: int(chunk) + 1})
		c, e := h.AcquireSBD()
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		got, e := c.ReadBinary(SBDCapabilities{ReadFinalResult: final, MaxMO: 1960, MaxMT: 1960})
		if e != nil || !bytes.Equal(got, p) {
			t.Fatalf("read %d bytes: %v", len(p), e)
		}
	})
}
func TestSBDLoopbackResponseVariants(t *testing.T) {
	for _, line := range []string{"0", "SBDTC: Outbound SBD Copied to Inbound SBD: size = 1960"} {
		if !sbdLoopbackOK([]string{line}) {
			t.Fatal(line)
		}
	}
	for _, line := range []string{"1", "SBDTC: bad", "SBDTC: Outbound SBD Copied to Inbound SBD: size = 1961"} {
		if sbdLoopbackOK([]string{line}) {
			t.Fatal(line)
		}
	}
}
