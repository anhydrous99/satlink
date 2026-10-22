package satlink

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Circuit-switched data calls (ATD without ';'): placing them, the
// online-data byte stream, and getting the handset back afterwards.
//
// Every path out of a call must really end it: a forgotten data call keeps
// airtime billing by the minute. Hence the care around timed-out dials,
// stale answers, and the +++ escape.

// DefaultEscapeGuard is the silence kept around +++: comfortably above the
// handsets' S12 guard time of 0.8–1 s.
const DefaultEscapeGuard = 1500 * time.Millisecond

// MaxEscapeGuard is the longest escape guard accepted: S12 itself goes up to
// 5.1 s.
const MaxEscapeGuard = 10 * time.Second

// The handset's verbose result codes end in a bare CR. Whatever runs over
// the call must keep bare CRs out of its own bytes — PPP does when CR is in
// its ACCM, as package satnet asks — so data cannot be mistaken for these.
const (
	noCarrierCode = "\r\nNO CARRIER\r"
	okCode        = "\r\nOK\r"
)

// Setting up a call.
const (
	// defaultConnectTimeout is how long a call is given to connect by
	// default: satellite call setup is slow.
	defaultConnectTimeout = 90 * time.Second
	// hangUpOnDTRDrop makes DTR going low end the call (Hayes &D2).
	hangUpOnDTRDrop = "AT&D2"
	// abortByte ends a call attempt still in progress, as any byte from us
	// does (Iridium ISU AT Command Reference, D); its final result follows
	// within seconds.
	abortByte = "\r"
	// abortGrace is how long that final result is awaited.
	abortGrace = 10 * time.Second
)

// BearerService is an AT+CBST bearer service: data rate, modem type and
// connection element (3GPP TS 27.007 §6.7).
type BearerService struct {
	// Speed is <speed>, e.g. 6 = 4800 bit/s V.32, 7 = 9600 bit/s V.32,
	// 71 = 9600 bit/s V.110.
	Speed int
	// Name is <name>, the modem type; 0 = asynchronous modem.
	Name int
	// ConnectionElement is <ce>; 1 = non-transparent, that is
	// error-corrected by the radio link protocol.
	ConnectionElement int
}

// command returns the AT+CBST command that selects b.
func (b BearerService) command() string {
	return fmt.Sprintf("AT+CBST=%d,%d,%d", b.Speed, b.Name, b.ConnectionElement)
}

// DataCallSettings says how to place or answer a data call. Start from
// [DefaultDataCallSettings]: the zero value's timeouts are zero, and it has
// no Cancellation. Copies of the settings share their Cancellation.
type DataCallSettings struct {
	// Bearer is selected with AT+CBST before dialling; nil keeps the
	// handset's setting.
	Bearer *BearerService

	// ConnectTimeout is how long to wait for CONNECT; satellite call setup
	// is slow. A call still trying after this is aborted. Zero or less
	// gives up at once.
	ConnectTimeout time.Duration

	// EscapeGuard is the silence kept before and after +++: the handset's
	// S12 guard time plus margin, at most [MaxEscapeGuard]. Zero or less
	// keeps none.
	EscapeGuard time.Duration

	// EndCallWhenDTRDrops sends AT&D2 first, so that the handset ends the
	// call when DTR drops. Desktop systems drop DTR when the serial port
	// closes, even when the program crashes; on Android only an explicit
	// close does (see the Android transport), so a killed app can leave the
	// call up until it next opens the handset. That handsets honour DTR
	// over USB is not yet verified on hardware.
	EndCallWhenDTRDrops bool

	// Cancellation stops the call while it is being set up; keep it to
	// cancel with. nil: the setup cannot be cancelled.
	Cancellation *Cancellation
}

// DefaultDataCallSettings returns the handset's own bearer, a 90-second
// connect timeout, [DefaultEscapeGuard], AT&D2 so that dropping DTR ends
// the call, and a new [Cancellation] for this call.
func DefaultDataCallSettings() DataCallSettings {
	return DataCallSettings{
		Bearer:              nil,
		ConnectTimeout:      defaultConnectTimeout,
		EscapeGuard:         DefaultEscapeGuard,
		EndCallWhenDTRDrops: true,
		Cancellation:        NewCancellation(),
	}
}

