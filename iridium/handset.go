package iridium

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anhydrous99/satlink/internal/debugtext"
)

// Response is what a successful command returned.
type Response struct {
	// Lines are the command's information lines, without the echo and
	// without unsolicited result codes, which are queued instead.
	Lines []string
	// Connected reports that the command ended in CONNECT rather than OK:
	// the handset is online, a data call is up.
	Connected bool
	// ConnectMessage is what followed CONNECT, usually the data rate; empty
	// when nothing did, or when the command ended in OK.
	ConnectMessage string
}

// DeviceInfo identifies the handset.
type DeviceInfo struct {
	// Manufacturer as reported by AT+CGMI, such as "Iridium".
	Manufacturer string
	// Model as reported by AT+CGMM, such as "9575".
	Model string
	// Revision is the firmware revision reported by AT+CGMR.
	Revision string
	// IMEI as reported by AT+CGSN.
	IMEI string
}

// ChargeStatus is the battery status field of +CBC. Its value is the code
// the handset reported; codes without a constant are unknown statuses.
type ChargeStatus int

// The charge statuses +CBC defines.
const (
	// ChargeStatusOnBattery means the handset runs on its battery.
	ChargeStatusOnBattery ChargeStatus = 0
	// ChargeStatusExternalPower means the handset runs on external power.
	ChargeStatusExternalPower ChargeStatus = 1
	// ChargeStatusNoBattery means no battery is connected.
	ChargeStatusNoBattery ChargeStatus = 2
	// ChargeStatusPowerFault means a power fault: calls are inhibited.
	ChargeStatusPowerFault ChargeStatus = 3
)

// String returns the status's name, such as "OnBattery", or "Unknown(7)"
// for a code without a constant.
func (s ChargeStatus) String() string {
	switch s {
	case ChargeStatusOnBattery:
		return "OnBattery"
	case ChargeStatusExternalPower:
		return "ExternalPower"
	case ChargeStatusNoBattery:
		return "NoBattery"
	case ChargeStatusPowerFault:
		return "PowerFault"
	default:
		return fmt.Sprintf("Unknown(%d)", int(s))
	}
}

// ChargerStatus is the charger connection status field of +CBC, which
// 9555(A) and 9575 handsets report. Its value is the code the handset
// reported; codes without a constant are unknown statuses.
type ChargerStatus int

// The charger statuses +CBC defines (ISU AT Command Reference, +CBC).
const (
	// ChargerStatusUnreported means the handset reported no charger status,
	// as only 9555(A) and 9575 handsets do.
	ChargerStatusUnreported ChargerStatus = 0
	// ChargerStatusCharging means a charger is connected and charging.
	ChargerStatusCharging ChargerStatus = 1
	// ChargerStatusNotCharging means a charger is connected, not charging.
	ChargerStatusNotCharging ChargerStatus = 2
	// ChargerStatusNotConnected means no charger is connected.
	ChargerStatusNotConnected ChargerStatus = 3
)

// String returns the status's name, such as "Charging", or "Unknown(7)"
// for a code without a constant.
func (s ChargerStatus) String() string {
	switch s {
	case ChargerStatusUnreported:
		return "Unreported"
	case ChargerStatusCharging:
		return "Charging"
	case ChargerStatusNotCharging:
		return "NotCharging"
	case ChargerStatusNotConnected:
		return "NotConnected"
	default:
		return fmt.Sprintf("Unknown(%d)", int(s))
	}
}

// Battery is what +CBC reports: +CBC: <bcs>,<bcl>[,<ccs>,<bal>].
type Battery struct {
	// Status is the battery connection status.
	Status ChargeStatus
	// Bars is the charge as the handset's battery indicator shows it: 0 to
	// 4 on 9555(A) and 9575 handsets, 0 to 3 on the others.
	Bars int
	// Charger is the charger's status: ChargerStatusUnreported but on
	// 9555(A) and 9575 handsets.
	Charger ChargerStatus
	// Level is the charge in percent, 0 to 100, from the battery's
	// accumulator. Only 9555(A) and 9575 handsets report it, as
	// LevelReported says: on the others it is zero.
	Level int
	// LevelReported reports that the handset reported Level.
	LevelReported bool
}

