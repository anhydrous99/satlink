// Package memconn is an in-memory connection between crypto/tls and code
// that moves bytes itself, at its own pace: the simulator's TLS echo, whose
// records come from and go to a TCP socket of the simulated internet, and
// the TLS client of the simulator's tests, pumped to and from a client
// socket. crypto/tls has no sans-I/O interface, so it runs in a goroutine of
// its own over a [Conn], and the owner pumps the other side.
//
// A Conn has two sides:
//   - the net.Conn side, for crypto/tls: Read blocks for bytes from the
//     peer and honours the read deadline; Write never blocks, queueing what
//     crypto/tls writes for the owner; Close ends the connection;
//   - the pump side, for the owner: [Conn.Inject] hands over bytes from the
//     peer, [Conn.CloseInbound] the peer's FIN, [Conn.Extract] takes what
//     crypto/tls wrote, and [Conn.AwaitReader] waits until crypto/tls has
//     dealt with everything injected, so that one step of the owner can
//     carry a request and its answer. [Conn.Pending], [Conn.EOFDelivered]
//     and [Conn.Closed] report the state the owner decides on.
//
// Nothing ever crosses a network or the operating system.
package memconn