// DataCall is a connected data call.
//
// The handset is in online-data mode: what is read and written through this
// [Transport] belongs to the remote end, not to the AT command interpreter.
// [DataCall.HangUp] returns the handset to command mode. Until then the
// handset is busy: its own methods fail with [ErrBusy].
//
// Hang up every call, in a deferred function that checks HangUp's error,
// which says when the call may still be up. A DataCall dropped without
// HangUp ends nothing: the handset stays busy, [Handset.EndDataCall]
// included, and the call up and billing until [Handset.Close] — which for
// a serial port drops DTR, ending the call after AT&D2 — or the program's
// end. (The Rust version's DataCall held the handset, and so its port:
// dropping the call closed the port.)
//
// A DataCall is used by one goroutine at a time. Once HangUp has been
// called, WriteAll, ReadTimeout, RaiseDTR, DropDTR and HangUp itself fail
// with an [*Error] of kind [ErrCall] whose message is "the data call is
// over"; ConnectMessage and CarrierLost keep reporting the call's last
// state.
//
// The zero DataCall is not usable; [Handset.DialData] and
// [Handset.AnswerData] make one.
type DataCall struct {
	// handset is the handset the call holds; the call uses its transport
	// without its lock until HangUp.
	handset        *Handset
	connectMessage string
	// earlyData arrived together with CONNECT; it is served before
	// anything else.
	earlyData []byte
	// carrier watches the stream for the handset's NO CARRIER.
	carrier resultCodeWatch
	// escapeGuard is the silence kept around +++ when hanging up.
	escapeGuard time.Duration
	// lastWrite is when the call last wrote: the silence before +++
	// counts from then.
	lastWrite time.Time
	// over is set by HangUp: from then on the handset is not the call's.
	over bool
}

// A DataCall is the transport of whatever runs over the call, DTR included.
var (
	_ Transport     = (*DataCall)(nil)
	_ DTRController = (*DataCall)(nil)
)

// DialData dials number as a data call (ATD<number>) and waits for CONNECT,
// unless settings.Cancellation stops it first.
//
// number must be dialable (digits, '*' and '#' after an optional '+'), and
// settings.EscapeGuard at most [MaxEscapeGuard], so that the call could be
// ended; otherwise DialData fails with ErrInvalidArgument before anything
// is sent. The handset is prepared first: stale answers left by commands
// that timed out are dropped — they would be read as answers to ours — then
// AT&D2 and AT+CBST are sent as settings asks, each waiting for the
// handset's timeout, but at most ten seconds. A handset still in a call
// shows it there: it keeps talking for two seconds, relaying the call's
// data (ErrCall "the handset keeps sending data: it is still online"), or
// answers with a call's result code (ErrCall) or not at all (ErrTimeout).
// That call is ended as [DataCall.HangUp] ends one, and DialData fails with
// that error, or with the hang-up's (below). A handset may answer OK to the
// dial before the call connects, so OK does not end the wait; data that
// arrived together with CONNECT is kept for the first reads.
//
// When no CONNECT comes — the wait reaches settings.ConnectTimeout, is
// cancelled, or loses the transport — one byte is sent to abort the
// attempt, and only a final result other than CONNECT within ten seconds
// proves that it ended. Without that proof — a late CONNECT, no final
// result, the abort failing — the call might still come up, so it is ended
// as [DataCall.HangUp] ends one. The error is then the ErrTimeout, the
// ErrCancelled or the transport's error (or the abort's failure); if ending
// the call failed too, it is that failure instead, with CallMayStillBeUp
// set: errors.Is(err, ErrCallMayStillBeUp).
//
// A cancellation is seen within a fifth of a second, but never sooner than
// 200 ms after the dial command, as a handset may ignore an abort sent
// within 125 ms of a command (V.250 §5.6.1). One that comes before the dial
// command fails the setup with ErrCancelled, and nothing is dialled.
//
// On every error the handset is released for its own methods. It is back
// in command mode unless the error has CallMayStillBeUp set: the call may
// then still be up and billing; retry with [Handset.EndDataCall] once the
// handset answers again, and power-cycle it as the last resort. DialData
// fails with ErrBusy if a data call already holds the handset. A
// transport's panic releases the handset too, and is passed on once DTR
// has been pulsed, as [DataCall.HangUp] does, since the call may have come
// up meanwhile.
//
// The handset is busy from the start of the dial: like every method,
// DialData first waits for an exchange in progress in another goroutine —
// a PollURCs, say — and meanwhile the other methods fail with ErrBusy, and
// the Cancellation takes effect only once that exchange is over. A call
// that connects is the caller's to hang up (see [DataCall]).
func (h *Handset) DialData(number string, settings DataCallSettings) (*DataCall, error) {
	if err := validateNumber(number); err != nil {
		return nil, err
	}
	command := dialCommand + number
	return h.setUpCall(settings, func(settings DataCallSettings) outcome {
		return h.dialData(command, settings)
	})
}

