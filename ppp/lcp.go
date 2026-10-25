package ppp

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// The LCP option policy (RFC 1661 §6, RFC 1662 §7.1, RFC 1994 §3).

// LCP configuration option types.
const (
	lcpOptionMRU                       uint8 = 1
	lcpOptionACCM                      uint8 = 2
	lcpOptionAuthentication            uint8 = 3
	lcpOptionMagicNumber               uint8 = 5
	lcpOptionProtocolCompression       uint8 = 7
	lcpOptionAddressControlCompression uint8 = 8
)

// chapMD5Algorithm is the CHAP algorithm octet for MD5 (RFC 1994 §3).
const chapMD5Algorithm byte = 5

// The reasons LCP negotiation fails.
var (
	errUnsupportedAuthentication = errors.New("the peer can only authenticate with a protocol we do not support")
	errAuthenticationRefused     = errors.New("the peer refuses to authenticate")
)

// optionValue returns the Authentication-Protocol option value naming p.
func (p AuthProtocol) optionValue() []byte {
	if p == AuthProtocolCHAPMD5 {
		return append(binary.BigEndian.AppendUint16(nil, protocolCHAP), chapMD5Algorithm)
	}
	// AuthProtocolPAP: NewEndpoint refuses every protocol without a
	// constant.
	return binary.BigEndian.AppendUint16(nil, protocolPAP)
}

// authProtocolFromOptionValue returns the protocol an
// Authentication-Protocol option value names, and false for one we do not
// speak.
func authProtocolFromOptionValue(value []byte) (AuthProtocol, bool) {
	if len(value) < uint16Len {
		return 0, false
	}
	number, algorithm := binary.BigEndian.Uint16(value), value[uint16Len:]
	switch {
	case number == protocolPAP && len(algorithm) == 0:
		return AuthProtocolPAP, true
	case number == protocolCHAP && bytes.Equal(algorithm, []byte{chapMD5Algorithm}):
		return AuthProtocolCHAPMD5, true
	default:
		return 0, false
	}
}

// ownLCPOptions are what we propose. An option is dropped from future
// requests once the peer rejects it.
type ownLCPOptions struct {
	// mru is the MRU we propose; zero proposes none, leaving the default.
	mru uint16
	// accm holds the control characters we ask the peer to escape, while
	// proposesACCM holds.
	accm         uint32
	proposesACCM bool
	// magicNumber is ours; zero once the peer refused magic numbers.
	magicNumber               uint32
	protocolCompression       bool
	addressControlCompression bool
	// authentication is the protocol we demand that the peer prove its
	// identity with, while demandsAuthentication holds.
	authentication        AuthProtocol
	demandsAuthentication bool
}

// peerLCPOptions are the peer's settings, from the last Configure-Request
// we acknowledged.
type peerLCPOptions struct {
	mru                       uint16
	accm                      uint32
	protocolCompression       bool
	addressControlCompression bool
	// authentication is the protocol the peer demands that we prove our
	// identity with, while demandsAuthentication holds.
	authentication        AuthProtocol
	demandsAuthentication bool
}

// defaultPeerLCPOptions returns the settings of a peer that has not asked
// for anything (RFC 1661 §6, RFC 1662 §7.1).
func defaultPeerLCPOptions() peerLCPOptions {
	return peerLCPOptions{mru: DefaultMRU, accm: DefaultACCM}
}

// lcpNegotiator is LCP's half of the negotiation.
type lcpNegotiator struct {
	own  ownLCPOptions
	peer peerLCPOptions
	// receiveLimit is the largest information field we take from the peer:
	// the endpoint's decoder is sized to it, so we never adopt a larger MRU
	// that the peer suggests.
	receiveLimit uint16
	// authenticatesOnRequest is whether we agree to prove our identity when
	// the peer asks (client role).
	authenticatesOnRequest bool
}

// newClientLCP returns the dialling side's policy, which proves its
// identity when the peer asks and demands nothing of the peer.
func newClientLCP(mru uint16, receiveACCM uint32) *lcpNegotiator {
	negotiator := newLCP(mru, receiveACCM)
	negotiator.authenticatesOnRequest = true
	return negotiator
}

// newServerLCP returns the answering side's policy, which never proves its
// own identity and, unless demanded is nil, demands that the peer prove its
// identity with demanded's protocol.
func newServerLCP(mru uint16, receiveACCM uint32, demanded *Authentication) *lcpNegotiator {
	negotiator := newLCP(mru, receiveACCM)
	if demanded != nil {
		negotiator.own.authentication = demanded.Protocol
		negotiator.own.demandsAuthentication = true
	}
	return negotiator
}

