package wire

import "errors"

// ErrMalformed reports a packet that could not be parsed: it is malformed,
// or uses something the stack does not support.
var ErrMalformed = errors.New("malformed or unsupported packet")
