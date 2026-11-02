package iridium

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// SBDCapabilities describes software limits, not verified network provisioning.
type SBDCapabilities struct {
	// ReadFinalResult waits for the trailing OK emitted by voice handsets.
	ReadFinalResult bool
	MaxMO, MaxMT    int
	RingAlerts      bool
}

// SBDProfile returns the documented voice-terminal profile. Unknown models
// need explicit limits. A provider may configure smaller limits.
func SBDProfile(model string) (SBDCapabilities, error) {
	switch strings.TrimSpace(model) {
	case "9555", "9555A", "9575", "9575P", "9575E":
		return SBDCapabilities{true, 1960, 1890, true}, nil
	default:
		return SBDCapabilities{}, invalidArgument("unknown SBD model; supply explicit capabilities")
	}
}

// SBDStatus reports the two local buffers and the last known gateway queue.
type SBDStatus struct{ MOFlag, MOMSN, MTFlag, MTMSN, RingAlert, Queued int }

// SBDSessionSettings controls one satellite session. Cancellation before
// submission prevents transmission; afterwards the result is still awaited.
type SBDSessionSettings struct {
	Timeout       time.Duration
	Cancellation  *Cancellation
	Alert, Legacy bool
}

// DefaultSBDSessionSettings allows two minutes for a session.
func DefaultSBDSessionSettings() SBDSessionSettings {
	return SBDSessionSettings{Timeout: 120 * time.Second}
}

// SBDSessionResult preserves gateway statuses even when an exchange fails.
// Accepted means gateway acceptance, not application delivery.
type SBDSessionResult struct {
	Reported                                           bool
	MOStatus, MOMSN, MTStatus, MTMSN, MTLength, Queued int
	Legacy                                             bool
}

func (r SBDSessionResult) Accepted() bool {
	if !r.Reported {
		return false
	}
	if r.Legacy {
		return r.MOStatus == 0 || r.MOStatus == 1
	}
	return r.MOStatus >= 0 && r.MOStatus <= 4
}

// Retryable identifies temporary failures. Unknown statuses require review.
func (r SBDSessionResult) Retryable() bool {
	if !r.Reported {
		return false
	}
	switch r.MOStatus {
	case 10, 11, 13, 17, 18, 19, 32, 35, 36, 38:
		return true
	}
	return r.Legacy && r.MOStatus == 2
}

// SBDExchange includes an incoming payload, if MTStatus is 1. Incoming data
// remains in the handset until explicitly cleared, allowing durable storage.
type SBDExchange struct {
	Result  SBDSessionResult
	Payload []byte
}

// SBDClient exclusively owns a handset until Close. It does not close USB.
// Methods serialize on the handset; ordinary handset methods return ErrBusy.
type SBDClient struct {
	h      *Handset
	closed atomic.Bool
}

