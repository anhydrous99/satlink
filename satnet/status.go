package satnet

import (
	"fmt"
	"time"

	"github.com/anhydrous99/satlink/ppp"
)

// What the app can observe about a link.

// DownReason is why PPP went down.
type DownReason = ppp.DownReason

// IPv4Settings is the IPv4 addressing PPP agreed on.
type IPv4Settings = ppp.IPv4Settings

// LinkState is where a link is in its life. It is comparable with ==.
type LinkState struct {
	// Kind is the stage of the link's life.
	Kind LinkStateKind
	// Reason is why the link is closing or closed (LinkStateClosing,
	// LinkStateClosed).
	Reason CloseReason
}

// String returns the state's name, with the reason for a closing or closed
// link, such as "Closed(the call dropped)".
func (s LinkState) String() string {
	switch s.Kind {
	case LinkStateClosing, LinkStateClosed:
		return s.Kind.String() + "(" + s.Reason.String() + ")"
	default:
		return s.Kind.String()
	}
}

// LinkStateKind is the stage of a link's life.
type LinkStateKind int

// The stages of a link's life.
const (
	// LinkStateNegotiating means the call is up and PPP is negotiating.
	LinkStateNegotiating LinkStateKind = iota
	// LinkStateUp means IP traffic flows.
	LinkStateUp
	// LinkStateClosing means the link is ending, for the state's Reason:
	// after a requested close, data already written is still delivered
	// first; then the call is hung up.
	LinkStateClosing
	// LinkStateClosed means the link is gone and the call has ended —
	// unless [Status].CallMayStillBeUp says the hang-up failed.
	LinkStateClosed
)

// String returns the stage's name, such as "Up".
func (k LinkStateKind) String() string {
	switch k {
	case LinkStateNegotiating:
		return "Negotiating"
	case LinkStateUp:
		return "Up"
	case LinkStateClosing:
		return "Closing"
	case LinkStateClosed:
		return "Closed"
	default:
		return fmt.Sprintf("LinkStateKind(%d)", int(k))
	}
}

// CloseReason is why a link ended. It is comparable with ==.
type CloseReason struct {
	// Kind classifies the reason.
	Kind CloseReasonKind
	// PPP is why PPP went down (CloseReasonPPP).
	PPP DownReason
	// Detail is what failed (CloseReasonTransport, CloseReasonInternal).
	Detail string
}

// String describes the reason, such as "the call dropped".
func (r CloseReason) String() string {
	switch r.Kind {
	case CloseReasonRequested:
		return "closed by the app"
	case CloseReasonCarrierLost:
		return "the call dropped"
	case CloseReasonIdleTimeout:
		return "idle for too long"
	case CloseReasonCallTimeLimit:
		return "call time limit reached"
	case CloseReasonDataLimit:
		return "data limit reached"
	case CloseReasonNegotiationTimedOut:
		return "PPP did not come up in time"
	case CloseReasonPPP:
		return "PPP " + r.PPP.String()
	case CloseReasonTransport:
		return "handset I/O failed: " + r.Detail
	case CloseReasonInternal:
		return "internal error: " + r.Detail
	default:
		return r.Kind.String()
	}
}

// CloseReasonKind classifies a [CloseReason].
type CloseReasonKind int

// The reasons a link ends.
const (
	// CloseReasonRequested means the app closed the link.
	CloseReasonRequested CloseReasonKind = iota
	// CloseReasonCarrierLost means the call dropped (NO CARRIER): signal
	// lost, or the network ended it.
	CloseReasonCarrierLost
	// CloseReasonIdleTimeout means there was no application traffic for the
	// configured idle timeout.
	CloseReasonIdleTimeout
	// CloseReasonCallTimeLimit means the call reached its configured
	// maximum duration.
	CloseReasonCallTimeLimit
	// CloseReasonDataLimit means the configured data volume is used up.
	CloseReasonDataLimit
	// CloseReasonNegotiationTimedOut means PPP did not come up within the
	// negotiation timeout.
	CloseReasonNegotiationTimedOut
	// CloseReasonPPP means PPP went down: terminated by the peer, failed
	// authentication, …
	CloseReasonPPP
	// CloseReasonTransport means reading from or writing to the handset
	// failed.
	CloseReasonTransport
	// CloseReasonInternal means a fault inside the link's own code. The link
	// still hangs up, and [Status].CallMayStillBeUp says if that failed.
	CloseReasonInternal
)

// String returns the kind's name, such as "CarrierLost".
func (k CloseReasonKind) String() string {
	switch k {
	case CloseReasonRequested:
		return "Requested"
	case CloseReasonCarrierLost:
		return "CarrierLost"
	case CloseReasonIdleTimeout:
		return "IdleTimeout"
	case CloseReasonCallTimeLimit:
		return "CallTimeLimit"
	case CloseReasonDataLimit:
		return "DataLimit"
	case CloseReasonNegotiationTimedOut:
		return "NegotiationTimedOut"
	case CloseReasonPPP:
		return "PPP"
	case CloseReasonTransport:
		return "Transport"
	case CloseReasonInternal:
		return "Internal"
	default:
		return fmt.Sprintf("CloseReasonKind(%d)", int(k))
	}
}

// Usage is the airtime and traffic so far.
type Usage struct {
	// ConnectedFor is the time since CONNECT, including the hang-up once it
	// is over: the billed airtime.
	ConnectedFor time.Duration
	// BytesSent counts the bytes written to the handset, PPP framing
	// included.
	BytesSent uint64
	// BytesReceived counts the bytes read from the handset, PPP framing
	// included.
	BytesReceived uint64
}

// Status is the state of a link at one moment. Two Status values compared with ==
// compare IPv4 by pointer, not the settings; compare the fields instead.
type Status struct {
	// State is where the link is in its life.
	State LinkState
	// ConnectMessage is what the handset reported after CONNECT, usually
	// the data rate.
	ConnectMessage string
	// IPv4 is the addressing PPP agreed on; nil until it has. It is the
	// Status's own copy.
	IPv4 *IPv4Settings
	// Usage is the airtime and traffic so far.
	Usage Usage
	// CallMayStillBeUp reports that the hang-up could not be confirmed
	// (neither ATH after the escape nor a DTR drop was), so the call may
	// still be up and billing. Check the handset — its EndDataCall retries
	// once it answers — and power-cycle it as a last resort.
	CallMayStillBeUp bool
}
