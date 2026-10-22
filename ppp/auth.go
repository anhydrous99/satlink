package ppp

import (
	"crypto/md5"
	"crypto/subtle"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/anhydrous99/satlink/internal/utf8lossy"
)

// The authentication phase: PAP (RFC 1334) and CHAP with MD5 (RFC 1994).

// challengeLen is the size of the CHAP challenges we issue, in bytes.
const challengeLen = 16

// authenticatorName is the name we put in our CHAP challenges.
const authenticatorName = "satlink-ppp"

// lengthPrefixLen is the one-octet length in front of the names, passwords,
// challenges and messages PAP and CHAP carry.
const lengthPrefixLen = 1

// PAP packet codes (RFC 1334 §2.2).
const (
	papRequest code = 1
	papAck     code = 2
	papNak     code = 3
)

// CHAP packet codes (RFC 1994 §4).
const (
	chapChallenge code = 1
	chapResponse  code = 2
	chapSuccess   code = 3
	chapFailure   code = 4
)

// The messages our authenticator sends with its verdict.
const (
	welcomeMessage = "Welcome"
	deniedMessage  = "Access denied"
)

// outcomeKind classifies an outcome.
type outcomeKind int

// How an authentication exchange can stand.
const (
	// outcomeUndecided means that the exchange has not ended yet.
	outcomeUndecided outcomeKind = iota
	outcomeSucceeded
	outcomeFailed
)

// outcome is how an authentication exchange ended.
type outcome struct {
	kind outcomeKind
	// reason is why authentication failed (outcomeFailed).
	reason string
}

// The outcomes without a payload. They are never assigned to.
var (
	undecided = outcome{kind: outcomeUndecided}
	succeeded = outcome{kind: outcomeSucceeded}
)

// failed reports a failed authentication, and why.
func failed(reason string) outcome {
	return outcome{kind: outcomeFailed, reason: reason}
}

// protocolNumber returns the number of the PPP protocol p's packets travel
// under.
func (p AuthProtocol) protocolNumber() uint16 {
	if p == AuthProtocolCHAPMD5 {
		return protocolCHAP
	}
	return protocolPAP
}

// negotiationTime returns a whole negotiation's worth of time: Max-Configure
// restart periods.
func (t Timers) negotiationTime() time.Duration {
	return scaled(t.Restart, t.MaxConfigure)
}

// authExchange is the authentication exchange for one direction of the
// link: an authenticatee proves our identity (client role), an
// authenticator checks the peer's (server role).
type authExchange interface {
	// protocolNumber returns the number of the protocol the exchange
	// speaks.
	protocolNumber() uint16
	// start begins the exchange.
	start(now time.Time, out *outbox)
	// receive handles one packet of the exchange's protocol.
	receive(received packet, now time.Time, out *outbox) outcome
	// handleTimeout retransmits or gives up once the exchange's timer has
	// expired.
	handleTimeout(now time.Time, out *outbox) outcome
	// nextTimeout returns when handleTimeout next needs to run; the zero
	// Time when no timer is running.
	nextTimeout() time.Time
}

// authenticatee proves our identity to a peer that demanded it.
type authenticatee struct {
	protocol    AuthProtocol
	credentials Credentials
	timers      Timers
	// identifier is that of our PAP request, or of the challenge we last
	// answered.
	identifier uint8
	// chapResponse is our last CHAP Response, repeated until the
	// authenticator answers it: its Success may be lost on the line (RFC
	// 1994 §4.1). nil when there is none.
	chapResponse *packet
	retriesLeft  int
	// deadline is when to retransmit or give up; the zero Time when no
	// timer is running.
	deadline time.Time
	// accepted is set once the authenticator has accepted us, so that a
	// repeated Success is not reported again.
	accepted bool
}

// newAuthenticatee returns an exchange that proves our identity with
// credentials over protocol.
func newAuthenticatee(protocol AuthProtocol, credentials Credentials, timers Timers) *authenticatee {
	return &authenticatee{
		protocol:    protocol,
		credentials: credentials,
		timers:      timers,
		retriesLeft: timers.MaxConfigure,
	}
}

// protocolNumber returns the number of the protocol we prove our identity
// with.
func (a *authenticatee) protocolNumber() uint16 {
	return a.protocol.protocolNumber()
}

