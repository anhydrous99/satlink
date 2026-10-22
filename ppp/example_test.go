package ppp_test

import (
	"bytes"
	"fmt"
	"time"

	"github.com/anhydrous99/satlink/ppp"
)

// The endpoint's first words: a framed LCP Configure-Request for the
// modem. Its bytes carry a random magic number, so the example prints what
// every run has in common.
func ExampleEndpoint() {
	credentials := ppp.Credentials{Username: "user", Password: "secret"}
	endpoint, err := ppp.NewEndpoint(ppp.NewConfig(ppp.ClientRole(credentials)))
	if err != nil {
		fmt.Println(err)
		return
	}

	endpoint.Open(time.Now())
	toModem := endpoint.TakeOutput() // a framed LCP Configure-Request

	const flag = 0x7E // delimits PPP frames (RFC 1662 §4.1)
	fmt.Println("flag-delimited:", toModem[0] == flag && toModem[len(toModem)-1] == flag)
	fmt.Println("frames:", bytes.Count(toModem, []byte{flag})-1)
	fmt.Printf("header: % X\n", toModem[1:6]) // address, escaped control, the LCP protocol
	// Output:
	// flag-delimited: true
	// frames: 1
	// header: FF 7D 23 C0 21
}
