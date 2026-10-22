package ppp

import (
	"bytes"
	"crypto/md5"
	"testing"
)

// testCredentials are what the authentication tests' peers present.
var testCredentials = Credentials{Username: "user", Password: "pass"}

// sequence returns the bytes from first up to last, inclusive.
func sequence(first, last byte) []byte {
	var values []byte
	for value := int(first); value <= int(last); value++ {
		values = append(values, byte(value))
	}
	return values
}

// assertOutcome fails the test unless got is want.
func assertOutcome(t *testing.T, got, want outcome) {
	t.Helper()
	if got != want {
		t.Fatalf("outcome = %+v, want %+v", got, want)
	}
}

// assertNoTimerRuns fails the test if exchange still runs a timer.
func assertNoTimerRuns(t *testing.T, exchange authExchange) {
	t.Helper()
	if deadline := exchange.nextTimeout(); !deadline.IsZero() {
		t.Errorf("a timer still runs until %v", deadline)
	}
}

// onTheLine returns a control packet as the line carries it: its header,
// spelled as hex octets, then data.
func onTheLine(tb testing.TB, header, data string) []byte {
	tb.Helper()
	return append(hexBytes(tb, header), data...)
}

// mustParsePacket returns the control packet information holds, failing the
// test when it holds none.
func mustParsePacket(t *testing.T, information []byte) packet {
	t.Helper()
	received, ok := parsePacket(information)
	if !ok {
		t.Fatalf("% X is not a control packet", information)
	}
	return received
}

// assertSent empties out, failing the test unless it held one packet, of
// protocol, whose octets are want.
func assertSent(t *testing.T, out *outbox, protocol uint16, want []byte) {
	t.Helper()
	sent := takeOnly(t, out)
	if got := sent.packet.encode(); sent.protocol != protocol || !bytes.Equal(got, want) {
		t.Fatalf("sent % X for protocol %#04x, want % X for %#04x", got, sent.protocol, want, protocol)
	}
}

// The two tests below write the PAP and CHAP codes as RFC 1334 §2.2 and RFC
// 1994 §4 number them, so that they check auth.go's constants instead of
// trusting them: both ends of every other test share those constants, and
// a real peer would not.

func TestPAPSpeaksRFC1334sCodes(t *testing.T) {
	tests := []struct {
		name     string
		password string
		// request is the Authenticate-Request (1); answer is the
		// Authenticate-Ack (2) or -Nak (3) to it.
		request, answer []byte
		want            outcome
	}{
		{"the expected credentials", "pass", onTheLine(t, "01 01 00 0E", "\x04user\x04pass"),
			onTheLine(t, "02 01 00 0C", "\x07Welcome"), succeeded},
		{"a wrong password", "guess", onTheLine(t, "01 01 00 0F", "\x04user\x05guess"),
			onTheLine(t, "03 01 00 12", "\x0DAccess denied"), failed("Access denied")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticatee := newAuthenticatee(AuthProtocolPAP, Credentials{Username: "user", Password: test.password}, DefaultTimers())
			authenticator := newAuthenticator(AuthProtocolPAP, testCredentials, DefaultTimers())
			var out outbox
			authenticator.start(testStart, &out)

			authenticatee.start(testStart, &out)
			assertSent(t, &out, protocolPAP, test.request)
			authenticator.receive(mustParsePacket(t, test.request), testStart, &out)
			assertSent(t, &out, protocolPAP, test.answer)
			got := authenticatee.receive(mustParsePacket(t, test.answer), testStart, &out)

			assertOutcome(t, got, test.want)
		})
	}
}

