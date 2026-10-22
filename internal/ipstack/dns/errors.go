package dns

import "errors"

// The errors a socket's operations return. smoltcp has one enum per
// operation (StartQueryError, and GetQueryResultError for TakeQueryResult)
// with these variants; each operation documents which of them it returns.
// smoltcp capitalises their texts ("Invalid name"); Go error strings start in
// lower case.
var (
	// ErrInvalidName reports a name to look up that is empty or has an
	// empty label or one longer than 63 octets.
	ErrInvalidName = errors.New("invalid name")
	// ErrNameTooLong reports a name to look up whose wire form exceeds
	// MaxNameSize octets.
	ErrNameTooLong = errors.New("name too long")
	// ErrPending reports a query that has no answer yet.
	ErrPending = errors.New("query is not done yet")
	// ErrFailed reports a query that failed: the name does not exist, the
	// answer held no address, or no server answered in time.
	ErrFailed = errors.New("query failed")
)
