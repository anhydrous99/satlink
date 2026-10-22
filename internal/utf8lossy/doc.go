// Package utf8lossy makes text of bytes that need not be UTF-8 as Rust's
// String::from_utf8_lossy makes it: each maximal subpart of an ill-formed
// sequence (Unicode §3.9, "U+FFFD Substitution of Maximal Subparts")
// becomes one U+FFFD. The Rust version read the handset's lines, a PPP
// peer's messages, what the simulated handset and its DNS server receive,
// and an HTTP response's head so. Go's strings.ToValidUTF8 would replace a
// whole run of ill-formed bytes with one U+FFFD, and utf8.DecodeRune
// reports each byte apart.
package utf8lossy
