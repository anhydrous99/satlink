package serial

import (
	"strconv"

	"go.bug.st/serial/enumerator"

	"github.com/anhydrous99/satlink/iridium"
)

// Port is a serial port that looks like an Iridium handset.
type Port struct {
	// Path is the device to open, such as /dev/cu.usbmodem1101 or
	// /dev/ttyACM0.
	Path string
	// ProductID is the USB product ID.
	ProductID uint16
	// Model is the model name of a product the official driver knows, such
	// as "Iridium 9575"; empty for other products.
	Model string
	// SerialNumber is the USB serial number; empty when the device reports
	// none.
	SerialNumber string
}

// portLister lists the serial ports with their USB details, as the
// enumerator's GetDetailedPortsList does: the filters name the devices it
// may probe actively, none here.
type portLister func(activeUSBProbeFilters ...func(vid, pid string) bool) ([]*enumerator.PortDetails, error)

// Discover lists the serial ports whose USB vendor ID is Iridium's
// (iridium.USBVendorID). On macOS the port is the callout node
// (/dev/cu.*), which does not block waiting for carrier detect: the
// enumerator reports no other. On Linux, discovery reads sysfs.
func Discover() ([]Port, error) {
	return discover(enumerator.GetDetailedPortsList)
}

// discover is Discover, listing the ports with listPorts.
func discover(listPorts portLister) ([]Port, error) {
	details, err := listPorts()
	if err != nil {
		return nil, transportError(err)
	}
	return handsetPorts(details), nil
}

// handsetPorts returns the USB ports among details whose vendor ID is
// Iridium's, in the enumerator's order.
func handsetPorts(details []*enumerator.PortDetails) []Port {
	var ports []Port
	for _, detail := range details {
		if port, ok := handsetPort(detail); ok {
			ports = append(ports, port)
		}
	}
	return ports
}

// handsetPort returns the port detail describes, and whether it is an
// Iridium handset's: a USB port with Iridium's vendor ID.
func handsetPort(detail *enumerator.PortDetails) (Port, bool) {
	if detail == nil || !detail.IsUSB {
		return Port{}, false
	}
	vendorID, ok := parseUSBID(detail.VID)
	if !ok || vendorID != iridium.USBVendorID {
		return Port{}, false
	}
	// The vendor makes it a handset; a product ID the enumerator garbled
	// leaves it unnamed, with ProductID 0.
	productID, _ := parseUSBID(detail.PID)
	model, _ := iridium.ModelForProductID(productID)
	return Port{
		Path:         detail.Name,
		ProductID:    productID,
		Model:        model,
		SerialNumber: detail.SerialNumber,
	}, true
}

// parseUSBID parses a USB vendor or product ID as the enumerator writes it,
// four hexadecimal digits in either case, and reports whether it could; 0
// when it could not.
func parseUSBID(hex string) (uint16, bool) {
	id, err := strconv.ParseUint(hex, 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(id), true
}
