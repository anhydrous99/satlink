package satnet_test

import (
	"errors"
	"log"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/satnet"
)

// This example sends a greeting to the one destination the policy allows,
// then hangs up. It needs an initialised handset, such as package serial
// connects, so it is compiled but not run.
func Example() {
	var handset *satlink.Handset // satlink.NewHandset over a serial.Transport, initialised
	if err := greet(handset); err != nil {
		log.Print(err)
	}
}

// greet is the example's work. Where the Rust documentation returns early
// and lets the dropped link hang up, it closes the link on every path: Go
// drops nothing, and a link left to the garbage collector hangs up only
// once it runs.
func greet(handset *satlink.Handset) error {
	policy, err := satnet.ParsePolicy("relay.example.com:443")
	if err != nil {
		return err
	}
	link, err := satnet.Establish(handset, satnet.NewConfig(policy)) // about 30 s
	if err != nil {
		return err
	}
	conn, err := link.Dial("relay.example.com", 443, time.Minute)
	if err == nil {
		_, err = conn.Write([]byte("hello from the sky"))
	}
	// Closing delivers what was written, then hangs up, even after a
	// failure: airtime is billed by the minute. The handset is back in
	// command mode, ready for SMS.
	return errors.Join(err, link.Close())
}