// AnswerData answers an incoming data call (ATA, after RING) and waits for
// CONNECT, unless settings.Cancellation stops it first, checking settings,
// preparing the handset and handling a CONNECT that does not come as
// [Handset.DialData] does. An OK means a voice call was answered: it is
// hung up straight away and AnswerData fails with ErrCall "ATA answered a
// voice call, not a data call". Should ATH fail, the call is still up and
// is ended as a data call is: the error is then ATH's, or the hang-up's
// with CallMayStillBeUp set. The handset is busy from the start, a
// transport's panic is passed on as DialData passes it on, and a call that
// connects is the caller's to hang up.
func (h *Handset) AnswerData(settings DataCallSettings) (*DataCall, error) {
	return h.setUpCall(settings, h.answerData)
}

// EndDataCall ends a data call the handset may be in — including one a
// previous session left up — and returns it to command mode: guard of
// silence, +++, guard again, then ATH, or a DTR drop if the escape fails,
// as [DataCall.HangUp] does. It is harmless when no call is up: the escape
// is ignored and ATH answers OK.
//
// A guard over [MaxEscapeGuard] fails with ErrInvalidArgument, and a
// handset that a data call holds with ErrBusy, before anything is sent;
// every other error means the call may still be up: its CallMayStillBeUp
// is set. A call this process placed is ended with [DataCall.HangUp]
// instead.
func (h *Handset) EndDataCall(guard time.Duration) error {
	return h.perform(func() error {
		if err := validateEscapeGuard(guard); err != nil {
			return err
		}
		if err := h.endDataCall(guard); err != nil {
			return callMayStillBeUp(err)
		}
		return nil
	})
}

// ConnectMessage returns whatever followed CONNECT, usually the data rate
// ("9600").
func (c *DataCall) ConnectMessage() string {
	return c.connectMessage
}

// CarrierLost reports whether the handset said NO CARRIER: the call is over.
// Only the handset's own result code counts — "\r\nNO CARRIER\r", which
// data cannot contain once PPP escapes CR — even when split across reads.
func (c *DataCall) CarrierLost() bool {
	return c.carrier.seen
}

// WriteAll sends p to the remote end. After NO CARRIER the handset
// interprets what it receives as commands — A/ would even redial — so
// nothing more is written: it fails with ErrCall "NO CARRIER".
func (c *DataCall) WriteAll(p []byte) error {
	if err := c.checkNotOver(); err != nil {
		return err
	}
	if c.carrier.seen {
		return noCarrierError()
	}
	err := c.handset.write(p)
	c.lastWrite = time.Now()
	return err
}

// ReadTimeout reads what the remote end sent, waiting at most timeout, as
// [Transport] specifies. Data that arrived together with CONNECT comes
// first. It watches the stream for the handset's NO CARRIER.
func (c *DataCall) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	if err := c.checkNotOver(); err != nil {
		return 0, err
	}
	if len(c.earlyData) > 0 {
		served := copy(p, c.earlyData)
		c.earlyData = c.earlyData[served:]
		return served, nil
	}
	received, err := c.handset.read(p, timeout)
	if err != nil {
		return 0, err
	}
	c.carrier.observe(p[:received])
	return received, nil
}

// RaiseDTR raises the handset transport's DTR line, if it has one.
func (c *DataCall) RaiseDTR() error {
	if err := c.checkNotOver(); err != nil {
		return err
	}
	return c.handset.raiseDTR()
}

// DropDTR drops the handset transport's DTR line, if it has one; with
// AT&D2 that ends the call.
func (c *DataCall) DropDTR() error {
	if err := c.checkNotOver(); err != nil {
		return err
	}
	return c.handset.dropDTR()
}