// nextTimeout returns when to retransmit or give up; the zero Time when no
// timer is running.
func (a *authenticatee) nextTimeout() time.Time {
	return a.deadline
}

// start sends our PAP request, or starts waiting for the CHAP challenge.
func (a *authenticatee) start(now time.Time, out *outbox) {
	if a.protocol == AuthProtocolPAP {
		a.sendPAPRequest(now, out)
		return
	}
	// The authenticator speaks first; allow it a whole negotiation's worth
	// of time.
	a.deadline = now.Add(a.timers.negotiationTime())
}

// receive answers a CHAP challenge, or reads the authenticator's decision.
func (a *authenticatee) receive(received packet, now time.Time, out *outbox) outcome {
	if a.protocol == AuthProtocolCHAPMD5 && received.code == chapChallenge {
		a.answerChallenge(received, now, out)
		return undecided
	}
	result := a.outcomeOf(received)
	if result.kind == outcomeUndecided {
		return undecided
	}
	a.deadline = time.Time{}
	a.chapResponse = nil
	if result.kind == outcomeSucceeded {
		if a.accepted {
			return undecided // a duplicate, or the answer to a re-challenge
		}
		a.accepted = true
	}
	return result
}

// outcomeOf returns the authenticator's decision reply carries, if it
// carries one.
func (a *authenticatee) outcomeOf(reply packet) outcome {
	if a.protocol == AuthProtocolPAP {
		return papOutcome(reply)
	}
	if reply.identifier != a.identifier {
		return undecided
	}
	return chapOutcome(reply)
}

// papOutcome reads a PAP reply. Any reply counts: it may answer an earlier
// copy of the request.
func papOutcome(reply packet) outcome {
	switch reply.code {
	case papAck:
		return succeeded
	case papNak:
		return failed(papMessage(reply.data))
	default:
		return undecided
	}
}

// chapOutcome reads a CHAP reply to the challenge we answered.
func chapOutcome(reply packet) outcome {
	switch reply.code {
	case chapSuccess:
		return succeeded
	case chapFailure:
		return failed(readable(reply.data))
	default:
		return undecided
	}
}

// answerChallenge sends the Response to a CHAP challenge.
func (a *authenticatee) answerChallenge(challenge packet, now time.Time, out *outbox) {
	value, _, ok := splitLengthPrefixed(challenge.data)
	if !ok {
		return
	}
	a.identifier = challenge.identifier
	digest := chapDigest(challenge.identifier, []byte(a.credentials.Password), value)
	a.chapResponse = &packet{
		code:       chapResponse,
		identifier: challenge.identifier,
		data:       lengthPrefixedThen(digest[:], []byte(a.credentials.Username)),
	}
	a.retriesLeft = a.timers.MaxConfigure
	a.sendCHAPResponse(now, out)
}

// handleTimeout repeats our request or response, or gives up once the
// retries have run out or the challenge never came.
func (a *authenticatee) handleTimeout(now time.Time, out *outbox) outcome {
	if !isDue(a.deadline, now) {
		return undecided
	}
	if a.retriesLeft > 0 && a.hasSomethingToRepeat() {
		a.repeat(now, out)
		return undecided
	}
	a.deadline = time.Time{}
	a.chapResponse = nil
	return failed("the peer never completed authentication")
}

// hasSomethingToRepeat reports whether a request or response of ours
// awaits an answer. A CHAP authenticatee still waiting for the challenge
// has none: the authenticator speaks first.
func (a *authenticatee) hasSomethingToRepeat() bool {
	return a.protocol == AuthProtocolPAP || a.chapResponse != nil
}

// repeat sends our PAP request or CHAP response again.
func (a *authenticatee) repeat(now time.Time, out *outbox) {
	if a.protocol == AuthProtocolPAP {
		a.sendPAPRequest(now, out)
		return
	}
	a.sendCHAPResponse(now, out)
}

// sendCHAPResponse (re)sends our CHAP Response, which must be set; it keeps
// the challenge's identifier.
func (a *authenticatee) sendCHAPResponse(now time.Time, out *outbox) {
	out.push(protocolCHAP, *a.chapResponse)
	a.countTransmission(now)
}