// RegistrationStatus is the network registration status of +CREG. The
// constants' values are the 3GPP TS 27.007 codes. [Handset.Registration]
// maps codes without a constant of their own to RegistrationStatusUnknown,
// so Registration.Status is always one of these six constants.
type RegistrationStatus int

// The registration statuses.
const (
	// RegistrationStatusNotRegistered means not registered and not
	// searching.
	RegistrationStatusNotRegistered RegistrationStatus = iota
	// RegistrationStatusHome means registered on the home network.
	RegistrationStatusHome
	// RegistrationStatusSearching means not registered, searching for a
	// network.
	RegistrationStatusSearching
	// RegistrationStatusDenied means registration was denied.
	RegistrationStatusDenied
	// RegistrationStatusUnknown means the status is unknown, which is also
	// what every code without a constant of its own becomes.
	RegistrationStatusUnknown
	// RegistrationStatusRoaming means registered on a visited network.
	RegistrationStatusRoaming
)

// String returns the status's name, such as "Home".
func (s RegistrationStatus) String() string {
	switch s {
	case RegistrationStatusNotRegistered:
		return "NotRegistered"
	case RegistrationStatusHome:
		return "Home"
	case RegistrationStatusSearching:
		return "Searching"
	case RegistrationStatusDenied:
		return "Denied"
	case RegistrationStatusUnknown:
		return "Unknown"
	case RegistrationStatusRoaming:
		return "Roaming"
	default:
		return fmt.Sprintf("RegistrationStatus(%d)", int(s))
	}
}

// Registration is what +CREG reports.
type Registration struct {
	// Status is the registration status.
	Status RegistrationStatus
	// LocationArea is the location area code; empty when not reported.
	LocationArea string
	// CellID is the cell identity; empty when not reported.
	CellID string
}

// SMSFilter selects which stored messages [Handset.ListSMS] lists. A value
// without a constant of its own is refused.
type SMSFilter int

// The message filters of text-mode +CMGL.
const (
	// SMSFilterAll lists every message ("ALL").
	SMSFilterAll SMSFilter = iota
	// SMSFilterUnread lists received messages not read yet ("REC UNREAD").
	SMSFilterUnread
	// SMSFilterRead lists received messages already read ("REC READ").
	SMSFilterRead
	// SMSFilterUnsent lists stored messages not sent yet ("STO UNSENT").
	SMSFilterUnsent
	// SMSFilterSent lists stored messages already sent ("STO SENT").
	SMSFilterSent
)

// String returns the filter's name, such as "Unread".
func (f SMSFilter) String() string {
	switch f {
	case SMSFilterAll:
		return "All"
	case SMSFilterUnread:
		return "Unread"
	case SMSFilterRead:
		return "Read"
	case SMSFilterUnsent:
		return "Unsent"
	case SMSFilterSent:
		return "Sent"
	default:
		return fmt.Sprintf("SMSFilter(%d)", int(f))
	}
}

// SMSMessage is a message stored on the handset or its SIM.
type SMSMessage struct {
	// Index is the message's storage index, for ReadSMS and DeleteSMS.
	Index int
	// Status is the storage status as the handset words it, such as
	// "REC UNREAD".
	Status string
	// Address is the sender (received messages) or recipient (stored
	// messages) number.
	Address string
	// Timestamp is the service-centre timestamp as the handset sent it
	// (yy/MM/dd,hh:mm:ss±zz); empty when there is none.
	Timestamp string
	// Text is the message text, its lines joined by "\n".
	Text string
}

