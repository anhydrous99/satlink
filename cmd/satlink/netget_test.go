package main

import (
	"crypto/x509"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/satnet"
	"golang.org/x/crypto/x509roots/fallback/bundle"
)

// An https:// fetch trusts the public web PKI — Mozilla's root program, and
// nothing else — asks nothing more of a server, such as a pinned key, and
// offers it no protocol through ALPN: a server offered h2 could answer an
// HTTP/1.0 request in HTTP/2. The golden file net-get-https-untrusted cannot show it: a
// fetch that trusted something else would fail there just the same, as
// would every real one, after paying for the call. So the test looks at
// the connector the fetch dials with.
func TestAnHTTPSFetchTrustsThePublicWebPKI(t *testing.T) {
	link := &linkRecorder{}

	_, _, err := fetch(link, mustParseHTTPURL(t, "https://example.com/"))

	if !errors.Is(err, errNotDialled) || link.tlsConnector == nil {
		t.Fatalf("fetch = %v, having dialled with the TLS connector %v; want a TLS dial", err, link.tlsConnector)
	}
	config := link.tlsConnector.Config()
	if !config.RootCAs.Equal(mozillasRootProgram(t)) {
		t.Error("the roots trusted are not Mozilla's root program")
	}
	if config.VerifyConnection != nil {
		t.Error("a server must also match a pinned key")
	}
	if len(config.NextProtos) != 0 {
		t.Errorf("the fetch offers %q through ALPN, but speaks HTTP/1.0 alone", config.NextProtos)
	}
}

// mozillasRootProgram returns the roots of Mozilla's program, from the NSS
// bundle that satnet takes the public web PKI from.
func mozillasRootProgram(t *testing.T) *x509.CertPool {
	t.Helper()
	roots := x509.NewCertPool()
	for root := range bundle.Roots() {
		certificate, err := x509.ParseCertificate(root.Certificate)
		if err != nil {
			t.Fatal(err)
		}
		roots.AddCert(certificate)
	}
	return roots
}

// The request names the port but keeps the fragment back.
func TestTheRequestNamesThePortButKeepsTheFragmentBack(t *testing.T) {
	request := httpRequest(mustParseHTTPURL(t, "http://example.com:8080/page?q=1#top"))

	if want := "GET /page?q=1 HTTP/1.0\r\nHost: example.com:8080\r\n"; !strings.HasPrefix(request, want) {
		t.Errorf("request = %q, want it to start with %q", request, want)
	}
}

func TestTheRequestNamesTheProgramAndAsksForTheConnectionToClose(t *testing.T) {
	request := httpRequest(mustParseHTTPURL(t, "http://web.test/"))

	want := "GET / HTTP/1.0\r\nHost: web.test\r\nUser-Agent: satlink-cli\r\nConnection: close\r\n\r\n"
	if request != want {
		t.Errorf("request = %q, want %q", request, want)
	}
}

// transferRecorder is a connection to a server that answers response,
// whatever it is asked. It records the request, the deadline set and how
// often, and the deadline in force as each write began. Setting a deadline
// fails with deadlineFailure, if set, and writing with writeFailure.
type transferRecorder struct {
	response                      *strings.Reader
	deadlineFailure, writeFailure error
	request                       []byte
	deadline                      time.Time
	deadlinesSet                  int
	writeDeadlines                []time.Time
}

func (c *transferRecorder) SetDeadline(deadline time.Time) error {
	if c.deadlineFailure != nil {
		return c.deadlineFailure
	}
	c.deadline = deadline
	c.deadlinesSet++
	return nil
}

func (c *transferRecorder) Write(p []byte) (int, error) {
	if c.writeFailure != nil {
		return 0, c.writeFailure
	}
	c.writeDeadlines = append(c.writeDeadlines, c.deadline)
	c.request = append(c.request, p...)
	return len(p), nil
}

func (c *transferRecorder) Read(p []byte) (int, error) {
	return c.response.Read(p)
}

