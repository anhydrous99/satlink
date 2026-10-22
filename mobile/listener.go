package mobile

import (
	"time"

	"github.com/anhydrous99/satlink/satnet"
)

// The goroutine that tells an app's LinkListener about a link's state.

// listenerPoll is how long a goroutine following a link — the listener, or
// a port closer — waits for news before looking again.
const listenerPoll = time.Second

// listenerGoroutine names the listener goroutine, as the Rust bindings
// named its thread.
const listenerGoroutine = "satlink-link-listener"

// deliverStateChanges tells listener about every change of the link's state
// that watcher follows, until the link has closed.
func deliverStateChanges(watcher *satnet.Watcher, listener LinkListener) {
	defer logPanic("the link listener")
	for {
		state, changed := watcher.NextStateChange(listenerPoll)
		if !changed {
			continue
		}
		notifyListener(listener, state)
		if state.Kind == satnet.LinkStateClosed {
			return
		}
	}
}

// notifyListener hands state to listener. An app-side failure must not stop
// the stream of notifications: an error it returns is ignored, and a panic
// is logged.
func notifyListener(listener LinkListener, state satnet.LinkState) {
	defer logPanic("the link listener")
	// The listener's error has nowhere to go: the link carries on, and so do
	// the notifications.
	_ = listener.OnStateChanged(linkStateOf(state))
}
