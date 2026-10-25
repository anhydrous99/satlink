package mobile

import (
	"errors"
	"sync"

	"github.com/anhydrous99/satlink/iridium"
	"github.com/anhydrous99/satlink/satnet"
)

// The handset itself: status, SMS and calls.

// The charge statuses of [Battery].Status.
const (
	// ChargeStatusOnBattery means the handset runs on its battery.
	ChargeStatusOnBattery = iota
	// ChargeStatusExternalPower means the handset runs on external power.
	ChargeStatusExternalPower
	// ChargeStatusNoBattery means no battery is connected.
	ChargeStatusNoBattery
	// ChargeStatusPowerFault means a power fault: calls are inhibited.
	ChargeStatusPowerFault
	// ChargeStatusUnknown is any other status the handset reports.
	ChargeStatusUnknown
)

// The charger statuses of [Battery].Charger.
const (
	// ChargerStatusUnreported means the handset reported no charger status,
	// as only 9555(A) and 9575 handsets do.
	ChargerStatusUnreported = iota
	// ChargerStatusCharging means a charger is connected and charging.
	ChargerStatusCharging
	// ChargerStatusNotCharging means a charger is connected, not charging.
	ChargerStatusNotCharging
	// ChargerStatusNotConnected means no charger is connected.
	ChargerStatusNotConnected
	// ChargerStatusUnknown is any other status the handset reports.
	ChargerStatusUnknown
)

// The registration statuses of [Registration].Status.
const (
	// RegistrationStatusNotRegistered means not registered and not
	// searching.
	RegistrationStatusNotRegistered = iota
	// RegistrationStatusHome means registered on the home network.
	RegistrationStatusHome
	// RegistrationStatusSearching means searching for a network.
	RegistrationStatusSearching
	// RegistrationStatusDenied means registration was denied.
	RegistrationStatusDenied
	// RegistrationStatusUnknown means the status is unknown.
	RegistrationStatusUnknown
	// RegistrationStatusRoaming means registered on a visited network.
	RegistrationStatusRoaming
)

// The message filters of [IridiumHandset.ListSMS].
const (
	// SMSFilterAll lists every message.
	SMSFilterAll = iota
	// SMSFilterUnread lists received messages not read yet.
	SMSFilterUnread
	// SMSFilterRead lists received messages already read.
	SMSFilterRead
	// SMSFilterUnsent lists stored messages not sent yet.
	SMSFilterUnsent
	// SMSFilterSent lists stored messages already sent.
	SMSFilterSent
)

// DeviceInfo identifies the handset.
type DeviceInfo struct {
	// Manufacturer as reported by AT+CGMI.
	Manufacturer string
	// Model as reported by AT+CGMM.
	Model string
	// Revision is the firmware revision reported by AT+CGMR.
	Revision string
	// IMEI as reported by AT+CGSN.
	IMEI string
}

// Battery is the battery's status and charge.
type Battery struct {
	// Status is one of the ChargeStatus constants.
	Status int
	// Bars is the charge as the handset's battery indicator shows it: 0 to
	// 4 on 9555(A) and 9575 handsets, 0 to 3 on the others.
	Bars int
	// Charger is one of the ChargerStatus constants: ChargerStatusUnreported
	// but on 9555(A) and 9575 handsets.
	Charger int
	// Level is the charge in percent, 0 to 100. Only 9555(A) and 9575
	// handsets report it, as LevelReported says: on the others it is zero.
	Level int
	// LevelReported reports that the handset reported Level.
	LevelReported bool
}

// Registration is the network registration.
type Registration struct {
	// Status is one of the RegistrationStatus constants.
	Status int
	// LocationArea is the location area code; "" when not reported.
	LocationArea string
	// CellID is the cell identity; "" when not reported.
	CellID string
}

// SMSMessage is a message stored on the handset or its SIM.
type SMSMessage struct {
	// Index is the storage index, for ReadSMS and DeleteSMS.
	Index int
	// Status is the storage status, such as "REC UNREAD".
	Status string
	// Address is the sender or recipient number.
	Address string
	// Timestamp is the service-centre timestamp (yy/MM/dd,hh:mm:ss±zz); ""
	// when there is none.
	Timestamp string
	// Text is the message text.
	Text string
}

// CommandResponse is what a raw command returned.
type CommandResponse struct {
	// Lines are the information lines; never nil.
	Lines *StringList
}