func TestCHAPSpeaksRFC1994sCodes(t *testing.T) {
	tests := []struct {
		name     string
		password string
		// verdict is the Success (3) or Failure (4) answering the Response.
		verdict []byte
		want    outcome
	}{
		{"the shared secret", "pass", onTheLine(t, "03 01 00 0B", "Welcome"), succeeded},
		{"a wrong secret", "guess", onTheLine(t, "04 01 00 11", "Access denied"), failed("Access denied")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticatee := newAuthenticatee(AuthProtocolCHAPMD5, Credentials{Username: "user", Password: test.password}, DefaultTimers())
			authenticator := newAuthenticator(AuthProtocolCHAPMD5, testCredentials, DefaultTimers())
			var out outbox
			authenticatee.start(testStart, &out)

			// The Challenge (1) carries its 16-octet value, then the
			// authenticator's name; the Response (2), the digest of the
			// identifier, the secret and the value, then the peer's name.
			authenticator.start(testStart, &out)
			challenge := onTheLine(t, "01 01 00 20 10", string(authenticator.challenge)+"satlink-ppp")
			assertSent(t, &out, protocolCHAP, challenge)
			authenticatee.receive(mustParsePacket(t, challenge), testStart, &out)
			digest := chapDigest(1, []byte(test.password), authenticator.challenge)
			response := onTheLine(t, "02 01 00 19 10", string(digest[:])+"user")
			assertSent(t, &out, protocolCHAP, response)
			authenticator.receive(mustParsePacket(t, response), testStart, &out)
			assertSent(t, &out, protocolCHAP, test.verdict)
			got := authenticatee.receive(mustParsePacket(t, test.verdict), testStart, &out)

			assertOutcome(t, got, test.want)
		})
	}
}

func TestCHAPDigestMatchesRFC1994(t *testing.T) {
	// MD5(0x01 || "secret" || 00 01 .. 0f), computed independently.
	challenge := sequence(0x00, 0x0F)
	independent := md5.New()
	for _, part := range [][]byte{{1}, []byte("secret"), challenge} {
		_, _ = independent.Write(part) // a hash.Hash never returns an error
	}

	digest := chapDigest(1, []byte("secret"), challenge)

	if want := independent.Sum(nil); !bytes.Equal(digest[:], want) {
		t.Errorf("digest = % X, want % X", digest, want)
	}
}

func TestCHAPDigestMatchesTheReferenceVector(t *testing.T) {
	digest := chapDigest(0x2A, []byte("s3cr3t"), sequence(0x10, 0x1F))

	want := []byte{0xFE, 0x94, 0xD3, 0x56, 0x2C, 0xAF, 0xDB, 0x64, 0x24, 0xD3, 0x7E, 0xFC, 0x02, 0x1B, 0x49, 0xC8}
	if !bytes.Equal(digest[:], want) {
		t.Errorf("digest = % X, want % X", digest, want)
	}
}

func TestPAPRequestMatchesTheReferenceVector(t *testing.T) {
	authenticatee := newAuthenticatee(AuthProtocolPAP, testCredentials, DefaultTimers())
	var out outbox

	authenticatee.start(testStart, &out)

	sent := takeOnly(t, &out)
	if sent.protocol != protocolPAP || sent.packet.code != papRequest {
		t.Fatalf("sent %+v, want a PAP Authenticate-Request", sent)
	}
	if want := []byte("\x04user\x04pass"); !bytes.Equal(sent.packet.data, want) {
		t.Errorf("request data = %q, want %q", sent.packet.data, want)
	}
}

func TestACHAPResponseIsRepeatedUntilAnswered(t *testing.T) {
	timers := DefaultTimers()
	now := testStart
	authenticatee := newAuthenticatee(AuthProtocolCHAPMD5, testCredentials, timers)
	var out outbox
	authenticatee.start(now, &out)
	challenge := packet{code: chapChallenge, identifier: 9, data: lengthPrefixedThen(bytes.Repeat([]byte{7}, 16), []byte("gw"))}

	authenticatee.receive(challenge, now, &out)
	response := takeFirst(t, &out)
	now = now.Add(timers.Restart)
	assertOutcome(t, authenticatee.handleTimeout(now, &out), undecided)
	// The Success was lost: say it again.
	assertOutgoing(t, out.take(), []outgoingPacket{{protocolCHAP, response}})

	success := packet{code: chapSuccess, identifier: 9}
	assertOutcome(t, authenticatee.receive(success, now, &out), succeeded)
	assertNoTimerRuns(t, authenticatee)
}

