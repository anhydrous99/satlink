package mobile

import (
	"errors"
	"fmt"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/satnet"
)

// Where an IridiumHandset's handset is: here, lent to a link, or stranded
// by a data call that could not be ended. The handset itself never leaves
// the IridiumHandset: while a link has it, its own methods fail with
// iridium.ErrBusy. It therefore cannot be lost with its link, and needs no
// transport opened again to be reached. An IridiumHandset closed while a
// link has its handset leaves the port open for that link, which hangs up
// through it, until the call has ended; and once closed, it refuses to work,
// wherever its handset is.

// handsetSlot is where an IridiumHandset's handset is.
type handsetSlot int

const (
	// slotHere means the handset is ready for commands.
	slotHere handsetSlot = iota
	// slotStranded means a data call the handset was in could not be ended
	// and may still be up — its link's, one Answer took by mistake, or one
	// whose CONNECT no command awaited — so only Reinitialize may use it.
	slotStranded
	// slotDialling means a link is being set up with the handset.
	slotDialling
	// slotLent means a link has the handset until its call ends.
	slotLent
)

// slotNames are the slots' names, indexed by slot.
var slotNames = [...]string{
	slotHere:     "Here",
	slotStranded: "Stranded",
	slotDialling: "Dialling",
	slotLent:     "Lent",
}

// String returns the slot's name, such as "Lent".
func (s handsetSlot) String() string {
	if s < 0 || int(s) >= len(slotNames) {
		return fmt.Sprintf("handsetSlot(%d)", int(s))
	}
	return slotNames[s]
}

// reclaim takes the handset back from a lent link whose call has ended,
// learning so from the link's status: stranded if the link could not hang
// up, otherwise ready. The methods call it with h.mu held, before anything else.
func (h *IridiumHandset) reclaim() {
	h.slotMu.Lock()
	defer h.slotMu.Unlock()
	if h.slot != slotLent {
		return
	}
	status := h.borrower.Status()
	if status.State.Kind != satnet.LinkStateClosed {
		return
	}
	// A Close while the link had the handset left the closing to a port
	// closer, which closes it now that the call has ended.
	h.handBack(status.CallMayStillBeUp)
}

// handBack records that the handset is back from a link: stranded if the
// call may still be up, otherwise ready. h.slotMu is held.
func (h *IridiumHandset) handBack(callMayStillBeUp bool) {
	h.slot, h.borrower = slotHere, nil
	if callMayStillBeUp {
		h.slot = slotStranded
	}
}

// unavailable reports why the handset cannot be used now, and nil when it
// can.
func (h *IridiumHandset) unavailable() error {
	h.slotMu.Lock()
	defer h.slotMu.Unlock()
	return h.unavailableNow()
}

// unavailableNow is unavailable with h.slotMu held: the app closed the
// IridiumHandset, a link has its handset, or the handset is stranded.
func (h *IridiumHandset) unavailableNow() error {
	if h.closed {
		return invalidArgument(handsetClosed)
	}
	return unavailableIn(h.slot)
}

// unavailableIn reports why a handset in slot cannot be used, and nil when
// it can.
func unavailableIn(slot handsetSlot) error {
	switch slot {
	case slotHere:
		return nil
	case slotStranded:
		return hangUpFailed(strandedReason)
	default:
		return &SatlinkError{Kind: SatlinkErrorHandsetBusy}
	}
}

// reinitialization returns how Reinitialize gets the handset talking again
// where it is now — Initialize for a ready one, endStrandedCall for a
// stranded one — or why it cannot.
func (h *IridiumHandset) reinitialization() (func(*iridium.Handset) error, error) {
	h.slotMu.Lock()
	defer h.slotMu.Unlock()
	switch {
	case h.closed:
		return nil, invalidArgument(handsetClosed)
	case h.slot == slotHere:
		return (*iridium.Handset).Initialize, nil
	case h.slot == slotStranded:
		return endStrandedCall, nil
	default:
		return nil, unavailableIn(h.slot)
	}
}

// recovered records that the handset answers again: no call is left up.
func (h *IridiumHandset) recovered() {
	h.slotMu.Lock()
	defer h.slotMu.Unlock()
	h.slot = slotHere
}

