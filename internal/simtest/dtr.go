package simtest

import "github.com/anhydrous99/satlink/iridium"

// noDTRLine is what iridium.Handset reports for a transport without a DTR
// line, and so what the wrappers of this package report too.
const noDTRLine = "this transport has no DTR line"

// dtrLineOf returns transport's DTR line, or, for a transport that has
// none, the error iridium.Handset reports in that case.
func dtrLineOf(transport iridium.Transport) (iridium.DTRController, error) {
	line, ok := transport.(iridium.DTRController)
	if !ok {
		return nil, &iridium.Error{Kind: iridium.ErrTransport, Message: noDTRLine}
	}
	return line, nil
}
