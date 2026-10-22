package simtest

import (
	"slices"
	"testing"
	"time"
)

func TestWithEverySwitchOffAFaultyTransportChangesNothing(t *testing.T) {
	t.Parallel()
	port := &recordingPort{toRead: []byte("OK")}
	faulty := NewFaultyTransport(port)

	writeErr := faulty.WriteAll([]byte("+++"))
	buffer := make([]byte, 8)
	count, readErr := faulty.ReadTimeout(buffer, time.Second)
	raiseErr, dropErr := faulty.RaiseDTR(), faulty.DropDTR()
	closeErr := faulty.Close()

	for _, err := range []error{writeErr, readErr, raiseErr, dropErr, closeErr} {
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}
	}
	written, dtrChanges, closed := port.seen()
	if !slices.Equal(written, []string{"+++"}) || string(buffer[:count]) != "OK" {
		t.Errorf("wrote %q and read %q; want \"+++\" and \"OK\"", written, buffer[:count])
	}
	if !slices.Equal(dtrChanges, []string{"raise", "drop"}) || closed != 1 {
		t.Errorf("DTR changes %q, closed %d times; want raise, drop and one close", dtrChanges, closed)
	}
}

func TestUnpluggedFailsEveryCallButClose(t *testing.T) {
	t.Parallel()
	port := &recordingPort{toRead: []byte("data")}
	faulty := NewFaultyTransport(port)
	faulty.Unplugged.Store(true)
	calls := map[string]func() error{
		"WriteAll": func() error { return faulty.WriteAll([]byte("AT\r")) },
		"ReadTimeout": func() error {
			_, err := faulty.ReadTimeout(make([]byte, 8), time.Second)
			return err
		},
		"RaiseDTR": faulty.RaiseDTR,
		"DropDTR":  faulty.DropDTR,
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			assertTransportError(t, call(), "unplugged")
		})
	}
	closeErr := faulty.Close()

	written, dtrChanges, closed := port.seen()
	if len(written) != 0 || len(dtrChanges) != 0 || len(port.toRead) != len("data") {
		t.Errorf("an unplugged transport let through writes %q, DTR changes %q or reads", written, dtrChanges)
	}
	if closeErr != nil || closed != 1 {
		t.Errorf("Close = %v after %d closes; want nil and the port closed", closeErr, closed)
	}
}

func TestDeafToEscapeDropsAnEscapeWrittenInOnePiece(t *testing.T) {
	t.Parallel()
	port := &recordingPort{}
	faulty := NewFaultyTransport(port)
	faulty.DeafToEscape.Store(true)

	for _, data := range []string{"+++", "++", "+", "a+++", "ATH\r"} {
		if err := faulty.WriteAll([]byte(data)); err != nil {
			t.Fatalf("WriteAll(%q) = %v", data, err)
		}
	}

	written, _, _ := port.seen()
	if want := []string{"++", "+", "a+++", "ATH\r"}; !slices.Equal(written, want) {
		t.Errorf("wrote %q; want %q, all but the escape", written, want)
	}
}

func TestPanicsOnEscapePanicsWithATransportBugOnTheEscapeOnly(t *testing.T) {
	t.Parallel()
	port := &recordingPort{}
	faulty := NewFaultyTransport(port)
	faulty.PanicsOnEscape.Store(true)

	escapePanic := panicValue(func() { _ = faulty.WriteAll([]byte("+++")) }) // It panics before returning.
	dataPanic := panicValue(func() { _ = faulty.WriteAll([]byte("data")) })  // Checked below through what was written.

	if escapePanic != "a transport bug" || dataPanic != nil {
		t.Errorf("panics %v and %v; want \"a transport bug\" for the escape only", escapePanic, dataPanic)
	}
	if written, _, _ := port.seen(); !slices.Equal(written, []string{"data"}) {
		t.Errorf("wrote %q; want only the data", written)
	}
}

func TestPanicsOnNextEscapePanicsOnceAndTurnsItselfOff(t *testing.T) {
	t.Parallel()
	port := &recordingPort{}
	faulty := NewFaultyTransport(port)
	faulty.PanicsOnNextEscape.Store(true)

	first := panicValue(func() { _ = faulty.WriteAll([]byte("+++")) }) // It panics before returning.
	var err error
	second := panicValue(func() { err = faulty.WriteAll([]byte("+++")) })

	if first != "a transport bug" || second != nil {
		t.Errorf("panics %v then %v; want \"a transport bug\" once", first, second)
	}
	if written, _, _ := port.seen(); faulty.PanicsOnNextEscape.Load() || err != nil || !slices.Equal(written, []string{"+++"}) {
		t.Errorf("after the panic: switch %v, wrote %q, %v; want off and the second escape written",
			faulty.PanicsOnNextEscape.Load(), written, err)
	}
}

func TestFailsNextHangUpLosesOneATHAndTurnsItselfOff(t *testing.T) {
	t.Parallel()
	port := &recordingPort{}
	faulty := NewFaultyTransport(port)
	faulty.FailsNextHangUp.Store(true)

	otherErr := faulty.WriteAll([]byte("AT\r"))
	firstErr := faulty.WriteAll([]byte("ATH\r"))
	secondErr := faulty.WriteAll([]byte("ATH\r"))

	assertTransportError(t, firstErr, "USB write failed")
	if otherErr != nil || secondErr != nil || faulty.FailsNextHangUp.Load() {
		t.Errorf("AT, the second ATH = %v, %v with the switch %v; want both written and the switch off",
			otherErr, secondErr, faulty.FailsNextHangUp.Load())
	}
	if written, _, _ := port.seen(); !slices.Equal(written, []string{"AT\r", "ATH\r"}) {
		t.Errorf("wrote %q; want AT and the second ATH", written)
	}
}