// How long commands wait for their final result.
const (
	// defaultTimeout is the ordinary commands' timeout until SetTimeout.
	defaultTimeout = 5 * time.Second
	// dialTimeout bounds voice dialling and answering: satellite call setup
	// is slow.
	dialTimeout = 90 * time.Second
	// signalQualityTimeout bounds AT+CSQ, which waits for a new reading:
	// usually within two seconds, within ten while the handset acquires the
	// system or hands over between satellites, and over fifty, ending in
	// ERROR, while it is not registered (ISU AT Command Reference, +CSQ). A
	// shorter wait would leave its late answer to pass for the next
	// command's: a 9555A asked again within 4.5 s holds the second AT+CSQ,
	// and drops every other command, until then.
	signalQualityTimeout = 60 * time.Second
	// maxHangUpTimeout is the longest wait for ATH's answer, whatever the
	// handset's timeout: what follows when none comes — dropping DTR, or
	// reporting that the call may still be up — must not be put off for
	// good.
	maxHangUpTimeout = 10 * time.Second
)

// The commands the handset's own methods build on.
const (
	// attentionCommand does nothing but get the handset's attention.
	attentionCommand = "AT"
	// dialCommand dials the number that follows it.
	dialCommand = "ATD"
	// voiceCallSuffix ends a dial command that places a voice call.
	voiceCallSuffix = ";"
	// answerCommand answers an incoming call.
	answerCommand = "ATA"
	// returnOnlineCommand takes the handset back online into a call left
	// for command mode.
	returnOnlineCommand = "ATO"
	// hangUpCommand ends a call.
	hangUpCommand = "ATH"
)

// Handset is a connected Iridium handset.
//
// All methods block (see the package documentation for the timeouts).
// Unsolicited result codes (incoming SMS, RING, …) seen while waiting for
// responses are queued; fetch them with [Handset.TakeURCs] or wait for new
// ones with [Handset.PollURCs].
//
// A Handset is safe for use by several goroutines; each method holds the
// handset for its whole exchange. While a [DataCall] holds the handset, the
// methods that talk to it fail at once with an [*Error] of kind [ErrBusy],
// TakeURCs returns nothing, and SetTimeout, Transport and Close keep
// working.
//
// The zero Handset is not usable; create one with [NewHandset].
type Handset struct {
	transport Transport

	// claimed is set while a data call holds the handset, from the start of
	// its dial until it is hung up: the call then uses the transport
	// without the lock, and the handset's own methods fail with ErrBusy.
	claimed atomic.Bool
	// timeout is the ordinary commands' timeout, a time.Duration. It is
	// atomic so that SetTimeout never waits for a dial holding the lock.
	timeout atomic.Int64
	// closeOnce closes the transport once, without the lock, so that
	// closing can end an exchange in progress.
	closeOnce sync.Once

	// mu serialises exchanges with the handset: it guards the fields below
	// and the transport's reads and writes, except while a data call holds
	// the handset.
	mu sync.Mutex
	// rx holds what the handset sent that is not consumed yet.
	rx lineBuffer
	// urcs are the unsolicited result codes received, oldest first.
	urcs []string
	// lastReceived is when the handset last sent something: the next
	// command line waits commandSettle from then. A data call sets it too,
	// as it reads through the handset.
	lastReceived time.Time
}

// NewHandset returns a handset that talks through transport. Nothing is
// sent until the first command; call [Handset.Initialize] to put the
// handset into a known state. NewHandset panics if transport is nil.
func NewHandset(transport Transport) *Handset {
	if transport == nil {
		panic("iridium: NewHandset needs a transport")
	}
	h := &Handset{transport: transport}
	h.timeout.Store(int64(defaultTimeout))
	return h
}

// SetTimeout sets how long ordinary commands wait for their final result;
// the default is five seconds. Commands with their own timeout (signal
// readings, SMS submission, dialling, [Handset.CommandTimeout]) are not
// affected. A
// timeout of zero or less waits for nothing: every command it bounds is
// sent and fails with ErrTimeout at once, even when its answer has
// arrived. The longest, math.MaxInt64, means no deadline.
//
// It also bounds, to at most ten seconds, the wait for the handset's
// answer to ATH — [Handset.HangUp]'s, and the one that ends a data call —
// and to the commands that prepare a dial, which a handset left in a call
// never answers: what follows when no answer comes must not be put off for
// good.
func (h *Handset) SetTimeout(timeout time.Duration) {
	h.timeout.Store(int64(timeout))
}

