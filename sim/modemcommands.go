package sim

import (
	"fmt"
	"time"
)

// What each AT command does.

const (
	manufacturer = "Iridium"
	revision     = "Call Processor Version: SIM-1.0"
	// imsi is Iridium's mobile country and network codes (901 03) and an
	// obviously made-up subscriber number.
	imsi = "901039999999999"
	// batteryReport is on external power, 80 % charged.
	batteryReport = "+CBC: 1,080"
	// registrationReport is registered on the home network, with
	// unsolicited registration reports off.
	registrationReport = "+CREG: 0,1"
	// maxSignalBars is five: Iridium handsets show no more.
	maxSignalBars = 5
	// dataCallInProgressReport and readyReport are what +CPAS reports while
	// a data call is up, and otherwise (ISU AT Command Reference).
	dataCallInProgressReport = "+CPAS: 004"
	readyReport              = "+CPAS: 000"
)

// execute runs c and sends its reply.
func (m *modem) execute(c command, now time.Time) {
	switch c.kind {
	case commandAttention:
		m.reply(okReply())
	case commandSetting:
		m.reply(m.configure(c.setting))
	case commandQuery:
		m.reply(okReply(m.report(c.query, now)))
	case commandSMS:
		m.reply(m.executeSMS(c.sms))
	case commandCall:
		if callReply, hasReply := m.executeCall(c.call, now); hasReply {
			m.reply(callReply)
		}
	default:
		panic(fmt.Sprintf("sim: a command of unknown kind %d", c.kind))
	}
}

// submitSMS sends a message typed after AT+CMGS.
func (m *modem) submitSMS(message outgoingSMS) {
	m.messageReference++ // A one-byte reference, which wraps as on the handset.
	logInfo(fmt.Sprintf("simulated handset: SMS to %s", message.number))
	m.stats.SMSSent = append(m.stats.SMSSent, SentSMS{Number: message.number, Text: message.text})
	m.reply(okReply(fmt.Sprintf("+CMGS: %d", m.messageReference)))
}

func (m *modem) configure(s setting) reply {
	switch s.kind {
	case settingReset:
		m.settings = defaultSettings()
	case settingEcho:
		m.settings.echo = s.on
	case settingBearer:
		m.settings.bearer = s.bearer
	case settingHangUpOnDTRDrop:
		m.settings.hangUpOnDTRDrop = s.on
	}
	return okReply()
}

// fixedReports are the answers of the queries that never change.
var fixedReports = map[query]string{
	queryManufacturer: manufacturer,
	queryRevision:     revision,
	queryIMSI:         imsi,
	queryBattery:      batteryReport,
	queryRegistration: registrationReport,
}

// report returns what q reports at now.
func (m *modem) report(q query, now time.Time) string {
	if fixed, ok := fixedReports[q]; ok {
		return fixed
	}
	switch q {
	case queryBearer:
		return fmt.Sprintf("+CBST: %v", m.settings.bearer)
	case queryModel:
		return m.config.Model
	case queryIMEI:
		return m.config.IMEI
	case querySignalQuality:
		return fmt.Sprintf("+CSQ:%d", m.signalBars())
	case queryFastSignalQuality:
		return fmt.Sprintf("+CSQF:%d", m.signalBars())
	case queryClock:
		return fmt.Sprintf(`+CCLK: "%v"`, timestampAt(now))
	case queryCallRelease:
		return fmt.Sprintf("+CEER: %v", m.lastRelease)
	case queryActivityStatus:
		return m.activityStatus()
	default:
		panic(fmt.Sprintf("sim: an unknown query %d", q))
	}
}

// activityStatus returns what +CPAS reports: whether a data call is up.
func (m *modem) activityStatus() string {
	if m.call.kind == callConnected {
		return dataCallInProgressReport
	}
	return readyReport
}

// signalBars returns the configured signal strength as the handset reports
// it: 0 to 5 bars.
func (m *modem) signalBars() int {
	return min(max(m.config.SignalBars, 0), maxSignalBars)
}

func (m *modem) executeSMS(c smsCommand) reply {
	switch c.kind {
	case smsSend:
		m.input = inputMode{kind: inputTextEntry, draft: newSMSDraft(c.number)}
		return reply{kind: replyTextPrompt}
	case smsList:
		return okReply(m.inbox.list(c.filter)...)
	case smsRead:
		lines, found := m.inbox.read(c.index)
		if !found {
			return smsErrorReply(smsErrorInvalidMemoryIndex)
		}
		return okReply(lines...)
	case smsDelete:
		if !m.inbox.remove(c.index) {
			return smsErrorReply(smsErrorInvalidMemoryIndex)
		}
		return okReply()
	default:
		panic(fmt.Sprintf("sim: an SMS command of unknown kind %d", c.kind))
	}
}

// executeCall runs a call command and returns its reply, if it has one now:
// false when the reply comes later, or has already been sent along with a
// change of mode.
func (m *modem) executeCall(c callCommand, now time.Time) (reply, bool) {
	switch c.kind {
	case callDial:
		return m.dial(c.number, c.callKind, now)
	case callAnswer:
		// Nothing ever rings on the simulated handset.
		return reply{kind: replyNoCarrier}, true
	case callHangUp:
		return m.hangUp(), true
	case callReturnOnline:
		return m.returnOnline(now)
	default:
		panic(fmt.Sprintf("sim: a call command of unknown kind %d", c.kind))
	}
}

func (m *modem) dial(number string, kind callKind, now time.Time) (reply, bool) {
	if m.call.kind != callIdle {
		// One call at a time; the data call held in online-command mode
		// comes first.
		return reply{kind: replyError}, true
	}
	m.stats.CallsPlaced++
	if kind == callVoice {
		logInfo(fmt.Sprintf("simulated handset: voice call to %s, without audio", number))
		m.stats.CallsConnected++
		return okReply(), true
	}
	m.startDataCall(number, now)
	return reply{}, false
}

func (m *modem) hangUp() reply {
	if m.call.kind == callConnected {
		logInfo("simulated handset: the app hung up")
		m.releaseCall(releaseClearedByApp)
	}
	return okReply()
}

func (m *modem) returnOnline(now time.Time) (reply, bool) {
	if m.call.kind != callConnected {
		return reply{kind: replyNoCarrier}, true
	}
	m.goOnline(now)
	return reply{}, false
}