func TestReadableStripsControlCharacters(t *testing.T) {
	if got := readable([]byte("\x1b[31mDenied\r\n")); got != "[31mDenied" {
		t.Errorf("readable = %q, want %q", got, "[31mDenied")
	}
}

func TestLengthPrefixedFieldsRoundTrip(t *testing.T) {
	encoded := lengthPrefixedThen([]byte("user"), []byte("tail"))

	value, rest, ok := splitLengthPrefixed(encoded)
	if !ok || string(value) != "user" || string(rest) != "tail" {
		t.Errorf("split = %q, %q, %t; want \"user\", \"tail\", true", value, rest, ok)
	}
	if value, rest, ok := splitLengthPrefixed([]byte{5, 1, 2}); ok {
		t.Errorf("a field shorter than its length split as %q, %q", value, rest)
	}
	if _, _, ok := splitLengthPrefixed(nil); ok {
		t.Error("nothing split as a field")
	}
}

func TestReadableReplacesInvalidUTF8AsRustDoes(t *testing.T) {
	// The expected texts are what Rust's readable, built on
	// String::from_utf8_lossy, returned for the same bytes.
	tests := []struct {
		text []byte
		want string
	}{
		{[]byte("\xE2\x82A\xFFB"), "�A�B"},
		{[]byte("\xF0\x9F\x98"), "�"},
		{[]byte("\xED\xA0\x80x"), "���x"},
		{[]byte("\xC0\xAFz"), "��z"},
		{[]byte("\xE0\x80\xAF"), "���"},
		{[]byte("\xF4\x90\x80\x80"), "����"},
		{[]byte("ok\xF0\x9F\x98\x80!"), "ok\U0001F600!"},
		{[]byte("  \x00 x \x7F\xC2\x85 "), "x"},
		{[]byte("\xEF\xBF\xBD"), "�"},
	}
	for _, test := range tests {
		if got := readable(test.text); got != test.want {
			t.Errorf("readable(% X) = %q, want %q", test.text, got, test.want)
		}
	}
}

func TestAPAPAuthenticatorAcceptsTheExpectedCredentialsOnly(t *testing.T) {
	welcome := packet{code: papAck, identifier: 1, data: []byte("\x07Welcome")}
	denied := packet{code: papNak, identifier: 1, data: []byte("\x0DAccess denied")}
	refused := failed("the peer sent wrong credentials")
	tests := []struct {
		name    string
		request []byte
		want    outcome
		answer  packet
	}{
		{"the expected credentials", []byte("\x04user\x04pass"), succeeded, welcome},
		{"a wrong password", []byte("\x04user\x05guess"), refused, denied},
		{"a wrong name", []byte("\x05pilot\x04pass"), refused, denied},
		{"a password cut short", []byte("\x04user\x09pa"), refused, denied},
		{"no password", []byte("\x04user"), refused, denied},
		{"nothing", nil, refused, denied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := newAuthenticator(AuthProtocolPAP, testCredentials, DefaultTimers())
			var out outbox
			authenticator.start(testStart, &out)

			got := authenticator.receive(packet{code: papRequest, identifier: 1, data: test.request}, testStart, &out)

			assertOutcome(t, got, test.want)
			assertOutgoing(t, out.take(), []outgoingPacket{{protocolPAP, test.answer}})
			// The endpoint keeps the exchange for the life of the link: a
			// timer left running would end the link later.
			assertNoTimerRuns(t, authenticator)
		})
	}
}

func TestAnAuthenticatorDecidesOnceButAnswersEveryRetransmission(t *testing.T) {
	authenticator := newAuthenticator(AuthProtocolPAP, testCredentials, DefaultTimers())
	var out outbox
	authenticator.start(testStart, &out)
	request := packet{code: papRequest, identifier: 1, data: []byte("\x04user\x04pass")}
	assertOutcome(t, authenticator.receive(request, testStart, &out), succeeded)
	out.take()

	retransmitted := packet{code: papRequest, identifier: 2, data: []byte("\x04user\x05wrong")}
	got := authenticator.receive(retransmitted, testStart, &out)

	assertOutcome(t, got, undecided)
	ack := packet{code: papAck, identifier: 2, data: []byte("\x07Welcome")}
	assertOutgoing(t, out.take(), []outgoingPacket{{protocolPAP, ack}})
}

