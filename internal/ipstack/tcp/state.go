package tcp

import "strconv"

// State is the state of a TCP socket in the RFC 793 state machine. The zero
// State is StateClosed.
type State int

// The states of RFC 793 § 3.2, with its descriptions.
const (
	// StateClosed is no connection at all: the socket is neither listening
	// nor connected, and can be reused or removed.
	StateClosed State = iota
	// StateListen is waiting for a connection request from any remote TCP
	// and port.
	StateListen
	// StateSynSent is waiting for a matching connection request after
	// having sent one.
	StateSynSent
	// StateSynReceived is waiting for a confirming acknowledgement of a
	// connection request after having both received and sent one.
	StateSynReceived
	// StateEstablished is an open connection: data received can be
	// delivered to the user, and the normal state for data transfer.
	StateEstablished
	// StateFinWait1 is waiting for a connection termination request from
	// the remote TCP, or an acknowledgement of the termination request
	// previously sent.
	StateFinWait1
	// StateFinWait2 is waiting for a connection termination request from
	// the remote TCP.
	StateFinWait2
	// StateCloseWait is waiting for a connection termination request from
	// the local user (Close), after the remote TCP has closed its side.
	StateCloseWait
	// StateClosing is waiting for an acknowledgement of the connection
	// termination request sent, after receiving the remote TCP's own.
	StateClosing
	// StateLastAck is waiting for an acknowledgement of the connection
	// termination request sent after the remote TCP's.
	StateLastAck
	// StateTimeWait is waiting for enough time to pass to be sure the
	// remote TCP received the acknowledgement of its connection termination
	// request.
	StateTimeWait
)

var stateNames = [...]string{
	StateClosed:      "CLOSED",
	StateListen:      "LISTEN",
	StateSynSent:     "SYN-SENT",
	StateSynReceived: "SYN-RECEIVED",
	StateEstablished: "ESTABLISHED",
	StateFinWait1:    "FIN-WAIT-1",
	StateFinWait2:    "FIN-WAIT-2",
	StateCloseWait:   "CLOSE-WAIT",
	StateClosing:     "CLOSING",
	StateLastAck:     "LAST-ACK",
	StateTimeWait:    "TIME-WAIT",
}

// String returns the state's RFC 793 name ("SYN-SENT").
func (s State) String() string {
	if s < 0 || int(s) >= len(stateNames) {
		return "State(" + strconv.Itoa(int(s)) + ")"
	}
	return stateNames[s]
}
