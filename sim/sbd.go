package sim

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type sbdModem struct {
	mo, mt, input                            []byte
	incoming, sent                           [][]byte
	expected                                 int
	writing, text, session, registered, ring bool
	momsn, mtmsn, status                     int
	due                                      time.Time
	delay                                    time.Duration
	drop, corrupt, unsupported               bool
}

// InjectSBD places a gateway message in the simulation and emits an alert
// when SBD registration and ring reporting are enabled.
func (c *Control) InjectSBD(p []byte) {
	c.shared.locked.use(func(m *modem, _ time.Time) {
		m.sbd.incoming = append(m.sbd.incoming, append([]byte(nil), p...))
		if m.sbd.registered && m.sbd.ring {
			m.toApp = append(m.toApp, framed("SBDRING")...)
		}
	})
}

// SentSBD returns independent copies of payloads accepted by the gateway.
func (c *Control) SentSBD() (result [][]byte) {
	c.shared.locked.use(func(m *modem, _ time.Time) {
		for _, p := range m.sbd.sent {
			result = append(result, append([]byte(nil), p...))
		}
	})
	return
}

// SetSBDOutcome controls subsequent sessions. drop loses the final response
// after gateway acceptance, allowing duplicate-delivery recovery tests.
func (c *Control) SetSBDOutcome(status int, delay time.Duration, drop bool) {
	c.shared.locked.use(func(m *modem, _ time.Time) { m.sbd.status = status; m.sbd.delay = delay; m.sbd.drop = drop })
}