func TestCHAPAcceptsTheExpectedNameAndSecretOnly(t *testing.T) {
	tests := []struct {
		name  string
		peer  Credentials
		want  outcome
		reply code
	}{
		{"the shared secret", testCredentials, succeeded, chapSuccess},
		{"a wrong secret", Credentials{Username: "user", Password: "guess"}, failed("Access denied"), chapFailure},
		{"the shared secret under another name", Credentials{Username: "pilot", Password: "pass"}, failed("Access denied"), chapFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := newAuthenticator(AuthProtocolCHAPMD5, testCredentials, DefaultTimers())
			authenticatee := newAuthenticatee(AuthProtocolCHAPMD5, test.peer, DefaultTimers())
			var out outbox
			authenticatee.start(testStart, &out)
			authenticator.start(testStart, &out)

			authenticatee.receive(takeFirst(t, &out), testStart, &out)
			authenticator.receive(takeFirst(t, &out), testStart, &out)
			reply := takeFirst(t, &out)
			got := authenticatee.receive(reply, testStart, &out)

			assertOutcome(t, got, test.want)
			// PAP and CHAP codes are numbers of their own: print them as such.
			if reply.code != test.reply {
				t.Errorf("reply = %d, want %d", reply.code, test.reply)
			}
			assertNoTimerRuns(t, authenticator) // no challenge is repeated
		})
	}
}

func TestACHAPAuthenticatorIgnoresResponsesToOtherChallenges(t *testing.T) {
	timers := DefaultTimers()
	authenticator := newAuthenticator(AuthProtocolCHAPMD5, testCredentials, timers)
	var out outbox
	authenticator.start(testStart, &out)
	challenge := takeFirst(t, &out)
	response := chapResponseTo(t, challenge)
	stray := response
	stray.identifier++

	assertOutcome(t, authenticator.receive(stray, testStart, &out), undecided)
	if sent := out.take(); len(sent) != 0 {
		t.Errorf("a Response to another challenge was answered with %+v", sent)
	}
	if want := testStart.Add(timers.Restart); authenticator.nextTimeout() != want {
		t.Errorf("next timeout = %v, want the challenge's %v", authenticator.nextTimeout(), want)
	}

	assertOutcome(t, authenticator.receive(response, testStart, &out), succeeded)
}

// chapResponseTo returns the Response that a peer with testCredentials
// sends to challenge.
func chapResponseTo(t *testing.T, challenge packet) packet {
	t.Helper()
	authenticatee := newAuthenticatee(AuthProtocolCHAPMD5, testCredentials, DefaultTimers())
	var out outbox
	authenticatee.start(testStart, &out)
	authenticatee.receive(challenge, testStart, &out)
	return takeFirst(t, &out)
}

func TestAChallengeNamesTheAuthenticator(t *testing.T) {
	authenticator := newAuthenticator(AuthProtocolCHAPMD5, testCredentials, DefaultTimers())
	var out outbox

	authenticator.start(testStart, &out)

	challenge := takeOnly(t, &out)
	value, name, ok := splitLengthPrefixed(challenge.packet.data)
	if !ok || len(value) != challengeLen || string(name) != authenticatorName {
		t.Errorf("challenge data = % X, want a %d-byte value and the name %q", challenge.packet.data, challengeLen, authenticatorName)
	}
}

func TestAPAPAuthenticateeReportsTheNaksMessage(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"a message", []byte("\x0DAccess denied"), "Access denied"},
		{"a message with control characters", []byte("\x09\x1b[1mNo!\r\n"), "[1mNo!"},
		{"a message cut short", []byte("\x0DAccess"), ""},
		{"no message", nil, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticatee := newAuthenticatee(AuthProtocolPAP, testCredentials, DefaultTimers())
			var out outbox
			authenticatee.start(testStart, &out)

			got := authenticatee.receive(packet{code: papNak, identifier: 7, data: test.data}, testStart, &out)

			assertOutcome(t, got, failed(test.want))
		})
	}
}