// commandTimeout is the ordinary commands' timeout.
func (h *Handset) commandTimeout() time.Duration {
	return time.Duration(h.timeout.Load())
}

// hangUpTimeout is the command timeout, but never so long that ending a
// call is put off for good: what follows an unanswered ATH, or an
// unanswered command before a dial, which shows a call left up.
func (h *Handset) hangUpTimeout() time.Duration {
	return min(h.commandTimeout(), maxHangUpTimeout)
}

// Initialize synchronises with the handset and puts it into a known state:
// echo off (ATE0), verbose result codes (ATV1), verbose +CME ERROR
// (AT+CMEE=2) and SMS text mode (AT+CMGF=1). The last two are optional on
// some firmware and their failure is ignored.
//
// Any half-entered SMS a previous session left is aborted (ESC) and stale
// input discarded first, for at most two seconds: a handset left in a call
// may relay data without pause.
//
// A data call a previous session left up — an app that crashed, say — is
// ended first. A handset still relaying it does not answer: only the
// handset's own result code counts as its answer to AT, as it ends in a
// bare CR, which a call's data never holds, so the far end cannot pass for
// the handset with an OK line of its own. That call is ended as
// [Handset.EndDataCall] does, with [DefaultEscapeGuard], before trying once
// more. A handset that answers may still hold the call in online-command
// mode — escaped from but never hung up — which AT+CPAS reveals: it is hung
// up with ATH. Firmware without +CPAS cannot tell, and is left as it is.
//
// The error is of kind [ErrCallMayStillBeUp] if either failed: a handset
// that still does not answer once ending a call failed may be relaying
// one, and one whose ATH failed still holds it. A handset that does not
// answer otherwise fails with ErrTimeout for command "AT".
func (h *Handset) Initialize() error {
	return h.perform(h.initialize)
}

// Command sends a raw AT command, without its trailing CR, and waits for
// its final result within the handset's timeout.
//
// It fails with ErrInvalidArgument if command contains CR or LF, ErrTimeout
// when no final result arrives in time, ErrCommandFailed on ERROR,
// ErrEquipment on +CME ERROR, ErrSMS on +CMS ERROR, and ErrCall on NO
// CARRIER, BUSY, NO ANSWER and NO DIALTONE. A CONNECT ends only commands
// that can take the handset online (a data dial, ATA, ATO; the Response then
// says Connected); after any other command it fails with ErrCall, as a late
// CONNECT from an abandoned dial must not pass for an answer.
func (h *Handset) Command(command string) (Response, error) {
	return exchange(h, func() (Response, error) { return h.command(command) })
}

// CommandTimeout is [Handset.Command] with its own timeout instead of the
// handset's.
func (h *Handset) CommandTimeout(command string, timeout time.Duration) (Response, error) {
	return exchange(h, func() (Response, error) { return h.commandWithTimeout(command, timeout) })
}

// Info reports manufacturer, model, firmware revision and IMEI (AT+CGMI,
// AT+CGMM, AT+CGMR, AT+CGSN).
func (h *Handset) Info() (DeviceInfo, error) {
	return exchange(h, h.info)
}

// IMSI reports the subscriber identity (AT+CIMI), which requires a SIM.
func (h *Handset) IMSI() (string, error) {
	return exchange(h, func() (string, error) { return h.queryText("AT+CIMI") })
}

// SignalQuality reports the signal strength in bars, 0 to 5 on Iridium
// handsets, from a new reading (AT+CSQ). The handset answers once it has
// one, usually within two seconds, so SignalQuality waits up to a minute,
// whatever SetTimeout says. One that is not registered may take fifty
// seconds and then fail with ErrCommandFailed: check [Handset.Registration]
// first, or use [Handset.LastSignalQuality].
func (h *Handset) SignalQuality() (int, error) {
	return exchange(h, h.signalQuality)
}

// LastSignalQuality reports the last signal strength the handset measured,
// in bars, 0 to 5, at once (AT+CSQF): what its signal indicator shows.
func (h *Handset) LastSignalQuality() (int, error) {
	return exchange(h, h.lastSignalQuality)
}

