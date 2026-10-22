package mobile

import (
	"bytes"
	"io"
	"math"
	"net"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/satnet"
)

// arrivedData is what a fakeStream's read hands over.
var arrivedData = []byte("abc")

// fakeStream is a stream connection whose one read returns n bytes of
// arrivedData with err, and whose read deadline refuses with deadlineErr.
type fakeStream struct {
	n           int
	err         error
	deadlineErr error
}

func (f fakeStream) Read(p []byte) (int, error) {
	return copy(p, arrivedData[:f.n]), f.err
}

func (f fakeStream) SetReadDeadline(time.Time) error {
	return f.deadlineErr
}

func TestReceivingTellsDataTheEndOfTheStreamAndFailuresApart(t *testing.T) {
	truncated := &satnet.Error{Kind: satnet.ErrTLSTruncated}
	cases := []struct {
		name   string
		stream fakeStream
		want   Received
	}{
		{name: "data", stream: fakeStream{n: 3}, want: Received{Data: arrivedData}},
		// Data that arrived with an error comes first; the next call reports
		// the error.
		{name: "data with the end of the stream", stream: fakeStream{n: 2, err: io.EOF}, want: Received{Data: arrivedData[:2]}},
		{name: "data with a truncation", stream: fakeStream{n: 2, err: truncated}, want: Received{Data: arrivedData[:2]}},
		{name: "the end of the stream", stream: fakeStream{err: io.EOF}, want: Received{Finished: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := receiveStream(c.stream, 64, 0)

			if err != nil || got.Finished != c.want.Finished || !bytes.Equal(got.Data, c.want.Data) {
				t.Errorf("receiveStream = %+v, %v, want %+v", got, err, c.want)
			}
		})
	}
}

func TestReceivingReportsWhatFailed(t *testing.T) {
	closed := &SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "the connection is closed"}
	cases := []struct {
		name     string
		stream   fakeStream
		want     *SatlinkError
		wantText string
	}{
		{
			// A session the server ended without its goodbye may be cut
			// short: never the end of the stream, which says it is whole.
			name:     "a TLS session cut short",
			stream:   fakeStream{err: &satnet.Error{Kind: satnet.ErrTLSTruncated}},
			want:     &SatlinkError{Kind: SatlinkErrorTLSTruncated},
			wantText: "the server closed the connection without ending the TLS session",
		},
		{
			name:     "nothing in time",
			stream:   fakeStream{err: &satnet.Error{Kind: satnet.ErrTimedOut, Operation: "TCP read"}},
			want:     &SatlinkError{Kind: SatlinkErrorTimeout, Command: "TCP read"},
			wantText: "timed out waiting for TCP read",
		},
		{
			name:     "a connection closed while reading",
			stream:   fakeStream{err: net.ErrClosed},
			want:     closed,
			wantText: "invalid argument: the connection is closed",
		},
		{
			name:     "a deadline a closed connection refuses",
			stream:   fakeStream{n: 3, deadlineErr: net.ErrClosed},
			want:     closed,
			wantText: "invalid argument: the connection is closed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := receiveStream(c.stream, 64, 0)

			if got != nil {
				t.Errorf("receiveStream returned %+v with its error", got)
			}
			assertSatlinkError(t, err, c.want, c.wantText)
		})
	}
}

func TestAReceiveBufferHoldsAtMost64KiB(t *testing.T) {
	// Receive's documented cap, written out, so that the test checks
	// maxReceiveLen instead of trusting it.
	const documentedCap = 64 * 1024
	// A slice, not a map: where an int has 32 bits, MaxInt is MaxInt32.
	cases := []struct{ maxLen, want int }{
		{maxLen: 0, want: 0},
		{maxLen: 1, want: 1},
		{maxLen: documentedCap, want: documentedCap},
		{maxLen: documentedCap + 1, want: documentedCap},
		{maxLen: math.MaxInt32, want: documentedCap},
		{maxLen: math.MaxInt, want: documentedCap},
	}
	for _, c := range cases {
		if buffer := receiveBuffer(c.maxLen); len(buffer) != c.want {
			t.Errorf("receiveBuffer(%d) = %d bytes, want %d", c.maxLen, len(buffer), c.want)
		}
	}
}

func TestAStreamBufferNeedsRoomForAByte(t *testing.T) {
	if buffer, err := streamBuffer(1); err != nil || len(buffer) != 1 {
		t.Errorf("streamBuffer(1) = %d bytes, %v, want 1", len(buffer), err)
	}
	for _, maxLen := range []int{0, -1, math.MinInt} {
		_, err := streamBuffer(maxLen)

		assertSatlinkError(t, err,
			&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "maxLen must be at least 1"},
			"invalid argument: maxLen must be at least 1")
	}
}

func TestADatagramBufferMayBeEmptyButNotNegative(t *testing.T) {
	if buffer, err := datagramBuffer(0); err != nil || len(buffer) != 0 {
		t.Errorf("datagramBuffer(0) = %d bytes, %v, want an empty buffer", len(buffer), err)
	}
	for _, maxLen := range []int{-1, math.MinInt} {
		_, err := datagramBuffer(maxLen)

		assertSatlinkError(t, err,
			&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "maxLen must not be negative"},
			"invalid argument: maxLen must not be negative")
	}
}
