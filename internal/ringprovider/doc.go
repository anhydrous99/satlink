// Package ringprovider holds the TLS 1.2 cipher suites and the key exchange
// groups of rustls's ring provider, rustls::crypto::ring::default_provider(),
// as crypto/tls names them. satnet's client offers them and sim's TLS echo
// serves them, so both keep to one list.
//
// The provider's TLS 1.3 suites need no list: they are crypto/tls's three,
// which neither a client nor a server can choose.
package ringprovider
