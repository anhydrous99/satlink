package sim

import (
	"net/netip"
	"time"
)

// A connected data call: the satellite link in both directions, with the
// gateway at its far end.

// circuit is the data path of a connected call.
type circuit struct {
	uplink   *delayLine
	gateway  *gateway
	downlink *delayLine
}

// connectCircuit connects the call: the gateway answers and starts PPP. It
// fails when the gateway cannot start with config.
func connectCircuit(config Config, now time.Time) (*circuit, error) {
	gateway, err := startGateway(config.Network, config.Authentication, now)
	if err != nil {
		return nil, err
	}
	return &circuit{
		uplink:   newDelayLine(config.Link),
		gateway:  gateway,
		downlink: newDelayLine(config.Link),
	}, nil
}

// restartPPP makes the gateway start PPP over and assign clientAddress.
func (c *circuit) restartPPP(clientAddress netip.Addr, now time.Time) error {
	return c.gateway.restartPPP(clientAddress, now)
}

// send puts bytes from the app on the uplink.
func (c *circuit) send(data []byte, now time.Time) {
	c.uplink.send(data, now)
}

// run lets the far end catch up with now: what crossed the uplink reaches
// the gateway, and the gateway's answers start down the downlink.
func (c *circuit) run(now time.Time) {
	if arrived := c.uplink.receive(now); len(arrived) > 0 {
		c.gateway.receive(arrived, now)
	}
	c.gateway.advance(now)
	c.downlink.send(c.gateway.takeOutput(), now)
}

// receive returns what has come down the downlink by now.
func (c *circuit) receive(now time.Time) []byte {
	return c.downlink.receive(now)
}

// takeDiscardedBytes returns the bytes the discard service has read since
// the last call.
func (c *circuit) takeDiscardedBytes() uint64 {
	return c.gateway.takeDiscardedBytes()
}

// close ends what the call keeps running in the background: the
// internet's TLS sessions.
func (c *circuit) close() {
	c.gateway.close()
}
