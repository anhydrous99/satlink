//go:build linux || darwin || freebsd || openbsd

package serial

import (
	"testing"

	serialport "go.bug.st/serial"
)

// otherShape is a port whose fields are not go.bug.st/serial v1.8.0's, as
// after an upgrade that changed them.
type otherShape struct {
	serialport.Port
	handle      string
	closeSignal *int
}

func TestAPortOfAnotherShapeHasNoDescriptorsToMark(t *testing.T) {
	descriptor := 5
	ports := map[string]serialport.Port{
		"none":                    nil,
		"no such fields":          newFakePort(),
		"fields of other kinds":   &otherShape{handle: "5", closeSignal: &descriptor},
		"no close pipe":           &otherShape{handle: "5"},
		"a struct, not a pointer": otherShape{},
		"a nil pointer":           (*otherShape)(nil),
	}
	for name, port := range ports {
		t.Run(name, func(t *testing.T) {
			if descriptors := descriptorsOf(port); len(descriptors) != 0 {
				t.Errorf("descriptorsOf = %v, want none", descriptors)
			}
		})
	}
}
