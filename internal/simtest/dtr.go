package simtest

import "github.com/anhydrous99/satlink"

// noDTRLine is what satlink.Handset reports for a transport without a DTR
// line, and so what the wrappers of this package report too.
const noDTRLine = "this transport has no DTR line"

// dtrLineOf returns transport's DTR line, or, for a transport that has
// none, the error satlink.Handset reports in that case.
func dtrLineOf(transport satlink.Transport) (satlink.DTRController, error) {
	line, ok := transport.(satlink.DTRController)
	if !ok {
		return nil, &satlink.Error{Kind: satlink.ErrTransport, Message: noDTRLine}
	}
	return line, nil
}
