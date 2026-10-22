package serial

import (
	"errors"
	"slices"
	"testing"

	"go.bug.st/serial/enumerator"

	"github.com/anhydrous99/satlink"
)

func TestDiscoverKeepsIridiumUSBPorts(t *testing.T) {
	for _, test := range discoveryCases() {
		t.Run(test.name, func(t *testing.T) {
			lister := listerOf([]*enumerator.PortDetails{test.detail}, nil)

			ports, err := discover(lister)

			if err != nil || !slices.Equal(ports, test.want) {
				t.Errorf("discover = %+v, %v; want %+v", ports, err, test.want)
			}
		})
	}
}

// discoveryCase is a port as the enumerator details it, and what Discover
// makes of it.
type discoveryCase struct {
	name   string
	detail *enumerator.PortDetails
	want   []Port
}

// discoveryCases are the ports Discover must keep, name, or leave out.
func discoveryCases() []discoveryCase {
	return []discoveryCase{
		{
			name:   "a known product, the vendor ID in upper case",
			detail: &enumerator.PortDetails{Name: "/dev/cu.usbmodem1101", IsUSB: true, VID: "1EDD", PID: "9575", SerialNumber: "300234010123450"},
			want:   []Port{{Path: "/dev/cu.usbmodem1101", ProductID: 0x9575, Model: "Iridium 9575", SerialNumber: "300234010123450"}},
		},
		{
			name:   "a known product, the vendor ID in lower case",
			detail: &enumerator.PortDetails{Name: "/dev/ttyACM0", IsUSB: true, VID: "1edd", PID: "a555"},
			want:   []Port{{Path: "/dev/ttyACM0", ProductID: 0xA555, Model: "Iridium 9555A"}},
		},
		{
			name:   "an unknown product",
			detail: &enumerator.PortDetails{Name: "/dev/ttyACM1", IsUSB: true, VID: "1EDD", PID: "1234"},
			want:   []Port{{Path: "/dev/ttyACM1", ProductID: 0x1234}},
		},
		{
			name:   "a product ID the enumerator garbled",
			detail: &enumerator.PortDetails{Name: "/dev/ttyACM2", IsUSB: true, VID: "1EDD", PID: "12345"},
			want:   []Port{{Path: "/dev/ttyACM2"}},
		},
		{
			name:   "another vendor",
			detail: &enumerator.PortDetails{Name: "/dev/ttyUSB0", IsUSB: true, VID: "0403", PID: "6001"},
		},
		{
			name:   "a missing vendor ID",
			detail: &enumerator.PortDetails{Name: "/dev/ttyUSB1", IsUSB: true, PID: "9575"},
		},
		{
			name:   "a port that is not USB",
			detail: &enumerator.PortDetails{Name: "/dev/ttyS0", VID: "1EDD", PID: "9575"},
		},
		{
			name: "no details at all",
		},
	}
}

func TestDiscoverKeepsTheEnumeratorsOrder(t *testing.T) {
	details := []*enumerator.PortDetails{
		{Name: "/dev/ttyACM1", IsUSB: true, VID: "1EDD", PID: "9555"},
		{Name: "/dev/ttyUSB0", IsUSB: true, VID: "0403", PID: "6001"},
		{Name: "/dev/ttyACM0", IsUSB: true, VID: "1EDD", PID: "520C"},
	}

	ports, err := discover(listerOf(details, nil))

	want := []Port{
		{Path: "/dev/ttyACM1", ProductID: 0x9555, Model: "Iridium 9555"},
		{Path: "/dev/ttyACM0", ProductID: 0x520C, Model: "Iridium ROC (service port)"},
	}
	if err != nil || !slices.Equal(ports, want) {
		t.Errorf("discover = %+v, %v; want %+v", ports, err, want)
	}
}

func TestDiscoverProbesNoDevice(t *testing.T) {
	var filters int
	lister := func(activeUSBProbeFilters ...func(vid, pid string) bool) ([]*enumerator.PortDetails, error) {
		filters = len(activeUSBProbeFilters)
		return nil, nil
	}

	if _, err := discover(lister); err != nil {
		t.Fatalf("discover: %v", err)
	}

	if filters != 0 {
		t.Errorf("discover asked to probe with %d filters: probing may disturb a handset", filters)
	}
}

func TestDiscoverReportsAnEnumerationFailure(t *testing.T) {
	_, err := discover(listerOf(nil, errPort))

	if !errors.Is(err, satlink.ErrTransport) || !errors.Is(err, errPort) {
		t.Errorf("discover = %v, want ErrTransport wrapping the enumerator's error", err)
	}
}

// listerOf returns a portLister that lists details, or fails with err.
func listerOf(details []*enumerator.PortDetails, err error) portLister {
	return func(...func(vid, pid string) bool) ([]*enumerator.PortDetails, error) {
		return details, err
	}
}
