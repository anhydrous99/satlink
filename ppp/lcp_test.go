package ppp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// testClientLCP returns the client policy used by these tests: the default
// MRU and no control characters escaped towards us.
func testClientLCP() *lcpNegotiator {
	return newClientLCP(DefaultMRU, 0)
}

func TestAcceptsATypicalPPPDRequest(t *testing.T) {
	lcp := testClientLCP()
	request := []configOption{
		option(lcpOptionMRU, 0x05, 0xDC),
		option(lcpOptionACCM, 0, 0, 0, 0),
		option(lcpOptionAuthentication, 0xC0, 0x23),
		option(lcpOptionMagicNumber, 0x12, 0x34, 0x56, 0x78),
		option(lcpOptionProtocolCompression),
		option(lcpOptionAddressControlCompression),
	}

	for _, proposed := range request {
		assertVerdict(t, lcp.judge(proposed), ackVerdict())
	}
}

func TestSteersThePeerTowardsSupportedAuthentication(t *testing.T) {
	msCHAPv2 := option(lcpOptionAuthentication, 0xC2, 0x23, 0x81)
	server := newServerLCP(DefaultMRU, 0, &Authentication{Protocol: AuthProtocolPAP})

	assertVerdict(t, testClientLCP().judge(msCHAPv2), nakVerdict([]byte{0xC2, 0x23, chapMD5Algorithm}))
	assertVerdict(t, server.judge(msCHAPv2), rejectVerdict())
}

func TestRejectsUnknownAndMalformedOptions(t *testing.T) {
	tests := []struct {
		name     string
		proposed configOption
	}{
		{"Callback", option(13, 6)},
		{"a two-octet ACCM", option(lcpOptionACCM, 0, 0)},
		{"Protocol-Field-Compression with a value", option(lcpOptionProtocolCompression, 1)},
		{"a one-octet MRU", option(lcpOptionMRU, 1)},
		{"a two-octet magic number", option(lcpOptionMagicNumber, 1, 2)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertVerdict(t, testClientLCP().judge(test.proposed), rejectVerdict())
		})
	}
}

func TestSuspectsALoopWhenItsOwnMagicNumberReturns(t *testing.T) {
	lcp := testClientLCP()
	own := binary.BigEndian.AppendUint32(nil, lcp.ownMagicNumber())

	for _, suspicious := range [][]byte{own, {0, 0, 0, 0}} {
		if got := lcp.judge(option(lcpOptionMagicNumber, suspicious...)); got.kind != verdictNak {
			t.Errorf("verdict on magic number % X = %+v, want a Nak", suspicious, got)
		}
	}
}

func TestRemembersWhatThePeerNegotiated(t *testing.T) {
	lcp := testClientLCP()

	lcp.acceptPeerOptions([]configOption{
		option(lcpOptionMRU, 0x02, 0x40),
		option(lcpOptionACCM, 0, 0x0A, 0, 0),
		option(lcpOptionAuthentication, 0xC2, 0x23, 0x05),
		option(lcpOptionAddressControlCompression),
	})

	if got := lcp.peerMRU(); got != 576 {
		t.Errorf("peer MRU = %d, want 576", got)
	}
	if protocol, ok := lcp.authenticationToPerform(); !ok || protocol != AuthProtocolCHAPMD5 {
		t.Errorf("authentication to perform = %v, %t; want CHAPMD5, true", protocol, ok)
	}
	want := frameEncoding{accm: 0x000A_0000, compressAddressAndControl: true, compressProtocol: false}
	if got := lcp.transmitEncoding(); got != want {
		t.Errorf("transmit encoding = %+v, want %+v", got, want)
	}
}

func TestAdaptsItsOwnOptionsToNaksAndRejects(t *testing.T) {
	lcp := newClientLCP(DefaultMRU, 0x0000_2400)

	if err := lcp.applyNak([]configOption{option(lcpOptionACCM, 0, 0x0A, 0, 0)}); err != nil {
		t.Fatal(err)
	}
	if got := lcp.receiveACCM(); got != 0x000A_2400 {
		t.Errorf("receive ACCM after the Nak = %#x, want 0xa2400", got)
	}
	rejected := []configOption{option(lcpOptionMagicNumber, 1, 2, 3, 4), option(lcpOptionACCM, 0, 0, 0, 0)}
	if err := lcp.applyReject(rejected); err != nil {
		t.Fatal(err)
	}

	if got := lcp.ownMagicNumber(); got != 0 {
		t.Errorf("own magic number = %#x, want 0", got)
	}
	if got := lcp.receiveACCM(); got != DefaultACCM {
		t.Errorf("receive ACCM = %#x, want the default", got)
	}
	for _, proposed := range lcp.ownOptions() {
		if proposed.kind == lcpOptionMagicNumber || proposed.kind == lcpOptionACCM {
			t.Errorf("a rejected option is still proposed: %+v", proposed)
		}
	}
}