// KnownProduct is a USB product the official Iridium driver supports.
type KnownProduct struct {
	// ProductID is the USB product ID.
	ProductID int
	// Model is the model name, such as "Iridium 9575".
	Model string
}

// IridiumUSBVendorID returns Iridium's USB vendor ID (0x1EDD), for USB
// device filters.
func IridiumUSBVendorID() int {
	return int(iridium.USBVendorID)
}

// KnownProducts returns the products the official Iridium driver supports.
func KnownProducts() *KnownProductList {
	products := iridium.KnownProducts()
	list := &KnownProductList{items: make([]KnownProduct, len(products))}
	for i, product := range products {
		list.items[i] = KnownProduct{ProductID: int(product.ProductID), Model: product.Model}
	}
	return list
}

// IridiumHandset is a connected Iridium handset: status, SMS and voice
// calls. It is safe for use from several threads; calls run one after
// another.
//
// A SatelliteLink can borrow it for a data call (EstablishLinkWithHandset).
// Until that call ends, however it ends, its methods fail with
// SatlinkErrorHandsetBusy, and afterwards they work again. After a data
// call that could not be ended — the link's, while closing or after failing
// to come up, one Answer took by mistake, or one whose CONNECT no command
// awaited — the call may still be up, and the handset is
// stranded: its methods fail with SatlinkErrorHangUpFailed until
// Reinitialize succeeds. A fault inside the library while a link is set up
// strands it too. Closing the handset meanwhile leaves its port to the link
// until the call has ended (see Close).
//
// In Objective-C and Swift, make one with SatlinkMobileNewIridiumHandset or
// SatlinkMobileOpenSerialHandset, never with the init it inherits from
// NSObject: one made so has no Go side, and its first use ends the app.
// scripts/build-bindings.sh marks that init unavailable.
type IridiumHandset struct {
	// handset is the handset itself, which stays here whatever a link does
	// with it; nil in a zero value.
	handset *iridium.Handset

	// mu serialises the methods but Close, and every change of the slot.
	mu sync.Mutex
	// slotMu guards slot, borrower and closed. Taken after mu, it is held
	// only briefly, never while the handset is in use, so Close, which
	// takes it alone, never waits for a command.
	slotMu sync.Mutex
	// slot is where the handset is.
	slot handsetSlot
	// borrower follows the link that has the handset (slotLent).
	borrower *satnet.Watcher
	// closed records the app's Close. The IridiumHandset refuses to work
	// from then on; and while a link has the handset, or is being set up
	// with it, the handset closes once that link is done with it.
	closed bool
}

// NewIridiumHandset connects through a transport the app provides and
// initialises the handset (echo off, verbose errors, SMS PDU mode), ending
// a data call an earlier session left up. If the handset does not answer
// and a data call it may be relaying could not be ended, or a data call
// that came up meanwhile could not be, it fails with
// SatlinkErrorHangUpFailed.
func NewIridiumHandset(transport ByteTransport) (handset *IridiumHandset, err error) {
	defer recoverPanic(&err)
	if transport == nil {
		return nil, nilArgument("transport")
	}
	return newIridiumHandset(newForeignTransport(transport))
}

// newIridiumHandset returns an IridiumHandset over transport, initialised.
func newIridiumHandset(transport iridium.Transport) (*IridiumHandset, error) {
	handset, err := initializedHandset(transport)
	if err != nil {
		return nil, err
	}
	return &IridiumHandset{handset: handset}, nil
}

// initializedHandset returns a handset over transport, initialised. If that
// fails, a panic included, the handset is closed: a serial port it opened
// is released, and a ByteTransport stays the app's.
func initializedHandset(transport iridium.Transport) (*iridium.Handset, error) {
	handset := iridium.NewHandset(transport)
	initialized := false
	defer closeUnlessHandedOn(handset, &initialized)
	if err := handset.Initialize(); err != nil {
		return nil, fromError(err)
	}
	initialized = true
	return handset, nil
}

// closeUnlessHandedOn closes handset unless *handedOn. A function that
// hands a handset it made on only when it succeeds defers it, so that every
// other way out, a panic the boundary recovers included, releases a serial
// port that function opened: the port is free again, and closing it drops DTR, which ends a call left
// up. What went wrong is what the app needs to hear; closing only lets go
// of the port.
func closeUnlessHandedOn(handset *iridium.Handset, handedOn *bool) {
	if !*handedOn {
		_ = handset.Close()
	}
}

