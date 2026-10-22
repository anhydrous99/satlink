package ringprovider

import "crypto/tls"

// TLS12CipherSuites returns the TLS 1.2 cipher suites, in order of
// preference: ECDHE key exchange, signed with ECDSA or RSA, and an AEAD
// cipher. crypto/tls's defaults would add the ECDHE suites with CBC and
// HMAC-SHA1. They are left out because padding oracles attack their
// MAC-then-encrypt construction. Each call returns a list of its own.
func TLS12CipherSuites() []uint16 {
	return []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	}
}

// KeyExchangeGroups returns the key exchange groups, in order of
// preference: X25519, P-256 and P-384, none of them post-quantum. Each call
// returns a list of its own.
func KeyExchangeGroups() []tls.CurveID {
	return []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}
}