// Battery reports the battery's status and charge (AT+CBC): the bars of
// the handset's indicator on every handset, and the charger and the charge
// in percent on 9555(A) and 9575 handsets.
func (h *Handset) Battery() (Battery, error) {
	return exchange(h, h.battery)
}

// Registration reports the network registration (AT+CREG?).
func (h *Handset) Registration() (Registration, error) {
	return exchange(h, h.registration)
}

// Clock reports the handset's clock as +CCLK words it,
// yy/MM/dd,hh:mm:ss±zz, without quotes.
func (h *Handset) Clock() (string, error) {
	return exchange(h, h.clock)
}

// SendSMS sends a text message (AT+CMGS, in text mode) and returns the
// message reference the network assigned. Submission goes over the
// satellite link and may take up to two minutes.
//
// number must be dialable (digits, '*' and '#' after an optional '+') and
// text must not contain Ctrl-Z or ESC, which end or abort text entry;
// otherwise it fails with ErrInvalidArgument before anything is sent. When
// the handset's prompt for the text does not come in time, it is sent ESC
// to leave text entry, so that it accepts commands again. Once the text has
// been entered, a timeout leaves the handset submitting the message, and
// its late answer may still follow.
func (h *Handset) SendSMS(number, text string) (int, error) {
	return exchange(h, func() (int, error) { return h.sendSMS(number, text) })
}

// ListSMS lists the stored messages filter selects (AT+CMGL). Listing marks
// unread messages read, as 3GPP TS 27.005 specifies. A filter without a
// constant of its own fails with ErrInvalidArgument, "%d is not an SMS
// filter", before anything is sent.
func (h *Handset) ListSMS(filter SMSFilter) ([]SMSMessage, error) {
	return exchange(h, func() ([]SMSMessage, error) { return h.listSMS(filter) })
}

// ReadSMS reads the message stored at index (AT+CMGR), which marks it read.
// A negative index fails with ErrInvalidArgument.
func (h *Handset) ReadSMS(index int) (SMSMessage, error) {
	return exchange(h, func() (SMSMessage, error) { return h.readSMS(index) })
}

// DeleteSMS deletes the message stored at index (AT+CMGD). A negative index
// fails with ErrInvalidArgument.
func (h *Handset) DeleteSMS(index int) error {
	return h.perform(func() error { return h.deleteSMS(index) })
}

// Dial starts a voice call to number (ATD<number>;), waiting up to ninety
// seconds. number must be dialable, as for [Handset.SendSMS].
func (h *Handset) Dial(number string) error {
	return h.perform(func() error { return h.dial(number) })
}

// Answer answers an incoming voice call (ATA), waiting up to ninety
// seconds. Late answers to commands that timed out are dropped first, as
// one would pass for ATA's.
//
// A data call answered by mistake is ended again at once, as nothing else
// would end it, and Answer fails with ErrCall "ATA answered a data call,
// which was ended again" — failing that, with [ErrCallMayStillBeUp], "the
// call may still be up: ATA answered a data call that could not be ended:
// …". So is one that may come up after ATA's answer was lost, as when ATA
// times out or the transport fails while it waits: the attempt is aborted
// first, and the call ended unless the handset shows a voice call up
// (AT+CLCC, AT+CPAS); Answer then fails with ATA's error, or with
// ErrCallMayStillBeUp. Answer data calls with [Handset.AnswerData].
func (h *Handset) Answer() error {
	return h.perform(h.answer)
}

// HangUp ends the call the handset is in with ATH — a data call only from
// command mode, which [Handset.EndDataCall] reaches first; a data call this
// process placed is ended with [DataCall.HangUp]. OK when no call is up,
// and NO CARRIER when it had just ended, both mean that none is left. ATH
// waits for the handset's timeout, but at most ten seconds.
func (h *Handset) HangUp() error {
	return h.perform(h.hangUp)
}

