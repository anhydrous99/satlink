package mobile

import (
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/satnet"
)

// The goroutine that closes a handset, and so the serial port it opened,
// once the call of the link that has it has ended: for a link over a port
// it opened itself (EstablishSerialLink), and for an IridiumHandset closed
// while a link has its handset. The link hangs up through the port, so the
// port must outlive the call.

// portCloserGoroutine names the goroutine that closes a port once the call
// that needs it has ended.
const portCloserGoroutine = "satlink-port-closer"

// closeAfterCall closes handset in the background once the call of the
// link that watcher follows has ended.
func closeAfterCall(watcher *satnet.Watcher, handset *iridium.Handset) {
	go labelled(portCloserGoroutine, func() {
		closeWhenCallEnds(watcher, handset)
	})
}

// closeWhenCallEnds closes handset once the call of the link that watcher
// follows has ended, however it ends. It holds neither the link nor its
// SatelliteLink, so a link the app drops still hangs up and has its port
// closed.
func closeWhenCallEnds(watcher *satnet.Watcher, handset *iridium.Handset) {
	defer logPanic("the port closer")
	for {
		if _, closed := watcher.WaitClosed(listenerPoll); closed {
			break
		}
	}
	// Nobody is left to tell: the call is over, and the port goes with it.
	_ = handset.Close()
}
