package tcp

// The tests of smoltcp's socket/tcp.rs for reassembly (src/socket/tcp.rs,
// lines 8853-8965).

import (
	"testing"
)

func TestOutOfOrder(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	sendExpectingReply(t, s,
		fromRemote(withSeq(remoteSeq.Add(1+3)), withAck(localSeq.Add(1)), withPayload([]byte("def"))),
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1))))
	receiveExactly(t, s, "")

	sendExpectingReply(t, s,
		fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abcdef"))),
		fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+6)), withWindow(58)))

	receiveExactly(t, s, "abcdef")
}

func TestBufferWraparoundRx(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	replaceReceiveBuffer(s, smallBufferLen)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))
	receiveExactly(t, s, "abc")
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+3)), withAck(localSeq.Add(1)), withPayload([]byte("defghi"))))
	data := make([]byte, 6)

	received := mustReceiveSlice(t, s, data)

	if received != 6 || string(data) != "defghi" {
		t.Errorf("ReceiveSlice = %d, %q; want 6, %q", received, data, "defghi")
	}
}

func TestBufferWraparoundTx(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetNagleEnabled(false)
	s.transmitBuffer = NewSocketBuffer(9)
	mustSendSlice(t, s, []byte("xxxyyy"))
	if dequeued := s.transmitBuffer.DequeueMany(3); string(dequeued) != "xxx" {
		t.Fatalf("DequeueMany(3) = %q, want %q", dequeued, "xxx")
	}
	assertTransmitBufferLen(t, s, 3)

	// "abcdef" is not contiguous in the transmit buffer: it wraps around the
	// end of the storage.
	mustSendSlice(t, s, []byte("abcdef"))

	receive(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1)), withPayload([]byte("yyyabc"))))
	receive(t, s, fromLocal(withSeq(localSeq.Add(1+6)), withAck(remoteSeq.Add(1)), withPayload([]byte("def"))))
}