// TakeURCs returns the unsolicited result codes received so far, oldest
// first, and empties the queue. It never talks to the handset, but waits
// for an exchange in progress in another goroutine, such as a SendSMS,
// which may take minutes. While a data call holds the handset it returns
// nothing and leaves the queue for later.
func (h *Handset) TakeURCs() []string {
	urcs, err := exchange(h, func() ([]string, error) { return h.takeURCs(), nil })
	if err != nil {
		// A data call holds the handset: nothing now, the queue stays.
		return nil
	}
	return urcs
}

// PollURCs waits up to timeout for unsolicited result codes and returns all
// queued ones, possibly none. It returns as soon as there is one.
func (h *Handset) PollURCs(timeout time.Duration) ([]string, error) {
	return exchange(h, func() ([]string, error) { return h.pollURCs(timeout) })
}

// Transport returns the transport the handset talks through.
func (h *Handset) Transport() Transport {
	return h.transport
}

// Close closes the transport if it implements io.Closer; for a serial port
// that also drops DTR, which with AT&D2 ends any call. Close does not wait
// for a command or data call in progress — closing the port is the last
// resort for ending a call — and they then fail with the closed transport's
// error. The transport is closed once; later calls return nil.
func (h *Handset) Close() error {
	var err error
	h.closeOnce.Do(func() {
		if closer, ok := h.transport.(io.Closer); ok {
			err = wrapTransportError(closer.Close())
		}
	})
	return err
}

// info is Info, with the handset's lock held.
func (h *Handset) info() (DeviceInfo, error) {
	var info DeviceInfo
	queries := []struct {
		command string
		answer  *string
	}{
		{"AT+CGMI", &info.Manufacturer},
		{"AT+CGMM", &info.Model},
		{"AT+CGMR", &info.Revision},
		{"AT+CGSN", &info.IMEI},
	}
	for _, query := range queries {
		text, err := h.queryText(query.command)
		if err != nil {
			return DeviceInfo{}, err
		}
		*query.answer = text
	}
	return info, nil
}

// signalQuality is SignalQuality, with the handset's lock held.
func (h *Handset) signalQuality() (int, error) {
	response, err := h.commandWithTimeout("AT+CSQ", signalQualityTimeout)
	if err != nil {
		return 0, err
	}
	return parseSignalBars(response, "+CSQ")
}

// lastSignalQuality is LastSignalQuality, with the handset's lock held.
func (h *Handset) lastSignalQuality() (int, error) {
	response, err := h.command("AT+CSQF")
	if err != nil {
		return 0, err
	}
	return parseSignalBars(response, "+CSQF")
}

// parseSignalBars returns the bars that follow "<prefix>:" in response.
func parseSignalBars(response Response, prefix string) (int, error) {
	fields, err := fieldsWithPrefix(response, prefix)
	if err != nil {
		return 0, err
	}
	return parseNumber(fields, 0, prefix, byteField)
}

// battery is Battery, with the handset's lock held.
func (h *Handset) battery() (Battery, error) {
	fields, err := h.queryFields("AT+CBC", "+CBC")
	if err != nil {
		return Battery{}, err
	}
	return parseBattery(fields)
}

// batteryFieldsWithLevel is how many fields +CBC has when it reports the
// charger and the charge in percent: <bcs>,<bcl>,<ccs>,<bal>.
const batteryFieldsWithLevel = 4

// parseBattery parses the fields of +CBC: <bcs>,<bcl>[,<ccs>,<bal>]. The
// charger and the level are read only when both are there.
func parseBattery(fields []string) (Battery, error) {
	status, err := parseNumber(fields, 0, "+CBC", byteField)
	if err != nil {
		return Battery{}, err
	}
	bars, err := parseNumber(fields, 1, "+CBC", byteField)
	if err != nil {
		return Battery{}, err
	}
	battery := Battery{Status: ChargeStatus(status), Bars: bars}
	if len(fields) < batteryFieldsWithLevel {
		return battery, nil
	}
	charger, err := parseNumber(fields, 2, "+CBC", byteField)
	if err != nil {
		return Battery{}, err
	}
	level, err := parseNumber(fields, 3, "+CBC", byteField)
	if err != nil {
		return Battery{}, err
	}
	battery.Charger, battery.Level, battery.LevelReported = ChargerStatus(charger), level, true
	return battery, nil
}