// sendPAPRequest sends our name and password. Every transmission carries a
// new identifier (RFC 1334 §2.2.1).
func (a *authenticatee) sendPAPRequest(now time.Time, out *outbox) {
	a.identifier++
	password := lengthPrefixedThen([]byte(a.credentials.Password), nil)
	request := lengthPrefixedThen([]byte(a.credentials.Username), password)
	out.push(protocolPAP, packet{code: papRequest, identifier: a.identifier, data: request})
	a.countTransmission(now)
}

// countTransmission uses up one retry and restarts the timer.
func (a *authenticatee) countTransmission(now time.Time) {
	a.retriesLeft = max(a.retriesLeft-1, 0)
	a.deadline = now.Add(a.timers.Restart)
}

// authenticator checks the identity of a peer that agreed to authenticate.
type authenticator struct {
	protocol    AuthProtocol
	expected    Credentials
	timers      Timers
	identifier  uint8
	challenge   []byte
	retriesLeft int
	// deadline is when to retransmit or give up; the zero Time when no
	// timer is running.
	deadline time.Time
	// decided is set once the peer was accepted or refused, which accepted
	// then says; later requests get the same answer.
	decided  bool
	accepted bool
}

// newAuthenticator returns an exchange that checks that the peer proves
// its identity with the expected credentials over protocol.
func newAuthenticator(protocol AuthProtocol, expected Credentials, timers Timers) *authenticator {
	return &authenticator{
		protocol:    protocol,
		expected:    expected,
		timers:      timers,
		challenge:   randomBytes(challengeLen),
		retriesLeft: timers.MaxConfigure,
	}
}

// protocolNumber returns the number of the protocol the peer proves its
// identity with.
func (a *authenticator) protocolNumber() uint16 {
	return a.protocol.protocolNumber()
}

// nextTimeout returns when to retransmit or give up; the zero Time when no
// timer is running.
func (a *authenticator) nextTimeout() time.Time {
	return a.deadline
}

// start sends our CHAP challenge, or starts waiting for the PAP request.
func (a *authenticator) start(now time.Time, out *outbox) {
	if a.protocol == AuthProtocolPAP {
		// The peer speaks first; allow it a whole negotiation's worth of
		// time.
		a.deadline = now.Add(a.timers.negotiationTime())
		return
	}
	a.identifier++
	a.sendChallenge(now, out)
}

// receive answers the peer's attempt to authenticate, and reports the
// decision the first time.
func (a *authenticator) receive(received packet, _ time.Time, out *outbox) outcome {
	accepted, isAttempt := a.check(received)
	if !isAttempt {
		return undecided
	}
	// A retransmitted request means that our answer was lost: repeat it,
	// but decide only once.
	firstDecision := !a.decided
	if firstDecision {
		a.decided, a.accepted = true, accepted
	}
	a.answer(received.identifier, out)
	a.deadline = time.Time{}
	switch {
	case !firstDecision:
		return undecided
	case a.accepted:
		return succeeded
	default:
		return failed("the peer sent wrong credentials")
	}
}

// check reports whether received, if it is the peer's attempt to
// authenticate — a PAP request, or the response to our challenge —
// presents the expected credentials.
func (a *authenticator) check(received packet) (accepted, isAttempt bool) {
	switch {
	case a.protocol == AuthProtocolPAP && received.code == papRequest:
		return a.acceptsPAPRequest(received.data), true
	case a.protocol == AuthProtocolCHAPMD5 && received.code == chapResponse && received.identifier == a.identifier:
		return a.acceptsCHAPResponse(received.data), true
	default:
		return false, false
	}
}

// acceptsPAPRequest reports whether a PAP request carries the expected
// name and password.
func (a *authenticator) acceptsPAPRequest(data []byte) bool {
	username, rest, ok := splitLengthPrefixed(data)
	if !ok {
		return false
	}
	password, _, ok := splitLengthPrefixed(rest)
	if !ok {
		return false
	}
	return string(username) == a.expected.Username &&
		subtle.ConstantTimeCompare(password, []byte(a.expected.Password)) == 1
}

