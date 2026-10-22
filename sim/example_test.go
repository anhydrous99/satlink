package sim_test

import (
	"fmt"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/sim"
)

// A simulated handset stands in for a real one behind iridium.Handset, and
// its control plays the network's part.
func Example() {
	simulator := sim.New(sim.DefaultConfig())
	control := simulator.Control()
	handset := iridium.NewHandset(simulator)
	if err := handset.Initialize(); err != nil {
		fmt.Println(err)
		return
	}
	bars, err := handset.SignalQuality()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("signal:", bars, "bars")

	control.ReceiveSMS("+881631234567", "Hello from orbit")
	inbox, err := handset.ListSMS(iridium.SMSFilterUnread)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("message:", inbox[0].Text)
	// Output:
	// signal: 4 bars
	// message: Hello from orbit
}