// Reinitialize gets the handset talking again, ending any data call it is
// still in: the way back after SatlinkErrorHangUpFailed, which it fails
// with again until that call is ended. A stranded handset is initialised,
// then hung up, and is usable again once both succeed. While a link has
// the handset, it fails with SatlinkErrorHandsetBusy.
func (h *IridiumHandset) Reinitialize() (err error) {
	defer recoverPanic(&err)
	handset, err := made(h.handset, "IridiumHandset")
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reclaim()
	reinitialize, err := h.reinitialization()
	if err != nil {
		return err
	}
	outcome := reinitialize(handset)
	switch {
	case outcome == nil:
		h.recovered()
	case isCallMayStillBeUp(outcome):
		h.strand()
	}
	return fromError(outcome)
}

// endStrandedCall initialises a handset whose data call could not be hung
// up, and ends the call: it may be held in online-command mode, which only
// firmware with +CPAS reveals to Initialize, and ATH answers OK when there
// is none. Until both succeed the call may still be up, whatever failed, so
// every error it returns is of kind iridium.ErrCallMayStillBeUp.
func endStrandedCall(handset *iridium.Handset) error {
	if err := handset.Initialize(); err != nil {
		return asCallMayStillBeUp(err)
	}
	if err := handset.HangUp(); err != nil {
		return callMayStillBeUpError("ATH failed: " + err.Error())
	}
	return nil
}

// isCallMayStillBeUp reports whether err is the handset's
// iridium.ErrCallMayStillBeUp: a data call it could not end may still be
// up. That kind, which the app hears as SatlinkErrorHangUpFailed, strands
// the handset. The CallMayStillBeUp flag that other kinds may carry is not
// returned by any IridiumHandset method.
func isCallMayStillBeUp(err error) bool {
	var core *iridium.Error
	return errors.As(err, &core) && core.Kind == iridium.ErrCallMayStillBeUp
}

// asCallMayStillBeUp returns err, which says what failed while a data call
// may still be up, as an error of kind iridium.ErrCallMayStillBeUp:
// unchanged if it is one, otherwise with its text as the message.
func asCallMayStillBeUp(err error) error {
	if isCallMayStillBeUp(err) {
		return err
	}
	return callMayStillBeUpError(err.Error())
}

// callMayStillBeUpError reports a data call that could not be ended, as the
// handset reports one: kind iridium.ErrCallMayStillBeUp, whose errors always
// carry the flag, with message saying what failed.
func callMayStillBeUpError(message string) *iridium.Error {
	return &iridium.Error{Kind: iridium.ErrCallMayStillBeUp, Message: message, CallMayStillBeUp: true}
}

// Command sends a raw AT command, such as "AT+CSQ", and waits up to
// timeoutMillis for its final result.
//
// A command line that can take the handset online fails with
// SatlinkErrorInvalidArgument before anything is sent. One does if it holds
// a data dial (D without ';'), A (answer) or O (return online) anywhere
// among its commands, as the handset reads them, or A/, which repeats the
// last line. So "ATD123", "at d123", "ATE0D123", "AT+CSQ D123",
// "AT+CSQD123" and "ATA0" are refused. So are auto-answer, "ATS0=1", the
// writes to S3 and S5, which change how the handset reads the lines after,
// and "=n", which writes the register last referred to. Each would leave a
// data call up, billing, that nothing ends. An extended command that the
// ISU AT Command Reference does not define is read letter by letter, and
// refused if a letter can go online: "AT+BOGUS" is. A space ends an
// extended command, and what follows is read as commands: "AT+CKPD=160" is
// sent, but "AT+CKPD = 160" is refused. So is a line that holds a character
// other than printable ASCII, such as a backspace, which can change the
// line the handset runs. A link (EstablishLinkWithHandset) places a data
// call and ends it.
//
// A CONNECT that arrives all the same with the command's answer, such as
// that of a call the handset answered on its own, has its call hung up at
// once, and Command fails with SatlinkErrorCall. One that arrives after
// Command gave up or returned, as that of a call answered by a key press
// ("AT+CKPD=140", the Green key) may, is hung up by the next method that
// talks to the handset, which fails likewise; Reinitialize, run after it
// arrived, hangs it up as a call left up, and succeeds. If that hang-up
// fails, the method fails with SatlinkErrorHangUpFailed and the handset is
// stranded: the call may still be up and billing, and Reinitialize ends it.
// A response line that reads as CONNECT counts as a call's, even if it is
// message text, as AT+SBDRT's may be.
func (h *IridiumHandset) Command(command string, timeoutMillis int64) (response *CommandResponse, err error) {
	defer recoverPanic(&err)
	timeout := timeoutFromMillis(timeoutMillis)
	answer, err := withHandset(h, func(handset *iridium.Handset) (iridium.Response, error) {
		return handset.CommandTimeout(command, timeout)
	})
	if err != nil {
		return nil, err
	}
	return &CommandResponse{Lines: &StringList{items: answer.Lines}}, nil
}

