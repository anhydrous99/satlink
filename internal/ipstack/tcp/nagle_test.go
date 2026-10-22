package tcp

// Tests of Nagle's algorithm.

import (
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestNagle(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6
	mustSendSlice(t, s, []byte("abcdef"))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("abcdef"))))

	// If there's data in flight, full segments get sent.
	mustSendSlice(t, s, []byte("foobar"))
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+6)), withAck(remoteSeq.Add(1)), withPayload([]byte("foobar"))))

	mustSendSlice(t, s, []byte("aaabbbccc"))
	// If there's data in flight, not-full segments don't get sent.
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+6+6)), withAck(remoteSeq.Add(1)), withPayload([]byte("aaabbb"))))

	// Data gets ACKed, so there's no longer data in flight.
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1+6+6+6))))

	// Now non-full segment gets sent.
	receiveOnly(t, s, fromLocal(withSeq(localSeq.Add(1+6+6+6)), withAck(remoteSeq.Add(1)), withPayload([]byte("ccc"))))
}

func TestFinalPacketInStreamDoesntWaitForNagle(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.remoteMSS = 6
	mustSendSlice(t, s, []byte("abcdef0"))

	s.Close()

	receiveExact(t, s, fromLocal(withControl(wire.TCPControlNone), withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)),
		withPayload([]byte("abcdef"))))
	receiveExact(t, s, fromLocal(withControl(wire.TCPControlFIN), withSeq(localSeq.Add(1+6)), withAck(remoteSeq.Add(1)),
		withPayload([]byte("0"))))
}
