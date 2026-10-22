package main

import (
	"fmt"
	"math"
	"slices"
	"testing"
)

// It reads the status code.
func TestReadsTheStatusCode(t *testing.T) {
	tests := []struct {
		head   string
		status int
		ok     bool
	}{
		{"HTTP/1.1 200 OK\r\nA: b", 200, true},
		{"HTTP/1.0 204", 204, true},
		{"SSH-2.0-OpenSSH_9.6", 0, false},
		{"HTTP/1.1 2000 Too Many Digits", 0, false},
	}
	for _, test := range tests {
		status, ok := statusCode(test.head)
		if status != test.status || ok != test.ok {
			t.Errorf("statusCode(%q) = %d, %t, want %d, %t", test.head, status, ok, test.status, test.ok)
		}
	}
}

func TestAStatusLineNeedsAVersionAndThreeDigits(t *testing.T) {
	for _, head := range []string{"", "HTTP/1.1", "HTTP/1.1 2x0 OK", "http/1.1 200 OK", "HTTP/1.0 204\r"} {
		if status, ok := statusCode(head); ok {
			t.Errorf("statusCode(%q) = %d, want none", head, status)
		}
	}
}

// Responses that never carry a body need no length.
func TestResponsesThatNeverCarryABodyNeedNoLength(t *testing.T) {
	tests := []struct {
		status int
		head   string
		want   bodyLength
	}{
		{304, "HTTP/1.1 304 Not Modified\r\nContent-Length: 1234", bodyLength{bytes: 0, known: true}},
		{204, "HTTP/1.1 204 No Content", bodyLength{bytes: 0, known: true}},
		{200, "HTTP/1.0 200 OK\r\nContent-Length: 5", bodyLength{bytes: 5, known: true}},
		{200, "HTTP/1.0 200 OK", bodyLength{}},
	}
	for _, test := range tests {
		if got, err := expectedBodyLength(test.status, test.head); err != nil || got != test.want {
			t.Errorf("expectedBodyLength(%d, %q) = %+v, %v, want %+v", test.status, test.head, got, err, test.want)
		}
	}
}

// Framing the client cannot check is an error.
func TestFramingTheClientCannotCheckIsAnError(t *testing.T) {
	for _, head := range []string{
		"HTTP/1.0 200 OK\r\nContent-Length: 5, 5",
		"HTTP/1.0 200 OK\r\nContent-Length: abc",
		"HTTP/1.0 200 OK\r\nContent-Length: +5",
		"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked",
		// The same as "5, 7" (RFC 9110 §5.3).
		"HTTP/1.1 200 OK\r\nContent-Length: 5\r\nContent-Length: 7",
	} {
		if length, err := expectedBodyLength(200, head); err == nil {
			t.Errorf("expectedBodyLength(200, %q) = %+v, want an error", head, length)
		}
	}
}

func TestFramingThatCannotBeCheckedSaysWhy(t *testing.T) {
	tests := []struct {
		head string
		want string
	}{
		{"HTTP/1.1 200 OK\r\nTransfer-Encoding: gzip\r\nTransfer-Encoding: chunked",
			`the reply uses Transfer-Encoding "gzip, chunked", which HTTP/1.0 does not allow`},
		{"HTTP/1.0 200 OK\r\nContent-Length: 18446744073709551616",
			`the Content-Length "18446744073709551616" is not a length`},
		{"HTTP/1.0 200 OK\r\nContent-Length:", `the Content-Length "" is not a length`},
		// The escapes are \u{1b} and \u{1}, where %q would write "\x1b" and "\x01".
		{"HTTP/1.1 200 OK\r\nTransfer-Encoding: x\x1by",
			`the reply uses Transfer-Encoding "x\u{1b}y", which HTTP/1.0 does not allow`},
		{"HTTP/1.0 200 OK\r\nContent-Length: 5\x01", `the Content-Length "5\u{1}" is not a length`},
	}
	for _, test := range tests {
		if _, err := expectedBodyLength(200, test.head); err == nil || err.Error() != test.want {
			t.Errorf("expectedBodyLength(200, %q) = %v, want %s", test.head, err, test.want)
		}
	}
}

