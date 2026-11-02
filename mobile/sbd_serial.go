//go:build !android && (!ios || (macos && !maccatalyst))

package mobile

import (
	"context"
	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/serial"
)

// NewSerialSBDManager creates a worker for desktop serial USB connections.
func NewSerialSBDManager(q *SBDQueue, path string, settings *SBDSettings) (manager *SBDManager, err error) {
	defer sbdBoundaryError(&err)
	defer recoverPanic(&err)
	return makeSBDManager(q, settings, func(context.Context) (*iridium.Handset, error) { return serial.Connect(path, serial.DefaultBaud) })
}
