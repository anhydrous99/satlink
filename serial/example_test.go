package serial_test

import (
	"errors"
	"log"
	"time"

	"github.com/anhydrous99/satlink/satnet"
	"github.com/anhydrous99/satlink/serial"
)

// This example is the README's quick start: it opens the only handset on
// USB, sends a greeting over TLS to the one destination the policy allows,
// and hangs up. It needs a handset, so it is compiled but not run.
func Example() {
	if err := greetOverTLS(); err != nil {
		log.Print(err)
	}
}

// greetOverTLS is the example's work. It closes the link on every path
// once it is up: airtime is billed by the minute.
func greetOverTLS() error {
	handset, err := serial.Connect("", serial.DefaultBaud) // the only handset on USB
	if err != nil {
		return err
	}
	defer handset.Close()

	policy, err := satnet.ParsePolicy("relay.example.com:443")
	if err != nil {
		return err
	}
	link, err := satnet.Establish(handset, satnet.NewConfig(policy)) // about 30 s
	if err != nil {
		return err
	}
	tls, err := satnet.NewTLSConnector(satnet.TLSOptions{}) // one for the whole app
	if err == nil {
		var conn *satnet.TLSConn
		conn, err = link.DialTLS("relay.example.com", 443, tls, 2*time.Minute)
		if err == nil {
			_, err = conn.Write([]byte("hello from the sky"))
		}
	}
	// Close delivers what was written, then hangs up, even after a failure:
	// airtime is billed by the minute. The handset is then free for SMS.
	return errors.Join(err, link.Close())
}
