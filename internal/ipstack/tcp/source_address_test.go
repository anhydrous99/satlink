package tcp

// Tests of a change of the interface's IP address.

import (
	"net/netip"
	"testing"
)

func TestEstablishedCloseOnSrcIPChange(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)

	// Verify that the socket is working normally.
	mustSendSlice(t, s, []byte("abc"))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abc"))))

	// Simulate interface IP change - remove the socket's source IP and add a
	// different one.
	newAddrs := []netip.Prefix{netip.PrefixFrom(otherAddr, 24)}
	if err := s.cx.SetIPAddrs(newAddrs); err != nil {
		t.Fatalf("SetIPAddrs(%v): %v", newAddrs, err)
	}

	// The socket's source IP is no longer on the interface. When Dispatch
	// runs, it should detect this and reset the socket silently (no RST sent,
	// since that would use the invalid source IP).
	mustSendSlice(t, s, []byte("def"))
	receiveNothing(t, s)
	assertState(t, s, StateClosed)
}
