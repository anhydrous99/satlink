package iridium

import "time"

// Getting the handset back from a data call: the Hayes escape to command
// mode, ATH, and dropping DTR as the last resort. Everything here runs with
// the handset's lock held.

// The Hayes escape from online-data mode back to command mode.
const (
	// escapeSequence is the Hayes escape sequence back to command mode,
	// written in one piece.
	escapeSequence = "+++"
	// escapeConfirmationGuards is how long, in guard times, the handset is
	// given to confirm the escape: it answers OK one guard time after +++;
	// allow three.
	escapeConfirmationGuards = 3
	// escapeAttempts is how many times +++ is tried: the first can be lost
	// if the remote end was still sending.
	escapeAttempts = 2
	// escapeAttemptsAfterNoCarrier is how many times +++ is tried after NO
	// CARRIER: the handset is then most likely back in command mode, where
	// the escape goes unanswered, so once is enough to be sure.
	escapeAttemptsAfterNoCarrier = 1
)

// rawHangUp is ATH as it is written while the handset may still be online:
// raw, not through exec, as only the handset's own result code may confirm
// it.
const rawHangUp = hangUpCommand + commandEnd

// dtrDrop is long enough for the handset to notice DTR dropping: above its
// S25 debounce time of 50 ms.
const dtrDrop = 150 * time.Millisecond

// escape is the Hayes escape from online-data mode back to command mode.
type escape struct {
	// guard is the silence kept before and after +++.
	guard time.Duration
	// quietSince is when we last wrote: the silence before +++ counts from
	// then.
	quietSince time.Time
	// attempts is how many times +++ is tried before giving up on it.
	attempts int
}

// newEscape returns an escape with guard of silence around +++, counted
// from quietSince, tried escapeAttempts times. A guard of zero or less
// keeps none.
func newEscape(guard time.Duration, quietSince time.Time) escape {
	return escape{guard: max(guard, 0), quietSince: quietSince, attempts: escapeAttempts}
}

// endDataCall ends a data call the handset may be in, keeping guard of
// silence around +++, as EndDataCall does once it has checked guard.
func (h *Handset) endDataCall(guard time.Duration) error {
	return h.endCall(newEscape(guard, time.Now()))
}

// endCall leaves data mode and hangs up; if the handset ignores the
// escape, it drops DTR instead. When both fail it reports the escape's
// error: dropping DTR is only the last resort.
func (h *Handset) endCall(plan escape) error {
	escapeErr := h.leaveDataMode(plan)
	if escapeErr == nil {
		return nil
	}
	if h.endCallByDroppingDTR() == nil {
		return nil
	}
	return escapeErr
}

// leaveDataMode escapes to command mode, then sends ATH. ATH is sent even
// when the escape went unconfirmed: in command mode it ends the call, and
// if the handset is still online it is a few harmless bytes of data.
func (h *Handset) leaveDataMode(plan escape) error {
	escaped, err := h.escapeToCommandMode(plan)
	if err != nil {
		return err
	}
	// Data-mode leftovers mean nothing in command mode.
	h.rx.clear()
	if escaped {
		return h.hangUp()
	}
	if h.hangUpWhileMaybeOnline() != nil {
		// What failed is the escape: the handset never left data mode.
		return timeoutError(escapeSequence)
	}
	return nil
}

// endCallByDroppingDTR drops DTR briefly: with AT&D2 the handset ends the
// call, with AT&D1 it only returns to command mode. ATH then ends whatever
// is left, and its answer confirms the handset is listening again.
func (h *Handset) endCallByDroppingDTR() error {
	if err := h.pulseDTR(); err != nil {
		return err
	}
	// Data still flowing shows the drop changed nothing. What the handset
	// says to the drop — NO CARRIER, or OK after AT&D1 — is dropped too, so
	// that it cannot pass for ATH's answer.
	if err := h.resynchronize(); err != nil {
		return err
	}
	return h.hangUpWhileMaybeOnline()
}

