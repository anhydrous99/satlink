package main

import "testing"

// mustParseHTTPURL parses rawURL, failing the test if it does not parse.
func mustParseHTTPURL(t *testing.T, rawURL string) httpURL {
	t.Helper()
	url, err := parseHTTPURL(rawURL)
	if err != nil {
		t.Fatalf("parseHTTPURL(%q) failed: %v", rawURL, err)
	}
	return url
}

// The URLs that parse.
func TestParsesURLs(t *testing.T) {
	tests := []struct {
		url  string
		want httpURL
	}{
		{"https://example.com:8443/a/b?c", httpURL{secure: true, host: "example.com", port: 8443, path: "/a/b?c"}},
		{"http://web.test", httpURL{secure: false, host: "web.test", port: 80, path: "/"}},
	}
	for _, test := range tests {
		if got := mustParseHTTPURL(t, test.url); got != test.want {
			t.Errorf("parseHTTPURL(%q) = %+v, want %+v", test.url, got, test.want)
		}
	}
}

// The URLs that do not parse.
func TestRefusesWhatIsNotAnHTTPURL(t *testing.T) {
	for _, url := range []string{
		"ftp://example.com",
		"http://:80/",
		"example.com",
		"http//example.com/",
		"http://example.com:http/",
		"http://example.com:65536/",
		"http://example.com:/",
	} {
		if _, err := parseHTTPURL(url); err == nil {
			t.Errorf("parseHTTPURL(%q) succeeded", url)
		}
	}
}

func TestARefusedURLIsNamedInTheError(t *testing.T) {
	tests := map[string]string{
		"ftp://example.com": `"ftp://example.com" is not a valid http:// or https:// URL`,
		// The escape for a vertical tab is \u{b}, where %q would write "\v".
		"http://web.test/a\vb": `"http://web.test/a\u{b}b" is not a valid http:// or https:// URL`,
	}
	for url, want := range tests {
		if _, err := parseHTTPURL(url); err == nil || err.Error() != want {
			t.Errorf("parseHTTPURL(%q) fails with %v, want %s", url, err, want)
		}
	}
}

// A URL that would break the request line is refused before dialling.
func TestAURLThatWouldBreakTheRequestLineIsRefusedBeforeDialling(t *testing.T) {
	for _, url := range []string{
		"http://web.test/my file.txt",
		"http://web.test/a\tb",
		"http://web.test/x HTTP/1.1\r\nX-Injected: 1",
		"http://web.test/?q=a b",
	} {
		if _, err := parseHTTPURL(url); err == nil {
			t.Errorf("parseHTTPURL(%q) succeeded", url)
		}
	}
	mustParseHTTPURL(t, "http://web.test/my%20file.txt")
}

func TestEveryKindOfWhiteSpaceAndControlCharacterIsRefused(t *testing.T) {
	for _, character := range []string{"\u00a0", "\u3000", "\u2028", "\x00", "\x7f", "\u0085", "\u009b"} {
		if url := "http://web.test/a" + character + "b"; !isRefused(url) {
			t.Errorf("parseHTTPURL(%q) succeeded", url)
		}
	}
	// Neither white space nor a control character, though invisible.
	mustParseHTTPURL(t, "http://web.test/a\u200bb")
}

// isRefused reports whether parseHTTPURL refuses url.
func isRefused(url string) bool {
	_, err := parseHTTPURL(url)
	return err != nil
}

func TestAPortMayCarryAPlusSign(t *testing.T) {
	url := mustParseHTTPURL(t, "http://example.com:+8080/")

	if url.port != 8080 {
		t.Errorf("port = %d, want 8080", url.port)
	}
}

// The fragment stays behind, and a bare query gets a path.
func TestTheFragmentStaysBehindAndABareQueryGetsAPath(t *testing.T) {
	url := mustParseHTTPURL(t, "http://example.com:8080/page?q=1#top")
	if url.port != 8080 || url.path != "/page?q=1" {
		t.Errorf("port and path = %d, %q, want 8080, /page?q=1", url.port, url.path)
	}

	url = mustParseHTTPURL(t, "http://example.com?q=1#top")
	if url.host != "example.com" || url.path != "/?q=1" {
		t.Errorf("host and path = %q, %q, want example.com, /?q=1", url.host, url.path)
	}
}

// The Host header names the port unless it is the default.
func TestTheHostHeaderNamesThePortUnlessItIsTheDefault(t *testing.T) {
	tests := []struct{ url, authority string }{
		{"http://example.com:8080/", "example.com:8080"},
		{"http://example.com:80/", "example.com"},
		{"https://example.com/", "example.com"},
		{"https://example.com:80/", "example.com:80"},
	}
	for _, test := range tests {
		if got := mustParseHTTPURL(t, test.url).authority(); got != test.authority {
			t.Errorf("authority of %q = %q, want %q", test.url, got, test.authority)
		}
	}
}
