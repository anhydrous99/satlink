package ppp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

// Helpers shared by the package's tests: fixtures, assertions on packets
// and options, frames, two endpoints joined back to back, and names for
// the internal kinds that test failures print.

// testStart is the clock reading the sans-I/O tests start from; any fixed
// time will do.
var testStart = time.Date(2026, time.October, 19, 0, 0, 0, 0, time.UTC)

// The addressing of the tests' network: the server's own address, the one
// it hands the client, and its DNS server.
var (
	serverAddress = netip.MustParseAddr("10.64.0.1")
	clientAddress = netip.MustParseAddr("10.64.0.2")
	dnsServer     = netip.MustParseAddr("10.64.0.53")
)

// protocolIPv6 is a protocol the endpoint does not speak.
const protocolIPv6 uint16 = 0x0057

// hexBytes returns the bytes text spells as space-separated hex octets.
func hexBytes(tb testing.TB, text string) []byte {
	tb.Helper()
	decoded, err := hex.DecodeString(strings.Join(strings.Fields(text), ""))
	if err != nil {
		tb.Fatalf("bad hex %q: %v", text, err)
	}
	return decoded
}

// option returns an option of the given kind and value.
func option(kind uint8, value ...byte) configOption {
	return configOption{kind: kind, value: value}
}

// peerRequest returns a Configure-Request of the peer's.
func peerRequest(identifier uint8, options ...configOption) packet {
	return packet{code: codeConfigureRequest, identifier: identifier, data: encodeOptionList(options)}
}

// answerOf returns the peer's Configure-Ack, -Nak or -Reject of request, as
// answer says, carrying request's own options.
func answerOf(answer code, request packet) packet {
	return packet{code: answer, identifier: request.identifier, data: request.data}
}

// nakOf returns a Configure-Nak of request that suggests its own options.
func nakOf(request packet) packet {
	return answerOf(codeConfigureNak, request)
}

// stallingAnswer is an answer a peer can stall a negotiation with: it names
// nothing the requester can change, so the same request goes out again.
type stallingAnswer struct {
	name string
	// code is the answer's, Configure-Nak (3) or -Reject (4): written out,
	// so that the tests check packet.go's constants instead of trusting
	// them.
	code byte
	// options are the answer's options, as hex octets.
	options string
}

// to returns the answer to request, framed for the line as a packet of
// protocol.
func (s stallingAnswer) to(t *testing.T, protocol uint16, request packet) []byte {
	t.Helper()
	answer := packet{code: code(s.code), identifier: request.identifier, data: hexBytes(t, s.options)}
	return framed(protocol, answer.encode())
}

// stallNegotiation gives endpoint answer to its request of protocol count
// times, starting with request, and fails the test unless endpoint asks
// again each time with the same options under a new identifier. It returns
// endpoint's last request.
func stallNegotiation(t *testing.T, endpoint *Endpoint, protocol uint16, request packet, answer stallingAnswer, count int) packet {
	t.Helper()
	for range count {
		endpoint.HandleInput(answer.to(t, protocol, request), testStart)
		again := onlyRequest(t, endpoint, protocol)
		if !bytes.Equal(again.data, request.data) || again.identifier == request.identifier {
			t.Fatalf("asked again with %v after %v, want the same options under a new identifier", again, request)
		}
		request = again
	}
	return request
}

// onlyRequest returns what endpoint sent of protocol, failing the test
// unless it is one Configure-Request.
func onlyRequest(t *testing.T, endpoint *Endpoint, protocol uint16) packet {
	t.Helper()
	sent := controlPackets(endpoint.TakeOutput(), protocol)
	if len(sent) != 1 || sent[0].code != codeConfigureRequest {
		t.Fatalf("sent %+v of protocol %#04x, want one ConfigureRequest", sent, protocol)
	}
	return sent[0]
}

// assertOptions fails the test unless got holds want's options, in order.
func assertOptions(t *testing.T, got, want []configOption) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("options = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index].kind != want[index].kind || !bytes.Equal(got[index].value, want[index].value) {
			t.Fatalf("options = %+v, want %+v", got, want)
		}
	}
}