// No other test could show the deadline sooner than ten minutes.
func TestNetGetAllowsTenMinutesForTheRequestAndItsWholeResponse(t *testing.T) {
	response := "HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\nhello"
	conn := &transferRecorder{response: strings.NewReader(response)}
	url := mustParseHTTPURL(t, "http://web.test/")
	before := time.Now()

	received, ending, err := exchange(conn, url)

	after := time.Now()
	if err != nil || string(received) != response || ending.kind != endingComplete {
		t.Fatalf("exchange = %q, %v, %v; want the response, complete", received, ending, err)
	}
	if string(conn.request) != httpRequest(url) {
		t.Errorf("requested %q, want %q", conn.request, httpRequest(url))
	}
	// As doc.go promises: one deadline, ten minutes away, from the request
	// on.
	const allowed = 10 * time.Minute
	if conn.deadlinesSet != 1 || !isAllowedAfter(conn.deadline, allowed, before, after) {
		t.Errorf("the deadline was set %d times, to %v; want once, to between %v and %v",
			conn.deadlinesSet, conn.deadline, before.Add(allowed), after.Add(allowed))
	}
	for i, deadline := range conn.writeDeadlines {
		if !deadline.Equal(conn.deadline) {
			t.Errorf("write %d of the request found the deadline %v, want %v", i, deadline, conn.deadline)
		}
	}
}

func TestAnExchangeEndsAtADeadlineOrRequestThatFails(t *testing.T) {
	refused := errors.New("refused")
	for name, conn := range map[string]*transferRecorder{
		// Without its deadline, the exchange could hold the call until a
		// limit ends it: nothing is sent.
		"the deadline": {deadlineFailure: refused},
		"the request":  {writeFailure: refused},
	} {
		conn.response = strings.NewReader("HTTP/1.0 204 No Content\r\n\r\n")

		received, _, err := exchange(conn, mustParseHTTPURL(t, "http://web.test/"))

		if err != refused || received != nil || len(conn.request) != 0 {
			t.Errorf("%s fails: exchange = %q, %v, having sent %q; want nothing and %v, having sent nothing",
				name, received, err, conn.request, refused)
		}
	}
}

// reply is one result of a receive function.
type reply struct {
	count int
	err   error
}

// readThen reads with readResponse from a receive function that first
// returns three bytes, then last.
func readThen(last reply) ([]byte, responseEnding) {
	replies := []reply{{count: 3}, last}
	return readResponse(func(p []byte) (int, error) {
		next := replies[0]
		replies = replies[1:]
		copy(p, strings.Repeat("x", next.count))
		return next.count, next.err
	})
}

// callDropped is how reading fails when the call has dropped.
var callDropped = &satnet.Error{Kind: satnet.ErrLinkDown, Reason: satnet.CloseReason{Kind: satnet.CloseReasonCarrierLost}}

// Reading stops at a close, a truncation or the deadline.
func TestReadingStopsAtACloseATruncationOrTheDeadline(t *testing.T) {
	tests := []struct {
		name   string
		last   reply
		ending responseEnding
	}{
		{"closed", reply{err: io.EOF}, responseEnding{kind: endingClosed}},
		{"truncated", reply{err: &satnet.Error{Kind: satnet.ErrTLSTruncated}}, responseEnding{kind: endingTruncated}},
		{"timed out", reply{err: &satnet.Error{Kind: satnet.ErrTimedOut, Operation: "TCP read"}},
			responseEnding{kind: endingTimedOut}},
		// What arrived before the call dropped is kept too.
		{"failed", reply{err: callDropped}, responseEnding{kind: endingFailed, err: callDropped}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, ending := readThen(test.last)

			if string(response) != "xxx" || ending != test.ending {
				t.Errorf("readResponse = %q, %v, want xxx, %v", response, ending, test.ending)
			}
		})
	}
}

