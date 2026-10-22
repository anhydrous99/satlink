package main

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/anhydrous99/satlink/internal/debugtext"
)

// The parts of an http:// or https:// URL that a GET request needs.

// The schemes' default ports (RFC 9110 §4.2).
const (
	httpPort  = 80
	httpsPort = 443
)

// httpURL is an http:// or https:// URL, as far as a GET request needs it.
type httpURL struct {
	// secure is set for https://.
	secure bool
	host   string
	port   uint16
	// path is the path and query, as the request line gives them.
	path string
}

// urlValue keeps the URL that `net get` was given.
type urlValue struct {
	target *httpURL
}

// Set parses rawURL into the target.
func (v urlValue) Set(rawURL string) error {
	url, err := parseHTTPURL(rawURL)
	if err != nil {
		return err
	}
	*v.target = url
	return nil
}

// parseHTTPURL parses an http:// or https:// URL. The command line is
// checked with it as it is read, so a bad URL never costs a call.
func parseHTTPURL(rawURL string) (httpURL, error) {
	// They would break the request line, and a URL never holds them (RFC
	// 3986 §2): a space is written %20.
	if strings.ContainsFunc(rawURL, breaksTheRequestLine) {
		return httpURL{}, notAnHTTPURL(rawURL)
	}
	scheme, rest, found := strings.Cut(rawURL, "://")
	secure, known := isSecureScheme(scheme)
	if !found || !known {
		return httpURL{}, notAnHTTPURL(rawURL)
	}
	// The fragment stays with the client (RFC 3986 §3.5).
	rest, _, _ = strings.Cut(rest, "#")
	// The authority ends where the path or the query begins.
	authority, path := rest, ""
	if end := strings.IndexAny(rest, "/?"); end >= 0 {
		authority, path = rest[:end], rest[end:]
	}
	url := httpURL{secure: secure, path: originForm(path)}
	host, port, err := splitAuthority(authority, url.defaultPort())
	if err != nil || host == "" {
		return httpURL{}, notAnHTTPURL(rawURL)
	}
	url.host, url.port = host, port
	return url, nil
}

// breaksTheRequestLine reports whether character, white space or a control
// character, would break the request line that a URL holding it is sent in.
func breaksTheRequestLine(character rune) bool {
	return unicode.IsSpace(character) || unicode.IsControl(character)
}

// notAnHTTPURL refuses rawURL.
func notAnHTTPURL(rawURL string) error {
	return errors.New(debugtext.Quote(rawURL) + " is not a valid http:// or https:// URL")
}

// isSecureScheme reports whether scheme is https rather than http, and
// whether it is either.
func isSecureScheme(scheme string) (secure, known bool) {
	switch scheme {
	case "https":
		return true, true
	case "http":
		return false, true
	default:
		return false, false
	}
}

// splitAuthority splits authority into host and port at its last ':', the
// port being fallback when there is none.
func splitAuthority(authority string, fallback uint16) (string, uint16, error) {
	colon := strings.LastIndexByte(authority, ':')
	if colon < 0 {
		return authority, fallback, nil
	}
	// Decimal digits after an optional '+', as parseUnsigned reads them.
	port, err := parseUnsigned(authority[colon+1:])
	if err == nil && port > math.MaxUint16 {
		err = errNumberTooLarge
	}
	return authority[:colon], uint16(port), err
}

// authority is the host, with the port unless it is the scheme's default, as
// the Host header gives them (RFC 9112 §3.2).
func (u httpURL) authority() string {
	if u.port == u.defaultPort() {
		return u.host
	}
	return u.host + ":" + strconv.Itoa(int(u.port))
}

// defaultPort is the port of the URL's scheme when the URL names none.
func (u httpURL) defaultPort() uint16 {
	if u.secure {
		return httpsPort
	}
	return httpPort
}

// originForm is the path to request: "/" when the URL has none (RFC 9112
// §3.2.1).
func originForm(path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}