// strand records that a data call the handset was in could not be ended
// and may still be up: only Reinitialize may use a handset that was ready.
func (h *IridiumHandset) strand() {
	h.slotMu.Lock()
	defer h.slotMu.Unlock()
	if h.slot == slotHere {
		h.slot = slotStranded
	}
}

// lend lends the handset to a link being set up, unless it cannot be used
// now, and returns the loan through which the setup settles where the
// handset goes.
func (h *IridiumHandset) lend() (*handsetLoan, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reclaim()
	h.slotMu.Lock()
	defer h.slotMu.Unlock()
	if err := h.unavailableNow(); err != nil {
		return nil, err
	}
	h.slot = slotDialling
	return &handsetLoan{owner: h}, nil
}

// handsetLoan is an IridiumHandset's handset lent to a link being set up.
// The setup settles where the handset goes exactly once: on to its link
// (lentTo), or back
// to the IridiumHandset (returnedAfter). Only the goroutine that sets the
// link up uses the loan.
type handsetLoan struct {
	// owner is the IridiumHandset that lent its handset.
	owner *IridiumHandset
	// settled records that lentTo or returnedAfter has run.
	settled bool
}

// lentTo records that the link borrower follows is up and keeps the handset
// until its call ends. If the app closed the IridiumHandset while the link
// was set up, the handset closes once that call has ended.
func (l *handsetLoan) lentTo(borrower *satnet.Watcher) {
	h := l.owner
	h.mu.Lock()
	defer h.mu.Unlock()
	h.slotMu.Lock()
	defer h.slotMu.Unlock()
	h.slot, h.borrower = slotLent, borrower
	if h.closed {
		closeAfterCall(borrower, h.handset)
	}
	l.settled = true
}

// returnedAfter records that the link being set up with the handset did not
// come up, failing with establishing: the handset is back, stranded if the
// call may still be up.
func (l *handsetLoan) returnedAfter(establishing error) {
	l.owner.endDialling(errors.Is(establishing, iridium.ErrCallMayStillBeUp))
	l.settled = true
}

// abandonUnlessSettled abandons the loan unless the setup has settled it.
// EstablishLinkWithHandset defers it, so that a way out that settles
// nothing, a panic, still ends the lending. A settled loan leaves the slot
// alone: by then the handset may be another setup's.
func (l *handsetLoan) abandonUnlessSettled() {
	if !l.settled {
		l.owner.abandonDialling()
	}
}

// abandonDialling strands the handset of a link whose setting up ended
// without settling where the handset goes, which only a panic does: whether
// a call is up is unknown, and Reinitialize ends any. While a setup has
// yet to settle, the handset is still being lent to it, as nothing else
// changes that slot.
func (h *IridiumHandset) abandonDialling() {
	h.endDialling(true)
}

// endDialling takes the handset back, if it is still being lent, from a
// link that did not come up — stranded if the call may still be up — and
// closes it if the app closed the IridiumHandset meanwhile.
func (h *IridiumHandset) endDialling(callMayStillBeUp bool) {
	if h.handBackFromDialling(callMayStillBeUp) {
		// The app's Close returned long ago, so an error closing has
		// nowhere to go; why the link did not come up is what it hears.
		_ = h.handset.Close()
	}
}

// handBackFromDialling takes the handset back, if it is still being lent,
// and reports whether the app closed the IridiumHandset meanwhile, all
// under one lock, so that a Close cannot slip in between.
func (h *IridiumHandset) handBackFromDialling(callMayStillBeUp bool) (closedMeanwhile bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.slotMu.Lock()
	defer h.slotMu.Unlock()
	if h.slot != slotDialling {
		return false
	}
	h.handBack(callMayStillBeUp)
	return h.closed
}

// recordClose records that the app has closed the IridiumHandset, and
// reports whether the caller closes the handset now: not when it was closed
// already, nor while a link has it or is being set up with it, as that link
// hangs up through its port, which closes once the link is done with it.
func (h *IridiumHandset) recordClose() (closeNow bool) {
	h.slotMu.Lock()
	defer h.slotMu.Unlock()
	if h.closed {
		return false
	}
	h.closed = true
	switch h.slot {
	case slotLent:
		closeAfterCall(h.borrower, h.handset)
		return false
	case slotDialling:
		// lentTo starts the port closer, or endDialling closes the handset.
		return false
	default:
		return true
	}
}
