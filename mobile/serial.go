//go:build !android && (!ios || (macos && !maccatalyst))

package mobile

import (
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/satnet"
	"github.com/anhydrous99/satlink/serial"
)

// Desktop serial-port access. Android, iOS and Mac Catalyst builds leave it
// out: apps there cannot open serial devices, and the serial library's
// port discovery needs the desktop operating systems' APIs. For gomobile's
// macOS target, gobind reads this package with GOOS=ios and the macos tag,
// and gomobile compiles it with GOOS=darwin and the macos tag, against the
// macOS SDK: the constraint keeps the serial API in both. On macOS it needs
// cgo, which gomobile builds use anyway.

// SerialHandsetPort is a serial port that belongs to an Iridium handset.
type SerialHandsetPort struct {
	// Path is the device, such as /dev/cu.usbmodem1101 or /dev/ttyACM0.
	Path string
	// ProductID is the USB product ID.
	ProductID int
	// Model is the model name of a known product; "" for others.
	Model string
	// SerialNumber is the USB serial number; "" when the device reports
	// none.
	SerialNumber string
}

// SerialHandsetPortList is a list of serial ports.
type SerialHandsetPortList struct {
	items []SerialHandsetPort
}

// Len returns the number of ports.
func (l *SerialHandsetPortList) Len() int {
	return len(l.items)
}

// Get returns a copy of the port at index, or nil when index is out of
// range.
func (l *SerialHandsetPortList) Get(index int) *SerialHandsetPort {
	if index < 0 || index >= len(l.items) {
		return nil
	}
	port := l.items[index]
	return &port
}

// DiscoverSerialHandsets lists the serial ports that belong to Iridium
// handsets.
func DiscoverSerialHandsets() (ports *SerialHandsetPortList, err error) {
	defer recoverPanic(&err)
	found, err := serial.Discover()
	if err != nil {
		return nil, fromError(err)
	}
	return serialPortListOf(found), nil
}

// serialPortListOf returns the app's copy of ports.
func serialPortListOf(ports []serial.Port) *SerialHandsetPortList {
	list := &SerialHandsetPortList{items: make([]SerialHandsetPort, len(ports))}
	for i, port := range ports {
		list.items[i] = SerialHandsetPort{
			Path:         port.Path,
			ProductID:    int(port.ProductID),
			Model:        port.Model,
			SerialNumber: port.SerialNumber,
		}
	}
	return list
}

// OpenSerialHandset opens a serial device such as /dev/cu.usbmodem1101 or
// /dev/ttyACM0 at baud and initialises the handset.
func OpenSerialHandset(path string, baud int) (handset *IridiumHandset, err error) {
	defer recoverPanic(&err)
	port, err := serial.Open(path, baud)
	if err != nil {
		return nil, fromError(err)
	}
	return newIridiumHandset(port)
}

// EstablishSerialLink dials through a serial device and brings the link
// up, unless cancellation, if not nil, stops it, as EstablishLink does over
// a ByteTransport. The link keeps the port, which only one may open, until
// its call has ended. A fault inside the library while the link is set up
// (SatlinkErrorInternal) closes the port at once, which drops DTR and so
// ends a call it left up.
func EstablishSerialLink(path string, baud int, settings *LinkSettings, listener LinkListener, cancellation *Cancellation) (link *SatelliteLink, err error) {
	// Five parameters: gomobile has no default arguments to spare any of them.
	defer recoverPanic(&err)
	if settings == nil {
		return nil, nilArgument("settings")
	}
	config, err := settings.config(cancellation)
	if err != nil {
		return nil, err
	}
	port, err := serial.Open(path, baud)
	if err != nil {
		return nil, fromError(err)
	}
	return establishPortLink(port, config, listener)
}

// establishPortLink brings a link up over port, a serial port the link owns
// from now on: it is closed at once if the link does not come up, and
// otherwise once the call has ended.
func establishPortLink(port iridium.Transport, config satnet.Config, listener LinkListener) (*SatelliteLink, error) {
	handset, err := initializedHandset(port)
	if err != nil {
		return nil, err
	}
	link, err := establishOwnLink(handset, config, listener)
	if err != nil {
		return nil, err
	}
	// The port belongs to the link, and the app has no other handle on it.
	closeAfterCall(link.watcher, handset)
	return link, nil
}
