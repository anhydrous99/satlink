package ppp

import (
	"crypto/md5"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// Tests of the authentication phase at the endpoint: what the
// server demands and the peer refuses, lost packets, and the timers.

func TestTheLongestCredentialsAuthenticate(t *testing.T) {
	// 255 bytes each, the most NewEndpoint accepts: PAP sends both behind
	// one-octet lengths.
	longest := Credentials{Username: strings.Repeat("u", 255), Password: strings.Repeat("p", 255)}
	for _, protocol := range []AuthProtocol{AuthProtocolPAP, AuthProtocolCHAPMD5} {
		t.Run(protocol.String(), func(t *testing.T) {
			link := newTestLink(t, NewConfig(ClientRole(longest)), serverConfigDemandingCredentials(protocol, longest))

			link.settle(t)

			if link.client.Phase() != PhaseOpen || link.server.Phase() != PhaseOpen {
				t.Errorf("phases = %v and %v, want Open", link.client.Phase(), link.server.Phase())
			}
		})
	}
}

func TestAuthenticationPacketsOfTheOtherProtocolAreDiscarded(t *testing.T) {
	// Each packet's code means something to the exchange that runs: handed
	// to it, the packet would draw an answer or decide the exchange.
	tests := []struct {
		name     string
		demanded AuthProtocol
		protocol uint16
		code     code
		data     []byte
	}{
		{"a PAP request where CHAP takes a Challenge", AuthProtocolCHAPMD5, protocolPAP, papRequest, []byte("\x05pilot\x06s3cret")},
		{"a PAP Nak where CHAP takes a Success", AuthProtocolCHAPMD5, protocolPAP, papNak, []byte("\x0DAccess denied")},
		{"a CHAP Response where PAP takes an Ack", AuthProtocolPAP, protocolCHAP, chapResponse, lengthPrefixedThen(make([]byte, md5.Size), []byte("pilot"))},
		{"a CHAP Success where PAP takes a Nak", AuthProtocolPAP, protocolCHAP, chapSuccess, []byte("Welcome")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link, attempt := authenticatingClient(t, test.demanded)
			// The packet carries the attempt's identifier, as an answer to it
			// would.
			hostile := packet{code: test.code, identifier: attempt.identifier, data: test.data}
			before := link.client.Stats().DiscardedPackets

			link.client.HandleInput(framed(test.protocol, hostile.encode()), link.now)

			if output := link.client.TakeOutput(); output != nil {
				t.Errorf("answered with % X", output)
			}
			if got := link.client.Stats().DiscardedPackets - before; got != 1 {
				t.Errorf("discarded %d packets, want 1", got)
			}
			if link.client.Phase() != PhaseAuthenticate {
				t.Errorf("phase = %v, want Authenticate", link.client.Phase())
			}
		})
	}
}

// authenticatingClient returns a link whose server demands protocol and
// whose client has made its attempt to authenticate — its PAP request, or
// its response to the CHAP challenge — and that attempt.
func authenticatingClient(t *testing.T, protocol AuthProtocol) (*testLink, packet) {
	t.Helper()
	link := newTestLink(t, clientConfig(), serverConfigDemanding(protocol))
	link.exchangeUntil(t, func() bool { return link.client.Phase() == PhaseAuthenticate })
	link.client.HandleInput(link.server.TakeOutput(), link.now) // the CHAP challenge; nothing over PAP
	attempts := controlPackets(link.client.TakeOutput(), protocol.protocolNumber())
	if len(attempts) != 1 {
		t.Fatalf("the client sent %v, want one attempt to authenticate", attempts)
	}
	return link, attempts[0]
}

func TestAPeerThatRejectsTheAuthenticationProtocolEndsTheLink(t *testing.T) {
	for _, protocol := range []AuthProtocol{AuthProtocolPAP, AuthProtocolCHAPMD5} {
		t.Run(protocol.String(), func(t *testing.T) {
			link := newTestLink(t, clientConfig(), serverConfigDemanding(protocol))
			link.exchangeUntil(t, func() bool { return link.client.Phase() == PhaseAuthenticate })
			rejected := binary.BigEndian.AppendUint16(nil, protocol.protocolNumber())

			link.client.HandleInput(lcpPacket(codeProtocolReject, 1, rejected), link.now)
			link.settle(t)

			assertDownReason(t, link.client, DownReason{
				Kind:   DownReasonAuthenticationFailed,
				Detail: "the peer rejected the authentication protocol",
			})
		})
	}
}

func TestAServerThatInsistsOnAuthenticatingAPeerThatWillNotGivesUp(t *testing.T) {
	demanding := serverConfigDemanding(AuthProtocolPAP)
	link := newTestLink(t, demanding, serverConfig())

	link.settle(t)

	assertDownReason(t, link.client, DownReason{
		Kind:     DownReasonNegotiationFailed,
		Protocol: "LCP",
		Detail:   "the peer refuses to authenticate",
	})
}

