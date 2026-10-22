package satnet

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"slices"
	"testing"
	"time"
)

// Go only: crypto/tls alone takes keys that rustls's ring backend refuses —
// RSA keys of 1024 to 2040 bits among them, which are too weak to trust —
// and it asks servers for keys that ring cannot verify, ML-DSA and ECDSA on
// P-521, which must then be taken.

func TestTheKeysAServersChainMayHold(t *testing.T) {
	p256, p384, p521 := generateKey(t), generateECDSAKey(t, elliptic.P384()), generateECDSAKey(t, elliptic.P521())
	rsa1024, rsa2048 := generateRSAKey(t, 1024), generateRSAKey(t, 2048)
	rsa2040, rsa2041 := generateRSAKey(t, 2040), generateRSAKey(t, 2041)
	ed25519Key, mldsaKey := generateEd25519Key(t), generateMLDSAKey(t)
	for _, c := range []struct {
		name             string
		caKey, serverKey crypto.Signer
		trusted          bool
	}{
		{"an RSA-2048 server key", p256, rsa2048, true},
		{"an RSA-2048 CA", rsa2048, p256, true},
		// A modulus of 2041 bits fills 256 bytes, as ring counts its minimum.
		{"an RSA-2041 server key", p256, rsa2041, true},
		{"a P-384 CA and an Ed25519 server key", p384, ed25519Key, true},
		{"a P-521 server key", p256, p521, true},
		{"a P-521 CA", p521, p256, true},
		{"an ML-DSA server key", p256, mldsaKey, true},
		{"an ML-DSA CA", mldsaKey, p256, true},
		{"an RSA-2040 server key", p256, rsa2040, false},
		{"an RSA-1024 server key", p256, rsa1024, false},
		{"an RSA-1024 CA", rsa1024, p256, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fixture := newTLSFixtureWithKeys(t, c.caKey, c.serverKey)
			peer := servePeer(t, fixture.server, nil)

			_, err := fixture.connector(t).secure(peer.tcp, tlsTestHost, time.Now().Add(waitBudget))

			if c.trusted && err != nil {
				t.Errorf("secure = %v, want the server trusted", err)
			}
			if !c.trusted {
				expectTLSError(t, err, "TLS: invalid peer certificate: a key in the chain is RSA of under 2041 or over 8192 bits, or ECDSA on P-224")
			}
		})
	}
}

// keysOnlyCryptoTLSAsksFor returns, by name, keys of the kinds crypto/tls
// asks servers for and rustls never does.
func keysOnlyCryptoTLSAsksFor(t *testing.T) map[string]crypto.Signer {
	t.Helper()
	return map[string]crypto.Signer{
		"ML-DSA-65": generateMLDSAKey(t),
		"P-521":     generateECDSAKey(t, elliptic.P521()),
	}
}

// newTLSFixturePreferring makes a fixture whose server prefers a certificate
// for preferredKey to its P-256 one, and returns it with that certificate.
// crypto/tls's server sends the first certificate the client's offer allows.
func newTLSFixturePreferring(t *testing.T, preferredKey crypto.Signer) (*tlsFixture, tls.Certificate) {
	t.Helper()
	fixture := newTLSFixture(t)
	preferred := fixture.serverCertificate(t, preferredKey)
	fixture.server.Certificates = slices.Insert(fixture.server.Certificates, 0, preferred)
	return fixture, preferred
}

// TestAServerSendingTheCertificateCryptoTLSAskedForIsTrusted serves a server
// that prefers an ML-DSA or a P-521 certificate to its P-256 one: crypto/tls
// asks for both kinds, where rustls asks for neither and is sent the P-256
// certificate.
func TestAServerSendingTheCertificateCryptoTLSAskedForIsTrusted(t *testing.T) {
	for name, preferredKey := range keysOnlyCryptoTLSAsksFor(t) {
		t.Run(name, func(t *testing.T) {
			fixture, preferred := newTLSFixturePreferring(t, preferredKey)
			peer := servePeer(t, fixture.server, nil)

			conn, err := fixture.connector(t).secure(peer.tcp, tlsTestHost, time.Now().Add(waitBudget))

			if err != nil {
				t.Fatalf("secure = %v, want the server trusted", err)
			}
			if sent := conn.session.ConnectionState().PeerCertificates[0]; !bytes.Equal(sent.Raw, preferred.Certificate[0]) {
				t.Errorf("the server sent its %v certificate, not the %s one it prefers", sent.PublicKeyAlgorithm, name)
			}
		})
	}
}

