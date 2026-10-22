package sim

import (
	"fmt"
	"net/netip"
	"time"
)

// The handset's firmware: the AT command interpreter, SMS text entry, and a
// data call with its online-data and online-command modes.
//
// Time is passed in rather than read from the clock, so that every timer
// path can be tested without waiting.

// abortGrace is how soon after a dial command input is ignored rather than
// aborting the call: ITU-T V.250 §5.6.1 leaves the app time to follow the
// command line's CR with a LF.
const abortGrace = 125 * time.Millisecond

// modem is the simulated handset's state and behaviour.
type modem struct {
	config   Config
	settings settings
	input    inputMode
	call     callState
	inbox    inbox
	// toApp holds the bytes waiting for the app to read them.
	toApp []byte
	// unsolicited holds unsolicited results until the app can take them.
	unsolicited      []string
	lastRelease      callRelease
	messageReference uint8
	stats            Stats
}

// settings are what the app can change with commands; ATZ restores the
// defaults.
type settings struct {
	echo            bool
	bearer          bearer
	hangUpOnDTRDrop bool
}

// defaultSettings returns echo on, as handsets leave the factory. Closing
// the port leaves a call up until the app asks otherwise with AT&D2, so an
// app that forgets to finds out in tests rather than on its airtime bill.
func defaultSettings() settings {
	return settings{echo: true, bearer: defaultBearer, hangUpOnDTRDrop: false}
}

// inputKind is where the app's bytes go.
type inputKind int

const (
	// inputCommand is AT command lines: command mode, or online-command
	// mode during a call.
	inputCommand inputKind = iota
	// inputTextEntry is the text of an SMS, after the "> " prompt.
	inputTextEntry
	// inputOnlineData is the far end of the data call (online-data mode).
	inputOnlineData
)

// inputMode is where the app's bytes go, and the state that takes them.
type inputMode struct {
	kind   inputKind
	line   *lineAssembler  // inputCommand
	draft  *smsDraft       // inputTextEntry
	escape *escapeDetector // inputOnlineData
}

func commandInput() inputMode {
	return inputMode{kind: inputCommand, line: &lineAssembler{}}
}

// callStateKind is the stage of the handset's call.
type callStateKind int

const (
	// callIdle is no data call: voice calls, taken without audio, keep
	// no state.
	callIdle callStateKind = iota
	// callDialing is a data call the network has not connected yet.
	callDialing
	// callConnected is a data call the network has connected, in
	// online-data or online-command mode.
	callConnected
)

// callState is the handset's call.
type callState struct {
	kind callStateKind
	// connectsAt is when the network connects the call (callDialing).
	connectsAt time.Time
	// abortableFrom is when input from the app starts to abort the call
	// (callDialing).
	abortableFrom time.Time
	// circuit is the call's data path (callConnected).
	circuit *circuit
}

// callRelease is why the last data call ended, as AT+CEER reports it.
type callRelease int

const (
	releaseNoCallYet callRelease = iota
	releaseClearedByApp
	releaseDroppedByNetwork
	releaseSetupAborted
	releaseGatewayUnavailable
)

// String returns the reason as AT+CEER words it.
func (r callRelease) String() string {
	switch r {
	case releaseNoCallYet:
		return "No report available"
	case releaseClearedByApp:
		return "Normal call clearing"
	case releaseDroppedByNetwork:
		return "Network out of order"
	case releaseSetupAborted:
		return "Call setup aborted"
	case releaseGatewayUnavailable:
		return "Service or option not available"
	default:
		return fmt.Sprintf("callRelease(%d)", int(r))
	}
}

// newModem returns a handset in command mode, with factory settings and an
// empty inbox.
func newModem(config Config) *modem {
	return &modem{
		config:      config,
		settings:    defaultSettings(),
		input:       commandInput(),
		call:        callState{kind: callIdle},
		lastRelease: releaseNoCallYet,
	}
}

// receive takes the bytes the app wrote at now.
func (m *modem) receive(input []byte, now time.Time) {
	m.advance(now)
	for len(input) > 0 {
		input = m.consume(input, now)
	}
}

