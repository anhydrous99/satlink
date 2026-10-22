package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
	"unicode"

	"github.com/anhydrous99/satlink/internal/utf8lossy"
	"github.com/anhydrous99/satlink/satnet"
	"github.com/spf13/cobra"
)

// `satlink net get`: an HTTP/1.0 GET over the link, the response's head on
// standard error and its body on standard output.

// transferTimeout is the time allowed for an HTTP request and its whole
// response, once connected.
const transferTimeout = 600 * time.Second

// chunkSize is how many bytes a read takes at a time: over three seconds of
// what the satellite line carries.
const chunkSize = 1024

// userAgent names this program to the servers it fetches from.
const userAgent = programName + "-cli"

// getAction fetches url and prints its body.
type getAction struct {
	needsACall
	url httpURL
}

// getCommand is `satlink net get`. Its URL is checked as the command line is
// read, so that a bad URL never costs a call.
func getCommand(start func(netAction) error) *command {
	var action getAction
	return netSubcommand(&cobra.Command{Use: "get", Short: "Fetch an http:// or https:// URL and print its body"},
		start, func() netAction { return action }, requiredPositional("URL", "http:// or https:// URL to fetch", urlValue{target: &action.url}))
}

func (g getAction) target() (netTarget, bool) {
	return netTarget{host: g.url.host, port: g.url.port}, true
}

// execute fetches the URL with HTTP/1.0 and prints the response.
func (g getAction) execute(link netLink, s streams) error {
	response, ending, err := fetch(link, g.url)
	if err != nil {
		return err
	}
	// The response is in: hang up while it is written out, which can wait
	// on a slow reader such as a pager.
	link.RequestClose()
	return printHTTPResponse(response, ending, s)
}

// printHTTPResponse prints the final response of response, which ending
// ended: its head on standard error, then its body on standard output. It
// fails, after printing what arrived, when the body may be incomplete, and
// when the response cannot be used.
func printHTTPResponse(response []byte, ending responseEnding, s streams) error {
	head, body, ok := finalResponse(response)
	if !ok {
		return unfinishedHead(len(response), ending)
	}
	// Read as lossy UTF-8: what is not UTF-8 becomes U+FFFD.
	headText := utf8lossy.String(head)
	if err := writef(s.stderr, "%s\n", strings.TrimRightFunc(headText, unicode.IsSpace)); err != nil {
		return err
	}
	status, ok := statusCode(headText)
	if !ok {
		return errors.New("the reply is not an HTTP response")
	}
	expected, err := expectedBodyLength(status, headText)
	if err != nil {
		return err
	}
	// Like curl, keep what arrived even when it may be incomplete.
	if _, err := s.stdout.Write(body); err != nil {
		return err
	}
	return checkComplete(expected, len(body), ending)
}

// dialer is the link as a fetch uses it: it opens TCP connections, secured
// with TLS or not. *satnet.Link is one.
type dialer interface {
	Dial(host string, port uint16, timeout time.Duration) (*satnet.TCPConn, error)
	DialTLS(host string, port uint16, connector *satnet.TLSConnector, timeout time.Duration) (*satnet.TLSConn, error)
}

// fetch sends a GET for url over link and reads the response, all within
// transferTimeout once connected.
func fetch(link dialer, url httpURL) ([]byte, responseEnding, error) {
	conn, err := connect(link, url)
	if err != nil {
		return nil, responseEnding{}, err
	}
	defer func() {
		// The response is in, or abandoned, so a failure to close changes
		// nothing; the link's own close follows. Closing a TLS connection
		// sends a close_notify before TCP's end, within the deadline, where
		// a plain TCP close would send only the end.
		_ = conn.Close()
	}()
	return exchange(conn, url)
}

// exchangeStream is the connection as an exchange uses it: one deadline for
// the request and its response, the request written, and the response
// read. A connection that connect opens is one.
type exchangeStream interface {
	io.ReadWriter
	SetDeadline(deadline time.Time) error
}

// exchange sends the GET for url over conn and reads the response, with one
// deadline for the request and the whole response: transferTimeout from
// now.
func exchange(conn exchangeStream, url httpURL) ([]byte, responseEnding, error) {
	if err := conn.SetDeadline(time.Now().Add(transferTimeout)); err != nil {
		return nil, responseEnding{}, err
	}
	if _, err := io.WriteString(conn, httpRequest(url)); err != nil {
		return nil, responseEnding{}, err
	}
	response, ending := readResponse(conn.Read)
	return response, ending, nil
}