func TestAServerWhoseAuthenticationIsRefusedGivesUp(t *testing.T) {
	demanding := &Authentication{Protocol: AuthProtocolCHAPMD5}
	refused := newServerLCP(DefaultMRU, 0, demanding)
	if err := refused.applyReject([]configOption{option(lcpOptionAuthentication, 0xC2, 0x23, 0x05)}); err == nil {
		t.Error("a refused authentication was accepted")
	}

	steered := newServerLCP(DefaultMRU, 0, demanding)
	if err := steered.applyNak([]configOption{option(lcpOptionAuthentication, 0xC0, 0x23)}); err != nil {
		t.Fatal(err)
	}

	if protocol, ok := steered.authenticationToVerify(); !ok || protocol != AuthProtocolPAP {
		t.Errorf("authentication to verify = %v, %t; want PAP, true", protocol, ok)
	}
}

func TestAServerCannotBeSteeredToAProtocolItDoesNotSpeak(t *testing.T) {
	server := newServerLCP(DefaultMRU, 0, &Authentication{Protocol: AuthProtocolPAP})

	err := server.applyNak([]configOption{option(lcpOptionAuthentication, 0xC2, 0x23, 0x81)})

	if err == nil || err.Error() != "the peer can only authenticate with a protocol we do not support" {
		t.Errorf("error = %v, want the unsupported-protocol failure", err)
	}
}

func TestAClientIgnoresHintsAboutOptionsItDidNotPropose(t *testing.T) {
	lcp := testClientLCP()
	before := lcp.ownOptions()

	// PAP, MS-CHAPv2 — which we do not speak — and a Callback.
	hints := []configOption{
		option(lcpOptionAuthentication, 0xC0, 0x23),
		option(lcpOptionAuthentication, 0xC2, 0x23, 0x81),
		option(13, 6),
	}
	if err := lcp.applyNak(hints); err != nil {
		t.Fatal(err)
	}
	if err := lcp.applyReject(hints); err != nil {
		t.Fatal(err)
	}

	assertOptions(t, lcp.ownOptions(), before)
}

func TestProposesTheConfiguredMRUAndHeaderCompression(t *testing.T) {
	lcp := newClientLCP(576, 0)

	proposed := lcp.ownOptions()

	assertOptions(t, proposed, []configOption{
		option(lcpOptionMRU, 0x02, 0x40),
		option(lcpOptionACCM, 0, 0, 0, 0),
		uint32Option(lcpOptionMagicNumber, lcp.ownMagicNumber()),
		option(lcpOptionProtocolCompression),
		option(lcpOptionAddressControlCompression),
	})
}

func TestAServerProposesTheAuthenticationItDemands(t *testing.T) {
	tests := []struct {
		protocol AuthProtocol
		want     []byte
	}{
		{AuthProtocolPAP, []byte{0xC0, 0x23}},
		{AuthProtocolCHAPMD5, []byte{0xC2, 0x23, 0x05}},
	}
	for _, test := range tests {
		t.Run(test.protocol.String(), func(t *testing.T) {
			server := newServerLCP(DefaultMRU, 0, &Authentication{Protocol: test.protocol})

			proposed := server.ownOptions()

			if len(proposed) < 2 || proposed[1].kind != lcpOptionAuthentication || !bytes.Equal(proposed[1].value, test.want) {
				t.Errorf("options = %+v, want an Authentication-Protocol of % X after the ACCM", proposed, test.want)
			}
		})
	}
}