// registration is Registration, with the handset's lock held.
func (h *Handset) registration() (Registration, error) {
	// +CREG: <n>,<stat>[,<lac>,<ci>]
	fields, err := h.queryFields("AT+CREG?", "+CREG")
	if err != nil {
		return Registration{}, err
	}
	stat, err := parseNumber(fields, 1, "+CREG", byteField)
	if err != nil {
		return Registration{}, err
	}
	return Registration{
		Status:       registrationStatus(stat),
		LocationArea: fieldAt(fields, 2),
		CellID:       fieldAt(fields, 3),
	}, nil
}

// registrationStatus returns the status a +CREG <stat> code stands for:
// RegistrationStatusUnknown for codes without a constant of their own.
func registrationStatus(code int) RegistrationStatus {
	status := RegistrationStatus(code)
	if status < RegistrationStatusNotRegistered || status > RegistrationStatusRoaming {
		return RegistrationStatusUnknown
	}
	return status
}

// clock is Clock, with the handset's lock held.
func (h *Handset) clock() (string, error) {
	line, err := h.queryLine("AT+CCLK?", "+CCLK")
	if err != nil {
		return "", err
	}
	return strings.Trim(line, `"`), nil
}

// queryText returns the first information line of command's answer, with
// the command's own "+XXX:" prefix stripped if it carries one.
func (h *Handset) queryText(command string) (string, error) {
	response, err := h.command(command)
	if err != nil {
		return "", err
	}
	if len(response.Lines) == 0 {
		return "", parseError(fmt.Sprintf("%s returned no data", command))
	}
	line := response.Lines[0]
	if prefix := responsePrefix(command); prefix != "" {
		if text, ok := infoText(line, prefix); ok {
			line = text
		}
	}
	return strings.TrimSpace(line), nil
}

// queryLine returns what follows "<prefix>:" in command's answer.
func (h *Handset) queryLine(command, prefix string) (string, error) {
	response, err := h.command(command)
	if err != nil {
		return "", err
	}
	return lineWithPrefix(response, prefix)
}

// queryFields returns the fields that follow "<prefix>:" in command's
// answer.
func (h *Handset) queryFields(command, prefix string) ([]string, error) {
	response, err := h.command(command)
	if err != nil {
		return nil, err
	}
	return fieldsWithPrefix(response, prefix)
}

// lineWithPrefix returns what follows "<prefix>:" in the first of
// response's lines that starts so, trimmed.
func lineWithPrefix(response Response, prefix string) (string, error) {
	for _, line := range response.Lines {
		if text, ok := infoText(line, prefix); ok {
			return strings.TrimSpace(text), nil
		}
	}
	return "", parseError(fmt.Sprintf("no %s line in %s", prefix, debugList(response.Lines)))
}

// fieldsWithPrefix returns the fields that follow "<prefix>:" in response.
func fieldsWithPrefix(response Response, prefix string) ([]string, error) {
	line, err := lineWithPrefix(response, prefix)
	if err != nil {
		return nil, err
	}
	return SplitCSV(line), nil
}

// infoText returns what follows "<prefix>:" in line, and whether line
// starts so.
func infoText(line, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return "", false
	}
	return strings.CutPrefix(rest, ":")
}

// validateNumber refuses a number that is not dialable: digits, '*' and
// '#' after an optional '+'. Anything else could smuggle in another command.
func validateNumber(number string) error {
	digits := strings.TrimPrefix(number, "+")
	if digits == "" || strings.IndexFunc(digits, isNotDialable) >= 0 {
		return invalidArgument(debugtext.Quote(number) + " is not a dialable number")
	}
	return nil
}

// isNotDialable reports whether c cannot be part of a dialled number.
func isNotDialable(c rune) bool {
	return (c < '0' || c > '9') && c != '*' && c != '#'
}