func TestADuplicateSuccessIsReportedOnce(t *testing.T) {
	authenticatee := newAuthenticatee(AuthProtocolPAP, testCredentials, DefaultTimers())
	var out outbox
	authenticatee.start(testStart, &out)
	ack := packet{code: papAck, identifier: 1}

	assertOutcome(t, authenticatee.receive(ack, testStart, &out), succeeded)
	assertOutcome(t, authenticatee.receive(ack, testStart, &out), undecided)
}

func TestAFailureIsReportedEvenAfterASuccess(t *testing.T) {
	authenticatee := newAuthenticatee(AuthProtocolCHAPMD5, testCredentials, DefaultTimers())
	var out outbox
	authenticatee.start(testStart, &out)
	authenticatee.receive(packet{code: chapChallenge, identifier: 1, data: []byte{1, 0xAA}}, testStart, &out)
	assertOutcome(t, authenticatee.receive(packet{code: chapSuccess, identifier: 1}, testStart, &out), succeeded)

	// The authenticator may challenge again at any time (RFC 1994 §2.2).
	authenticatee.receive(packet{code: chapChallenge, identifier: 2, data: []byte{1, 0xBB}}, testStart, &out)
	got := authenticatee.receive(packet{code: chapFailure, identifier: 2, data: []byte("no")}, testStart, &out)

	assertOutcome(t, got, failed("no"))
}

func TestACHAPAuthenticateeIgnoresRepliesToOtherChallenges(t *testing.T) {
	authenticatee := newAuthenticatee(AuthProtocolCHAPMD5, testCredentials, DefaultTimers())
	var out outbox
	authenticatee.start(testStart, &out)
	authenticatee.receive(packet{code: chapChallenge, identifier: 3, data: []byte{1, 0xAA}}, testStart, &out)

	assertOutcome(t, authenticatee.receive(packet{code: chapSuccess, identifier: 4}, testStart, &out), undecided)
	assertOutcome(t, authenticatee.receive(packet{code: chapResponse, identifier: 3}, testStart, &out), undecided)
	assertOutcome(t, authenticatee.receive(packet{code: chapFailure, identifier: 3, data: []byte("no")}, testStart, &out), failed("no"))
}

func TestAMalformedChallengeIsIgnored(t *testing.T) {
	authenticatee := newAuthenticatee(AuthProtocolCHAPMD5, testCredentials, DefaultTimers())
	var out outbox
	authenticatee.start(testStart, &out)

	authenticatee.receive(packet{code: chapChallenge, identifier: 3, data: []byte{16, 1, 2}}, testStart, &out)

	if sent := out.take(); len(sent) != 0 {
		t.Errorf("a malformed challenge was answered with %+v", sent)
	}
}

func TestAnAuthenticateeGivesUpWhenThePeerNeverAnswers(t *testing.T) {
	timers := DefaultTimers()
	for _, protocol := range []AuthProtocol{AuthProtocolPAP, AuthProtocolCHAPMD5} {
		t.Run(protocol.String(), func(t *testing.T) {
			authenticatee := newAuthenticatee(protocol, testCredentials, timers)
			var out outbox
			now := testStart
			authenticatee.start(now, &out)

			var result outcome
			for range 2 * timers.MaxConfigure {
				now = now.Add(timers.negotiationTime())
				if result = authenticatee.handleTimeout(now, &out); result != undecided {
					break
				}
			}

			assertOutcome(t, result, failed("the peer never completed authentication"))
			assertNoTimerRuns(t, authenticatee)
		})
	}
}

