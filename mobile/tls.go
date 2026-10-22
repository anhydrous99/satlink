package mobile

import (
	"crypto/sha256"
	"slices"
	"sync"

	"github.com/anhydrous99/satlink/satnet"
)

// TLSSettings are the trust settings for a [TLSConnector]. Create them with
// NewTLSSettings, which gomobile makes the constructor.
//
// NewTLSConnector reads them once, as it starts: changing them afterwards
// changes nothing for its connector. The Add methods may be called from any
// thread meanwhile; TrustPublicRoots, though, must not be set while another
// thread hands the settings to NewTLSConnector.
type TLSSettings struct {
	// TrustPublicRoots trusts the public web PKI (Mozilla's root program).
	TrustPublicRoots bool

	// mu guards the lists the Add methods fill: the app may add to them
	// from one thread while another makes a connector, which takes a copy.
	mu               sync.Mutex
	extraRootsPEM    []string
	pinnedPublicKeys [][]byte
	alpnProtocols    []string
}

// NewTLSSettings returns settings that trust the public web PKI, pin
// nothing and offer no ALPN.
func NewTLSSettings() *TLSSettings {
	return &TLSSettings{TrustPublicRoots: true}
}

// AddRootCertificatesPEM also trusts the certificates of a PEM bundle, such
// as a private CA. It is checked when the connector is made.
func (s *TLSSettings) AddRootCertificatesPEM(pem string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extraRootsPEM = append(s.extraRootsPEM, pem)
}

// AddPinnedPublicKeySHA256 also requires, once any pin is added, the
// server's key to match one of the pins: the SHA-256 of its DER
// SubjectPublicKeyInfo, 32 bytes. It is checked when the connector is made.
//
// Pin the key of every certificate the server holds: one that holds
// several may send satlink another one than it sends a client with other
// signature preferences, since the client's offered algorithms decide which
// certificate it sends (see satnet.TLSOptions.PinnedPublicKeys).
func (s *TLSSettings) AddPinnedPublicKeySHA256(sha256 []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pinnedPublicKeys = append(s.pinnedPublicKeys, slices.Clone(sha256))
}

// AddALPNProtocol also offers protocol through ALPN; the first added is the
// most preferred.
func (s *TLSSettings) AddALPNProtocol(protocol string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alpnProtocols = append(s.alpnProtocols, protocol)
}

// copied returns a copy of the settings, whose lists no other thread
// changes, so that the checks NewTLSConnector makes, in their order, all see
// the same settings.
func (s *TLSSettings) copied() *TLSSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &TLSSettings{
		TrustPublicRoots: s.TrustPublicRoots,
		extraRootsPEM:    slices.Clone(s.extraRootsPEM),
		pinnedPublicKeys: slices.Clone(s.pinnedPublicKeys),
		alpnProtocols:    slices.Clone(s.alpnProtocols),
	}
}

// pins returns the pinned keys as satnet takes them, refusing a pin that is
// not a SHA-256 hash.
func (s *TLSSettings) pins() ([][sha256.Size]byte, error) {
	pins := make([][sha256.Size]byte, len(s.pinnedPublicKeys))
	for i, pin := range s.pinnedPublicKeys {
		if len(pin) != sha256.Size {
			return nil, invalidArgument("a SHA-256 pin is 32 bytes")
		}
		pins[i] = [sha256.Size]byte(pin)
	}
	return pins, nil
}

// options returns satnet's options for these settings, with pins.
func (s *TLSSettings) options(pins [][sha256.Size]byte) satnet.TLSOptions {
	roots := make([][]byte, len(s.extraRootsPEM))
	for i, pem := range s.extraRootsPEM {
		roots[i] = []byte(pem)
	}
	return satnet.TLSOptions{
		ExtraRootsPEM:      roots,
		WithoutPublicRoots: !s.TrustPublicRoots,
		PinnedPublicKeys:   pins,
		ALPNProtocols:      slices.Clone(s.alpnProtocols),
	}
}

// trustsNothing reports whether these settings give no root at all: then,
// and only then, satnet refuses them for trusting nothing, as every PEM
// bundle it accepts holds a certificate.
func (s *TLSSettings) trustsNothing() bool {
	return !s.TrustPublicRoots && len(s.extraRootsPEM) == 0
}

// TLSConnector makes TLS connections. Create one and reuse it: its session
// cache makes later handshakes with the same server several times cheaper.
// It is safe for use from several threads, so one can serve the whole app.
//
// In Objective-C and Swift, make one with SatlinkMobileNewTLSConnector,
// never with the init it inherits from NSObject: one made so has no Go
// side, and its first use ends the app. scripts/build-bindings.sh marks
// that init unavailable.
type TLSConnector struct {
	// connector is the connector itself; nil in a zero value.
	connector *satnet.TLSConnector
}

// NewTLSConnector returns a connector with settings' trust settings. It
// fails with SatlinkErrorTLS for certificates that do not parse, with
// SatlinkErrorInvalidArgument for a pin that is not 32 bytes ("a SHA-256 pin
// is 32 bytes"), and with SatlinkErrorTLS for settings that trust nothing,
// checked in that order.
func NewTLSConnector(settings *TLSSettings) (connector *TLSConnector, err error) {
	defer recoverPanic(&err)
	if settings == nil {
		return nil, nilArgument("settings")
	}
	trust := settings.copied()
	// satnet checks the PEM bundles and whether anything is trusted in one
	// go, after the pins are read.
	pins, pinErr := trust.pins()
	built, err := satnet.NewTLSConnector(trust.options(pins))
	switch {
	case err != nil && !trust.trustsNothing():
		return nil, fromError(err) // a PEM bundle that does not parse
	case pinErr != nil:
		return nil, pinErr
	case err != nil:
		return nil, fromError(err) // nothing is trusted
	}
	return &TLSConnector{connector: built}, nil
}