func TestAdaptsItsMRUMagicNumberAndCompressionToNaks(t *testing.T) {
	lcp := newClientLCP(576, 0)
	before := lcp.ownMagicNumber()

	err := lcp.applyNak([]configOption{
		option(lcpOptionMRU, 0x00, 0x20),
		option(lcpOptionMagicNumber, 0, 0, 0, 1),
		option(lcpOptionProtocolCompression),
		option(lcpOptionAddressControlCompression),
	})
	if err != nil {
		t.Fatal(err)
	}

	if lcp.own.mru != 0 {
		t.Errorf("MRU = %d after a Nak below the minimum, want none proposed", lcp.own.mru)
	}
	if lcp.ownMagicNumber() == before || lcp.ownMagicNumber() == 0 {
		t.Errorf("magic number = %#x after a Nak, want a fresh one", lcp.ownMagicNumber())
	}
	if lcp.own.protocolCompression || lcp.own.addressControlCompression {
		t.Error("header compression is still proposed after a Nak")
	}
}

func TestDropsMRUAndCompressionOnceRejected(t *testing.T) {
	lcp := newClientLCP(576, 0)

	err := lcp.applyReject([]configOption{
		option(lcpOptionMRU, 0x02, 0x40),
		option(lcpOptionProtocolCompression),
		option(lcpOptionAddressControlCompression),
	})
	if err != nil {
		t.Fatal(err)
	}

	assertOptions(t, lcp.ownOptions(), []configOption{
		option(lcpOptionACCM, 0, 0, 0, 0),
		uint32Option(lcpOptionMagicNumber, lcp.ownMagicNumber()),
	})
}

func TestAdoptsAnMRUSuggestionThatCarriesIPv4(t *testing.T) {
	tests := []struct {
		name       string
		suggestion configOption
		want       uint16
	}{
		{"500", option(lcpOptionMRU, 0x01, 0xF4), 500},
		{"the smallest that does, 68", option(lcpOptionMRU, 0x00, 0x44), 68},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lcp := testClientLCP()

			if err := lcp.applyNak([]configOption{test.suggestion}); err != nil {
				t.Fatal(err)
			}

			if lcp.own.mru != test.want {
				t.Errorf("MRU = %d, want %d", lcp.own.mru, test.want)
			}
		})
	}
}

func TestAnACCMNakAfterARejectProposesTheACCMAgain(t *testing.T) {
	lcp := testClientLCP()
	if err := lcp.applyReject([]configOption{option(lcpOptionACCM, 0, 0, 0, 0)}); err != nil {
		t.Fatal(err)
	}

	if err := lcp.applyNak([]configOption{option(lcpOptionACCM, 0, 0x0A, 0, 0)}); err != nil {
		t.Fatal(err)
	}

	// The default map, now in force, with what the peer asks for added.
	assertOptions(t, lcp.ownOptions(), []configOption{
		option(lcpOptionACCM, 0xFF, 0xFF, 0xFF, 0xFF),
		uint32Option(lcpOptionMagicNumber, lcp.ownMagicNumber()),
		option(lcpOptionProtocolCompression),
		option(lcpOptionAddressControlCompression),
	})
}

func TestSuggestsTheMinimumForAnMRUTooSmallForIPv4(t *testing.T) {
	assertVerdict(t, testClientLCP().judge(option(lcpOptionMRU, 0, 67)), nakVerdict([]byte{0, 68}))
	assertVerdict(t, testClientLCP().judge(option(lcpOptionMRU, 0, 68)), ackVerdict())
}

func TestThePeerOptionsResetToTheirDefaultsWithEveryRequest(t *testing.T) {
	lcp := testClientLCP()
	lcp.acceptPeerOptions([]configOption{
		option(lcpOptionMRU, 0x02, 0x40),
		option(lcpOptionAuthentication, 0xC0, 0x23),
		option(lcpOptionProtocolCompression),
	})

	lcp.acceptPeerOptions(nil)

	if lcp.peer != defaultPeerLCPOptions() {
		t.Errorf("peer options = %+v, want the defaults", lcp.peer)
	}
}

func TestAuthenticationOptionValuesRoundTrip(t *testing.T) {
	for _, protocol := range []AuthProtocol{AuthProtocolPAP, AuthProtocolCHAPMD5} {
		if got, ok := authProtocolFromOptionValue(protocol.optionValue()); !ok || got != protocol {
			t.Errorf("%v round-trips as %v, %t", protocol, got, ok)
		}
	}
	for _, unknown := range [][]byte{nil, {0xC0}, {0xC0, 0x23, 0x00}, {0xC2, 0x23}, {0xC2, 0x23, 0x81}} {
		if got, ok := authProtocolFromOptionValue(unknown); ok {
			t.Errorf("% X reads as %v", unknown, got)
		}
	}
}
