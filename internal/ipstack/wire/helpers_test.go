package wire

import (
	"net/netip"
	"testing"
)

// The addresses the TCP and UDP tests send segments between.
var (
	testSrcAddr = netip.MustParseAddr("192.168.1.1")
	testDstAddr = netip.MustParseAddr("192.168.1.2")
)

// addressesThatAreNotIPv4 are addresses the functions that write or checksum
// an address must refuse.
var addressesThatAreNotIPv4 = map[string]netip.Addr{
	"IPv6":        netip.MustParseAddr("2001:db8::1"),
	"IPv4-mapped": netip.MustParseAddr("::ffff:192.168.1.1"),
	"zero Addr":   {},
}

// field is the value an accessor returned and the value it should have.
type field struct {
	name      string
	got, want any
}

// checkFields reports every field whose value differs from the wanted one.
func checkFields(t *testing.T, fields []field) {
	t.Helper()
	for _, f := range fields {
		if f.got != f.want {
			t.Errorf("%s = %v, want %v", f.name, f.got, f.want)
		}
	}
}

// recoverPanic runs action and returns the value it panicked with, or nil.
func recoverPanic(action func()) (recovered any) {
	defer func() { recovered = recover() }()
	action()
	return nil
}

// expectPanic reports an error unless action panics.
func expectPanic(t *testing.T, description string, action func()) {
	t.Helper()
	if recoverPanic(action) == nil {
		t.Errorf("%s did not panic", description)
	}
}

// expectPanicWith reports an error unless action panics with message.
func expectPanicWith(t *testing.T, message string, action func()) {
	t.Helper()
	if recovered := recoverPanic(action); recovered != message {
		t.Errorf("panicked with %v, want %q", recovered, message)
	}
}

// filled returns a buffer of length octets, each set to octet. The tests
// emit into buffers filled with 0xa5 to show that every octet is
// written.
func filled(length int, octet byte) []byte {
	buffer := make([]byte, length)
	for i := range buffer {
		buffer[i] = octet
	}
	return buffer
}
