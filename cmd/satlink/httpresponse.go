package main

import (
	"bytes"
	"errors"
	"iter"
	"math"
	"strconv"
	"strings"

	"github.com/anhydrous99/satlink/internal/asciicase"
	"github.com/anhydrous99/satlink/internal/rustdebug"
	"github.com/anhydrous99/satlink/internal/utf8lossy"
)

// Reading an HTTP response: where its head ends, its status code, how long
// its body must be, and so where it ends.

// headerEnd is the blank line that ends a response's head.
const headerEnd = "\r\n\r\n"

// statusCodeLength is the number of digits in a status code (RFC 9110 §15).
const statusCodeLength = 3

// The interim responses (1xx) come before the final one (RFC 9110 §15.2).
const (
	firstInterimStatus = 100
	lastInterimStatus  = 199
)

// The statuses whose responses never carry a body, besides 1xx (RFC 9110
// §6.4.1).
const (
	statusNoContent   = 204
	statusNotModified = 304
)

// The header fields that frame a body, by their names in lower case.
const (
	contentLengthField    = "content-length"
	transferEncodingField = "transfer-encoding"
)

// splitHTTPResponse returns the head and the body of response, and false if
// the head never ended.
func splitHTTPResponse(response []byte) (head, body []byte, ok bool) {
	return bytes.Cut(response, []byte(headerEnd))
}

// finalResponse returns the head and the body of the final response, past
// any interim (1xx) responses before it (RFC 9110 §15.2), and false if its
// head never ended.
func finalResponse(response []byte) (head, body []byte, ok bool) {
	head, body, ok = splitHTTPResponse(response)
	for ok && isInterim(head) {
		head, body, ok = splitHTTPResponse(body)
	}
	return head, body, ok
}

// isInterim reports whether head is the head of an interim (1xx) response.
func isInterim(head []byte) bool {
	status, ok := statusCode(utf8lossy.String(head))
	return ok && status >= firstInterimStatus && status <= lastInterimStatus
}

// statusCode is the code of the status line that starts head, such as 200
// for "HTTP/1.1 200 OK", and false when head does not start with one.
func statusCode(head string) (int, bool) {
	fields := strings.Split(firstLine(head), " ")
	if len(fields) < 2 {
		return 0, false
	}
	version, code := fields[0], fields[1]
	if !strings.HasPrefix(version, "HTTP/") || !isStatusCode(code) {
		return 0, false
	}
	status, err := strconv.Atoi(code)
	return status, err == nil
}

// isStatusCode reports whether code is a status code's three digits.
func isStatusCode(code string) bool {
	if len(code) != statusCodeLength {
		return false
	}
	for _, digit := range []byte(code) {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// bodyLength is how long a response's body must be, when the response
// says. The length is a uint64, as Rust's usize is on 64-bit platforms, so
// that every length the Rust version took is taken.
type bodyLength struct {
	bytes uint64
	known bool
}

// expectedBodyLength is how long the body must be: empty for statuses that
// never carry one, otherwise what Content-Length says, if anything. Framing
// the client cannot check is an error rather than no length.
func expectedBodyLength(status int, head string) (bodyLength, error) {
	if status == statusNoContent || status == statusNotModified {
		return bodyLength{bytes: 0, known: true}, nil
	}
	// Not allowed in a reply to HTTP/1.0 (RFC 9112 §6.1).
	if coding, found := header(head, transferEncodingField); found {
		return bodyLength{}, errors.New("the reply uses Transfer-Encoding " + rustdebug.Quote(coding) + ", which HTTP/1.0 does not allow")
	}
	length, found := header(head, contentLengthField)
	if !found {
		return bodyLength{}, nil
	}
	// 1*DIGIT (RFC 9110 §8.6): ParseUint takes no sign, where Rust's parse
	// would also take "+5".
	bytes, err := strconv.ParseUint(length, 10, 64)
	if err != nil {
		return bodyLength{}, errors.New("the Content-Length " + rustdebug.Quote(length) + " is not a length")
	}
	return bodyLength{bytes: bytes, known: true}, nil
}

// header is the value of the header field called name, in lower case: its
// field lines joined by commas, which means the same (RFC 9110 §5.3); false
// when head has none.
func header(head, name string) (string, bool) {
	var values []string
	// The first line is the status line.
	_, fieldLines, _ := strings.Cut(head, "\n")
	for line := range lines(fieldLines) {
		field, value, found := strings.Cut(line, ":")
		// Field names compare as ASCII, as Rust's eq_ignore_ascii_case:
		// Unicode case folding, strings.EqualFold's, would take "ſ" for "s".
		if found && asciicase.EqualFold(strings.TrimSpace(field), name) {
			values = append(values, strings.TrimSpace(value))
		}
	}
	return strings.Join(values, ", "), len(values) > 0
}

// announcedEnd is where response will end, once its final head has arrived
// and says how long the body is, and false until then.
func announcedEnd(response []byte) (uint64, bool) {
	head, body, ok := finalResponse(response)
	if !ok {
		return 0, false
	}
	headText := utf8lossy.String(head)
	status, ok := statusCode(headText)
	if !ok {
		return 0, false
	}
	expected, err := expectedBodyLength(status, headText)
	if err != nil || !expected.known {
		return 0, false
	}
	beforeBody := uint64(len(response) - len(body))
	// A length past any address leaves the end to the server's close.
	if expected.bytes > math.MaxUint64-beforeBody {
		return 0, false
	}
	return beforeBody + expected.bytes, true
}

// firstLine is the first of text's lines, "" when it has none.
func firstLine(text string) string {
	for line := range lines(text) {
		return line
	}
	return ""
}

// lines yields the lines of text as Rust's str::lines does: split at "\n" or
// "\r\n", without the line endings, and no empty line after the last one.
func lines(text string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for line := range strings.Lines(text) {
			if withoutNewline, ended := strings.CutSuffix(line, "\n"); ended {
				line = strings.TrimSuffix(withoutNewline, "\r")
			}
			if !yield(line) {
				return
			}
		}
	}
}
