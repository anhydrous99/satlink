package iridium

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anhydrous99/satlink/internal/asciicase"
	"github.com/anhydrous99/satlink/internal/debugtext"
)

// Command execution: writing a command, and collecting its answer and its
// final result. Everything here runs with the handset's lock held.

// commandEnd ends every command line: register S3, carriage return.
const commandEnd = "\r"

// commandSettle is how long a command line waits after the handset last
// sent something. A 9555A drops about one command in four written within a
// millisecond of its previous answer, with no result code at all, and none
// written a millisecond or more after it. Twenty milliseconds leaves a wide
// margin, and costs nothing beside the satellite's latencies.
const commandSettle = 20 * time.Millisecond

// readChunk is the most read from the transport at a time.
const readChunk = 512

// Cancelling a dial or an answer while it waits for CONNECT.
const (
	// abortIgnoredFor is how long a cancellation waits after the command it
	// stops: a handset may ignore what it receives in the first 125 ms
	// after a command (V.250 §5.6.1), so an abort sent sooner could be
	// lost.
	abortIgnoredFor = 200 * time.Millisecond
	// cancellationCheck is how often a cancellable wait looks at its
	// cancellation.
	cancellationCheck = 200 * time.Millisecond
)

// completion says what ends a command.
type completion int

const (
	// untilFinalResult waits for the command's final result code. CONNECT
	// only counts for commands that can take the handset online.
	untilFinalResult completion = iota
	// untilConnect waits for CONNECT or a failure. A handset may answer OK
	// to a dial command before the call connects, so OK does not end it.
	untilConnect
)

// endsWith reports whether result ends a wait until c: every final result
// does, except an OK while CONNECT is awaited.
func (c completion) endsWith(result FinalResult) bool {
	return c != untilConnect || result.Kind != FinalResultOK
}

// request is a command for exec to run.
type request struct {
	// command is the command line without its CR.
	command string
	// timeout bounds the wait for the final result.
	timeout time.Duration
	// completion says what ends the command.
	completion completion
	// textEntry is written once the handset prompts with "> ": an SMS text
	// and the Ctrl-Z that ends it. Empty when the command takes no text.
	textEntry []byte
	// cancellation, once cancelled, ends the wait of a dial or answer as a
	// timeout does, with ErrCancelled: the attempt may still be in
	// progress. nil for a command that cannot be cancelled.
	cancellation *Cancellation
	// reportsConnect has a CONNECT that the command cannot await fail it
	// with ErrCall, its call left up for the caller to end, instead of
	// being ended here (see unawaitedConnect): a data call's setup ends
	// such a call itself, once, with the escape guard its settings give.
	reportsConnect bool
}

// commandRun is a command between its write and its final result.
type commandRun struct {
	request
	// ownPrefix is the prefix of the command's own information lines, such
	// as "+CSQ"; "" when it has none.
	ownPrefix string
	// sentAt is when the command was written.
	sentAt time.Time
	// lines are the information lines received so far.
	lines []string
}

// command runs command within the handset's timeout, waiting for its final
// result.
func (h *Handset) command(command string) (Response, error) {
	return h.commandWithTimeout(command, h.commandTimeout())
}

// commandWithTimeout runs command within timeout, waiting for its final
// result.
func (h *Handset) commandWithTimeout(command string, timeout time.Duration) (Response, error) {
	return h.exec(request{command: command, timeout: timeout, completion: untilFinalResult})
}

// exec sends req's command and waits for what ends it. A CONNECT already
// received fails it unsent (see unawaitedConnect).
func (h *Handset) exec(req request) (Response, error) {
	if err := checkOneLine(req.command); err != nil {
		return Response{}, err
	}
	// Anything complete in the buffer now predates this command.
	if h.queueCompleteLines() {
		return Response{}, h.unawaitedConnect(req, "before "+debugtext.Quote(req.command))
	}
	if err := h.writeCommandLine(req.command); err != nil {
		return Response{}, err
	}
	run := &commandRun{request: req, ownPrefix: responsePrefix(req.command), sentAt: time.Now()}
	return h.awaitResponse(run)
}

// checkOneLine refuses a command that holds CR or LF: it would be more than
// one command line.
func checkOneLine(command string) error {
	if strings.ContainsAny(command, "\r\n") {
		return invalidArgument("command must not contain CR or LF")
	}
	return nil
}

// writeCommandLine writes command and the CR that ends it, once
// commandSettle has passed since the handset last sent something.
// Whatever it sends meanwhile stays for the next read.
func (h *Handset) writeCommandLine(command string) error {
	time.Sleep(time.Until(h.lastReceived.Add(commandSettle)))
	return h.write([]byte(command + commandEnd))
}

