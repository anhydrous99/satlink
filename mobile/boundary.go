package mobile

import "fmt"

// What every exported function and method shares as the boundary to the
// host app: a panic that crossed it would end the app, as gomobile turns it
// into a crash, and the app's language can pass nil and zero values that Go
// code would not.

// recoverPanic turns a panic in progress into a SatlinkErrorInternal in
// *err, the error result of the exported function or method that deferred
// it. Every exported function and method that returns an error defers it
// first.
func recoverPanic(err *error) {
	if recovered := recover(); recovered != nil {
		*err = internalError(recovered)
	}
}

// survivePanic recovers a panic in progress in an exported method that has
// no error to report it with, and logs it: the method then returns the
// empty result it set before deferring survivePanic. method names it in the
// log, such as "SatelliteLink.Status".
func survivePanic(method string) {
	if recovered := recover(); recovered != nil {
		logError(fmt.Sprintf("recovered a panic in %s: %v", method, recovered))
	}
}

// internalError reports a recovered panic, whose value says what went
// wrong.
func internalError(recovered any) *SatlinkError {
	return &SatlinkError{Kind: SatlinkErrorInternal, Reason: fmt.Sprint(recovered)}
}

// made returns part, what this package's constructor puts into an object of
// type typeName, or, when part is nil, that the object is a zero value from
// gomobile's no-argument constructor, such as Java's new SatelliteLink().
func made[T any](part *T, typeName string) (*T, error) {
	if part == nil {
		return nil, invalidArgument(fmt.Sprintf("the %s was not made by this library", typeName))
	}
	return part, nil
}

// nilArgument reports a nil argument, naming the parameter.
func nilArgument(parameter string) *SatlinkError {
	return invalidArgument(parameter + " must not be nil")
}

// invalidArgument reports an argument or setting refused before anything
// was sent.
func invalidArgument(reason string) *SatlinkError {
	return &SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: reason}
}
