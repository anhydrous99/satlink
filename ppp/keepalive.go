package ppp

import (
	"bytes"
	"encoding/binary"
	"time"
)

// LCP echoes: keepalive probes that notice a peer which has silently gone
// away, our answers to the peer's probes, and the loopback detection our
// own probes coming back allow.

// magicNumberLen is the size of the magic number at the start of every
// Echo and Discard packet's data.
const magicNumberLen = 4

// echoOverhead is the LCP header plus magic number in front of echoed
// data.
const echoOverhead = headerLen + magicNumberLen

// ownProbeLoopbackLimit is how many of our own echo probes may come back
// before the line is called looped.
const ownProbeLoopbackLimit = 3

// probeAction is what the keepalive prober wants done.
type probeAction int

// The prober's requests.
const (
	probeNotDue probeAction = iota
	probeSend
	probeGiveUp
)

// prober sends LCP Echo-Requests once the link has been silent for a
// while.
type prober struct {
	settings Keepalive
	// nextProbe is when the next probe is due; the zero Time while the
	// prober is stopped.
	nextProbe  time.Time
	unanswered int
}

// newProberFor returns a prober for keepalive, and nil when keepalive is
// nil.
func newProberFor(keepalive *Keepalive) *prober {
	if keepalive == nil {
		return nil
	}
	return &prober{settings: *keepalive}
}

// start begins probing after a silent interval from now.
func (p *prober) start(now time.Time) {
	p.unanswered = 0
	p.nextProbe = now.Add(p.settings.Interval)
}

// stop stops probing.
func (p *prober) stop() {
	p.nextProbe = time.Time{}
}

// heardFromPeer restarts the silent interval, unless the prober is
// stopped.
func (p *prober) heardFromPeer(now time.Time) {
	if !p.nextProbe.IsZero() {
		p.start(now)
	}
}

// nextTimeout returns when the next probe is due; the zero Time while the
// prober is stopped.
func (p *prober) nextTimeout() time.Time {
	return p.nextProbe
}

// poll says whether a probe is due at now, or whether too many went
// unanswered.
func (p *prober) poll(now time.Time) probeAction {
	if !isDue(p.nextProbe, now) {
		return probeNotDue
	}
	if p.unanswered >= p.settings.MaxMissed {
		p.stop()
		return probeGiveUp
	}
	p.unanswered++
	p.nextProbe = now.Add(p.settings.Interval)
	return probeSend
}

// probeKeepalive sends a probe when one is due, and declares the peer
// unresponsive when too many went unanswered.
func (e *Endpoint) probeKeepalive(now time.Time) {
	if e.prober == nil {
		return
	}
	switch e.prober.poll(now) {
	case probeSend:
		magicNumber := binary.BigEndian.AppendUint32(nil, e.lcp.negotiator.ownMagicNumber())
		e.outbox.push(protocolLCP, packet{code: codeEchoRequest, identifier: e.nextLCPIdentifier(), data: magicNumber})
	case probeGiveUp:
		e.abandonLink(DownReason{Kind: DownReasonPeerUnresponsive})
	}
}

// answerEchoRequest answers the peer's probe with our magic number and its
// data, as far as the peer's MRU allows.
func (e *Endpoint) answerEchoRequest(request packet) {
	if !e.lcp.isOpened() {
		return // only answered in the Opened state (RFC 1661 §5.8)
	}
	room := max(e.MTU()-echoOverhead, 0)
	echoed := request.data[min(magicNumberLen, len(request.data)):]
	echoed = echoed[:min(room, len(echoed))]
	reply := binary.BigEndian.AppendUint32(make([]byte, 0, magicNumberLen+len(echoed)), e.lcp.negotiator.ownMagicNumber())
	e.outbox.push(protocolLCP, packet{code: codeEchoReply, identifier: request.identifier, data: append(reply, echoed...)})
}

// isOwnProbe reports whether f is an Echo- or Discard-Request carrying our
// own magic number: one of our probes reflected by a looped line (RFC 1661
// §6.4).
func (e *Endpoint) isOwnProbe(f frame) bool {
	own := e.lcp.negotiator.ownMagicNumber()
	if own == 0 || f.protocol != protocolLCP {
		return false
	}
	probe, ok := parsePacket(f.information)
	if !ok || (probe.code != codeEchoRequest && probe.code != codeDiscardRequest) {
		return false
	}
	return bytes.HasPrefix(probe.data, binary.BigEndian.AppendUint32(nil, own))
}

// onOwnProbeReturned counts one of our probes coming back, and ends the
// link once too many have.
func (e *Endpoint) onOwnProbeReturned() {
	e.ownProbesReturned++
	if e.ownProbesReturned >= ownProbeLoopbackLimit {
		e.abandonLink(DownReason{Kind: DownReasonLoopedBack})
	}
}