// awaitResponse reads run's answer until its final result, entering its text
// when the handset prompts for it, or until its time is up or it is
// cancelled.
func (h *Handset) awaitResponse(run *commandRun) (Response, error) {
	deadline := deadlineAfter(run.timeout)
	for {
		if result, ended := h.takeAnswerLines(run); ended {
			return h.finish(run, result)
		}
		if run.awaitsPrompt() && h.rx.takePrompt() {
			if err := h.enterText(run); err != nil {
				return Response{}, err
			}
			continue
		}
		wait, ok := timeToWait(deadline)
		if !ok {
			return Response{}, h.giveUp(run)
		}
		wait, err := run.cancellableWait(wait)
		if err != nil {
			return Response{}, err
		}
		if err := h.fill(wait); err != nil {
			return Response{}, err
		}
	}
}

// cancellableWait returns how long run may read before it looks at its
// cancellation again, at most wait, or ErrCancelled once it is cancelled
// and abortIgnoredFor has passed since its command. A run without a
// cancellation reads for all of wait.
func (run *commandRun) cancellableWait(wait time.Duration) (time.Duration, error) {
	if run.cancellation == nil {
		return wait, nil
	}
	// Not too soon: the abort that follows must not be ignored.
	if run.cancellation.Cancelled() && time.Since(run.sentAt) >= abortIgnoredFor {
		return 0, cancelledError()
	}
	return min(wait, cancellationCheck), nil
}

// takeAnswerLines takes the complete lines received so far as run's answer —
// skipping the echo, queueing URCs and keeping information lines — until a
// final result that ends run, which it reports.
func (h *Handset) takeAnswerLines(run *commandRun) (FinalResult, bool) {
	for {
		line, ok := h.rx.nextLine()
		if !ok {
			return FinalResult{}, false
		}
		result, isFinal := classifyFinalResult(line)
		// The echo, whatever the case of its ASCII letters: Unicode case
		// folding would take a line the handset never echoed for it. A
		// CONNECT never passes for the echo of a raw line written as one:
		// it may be a call's.
		if asciicase.EqualFold(line, run.command) && (!isFinal || result.Kind != FinalResultConnect) {
			continue
		}
		if isFinal {
			if run.completion.endsWith(result) {
				return result, true
			}
			continue
		}
		if isURC(line, run.ownPrefix) {
			h.urcs = append(h.urcs, line)
		} else {
			run.lines = append(run.lines, line)
		}
	}
}

// awaitsPrompt reports whether run still has text to enter at the handset's
// prompt.
func (run *commandRun) awaitsPrompt() bool {
	return len(run.textEntry) > 0
}

// enterText writes run's text, now that the handset prompts for it.
func (h *Handset) enterText(run *commandRun) error {
	text := run.textEntry
	run.textEntry = nil
	return h.write(text)
}

// giveUp reports that run got no final result in time. A handset still
// waiting for text is sent ESC first, to leave text-entry mode so that it
// accepts commands again.
func (h *Handset) giveUp(run *commandRun) error {
	if run.awaitsPrompt() {
		// Best effort: the timeout is what the caller needs to hear about,
		// and a handset that cannot be written to fails the next command.
		_ = h.write([]byte{esc})
	}
	return timeoutError(run.command)
}

// finish turns run's final result into its response or its error. A
// command that cannot take the handset online fails on a CONNECT, as its
// final result or received after it (see unawaitedConnect): a late one from
// a command given up on, say, must not pass for an answer, nor one that
// came with the answer be left unread.
func (h *Handset) finish(run *commandRun, result FinalResult) (Response, error) {
	if run.completion == untilFinalResult && !mayGoOnline(run.command) {
		if result.Kind == FinalResultConnect {
			return Response{}, h.unawaitedConnect(run.request, "during "+debugtext.Quote(run.command))
		}
		if h.queueCompleteLines() {
			return Response{}, h.unawaitedConnect(run.request, "after "+debugtext.Quote(run.command))
		}
	}
	return run.response(result)
}

// unawaitedConnect is the error of req, whose command did not await the
// CONNECT that arrived when says. That call is ended (see
// endCallThatConnected), unless req reports the CONNECT for its caller to
// end the call.
func (h *Handset) unawaitedConnect(req request, when string) error {
	if req.reportsConnect {
		return callError(wentOnline(when))
	}
	return h.endCallThatConnected(when)
}

// endCallThatConnected ends the data call of a CONNECT that no command of
// ours awaited, which arrived when says: a call the handset answered on its
// own, say, or one a command given up on set up. Nothing else would end it.
// It returns ErrCall once that is done, and ErrCallMayStillBeUp if it
// failed.
func (h *Handset) endCallThatConnected(when string) error {
	if err := h.endDataCall(DefaultEscapeGuard); err != nil {
		return callMayStillBeUpError(fmt.Sprintf("%s, and the call could not be ended: %v", wentOnline(when), err))
	}
	return callError(wentOnline(when) + ", and the call was ended")
}

