package ringprovider

import (
	"crypto/tls"
	"slices"
	"testing"
)

// The lists are rustls 0.23.46's, written out from src/crypto/ring/mod.rs:
// the TLS 1.2 part of ALL_CIPHER_SUITES, and ALL_KX_GROUPS, which the
// provider's defaults are.

func TestTheTLS12CipherSuitesAreTheProvidersInItsOrder(t *testing.T) {
	t.Parallel()
	want := []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	}

	if got := TLS12CipherSuites(); !slices.Equal(got, want) {
		t.Errorf("TLS12CipherSuites() = %v, want %v", got, want)
	}
}

func TestTheKeyExchangeGroupsAreTheProvidersInItsOrder(t *testing.T) {
	t.Parallel()
	want := []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}

	if got := KeyExchangeGroups(); !slices.Equal(got, want) {
		t.Errorf("KeyExchangeGroups() = %v, want %v", got, want)
	}
}

// TestEachCallReturnsAListOfItsOwn changes the lists one caller got: a
// tls.Config keeps the list it is given, so another caller's must not
// change with it.
func TestEachCallReturnsAListOfItsOwn(t *testing.T) {
	t.Parallel()
	suites, groups := TLS12CipherSuites(), KeyExchangeGroups()

	suites[0], groups[0] = 0, 0

	if TLS12CipherSuites()[0] == 0 || KeyExchangeGroups()[0] == 0 {
		t.Error("a change to one caller's list reached the next caller's")
	}
}