// advance moves the simulation to now: call setup, the escape guard time
// and the data call's traffic.
func (m *modem) advance(now time.Time) {
	m.completeCallSetup(now)
	m.watchEscapeGuard(now)
	m.runCircuit(now)
}

// readInto copies waiting output into p, and returns how many bytes it
// copied.
func (m *modem) readInto(p []byte) int {
	count := copy(p, m.toApp)
	m.toApp = m.toApp[count:]
	m.reportUnsolicited()
	return count
}

// restartPPP makes the gateway start PPP over on the call in progress,
// assigning clientAddress; without a call there is nothing to restart.
func (m *modem) restartPPP(clientAddress netip.Addr, now time.Time) error {
	if m.call.kind != callConnected {
		return nil
	}
	return m.call.circuit.restartPPP(clientAddress, now)
}

// dropCall ends the call, or the attempt to set one up, from the network
// side.
func (m *modem) dropCall() {
	if m.call.kind == callIdle {
		return
	}
	logInfo("simulated handset: the network dropped the call")
	m.releaseCall(releaseDroppedByNetwork)
	m.reply(reply{kind: replyNoCarrier})
}

// dropDTR is DTR dropping: the app lowered it, or closed the port.
func (m *modem) dropDTR() {
	if m.settings.hangUpOnDTRDrop && m.call.kind != callIdle {
		logInfo("simulated handset: DTR dropped, ending the call")
		m.releaseCall(releaseClearedByApp)
	}
}

// receiveSMS stores a message that arrived at now and announces it with
// +CMTI.
func (m *modem) receiveSMS(from, text string, now time.Time) {
	index := m.inbox.store(from, text, timestampAt(now))
	m.unsolicited = append(m.unsolicited, fmt.Sprintf(`+CMTI: "SM",%d`, index))
	m.reportUnsolicited()
}

// shutDown ends what the modem keeps running in the background, for a
// modem nobody can reach any more.
func (m *modem) shutDown() {
	m.closeCircuit()
}

// consume hands input to whatever takes the app's bytes right now, one
// byte at a time while each may change the mode, and returns what is left.
func (m *modem) consume(input []byte, now time.Time) []byte {
	b, rest := input[0], input[1:]
	if m.call.kind == callDialing {
		// Real handsets abandon call setup when the app sends anything
		// after the grace time.
		if !now.Before(m.call.abortableFrom) {
			m.abortCallSetup()
		}
		return rest
	}
	switch m.input.kind {
	case inputCommand:
		m.receiveCommandByte(b, now)
	case inputTextEntry:
		m.receiveText(b)
	case inputOnlineData:
		// Nothing within one write can end online-data mode: that takes
		// silence.
		m.sendOnlineData(input, now)
		return nil
	}
	return rest
}

func (m *modem) receiveCommandByte(b byte, now time.Time) {
	m.echo(b)
	finished, ok := m.input.line.push(b)
	if !ok {
		return
	}
	if finished.tooLong {
		m.reply(reply{kind: replyError})
		return
	}
	m.runCommandLine(finished.text, now)
}

func (m *modem) runCommandLine(text string, now time.Time) {
	// Lines without the AT prefix are not for the handset.
	body, ok := stripAttention(text)
	if !ok {
		return
	}
	parsed, ok := parseCommand(body)
	if !ok {
		m.reply(reply{kind: replyError})
		return
	}
	m.execute(parsed, now)
}

func (m *modem) receiveText(b byte) {
	typed := m.input.draft.typeByte(b)
	switch typed.kind {
	case entryContinues:
		m.echo(b)
	case entrySubmitted:
		m.input = commandInput()
		m.submitSMS(typed.message)
	case entryCancelled:
		m.input = commandInput()
		m.reply(okReply())
	case entryTooLong:
		m.input = commandInput()
		m.reply(smsErrorReply(smsErrorInvalidTextModeParameter))
	}
}

func (m *modem) sendOnlineData(input []byte, now time.Time) {
	m.forwardToNetwork(m.input.escape.filter(input, now), now)
}

