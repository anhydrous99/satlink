package satnet

import (
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"slices"
)

// Public-key pinning on top of full certificate-chain verification.
//
// Only the server's own certificate is checked against the pins. The
// intermediates a server sends are chosen by the server, so a pin that any
// sent certificate could satisfy would let an attacker append the pinned
// certificate to an unrelated valid chain.

// errKeyNotPinned is the handshake's error when the server's key matches no
// pin.
var errKeyNotPinned = errors.New("invalid peer certificate: the public key is not pinned")

// verifyPinnedKey returns a check for tls.Config.VerifyConnection that
// requires the SHA-256 of the server's own DER SubjectPublicKeyInfo to be
// one of pins. crypto/tls runs it after verifying the whole chain, and for
// resumed sessions too.
func verifyPinnedKey(pins [][sha256.Size]byte) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errKeyNotPinned
		}
		key := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
		if !slices.Contains(pins, key) {
			return errKeyNotPinned
		}
		return nil
	}
}
