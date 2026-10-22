package mobile

import (
	"bytes"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/simtest"
)

// recordingTransport is a ByteTransport that records how it is called and
// answers every Read with answer, or every call with err.
type recordingTransport struct {
	written      [][]byte
	readMaxLen   int
	readTimeout  int64
	answer       []byte
	err          error
	raisedDTR    int
	droppedDTR   int
	panicMessage string
}

func (r *recordingTransport) Write(data []byte) error {
	r.mayPanic()
	r.written = append(r.written, bytes.Clone(data))
	return r.err
}

func (r *recordingTransport) Read(maxLen int, timeoutMillis int64) ([]byte, error) {
	r.mayPanic()
	r.readMaxLen, r.readTimeout = maxLen, timeoutMillis
	return r.answer, r.err
}

func (r *recordingTransport) RaiseDTR() error {
	r.raisedDTR++
	return r.err
}

func (r *recordingTransport) DropDTR() error {
	r.droppedDTR++
	return r.err
}

// mayPanic panics with panicMessage, if one is set, as a buggy transport
// might.
func (r *recordingTransport) mayPanic() {
	if r.panicMessage != "" {
		panic(r.panicMessage)
	}
}

func TestTheTransportWaitsWholeMillisecondsThatAJavaIntHolds(t *testing.T) {
	cases := []struct {
		timeout time.Duration
		want    int64
	}{
		{timeout: -time.Second, want: 1},
		{timeout: 0, want: 1},
		{timeout: 500 * time.Microsecond, want: 1},
		{timeout: 1900 * time.Microsecond, want: 1},
		{timeout: 2 * time.Second, want: 2000},
		{timeout: math.MaxInt32 * time.Millisecond, want: math.MaxInt32},
		{timeout: (math.MaxInt32 + 1) * time.Millisecond, want: math.MaxInt32},
		{timeout: math.MaxInt64, want: math.MaxInt32},
	}
	for _, c := range cases {
		host := &recordingTransport{}

		if _, err := newForeignTransport(host).ReadTimeout(make([]byte, 8), c.timeout); err != nil {
			t.Fatalf("ReadTimeout: %v", err)
		}

		if host.readTimeout != c.want {
			t.Errorf("a %v read waits %d ms, want %d", c.timeout, host.readTimeout, c.want)
		}
	}
}

func TestAReadCopiesAtMostWhatFits(t *testing.T) {
	host := &recordingTransport{answer: []byte("0123456789")}
	buffer := make([]byte, 4)

	n, err := newForeignTransport(host).ReadTimeout(buffer, time.Second)

	if err != nil || n != len(buffer) || string(buffer) != "0123" {
		t.Errorf("ReadTimeout = %d, %v with %q, want 4 bytes, \"0123\"", n, err, buffer)
	}
	if host.readMaxLen != len(buffer) {
		t.Errorf("the transport was asked for %d bytes, want %d", host.readMaxLen, len(buffer))
	}
}

func TestAReadOfNothingIsNoData(t *testing.T) {
	host := &recordingTransport{answer: nil}

	n, err := newForeignTransport(host).ReadTimeout(make([]byte, 8), time.Millisecond)

	if n != 0 || err != nil {
		t.Errorf("ReadTimeout = %d, %v, want 0, nil", n, err)
	}
}

func TestWritesReachTheTransportButEmptyOnesDoNot(t *testing.T) {
	host := &recordingTransport{}
	transport := newForeignTransport(host)

	if err := transport.WriteAll(nil); err != nil {
		t.Fatalf("WriteAll(nil): %v", err)
	}
	if err := transport.WriteAll([]byte("AT\r")); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}

	if len(host.written) != 1 || string(host.written[0]) != "AT\r" {
		t.Errorf("the transport was written %q, want only \"AT\\r\"", host.written)
	}
}

func TestDTRChangesReachTheTransport(t *testing.T) {
	host := &recordingTransport{}
	transport := newForeignTransport(host)

	if err := transport.DropDTR(); err != nil {
		t.Fatalf("DropDTR: %v", err)
	}
	if err := transport.RaiseDTR(); err != nil {
		t.Fatalf("RaiseDTR: %v", err)
	}

	if host.droppedDTR != 1 || host.raisedDTR != 1 {
		t.Errorf("DTR dropped %d and raised %d times, want once each", host.droppedDTR, host.raisedDTR)
	}
}

func TestHostErrorsBecomeTransportErrorsWithTheirMessage(t *testing.T) {
	operations := map[string]func(*foreignTransport) error{
		"write": func(f *foreignTransport) error { return f.WriteAll([]byte("AT\r")) },
		"read": func(f *foreignTransport) error {
			_, err := f.ReadTimeout(make([]byte, 8), time.Second)
			return err
		},
		"raise DTR": (*foreignTransport).RaiseDTR,
		"drop DTR":  (*foreignTransport).DropDTR,
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			host := &recordingTransport{err: errors.New("USB write failed (-1)")}

			err := operation(newForeignTransport(host))

			var core *satlink.Error
			if !errors.As(err, &core) || core.Kind != satlink.ErrTransport || core.Message != "USB write failed (-1)" {
				t.Errorf("error is %#v, want ErrTransport with the host's message", err)
			}
		})
	}
}