func TestAPAPAuthenticateeSendsMaxConfigureRequests(t *testing.T) {
	timers := DefaultTimers()
	authenticatee := newAuthenticatee(AuthProtocolPAP, testCredentials, timers)
	var out outbox
	now := testStart
	authenticatee.start(now, &out)

	requests := len(out.take())
	for authenticatee.handleTimeout(now, &out) == undecided {
		requests += len(out.take())
		now = now.Add(timers.Restart)
	}

	if requests != timers.MaxConfigure {
		t.Errorf("sent %d requests, want %d", requests, timers.MaxConfigure)
	}
}

func TestEveryPAPRequestCarriesANewIdentifier(t *testing.T) {
	timers := DefaultTimers()
	authenticatee := newAuthenticatee(AuthProtocolPAP, testCredentials, timers)
	var out outbox
	now := testStart
	authenticatee.start(now, &out)

	identifiers := []uint8{takeFirst(t, &out).identifier}
	for range timers.MaxConfigure - 1 {
		now = now.Add(timers.Restart)
		authenticatee.handleTimeout(now, &out)
		identifiers = append(identifiers, takeFirst(t, &out).identifier)
	}

	// Every transmission is a new request (RFC 1334 §2.2.1).
	used := make(map[uint8]bool)
	for _, identifier := range identifiers {
		if used[identifier] {
			t.Fatalf("identifiers = %v, want a new one each time", identifiers)
		}
		used[identifier] = true
	}
}

func TestEveryChallengeAnsweredGetsAFullSetOfRetries(t *testing.T) {
	timers := DefaultTimers()
	authenticatee := newAuthenticatee(AuthProtocolCHAPMD5, testCredentials, timers)
	var out outbox
	now := testStart
	authenticatee.start(now, &out)
	challenge := packet{code: chapChallenge, identifier: 1, data: []byte{1, 0xAA}}
	authenticatee.receive(challenge, now, &out)
	for range timers.MaxConfigure - 1 { // the Response's retries, all used up
		now = now.Add(timers.Restart)
		authenticatee.handleTimeout(now, &out)
	}
	out.take()

	// The authenticator, which heard none of them, challenges again.
	authenticatee.receive(challenge, now, &out)
	response := takeOnly(t, &out)
	now = now.Add(timers.Restart)

	assertOutcome(t, authenticatee.handleTimeout(now, &out), undecided)
	assertOutgoing(t, out.take(), []outgoingPacket{response})
}

func TestAnAuthenticatorGivesUpWhenThePeerNeverAuthenticates(t *testing.T) {
	timers := DefaultTimers()
	for _, protocol := range []AuthProtocol{AuthProtocolPAP, AuthProtocolCHAPMD5} {
		t.Run(protocol.String(), func(t *testing.T) {
			authenticator := newAuthenticator(protocol, testCredentials, timers)
			var out outbox
			now := testStart
			authenticator.start(now, &out)

			var result outcome
			for range 2 * timers.MaxConfigure {
				now = now.Add(timers.Restart)
				if result = authenticator.handleTimeout(now, &out); result != undecided {
					break
				}
			}

			assertOutcome(t, result, failed("the peer never authenticated"))
			// The peer has a whole negotiation's worth of time: for its
			// MaxConfigure PAP requests, sent a Restart apart, or to answer
			// one of our MaxConfigure challenges.
			if elapsed := now.Sub(testStart); elapsed != timers.negotiationTime() {
				t.Errorf("gave up after %v, want %v", elapsed, timers.negotiationTime())
			}
			assertNoTimerRuns(t, authenticator)
		})
	}
}

func TestACHAPAuthenticatorSendsMaxConfigureChallenges(t *testing.T) {
	timers := DefaultTimers()
	authenticator := newAuthenticator(AuthProtocolCHAPMD5, testCredentials, timers)
	var out outbox
	now := testStart
	authenticator.start(now, &out)

	// A lost challenge is only ever recovered this way: the authenticatee
	// waits for one.
	challenges := len(out.take())
	var result outcome
	for range 2 * timers.MaxConfigure {
		now = now.Add(timers.Restart)
		result = authenticator.handleTimeout(now, &out)
		challenges += len(out.take())
		if result != undecided {
			break
		}
	}

	assertOutcome(t, result, failed("the peer never authenticated"))
	if challenges != timers.MaxConfigure {
		t.Errorf("sent %d challenges, want %d", challenges, timers.MaxConfigure)
	}
	// A challenge every Restart, and a Restart for the last to be answered.
	if elapsed := now.Sub(testStart); elapsed != timers.negotiationTime() {
		t.Errorf("gave up after %v, want %v", elapsed, timers.negotiationTime())
	}
}

