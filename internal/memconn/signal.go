package memconn

// signal is a broadcast that waiters can select on together with a timer:
// a waiter takes the current channel, and fire closes it, waking every
// waiter, and puts a fresh one in its place. The Conn's mutex guards it.
type signal struct {
	fired chan struct{}
}

// newSignal returns a signal no one waits for yet.
func newSignal() signal {
	return signal{fired: make(chan struct{})}
}

// channel returns the channel the next fire closes.
func (s *signal) channel() <-chan struct{} {
	return s.fired
}

// fire wakes everything waiting on the current channel.
func (s *signal) fire() {
	close(s.fired)
	s.fired = make(chan struct{})
}
