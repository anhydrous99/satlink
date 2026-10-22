package satnet

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"slices"
)

// The public keys a server's certificate chain may not hold: those that
// rustls's ring backend, the Rust version's crypto, refuses and crypto/x509
// takes — an RSA key of under 2041 or over 8192 bits, and an ECDSA key on
// P-224. Refusing them keeps the Rust version's result whenever a server
// sends both versions the same chain, and the short RSA keys crypto/x509
// takes, from 1024 bits, are too weak to trust anyway. crypto/tls itself
// refuses an RSA key of over 8192 bits in a certificate the server sends,
// so only a trust anchor's reaches this check.
//
// A server that holds several certificates may send the two versions
// different ones: crypto/tls asks for ML-DSA, then RSA-PSS, signatures
// before ECDSA ones, where rustls asks for ECDSA first and never for ML-DSA
// or P-521 (see [TLSOptions.PinnedPublicKeys]). So the keys that ring
// cannot verify but crypto/tls asks for — ML-DSA, and ECDSA on P-521 — are
// taken: crypto/tls cannot be told not to offer their signature schemes,
// and a server that holds such a certificate next to a classical one may
// send us that one, where it sends rustls the classical one; refusing what
// we asked for would fail connections that the Rust version makes. The
// signatures are left to crypto/x509, which also verifies some that ring
// does not, such as ecdsa-with-SHA512.

// The sizes of the RSA keys ring verifies, in bits. ring compares its
// minimum with the modulus's length rounded up to whole bytes, so it takes
// moduli from 2041 bits, and its maximum with the exact length
// (ring-0.17.14/src/rsa/public_modulus.rs).
const (
	minRSAKeyBits = 2048
	maxRSAKeyBits = 8192
)

// errRefusedKey is the handshake's error when every chain to a trusted root
// holds a refused key.
var errRefusedKey = errors.New("invalid peer certificate: a key in the chain is RSA of under 2041 or over 8192 bits, or ECDSA on P-224")

// verifyChainKeys is a check for tls.Config.VerifyPeerCertificate: one of
// the chains crypto/tls verified, from the server's certificate to a trusted
// root, must hold no refused key, as rustls needs one path it can verify.
// crypto/tls runs it after verifying the chains and before checking the
// server's handshake signature, for full handshakes: a resumed session was
// checked when it was made. A configuration from [TLSConnector.Config] that
// skips verification verifies no chain, and then none is checked.
func verifyChainKeys(_ [][]byte, verifiedChains [][]*x509.Certificate) error {
	if len(verifiedChains) == 0 || slices.ContainsFunc(verifiedChains, holdsNoRefusedKey) {
		return nil
	}
	return errRefusedKey
}

// holdsNoRefusedKey reports whether no key of chain is refused: neither the
// server's own, which signs the handshake, nor any issuer's, up to the
// root's.
func holdsNoRefusedKey(chain []*x509.Certificate) bool {
	for _, certificate := range chain {
		if isRefusedKey(certificate.PublicKey) {
			return false
		}
	}
	return true
}

// isRefusedKey reports whether a chain may not hold key: an RSA key of under
// 2041 or over 8192 bits, or an ECDSA key on P-224.
func isRefusedKey(key crypto.PublicKey) bool {
	switch key := key.(type) {
	case *rsa.PublicKey:
		// Size is the modulus's length in whole bytes, as ring's minimum
		// counts it.
		return key.Size()*8 < minRSAKeyBits || key.N.BitLen() > maxRSAKeyBits
	case *ecdsa.PublicKey:
		return key.Curve == elliptic.P224()
	default:
		return false
	}
}