// HangUp ends the call and returns the handset to command mode, releasing
// it for its own methods whatever the outcome, even when the transport
// panics. A transport's panic is passed on once DTR has been dropped for
// 150 ms and raised again, best effort, which after AT&D2 ends the call
// all the same: the Rust version's hang-up owned the handset, and its
// unwinding dropped the transport, which dropped DTR.
//
// The +++ escape is used even after NO CARRIER: that text could have come
// from the remote end, and in command mode the escape is harmless. Silence
// of the escape guard is kept before +++ (counted from the last write) and
// the handset is given three guard times to confirm with its own OK; an
// unconfirmed escape is tried once more, except after NO CARRIER, when the
// handset is most likely back in command mode, where the escape goes
// unanswered: once is enough to be sure. An OK that comes early, from a far
// end that ignores the ACCM, still has the guard waited out, as a byte from
// us inside it would cancel the escape.
//
// ATH follows even when the escape went unconfirmed: in command mode it
// ends the call, and if the handset is still online it is a few harmless
// bytes of data. While the handset may still be online, only its own result
// codes count as its answer: they end in a bare CR, which PPP keeps out of
// the data, so the far end cannot fake a successful hang-up. If the handset
// ignores the escape, DTR is dropped for 150 ms instead (the handset needs
// at least 50 ms to notice); data still flowing two seconds later shows
// that the drop changed nothing, and otherwise ATH ends whatever is left.
//
// An error means the hang-up could not be confirmed — neither ATH after the
// escape nor a DTR drop was — so the call may still be up and billing:
// every error HangUp returns has CallMayStillBeUp set, except that of a
// DataCall already hung up. Retry with [Handset.EndDataCall] once the
// handset answers again; power-cycling it is the last resort. When both the
// escape and the DTR drop fail, the escape's error is reported (ErrTimeout
// for command "+++" when the handset never left data mode).
func (c *DataCall) HangUp() error {
	if err := c.checkNotOver(); err != nil {
		return err
	}
	c.over = true
	handset := c.handset
	handset.mu.Lock()
	defer handset.mu.Unlock()
	// Deferred, so that a transport's panic releases the handset too, and
	// run before the unlock, so that a method waiting for the lock finds the
	// handset free.
	defer handset.release()
	returned := false
	// Run before the release: a panic that cut the hang-up short has DTR
	// pulsed while the handset is still the call's.
	defer handset.pulseDTRUnless(&returned)
	err := handset.endCall(c.plannedEscape())
	returned = true
	if err != nil {
		return callMayStillBeUp(err)
	}
	return nil
}

// checkNotOver fails once HangUp has been called: the handset is no longer
// the call's to use.
func (c *DataCall) checkNotOver() error {
	if c.over {
		return callError("the data call is over")
	}
	return nil
}

// plannedEscape is the escape that ends this call: quiet since its last
// write, and tried only once after NO CARRIER.
func (c *DataCall) plannedEscape() escape {
	plan := newEscape(c.escapeGuard, c.lastWrite)
	if c.carrier.seen {
		plan.attempts = escapeAttemptsAfterNoCarrier
	}
	return plan
}

// outcomeKind says how setting up a call ended.
type outcomeKind int

const (
	// callConnected means CONNECT: the handset is online.
	callConnected outcomeKind = iota
	// callFailed means no call; the handset is in command mode.
	callFailed
	// callPerhapsConnected means the attempt was given up, but the call
	// came up after all — or nothing proves it did not: it must be ended.
	callPerhapsConnected
)

// outcome is how setting up a call ended.
type outcome struct {
	kind outcomeKind
	// connectMessage is what followed CONNECT (callConnected).
	connectMessage string
	// err is why the attempt failed or was given up (callFailed,
	// callPerhapsConnected).
	err error
}

// connected is the outcome of a call that connected with connectMessage.
func connected(connectMessage string) outcome {
	return outcome{kind: callConnected, connectMessage: connectMessage}
}

// failed is the outcome of an attempt that ended without a call, as err
// says.
func failed(err error) outcome {
	return outcome{kind: callFailed, err: err}
}

// perhapsConnected is the outcome of an attempt given up for err, which may
// have connected nonetheless.
func perhapsConnected(err error) outcome {
	return outcome{kind: callPerhapsConnected, err: err}
}

// callStart places or answers a prepared call as settings say, waiting for
// CONNECT, and reports how that went.
type callStart func(settings DataCallSettings) outcome

