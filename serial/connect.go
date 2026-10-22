package serial

import (
	"strings"

	"github.com/anhydrous99/satlink/iridium"
)

// ChoosePort returns the serial device to use: path if it is not empty,
// otherwise the only handset [Discover] finds, chosen as [ChoosePortFrom]
// chooses.
func ChoosePort(path string) (string, error) {
	return choosePort(path, Discover)
}

// ChoosePortFrom returns the path of the only handset among ports, those
// [Discover] found, so that a program that has listed the handsets chooses
// as [ChoosePort] does. It fails with "no Iridium handset found on USB"
// when there is none, or with "several handsets found, choose one: …"
// listing their paths.
func ChoosePortFrom(ports []Port) (string, error) {
	switch len(ports) {
	case 0:
		return "", &iridium.Error{Kind: iridium.ErrTransport, Message: "no Iridium handset found on USB"}
	case 1:
		return ports[0].Path, nil
	default:
		paths := make([]string, len(ports))
		for i, port := range ports {
			paths[i] = port.Path
		}
		return "", &iridium.Error{
			Kind:    iridium.ErrTransport,
			Message: "several handsets found, choose one: " + strings.Join(paths, ", "),
		}
	}
}

// choosePort is ChoosePort, discovering handsets with discoverPorts.
func choosePort(path string, discoverPorts func() ([]Port, error)) (string, error) {
	if path != "" {
		return path, nil
	}
	found, err := discoverPorts()
	if err != nil {
		return "", err
	}
	return ChoosePortFrom(found)
}

// Connect opens path — or, when path is empty, the only handset discovered
// — at baud and initialises the handset (iridium.Handset.Initialize). If
// initialising fails, a panic included, the port is closed again.
func Connect(path string, baud int) (*iridium.Handset, error) {
	return connect(path, baud, openCloseOnExec, Discover)
}

// connect is Connect, opening the port with openPort and discovering
// handsets with discoverPorts.
func connect(path string, baud int, openPort portOpener, discoverPorts func() ([]Port, error)) (*iridium.Handset, error) {
	path, err := choosePort(path, discoverPorts)
	if err != nil {
		return nil, err
	}
	transport, err := open(path, baud, openPort)
	if err != nil {
		return nil, err
	}
	handset := iridium.NewHandset(transport)
	initialized := false
	defer func() {
		if !initialized {
			// Closed on every way out but success, a panic included: closing
			// gives the port back and drops DTR. The initialisation's error, or the panic,
			// is what the caller needs; a failed close adds nothing it could
			// act on.
			_ = handset.Close()
		}
	}()
	if err := handset.Initialize(); err != nil {
		return nil, err
	}
	initialized = true
	return handset, nil
}