// Info reports manufacturer, model, firmware revision and IMEI.
func (h *IridiumHandset) Info() (info *DeviceInfo, err error) {
	defer recoverPanic(&err)
	reported, err := withHandset(h, (*iridium.Handset).Info)
	if err != nil {
		return nil, err
	}
	return &DeviceInfo{
		Manufacturer: reported.Manufacturer,
		Model:        reported.Model,
		Revision:     reported.Revision,
		IMEI:         reported.IMEI,
	}, nil
}

// IMSI reports the subscriber identity, which requires a SIM.
func (h *IridiumHandset) IMSI() (imsi string, err error) {
	defer recoverPanic(&err)
	return withHandset(h, (*iridium.Handset).IMSI)
}

// SignalQuality reports the signal bars, 0 to 5, from a new reading. The
// handset answers once it has one, usually within two seconds, so this
// waits up to a minute. One that is not registered may take fifty seconds
// and then fail: check Registration first, or use LastSignalQuality.
func (h *IridiumHandset) SignalQuality() (bars int, err error) {
	defer recoverPanic(&err)
	return withHandset(h, (*iridium.Handset).SignalQuality)
}

// LastSignalQuality reports the last signal bars the handset measured, 0
// to 5, at once: what its signal indicator shows.
func (h *IridiumHandset) LastSignalQuality() (bars int, err error) {
	defer recoverPanic(&err)
	return withHandset(h, (*iridium.Handset).LastSignalQuality)
}

// Battery reports the battery's status and charge: the bars of the
// handset's indicator on every handset, and the charger and the charge in
// percent on 9555(A) and 9575 handsets.
func (h *IridiumHandset) Battery() (battery *Battery, err error) {
	defer recoverPanic(&err)
	reported, err := withHandset(h, (*iridium.Handset).Battery)
	if err != nil {
		return nil, err
	}
	return &Battery{
		Status:        chargeStatusOf(reported.Status),
		Bars:          reported.Bars,
		Charger:       chargerStatusOf(reported.Charger),
		Level:         reported.Level,
		LevelReported: reported.LevelReported,
	}, nil
}

// Registration reports the network registration.
func (h *IridiumHandset) Registration() (registration *Registration, err error) {
	defer recoverPanic(&err)
	reported, err := withHandset(h, (*iridium.Handset).Registration)
	if err != nil {
		return nil, err
	}
	return &Registration{
		Status:       registrationStatusOf(reported.Status),
		LocationArea: reported.LocationArea,
		CellID:       reported.CellID,
	}, nil
}

// Clock reports the handset's clock (yy/MM/dd,hh:mm:ss±zz).
func (h *IridiumHandset) Clock() (clock string, err error) {
	defer recoverPanic(&err)
	return withHandset(h, (*iridium.Handset).Clock)
}

// SendSMS sends a text message and returns the network's message
// reference. Submission can take over a minute.
func (h *IridiumHandset) SendSMS(number, text string) (reference int, err error) {
	defer recoverPanic(&err)
	return withHandset(h, func(handset *iridium.Handset) (int, error) {
		return handset.SendSMS(number, text)
	})
}

