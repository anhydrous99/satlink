package tcp

import (
	"net/netip"
	"time"

	"github.com/anhydrous99/satlink/internal/ipstack/internal/netlog"
	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

// transition validates the segment's control flag against the socket's state
// and updates the state: the state machine of RFC 793.
func (s *Socket) transition(a *arrival) outcome {
	if a.control == wire.TCPControlRST {
		return s.receiveRST()
	}
	switch s.state {
	case StateListen:
		return s.transitionFromListen(a)
	case StateSynSent:
		return s.transitionFromSynSent(a)
	case StateSynReceived:
		return s.transitionFromSynReceived(a)
	case StateEstablished:
		return s.transitionFromEstablished(a)
	case StateFinWait1:
		return s.transitionFromFinWait1(a)
	case StateFinWait2:
		return s.transitionFromFinWait2(a)
	case StateClosing:
		return s.transitionFromClosing(a)
	case StateCloseWait:
		return s.transitionFromCloseWait(a)
	case StateLastAck:
		return s.transitionFromLastAck(a)
	default:
		return unexpected(a)
	}
}

// unexpected drops a segment whose control flag the state does not allow.
func unexpected(a *arrival) outcome {
	netlog.Debug("unexpected packet", "segment", a.segment)
	return dropSegment()
}

func (s *Socket) receiveRST() outcome {
	switch {
	case s.state == StateListen:
		// RSTs are not accepted in the LISTEN state.
	case s.state == StateSynReceived && s.listenEndpoint.Port != 0:
		// RSTs in SYN-RECEIVED flip the socket back to the LISTEN state. Here
		// we need to additionally check listenEndpoint, because we want to
		// make sure that SYN-RECEIVED was actually converted from the LISTEN
		// state (another possible reason is TCP simultaneous open).
		netlog.Trace("received RST")
		s.forgetTuple()
		s.setState(StateListen)
	default:
		// RSTs in any other state close the socket.
		netlog.Trace("received RST")
		s.setState(StateClosed)
		s.forgetTuple()
	}
	return dropSegment()
}

// transitionFromListen takes a SYN, which changes the LISTEN state to
// SYN-RECEIVED.
func (s *Socket) transitionFromListen(a *arrival) outcome {
	if a.control != wire.TCPControlSYN {
		return unexpected(a)
	}
	netlog.Trace("received SYN")
	s.adoptPeerOptions(a.segment)
	s.tuple = wire.Some(fourTuple{
		local:  netip.AddrPortFrom(a.ip.DstAddr, a.segment.DstPort),
		remote: netip.AddrPortFrom(a.ip.SrcAddr, a.segment.SrcPort),
	})
	s.localSeqNumber = s.chooseISN(a.cx)
	s.remoteSeqNumber = a.segment.SeqNumber.Add(1)
	s.remoteLastSeq = s.localSeqNumber
	s.setState(StateSynReceived)
	s.timer.setForIdle(a.cx.Now(), s.keepAlive)
	return carryOn()
}

// transitionFromSynSent takes a SYN|ACK, which changes the SYN-SENT state to
// ESTABLISHED, or a SYN, which changes it to SYN-RECEIVED.
func (s *Socket) transitionFromSynSent(a *arrival) outcome {
	if a.control != wire.TCPControlSYN {
		return unexpected(a)
	}
	synACK := a.segment.AckNumber.IsSome()
	if synACK {
		netlog.Trace("received SYN|ACK")
	} else {
		netlog.Trace("received SYN")
	}
	s.adoptPeerOptions(a.segment)
	s.remoteSeqNumber = a.segment.SeqNumber.Add(1)
	s.remoteLastSeq = s.localSeqNumber.Add(1)
	s.remoteLastAck = wire.Some(a.segment.SeqNumber)
	if synACK {
		s.setState(StateEstablished)
	} else {
		s.setState(StateSynReceived)
	}
	return carryOn()
}

// adoptPeerOptions takes what the peer's SYN announces: its MSS, whether it
// supports selective acknowledgements, and its window scale.
func (s *Socket) adoptPeerOptions(syn wire.TCPRepr) {
	// Treat a zero MSS as if the option were absent, like Linux does.
	if mss, ok := syn.MaxSegmentSize.Get(); ok && mss != 0 {
		s.remoteMSS = max(int(mss), minRemoteMSS)
	}
	s.remoteHasSACK = syn.SACKPermitted
	s.remoteWindowScale = syn.WindowScale
	// Remote doesn't support window scaling, don't do it.
	if !s.remoteWindowScale.IsSome() {
		s.remoteWindowShift = 0
	}
	// A peer that does not send timestamps gets none back: this socket never
	// sends any.
}

func (s *Socket) transitionFromSynReceived(a *arrival) outcome {
	switch a.control {
	case wire.TCPControlNone:
		// ACK packets in the SYN-RECEIVED state change it to ESTABLISHED.
		s.setState(StateEstablished)
	case wire.TCPControlFIN:
		// FIN packets in the SYN-RECEIVED state change it to CLOSE-WAIT. It's
		// not obvious from RFC 793 that this is permitted, but 7th and 8th
		// steps in the "SEGMENT ARRIVES" event describe this behavior.
		s.acceptFIN()
		s.setState(StateCloseWait)
	default:
		return unexpected(a)
	}
	return carryOn()
}

func (s *Socket) transitionFromEstablished(a *arrival) outcome {
	switch a.control {
	case wire.TCPControlNone:
	case wire.TCPControlFIN:
		// FIN packets in ESTABLISHED state indicate the remote side has
		// closed.
		s.acceptFIN()
		s.setState(StateCloseWait)
	default:
		return unexpected(a)
	}
	return carryOn()
}

func (s *Socket) transitionFromFinWait1(a *arrival) outcome {
	switch {
	case a.control == wire.TCPControlNone:
		// ACK packets in FIN-WAIT-1 state change it to FIN-WAIT-2, if we've
		// already sent everything in the transmit buffer. If not, they reset
		// the retransmit timer.
		if a.acknowledged.ofFIN {
			s.setState(StateFinWait2)
		}
	case a.control == wire.TCPControlFIN && a.acknowledged.ofFIN:
		// FIN packets in FIN-WAIT-1 state change it to CLOSING, or to
		// TIME-WAIT if they also acknowledge our FIN.
		s.acceptFIN()
		s.enterTimeWait(a.cx.Now())
	case a.control == wire.TCPControlFIN:
		s.acceptFIN()
		s.setState(StateClosing)
	default:
		return unexpected(a)
	}
	return carryOn()
}

func (s *Socket) transitionFromFinWait2(a *arrival) outcome {
	switch a.control {
	case wire.TCPControlNone:
	case wire.TCPControlFIN:
		// FIN packets in FIN-WAIT-2 state change it to TIME-WAIT.
		s.acceptFIN()
		s.enterTimeWait(a.cx.Now())
	default:
		return unexpected(a)
	}
	return carryOn()
}

func (s *Socket) transitionFromClosing(a *arrival) outcome {
	if a.control != wire.TCPControlNone {
		return unexpected(a)
	}
	// ACK packets in CLOSING state change it to TIME-WAIT.
	if a.acknowledged.ofFIN {
		s.enterTimeWait(a.cx.Now())
	}
	return carryOn()
}

func (s *Socket) transitionFromCloseWait(a *arrival) outcome {
	if a.control != wire.TCPControlNone {
		return unexpected(a)
	}
	return carryOn()
}

func (s *Socket) transitionFromLastAck(a *arrival) outcome {
	if a.control != wire.TCPControlNone {
		return unexpected(a)
	}
	switch {
	case a.acknowledged.ofFIN:
		// ACK packets in LAST-ACK state change it to CLOSED. Clear the remote
		// endpoint, or we'll send an RST there.
		s.setState(StateClosed)
		s.forgetTuple()
	case a.acknowledged.length == 0:
		// Duplicate ACK; our FIN has not been acknowledged. Per RFC 9293
		// (3.10.7.4), send a challenge ACK.
		return s.challengeACK(a)
	}
	// Partial ACK: carry on to advance SND.UNA normally.
	return carryOn()
}

// acceptFIN takes the peer's FIN, which occupies one sequence number and ends
// the stream it sends.
func (s *Socket) acceptFIN() {
	s.remoteSeqNumber = s.remoteSeqNumber.Add(1)
	s.finReceived = true
}

// takesFIN reports whether the socket's state takes the peer's FIN
// (acceptFIN): a synchronized state in which the peer has yet to close its
// side. The others drop a segment with a FIN: LISTEN and SYN-SENT, before
// the peer's SYN, and CLOSE-WAIT, CLOSING, LAST-ACK and TIME-WAIT, after its
// FIN. But effectiveControl disregards a FIN beyond a hole, and CLOSE-WAIT,
// CLOSING and LAST-ACK then handle its segment as one without a FIN.
func (s *Socket) takesFIN() bool {
	switch s.state {
	case StateSynReceived, StateEstablished, StateFinWait1, StateFinWait2:
		return true
	default:
		return false
	}
}

// enterTimeWait moves to TIME-WAIT and starts timing it.
func (s *Socket) enterTimeWait(now time.Time) {
	s.setState(StateTimeWait)
	s.timer.setForClose(now)
}