func (m *modem) forwardToNetwork(data []byte, now time.Time) {
	if m.call.kind != callConnected {
		return
	}
	m.stats.DataBytesFromApp += uint64(len(data))
	m.call.circuit.send(data, now)
}

// startDataCall dials number for a data call. The call connects when the
// simulation next advances, even without setup time: by then the input
// that came with the dial command, such as the LF after its CR, has been
// ignored.
func (m *modem) startDataCall(number string, now time.Time) {
	logInfo(fmt.Sprintf("simulated handset: dialling %s for a data call", number))
	m.call = callState{
		kind:          callDialing,
		connectsAt:    later(now, m.config.Link.CallSetup),
		abortableFrom: later(now, abortGrace),
	}
}

func (m *modem) completeCallSetup(now time.Time) {
	if m.call.kind != callDialing || now.Before(m.call.connectsAt) {
		return
	}
	connected, err := connectCircuit(m.config, now)
	if err != nil {
		logWarn(fmt.Sprintf("simulated handset: the PPP gateway cannot start: %v", err))
		m.releaseCall(releaseGatewayUnavailable)
		m.reply(reply{kind: replyNoCarrier})
		return
	}
	m.call = callState{kind: callConnected, circuit: connected}
	m.stats.CallsConnected++
	m.stats.InDataCall = true
	m.goOnline(now)
}

func (m *modem) abortCallSetup() {
	m.releaseCall(releaseSetupAborted)
	m.reply(reply{kind: replyNoCarrier})
}

// goOnline enters online-data mode, with the escape guard time counted
// from now.
func (m *modem) goOnline(now time.Time) {
	m.reply(reply{kind: replyConnect})
	m.input = inputMode{kind: inputOnlineData, escape: newEscapeDetector(m.config.EscapeGuard, now)}
}

func (m *modem) watchEscapeGuard(now time.Time) {
	if m.input.kind != inputOnlineData {
		return
	}
	expiry := m.input.escape.expire(now)
	switch expiry.kind {
	case guardReleasedData:
		m.forwardToNetwork(expiry.data, now)
	case guardEscape:
		// Online-command mode: the call stays up while commands run.
		m.input = commandInput()
		m.reply(okReply())
	}
}

func (m *modem) runCircuit(now time.Time) {
	if m.call.kind != callConnected {
		return
	}
	c := m.call.circuit
	c.run(now)
	m.stats.DiscardedBytes += c.takeDiscardedBytes()
	// In online-command mode, data from the far end waits for ATO.
	if m.input.kind == inputOnlineData {
		data := c.receive(now)
		m.stats.DataBytesToApp += uint64(len(data))
		m.toApp = append(m.toApp, data...)
	}
}

// releaseCall ends any call and returns to command mode.
func (m *modem) releaseCall(reason callRelease) {
	m.closeCircuit()
	m.call = callState{kind: callIdle}
	m.input = commandInput()
	m.lastRelease = reason
	m.stats.InDataCall = false
}

// closeCircuit ends what a connected call keeps running in the
// background, which would otherwise outlive the call.
func (m *modem) closeCircuit() {
	if m.call.kind == callConnected {
		m.call.circuit.close()
	}
}

// reportUnsolicited reports held unsolicited results once the handset is
// idle and the app has read everything before them, so that a client
// which drops its input after a final result (as a data call's hang-up
// does) still sees them.
func (m *modem) reportUnsolicited() {
	if !m.isIdle() || len(m.toApp) > 0 {
		return
	}
	for _, text := range m.unsolicited {
		m.toApp = append(m.toApp, unsolicited(text)...)
	}
	m.unsolicited = nil
}

// isIdle reports whether the handset is in command mode without a call,
// the only time unsolicited results cannot get in the way.
func (m *modem) isIdle() bool {
	return m.call.kind == callIdle && m.input.kind == inputCommand
}

func (m *modem) echo(b byte) {
	if m.settings.echo {
		m.toApp = append(m.toApp, b)
	}
}

func (m *modem) reply(r reply) {
	m.toApp = append(m.toApp, r.encode()...)
}
