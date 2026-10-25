package iridium

import (
	"fmt"
	"time"
)

// Getting in step with the handset: initialising it — ending a data call a
// previous session left up — synchronising with a plain AT, and dropping
// what it still had to say before a command.

// The handset may still be talking when a session starts or a dial is
// prepared: answers to commands that timed out, or a call's data.
const (
	// initialQuiet is the silence that ends Initialize's first drain: the
	// end of whatever a previous session left the handset saying.
	initialQuiet = 300 * time.Millisecond
	// resyncQuiet is the silence that marks the end of whatever the handset
	// still had to say.
	resyncQuiet = 200 * time.Millisecond
	// resyncLimit is the longest the handset may keep talking: what still
	// flows after that is a call's data, not late answers.
	resyncLimit = 2 * time.Second
)

// Synchronising: AT until the handset answers.
const (
	// syncAttempts is how many plain ATs are tried before deciding the
	// handset is not listening.
	syncAttempts = 3
	// syncTimeout is how long each of them waits for the handset's answer.
	syncTimeout = 2 * time.Second
)

// initialize is Initialize, with the handset's lock held.
func (h *Handset) initialize() error {
	// Abort any half-entered SMS left over from a previous session, then
	// discard whatever was buffered.
	if err := h.write([]byte{esc, '\r'}); err != nil {
		return err
	}
	if err := h.drain(initialQuiet); err != nil {
		return err
	}
	if err := h.synchronizeEndingACallLeftUp(); err != nil {
		return err
	}
	if err := h.endCallHeldInCommandMode(); err != nil {
		return err
	}
	return h.configure()
}

// synchronizeEndingACallLeftUp synchronises with the handset. One that does
// not answer may still be relaying a data call a previous session left up
// (an app that crashed, say): that call is ended as EndDataCall ends it,
// with DefaultEscapeGuard, before trying once more. A handset that still
// does not answer once ending a call failed may be relaying one:
// ErrCallMayStillBeUp.
func (h *Handset) synchronizeEndingACallLeftUp() error {
	if h.synchronize() == nil {
		return nil
	}
	ended := h.endDataCall(DefaultEscapeGuard)
	err := h.synchronize()
	if err == nil || ended == nil {
		return err
	}
	return callMayStillBeUpError(fmt.Sprintf(
		"the handset does not answer (%v), and ending a data call it may be in failed: %v", err, ended))
}

// endCallHeldInCommandMode hangs up a data call held in online-command mode,
// where the handset answers commands while the call goes on. Firmware
// without +CPAS cannot tell, and is left as it is.
func (h *Handset) endCallHeldInCommandMode() error {
	if inDataCall, err := h.isInDataCall(); !inDataCall || err != nil {
		return err
	}
	if err := h.hangUp(); err != nil {
		return callMayStillBeUpError(fmt.Sprintf("ATH failed on a data call left up: %v", err))
	}
	return nil
}

// configure puts a synchronised handset into a known state: echo off,
// verbose results, verbose +CME ERROR, SMS PDU mode.
func (h *Handset) configure() error {
	if _, err := h.command("ATE0"); err != nil {
		return err
	}
	if _, err := h.command("ATV1"); err != nil {
		return err
	}
	// Optional on some firmware, as the handset works without them; but
	// what they show of a call is the caller's to hear about (callTrouble).
	for _, optional := range []string{"AT+CMEE=2", "AT+CMGF=0"} {
		if _, err := h.command(optional); callTrouble(err) != nil {
			return err
		}
	}
	return nil
}

// synchronize sends AT until the handset answers. One still in a call
// relays the far end, whose data may hold an OK line: only the handset's
// own result code, ending in a bare CR, shows that it is listening.
func (h *Handset) synchronize() error {
	for range syncAttempts {
		if err := h.writeCommandLine(attentionCommand); err != nil {
			return err
		}
		answered, err := h.awaitOwnResult(deadlineAfter(syncTimeout))
		if err != nil {
			return err
		}
		if answered {
			return nil
		}
	}
	return timeoutError(attentionCommand)
}

// resynchronize reads until the handset falls quiet, keeping unsolicited
// result codes and dropping the rest: late answers to commands that timed
// out, which would otherwise be taken as answers to the next ones. A
// handset that does not fall quiet is still relaying a call's data. It
// reports whether what it dropped held a CONNECT: a data call came up that
// no command awaited, a call the handset answered on its own, say, and is
// up still.
func (h *Handset) resynchronize() (connected bool, err error) {
	deadline := time.Now().Add(resyncLimit)
	var buf [readChunk]byte
	for {
		received, err := h.read(buf[:], resyncQuiet)
		if err != nil {
			return false, err
		}
		if received == 0 {
			break
		}
		if !time.Now().Before(deadline) {
			h.rx.clear()
			return false, callError("the handset keeps sending data: it is still online")
		}
		h.rx.push(buf[:received])
	}
	return h.keepURCs(), nil
}

// keepURCs queues the URCs among the complete lines received so far, up to
// a CONNECT, which it reports, and drops everything else received: what
// follows a CONNECT is its call's data.
func (h *Handset) keepURCs() (connected bool) {
	defer h.rx.clear()
	for line, ok := h.rx.nextLine(); ok; line, ok = h.rx.nextLine() {
		if result, isFinal := classifyFinalResult(line); isFinal && result.Kind == FinalResultConnect {
			return true
		}
		if isURC(line, "") {
			h.urcs = append(h.urcs, line)
		}
	}
	return false
}

// drain discards input until the handset is quiet for quiet, or for at most
// resyncLimit: one left in a call may relay data without pause.
func (h *Handset) drain(quiet time.Duration) error {
	deadline := time.Now().Add(resyncLimit)
	var buf [readChunk]byte
	for {
		received, err := h.read(buf[:], quiet)
		if err != nil {
			return err
		}
		if received == 0 || !time.Now().Before(deadline) {
			break
		}
	}
	h.rx.clear()
	return nil
}