// acceptsCHAPResponse reports whether a CHAP response carries the expected
// name and the digest only the shared secret gives.
func (a *authenticator) acceptsCHAPResponse(data []byte) bool {
	expected := chapDigest(a.identifier, []byte(a.expected.Password), a.challenge)
	digest, name, ok := splitLengthPrefixed(data)
	return ok && string(name) == a.expected.Username && subtle.ConstantTimeCompare(digest, expected[:]) == 1
}

// answer sends our verdict on the attempt with identifier.
func (a *authenticator) answer(identifier uint8, out *outbox) {
	if a.protocol == AuthProtocolPAP {
		out.push(protocolPAP, a.papAnswer(identifier))
		return
	}
	out.push(protocolCHAP, a.chapAnswer(identifier))
}

// papAnswer returns a PAP Ack or Nak, whose message is length-prefixed.
func (a *authenticator) papAnswer(identifier uint8) packet {
	if a.accepted {
		return packet{code: papAck, identifier: identifier, data: lengthPrefixedThen([]byte(welcomeMessage), nil)}
	}
	return packet{code: papNak, identifier: identifier, data: lengthPrefixedThen([]byte(deniedMessage), nil)}
}

// chapAnswer returns a CHAP Success or Failure, whose message fills the
// packet.
func (a *authenticator) chapAnswer(identifier uint8) packet {
	if a.accepted {
		return packet{code: chapSuccess, identifier: identifier, data: []byte(welcomeMessage)}
	}
	return packet{code: chapFailure, identifier: identifier, data: []byte(deniedMessage)}
}

// handleTimeout repeats our challenge, or gives up once the retries have
// run out or the PAP request never came.
func (a *authenticator) handleTimeout(now time.Time, out *outbox) outcome {
	if !isDue(a.deadline, now) {
		return undecided
	}
	if a.protocol == AuthProtocolCHAPMD5 && a.retriesLeft > 0 {
		a.sendChallenge(now, out)
		return undecided
	}
	a.deadline = time.Time{}
	return failed("the peer never authenticated")
}

// sendChallenge (re)sends our CHAP challenge.
func (a *authenticator) sendChallenge(now time.Time, out *outbox) {
	data := lengthPrefixedThen(a.challenge, []byte(authenticatorName))
	out.push(protocolCHAP, packet{code: chapChallenge, identifier: a.identifier, data: data})
	a.retriesLeft = max(a.retriesLeft-1, 0)
	a.deadline = now.Add(a.timers.Restart)
}

// chapDigest returns MD5 over the identifier, the shared secret and the
// challenge (RFC 1994 §4.1).
func chapDigest(identifier uint8, secret, challenge []byte) [md5.Size]byte {
	message := append([]byte{identifier}, secret...)
	return md5.Sum(append(message, challenge...))
}

// lengthPrefixedThen returns value behind a one-octet length, followed by
// rest.
func lengthPrefixedThen(value, rest []byte) []byte {
	if len(value) > math.MaxUint8 {
		panic("credentials are validated to fit one octet")
	}
	prefixed := make([]byte, 0, lengthPrefixLen+len(value)+len(rest))
	prefixed = append(prefixed, byte(len(value)))
	prefixed = append(prefixed, value...)
	return append(prefixed, rest...)
}

// splitLengthPrefixed returns the value behind data's one-octet length and
// what follows it, and false when data is shorter than its length says.
func splitLengthPrefixed(data []byte) (value, rest []byte, ok bool) {
	if len(data) < lengthPrefixLen {
		return nil, nil, false
	}
	end := lengthPrefixLen + int(data[0])
	if end > len(data) {
		return nil, nil, false
	}
	return data[lengthPrefixLen:end], data[end:], true
}

// papMessage returns the readable message of a PAP Ack or Nak, empty when
// it has none.
func papMessage(data []byte) string {
	message, _, ok := splitLengthPrefixed(data)
	if !ok {
		return ""
	}
	return readable(message)
}

// readable returns peer-supplied text made safe to show to a user: invalid
// UTF-8 replaced by U+FFFD, control
// characters dropped and surrounding white space trimmed.
func readable(text []byte) string {
	return strings.TrimSpace(strings.Map(withoutControl, utf8lossy.String(text)))
}

// withoutControl drops a control character, for strings.Map, and keeps
// every other character.
func withoutControl(character rune) rune {
	if unicode.IsControl(character) {
		return -1
	}
	return character
}
