package simtest

import (
	"math"
	"slices"
	"testing"
	"time"
)

func TestByteTransportWritesAndReadsThroughTheTransport(t *testing.T) {
	t.Parallel()
	port := &recordingPort{toRead: []byte("\r\nOK\r\n")}
	transport := NewByteTransport(port)

	writeErr := transport.Write([]byte("AT\r"))
	data, readErr := transport.Read(64, 250)

	if writeErr != nil || readErr != nil {
		t.Fatalf("Write, Read = %v, %v", writeErr, readErr)
	}
	if written, _, _ := port.seen(); !slices.Equal(written, []string{"AT\r"}) || string(data) != "\r\nOK\r\n" {
		t.Errorf("wrote %q and read %q", written, data)
	}
	if port.timeouts[0] != 250*time.Millisecond {
		t.Errorf("the transport waited %v; want 250ms", port.timeouts[0])
	}
}

func TestByteTransportReadsAtMostMaxLen(t *testing.T) {
	t.Parallel()
	transport := NewByteTransport(&recordingPort{toRead: []byte("abcdef")})

	data, err := transport.Read(4, 1)

	if err != nil || string(data) != "abcd" {
		t.Errorf("Read = %q, %v; want \"abcd\", nil", data, err)
	}
}

func TestByteTransportReturnsAnEmptySliceWhenNothingArrived(t *testing.T) {
	t.Parallel()
	transport := NewByteTransport(&recordingPort{})

	data, err := transport.Read(64, 1)

	if err != nil || data == nil || len(data) != 0 {
		t.Errorf("Read = %#v, %v; want an empty, non-nil slice", data, err)
	}
}

func TestAMaxLenBelowOneReadsNothing(t *testing.T) {
	t.Parallel()
	for _, maxLen := range []int{0, -1} {
		port := &recordingPort{toRead: []byte("unread")}
		transport := NewByteTransport(port)

		data, err := transport.Read(maxLen, 1)

		if err != nil || data == nil || len(data) != 0 || len(port.timeouts) != 0 {
			t.Errorf("Read(%d) = %#v, %v after %d reads; want an empty slice, no read", maxLen, data, err, len(port.timeouts))
		}
	}
}

func TestByteTransportTimeoutsSaturateInsteadOfOverflowing(t *testing.T) {
	t.Parallel()
	port := &recordingPort{}
	transport := NewByteTransport(port)

	for _, millis := range []int64{math.MaxInt64, math.MinInt64} {
		if _, err := transport.Read(1, millis); err != nil {
			t.Fatal(err)
		}
	}

	longest := time.Duration(longestMillis) * time.Millisecond
	if want := []time.Duration{longest, -longest}; !slices.Equal(port.timeouts, want) {
		t.Errorf("the transport waited %v; want %v", port.timeouts, want)
	}
}

func TestByteTransportPassesErrorsOnUnchanged(t *testing.T) {
	t.Parallel()
	transport := NewByteTransport(&recordingPort{failure: errPortGone})

	writeErr := transport.Write([]byte("x"))
	data, readErr := transport.Read(8, 1)
	raiseErr, dropErr := transport.RaiseDTR(), transport.DropDTR()

	for _, err := range []error{writeErr, readErr, raiseErr, dropErr} {
		if err != errPortGone {
			t.Errorf("got %v; want the port's own error", err)
		}
	}
	if data != nil {
		t.Errorf("a failed Read returned %q", data)
	}
}

func TestByteTransportChangesDTRThroughTheTransport(t *testing.T) {
	t.Parallel()
	port := &recordingPort{}
	transport := NewByteTransport(port)

	raiseErr, dropErr := transport.RaiseDTR(), transport.DropDTR()

	if _, dtrChanges, _ := port.seen(); raiseErr != nil || dropErr != nil || !slices.Equal(dtrChanges, []string{"raise", "drop"}) {
		t.Errorf("RaiseDTR, DropDTR = %v, %v with changes %q", raiseErr, dropErr, dtrChanges)
	}
}

func TestByteTransportWithoutADTRLineFailsAsTheHandsetWould(t *testing.T) {
	t.Parallel()
	transport := NewByteTransport(bareTransport{})

	assertTransportError(t, transport.RaiseDTR(), "this transport has no DTR line")
	assertTransportError(t, transport.DropDTR(), "this transport has no DTR line")
}

func TestByteTransportOverAFaultyTransportIsStubborn(t *testing.T) {
	t.Parallel()
	port := &recordingPort{}
	faulty := NewFaultyTransport(port)
	faulty.DeafToEscape.Store(true)
	faulty.DeafToDTR.Store(true)
	stubborn := NewByteTransport(faulty)

	writeErr, dropErr := stubborn.Write([]byte("+++")), stubborn.DropDTR()

	written, dtrChanges, _ := port.seen()
	if writeErr != nil || dropErr != nil || len(written) != 0 || len(dtrChanges) != 0 {
		t.Errorf("the escape and the DTR drop reached the port: %q, %q (%v, %v)", written, dtrChanges, writeErr, dropErr)
	}
}
