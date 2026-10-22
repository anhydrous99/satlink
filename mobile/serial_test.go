//go:build !android && (!ios || (macos && !maccatalyst))

package mobile

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/serial"
)

func TestSerialPortListHandsOutCopiesAndNothingOutOfRange(t *testing.T) {
	list := serialPortListOf([]serial.Port{
		{Path: "/dev/ttyACM0", ProductID: 0x9575, Model: "Iridium 9575", SerialNumber: "300234"},
		{Path: "/dev/ttyACM1", ProductID: 0x1234},
	})

	list.Get(0).Path = "changed"

	want := SerialHandsetPort{Path: "/dev/ttyACM0", ProductID: 0x9575, Model: "Iridium 9575", SerialNumber: "300234"}
	if list.Len() != 2 || *list.Get(0) != want || list.Get(1).Model != "" {
		t.Errorf("list holds %d ports, the first %+v, want %+v", list.Len(), *list.Get(0), want)
	}
	for _, index := range []int{-1, 2, math.MaxInt} {
		if got := list.Get(index); got != nil {
			t.Errorf("Get(%d) = %+v, want nil", index, *got)
		}
	}
	if (&SerialHandsetPortList{}).Len() != 0 {
		t.Error("a zero port list is not empty")
	}
}

func TestDiscoveringSerialHandsetsGivesAList(t *testing.T) {
	ports, err := DiscoverSerialHandsets()

	if err != nil || ports == nil {
		t.Errorf("DiscoverSerialHandsets() = %v, %v, want a list, perhaps empty", ports, err)
	}
}

func TestAMissingSerialDeviceIsATransportError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "ttyACM9")
	calls := map[string]func() error{
		"OpenSerialHandset": func() error {
			_, err := OpenSerialHandset(missing, serial.DefaultBaud)
			return err
		},
		"EstablishSerialLink": func() error {
			_, err := EstablishSerialLink(missing, serial.DefaultBaud, NewLinkSettings(), nil, nil)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if got := AsSatlinkError(call()); got == nil || got.Kind != SatlinkErrorTransport {
				t.Errorf("error is %v, want SatlinkErrorTransport", got)
			}
		})
	}
}

func TestASerialLinkChecksItsSettingsBeforeOpeningThePort(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "ttyACM9")
	refused := NewLinkSettings()
	refused.Allow("not a rule")
	cases := []struct {
		name     string
		settings *LinkSettings
	}{
		{name: "nil settings", settings: nil},
		{name: "a rule that does not parse", settings: refused},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := EstablishSerialLink(missing, serial.DefaultBaud, c.settings, nil, nil)

			if got := AsSatlinkError(err); got == nil || got.Kind != SatlinkErrorInvalidArgument {
				t.Errorf("error is %v, want SatlinkErrorInvalidArgument before the port is opened", got)
			}
		})
	}
}

func TestASerialLinksPortClosesOnceItsCallHasEnded(t *testing.T) {
	t.Parallel()
	port := newClosingPort(t)
	link, err := establishPortLink(port, linkConfig(t, NewLinkSettings()), nil)
	if err != nil {
		t.Fatalf("establishPortLink: %v", err)
	}
	if port.closed.Load() {
		t.Fatal("the port closed while the call was up")
	}

	if _, err := link.HangUp(linkTimeoutMillis); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	simtest.Eventually(t, portPatience, "the link closes its port", port.closed.Load)
}

func TestASerialLinksListenerHearsEveryChange(t *testing.T) {
	t.Parallel()
	listener := &StateRecorder{}
	link, err := establishPortLink(newClosingPort(t), linkConfig(t, NewLinkSettings()), listener)
	if err != nil {
		t.Fatalf("establishPortLink: %v", err)
	}
	HangUpAtCleanup(t, link)

	if _, err := link.HangUp(linkTimeoutMillis); err != nil {
		t.Fatalf("HangUp: %v", err)
	}

	AssertHeardEveryChange(t, listener)
}

// establishPortLinkAndDrop brings a link up over port and lets go of it. It
// is not inlined, so that nothing of it stays on the caller's stack.
//
//go:noinline
func establishPortLinkAndDrop(t *testing.T, port *closingPort) {
	t.Helper()
	if _, err := establishPortLink(port, linkConfig(t, NewLinkSettings()), nil); err != nil {
		t.Fatalf("establishPortLink: %v", err)
	}
}

func TestACollectedSerialLinkStillHangsUpAndClosesItsPort(t *testing.T) {
	// Its port closer must not keep the link reachable: only the link's
	// runtime cleanup ends a call the app merely let go of.
	t.Parallel()
	port := newClosingPort(t)

	establishPortLinkAndDrop(t, port)

	CollectGarbageUntil(t, "the dropped link closes its port", port.closed.Load)
	if port.control.Stats().InDataCall {
		t.Error("the call is still up")
	}
}

// A serial port this package opened itself is closed on every way out that
// does not hand it on, a fault recovered at the boundary included: the port
// is free for the next
// OpenSerialHandset, and closing it drops DTR, which ends a call the fault
// left up.

func TestAPanicWhileTheHandsetIsInitialisedClosesItsPort(t *testing.T) {
	t.Parallel()
	config := linkConfig(t, NewLinkSettings())
	calls := map[string]func(port *closingPort) error{
		"opening a handset": func(port *closingPort) error {
			_, err := newIridiumHandset(port)
			return err
		},
		"setting a link up": func(port *closingPort) error {
			_, err := establishPortLink(port, config, nil)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			port := newClosingPort(t)
			port.PanicsOnNextRead.Store(true)

			err := acrossTheBoundary(func() error { return call(port) })

			assertSatlinkError(t, err,
				&SatlinkError{Kind: SatlinkErrorInternal, Reason: "a transport bug"},
				"internal error: a transport bug")
			if !port.closed.Load() {
				t.Error("the port is still open after the panic")
			}
		})
	}
}

func TestAPanicOnceTheCallIsUpClosesTheSerialPortWhichEndsTheCall(t *testing.T) {
	t.Parallel()
	port := &connectPanickingPort{closingPort: newClosingPort(t)}
	// A handset deaf to the DTR pulse the panic sets off still sees the
	// port close, which drops DTR whatever the switch says.
	port.DeafToDTR.Store(true)
	config := linkConfig(t, NewLinkSettings())

	err := acrossTheBoundary(func() error {
		_, err := establishPortLink(port, config, nil)
		return err
	})

	assertSatlinkError(t, err,
		&SatlinkError{Kind: SatlinkErrorInternal, Reason: bugOnceConnected},
		"internal error: "+bugOnceConnected)
	if !port.closed.Load() {
		t.Fatal("the port is still open after the panic")
	}
	simtest.Eventually(t, portPatience, "dropping DTR ends the call", func() bool {
		return !port.control.Stats().InDataCall
	})
}

func TestASerialLinkThatDoesNotComeUpClosesItsPortAtOnce(t *testing.T) {
	t.Parallel()
	undialable := NewLinkSettings()
	undialable.Number = "not a number"
	cases := map[string]func(*closingPort) *LinkSettings{
		"a handset that does not answer": func(port *closingPort) *LinkSettings {
			port.Unplugged.Store(true)
			return NewLinkSettings()
		},
		"a number that cannot be dialled": func(*closingPort) *LinkSettings { return undialable },
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			port := newClosingPort(t)
			settings := arrange(port)

			_, err := establishPortLink(port, linkConfig(t, settings), nil)

			if err == nil || !port.closed.Load() {
				t.Errorf("establishPortLink failed with %v, port closed %t; want a failure and a closed port", err, port.closed.Load())
			}
		})
	}
}