// ListSMS lists the stored messages filter, one of the SMSFilter
// constants, selects; listing marks unread messages read. Another filter
// value fails with SatlinkErrorInvalidArgument, "%d is not an SMS filter",
// as iridium.Handset.ListSMS reports it.
func (h *IridiumHandset) ListSMS(filter int) (messages *SMSMessageList, err error) {
	defer recoverPanic(&err)
	// The SMSFilter constants have the values of iridium's, which refuses
	// any other.
	stored, err := withHandset(h, func(handset *iridium.Handset) ([]iridium.SMSMessage, error) {
		return handset.ListSMS(iridium.SMSFilter(filter))
	})
	if err != nil {
		return nil, err
	}
	list := &SMSMessageList{items: make([]SMSMessage, len(stored))}
	for i, message := range stored {
		list.items[i] = smsMessageOf(message)
	}
	return list, nil
}

// ReadSMS reads the message stored at index, which marks it read.
func (h *IridiumHandset) ReadSMS(index int) (message *SMSMessage, err error) {
	defer recoverPanic(&err)
	stored, err := withHandset(h, func(handset *iridium.Handset) (iridium.SMSMessage, error) {
		return handset.ReadSMS(index)
	})
	if err != nil {
		return nil, err
	}
	read := smsMessageOf(stored)
	return &read, nil
}

// DeleteSMS deletes the message stored at index.
func (h *IridiumHandset) DeleteSMS(index int) (err error) {
	defer recoverPanic(&err)
	return h.perform(func(handset *iridium.Handset) error {
		return handset.DeleteSMS(index)
	})
}

// Dial starts a voice call.
func (h *IridiumHandset) Dial(number string) (err error) {
	defer recoverPanic(&err)
	return h.perform(func(handset *iridium.Handset) error {
		return handset.Dial(number)
	})
}

// Answer answers an incoming voice call. A data call answered by mistake, or
// found up already, is ended at once, and Answer fails with
// SatlinkErrorCall; if it cannot be ended, with SatlinkErrorHangUpFailed,
// and the handset is stranded until Reinitialize ends the call.
func (h *IridiumHandset) Answer() (err error) {
	defer recoverPanic(&err)
	return h.perform((*iridium.Handset).Answer)
}

// HangUp ends a call: a voice call, or a data call held in online-command
// mode — escaped from but never hung up — which only firmware with +CPAS
// reveals when the handset is initialised. After SatlinkErrorHangUpFailed
// or SatlinkErrorInternal from a link of its own (EstablishLink), a new
// IridiumHandset on the same transport ends the call with it.
func (h *IridiumHandset) HangUp() (err error) {
	defer recoverPanic(&err)
	return h.perform((*iridium.Handset).HangUp)
}

// TakeEvents returns the unsolicited events already received, such as
// "RING" or `+CMTI: "SM",3`, oldest first; an empty list, never nil, when
// there are none or while the handset cannot be used — a link has it, or
// it is stranded (see PollEvents for why).
func (h *IridiumHandset) TakeEvents() (events *StringList) {
	events = &StringList{}
	defer survivePanic("IridiumHandset.TakeEvents")
	taken, err := withHandset(h, func(handset *iridium.Handset) ([]string, error) {
		return handset.TakeURCs(), nil
	})
	if err != nil {
		// The handset cannot be used now: no events until it can.
		return events
	}
	return &StringList{items: taken}
}

// PollEvents waits up to timeoutMillis for unsolicited events and returns
// all received ones, possibly none. Like the other methods, it fails with
// SatlinkErrorHandsetBusy while a link has the handset, and with
// SatlinkErrorHangUpFailed while the handset is stranded (see
// IridiumHandset).
//
// It is the method most likely to read the CONNECT of a data call that no
// command awaited, such as one the handset answered on its own. It hangs
// that call up and fails with SatlinkErrorCall; if the hang-up fails, it
// fails with SatlinkErrorHangUpFailed and strands the handset: the call may
// still be up and billing, and Reinitialize ends it. Any other method that
// talks to the handset may read such a CONNECT and fail the same way.
func (h *IridiumHandset) PollEvents(timeoutMillis int64) (events *StringList, err error) {
	defer recoverPanic(&err)
	timeout := timeoutFromMillis(timeoutMillis)
	polled, err := withHandset(h, func(handset *iridium.Handset) ([]string, error) {
		return handset.PollURCs(timeout)
	})
	if err != nil {
		return nil, err
	}
	return &StringList{items: polled}, nil
}

