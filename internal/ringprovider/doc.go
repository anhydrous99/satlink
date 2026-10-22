// Package ringprovider holds the TLS 1.2 cipher suites and the key exchange
// groups that satlink uses. satnet's client offers them and sim's TLS echo
// serves them, so both keep to one list.
//
// The list is short on purpose. Every TLS 1.2 suite in it has forward
// secrecy (ECDHE) and an AEAD cipher. The groups leave out post-quantum key
// exchange, which would add about 2.3 KB to every handshake over a link that
// carries some 300 bytes a second.
//
// TLS 1.3 suites need no list: they are crypto/tls's three, which neither a
// client nor a server can choose.
package ringprovider