// setUpCall claims the handset for a call, prepares it, starts the call
// with start, and settles how that went. Only a call that connected keeps
// the handset; every other way out, a panic included, releases it, and a
// panic has DTR pulsed first, as the call may have come up (see
// pulseDTRUnless).
func (h *Handset) setUpCall(settings DataCallSettings, start callStart) (*DataCall, error) {
	if err := h.claim(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	keepsHandset := false
	// Run before the unlock, so that a method waiting for the lock finds
	// the handset free.
	defer func() {
		if !keepsHandset {
			h.release()
		}
	}()
	returned := false
	defer h.pulseDTRUnless(&returned)
	call, err := h.connect(settings, start)
	returned = true
	keepsHandset = err == nil
	return call, err
}

// connect sets up the call and settles its outcome: a call that may have
// come up after it was given up on is ended.
func (h *Handset) connect(settings DataCallSettings, start callStart) (*DataCall, error) {
	result := h.prepareAndStart(settings, start)
	switch result.kind {
	case callConnected:
		return h.online(result.connectMessage, settings.EscapeGuard), nil
	case callPerhapsConnected:
		return nil, h.endAbandonedCall(result.err, settings.EscapeGuard)
	default:
		return nil, result.err
	}
}

// prepareAndStart prepares the handset and, unless the call was cancelled
// meanwhile, starts it with start.
func (h *Handset) prepareAndStart(settings DataCallSettings, start callStart) outcome {
	if err := h.prepare(settings); err != nil {
		return preparationFailed(err)
	}
	if settings.Cancellation.Cancelled() {
		return failed(cancelledError())
	}
	return start(settings)
}

// preparationFailed is the outcome of a preparation that failed with err. A
// call's result or its data without end, or silence where an answer was
// due: the handset may be in a call already, which must be ended.
func preparationFailed(err error) outcome {
	if errors.Is(err, ErrCall) || errors.Is(err, ErrTimeout) {
		return perhapsConnected(err)
	}
	return failed(err)
}

// prepare makes sure the call could be ended, drops stale answers left by
// commands that timed out — they would be read as answers to ours — then
// makes DTR drops end calls and picks the bearer.
func (h *Handset) prepare(settings DataCallSettings) error {
	if err := validateEscapeGuard(settings.EscapeGuard); err != nil {
		return err
	}
	if err := h.resynchronize(); err != nil {
		return err
	}
	// A handset left in a call never answers these, and their timeout is
	// what has that call ended (see preparationFailed): bounded as a
	// hang-up is.
	timeout := h.hangUpTimeout()
	if settings.EndCallWhenDTRDrops {
		if _, err := h.commandWithTimeout(hangUpOnDTRDrop, timeout); err != nil {
			return err
		}
	}
	if settings.Bearer != nil {
		if _, err := h.commandWithTimeout(settings.Bearer.command(), timeout); err != nil {
			return err
		}
	}
	return nil
}

// validateEscapeGuard refuses a guard longer than any handset's, with which
// a call could not be ended in reasonable time.
func validateEscapeGuard(guard time.Duration) error {
	if guard > MaxEscapeGuard {
		return invalidArgument(fmt.Sprintf("an escape guard of %s is longer than %s",
			debugDuration(guard), debugDuration(MaxEscapeGuard)))
	}
	return nil
}

// debugDuration writes d, which is not negative, as the Rust version's
// {:?} of a Duration does. Duration.String writes the same from a
// nanosecond to a minute, but Rust writes zero as "0ns"; from a second up,
// Rust writes whole seconds, then up to nine fractional digits without
// trailing zeros, then "s": "10s", "10.5s", and "90s" where String writes
// "1m30s".
func debugDuration(d time.Duration) string {
	switch {
	case d == 0:
		return "0ns"
	case d < time.Second:
		return d.String()
	}
	seconds, fraction := int64(d/time.Second), int64(d%time.Second)
	if fraction == 0 {
		return fmt.Sprintf("%ds", seconds)
	}
	digits := strings.TrimRight(fmt.Sprintf("%09d", fraction), "0")
	return fmt.Sprintf("%d.%ss", seconds, digits)
}

// dialData sends command, a data dial, and waits for CONNECT as settings
// say.
func (h *Handset) dialData(command string, settings DataCallSettings) outcome {
	response, err := h.exec(callRequest(command, settings, untilConnect))
	switch {
	case err == nil:
		return connected(response.ConnectMessage)
	case isUnfinished(err):
		return h.abortAttempt(err)
	default:
		return failed(err)
	}
}

// answerData answers with ATA and waits for CONNECT as settings say.
func (h *Handset) answerData(settings DataCallSettings) outcome {
	response, err := h.exec(callRequest(answerCommand, settings, untilFinalResult))
	switch {
	case err == nil && response.Connected:
		return connected(response.ConnectMessage)
	case err == nil:
		return h.hangUpVoiceCall()
	case isUnfinished(err):
		return h.abortAttempt(err)
	default:
		return failed(err)
	}
}

// callRequest is the request that places or answers a call with command:
// it waits up to the settings' connect timeout, until what until says,
// unless their cancellation stops it.
func callRequest(command string, settings DataCallSettings, until completion) request {
	return request{
		command:      command,
		timeout:      settings.ConnectTimeout,
		completion:   until,
		cancellation: settings.Cancellation,
	}
}

// hangUpVoiceCall hangs up a voice call answered instead of a data call —
// OK to ATA means a voice call — straight away. Should ATH fail, the call is
// still up and is ended as a data call is.
func (h *Handset) hangUpVoiceCall() outcome {
	if err := h.hangUp(); err != nil {
		return perhapsConnected(err)
	}
	return failed(callError("ATA answered a voice call, not a data call"))
}

// isUnfinished reports whether a dial or answer stopped waiting before its
// final result: timed out, cancelled, or cut off by a failing transport.
func isUnfinished(err error) bool {
	return errors.Is(err, ErrTimeout) || errors.Is(err, ErrCancelled) || errors.Is(err, ErrTransport)
}

// abortAttempt ends a call attempt that may still be trying to connect, as
// no final result came — the wait timed out or was cancelled, or the
// transport failed meanwhile, as unanswered says: one byte from us ends the
// attempt. Only a final result other than CONNECT proves it ended; the call
// might otherwise still come up. Nor does OK, which may still be unread
// when the transport failed: a dial's early one, or a voice call's that was
// answered.
func (h *Handset) abortAttempt(unanswered error) outcome {
	result, arrived, err := h.abortCallAttempt(untilConnect)
	switch {
	case err != nil:
		return perhapsConnected(err)
	case !arrived || result.Kind == FinalResultConnect:
		return perhapsConnected(unanswered)
	default:
		return failed(unanswered)
	}
}

// abortCallAttempt aborts a call attempt whose final result never came —
// one byte from us ends one still in progress — and waits for the result
// that follows, ending as until says, reporting whether one came.
func (h *Handset) abortCallAttempt(until completion) (FinalResult, bool, error) {
	if err := h.write([]byte(abortByte)); err != nil {
		return FinalResult{}, false, err
	}
	return h.awaitFinalResult(abortGrace, until)
}

// endAbandonedCall ends a call that may have come up after it was given up
// on, keeping guard of silence around +++. It returns why the call was
// given up, or, if it could not be ended, the hang-up's error with
// CallMayStillBeUp set.
func (h *Handset) endAbandonedCall(givenUp error, guard time.Duration) error {
	if err := h.endDataCall(guard); err != nil {
		return callMayStillBeUp(err)
	}
	return givenUp
}

// online returns the call that connected: the handset is in online-data
// mode, and what it sent after CONNECT is the call's first data.
func (h *Handset) online(connectMessage string, escapeGuard time.Duration) *DataCall {
	earlyData := h.rx.takeRemaining()
	carrier := newResultCodeWatch(noCarrierCode)
	carrier.observe(earlyData)
	return &DataCall{
		handset:        h,
		connectMessage: connectMessage,
		earlyData:      earlyData,
		carrier:        carrier,
		escapeGuard:    escapeGuard,
		lastWrite:      time.Now(),
	}
}

// resultCodeWatch spots result codes in a byte stream, even when split
// across reads.
type resultCodeWatch struct {
	codes [][]byte
	// tail is the end of the stream, kept to catch a code split across
	// reads: one byte short of the longest code.
	tail []byte
	// tailLength is the most of the stream tail keeps.
	tailLength int
	// seen is set once a code has gone by.
	seen bool
}

// newResultCodeWatch returns a watch for codes, none seen yet.
func newResultCodeWatch(codes ...string) resultCodeWatch {
	watch := resultCodeWatch{}
	longest := 1
	for _, code := range codes {
		watch.codes = append(watch.codes, []byte(code))
		longest = max(longest, len(code))
	}
	watch.tailLength = longest - 1
	return watch
}

// observe looks at data, the next bytes of the stream.
func (w *resultCodeWatch) observe(data []byte) {
	if w.seen || len(data) == 0 {
		return
	}
	w.tail = append(w.tail, data...)
	w.seen = w.tailHoldsCode()
	if excess := len(w.tail) - w.tailLength; excess > 0 {
		w.tail = append(w.tail[:0], w.tail[excess:]...)
	}
}

// tailHoldsCode reports whether one of the codes is in the tail.
func (w *resultCodeWatch) tailHoldsCode() bool {
	for _, code := range w.codes {
		if bytes.Contains(w.tail, code) {
			return true
		}
	}
	return false
}