// assertVerdict fails the test unless got is want.
func assertVerdict(t *testing.T, got, want verdict) {
	t.Helper()
	if got.kind != want.kind || !bytes.Equal(got.suggestion, want.suggestion) {
		t.Fatalf("verdict = %+v, want %+v", got, want)
	}
}

// takeOnly empties out, failing the test unless it held exactly one packet,
// which it returns.
func takeOnly(t *testing.T, out *outbox) outgoingPacket {
	t.Helper()
	waiting := out.take()
	if len(waiting) != 1 {
		t.Fatalf("%d packets were sent, want 1: %+v", len(waiting), waiting)
	}
	return waiting[0]
}

// takeFirst empties out, failing the test when it held no packet, and
// returns the first.
func takeFirst(t *testing.T, out *outbox) packet {
	t.Helper()
	waiting := out.take()
	if len(waiting) == 0 {
		t.Fatal("no packet was sent")
	}
	return waiting[0].packet
}

// assertPacket fails the test unless got is want.
func assertPacket(t *testing.T, got, want packet) {
	t.Helper()
	if !got.equal(want) {
		t.Fatalf("packet = %+v, want %+v", got, want)
	}
}

// assertPackets fails the test unless got holds want's packets, in order.
func assertPackets(t *testing.T, got, want []packet) {
	t.Helper()
	if !slices.EqualFunc(got, want, packet.equal) {
		t.Fatalf("packets = %+v, want %+v", got, want)
	}
}

