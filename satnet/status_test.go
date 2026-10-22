package satnet

import (
	"testing"

	"github.com/anhydrous99/satlink/ppp"
)

func TestCloseReasonsAreWordedAsInRust(t *testing.T) {
	for _, c := range []struct {
		reason CloseReason
		text   string
	}{
		{CloseReason{Kind: CloseReasonRequested}, "closed by the app"},
		{CloseReason{Kind: CloseReasonCarrierLost}, "the call dropped"},
		{CloseReason{Kind: CloseReasonIdleTimeout}, "idle for too long"},
		{CloseReason{Kind: CloseReasonCallTimeLimit}, "call time limit reached"},
		{CloseReason{Kind: CloseReasonDataLimit}, "data limit reached"},
		{CloseReason{Kind: CloseReasonNegotiationTimedOut}, "PPP did not come up in time"},
		{CloseReason{Kind: CloseReasonPPP, PPP: ppp.DownReason{Kind: ppp.DownReasonTerminatedByPeer}}, "PPP terminated by the peer"},
		{CloseReason{Kind: CloseReasonTransport, Detail: "transport error: unplugged"}, "handset I/O failed: transport error: unplugged"},
		{CloseReason{Kind: CloseReasonInternal, Detail: "a transport bug"}, "internal error: a transport bug"},
		{CloseReason{Kind: CloseReasonKind(42)}, "CloseReasonKind(42)"},
	} {
		if got := c.reason.String(); got != c.text {
			t.Errorf("String() = %q, want %q", got, c.text)
		}
	}
}

func TestCloseReasonKindsAreNamedAsTheRustVariants(t *testing.T) {
	names := map[CloseReasonKind]string{
		CloseReasonRequested:           "Requested",
		CloseReasonCarrierLost:         "CarrierLost",
		CloseReasonIdleTimeout:         "IdleTimeout",
		CloseReasonCallTimeLimit:       "CallTimeLimit",
		CloseReasonDataLimit:           "DataLimit",
		CloseReasonNegotiationTimedOut: "NegotiationTimedOut",
		CloseReasonPPP:                 "PPP",
		CloseReasonTransport:           "Transport",
		CloseReasonInternal:            "Internal",
	}
	for kind, name := range names {
		if got := kind.String(); got != name {
			t.Errorf("String() = %q, want %q", got, name)
		}
	}
}

func TestLinkStatesNameTheirReason(t *testing.T) {
	for _, c := range []struct {
		state LinkState
		text  string
	}{
		{LinkState{Kind: LinkStateNegotiating}, "Negotiating"},
		{LinkState{Kind: LinkStateUp}, "Up"},
		{closingState(CloseReasonRequested), "Closing(closed by the app)"},
		{LinkState{Kind: LinkStateClosed, Reason: CloseReason{Kind: CloseReasonCarrierLost}}, "Closed(the call dropped)"},
		{LinkState{Kind: LinkStateKind(7)}, "LinkStateKind(7)"},
	} {
		if got := c.state.String(); got != c.text {
			t.Errorf("String() = %q, want %q", got, c.text)
		}
	}
}
