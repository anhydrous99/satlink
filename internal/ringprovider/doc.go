// Package ringprovider holds what the port takes from rustls's ring
// provider, rustls::crypto::ring::default_provider(), which the Rust
// version's TLS client (iridium-net) and the simulator's TLS echo
// (iridium-sim) both used: its TLS 1.2 cipher suites and its key exchange
// groups, as crypto/tls names them. satnet's client offers them and sim's
// echo serves them, so both keep to one list, as the Rust crates kept to
// one provider.
//
// The provider's TLS 1.3 suites need no list: they are crypto/tls's three,
// which neither a client nor a server can choose.
package ringprovider
