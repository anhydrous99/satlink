package satnet_test

import (
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/satnet"
)

// This example sends a greeting to the one destination the policy allows,
// then hangs up. It needs an initialised handset, such as package serial
// connects, so it is compiled but not run.
func Example() {
	var handset *iridium.Handset // iridium.NewHandset over a serial.Transport, initialised
	if err := greet(handset); err != nil {
		log.Print(err)
	}
}

// greet is the example's work. It closes the link on every path, including
// early returns, rather than relying on a dropped link to hang up: a link left
// to the garbage collector hangs up only once it runs.
func greet(handset *iridium.Handset) error {
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

// This example fetches a page with net/http over the link. The transport
// dials through the link, and its TLS configuration shares the connector's
// session cache and trust settings, so later requests resume their
// session. It needs an initialised handset, so it is compiled but not run.
func ExampleLink_DialContext() {
	var handset *iridium.Handset // iridium.NewHandset over a serial.Transport, initialised
	if err := fetch(handset, "https://api.example.com/status"); err != nil {
		log.Print(err)
	}
}

// fetch is the example's work. It closes the link on every path once it
// is up.
func fetch(handset *iridium.Handset, url string) error {
	policy, err := satnet.ParsePolicy("api.example.com:443")
	if err != nil {
		return err
	}
	connector, err := satnet.NewTLSConnector(satnet.TLSOptions{}) // one for the whole app
	if err != nil {
		return err
	}
	link, err := satnet.Establish(handset, satnet.NewConfig(policy)) // about 30 s
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext:     link.DialContext,
		TLSClientConfig: connector.Config(),
	}}
	response, err := client.Get(url)
	if err == nil {
		_, err = io.Copy(io.Discard, response.Body)
		err = errors.Join(err, response.Body.Close())
	}
	return errors.Join(err, link.Close())
}
