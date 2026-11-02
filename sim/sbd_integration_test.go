package sim_test

import (
	"bytes"
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/sim"
	"testing"
)

func TestSBDSimulatorBinaryAndGatewayRoundTrips(t *testing.T) {
	transport := sim.New(sim.DefaultConfig())
	defer transport.Close()
	h := iridium.NewHandset(transport)
	if e := h.Initialize(); e != nil {
		t.Fatal(e)
	}
	c, e := h.AcquireSBD()
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	caps, _ := iridium.SBDProfile("9575")
	p := make([]byte, 1890)
	for i := range p {
		p[i] = byte(i)
	}
	if e = c.WriteBinary(p, caps); e != nil {
		t.Fatal(e)
	}
	if e = c.Loopback(); e != nil {
		t.Fatal(e)
	}
	got, e := c.ReadBinary(caps)
	if e != nil || !bytes.Equal(got, p) {
		t.Fatalf("loopback %v", e)
	}
	if e = c.Clear(2); e != nil {
		t.Fatal(e)
	}
	transport.Control().InjectSBD(p)
	x, e := c.Exchange([]byte("hello"), caps, iridium.DefaultSBDSessionSettings())
	if e != nil || !bytes.Equal(x.Payload, p) || !x.Result.Accepted() {
		t.Fatalf("exchange %+v %v", x.Result, e)
	}
	sent := transport.Control().SentSBD()
	if len(sent) != 1 || string(sent[0]) != "hello" {
		t.Fatal(sent)
	}
	if _, e = c.Exchange(nil, caps, iridium.DefaultSBDSessionSettings()); e == nil {
		t.Fatal("overwrote unread buffer")
	}
}