// newLCP returns the policy both sides share: our MRU unless it is the
// default, our receive ACCM, a magic number and header compression. We take
// frames of our MRU, and of DefaultMRU whatever MRU was negotiated, as every
// peer must (RFC 1661 §6.1).
func newLCP(mru uint16, receiveACCM uint32) *lcpNegotiator {
	own := ownLCPOptions{
		accm:         receiveACCM,
		proposesACCM: true,
		magicNumber:  nonzeroUint32(),
		// Our decoder reads compressed headers, so let the peer save the
		// bytes.
		protocolCompression:       true,
		addressControlCompression: true,
	}
	if mru != DefaultMRU {
		own.mru = mru
	}
	return &lcpNegotiator{own: own, peer: defaultPeerLCPOptions(), receiveLimit: max(mru, DefaultMRU)}
}

// transmitEncoding returns how to frame what we send, honouring what the
// peer asked for.
func (l *lcpNegotiator) transmitEncoding() frameEncoding {
	return frameEncoding{
		accm:                      l.peer.accm,
		compressAddressAndControl: l.peer.addressControlCompression,
		compressProtocol:          l.peer.protocolCompression,
	}
}

// receiveACCM returns the control characters the peer escapes for us — or
// will, once it agrees.
func (l *lcpNegotiator) receiveACCM() uint32 {
	if l.own.proposesACCM {
		return l.own.accm
	}
	return DefaultACCM
}

// ownMagicNumber returns our magic number, zero when the peer refused magic
// numbers.
func (l *lcpNegotiator) ownMagicNumber() uint32 {
	return l.own.magicNumber
}

// peerMRU returns the largest information field the peer accepts.
func (l *lcpNegotiator) peerMRU() uint16 {
	return l.peer.mru
}

// authenticationToPerform returns the protocol the peer demands that we
// prove our identity with, and false when it demands none.
func (l *lcpNegotiator) authenticationToPerform() (AuthProtocol, bool) {
	return l.peer.authentication, l.peer.demandsAuthentication
}

// authenticationToVerify returns the protocol the peer agreed to prove its
// identity with, and false when we demand none.
func (l *lcpNegotiator) authenticationToVerify() (AuthProtocol, bool) {
	return l.own.authentication, l.own.demandsAuthentication
}

// ownOptions returns the options for our next Configure-Request.
func (l *lcpNegotiator) ownOptions() []configOption {
	own := l.own
	var options []configOption
	if own.mru != 0 {
		options = append(options, uint16Option(lcpOptionMRU, own.mru))
	}
	if own.proposesACCM {
		options = append(options, uint32Option(lcpOptionACCM, own.accm))
	}
	if own.demandsAuthentication {
		options = append(options, configOption{kind: lcpOptionAuthentication, value: own.authentication.optionValue()})
	}
	if own.magicNumber != 0 {
		options = append(options, uint32Option(lcpOptionMagicNumber, own.magicNumber))
	}
	if own.protocolCompression {
		options = append(options, configOption{kind: lcpOptionProtocolCompression})
	}
	if own.addressControlCompression {
		options = append(options, configOption{kind: lcpOptionAddressControlCompression})
	}
	return options
}

// applyNak adopts the peer's suggestions for our options.
func (l *lcpNegotiator) applyNak(suggestions []configOption) error {
	for _, suggestion := range suggestions {
		if err := l.adopt(suggestion); err != nil {
			return err
		}
	}
	return nil
}

// adopt adopts one suggestion for our options.
func (l *lcpNegotiator) adopt(suggestion configOption) error {
	switch suggestion.kind {
	case lcpOptionMRU:
		l.adoptMRU(suggestion)
	case lcpOptionACCM:
		// Escape whatever else the peer needs escaped.
		if extra, ok := suggestion.uint32Value(); ok {
			l.own.accm = l.receiveACCM() | extra
			l.own.proposesACCM = true
		}
	case lcpOptionMagicNumber:
		l.own.magicNumber = nonzeroUint32()
	case lcpOptionProtocolCompression:
		l.own.protocolCompression = false
	case lcpOptionAddressControlCompression:
		l.own.addressControlCompression = false
	case lcpOptionAuthentication:
		return l.adoptAuthentication(suggestion.value)
	}
	return nil // anything else is a hint about an option we did not propose
}

// adoptMRU adopts the MRU the peer suggests if it carries an IPv4 packet
// and we can take frames that large. Otherwise we propose none, leaving the
// default.
func (l *lcpNegotiator) adoptMRU(suggestion configOption) {
	l.own.mru = 0
	if mru, ok := suggestion.uint16Value(); ok && MinMRU <= mru && mru <= l.receiveLimit {
		l.own.mru = mru
	}
}