// TestTheCertificateCryptoTLSAskedForMustBePinnedToo serves the servers of
// the test above. The Rust version is sent their P-256 certificate, so a pin
// of its key alone is enough there, and matches nothing here.
func TestTheCertificateCryptoTLSAskedForMustBePinnedToo(t *testing.T) {
	for name, preferredKey := range keysOnlyCryptoTLSAsksFor(t) {
		t.Run(name, func(t *testing.T) {
			fixture, _ := newTLSFixturePreferring(t, preferredKey)
			preferredKeySHA256 := publicKeySHA256(t, preferredKey.Public())

			p256KeyPinned := secureWithPins(t, fixture, fixture.serverKeySHA256)
			bothKeysPinned := secureWithPins(t, fixture, fixture.serverKeySHA256, preferredKeySHA256)

			expectTLSError(t, p256KeyPinned, "TLS: "+errKeyNotPinned.Error())
			if bothKeysPinned != nil {
				t.Errorf("secure, with both keys pinned = %v, want the server trusted", bothKeysPinned)
			}
		})
	}
}

// secureWithPins secures a connection to fixture's server with a connector
// that trusts the fixture's CA and pins pins, and returns the handshake's
// error.
func secureWithPins(t *testing.T, fixture *tlsFixture, pins ...[sha256.Size]byte) error {
	t.Helper()
	peer := servePeer(t, fixture.server, nil)
	_, err := fixture.connector(t, pins...).secure(peer.tcp, tlsTestHost, time.Now().Add(waitBudget))
	return err
}

// rsaPublicKeyOf returns an RSA public key with a modulus of bits bits, fit
// only for checks of its size.
func rsaPublicKeyOf(bits int) *rsa.PublicKey {
	return &rsa.PublicKey{N: new(big.Int).Lsh(big.NewInt(1), uint(bits-1)), E: 65537}
}

func TestTheRefusedKeys(t *testing.T) {
	for name, c := range map[string]struct {
		key     crypto.PublicKey
		refused bool
	}{
		// ring rounds a modulus up to whole bytes for its minimum only.
		"RSA of 2040 bits": {rsaPublicKeyOf(2040), true},
		"RSA of 2041 bits": {rsaPublicKeyOf(2041), false},
		"RSA of 2047 bits": {rsaPublicKeyOf(2047), false},
		"RSA of 2048 bits": {rsaPublicKeyOf(2048), false},
		"RSA of 8192 bits": {rsaPublicKeyOf(8192), false},
		"RSA of 8193 bits": {rsaPublicKeyOf(8193), true},
		"ECDSA on P-224":   {&ecdsa.PublicKey{Curve: elliptic.P224()}, true},
		"ECDSA on P-256":   {&ecdsa.PublicKey{Curve: elliptic.P256()}, false},
		"ECDSA on P-384":   {&ecdsa.PublicKey{Curve: elliptic.P384()}, false},
		"ECDSA on P-521":   {&ecdsa.PublicKey{Curve: elliptic.P521()}, false},
		"Ed25519":          {make(ed25519.PublicKey, ed25519.PublicKeySize), false},
		"ML-DSA":           {generateMLDSAKey(t).PublicKey(), false},
	} {
		if got := isRefusedKey(c.key); got != c.refused {
			t.Errorf("isRefusedKey(%s) = %t, want %t", name, got, c.refused)
		}
	}
}

func TestOneChainWithoutARefusedKeyIsEnough(t *testing.T) {
	server := &x509.Certificate{PublicKey: &ecdsa.PublicKey{Curve: elliptic.P256()}}
	weak := []*x509.Certificate{server, {PublicKey: rsaPublicKeyOf(1024)}}
	sound := []*x509.Certificate{server, {PublicKey: rsaPublicKeyOf(4096)}}

	if err := verifyChainKeys(nil, [][]*x509.Certificate{weak, sound}); err != nil {
		t.Errorf("verifyChainKeys(a weak chain and a sound one) = %v, want nil", err)
	}
	if err := verifyChainKeys(nil, [][]*x509.Certificate{weak, weak}); err != errRefusedKey {
		t.Errorf("verifyChainKeys(weak chains only) = %v, want %v", err, errRefusedKey)
	}
}

// TestAConfigurationThatSkipsVerificationChecksNoKey checks a configuration
// from Config with InsecureSkipVerify set, for which crypto/tls verifies no
// chain.
func TestAConfigurationThatSkipsVerificationChecksNoKey(t *testing.T) {
	if err := verifyChainKeys(nil, nil); err != nil {
		t.Errorf("verifyChainKeys(no chain) = %v, want nil", err)
	}
}