// assertOutgoing fails the test unless got holds want's packets, in order.
func assertOutgoing(t *testing.T, got, want []outgoingPacket) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d packets were sent, want %d: %+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index].protocol != want[index].protocol || !got[index].packet.equal(want[index].packet) {
			t.Fatalf("packet %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

// assertTransition fails the test unless got is want.
func assertTransition(t *testing.T, got, want transition) {
	t.Helper()
	if got != want {
		t.Fatalf("transition = %+v, want %+v", got, want)
	}
}

// negotiatedEncoding is what a typical peer agrees to, and what the tests'
// endpoints agree to with each other: no control characters escaped, both
// headers compressed.
func negotiatedEncoding() frameEncoding {
	return frameEncoding{accm: 0, compressAddressAndControl: true, compressProtocol: true}
}

// frameBytes returns information as one frame.
func frameBytes(protocol uint16, information []byte, encoding frameEncoding) []byte {
	return appendFrame(nil, protocol, information, encoding)
}

// framed returns information as one conservatively framed frame.
func framed(protocol uint16, information []byte) []byte {
	return frameBytes(protocol, information, conservativeEncoding())
}

// lcpPacket returns an LCP packet framed for the line.
func lcpPacket(code code, identifier uint8, data []byte) []byte {
	return framed(protocolLCP, packet{code: code, identifier: identifier, data: data}.encode())
}

// decodeAll returns the frames in data, for a decoder of the default MRU.
func decodeAll(data []byte) []frame {
	return newFrameDecoder(int(DefaultMRU)).decode(data)
}

// fcsOf returns the FCS transmitted after data.
func fcsOf(data []byte) uint16 {
	return ^updateFCS(fcsInitial, data)
}

// framesSent returns the frames in what an endpoint sent, read as a peer
// that asked for no control characters escaped reads them: framed
// conservatively or as negotiated, of any size an endpoint sends.
func framesSent(data []byte) []frame {
	decoder := newFrameDecoder(math.MaxUint16)
	decoder.setReceiveACCM(0)
	return decoder.decode(data)
}

// controlPackets returns the control packets of protocol in what an
// endpoint sent.
func controlPackets(data []byte, protocol uint16) []packet {
	var packets []packet
	for _, received := range framesSent(data) {
		if received.protocol != protocol {
			continue
		}
		if parsed, ok := parsePacket(received.information); ok {
			packets = append(packets, parsed)
		}
	}
	return packets
}

// withCode returns those of packets whose code is wanted.
func withCode(packets []packet, wanted code) []packet {
	var matching []packet
	for _, sent := range packets {
		if sent.code == wanted {
			matching = append(matching, sent)
		}
	}
	return matching
}

// withoutFramesOf returns what an endpoint sent without its frames of
// protocol, the others framed again, conservatively: as if those frames
// were lost on the line.
func withoutFramesOf(protocol uint16, data []byte) []byte {
	var kept []byte
	for _, sent := range framesSent(data) {
		if sent.protocol != protocol {
			kept = appendFrame(kept, sent.protocol, sent.information, conservativeEncoding())
		}
	}
	return kept
}

// pilotCredentials are the client's credentials, and those the server
// demands.
func pilotCredentials() Credentials {
	return Credentials{Username: "pilot", Password: "s3cret"}
}

// clientConfig returns the default client configuration.
func clientConfig() Config {
	return NewConfig(ClientRole(pilotCredentials()))
}

// serverConfig returns the server configuration of a network that demands
// no authentication.
func serverConfig() Config {
	return NewConfig(ServerRole(ServerConfig{
		LocalAddress: serverAddress,
		PeerAddress:  clientAddress,
		DNSServers:   []netip.Addr{dnsServer},
	}))
}

// serverConfigDemanding returns the server configuration of a network that
// demands authentication with protocol.
func serverConfigDemanding(protocol AuthProtocol) Config {
	return serverConfigDemandingCredentials(protocol, pilotCredentials())
}

// serverConfigDemandingCredentials returns the server configuration of a
// network that demands credentials, presented over protocol.
func serverConfigDemandingCredentials(protocol AuthProtocol, credentials Credentials) Config {
	return NewConfig(ServerRole(ServerConfig{
		LocalAddress:   serverAddress,
		PeerAddress:    clientAddress,
		DNSServers:     []netip.Addr{dnsServer},
		Authentication: &Authentication{Protocol: protocol, Credentials: credentials},
	}))
}

// withoutKeepalive returns config with keepalive turned off.
func withoutKeepalive(config Config) Config {
	config.Keepalive = nil
	return config
}

// withMRU returns config with mru as its MRU.
func withMRU(config Config, mru uint16) Config {
	config.MRU = mru
	return config
}

// mustEndpoint returns an endpoint for config, failing the test when
// config is refused.
func mustEndpoint(t *testing.T, config Config) *Endpoint {
	t.Helper()
	endpoint, err := NewEndpoint(config)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

// negotiatingEndpoint returns an endpoint for config that has just sent its
// first LCP Configure-Request, and that request.
func negotiatingEndpoint(t *testing.T, config Config) (*Endpoint, packet) {
	t.Helper()
	endpoint := mustEndpoint(t, config)
	endpoint.Open(testStart)
	return endpoint, controlPackets(endpoint.TakeOutput(), protocolLCP)[0]
}

// testLink is two endpoints joined back to back, sharing a clock the test
// controls.
type testLink struct {
	client *Endpoint
	server *Endpoint
	now    time.Time
}

// newTestLink returns a link whose two ends have just been opened.
func newTestLink(t *testing.T, client, server Config) *testLink {
	t.Helper()
	link := &testLink{client: mustEndpoint(t, client), server: mustEndpoint(t, server), now: testStart}
	link.client.Open(link.now)
	link.server.Open(link.now)
	return link
}

// establishedLink returns a link between a default client and server,
// open for IPv4.
func establishedLink(t *testing.T, server Config) *testLink {
	t.Helper()
	link := newTestLink(t, clientConfig(), server)
	link.settle(t)
	if link.client.Phase() != PhaseOpen {
		t.Fatalf("the client's phase = %v, want Open", link.client.Phase())
	}
	return link
}

// settle delivers everything in flight, both ways, until both ends fall
// silent.
func (l *testLink) settle(t *testing.T) {
	t.Helper()
	const maxRounds = 100
	for range maxRounds {
		toServer, toClient := l.client.TakeOutput(), l.server.TakeOutput()
		if len(toServer) == 0 && len(toClient) == 0 {
			return
		}
		l.server.HandleInput(toServer, l.now)
		l.client.HandleInput(toClient, l.now)
	}
	t.Fatal("the endpoints never fell silent")
}

// exchangeUntil delivers one round of output each way at a time until done
// reports true, failing the test if the link falls silent first.
func (l *testLink) exchangeUntil(t *testing.T, done func() bool) {
	t.Helper()
	const maxRounds = 100
	for range maxRounds {
		if done() {
			return
		}
		toServer, toClient := l.client.TakeOutput(), l.server.TakeOutput()
		if len(toServer) == 0 && len(toClient) == 0 {
			t.Fatal("the link fell silent first")
		}
		l.server.HandleInput(toServer, l.now)
		l.client.HandleInput(toClient, l.now)
	}
	t.Fatal("the link never got there")
}

// advance moves the clock on by, running both ends' timers.
func (l *testLink) advance(by time.Duration) {
	l.now = l.now.Add(by)
	l.client.HandleTimeout(l.now)
	l.server.HandleTimeout(l.now)
}

// advanceAsTheOwnersDo moves the clock on by and runs each end's timers
// only if NextTimeout says they are due, as satnet's driver and sim's
// gateway do, then delivers what they send.
func (l *testLink) advanceAsTheOwnersDo(t *testing.T, by time.Duration) {
	t.Helper()
	l.now = l.now.Add(by)
	for _, endpoint := range []*Endpoint{l.client, l.server} {
		if due, ok := endpoint.NextTimeout(); ok && !due.After(l.now) {
			endpoint.HandleTimeout(l.now)
		}
	}
	l.settle(t)
}

// events drains endpoint's events.
func events(endpoint *Endpoint) []Event {
	var drained []Event
	for {
		event, ok := endpoint.PollEvent()
		if !ok {
			return drained
		}
		drained = append(drained, event)
	}
}

// assertEvents fails the test unless got holds want's events, in order.
func assertEvents(t *testing.T, got, want []Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for index := range want {
		if !got[index].Equal(want[index]) {
			t.Fatalf("events = %+v, want %+v", got, want)
		}
	}
}

// magicNumberOf returns endpoint's magic number, as LCP's echo packets
// carry it.
func magicNumberOf(endpoint *Endpoint) []byte {
	return binary.BigEndian.AppendUint32(nil, endpoint.lcp.negotiator.ownMagicNumber())
}

// assertDownReason fails the test unless endpoint's only event is going
// down for want.
func assertDownReason(t *testing.T, endpoint *Endpoint, want DownReason) {
	t.Helper()
	assertEvents(t, events(endpoint), []Event{{Kind: EventDown, Reason: want}})
}

// assertDeadUntilOpenedAgain fails the test unless endpoint, whose link went
// down for good at now and whose output was taken, behaves as a dead link
// must: it runs no timer, sends and reports nothing as time passes, answers
// the peer's negotiation as a closed LCP does (RFC 1661 §4.1), and
// negotiates afresh once opened again.
func assertDeadUntilOpenedAgain(t *testing.T, endpoint *Endpoint, now time.Time) {
	t.Helper()
	if next, ok := endpoint.NextTimeout(); ok {
		t.Errorf("a dead link runs a timer until %v", next)
	}
	for range 3 {
		now = now.Add(DefaultKeepalive().Interval)
		endpoint.HandleTimeout(now)
		if output := endpoint.TakeOutput(); output != nil {
			t.Fatalf("a dead link sent % X", output)
		}
		assertEvents(t, events(endpoint), nil)
	}
	endpoint.HandleInput(framed(protocolLCP, peerRequest(60).encode()), now)
	closed := packet{code: codeTerminateAck, identifier: 60}
	assertPackets(t, controlPackets(endpoint.TakeOutput(), protocolLCP), []packet{closed})
	if endpoint.Phase() != PhaseDead {
		t.Fatalf("phase = %v, want Dead", endpoint.Phase())
	}
	endpoint.Open(now)
	if requests := controlPackets(endpoint.TakeOutput(), protocolLCP); len(requests) != 1 || requests[0].code != codeConfigureRequest {
		t.Errorf("Open sent %+v, want one ConfigureRequest", requests)
	}
}

// The texts test failures print for the automaton's and the exchanges'
// values, which fmt would otherwise print as numbers: it calls no methods
// of unexported fields. Only the tests print them, so their String methods
// live here.

// String returns the transition's name, with why for a finish, such as
// "Finished(TimedOut)".
func (t transition) String() string {
	if t.kind == transitionFinished {
		return fmt.Sprintf("Finished(%v)", t.finish)
	}
	return t.kind.String()
}

// String returns the finish's name, with the reason for a failure.
func (f finish) String() string {
	if f.kind == finishFailed {
		return fmt.Sprintf("Failed(%s)", f.reason)
	}
	return f.kind.String()
}

// String returns the outcome's name, with the reason for a failure.
func (o outcome) String() string {
	if o.kind == outcomeFailed {
		return fmt.Sprintf("Failed(%s)", o.reason)
	}
	return o.kind.String()
}

// String returns the verdict's name, with the suggestion of a Nak.
func (v verdict) String() string {
	if v.kind == verdictNak {
		return fmt.Sprintf("Nak(% X)", v.suggestion)
	}
	return v.kind.String()
}

// String returns the packet's code, identifier and data, the data in hex.
// The code is a number: PAP and CHAP number their codes their own way.
func (p packet) String() string {
	return fmt.Sprintf("{code:%d identifier:%d data:% X}", uint8(p.code), p.identifier, p.data)
}

// String returns the packet with the number of its protocol.
func (o outgoingPacket) String() string {
	return fmt.Sprintf("{protocol:%#04x packet:%v}", o.protocol, o.packet)
}

// String returns the state's name, such as "AckReceived".
func (s automatonState) String() string {
	names := [...]string{
		stateClosed:      "Closed",
		stateRequestSent: "RequestSent",
		stateAckReceived: "AckReceived",
		stateAckSent:     "AckSent",
		stateOpened:      "Opened",
		stateClosing:     "Closing",
	}
	return nameOf(int(s), names[:], "automatonState")
}

// String returns the transition kind's name, such as "Up".
func (k transitionKind) String() string {
	names := [...]string{transitionNone: "None", transitionUp: "Up", transitionDown: "Down", transitionFinished: "Finished"}
	return nameOf(int(k), names[:], "transitionKind")
}

// String returns the finish kind's name, such as "TimedOut".
func (k finishKind) String() string {
	names := [...]string{
		finishClosed:           "Closed",
		finishTerminatedByPeer: "TerminatedByPeer",
		finishTimedOut:         "TimedOut",
		finishFailed:           "Failed",
		finishLoopedBack:       "LoopedBack",
	}
	return nameOf(int(k), names[:], "finishKind")
}

// String returns the verdict kind's name, such as "Nak".
func (k verdictKind) String() string {
	names := [...]string{verdictAck: "Ack", verdictNak: "Nak", verdictReject: "Reject"}
	return nameOf(int(k), names[:], "verdictKind")
}

// String returns the outcome kind's name, such as "Succeeded".
func (k outcomeKind) String() string {
	names := [...]string{outcomeUndecided: "Undecided", outcomeSucceeded: "Succeeded", outcomeFailed: "Failed"}
	return nameOf(int(k), names[:], "outcomeKind")
}

// nameOf returns the name of value, one of a kind's constants, and
// "typeName(value)" for any other value.
func nameOf(value int, names []string, typeName string) string {
	if value >= 0 && value < len(names) {
		return names[value]
	}
	return fmt.Sprintf("%s(%d)", typeName, value)
}