// adoptAuthentication adopts the protocol the peer suggests proving its
// identity with instead of the one we demand.
func (l *lcpNegotiator) adoptAuthentication(value []byte) error {
	if !l.own.demandsAuthentication {
		return nil // a hint about an option we did not propose
	}
	alternative, ok := authProtocolFromOptionValue(value)
	if !ok {
		return errUnsupportedAuthentication
	}
	l.own.authentication = alternative
	return nil
}

// applyReject drops the options the peer refused.
func (l *lcpNegotiator) applyReject(rejected []configOption) error {
	for _, option := range rejected {
		switch option.kind {
		case lcpOptionMRU:
			l.own.mru = 0
		case lcpOptionACCM:
			l.own.proposesACCM = false
		case lcpOptionMagicNumber:
			l.own.magicNumber = 0
		case lcpOptionProtocolCompression:
			l.own.protocolCompression = false
		case lcpOptionAddressControlCompression:
			l.own.addressControlCompression = false
		case lcpOptionAuthentication:
			if l.own.demandsAuthentication {
				return errAuthenticationRefused
			}
		}
	}
	return nil
}

// judge answers one option of the peer's Configure-Request.
func (l *lcpNegotiator) judge(option configOption) verdict {
	switch option.kind {
	case lcpOptionMRU:
		return judgeMRU(option)
	case lcpOptionACCM:
		return ackIfValueLen(option, uint32Len)
	case lcpOptionAuthentication:
		return l.judgeAuthentication(option.value)
	case lcpOptionMagicNumber:
		return l.judgeMagicNumber(option)
	case lcpOptionProtocolCompression, lcpOptionAddressControlCompression:
		return ackIfValueLen(option, 0)
	default:
		return rejectVerdict()
	}
}

// judgeMRU accepts any MRU that carries an IPv4 packet, and suggests the
// smallest that does instead of a smaller one.
func judgeMRU(option configOption) verdict {
	mru, ok := option.uint16Value()
	switch {
	case !ok:
		return rejectVerdict()
	case mru < MinMRU:
		return nakVerdict(binary.BigEndian.AppendUint16(nil, MinMRU))
	default:
		return ackVerdict()
	}
}

// judgeAuthentication answers the peer's demand that we prove our
// identity.
func (l *lcpNegotiator) judgeAuthentication(value []byte) verdict {
	if !l.authenticatesOnRequest {
		return rejectVerdict()
	}
	if _, ok := authProtocolFromOptionValue(value); ok {
		return ackVerdict()
	}
	// Suggest the protocol we speak that keeps the password off the wire.
	return nakVerdict(AuthProtocolCHAPMD5.optionValue())
}

// judgeMagicNumber answers the peer's magic number.
func (l *lcpNegotiator) judgeMagicNumber(option configOption) verdict {
	number, ok := option.uint32Value()
	switch {
	case !ok:
		return rejectVerdict()
	case number == 0 || number == l.own.magicNumber:
		// Zero is invalid, and our own number coming back suggests a
		// looped-back line (RFC 1661 §6.4).
		return nakVerdict(binary.BigEndian.AppendUint32(nil, nonzeroUint32()))
	default:
		return ackVerdict()
	}
}

// ackIfValueLen accepts an option whose value has length octets, the only
// well-formed size, and rejects it otherwise.
func ackIfValueLen(option configOption, length int) verdict {
	if len(option.value) == length {
		return ackVerdict()
	}
	return rejectVerdict()
}

// acceptPeerOptions records the peer's settings from a Configure-Request we
// acknowledged; whatever it left out takes its default.
func (l *lcpNegotiator) acceptPeerOptions(options []configOption) {
	peer := defaultPeerLCPOptions()
	for _, option := range options {
		peer.record(option)
	}
	l.peer = peer
}

// record records one of the peer's options.
func (p *peerLCPOptions) record(option configOption) {
	switch option.kind {
	case lcpOptionMRU:
		p.mru = DefaultMRU
		if mru, ok := option.uint16Value(); ok {
			p.mru = mru
		}
	case lcpOptionACCM:
		p.accm = DefaultACCM
		if accm, ok := option.uint32Value(); ok {
			p.accm = accm
		}
	case lcpOptionAuthentication:
		p.authentication, p.demandsAuthentication = authProtocolFromOptionValue(option.value)
	case lcpOptionProtocolCompression:
		p.protocolCompression = true
	case lcpOptionAddressControlCompression:
		p.addressControlCompression = true
	}
}