// AcquireSBD reserves a handset for a manager or a sequence of SBD operations.
func (h *Handset) AcquireSBD() (*SBDClient, error) {
	if err := h.claim(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sbdUnsafe {
		h.release()
		return nil, &Error{Kind: ErrSBDUnsynchronised}
	}
	return &SBDClient{h: h}, nil
}

// Close releases ownership; it never aborts a satellite session.
func (c *SBDClient) Close() error {
	c.h.mu.Lock()
	defer c.h.mu.Unlock()
	if !c.closed.Swap(true) {
		c.h.release()
	}
	return nil
}
func sbdUse[T any](c *SBDClient, f func(*Handset) (T, error)) (T, error) {
	c.h.mu.Lock()
	defer c.h.mu.Unlock()
	var zero T
	if c.closed.Load() {
		return zero, invalidArgument("SBD client is closed")
	}
	if c.h.sbdUnsafe {
		return zero, &Error{Kind: ErrSBDUnsynchronised}
	}
	return f(c.h)
}
func sbdOnce[T any](h *Handset, f func(*SBDClient) (T, error)) (T, error) {
	c, e := h.AcquireSBD()
	if e != nil {
		var z T
		return z, e
	}
	defer c.Close()
	return f(c)
}

// Status queries AT+SBDSX without transmitting.
func (c *SBDClient) Status() (SBDStatus, error) { return sbdUse(c, (*Handset).sbdStatus) }

// SBDStatus queries the local buffers without transmitting.
func (h *Handset) SBDStatus() (SBDStatus, error) { return sbdOnce(h, (*SBDClient).Status) }
func (h *Handset) sbdStatus() (SBDStatus, error) {
	fields, e := h.queryFields("AT+SBDSX", "+SBDSX")
	if e != nil {
		return SBDStatus{}, e
	}
	n, e := sbdNumbers(fields, 6)
	if e != nil {
		return SBDStatus{}, e
	}
	if n[0] < 0 || n[0] > 1 || n[1] < 0 || n[2] < 0 || n[2] > 1 || n[4] < 0 || n[4] > 1 || n[5] < 0 {
		return SBDStatus{}, parseError("invalid SBD buffer flags")
	}
	return SBDStatus{n[0], n[1], n[2], n[3], n[4], n[5]}, nil
}
func sbdNumbers(fields []string, count int) ([]int, error) {
	if len(fields) != count {
		return nil, &Error{Kind: ErrParse, Message: "wrong SBD field count"}
	}
	n := make([]int, count)
	for i, f := range fields {
		v, e := strconv.Atoi(strings.TrimSpace(f))
		if e != nil || v < -1 || v > 65535 {
			return nil, &Error{Kind: ErrParse, Message: "invalid SBD numeric field"}
		}
		n[i] = v
	}
	return n, nil
}
func validSBDCaps(c SBDCapabilities) bool {
	return c.MaxMO > 0 && c.MaxMO <= 1960 && c.MaxMT > 0 && c.MaxMT <= 1960
}

// WriteBinary loads the outgoing buffer using AT+SBDWB; it does not transmit.
func (c *SBDClient) WriteBinary(p []byte, caps SBDCapabilities) error {
	_, e := sbdUse(c, func(h *Handset) (bool, error) { return true, h.sbdWrite(p, caps) })
	return e
}
func (h *Handset) sbdWrite(p []byte, caps SBDCapabilities) error {
	if !validSBDCaps(caps) || len(p) < 1 || len(p) > caps.MaxMO {
		return invalidArgument("payload exceeds SBD profile or is empty")
	}
	if err := h.sbdStart(fmt.Sprintf("AT+SBDWB=%d", len(p))); err != nil {
		return err
	}
	h.sbdUnsafe = true
	if _, e := h.sbdUntil("READY", deadlineAfter(h.commandTimeout())); e != nil {
		return e
	}
	wire := make([]byte, len(p)+2)
	copy(wire, p)
	binary.BigEndian.PutUint16(wire[len(p):], sbdChecksum(p))
	if e := h.write(wire); e != nil {
		return e
	}
	lines, e := h.sbdUntil("OK", deadlineAfter(h.commandTimeout()))
	if e != nil {
		return e
	}
	h.sbdUnsafe = false
	if len(lines) != 1 || lines[0] != "0" {
		return &Error{Kind: ErrSBD, Code: strings.Join(lines, ","), Message: "binary buffer write rejected"}
	}
	return nil
}
func sbdChecksum(p []byte) (sum uint16) {
	for _, b := range p {
		sum += uint16(b)
	}
	return
}

// WriteText loads a printable text message of at most 120 bytes, without radio use.
func (c *SBDClient) WriteText(text string) error {
	_, e := sbdUse(c, func(h *Handset) (bool, error) {
		if len(text) < 1 || len(text) > 120 {
			return false, invalidArgument("inline SBD text must contain 1 to 120 bytes")
		}
		if e := checkPrintable(text); e != nil {
			return false, e
		}
		_, e := h.command("AT+SBDWT=" + text)
		return true, e
	})
	return e
}

// Clear removes local buffers: 0 outgoing, 1 incoming, 2 both.
func (c *SBDClient) Clear(which int) error {
	_, e := sbdUse(c, func(h *Handset) (bool, error) { return true, h.sbdClear(which) })
	return e
}
func (h *Handset) sbdClear(which int) error {
	if which < 0 || which > 2 {
		return invalidArgument("SBD buffer must be 0, 1 or 2")
	}
	r, e := h.command(fmt.Sprintf("AT+SBDD%d", which))
	if e == nil && (len(r.Lines) != 1 || r.Lines[0] != "0") {
		return &Error{Kind: ErrSBD, Message: "buffer clear rejected"}
	}
	return e
}

// ReadBinary reads AT+SBDRB, validating its length and checksum.
// ReadFinalResult controls the trailing OK; payloads never enter the line parser.
func (c *SBDClient) ReadBinary(caps SBDCapabilities) ([]byte, error) {
	return sbdUse(c, func(h *Handset) ([]byte, error) { return h.sbdRead(caps) })
}
func (h *Handset) sbdRead(caps SBDCapabilities) ([]byte, error) {
	if !validSBDCaps(caps) {
		return nil, invalidArgument("invalid SBD capabilities")
	}
	if e := h.sbdStart("AT+SBDRB"); e != nil {
		return nil, e
	}
	h.sbdUnsafe = true
	deadline := deadlineAfter(h.commandTimeout())
	header, e := h.sbdBytes(2, deadline)
	if e != nil {
		return nil, e
	}
	n := int(binary.BigEndian.Uint16(header))
	if n > caps.MaxMT {
		return nil, &Error{Kind: ErrParse, Message: "SBD receive length exceeds profile"}
	}
	wire, e := h.sbdBytes(n+2, deadline)
	if e != nil {
		return nil, e
	}
	if caps.ReadFinalResult {
		if _, e = h.sbdUntil("OK", deadline); e != nil {
			return nil, e
		}
	}
	h.sbdUnsafe = false
	if sbdChecksum(wire[:n]) != binary.BigEndian.Uint16(wire[n:]) {
		return nil, &Error{Kind: ErrSBD, Message: "incoming checksum mismatch"}
	}
	return wire[:n], nil
}
func (h *Handset) sbdBytes(n int, deadline time.Time) ([]byte, error) {
	for len(h.rx.buf) < n {
		wait, ok := timeToWait(deadline)
		if !ok {
			return nil, timeoutError("SBD binary read")
		}
		if e := h.fill(wait); e != nil {
			return nil, e
		}
	}
	p := append([]byte(nil), h.rx.buf[:n]...)
	h.rx.buf = h.rx.buf[n:]
	return p, nil
}
func (h *Handset) sbdStart(command string) error {
	if h.queueCompleteLines() {
		return h.unawaitedConnect(request{command: command}, "before SBD command")
	}
	return h.writeCommandLine(command)
}
func (h *Handset) sbdUntil(end string, deadline time.Time) ([]string, error) {
	var lines []string
	for {
		line, ok := h.rx.nextLine()
		if !ok {
			wait, more := timeToWait(deadline)
			if !more {
				return lines, timeoutError("SBD " + end)
			}
			if e := h.fill(wait); e != nil {
				return lines, e
			}
			continue
		}
		if line == end {
			return lines, nil
		}
		if isURC(line, "") {
			h.urcs = append(h.urcs, line)
			continue
		}
		if result, final := classifyFinalResult(line); final && result.Kind == FinalResultCME {
			h.sbdUnsafe = false
			return lines, &Error{Kind: ErrEquipment, Code: result.Code}
		}
		if line == "ERROR" {
			h.sbdUnsafe = false
			return lines, &Error{Kind: ErrCommandFailed, Command: "SBD"}
		}
		lines = append(lines, line)
		if len(lines) > 32 {
			return nil, &Error{Kind: ErrParse, Message: "too many SBD response lines"}
		}
	}
}

// Session conducts one satellite session. Its result survives network failure.
func (c *SBDClient) Session(settings SBDSessionSettings) (SBDSessionResult, error) {
	return sbdUse(c, func(h *Handset) (SBDSessionResult, error) { return h.sbdSession(settings) })
}
func (h *Handset) sbdSession(settings SBDSessionSettings) (SBDSessionResult, error) {
	var result SBDSessionResult
	if e := validateSBDSettings(settings); e != nil {
		return result, e
	}
	command, prefix := "AT+SBDIX", "+SBDIX:"
	if settings.Alert {
		command = "AT+SBDIXA"
	}
	if settings.Legacy {
		command, prefix = "AT+SBDI", "+SBDI:"
	}
	h.sbdUnsafe = true
	response, e := h.commandWithTimeout(command, settings.Timeout)
	if e != nil {
		if errors.Is(e, ErrCommandFailed) || errors.Is(e, ErrEquipment) {
			h.sbdUnsafe = false
			return result, e
		}
		return result, &Error{Kind: ErrSBDUncertain, Err: e, Message: "session result not received"}
	}
	h.sbdUnsafe = false
	return parseSBDSession(response, prefix, settings.Legacy)
}
func parseSBDSession(response Response, prefix string, legacy bool) (SBDSessionResult, error) {
	var result SBDSessionResult
	for _, line := range response.Lines {
		if tail, ok := strings.CutPrefix(line, prefix); ok {
			n, e := sbdNumbers(strings.Split(tail, ","), 6)
			if e != nil {
				return result, &Error{Kind: ErrSBDUncertain, Err: e}
			}
			if !validSBDSessionNumbers(n) {
				return result, &Error{Kind: ErrSBDUncertain, Message: "invalid SBD session fields"}
			}
			result = SBDSessionResult{true, n[0], n[1], n[2], n[3], n[4], n[5], legacy}
			if !result.Accepted() {
				return result, &Error{Kind: ErrSBD, Code: strconv.Itoa(result.MOStatus)}
			}
			return result, nil
		}
	}
	return result, &Error{Kind: ErrSBDUncertain, Message: "missing session status"}
}

// Exchange sends payload, or performs a receive-only session for nil payload.
// It refuses to overwrite an unread incoming or unexpected outgoing buffer.
func (c *SBDClient) Exchange(payload []byte, caps SBDCapabilities, settings SBDSessionSettings) (SBDExchange, error) {
	return sbdUse(c, func(h *Handset) (SBDExchange, error) { return h.sbdExchange(payload, caps, settings) })
}
func (h *Handset) ExchangeSBD(p []byte, caps SBDCapabilities, s SBDSessionSettings) (SBDExchange, error) {
	return sbdOnce(h, func(c *SBDClient) (SBDExchange, error) { return c.Exchange(p, caps, s) })
}
func (h *Handset) sbdExchange(p []byte, caps SBDCapabilities, s SBDSessionSettings) (SBDExchange, error) {
	var x SBDExchange
	if e := validateSBDSettings(s); e != nil {
		return x, e
	}
	if !validSBDPayload(p, caps) {
		return x, invalidArgument("invalid SBD payload or limits")
	}
	if s.Cancellation != nil && s.Cancellation.Cancelled() {
		return x, cancelledError()
	}
	status, e := h.sbdStatus()
	if e != nil {
		return x, e
	}
	if status.MTFlag != 0 || status.MOFlag != 0 {
		return x, &Error{Kind: ErrSBD, Message: "recover existing SBD buffers before exchange"}
	}
	if p != nil {
		if e = h.sbdWrite(p, caps); e != nil {
			return x, e
		}
	} else if e = h.sbdClear(0); e != nil {
		return x, e
	}
	x.Result, e = h.sbdSession(s)
	if x.Result.MTStatus == 1 {
		var readErr error
		x.Payload, readErr = h.sbdRead(caps)
		if readErr != nil {
			return x, readErr
		}
	}
	return x, e
}

// Loopback copies the outgoing buffer to incoming using AT+SBDTC. No radio use.
func (c *SBDClient) Loopback() error {
	_, e := sbdUse(c, func(h *Handset) (bool, error) {
		r, e := h.command("AT+SBDTC")
		if e == nil && !sbdLoopbackOK(r.Lines) {
			e = &Error{Kind: ErrSBD, Message: "loopback rejected"}
		}
		return true, e
	})
	return e
}

// ConfigureRingAlerts changes local alert reporting, without registration.
func (c *SBDClient) ConfigureRingAlerts(enabled bool) error {
	_, e := sbdUse(c, func(h *Handset) (bool, error) {
		n := 0
		if enabled {
			n = 1
		}
		_, e := h.command(fmt.Sprintf("AT+SBDMTA=%d", n))
		return true, e
	})
	return e
}

// Registration reports the SBD registration state.
func (c *SBDClient) Registration() (int, error) {
	return sbdUse(c, func(h *Handset) (int, error) {
		f, e := h.queryFields("AT+SBDREG?", "+SBDREG")
		if e != nil {
			return 0, e
		}
		n, e := sbdNumbers(f, 1)
		if e != nil {
			return 0, e
		}
		return n[0], nil
	})
}

// Register explicitly performs an SBD registration, returning status and error code.
func (c *SBDClient) Register() (Response, error) {
	return sbdUse(c, func(h *Handset) (Response, error) {
		r, e := h.sbdNetworkCommand("AT+SBDREG")
		if e != nil {
			return r, e
		}
		fields, e := fieldsWithPrefix(r, "+SBDREG")
		if e != nil {
			return r, e
		}
		n, e := sbdNumbers(fields, 2)
		if e != nil {
			return r, e
		}
		if n[0] != 2 || n[1] != 0 {
			return r, &Error{Kind: ErrSBD, Code: strconv.Itoa(n[1]), Message: "registration rejected"}
		}
		return r, nil
	})
}

// Detach explicitly detaches SBD registration.
func (c *SBDClient) Detach() (Response, error) {
	return sbdUse(c, func(h *Handset) (Response, error) {
		r, e := h.sbdNetworkCommand("AT+SBDDET")
		if e == nil && (len(r.Lines) != 1 || r.Lines[0] != "0") {
			e = &Error{Kind: ErrSBD, Message: "detach rejected"}
		}
		return r, e
	})
}

// PollEvents polls URCs while the client owns the handset.
func (c *SBDClient) PollEvents(timeout time.Duration) ([]string, error) {
	return sbdUse(c, func(h *Handset) ([]string, error) { return h.pollURCs(timeout) })
}

// Info returns handset identity while the client owns it.
func (c *SBDClient) Info() (DeviceInfo, error) {
	return sbdUse(c, func(h *Handset) (DeviceInfo, error) {
		var v DeviceInfo
		var e error
		v.Manufacturer, e = h.queryText("AT+CGMI")
		if e != nil {
			return v, e
		}
		v.Model, e = h.queryText("AT+CGMM")
		if e != nil {
			return v, e
		}
		v.Revision, e = h.queryText("AT+CGMR")
		if e != nil {
			return v, e
		}
		v.IMEI, e = h.queryText("AT+CGSN")
		return v, e
	})
}
func sbdEntersData(command string) bool {
	u := strings.ToUpper(command)
	if strings.Contains(u, "+SBDI") || strings.Contains(u, "+SBDDET") || strings.Contains(strings.ReplaceAll(u, "+SBDREG?", ""), "+SBDREG") {
		return true
	}
	for _, name := range []string{"+SBDWB", "+SBDRB", "+SBDRT"} {
		if strings.Contains(u, name) {
			return true
		}
	}
	return strings.Contains(u, "+SBDWT") && !strings.Contains(u, "+SBDWT=")
}

// ReadText returns the incoming payload as bytes in a Go string, using binary
// framing so embedded AT result strings cannot terminate the read.
func (c *SBDClient) ReadText(caps SBDCapabilities) (string, error) {
	p, e := c.ReadBinary(caps)
	return string(p), e
}

func sbdLoopbackOK(lines []string) bool {
	if len(lines) != 1 {
		return false
	}
	if lines[0] == "0" {
		return true
	}
	prefix := "sbdtc: outbound sbd copied to inbound sbd: size = "
	line := strings.ToLower(lines[0])
	if !strings.HasPrefix(line, prefix) {
		return false
	}
	n, e := strconv.Atoi(strings.TrimPrefix(line, prefix))
	return e == nil && n >= 0 && n <= 1960
}

func validSBDSessionNumbers(n []int) bool {
	return n[0] >= 0 && n[1] >= 0 && n[2] >= 0 && n[2] <= 2 && n[4] >= 0 && n[4] <= 1960 && n[5] >= 0
}

func validateSBDSettings(s SBDSessionSettings) error {
	if s.Timeout <= 0 {
		return invalidArgument("SBD timeout must be positive")
	}
	if s.Legacy && s.Alert {
		return invalidArgument("legacy sessions cannot respond to ring alerts")
	}
	if s.Cancellation != nil && s.Cancellation.Cancelled() {
		return cancelledError()
	}
	return nil
}

func (h *Handset) sbdNetworkCommand(command string) (Response, error) {
	h.sbdUnsafe = true
	r, e := h.commandWithTimeout(command, 120*time.Second)
	if e == nil || errors.Is(e, ErrCommandFailed) || errors.Is(e, ErrEquipment) {
		h.sbdUnsafe = false
		return r, e
	}
	return r, &Error{Kind: ErrSBDUncertain, Message: "SBD command completion is unknown", Err: e}
}

func validSBDPayload(p []byte, c SBDCapabilities) bool {
	return validSBDCaps(c) && (p == nil || (len(p) > 0 && len(p) <= c.MaxMO))
}
