package tcp

// The tests of smoltcp's socket/tcp.rs for delayed ACKs (src/socket/tcp.rs,
// lines 9160-9357).

import (
	"testing"
	"time"
)

func TestDelayedACK(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetAckDelay(defaultACKDelay)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))

	// No ACK is immediately sent.
	receiveNothing(t, s)

	// After 10ms, it is sent.
	receive(t, s.at(11*time.Millisecond), fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+3)), withWindow(61)))
}

func TestDelayedACKWin(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetAckDelay(defaultACKDelay)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))

	// Reading the data off the buffer should cause a window update.
	receiveExactly(t, s, "abc")

	// However, no ACK or window update is immediately sent.
	receiveNothing(t, s)

	// After 10ms, it is sent.
	receive(t, s.at(11*time.Millisecond), fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+3))))
}

func TestDelayedACKReply(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	s.SetAckDelay(defaultACKDelay)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abc"))))
	receiveExactly(t, s, "abc")

	mustSendSlice(t, s, []byte("xyz"))

	// Writing data to the socket causes ACK to not be delayed, because it is
	// immediately sent with the data.
	receive(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+3)), withPayload([]byte("xyz"))))
}

func TestDelayedACKEveryRMSS(t *testing.T) {
	t.Parallel()
	s := socketEstablishedWithBufferSizes(t, defaultMSS*2, defaultMSS*2)
	s.SetAckDelay(defaultACKDelay)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload(make([]byte, defaultMSS-1))))

	// No ACK is immediately sent.
	receiveNothing(t, s)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+(defaultMSS-1))), withAck(localSeq.Add(1)), withPayload([]byte("a"))))

	// No ACK is immediately sent.
	receiveNothing(t, s)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+defaultMSS)), withAck(localSeq.Add(1)), withPayload([]byte("a"))))

	// RMSS+1 bytes of data has been received, so ACK is sent without delay.
	receive(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+(defaultMSS+1))), withWindow(defaultMSS-1)))
}

func TestDelayedACKEveryRMSSOrMore(t *testing.T) {
	t.Parallel()
	s := socketEstablishedWithBufferSizes(t, defaultMSS*2, defaultMSS*2)
	s.SetAckDelay(defaultACKDelay)
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload(make([]byte, defaultMSS))))

	// No ACK is immediately sent.
	receiveNothing(t, s)

	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+defaultMSS)), withAck(localSeq.Add(1)), withPayload([]byte("a"))))
	send(t, s, fromRemote(withSeq(remoteSeq.Add(1+(defaultMSS+1))), withAck(localSeq.Add(1)), withPayload([]byte("b"))))

	// RMSS+2 bytes of data has been received, so ACK is sent without delay.
	receive(t, s, fromLocal(withSeq(localSeq.Add(1)), withAck(remoteSeq.Add(1+(defaultMSS+2))), withWindow(defaultMSS-2)))
}
