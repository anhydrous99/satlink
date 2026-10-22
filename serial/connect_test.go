package serial

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	serialport "go.bug.st/serial"

	"github.com/anhydrous99/satlink"
)

func TestChoosePortTakesTheGivenPathWithoutLooking(t *testing.T) {
	discoverPorts := func() ([]Port, error) {
		t.Error("ChoosePort looked for handsets although a path was given")
		return nil, nil
	}

	path, err := choosePort("/dev/ttyACM3", discoverPorts)

	if err != nil || path != "/dev/ttyACM3" {
		t.Errorf("choosePort = %q, %v; want the given path", path, err)
	}
}

func TestChoosePortTakesTheOnlyHandset(t *testing.T) {
	path, err := choosePort("", discovering(Port{Path: "/dev/ttyACM0"}))

	if err != nil || path != "/dev/ttyACM0" {
		t.Errorf("choosePort = %q, %v; want /dev/ttyACM0", path, err)
	}
}

func TestChoosePortWithoutAHandsetSaysSo(t *testing.T) {
	_, err := choosePort("", discovering())

	assertTransportMessage(t, err, "no Iridium handset found on USB")
}

func TestChoosePortWithSeveralHandsetsListsThem(t *testing.T) {
	_, err := choosePort("", discovering(Port{Path: "/dev/ttyACM0"}, Port{Path: "/dev/ttyACM1"}))

	assertTransportMessage(t, err, "several handsets found, choose one: /dev/ttyACM0, /dev/ttyACM1")
}

func TestChoosePortFromChoosesAsChoosePortDoes(t *testing.T) {
	// The choice among ports already discovered.
	two := []Port{{Path: "/dev/ttyACM0"}, {Path: "/dev/ttyACM1"}}
	if path, err := ChoosePortFrom(two[:1]); err != nil || path != "/dev/ttyACM0" {
		t.Errorf("ChoosePortFrom(one port) = %q, %v; want its path", path, err)
	}
	_, err := ChoosePortFrom(nil)
	assertTransportMessage(t, err, "no Iridium handset found on USB")
	_, err = ChoosePortFrom(two)
	assertTransportMessage(t, err, "several handsets found, choose one: /dev/ttyACM0, /dev/ttyACM1")
}

func TestChoosePortPassesADiscoveryFailureOn(t *testing.T) {
	failure := &satlink.Error{Kind: satlink.ErrTransport, Err: errPort}

	_, err := choosePort("", func() ([]Port, error) { return nil, failure })

	if err != failure {
		t.Errorf("choosePort = %v, want the discovery's error", err)
	}
}

func TestConnectInitialisesTheHandset(t *testing.T) {
	port := newFakePort()
	port.answersOK = true

	handset, err := connect("/dev/ttyACM0", DefaultBaud, openerOf(port), discovering())

	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	for _, command := range []string{"AT\r", "AT+CPAS\r", "ATE0\r", "ATV1\r", "AT+CMEE=2\r", "AT+CMGF=1\r"} {
		if !strings.Contains(string(port.written), command) {
			t.Errorf("written %q, want %q among it", port.written, command)
		}
	}
	if port.closeCount() != 0 {
		t.Error("the port of an initialised handset was closed")
	}
	if err := handset.Close(); err != nil || port.closeCount() != 1 {
		t.Errorf("handset Close = %v after %d port closes", err, port.closeCount())
	}
}

func TestConnectClosesThePortWhenInitializeFails(t *testing.T) {
	// A port that takes no data fails Initialize at once; one that never
	// answered would take Initialize half a minute to give up on.
	port := newFakePort()
	port.fails = true

	_, err := connect("/dev/ttyACM0", DefaultBaud, openerOf(port), discovering())

	if !errors.Is(err, errPort) {
		t.Errorf("connect = %v, want the initialisation's error", err)
	}
	if port.closeCount() != 1 {
		t.Errorf("the port was closed %d times, want once", port.closeCount())
	}
}

func TestConnectClosesThePortWhenInitializePanics(t *testing.T) {
	// A panic unwinds past the open port, which must be closed: a caller
	// that recovers the panic must not be left holding a port open, with
	// DTR up.
	port := newFakePort()
	port.readPanic = func() { panic("a port bug") }

	panicked := panicOf(func() {
		_, _ = connect("/dev/ttyACM0", DefaultBaud, openerOf(port), discovering())
	})

	if panicked != "a port bug" {
		t.Errorf("connect panicked with %v, want the port's panic", panicked)
	}
	if port.closeCount() != 1 {
		t.Errorf("the port was closed %d times, want once", port.closeCount())
	}
}

func TestConnectReportsAPortThatDoesNotOpen(t *testing.T) {
	opener := func(string, *serialport.Mode) (serialport.Port, error) { return nil, errPort }

	_, err := connect("/dev/ttyACM0", DefaultBaud, opener, discovering())

	if !errors.Is(err, satlink.ErrTransport) || !errors.Is(err, errPort) {
		t.Errorf("connect = %v, want ErrTransport wrapping the library's error", err)
	}
}

func TestConnectWithoutAPathOpensTheOnlyHandset(t *testing.T) {
	port := newFakePort()
	port.answersOK = true
	var openedPath string
	opener := func(path string, _ *serialport.Mode) (serialport.Port, error) {
		openedPath = path
		return port, nil
	}

	handset, err := connect("", DefaultBaud, opener, discovering(Port{Path: "/dev/ttyACM2"}))

	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if openedPath != "/dev/ttyACM2" {
		t.Errorf("opened %q, want the handset discovered", openedPath)
	}
	if err := handset.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestConnectWithoutAPathOrAHandsetOpensNothing(t *testing.T) {
	opener := func(path string, _ *serialport.Mode) (serialport.Port, error) {
		t.Errorf("connect opened %q, yet no handset was found", path)
		return nil, errPort
	}

	_, err := connect("", DefaultBaud, opener, discovering())

	assertTransportMessage(t, err, "no Iridium handset found on USB")
}

func TestChoosePortTakesAGivenPath(t *testing.T) {
	path, err := ChoosePort("/dev/ttyACM9")

	if err != nil || path != "/dev/ttyACM9" {
		t.Errorf("ChoosePort = %q, %v; want the given path", path, err)
	}
}

func TestOpeningADeviceThatDoesNotExistFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-port")

	_, openErr := Open(missing, DefaultBaud)
	_, connectErr := Connect(missing, DefaultBaud)

	for _, err := range []error{openErr, connectErr} {
		if !errors.Is(err, satlink.ErrTransport) {
			t.Errorf("error = %v, want ErrTransport", err)
		}
	}
}

func TestOpeningADeviceThatDoesNotExistSaysSoAsTheCLibraryDoes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows the library reports a missing port with an error of its own")
	}
	missing := filepath.Join(t.TempDir(), "no-such-port")

	_, err := Open(missing, DefaultBaud)

	if got, want := fmt.Sprint(err), "transport error: No such file or directory"; got != want {
		t.Errorf("Open = %q, want %q", got, want)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open = %v, want it to match fs.ErrNotExist", err)
	}
}

// discovering returns a discovery that finds ports.
func discovering(ports ...Port) func() ([]Port, error) {
	return func() ([]Port, error) { return ports, nil }
}
