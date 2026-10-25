package iridium

import (
	"errors"
	"fmt"

	"github.com/anhydrous99/satlink/internal/debugtext"
)

// Voice calls: dialling, answering — ending the data calls that answering
// may bring up by mistake — and hanging up. Everything here runs with the
// handset's lock held.

// dial is Dial, with the handset's lock held.
func (h *Handset) dial(number string) error {
	if err := validateNumber(number); err != nil {
		return err
	}
	_, err := h.exec(request{
		command:    dialCommand + number + voiceCallSuffix,
		timeout:    dialTimeout,
		completion: untilFinalResult,
	})
	return err
}

// answer is Answer, with the handset's lock held.
func (h *Handset) answer() error {
	// A late answer to a command that timed out would pass for ATA's. ATA
	// would go into a call that came up meanwhile, or one whose data never
	// stop, as its data: that call is ended instead.
	switch connected, err := h.resynchronize(); {
	case errors.Is(err, ErrCall):
		return h.endCallStillRelayed(err)
	case err != nil:
		return err
	case connected:
		return h.endCallThatConnected("before " + debugtext.Quote(answerCommand))
	}
	response, err := h.exec(request{command: answerCommand, timeout: dialTimeout, completion: untilFinalResult})
	switch {
	case err == nil && !response.Connected:
		return nil // a voice call, as asked
	case err == nil:
		// A data call answered by mistake: nothing else would end it.
		return h.endAnsweredDataCall(callError("ATA answered a data call, which was ended again"))
	case isUnfinished(err):
		// Still answering, perhaps a data call that would come up unseen:
		// abort, and end the call should it come up anyway.
		return h.abortAnswer(err)
	default:
		return err
	}
}

// abortAnswer aborts an ATA whose final result never came, which unanswered
// says, and settles what it answered: OK after the abort is the voice call,
// CONNECT a data call, which is ended, and any other result no call. With
// no result at all, the call is kept if the handset shows a voice call up,
// and ended as a data call otherwise.
func (h *Handset) abortAnswer(unanswered error) error {
	result, arrived, err := h.abortCallAttempt(untilFinalResult)
	switch {
	case err != nil || !arrived:
		// Nothing says which: ATA's OK may have been lost.
		if h.isInVoiceCall() {
			return nil
		}
		return h.endAnsweredDataCall(unanswered)
	case result.Kind == FinalResultOK:
		return nil
	case result.Kind == FinalResultConnect:
		return h.endAnsweredDataCall(unanswered)
	default:
		return unanswered
	}
}

// endAnsweredDataCall ends a data call ATA may have brought up: it returns
// ended once that is done, and ErrCallMayStillBeUp if it failed.
func (h *Handset) endAnsweredDataCall(ended error) error {
	if err := h.endDataCall(DefaultEscapeGuard); err != nil {
		return callMayStillBeUpError(fmt.Sprintf("ATA answered a data call that could not be ended: %v", err))
	}
	return ended
}

// endCallStillRelayed ends the data call of a handset found relaying one,
// which found says: it returns found once that is done, and
// ErrCallMayStillBeUp if it failed.
func (h *Handset) endCallStillRelayed(found error) error {
	if err := h.endDataCall(DefaultEscapeGuard); err != nil {
		return callMayStillBeUpError(fmt.Sprintf("the handset keeps sending data, and the call could not be ended: %v", err))
	}
	return found
}

// hangUp is HangUp, with the handset's lock held: ATH, within the hang-up
// timeout. OK when no call is up, and NO CARRIER when it had just ended,
// both mean that none is left.
func (h *Handset) hangUp() error {
	_, err := h.commandWithTimeout(hangUpCommand, h.hangUpTimeout())
	if isNoCarrier(err) {
		return nil
	}
	return err
}