// pulseDTR drops DTR for long enough for the handset to notice, then
// raises it again.
func (h *Handset) pulseDTR() error {
	if err := h.dropDTR(); err != nil {
		return err
	}
	time.Sleep(dtrDrop)
	return h.raiseDTR()
}

// pulseDTRUnless pulses DTR unless *returned is set. Deferred around the
// part of a data call that holds the handset — setting the call up, or
// hanging it up — it pulses DTR only when a transport's panic unwinds
// through that part: the call may then be up, and with AT&D2 the pulse
// ends it. Unwinding does not close the handset, so DTR comes up again: with
// AT&D2 a DTR left down would end the next call.
//
// It is best effort, as nothing can be reported while a panic unwinds: a
// transport without a DTR line has nothing to pulse, and a second panic,
// from a transport that panics at everything, is dropped, so that the
// first goes on unchanged.
func (h *Handset) pulseDTRUnless(returned *bool) {
	if *returned {
		return
	}
	defer func() {
		// This recovers a panic of the pulse alone: the panic that has
		// pulseDTRUnless run is beyond its reach, and goes on unchanged.
		_ = recover()
	}()
	// Best effort, as said above: there is no caller to tell.
	_ = h.pulseDTR()
}

// hangUpWhileMaybeOnline sends ATH while the handset may still be online,
// relaying the far end — whose data could hold an OK line: only the
// handset's own OK or NO CARRIER, within the hang-up timeout, counts as its
// answer.
func (h *Handset) hangUpWhileMaybeOnline() error {
	if err := h.write([]byte(rawHangUp)); err != nil {
		return err
	}
	confirmed, err := h.awaitOwnResult(deadlineAfter(h.hangUpTimeout()))
	if err != nil {
		return err
	}
	if !confirmed {
		return timeoutError(hangUpCommand)
	}
	return nil
}

// escapeToCommandMode is the Hayes escape: a guard of silence, +++, and the
// handset switches to command mode once another guard passes without
// anything more from us. It reports whether the handset confirmed with OK
// (or NO CARRIER).
func (h *Handset) escapeToCommandMode(plan escape) (bool, error) {
	quietSince := plan.quietSince
	for range plan.attempts {
		if err := h.discardUntil(quietSince.Add(plan.guard)); err != nil {
			return false, err
		}
		if err := h.write([]byte(escapeSequence)); err != nil {
			return false, err
		}
		quietSince = time.Now()
		confirmed, err := h.awaitOwnResult(quietSince.Add(plan.guard * escapeConfirmationGuards))
		if err != nil {
			return false, err
		}
		if confirmed {
			// An OK can come early, from a far end that ignores the ACCM; a
			// byte from us inside the guard would still cancel the escape.
			if err := h.discardUntil(quietSince.Add(plan.guard)); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// discardUntil reads and discards incoming data until deadline, sending
// nothing.
func (h *Handset) discardUntil(deadline time.Time) error {
	var buf [readChunk]byte
	for {
		wait, ok := timeToWait(deadline)
		if !ok {
			return nil
		}
		if _, err := h.read(buf[:], wait); err != nil {
			return err
		}
	}
}

// awaitOwnResult waits until deadline for the handset's own OK or NO
// CARRIER, sending nothing meanwhile, and reports whether one came. Data
// from a call cannot pass for either: they end in a bare CR, which data
// never holds.
func (h *Handset) awaitOwnResult(deadline time.Time) (bool, error) {
	watch := newResultCodeWatch(okCode, noCarrierCode)
	var buf [readChunk]byte
	for !watch.seen {
		wait, ok := timeToWait(deadline)
		if !ok {
			return false, nil
		}
		received, err := h.read(buf[:], wait)
		if err != nil {
			return false, err
		}
		watch.observe(buf[:received])
	}
	return true, nil
}