func TestHostErrorsOfThisPackageKeepTheirReason(t *testing.T) {
	core := &satlink.Error{Kind: satlink.ErrTransport, Message: "the simulated handset is closed"}
	cases := []struct {
		name string
		err  error
		want *satlink.Error
	}{
		{
			name: "a transport SatlinkError gives its reason",
			err:  &SatlinkError{Kind: SatlinkErrorTransport, Reason: "USB gone"},
			want: &satlink.Error{Kind: satlink.ErrTransport, Message: "USB gone"},
		},
		{
			name: "another SatlinkError gives its text",
			err:  &SatlinkError{Kind: SatlinkErrorTimeout, Command: "bulkTransfer"},
			want: &satlink.Error{Kind: satlink.ErrTransport, Message: "timed out waiting for bulkTransfer"},
		},
		{
			name: "a handset's transport error passes unchanged",
			err:  core,
			want: core,
		},
		{
			name: "a handset error of another kind gives its text",
			err:  &satlink.Error{Kind: satlink.ErrTimeout, Command: "AT"},
			want: &satlink.Error{Kind: satlink.ErrTransport, Message: `timed out waiting for a response to "AT"`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got *satlink.Error
			if !errors.As(hostError(c.err), &got) || *got != *c.want {
				t.Errorf("hostError(%v) = %#v, want %#v", c.err, got, c.want)
			}
		})
	}
}

func TestAHandsetReportsItsTransportsFailure(t *testing.T) {
	host := &recordingTransport{err: errors.New("USB write failed (-1)")}

	_, err := NewIridiumHandset(host)

	assertSatlinkError(t, err,
		&SatlinkError{Kind: SatlinkErrorTransport, Reason: "USB write failed (-1)"},
		"transport: USB write failed (-1)")
}

func TestAPanickingTransportIsAnInternalError(t *testing.T) {
	calls := map[string]func(ByteTransport) error{
		"NewIridiumHandset": func(transport ByteTransport) error {
			_, err := NewIridiumHandset(transport)
			return err
		},
		"EstablishLink": func(transport ByteTransport) error {
			_, err := EstablishLink(transport, NewLinkSettings(), nil, nil)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call(&recordingTransport{panicMessage: "a transport bug"})

			assertSatlinkError(t, err,
				&SatlinkError{Kind: SatlinkErrorInternal, Reason: "a transport bug"},
				"internal error: a transport bug")
		})
	}
}

// TestACallAPanicLeftUpIsEndedByANewHandsetOnTheAppsTransport is the
// ByteTransport counterpart of
// TestAPanicOnceTheCallIsUpClosesTheSerialPortWhichEndsTheCall. A
// ByteTransport stays the app's, open, and so may the call, when the
// handset ignores the DTR pulse the panic sets off — whether DTR drops end
// calls over USB is unverified — or the transport has no DTR line:
// EstablishLink's documentation and SatlinkErrorInternal's tell the app to
// end it with a new IridiumHandset on the transport, as the Rust bindings'
// unwinding left the call up.
func TestACallAPanicLeftUpIsEndedByANewHandsetOnTheAppsTransport(t *testing.T) {
	t.Parallel()
	port := &connectPanickingPort{closingPort: newClosingPort(t)}
	port.DeafToDTR.Store(true)
	usb := simtest.NewByteTransport(port)

	_, err := EstablishLink(usb, NewLinkSettings(), nil, nil)

	assertSatlinkError(t, err,
		&SatlinkError{Kind: SatlinkErrorInternal, Reason: bugOnceConnected},
		"internal error: "+bugOnceConnected)
	if !port.control.Stats().InDataCall {
		t.Fatal("the panic left no call up: EstablishLink's documentation says it may")
	}
	if _, err := NewIridiumHandset(usb); err != nil {
		t.Fatalf("NewIridiumHandset: %v", err)
	}
	if port.control.Stats().InDataCall {
		t.Error("the call is still up once a new handset was made on the transport")
	}
}

// TestAPanicOnceTheCallIsUpPulsesDTRWhichEndsTheCall: the core pulses DTR
// as the panic unwinds out of the dial, which after AT&D2 ends the call
// over a ByteTransport whose DTR line reaches the handset, where the Rust
// bindings' unwinding left the call up.
func TestAPanicOnceTheCallIsUpPulsesDTRWhichEndsTheCall(t *testing.T) {
	t.Parallel()
	port := &connectPanickingPort{closingPort: newClosingPort(t)}
	usb := simtest.NewByteTransport(port)

	_, err := EstablishLink(usb, NewLinkSettings(), nil, nil)

	assertSatlinkError(t, err,
		&SatlinkError{Kind: SatlinkErrorInternal, Reason: bugOnceConnected},
		"internal error: "+bugOnceConnected)
	if port.DTRDrops() < 1 {
		t.Error("DTR was never dropped")
	}
	if port.control.Stats().InDataCall {
		t.Error("the call is still up after the DTR pulse")
	}
}