func TestDataThatCameWithTheEndIsKept(t *testing.T) {
	response, ending := readThen(reply{count: 2, err: io.EOF})

	if string(response) != "xxxxx" || ending.kind != endingClosed {
		t.Errorf("readResponse = %q, %v, want xxxxx, Closed", response, ending)
	}
}

// Reading stops once the response is as long as announced.
func TestReadingStopsOnceTheResponseIsAsLongAsAnnounced(t *testing.T) {
	for _, chunks := range [][]string{
		{"HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\nhel", "lo"},
		{"HTTP/1.0 204 No Content\r\n\r\n"},
	} {
		// The server keeps the connection open: nothing comes after.
		remaining := chunks
		response, ending := readResponse(func(p []byte) (int, error) {
			if len(remaining) == 0 {
				t.Fatal("a read once the response is complete")
			}
			count := copy(p, remaining[0])
			remaining = remaining[1:]
			return count, nil
		})

		if want := strings.Join(chunks, ""); ending.kind != endingComplete || string(response) != want {
			t.Errorf("readResponse = %q, %v, want %q, Complete", response, ending, want)
		}
	}
}

// A body is complete when its length says so or the session ended properly.
func TestABodyIsCompleteWhenItsLengthSaysSoOrTheSessionEndedProperly(t *testing.T) {
	five := bodyLength{bytes: 5, known: true}
	unknown := bodyLength{}
	reset := responseEnding{kind: endingFailed, err: &satnet.Error{Kind: satnet.ErrConnectionReset}}
	tests := []struct {
		expected bodyLength
		received int
		ending   responseEnding
		complete bool
	}{
		{five, 5, responseEnding{kind: endingTruncated}, true},
		{five, 5, responseEnding{kind: endingTimedOut}, true},
		{five, 5, reset, true},
		{five, 3, responseEnding{kind: endingClosed}, false},
		{five, 3, reset, false},
		{five, 11, responseEnding{kind: endingClosed}, false},
		{unknown, 5, responseEnding{kind: endingClosed}, true},
		{unknown, 5, responseEnding{kind: endingTruncated}, false},
		{unknown, 5, responseEnding{kind: endingTimedOut}, false},
		{unknown, 5, reset, false},
	}
	for _, test := range tests {
		err := checkComplete(test.expected, test.received, test.ending)
		if complete := err == nil; complete != test.complete {
			t.Errorf("checkComplete(%+v, %d, %v) = %v, want complete %t",
				test.expected, test.received, test.ending, err, test.complete)
		}
	}
}

func TestAnIncompleteBodySaysWhatIsMissing(t *testing.T) {
	five := bodyLength{bytes: 5, known: true}
	dropped := responseEnding{kind: endingFailed, err: callDropped}
	tests := []struct {
		expected bodyLength
		received int
		ending   responseEnding
		want     string
	}{
		{five, 11, responseEnding{kind: endingTimedOut}, "the body has 11 bytes, more than the 5 the response announced"},
		{five, 3, responseEnding{kind: endingTimedOut}, "the response did not arrive in full within 600 s"},
		{five, 3, dropped, "the satellite link is down: the call dropped"},
		{five, 3, responseEnding{kind: endingTruncated}, "the response ended after 3 of 5 bytes"},
		{bodyLength{}, 3, dropped, "the satellite link is down: the call dropped"},
		{bodyLength{}, 3, responseEnding{kind: endingTruncated},
			"the server closed the connection without ending the TLS session, so the response may be cut short"},
	}
	for _, test := range tests {
		err := checkComplete(test.expected, test.received, test.ending)
		if err == nil || err.Error() != test.want {
			t.Errorf("checkComplete(%+v, %d, %v) = %v, want %s",
				test.expected, test.received, test.ending, err, test.want)
		}
	}
}