// SetSBDFaults controls unsupported-command and incoming-checksum faults.
func (c *Control) SetSBDFaults(unsupported, corrupt bool) {
	c.shared.locked.use(func(m *modem, _ time.Time) { m.sbd.unsupported = unsupported; m.sbd.corrupt = corrupt })
}
func (m *modem) executeSBD(body string, now time.Time) bool {
	u := strings.ToUpper(body)
	if !strings.HasPrefix(u, "+SBD") {
		return false
	}
	if m.sbd.unsupported {
		m.reply(reply{kind: replyError})
		return true
	}
	if m.sbdCommand(u, now) {
		return true
	}
	if strings.HasPrefix(u, "+SBDWB=") {
		n, e := strconv.Atoi(body[7:])
		if e != nil || n < 1 || n > 1960 {
			m.reply(okReply("3"))
			return true
		}
		m.sbd.writing = true
		m.sbd.expected = n + 2
		m.sbd.input = nil
		m.sbd.due = now.Add(60 * time.Second)
		m.toApp = append(m.toApp, framed("READY")...)
		return true
	}
	if strings.HasPrefix(u, "+SBDWT=") {
		p := []byte(body[7:])
		if len(p) > 120 || len(p) == 0 {
			m.reply(reply{kind: replyError})
		} else {
			m.sbd.mo = append([]byte(nil), p...)
			m.reply(okReply())
		}
		return true
	}
	if strings.HasPrefix(u, "+SBDMTA=") {
		m.sbd.ring = strings.HasSuffix(u, "=1")
		m.reply(okReply())
		return true
	}
	m.reply(reply{kind: replyError})
	return true
}
func (m *modem) sbdCommand(u string, now time.Time) bool {
	switch u {
	case "+SBDSX":
		m.reply(okReply(fmt.Sprintf("+SBDSX: %d,%d,%d,%d,0,%d", flag(m.sbd.mo), m.sbd.momsn, flag(m.sbd.mt), m.sbd.mtmsn, len(m.sbd.incoming))))
	case "+SBDS":
		m.reply(okReply(fmt.Sprintf("+SBDS: %d,%d,%d,%d", flag(m.sbd.mo), m.sbd.momsn, flag(m.sbd.mt), m.sbd.mtmsn)))
	case "+SBDD0":
		m.sbd.mo = nil
		m.reply(okReply("0"))
	case "+SBDD1":
		m.sbd.mt = nil
		m.reply(okReply("0"))
	case "+SBDD2":
		m.sbd.mo = nil
		m.sbd.mt = nil
		m.reply(okReply("0"))
	case "+SBDTC":
		m.sbd.mt = append([]byte(nil), m.sbd.mo...)
		m.reply(okReply(fmt.Sprintf("SBDTC: Outbound SBD Copied to Inbound SBD: size = %d", len(m.sbd.mt))))
	case "+SBDRB":
		m.sbdRead()
	case "+SBDRT":
		m.reply(okReply("+SBDRT:", string(m.sbd.mt)))
	case "+SBDWT":
		m.sbd.writing = true
		m.sbd.text = true
		m.sbd.input = nil
		m.sbd.due = now.Add(60 * time.Second)
		m.toApp = append(m.toApp, framed("READY")...)
	case "+SBDIX", "+SBDIXA", "+SBDI":
		m.sbd.session = true
		m.sbd.text = u == "+SBDI"
		m.sbd.due = now.Add(m.sbd.delay + m.config.Link.CallSetup)
	case "+SBDREG":
		m.sbd.registered = true
		m.reply(okReply("+SBDREG: 2,0"))
	case "+SBDREG?":
		n := 0
		if m.sbd.registered {
			n = 2
		}
		m.reply(okReply(fmt.Sprintf("+SBDREG: %d", n)))
	case "+SBDDET":
		m.sbd.registered = false
		m.reply(okReply("0"))
	default:
		return false
	}
	return true
}
func flag(p []byte) int {
	if len(p) > 0 {
		return 1
	}
	return 0
}
func (m *modem) consumeSBD(b byte, _ time.Time) bool {
	if m.sbd.session {
		return true
	}
	if !m.sbd.writing {
		return false
	}
	m.sbd.input = append(m.sbd.input, b)
	if m.sbd.text {
		if b == '\r' {
			m.sbd.mo = append([]byte(nil), m.sbd.input[:len(m.sbd.input)-1]...)
			m.sbd.writing = false
			m.sbd.text = false
			m.reply(okReply("0"))
		}
		return true
	}
	if len(m.sbd.input) == m.sbd.expected {
		p := m.sbd.input
		n := len(p) - 2
		code := "0"
		if checksum(p[:n]) != binary.BigEndian.Uint16(p[n:]) {
			code = "2"
		} else {
			m.sbd.mo = append([]byte(nil), p[:n]...)
		}
		m.sbd.writing = false
		m.reply(okReply(code))
	}
	return true
}
func checksum(p []byte) (n uint16) {
	for _, b := range p {
		n += uint16(b)
	}
	return
}
func (m *modem) sbdRead() {
	wire := make([]byte, len(m.sbd.mt)+4)
	binary.BigEndian.PutUint16(wire, uint16(len(m.sbd.mt)))
	copy(wire[2:], m.sbd.mt)
	sum := checksum(m.sbd.mt)
	if m.sbd.corrupt {
		sum++
	}
	binary.BigEndian.PutUint16(wire[len(wire)-2:], sum)
	m.toApp = append(m.toApp, wire...)
	m.toApp = append(m.toApp, framed("OK")...)
}
func (m *modem) advanceSBD(now time.Time) {
	if now.Before(m.sbd.due) {
		return
	}
	if m.sbd.writing {
		m.sbd.writing = false
		m.sbd.text = false
		m.toApp = append(m.toApp, framed("1")...)
	}
	if !m.sbd.session {
		return
	}
	m.sbd.session = false
	status := m.sbd.status
	mt := 0
	if status >= 0 && status <= 4 {
		m.sbd.registered = true
		if len(m.sbd.mo) > 0 {
			m.sbd.sent = append(m.sbd.sent, append([]byte(nil), m.sbd.mo...))
			m.sbd.momsn = (m.sbd.momsn + 1) % 65536
		}
		if len(m.sbd.incoming) > 0 {
			m.sbd.mt = m.sbd.incoming[0]
			m.sbd.incoming = m.sbd.incoming[1:]
			m.sbd.mtmsn = (m.sbd.mtmsn + 1) % 65536
			mt = 1
		}
	}
	if m.sbd.drop {
		return
	}
	prefix := "+SBDIX"
	if m.sbd.text {
		prefix = "+SBDI"
		if status <= 4 {
			status = 1
		} else {
			status = 2
		}
	}
	m.reply(okReply(fmt.Sprintf("%s: %d,%d,%d,%d,%d,%d", prefix, status, m.sbd.momsn, mt, m.sbd.mtmsn, len(m.sbd.mt), len(m.sbd.incoming))))
}

// OpenTransport opens another port handle onto the same simulated phone.
// Only one handle may be used at a time. Modem buffers survive closing a port.
func (c *Control) OpenTransport() *Handset { return &Handset{shared: c.shared} }

func sbdCommandBody(text string) string {
	start := strings.Index(strings.ToUpper(text), "AT")
	if start < 0 {
		return ""
	}
	return strings.TrimLeft(text[start+2:], " ")
}