// Close closes a serial port the handset opened itself
// (OpenSerialHandset), which drops DTR; a ByteTransport stays the app's to
// close. While a link has the handset, or is being set up with it, the
// port stays open for that link, which hangs up through it, and closes once
// the call has ended. Close does not wait, and it is idempotent. The closed
// IridiumHandset refuses to work, even once such a link has ended: every
// method but Close fails with SatlinkErrorInvalidArgument, "the handset is
// closed", and TakeEvents returns nothing. A new IridiumHandset on the
// transport talks to the handset again.
func (h *IridiumHandset) Close() (err error) {
	defer recoverPanic(&err)
	handset, err := made(h.handset, "IridiumHandset")
	if err != nil {
		return err
	}
	if !h.recordClose() {
		return nil
	}
	return fromError(handset.Close())
}

// withHandset runs query on the handset, unless the handset cannot be used
// now, and returns its result.
func withHandset[T any](h *IridiumHandset, query func(*iridium.Handset) (T, error)) (T, error) {
	var result T
	err := h.perform(func(handset *iridium.Handset) error {
		var err error
		result, err = query(handset)
		return err
	})
	return result, err
}

// perform runs operation on the handset, unless the handset cannot be used
// now — a zero IridiumHandset, a closed one, a link has it, or it is
// stranded — and reports its error as the app sees it. An operation that
// leaves a data call that may still be up — Answer's, taken by mistake, or
// one whose CONNECT no command awaited — strands the handset.
func (h *IridiumHandset) perform(operation func(*iridium.Handset) error) error {
	handset, err := made(h.handset, "IridiumHandset")
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reclaim()
	if err := h.unavailable(); err != nil {
		return err
	}
	err = operation(handset)
	if isCallMayStillBeUp(err) {
		h.strand()
	}
	return fromError(err)
}

// chargeStatuses are the ChargeStatus constants of the handset's charge
// statuses; any other is ChargeStatusUnknown.
var chargeStatuses = map[iridium.ChargeStatus]int{
	iridium.ChargeStatusOnBattery:     ChargeStatusOnBattery,
	iridium.ChargeStatusExternalPower: ChargeStatusExternalPower,
	iridium.ChargeStatusNoBattery:     ChargeStatusNoBattery,
	iridium.ChargeStatusPowerFault:    ChargeStatusPowerFault,
}

// chargerStatuses are the ChargerStatus constants of the handset's charger
// statuses.
var chargerStatuses = map[iridium.ChargerStatus]int{
	iridium.ChargerStatusUnreported:   ChargerStatusUnreported,
	iridium.ChargerStatusCharging:     ChargerStatusCharging,
	iridium.ChargerStatusNotCharging:  ChargerStatusNotCharging,
	iridium.ChargerStatusNotConnected: ChargerStatusNotConnected,
}

// chargerStatusOf returns the ChargerStatus constant of status.
func chargerStatusOf(status iridium.ChargerStatus) int {
	if constant, ok := chargerStatuses[status]; ok {
		return constant
	}
	return ChargerStatusUnknown
}

// chargeStatusOf returns the ChargeStatus constant of status.
func chargeStatusOf(status iridium.ChargeStatus) int {
	if constant, ok := chargeStatuses[status]; ok {
		return constant
	}
	return ChargeStatusUnknown
}

// registrationStatuses are the RegistrationStatus constants of the
// handset's registration statuses.
var registrationStatuses = map[iridium.RegistrationStatus]int{
	iridium.RegistrationStatusNotRegistered: RegistrationStatusNotRegistered,
	iridium.RegistrationStatusHome:          RegistrationStatusHome,
	iridium.RegistrationStatusSearching:     RegistrationStatusSearching,
	iridium.RegistrationStatusDenied:        RegistrationStatusDenied,
	iridium.RegistrationStatusUnknown:       RegistrationStatusUnknown,
	iridium.RegistrationStatusRoaming:       RegistrationStatusRoaming,
}

// registrationStatusOf returns the RegistrationStatus constant of status;
// the handset reports no other statuses, and any would be unknown.
func registrationStatusOf(status iridium.RegistrationStatus) int {
	if constant, ok := registrationStatuses[status]; ok {
		return constant
	}
	return RegistrationStatusUnknown
}

// smsMessageOf returns the app's copy of a stored message.
func smsMessageOf(message iridium.SMSMessage) SMSMessage {
	return SMSMessage{
		Index:     message.Index,
		Status:    message.Status,
		Address:   message.Address,
		Timestamp: message.Timestamp,
		Text:      message.Text,
	}
}