// A response whose head never ended cannot be used.
func TestAResponseWhoseHeadNeverEndedCannotBeUsed(t *testing.T) {
	for _, response := range []string{"", "HTTP/1.0 200 OK\r\nContent-Ty", "HTTP/1.0 200 OK\r\nContent-Length: 0\r\n"} {
		if _, _, ok := splitHTTPResponse([]byte(response)); ok {
			t.Errorf("splitHTTPResponse(%q) found a head", response)
		}
	}
	tests := []struct {
		received int
		ending   responseEnding
		says     string
	}{
		{0, responseEnding{kind: endingClosed}, "without replying"},
		{27, responseEnding{kind: endingTruncated}, "before its headers"},
		{27, responseEnding{kind: endingTimedOut}, "within"},
		{27, responseEnding{kind: endingFailed, err: callDropped}, "the call dropped"},
	}
	for _, test := range tests {
		if err := unfinishedHead(test.received, test.ending); !strings.Contains(err.Error(), test.says) {
			t.Errorf("unfinishedHead(%d, %v) = %v, want it to say %q", test.received, test.ending, err, test.says)
		}
	}
}

func TestEndingsAreNamed(t *testing.T) {
	tests := []struct {
		ending responseEnding
		want   string
	}{
		{responseEnding{kind: endingComplete}, "Complete"},
		{responseEnding{kind: endingClosed}, "Closed"},
		{responseEnding{kind: endingTruncated}, "Truncated"},
		{responseEnding{kind: endingTimedOut}, "TimedOut"},
		{responseEnding{kind: endingFailed, err: callDropped}, "Failed(the satellite link is down: the call dropped)"},
		{responseEnding{kind: 7}, "endingKind(7)"},
	}
	for _, test := range tests {
		if got := test.ending.String(); got != test.want {
			t.Errorf("String() = %q, want %q", got, test.want)
		}
	}
}

func TestTheFinalResponsesHeadGoesToStandardErrorAndItsBodyToStandardOutput(t *testing.T) {
	closed := responseEnding{kind: endingClosed}
	tests := []struct {
		name           string
		response       string
		ending         responseEnding
		stderr, stdout string
		err            string
	}{
		{"complete", "HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\nhello", responseEnding{kind: endingComplete},
			"HTTP/1.0 200 OK\r\nContent-Length: 5\n", "hello", ""},
		{"after an interim response", "HTTP/1.1 100 Continue\r\n\r\nHTTP/1.0 200 OK\r\n\r\nhello", closed,
			"HTTP/1.0 200 OK\n", "hello", ""},
		{"cut short", "HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\nhel", closed,
			"HTTP/1.0 200 OK\r\nContent-Length: 5\n", "hel", "the response ended after 3 of 5 bytes"},
		{"head unfinished", "HTTP/1.0 200 OK\r\nContent-Ty", closed,
			"", "", "the response ended after 27 bytes, before its headers did"},
		{"not HTTP", "SSH-2.0-OpenSSH_9.6\r\n\r\nhello", closed,
			"SSH-2.0-OpenSSH_9.6\n", "", "the reply is not an HTTP response"},
		{"framed as HTTP/1.0 cannot be", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello", closed,
			"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\n", "",
			`the reply uses Transfer-Encoding "chunked", which HTTP/1.0 does not allow`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr strings.Builder

			err := printHTTPResponse([]byte(test.response), test.ending, streams{stdout: &stdout, stderr: &stderr})

			if errorText(err) != test.err || stderr.String() != test.stderr || stdout.String() != test.stdout {
				t.Errorf("printed %q and %q, then %v; want %q and %q, then %q",
					stderr.String(), stdout.String(), err, test.stderr, test.stdout, test.err)
			}
		})
	}
}

func TestAResponseThatCannotBePrintedFails(t *testing.T) {
	response := []byte("HTTP/1.0 200 OK\r\nContent-Length: 5\r\n\r\nhello")
	complete := responseEnding{kind: endingComplete}
	for _, broken := range []streams{
		{stdout: io.Discard, stderr: brokenWriter{}},
		{stdout: brokenWriter{}, stderr: io.Discard},
	} {
		if err := printHTTPResponse(response, complete, broken); err == nil {
			t.Error("printing to a broken stream succeeded")
		}
	}
}
