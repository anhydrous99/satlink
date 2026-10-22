package sim

import (
	"bytes"
	"fmt"
)

// The web server's side of an HTTP/1.0 exchange: wait for the request
// head, answer with the page, close.

const (
	// endOfHead is the blank line that ends a request head.
	endOfHead = "\r\n\r\n"
	// maxHeadLen is the longest request head waited for; a client that
	// sends more without a blank line is answered anyway.
	maxHeadLen = 8 * 1024
)

// requestHead is the request head received so far. The zero requestHead
// has received nothing.
type requestHead struct {
	bytes []byte
}

// absorb takes received bytes, and reports whether the head is complete.
func (h *requestHead) absorb(data []byte) bool {
	room := max(maxHeadLen-len(h.bytes), 0)
	h.bytes = append(h.bytes, data[:min(len(data), room)]...)
	return len(h.bytes) >= maxHeadLen || bytes.Contains(h.bytes, []byte(endOfHead))
}

// pageResponse returns the complete response that serves page.
func pageResponse(page string) []byte {
	return fmt.Appendf(nil, "HTTP/1.0 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s", len(page), page)
}