func TestAnEarlyAuthenticationTimeoutChangesNothing(t *testing.T) {
	timers := DefaultTimers()
	authenticatee := newAuthenticatee(AuthProtocolPAP, testCredentials, timers)
	authenticator := newAuthenticator(AuthProtocolCHAPMD5, testCredentials, timers)
	var out outbox
	authenticatee.start(testStart, &out)
	authenticator.start(testStart, &out)
	out.take()

	early := testStart.Add(timers.Restart / 2)
	assertOutcome(t, authenticatee.handleTimeout(early, &out), undecided)
	assertOutcome(t, authenticator.handleTimeout(early, &out), undecided)

	if sent := out.take(); len(sent) != 0 {
		t.Errorf("an early timeout sent %+v", sent)
	}
}

func TestExchangesSpeakTheirProtocolsNumbers(t *testing.T) {
	tests := []struct {
		exchange authExchange
		want     uint16
	}{
		{newAuthenticatee(AuthProtocolPAP, testCredentials, DefaultTimers()), protocolPAP},
		{newAuthenticatee(AuthProtocolCHAPMD5, testCredentials, DefaultTimers()), protocolCHAP},
		{newAuthenticator(AuthProtocolPAP, testCredentials, DefaultTimers()), protocolPAP},
		{newAuthenticator(AuthProtocolCHAPMD5, testCredentials, DefaultTimers()), protocolCHAP},
	}
	for _, test := range tests {
		if got := test.exchange.protocolNumber(); got != test.want {
			t.Errorf("%T speaks %#04x, want %#04x", test.exchange, got, test.want)
		}
	}
}

func TestAFieldLongerThanOneOctetCanCarryIsAProgrammingError(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a 256-byte field was length-prefixed")
		}
	}()

	lengthPrefixedThen(make([]byte, 256), nil)
}

func TestAPAPAuthenticateeIgnoresPacketsThatAreNotReplies(t *testing.T) {
	authenticatee := newAuthenticatee(AuthProtocolPAP, testCredentials, DefaultTimers())
	var out outbox
	authenticatee.start(testStart, &out)

	got := authenticatee.receive(packet{code: papRequest, identifier: 1, data: []byte("\x04user\x04pass")}, testStart, &out)

	assertOutcome(t, got, undecided)
	if authenticatee.nextTimeout().IsZero() {
		t.Error("the request's timer stopped")
	}
}

func TestAnAuthenticatorIgnoresPacketsThatAreNotAttempts(t *testing.T) {
	tests := []struct {
		name     string
		protocol AuthProtocol
		received packet
	}{
		{"a PAP Ack", AuthProtocolPAP, packet{code: papAck, identifier: 1}},
		{"a PAP Nak", AuthProtocolPAP, packet{code: papNak, identifier: 1}},
		{"a CHAP Challenge", AuthProtocolCHAPMD5, packet{code: chapChallenge, identifier: 1, data: []byte{1, 0xAA}}},
		{"a CHAP Success", AuthProtocolCHAPMD5, packet{code: chapSuccess, identifier: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := newAuthenticator(test.protocol, testCredentials, DefaultTimers())
			var out outbox
			authenticator.start(testStart, &out)
			out.take()
			deadline := authenticator.nextTimeout()

			got := authenticator.receive(test.received, testStart, &out)

			assertOutcome(t, got, undecided)
			if sent := out.take(); len(sent) != 0 {
				t.Errorf("answered with %+v", sent)
			}
			if authenticator.nextTimeout() != deadline {
				t.Errorf("next timeout = %v, want %v still", authenticator.nextTimeout(), deadline)
			}
		})
	}
}