// connect opens a TCP connection over link to the URL's host and port,
// secured with TLS for https://, within connectTimeout.
func connect(link dialer, url httpURL) (net.Conn, error) {
	if !url.secure {
		conn, err := link.Dial(url.host, url.port, connectTimeout)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	connector, err := newHTTPSConnector()
	if err != nil {
		return nil, err
	}
	conn, err := link.DialTLS(url.host, url.port, connector, connectTimeout)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// newHTTPSConnector returns the connector that secures an https:// fetch: it
// trusts the public web PKI and pins no key.
func newHTTPSConnector() (*satnet.TLSConnector, error) {
	return satnet.NewTLSConnector(satnet.TLSOptions{})
}

// httpRequest is the HTTP/1.0 GET of url, asking the server to close the
// connection after the response.
func httpRequest(url httpURL) string {
	return fmt.Sprintf("GET %s HTTP/1.0\r\nHost: %s\r\nUser-Agent: %s\r\nConnection: close\r\n\r\n",
		url.path, url.authority(), userAgent)
}

// endingKind is how a response ended.
type endingKind int

const (
	// endingComplete means its body reached the length its head announced.
	endingComplete endingKind = iota
	// endingClosed means the server closed the connection properly.
	endingClosed
	// endingTruncated means the server closed the connection without ending
	// the TLS session, so the response may be cut short. Many servers do,
	// after sending everything.
	endingTruncated
	// endingTimedOut means the transfer timeout ran out first.
	endingTimedOut
	// endingFailed means the connection or the link failed: reset, call
	// dropped, or a limit reached.
	endingFailed
)

// endingNames are the kinds' names, indexed by kind.
var endingNames = [...]string{
	endingComplete:  "Complete",
	endingClosed:    "Closed",
	endingTruncated: "Truncated",
	endingTimedOut:  "TimedOut",
	endingFailed:    "Failed",
}

// String returns the kind's name, such as "Truncated".
func (k endingKind) String() string {
	if k < 0 || int(k) >= len(endingNames) {
		return fmt.Sprintf("endingKind(%d)", int(k))
	}
	return endingNames[k]
}

// responseEnding is how a response ended, and, when reading failed, why.
type responseEnding struct {
	kind endingKind
	// err is why reading failed (endingFailed).
	err error
}

// String returns the ending's kind, with the failure for endingFailed:
// "Failed(the connection was reset)".
func (e responseEnding) String() string {
	if e.kind == endingFailed {
		return fmt.Sprintf("%v(%v)", e.kind, e.err)
	}
	return e.kind.String()
}

// failure is why reading stopped short when the time allowed ran out or
// reading failed, and nil otherwise.
func (e responseEnding) failure() error {
	switch e.kind {
	case endingTimedOut:
		return transferTimedOut()
	case endingFailed:
		return e.err
	default:
		return nil
	}
}

// readResponse reads with receive until the response is complete, the
// server closes the connection, the time allowed runs out, or reading
// fails, and keeps what arrived. Reading stops at the announced end of the
// body: a server that keeps the connection open regardless would otherwise
// hold the call until the idle limit.
func readResponse(receive func(p []byte) (int, error)) ([]byte, responseEnding) {
	var response []byte
	var end uint64
	endKnown := false
	chunk := make([]byte, chunkSize)
	for {
		count, err := receive(chunk)
		response = append(response, chunk[:count]...)
		if !endKnown {
			end, endKnown = announcedEnd(response)
		}
		if endKnown && uint64(len(response)) >= end {
			return response, responseEnding{kind: endingComplete}
		}
		if err != nil {
			return response, endingOf(err)
		}
	}
}

// endingOf is how a response ended whose reading stopped with err.
func endingOf(err error) responseEnding {
	switch {
	case errors.Is(err, io.EOF):
		return responseEnding{kind: endingClosed}
	case errors.Is(err, satnet.ErrTLSTruncated):
		return responseEnding{kind: endingTruncated}
	case errors.Is(err, satnet.ErrTimedOut):
		return responseEnding{kind: endingTimedOut}
	default:
		return responseEnding{kind: endingFailed, err: err}
	}
}

// checkComplete accepts a body as long as the response says it is; without
// a length, only a proper close shows that nothing is missing.
func checkComplete(expected bodyLength, received int, ending responseEnding) error {
	if expected.known {
		return checkLength(expected.bytes, uint64(received), ending)
	}
	return checkEnding(ending)
}

// checkLength accepts a body of received bytes when the response announced
// as many, expected.
func checkLength(expected, received uint64, ending responseEnding) error {
	switch {
	case received == expected:
		return nil
	case received > expected:
		return fmt.Errorf("the body has %d bytes, more than the %d the response announced", received, expected)
	}
	if err := ending.failure(); err != nil {
		return err
	}
	return fmt.Errorf("the response ended after %d of %d bytes", received, expected)
}

// checkEnding accepts a body whose length the response did not announce
// only when the server closed the connection properly.
func checkEnding(ending responseEnding) error {
	if ending.kind == endingComplete || ending.kind == endingClosed {
		return nil
	}
	if err := ending.failure(); err != nil {
		return err
	}
	return errors.New("the server closed the connection without ending the TLS session, " +
		"so the response may be cut short")
}

// unfinishedHead says why a response whose head never ended cannot be used.
func unfinishedHead(received int, ending responseEnding) error {
	if err := ending.failure(); err != nil {
		return err
	}
	if received == 0 {
		return errors.New("the server closed the connection without replying")
	}
	return fmt.Errorf("the response ended after %d bytes, before its headers did", received)
}

// transferTimedOut says that the response took longer than transferTimeout.
func transferTimedOut() error {
	return fmt.Errorf("the response did not arrive in full within %d s", int64(transferTimeout/time.Second))
}