// wentOnline says that a CONNECT no command awaited arrived when says.
func wentOnline(when string) string {
	return fmt.Sprintf("a CONNECT arrived %s: the handset went online", when)
}

// callTrouble returns err, a command's, if it is ErrCall or
// ErrCallMayStillBeUp, and nil otherwise. A method that can do without the
// command's answer still fails with these: a call's result came instead,
// or a data call came up meanwhile (see endCallThatConnected), and the
// caller must hear of it.
func callTrouble(err error) error {
	if errors.Is(err, ErrCall) || errors.Is(err, ErrCallMayStillBeUp) {
		return err
	}
	return nil
}

// response turns run's final result into the command's response, with the
// information lines received, or into its error.
func (run *commandRun) response(result FinalResult) (Response, error) {
	switch result.Kind {
	case FinalResultOK:
		return Response{Lines: run.lines}, nil
	case FinalResultConnect:
		return Response{Lines: run.lines, Connected: true, ConnectMessage: result.ConnectMessage}, nil
	case FinalResultError:
		return Response{}, &Error{Kind: ErrCommandFailed, Command: run.command}
	case FinalResultCME:
		return Response{}, &Error{Kind: ErrEquipment, Code: result.Code}
	case FinalResultCMS:
		return Response{}, &Error{Kind: ErrSMS, Code: result.Code}
	case FinalResultNoCarrier:
		return Response{}, noCarrierError()
	case FinalResultBusy:
		return Response{}, callError("BUSY")
	case FinalResultNoAnswer:
		return Response{}, callError("NO ANSWER")
	default:
		return Response{}, callError("NO DIALTONE")
	}
}

// awaitFinalResult waits up to timeout for the final result of something
// already under way, such as a call attempt being aborted, ending as until
// says, and reports whether one arrived. URCs are queued and other lines
// dropped meanwhile.
func (h *Handset) awaitFinalResult(timeout time.Duration, until completion) (FinalResult, bool, error) {
	deadline := deadlineAfter(timeout)
	for {
		if result, ok := h.takeFinalResult(until); ok {
			return result, true, nil
		}
		wait, ok := timeToWait(deadline)
		if !ok {
			return FinalResult{}, false, nil
		}
		if err := h.fill(wait); err != nil {
			return FinalResult{}, false, err
		}
	}
}

// takeFinalResult takes the complete lines received so far until a final
// result that ends the wait, as until says, and reports it, queueing URCs
// and dropping the rest.
func (h *Handset) takeFinalResult(until completion) (FinalResult, bool) {
	for {
		line, ok := h.rx.nextLine()
		if !ok {
			return FinalResult{}, false
		}
		result, isFinal := classifyFinalResult(line)
		switch {
		case isFinal && until.endsWith(result):
			return result, true
		case !isFinal && isURC(line, ""):
			h.urcs = append(h.urcs, line)
		}
	}
}

// queueCompleteLines moves every complete line received so far to the URC
// queue, up to a CONNECT, which it reports: a data call came up, and what
// follows is its data.
func (h *Handset) queueCompleteLines() (connected bool) {
	for line, ok := h.rx.nextLine(); ok; line, ok = h.rx.nextLine() {
		if result, isFinal := classifyFinalResult(line); isFinal && result.Kind == FinalResultConnect {
			return true
		}
		h.urcs = append(h.urcs, line)
	}
	return false
}

// takeURCs empties the URC queue and returns what it held, oldest first.
func (h *Handset) takeURCs() []string {
	urcs := h.urcs
	h.urcs = nil
	return urcs
}

// pollURCs waits up to timeout for unsolicited result codes and returns
// every queued one, as soon as there is one. A CONNECT fails it, once its
// call is ended (see endCallThatConnected); the queue stays for later.
func (h *Handset) pollURCs(timeout time.Duration) ([]string, error) {
	deadline := deadlineAfter(timeout)
	for {
		if h.queueCompleteLines() {
			return nil, h.endCallThatConnected("while waiting for unsolicited result codes")
		}
		wait, ok := timeToWait(deadline)
		if !ok || len(h.urcs) > 0 {
			return h.takeURCs(), nil
		}
		if err := h.fill(wait); err != nil {
			return nil, err
		}
	}
}

// fill reads what arrives within timeout into the line buffer.
func (h *Handset) fill(timeout time.Duration) error {
	var buf [readChunk]byte
	received, err := h.read(buf[:], timeout)
	if err != nil {
		return err
	}
	h.rx.push(buf[:received])
	return nil
}
