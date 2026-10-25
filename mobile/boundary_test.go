package mobile

import (
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/anhydrous99/satlink/internal/logtest"
)

// acrossTheBoundary runs body as an exported function runs its own: a
// panic in it is recovered into a SatlinkErrorInternal.
func acrossTheBoundary(body func() error) (err error) {
	defer recoverPanic(&err)
	return body()
}

func TestARecoveredPanicBecomesAnInternalErrorWithItsValue(t *testing.T) {
	cases := map[string]any{
		"a string": "a transport bug",
		"an error": errors.New("a transport bug"),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			err := acrossTheBoundary(func() error { panic(value) })

			assertSatlinkError(t, err,
				&SatlinkError{Kind: SatlinkErrorInternal, Reason: "a transport bug"},
				"internal error: a transport bug")
		})
	}
}

func TestNoPanicLeavesTheErrorAlone(t *testing.T) {
	err := acrossTheBoundary(func() error { return nil })

	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestAMethodWithoutAnErrorSurvivesAPanicWithItsEmptyResult(t *testing.T) {
	// No t.Parallel: logtest.Record replaces the process-wide logger.
	records := logtest.Record(t, logPackage, slog.LevelError)

	list := func() (events *StringList) {
		events = &StringList{}
		defer survivePanic("a method under test")
		panic("a bug")
	}()

	if list == nil || list.Len() != 0 {
		t.Errorf("the method returned %v, want its empty list", list)
	}
	want := logtest.Line{Level: slog.LevelError, Text: "recovered a panic in a method under test: a bug"}
	if logged := records(); !slices.Contains(logged, want) {
		t.Errorf("logged %+v, want %+v", logged, want)
	}
}

func TestMadeTellsAZeroValueApart(t *testing.T) {
	part := &recordingTransport{}

	got, err := made(part, "Thing")
	_, zeroErr := made[recordingTransport](nil, "Thing")

	if got != part || err != nil {
		t.Errorf("made(part) = %v, %v, want part", got, err)
	}
	assertSatlinkError(t, zeroErr,
		&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "the Thing was not made by this library"},
		"invalid argument: the Thing was not made by this library")
}
