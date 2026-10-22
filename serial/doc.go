// Package serial is the desktop transport for Iridium handsets, on macOS,
// Linux and Windows.
//
// The handset enumerates as a USB CDC-ACM device, so the operating system's
// own driver exposes it as a serial port: /dev/cu.usbmodem* on macOS,
// /dev/ttyACM* on Linux, a COM port on Windows. No vendor driver is needed.
// [Discover] finds handsets by Iridium's USB vendor ID, [Open] opens one as
// an iridium.Transport, and [Connect] does both and initialises the handset:
//
//	handset, err := serial.Connect("", serial.DefaultBaud)
//	if err != nil {
//		return err
//	}
//	defer handset.Close() // drops DTR: with AT&D2 any call ends
//	bars, err := handset.SignalQuality()
//
// # Keeping the call safe
//
// The port is opened exclusively, so no other program can open it
// meanwhile. On Unix, that is as far as the TIOCEXCL ioctl goes.
//
// If the program dies, the operating system closes the port and drops DTR.
// The handset then ends the call, as data calls set AT&D2, so airtime
// stops. Child processes the program starts do not inherit the port, which
// would keep it open, and DTR up, as long as they live. A transport garbage
// collected without Close closes its port too.
//
// # Building
//
// On macOS, port discovery uses IOKit through cgo: building this package
// for macOS needs cgo and the macOS SDK, also when cross-compiling. On
// Linux, discovery reads sysfs, so no libudev is needed.
//
// Errors are *iridium.Error values of kind iridium.ErrTransport.
package serial
