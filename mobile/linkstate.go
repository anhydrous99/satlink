package mobile

import (
	"net/netip"

	"github.com/anhydrous99/satlink/satnet"
)

// The app's copies of what satnet reports about a link: its state, why it
// ended, and its status.

// linkStateKinds are the LinkState constants of satnet's stages.
var linkStateKinds = map[satnet.LinkStateKind]int{
	satnet.LinkStateNegotiating: LinkStateNegotiating,
	satnet.LinkStateUp:          LinkStateUp,
	satnet.LinkStateClosing:     LinkStateClosing,
	satnet.LinkStateClosed:      LinkStateClosed,
}

// closeReasonKinds are the LinkCloseReason constants of satnet's reasons.
var closeReasonKinds = map[satnet.CloseReasonKind]int{
	satnet.CloseReasonRequested:           LinkCloseReasonRequested,
	satnet.CloseReasonCarrierLost:         LinkCloseReasonCarrierLost,
	satnet.CloseReasonIdleTimeout:         LinkCloseReasonIdleTimeout,
	satnet.CloseReasonCallTimeLimit:       LinkCloseReasonCallTimeLimit,
	satnet.CloseReasonDataLimit:           LinkCloseReasonDataLimit,
	satnet.CloseReasonNegotiationTimedOut: LinkCloseReasonNegotiationTimedOut,
	satnet.CloseReasonPPP:                 LinkCloseReasonPPP,
	satnet.CloseReasonTransport:           LinkCloseReasonTransport,
	satnet.CloseReasonInternal:            LinkCloseReasonInternal,
}

// linkStateOf returns the app's copy of state, with a Reason only for a
// link that is closing or closed.
func linkStateOf(state satnet.LinkState) *LinkState {
	copied := &LinkState{Kind: linkStateKinds[state.Kind]}
	if state.Kind == satnet.LinkStateClosing || state.Kind == satnet.LinkStateClosed {
		copied.Reason = closeReasonOf(state.Reason)
	}
	return copied
}

// closeReasonOf returns the app's copy of reason. Detail is what failed —
// for PPP, why PPP went down, as the Rust bindings word it — and "" for
// the reasons that need none.
func closeReasonOf(reason satnet.CloseReason) *LinkCloseReason {
	copied := &LinkCloseReason{Kind: closeReasonKinds[reason.Kind]}
	switch reason.Kind {
	case satnet.CloseReasonPPP:
		copied.Detail = reason.PPP.String()
	case satnet.CloseReasonTransport, satnet.CloseReasonInternal:
		copied.Detail = reason.Detail
	}
	return copied
}

// linkStatusOf returns the app's copy of status.
func linkStatusOf(status satnet.Status) *LinkStatus {
	copied := emptyLinkStatus()
	copied.State = linkStateOf(status.State)
	copied.ConnectMessage = status.ConnectMessage
	if status.IPv4 != nil {
		copied.LocalAddress = status.IPv4.LocalAddress.String()
		copied.DNSServers = addressListOf(status.IPv4.DNSServers)
	}
	copied.ConnectedSeconds = wholeSeconds(status.Usage.ConnectedFor)
	copied.BytesSent = countFromUnsigned(status.Usage.BytesSent)
	copied.BytesReceived = countFromUnsigned(status.Usage.BytesReceived)
	copied.CallMayStillBeUp = status.CallMayStillBeUp
	return copied
}

// emptyLinkStatus returns a status with nothing to report, its State and
// DNSServers set, as they never are nil.
func emptyLinkStatus() *LinkStatus {
	return &LinkStatus{State: &LinkState{}, DNSServers: &StringList{}}
}

// addressListOf returns addresses as text, such as "10.64.0.1".
func addressListOf(addresses []netip.Addr) *StringList {
	list := &StringList{items: make([]string, len(addresses))}
	for i, address := range addresses {
		list.items[i] = address.String()
	}
	return list
}
