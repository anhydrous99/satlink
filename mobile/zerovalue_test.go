package mobile

import (
	"fmt"
	"reflect"
	"testing"
)

// gomobile gives every struct without a constructor of its own a public
// no-argument one that makes a zero value, such as Java's new
// SatelliteLink(). These tests call every exported method of every such
// zero value, through reflect so that no method is forgotten, with zero
// arguments: none may panic, as a panic would end the app. A zero
// IridiumHandset is among them, though no binding makes one: in
// Objective-C and Swift, the init it inherits from NSObject makes an
// object that never reaches Go (unusableinits_test.go).

// errorType is the type of an error result.
var errorType = reflect.TypeFor[error]()

// unusableZeroValues are the zero values, with methods, of the types that
// only this package's functions make usable, by the name their errors give.
// (A zero TLSConnector, which has no methods, is refused as an argument.)
var unusableZeroValues = map[string]any{
	"IridiumHandset": &IridiumHandset{},
	"SatelliteLink":  &SatelliteLink{},
	"TCPConnection":  &TCPConnection{},
	"TLSConnection":  &TLSConnection{},
	"UDPConnection":  &UDPConnection{},
}

// usableZeroValues are the zero values, with methods, of the types that
// work as they are: settings, cancellations, lists and errors. The other
// records have no methods.
var usableZeroValues = []any{
	&LinkSettings{},
	&TLSSettings{},
	&Cancellation{},
	&StringList{},
	&SMSMessageList{},
	&KnownProductList{},
	&SatlinkError{},
}

// callWithZeroArguments calls method with the zero value of each parameter
// and returns its results, failing the test if it panics.
func callWithZeroArguments(t *testing.T, name string, method reflect.Value) []reflect.Value {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("%s panicked: %v", name, recovered)
		}
	}()
	arguments := make([]reflect.Value, method.Type().NumIn())
	for i := range arguments {
		arguments[i] = reflect.Zero(method.Type().In(i))
	}
	return method.Call(arguments)
}

// errorResult returns the error among results, if the method has one.
func errorResult(results []reflect.Value) (error, bool) {
	if len(results) == 0 || results[len(results)-1].Type() != errorType {
		return nil, false
	}
	err, _ := results[len(results)-1].Interface().(error)
	return err, true
}

func TestTheMethodsOfAZeroValueMadeByGomobileRefuseToWork(t *testing.T) {
	for typeName, zero := range unusableZeroValues {
		value := reflect.ValueOf(zero)
		for i := range value.NumMethod() {
			name := fmt.Sprintf("%s.%s", typeName, value.Type().Method(i).Name)
			t.Run(name, func(t *testing.T) {
				results := callWithZeroArguments(t, name, value.Method(i))

				err, returnsError := errorResult(results)
				if !returnsError {
					assertNoNilResult(t, name, results)
					return
				}
				reason := fmt.Sprintf("the %s was not made by this library", typeName)
				assertSatlinkError(t, err, &SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: reason}, "invalid argument: "+reason)
			})
		}
	}
}

func TestTheMethodsOfOtherZeroValuesWork(t *testing.T) {
	for _, zero := range usableZeroValues {
		value := reflect.ValueOf(zero)
		for i := range value.NumMethod() {
			name := fmt.Sprintf("%T.%s", zero, value.Type().Method(i).Name)
			t.Run(name, func(t *testing.T) {
				callWithZeroArguments(t, name, value.Method(i))
			})
		}
	}
}

// assertNoNilResult fails the test if a result of a method without an
// error, other than a list's Get, is a nil pointer: gomobile would hand it
// to Java as null and to Swift as nil.
func assertNoNilResult(t *testing.T, name string, results []reflect.Value) {
	t.Helper()
	for _, result := range results {
		if result.Kind() == reflect.Pointer && result.IsNil() {
			t.Errorf("%s returned a nil %s", name, result.Type())
		}
	}
}

func TestAZeroStatusHasItsStateAndDNSServers(t *testing.T) {
	status := (&SatelliteLink{}).Status()

	if status.State == nil || status.DNSServers == nil || status.DNSServers.Len() != 0 {
		t.Errorf("a zero link's status is %+v, want an empty State and DNSServers", *status)
	}
}

func TestAZeroHandsetHasNoEvents(t *testing.T) {
	if events := (&IridiumHandset{}).TakeEvents(); events == nil || events.Len() != 0 {
		t.Errorf("a zero handset's events are %v, want an empty list", events)
	}
}

func TestAZeroValueArgumentIsRefusedAsNotMadeByThisLibrary(t *testing.T) {
	_, err := EstablishLinkWithHandset(&IridiumHandset{}, NewLinkSettings(), nil, nil)

	assertSatlinkError(t, err,
		&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "the IridiumHandset was not made by this library"},
		"invalid argument: the IridiumHandset was not made by this library")
}