func TestAPeerThatNeverAuthenticatesIsRefused(t *testing.T) {
	timers := DefaultTimers()
	link := newTestLink(t, clientConfig(), serverConfigDemanding(AuthProtocolPAP))
	link.exchangeUntil(t, func() bool { return link.server.Phase() == PhaseAuthenticate })
	link.client.TakeOutput() // the client's PAP request is lost

	for link.server.Phase() != PhaseDead {
		link.server.TakeOutput() // and so is everything the server says
		link.now = link.now.Add(timers.Restart)
		link.server.HandleTimeout(link.now)
	}

	assertDownReason(t, link.server, DownReason{Kind: DownReasonAuthenticationFailed, Detail: "the peer never authenticated"})
}

func TestARestartOfLCPEndsTheAuthenticationExchange(t *testing.T) {
	link := newTestLink(t, clientConfig(), serverConfigDemanding(AuthProtocolPAP))
	link.exchangeUntil(t, func() bool { return link.client.Phase() == PhaseAuthenticate })
	link.client.TakeOutput() // the PAP request is lost

	link.client.HandleInput(framed(protocolLCP, peerRequest(77).encode()), link.now)
	link.now = link.now.Add(DefaultTimers().Restart)
	link.client.HandleTimeout(link.now)

	if requests := controlPackets(link.client.TakeOutput(), protocolPAP); len(requests) != 0 {
		t.Errorf("sent %+v while LCP renegotiated, want no PAP request", requests)
	}
	if link.client.Phase() != PhaseEstablish {
		t.Errorf("phase = %v, want Establish", link.client.Phase())
	}
}

func TestNextTimeoutIncludesTheAuthenticationTimer(t *testing.T) {
	link := newTestLink(t, withoutKeepalive(clientConfig()), serverConfigDemanding(AuthProtocolCHAPMD5))
	link.exchangeUntil(t, func() bool { return link.client.Phase() == PhaseAuthenticate })

	next, ok := link.client.NextTimeout()

	// The authenticator speaks first: the client allows it a whole
	// negotiation's worth of time.
	if want := link.now.Add(DefaultTimers().negotiationTime()); !ok || next != want {
		t.Errorf("next timeout = %v, %t; want the challenge's deadline %v", next, ok, want)
	}
}

func TestAnAuthenticatedLinkOutlivesTheAuthenticationTimer(t *testing.T) {
	for _, protocol := range []AuthProtocol{AuthProtocolPAP, AuthProtocolCHAPMD5} {
		t.Run(protocol.String(), func(t *testing.T) {
			link := establishedLink(t, serverConfigDemanding(protocol))
			events(link.client)
			events(link.server)
			keepalive := DefaultKeepalive()
			started := link.now

			// The endpoints keep their exchanges for the life of the link and
			// poll them: well past the time the authenticator allows the peer,
			// only the answered probes run.
			for link.now.Sub(started) < 5*time.Minute {
				link.advanceAsTheOwnersDo(t, keepalive.Interval)
			}

			nextProbe := link.now.Add(keepalive.Interval)
			for _, endpoint := range []*Endpoint{link.server, link.client} {
				assertEvents(t, events(endpoint), nil)
				if endpoint.Phase() != PhaseOpen {
					t.Errorf("the %v's phase = %v, want Open", endpoint.role, endpoint.Phase())
				}
				if next, ok := endpoint.NextTimeout(); !ok || next != nextProbe {
					t.Errorf("the %v's next timeout = %v, %t; want the next probe's, %v", endpoint.role, next, ok, nextProbe)
				}
			}
		})
	}
}

func TestAFailedRechallengeEndsAnAuthenticatedLink(t *testing.T) {
	link := establishedLink(t, serverConfigDemanding(AuthProtocolCHAPMD5))
	events(link.client)
	// The authenticator may challenge again at any time (RFC 1994 §2.2).
	rechallenge := packet{code: chapChallenge, identifier: 9, data: lengthPrefixedThen(make([]byte, challengeLen), []byte("gw"))}
	link.client.HandleInput(framed(protocolCHAP, rechallenge.encode()), link.now)
	link.client.TakeOutput() // the Response

	failure := packet{code: chapFailure, identifier: 9, data: []byte("Access denied")}
	link.client.HandleInput(framed(protocolCHAP, failure.encode()), link.now)
	link.settle(t)

	assertDownReason(t, link.client, DownReason{Kind: DownReasonAuthenticationFailed, Detail: "Access denied"})
}

func TestALostCHAPChallengeIsSentAgain(t *testing.T) {
	restart := DefaultTimers().Restart
	link := newTestLink(t, clientConfig(), serverConfigDemanding(AuthProtocolCHAPMD5))
	link.exchangeUntil(t, func() bool { return link.server.Phase() == PhaseAuthenticate })
	lost := link.now

	// The client waits for a challenge: the server's timer alone recovers.
	link.client.HandleInput(withoutFramesOf(protocolCHAP, link.server.TakeOutput()), link.now)
	link.settle(t)
	for link.client.Phase() != PhaseOpen && link.now.Sub(lost) < 2*restart {
		link.advanceAsTheOwnersDo(t, 100*time.Millisecond)
	}

	elapsed := link.now.Sub(lost)
	if link.client.Phase() != PhaseOpen || link.server.Phase() != PhaseOpen || elapsed > restart {
		t.Errorf("phases %v and %v %v after the challenge was lost, want Open within %v",
			link.client.Phase(), link.server.Phase(), elapsed, restart)
	}
}
