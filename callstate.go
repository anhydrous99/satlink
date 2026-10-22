package satlink

import "slices"

// Which call the handset is in, as AT+CPAS and AT+CLCC report it.
// Everything here runs with the handset's lock held.

// Call states, as the handset reports them.
const (
	// dataCallInProgress is what +CPAS reports while a data call is up
	// (Iridium ISU AT Command Reference, +CPAS and its table of call
	// states); a voice call reports 0 or 3.
	dataCallInProgress = 4
	// activeCall is the state of a call +CLCC lists as up, voice or data
	// (same reference and table); 006 is idle.
	activeCall = "000"
)

// isInDataCall reports whether +CPAS says that a data call is up; false
// when it cannot say.
func (h *Handset) isInDataCall() bool {
	fields, err := h.queryFields("AT+CPAS", "+CPAS")
	if err != nil {
		return false
	}
	status, err := parseNumber(fields, 0, "+CPAS", byteField)
	return err == nil && status == dataCallInProgress
}

// isInVoiceCall reports whether a voice call is up: the handset answers
// commands, which it does not while online, +CLCC lists a call up, and
// +CPAS says it is no data call. Late answers are dropped first, so that
// one to an earlier command cannot pass for AT's. false when it cannot say.
func (h *Handset) isInVoiceCall() bool {
	if h.resynchronize() != nil || h.synchronize() != nil {
		return false
	}
	if h.isInDataCall() {
		return false
	}
	states, err := h.queryFields("AT+CLCC", "+CLCC")
	return err == nil && slices.Contains(states, activeCall)
}