func TestAHeaderFieldIsFoundByItsNameInAnyASCIICase(t *testing.T) {
	tests := []struct {
		name string
		head string
		want bodyLength
	}{
		{"spaced and in mixed case", "HTTP/1.0 200 OK\r\ncontent-LENGTH :  7 ", bodyLength{bytes: 7, known: true}},
		{"the largest length", "HTTP/1.0 200 OK\r\nContent-Length: 18446744073709551615",
			bodyLength{bytes: math.MaxUint64, known: true}},
		{"another field", "HTTP/1.0 200 OK\r\nX-Content-Length: 3", bodyLength{}},
		{"a field as long", "HTTP/1.0 200 OK\r\nContent-Lengte: 3", bodyLength{}},
		// Unicode case folding would take the long s for an s.
		{"a name only Unicode folds to it", "HTTP/1.1 200 OK\r\nTran\u017ffer-Encoding: chunked", bodyLength{}},
		{"in the status line", "Content-Length: 5\r\nServer: x", bodyLength{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := expectedBodyLength(200, test.head); err != nil || got != test.want {
				t.Errorf("expectedBodyLength(200, %q) = %+v, %v, want %+v", test.head, got, err, test.want)
			}
		})
	}
}

// Interim responses are skipped and never taken for the final one.
func TestInterimResponsesAreSkippedAndNeverTakenForTheFinalOne(t *testing.T) {
	head, body, ok := finalResponse([]byte("HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 103 Early Hints\r\n" +
		"Link: </s.css>\r\n\r\nHTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello"))
	if !ok || string(head) != "HTTP/1.1 200 OK\r\nContent-Length: 5" || string(body) != "hello" {
		t.Errorf("finalResponse = %q, %q, %t, want the 200's head, hello, true", head, body, ok)
	}

	if head, body, ok := finalResponse([]byte("HTTP/1.1 100 Continue\r\n\r\n")); ok {
		t.Errorf("finalResponse of an interim response alone = %q, %q, want none", head, body)
	}
}

// A length no response could reach never ends one early.
func TestALengthNoResponseCouldReachNeverEndsOneEarly(t *testing.T) {
	head := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", uint64(math.MaxUint64))

	if end, ok := announcedEnd([]byte(head)); ok {
		t.Errorf("announcedEnd = %d, want none", end)
	}
}

func TestAResponseEndsWhereItsFinalHeadSays(t *testing.T) {
	tests := []struct {
		name     string
		response string
		end      uint64
		ok       bool
	}{
		{"sized", "HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\nhel", 43, true},
		{"after an interim response", "HTTP/1.1 100 Continue\r\n\r\nHTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\n",
			68, true},
		{"without a body", "HTTP/1.0 204 No Content\r\n\r\n", 27, true},
		{"before the head ended", "HTTP/1.0 200 OK\r\nContent-Length: 5\r\n", 0, false},
		{"without a length", "HTTP/1.0 200 OK\r\n\r\nhello", 0, false},
		{"without a status line", "SSH-2.0-OpenSSH_9.6\r\n\r\n", 0, false},
		{"with a length that cannot be checked", "HTTP/1.0 200 OK\r\nContent-Length: +5\r\n\r\n", 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if end, ok := announcedEnd([]byte(test.response)); end != test.end || ok != test.ok {
				t.Errorf("announcedEnd(%q) = %d, %t, want %d, %t", test.response, end, ok, test.end, test.ok)
			}
		})
	}
}

// It splits the HTTP head from the body.
func TestSplitsHTTPHeadFromBody(t *testing.T) {
	head, body, ok := splitHTTPResponse([]byte("HTTP/1.0 200 OK\r\nA: b\r\n\r\nhello"))

	if !ok || string(head) != "HTTP/1.0 200 OK\r\nA: b" || string(body) != "hello" {
		t.Errorf("splitHTTPResponse = %q, %q, %t, want the head, hello, true", head, body, ok)
	}
}

func TestLinesSplitAtLineEndings(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a\r\nb\nc", []string{"a", "b", "c"}},
		{"a\n", []string{"a"}},
		{"\n", []string{""}},
		{"a\rb\r", []string{"a\rb\r"}},
	}
	for _, test := range tests {
		if got := slices.Collect(lines(test.text)); !slices.Equal(got, test.want) {
			t.Errorf("lines(%q) = %q, want %q", test.text, got, test.want)
		}
	}
}