func TestWithoutCPASTheHandsetHearsACommandItDoesNotKnow(t *testing.T) {
	t.Parallel()
	port := &recordingPort{}
	faulty := NewFaultyTransport(port)
	faulty.WithoutCPAS.Store(true)

	for _, command := range []string{"AT+CPAS\r", "AT\r", "AT+CPAS\r"} {
		if err := faulty.WriteAll([]byte(command)); err != nil {
			t.Fatalf("WriteAll(%q) = %v", command, err)
		}
	}

	if written, _, _ := port.seen(); !slices.Equal(written, []string{"AT+NOSUCH\r", "AT\r", "AT+NOSUCH\r"}) {
		t.Errorf("wrote %q; want every AT+CPAS replaced, the rest as it was", written)
	}
}

func TestPanicsOnNextReadPanicsOnceAndTurnsItselfOff(t *testing.T) {
	t.Parallel()
	port := &recordingPort{toRead: []byte("x")}
	faulty := NewFaultyTransport(port)
	faulty.PanicsOnNextRead.Store(true)
	buffer := make([]byte, 4)

	first := panicValue(func() { _, _ = faulty.ReadTimeout(buffer, time.Second) }) // It panics before returning.
	var count int
	var err error
	second := panicValue(func() { count, err = faulty.ReadTimeout(buffer, time.Second) })

	if first != "a transport bug" || second != nil {
		t.Errorf("panics %v then %v; want \"a transport bug\" once", first, second)
	}
	if faulty.PanicsOnNextRead.Load() || err != nil || string(buffer[:count]) != "x" {
		t.Errorf("after the panic: switch %v, read %q, %v; want off, \"x\", nil",
			faulty.PanicsOnNextRead.Load(), buffer[:count], err)
	}
}

func TestDeafToDTRDropsBothDTRChangesButCountsTheDrops(t *testing.T) {
	t.Parallel()
	port := &recordingPort{}
	faulty := NewFaultyTransport(port)
	faulty.DeafToDTR.Store(true)

	raiseErr, dropErr := faulty.RaiseDTR(), faulty.DropDTR()

	if raiseErr != nil || dropErr != nil {
		t.Errorf("RaiseDTR, DropDTR = %v, %v; want success reported", raiseErr, dropErr)
	}
	if _, dtrChanges, _ := port.seen(); len(dtrChanges) != 0 {
		t.Errorf("DTR changes %q reached the port", dtrChanges)
	}
	if faulty.DTRDrops() != 1 {
		t.Errorf("DTRDrops() = %d; want 1", faulty.DTRDrops())
	}
}

func TestDTRDropsCountsEveryDropWhateverTheSwitches(t *testing.T) {
	t.Parallel()
	faulty := NewFaultyTransport(&recordingPort{})

	_ = faulty.DropDTR() // Each of these drops counts, whatever it returns.
	faulty.DeafToDTR.Store(true)
	_ = faulty.DropDTR() // As above.
	faulty.Unplugged.Store(true)
	_ = faulty.DropDTR() // As above.

	if faulty.DTRDrops() != 3 {
		t.Errorf("DTRDrops() = %d; want 3", faulty.DTRDrops())
	}
}

func TestATransportWithoutADTRLineFailsDTRChangesAsTheHandsetWould(t *testing.T) {
	t.Parallel()
	faulty := NewFaultyTransport(bareTransport{})

	assertTransportError(t, faulty.RaiseDTR(), "this transport has no DTR line")
	assertTransportError(t, faulty.DropDTR(), "this transport has no DTR line")
	if got := faulty.DropDTR().Error(); got != "transport error: this transport has no DTR line" {
		t.Errorf("the error says %q", got)
	}
}

func TestCloseClosesTheWrappedTransportWhateverTheSwitches(t *testing.T) {
	t.Parallel()
	port := &recordingPort{}
	faulty := NewFaultyTransport(port)
	faulty.Unplugged.Store(true)
	faulty.DeafToDTR.Store(true)
	faulty.DeafToEscape.Store(true)

	err := faulty.Close()

	if _, _, closed := port.seen(); err != nil || closed != 1 {
		t.Errorf("Close = %v with %d closes; want the port closed", err, closed)
	}
}

func TestClosingATransportThatCannotBeClosedDoesNothing(t *testing.T) {
	t.Parallel()

	if err := NewFaultyTransport(bareTransport{}).Close(); err != nil {
		t.Errorf("Close = %v; want nil", err)
	}
}

func TestErrorsOfTheWrappedTransportComeThroughUnchanged(t *testing.T) {
	t.Parallel()
	faulty := NewFaultyTransport(&recordingPort{failure: errPortGone})

	writeErr := faulty.WriteAll([]byte("AT\r"))
	_, readErr := faulty.ReadTimeout(make([]byte, 4), 0)

	if writeErr != errPortGone || readErr != errPortGone {
		t.Errorf("errors %v and %v; want the port's own", writeErr, readErr)
	}
}
