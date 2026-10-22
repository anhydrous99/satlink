package sim

import (
	"bytes"
	"testing"
)

func TestTheHeadMayArriveInPieces(t *testing.T) {
	t.Parallel()
	var head requestHead

	pieces := []bool{
		head.absorb([]byte("GET / HTTP/1.0\r\nHost: web.test\r")),
		head.absorb([]byte("\n\r")),
		head.absorb([]byte("\n")),
	}

	if pieces[0] || pieces[1] || !pieces[2] {
		t.Errorf("complete after each piece: %v; want only after the last", pieces)
	}
}

func TestAnEndlessHeadIsAnsweredAnyway(t *testing.T) {
	t.Parallel()
	var head requestHead

	almost := head.absorb(bytes.Repeat([]byte("x"), maxHeadLen-1))
	enough := head.absorb(bytes.Repeat([]byte("x"), 10))

	if almost || !enough {
		t.Errorf("complete: %v at %d bytes, %v beyond; want false, then true", almost, maxHeadLen-1, enough)
	}
}

func TestTheResponseAnnouncesThePageLength(t *testing.T) {
	t.Parallel()

	got := pageResponse("hi\n")

	want := "HTTP/1.0 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 3\r\n\r\nhi\n"
	if string(got) != want {
		t.Errorf("pageResponse = %q; want %q", got, want)
	}
}
