package mobile

import (
	"bytes"
	"crypto/sha256"
	"slices"
	"testing"

	"github.com/anhydrous99/satlink/sim"
)

func TestTLSSettingsBecomeTheConnectorsOptions(t *testing.T) {
	settings := NewTLSSettings()
	settings.AddRootCertificatesPEM("first bundle")
	pin := bytes.Repeat([]byte{7}, sha256.Size)
	settings.AddPinnedPublicKeySHA256(pin)
	settings.AddALPNProtocol("h2")
	settings.AddALPNProtocol("http/1.1")

	pins, err := settings.pins()
	options := settings.options(pins)

	if err != nil {
		t.Fatalf("pins(): %v", err)
	}
	if options.WithoutPublicRoots || len(options.ExtraRootsPEM) != 1 || string(options.ExtraRootsPEM[0]) != "first bundle" {
		t.Errorf("roots: without public %t, extra %q", options.WithoutPublicRoots, options.ExtraRootsPEM)
	}
	if len(options.PinnedPublicKeys) != 1 || !bytes.Equal(options.PinnedPublicKeys[0][:], pin) {
		t.Errorf("pins are %x", options.PinnedPublicKeys)
	}
	if !slices.Equal(options.ALPNProtocols, []string{"h2", "http/1.1"}) {
		t.Errorf("ALPN protocols are %q", options.ALPNProtocols)
	}
}

func TestNotTrustingPublicRootsLeavesThemOut(t *testing.T) {
	settings := NewTLSSettings()
	settings.TrustPublicRoots = false

	options := settings.options(nil)

	if !options.WithoutPublicRoots {
		t.Errorf("options() = %+v, want them without public roots", options)
	}
}

func TestAPinIsCopiedWhenAdded(t *testing.T) {
	settings := NewTLSSettings()
	pin := bytes.Repeat([]byte{7}, sha256.Size)
	settings.AddPinnedPublicKeySHA256(pin)

	pin[0] = 8
	pins, err := settings.pins()

	if err != nil || pins[0][0] != 7 {
		t.Errorf("the pin follows the app's array: %x, %v", pins, err)
	}
}

func TestAPinThatIsNotASHA256HashIsRefusedBeforeTheConnectorIsMade(t *testing.T) {
	for _, length := range []int{0, sha256.Size - 1, sha256.Size + 1} {
		settings := NewTLSSettings()
		settings.AddPinnedPublicKeySHA256(make([]byte, length))

		_, err := NewTLSConnector(settings)

		assertSatlinkError(t, err,
			&SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "a SHA-256 pin is 32 bytes"},
			"invalid argument: a SHA-256 pin is 32 bytes")
	}
}

func TestCertificatesThatDoNotParseAreATLSError(t *testing.T) {
	settings := NewTLSSettings()
	settings.AddRootCertificatesPEM("not a certificate")

	_, err := NewTLSConnector(settings)

	if details := AsSatlinkError(err); details == nil || details.Kind != SatlinkErrorTLS {
		t.Errorf("NewTLSConnector fails with %v, want SatlinkErrorTLS", err)
	}
}

func TestTLSSettingsAreCheckedInTheRustVersionsOrder(t *testing.T) {
	tests := []struct {
		name                     string
		badPEM, badPin, noPublic bool
		want                     SatlinkError
	}{
		{name: "a PEM bundle before a pin", badPEM: true, badPin: true,
			want: SatlinkError{Kind: SatlinkErrorTLS, Reason: "the PEM data holds no certificate"}},
		{name: "a pin before trusting nothing", badPin: true, noPublic: true,
			want: SatlinkError{Kind: SatlinkErrorInvalidArgument, Reason: "a SHA-256 pin is 32 bytes"}},
		{name: "trusting nothing", noPublic: true,
			want: SatlinkError{Kind: SatlinkErrorTLS, Reason: "no root trust anchors were provided"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := NewTLSSettings()
			settings.TrustPublicRoots = !test.noPublic
			if test.badPEM {
				settings.AddRootCertificatesPEM("not a certificate")
			}
			if test.badPin {
				settings.AddPinnedPublicKeySHA256(make([]byte, sha256.Size-1))
			}

			_, err := NewTLSConnector(settings)

			if got := AsSatlinkError(err); got == nil || *got != test.want {
				t.Errorf("NewTLSConnector fails with %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestTrustMayBeAddedWhileAConnectorReadsTheSettings(t *testing.T) {
	settings := NewTLSSettings()
	settings.TrustPublicRoots = false
	settings.AddRootCertificatesPEM(sim.SecureTestCAPEM())
	pin := bytes.Repeat([]byte{7}, sha256.Size)
	adding := make(chan struct{})
	go func() {
		defer close(adding)
		// One more root only: every connector parses them all.
		settings.AddRootCertificatesPEM(sim.SecureTestCAPEM())
		for range changesMeanwhile {
			settings.AddPinnedPublicKeySHA256(pin)
			settings.AddALPNProtocol("h2")
		}
	}()

	for range changesMeanwhile {
		if _, err := NewTLSConnector(settings); err != nil {
			t.Fatalf("NewTLSConnector: %v", err)
		}
	}

	<-adding
}

func TestAConnectorTrustingAPrivateCAIsMade(t *testing.T) {
	settings := NewTLSSettings()
	settings.TrustPublicRoots = false
	settings.AddRootCertificatesPEM(sim.SecureTestCAPEM())

	connector, err := NewTLSConnector(settings)

	if err != nil || connector.connector == nil {
		t.Errorf("NewTLSConnector() = %v, %v, want a connector", connector, err)
	}
}
