package tcp

// The tests of smoltcp's socket/tcp.rs for packet filtering
// (src/socket/tcp.rs, lines 9484-9549).

import (
	"testing"

	"github.com/anhydrous99/satlink/internal/ipstack/wire"
)

func TestDoesntAcceptWrongPort(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	replaceReceiveBuffer(s, smallBufferLen)
	toAnotherPort := fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withDstPort(localPort+1))
	fromAnotherPort := fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withSrcPort(remotePort+1))
	tests := []struct {
		name    string
		ip      wire.IPv4Repr
		segment wire.TCPRepr
	}{
		{"to another local port", sendIPTemplate, toAnotherPort},
		{"from another remote port", sendIPTemplate, fromAnotherPort},
		// The cases above are smoltcp's. Its SEND_IP_TEMPL has the
		// connection's addresses swapped, so they would be refused whatever
		// their ports; in a datagram from the peer, the port is the only thing
		// wrong.
		{"to another local port from the peer", ipHeader(remoteAddr, localAddr, toAnotherPort), toAnotherPort},
		{"from another remote port of the peer", ipHeader(remoteAddr, localAddr, fromAnotherPort), fromAnotherPort},
	}

	for _, test := range tests {
		if s.Accepts(test.ip, test.segment) {
			t.Errorf("the socket accepted a segment %s", test.name)
		}
	}
}

func TestDoesntAcceptWrongIP(t *testing.T) {
	t.Parallel()
	s := socketEstablished(t)
	segment := fromRemote(withSeq(remoteSeq.Add(1)), withAck(localSeq.Add(1)), withPayload([]byte("abcdef")))
	tests := []struct {
		name         string
		ip           wire.IPv4Repr
		wantAccepted bool
	}{
		{"from the peer", ipHeader(remoteAddr, localAddr, segment), true},
		{"from another source", ipHeader(otherAddr, localAddr, segment), false},
		{"to another destination", ipHeader(remoteAddr, otherAddr, segment), false},
	}

	for _, test := range tests {
		if accepted := s.Accepts(test.ip, segment); accepted != test.wantAccepted {
			t.Errorf("Accepts of a segment %s = %v, want %v", test.name, accepted, test.wantAccepted)
		}
	}
}
