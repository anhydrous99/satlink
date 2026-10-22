package satnet

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/x509roots/fallback/bundle"
)

// expectTLSError checks that err is ErrTLS with text.
func expectTLSError(t *testing.T, err error, text string) {
	t.Helper()
	if !errors.Is(err, ErrTLS) || err.Error() != text {
		t.Errorf("err = %v, want ErrTLS %q", err, text)
	}
}

func TestAPEMBundleWithoutCertificatesIsRefused(t *testing.T) {
	_, err := NewTLSConnector(TLSOptions{ExtraRootsPEM: [][]byte{[]byte("no certificate here")}})

	expectTLSError(t, err, "TLS: the PEM data holds no certificate")
}

func TestAPEMBundleWithAMalformedSectionIsRefused(t *testing.T) {
	malformed := []byte("-----BEGIN CERTIFICATE-----\nnot base64!\n-----END CERTIFICATE-----\n")

	_, err := NewTLSConnector(TLSOptions{ExtraRootsPEM: [][]byte{malformed}})

	expectTLSError(t, err, "TLS: invalid PEM: a section is malformed")
}

func TestOnlyCertificateBlocksOfAPEMBundleCount(t *testing.T) {
	fixture := newTLSFixture(t)
	bundle := slices.Concat(
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a key")}),
		pem.EncodeToMemory(&pem.Block{Type: pemCertificateType, Bytes: fixture.caDER}),
	)

	connector, err := NewTLSConnector(TLSOptions{ExtraRootsPEM: [][]byte{bundle}, WithoutPublicRoots: true})
	if err != nil {
		t.Fatal(err)
	}

	secureFake(t, connector, servePeer(t, fixture.server, nil))
}

func TestAMarkerInsideALineBeginsNoSection(t *testing.T) {
	fixture := newTLSFixture(t)
	bundle := slices.Concat(
		[]byte("# each root starts with -----BEGIN CERTIFICATE-----\n"),
		pem.EncodeToMemory(&pem.Block{Type: pemCertificateType, Bytes: fixture.caDER}),
	)

	connector, err := NewTLSConnector(TLSOptions{ExtraRootsPEM: [][]byte{bundle}, WithoutPublicRoots: true})
	if err != nil {
		t.Fatal(err)
	}

	secureFake(t, connector, servePeer(t, fixture.server, nil))
}

func TestCertificatesThatDoNotParseAreRefused(t *testing.T) {
	garbled := pem.EncodeToMemory(&pem.Block{Type: pemCertificateType, Bytes: []byte("garbled")})
	for name, options := range map[string]TLSOptions{
		"PEM": {ExtraRootsPEM: [][]byte{garbled}},
		"DER": {ExtraRootsDER: [][]byte{[]byte("garbled")}},
	} {
		if _, err := NewTLSConnector(options); !errors.Is(err, ErrTLS) || !strings.HasPrefix(err.Error(), "TLS: x509: ") {
			t.Errorf("%s: NewTLSConnector = %v, want crypto/x509's error", name, err)
		}
	}
}

func TestAConnectorThatWouldTrustNothingIsRefused(t *testing.T) {
	_, err := NewTLSConnector(TLSOptions{WithoutPublicRoots: true})

	expectTLSError(t, err, "TLS: no root trust anchors were provided")
}

func TestThePublicRootsAreMozillasProgram(t *testing.T) {
	program := x509.NewCertPool()
	roots := 0
	for root := range bundle.Roots() {
		certificate, err := x509.ParseCertificate(root.Certificate)
		if err != nil {
			t.Fatalf("a root of Mozilla's program does not parse: %v", err)
		}
		program.AddCert(certificate)
		roots++
	}
	if count := publicRoots().count; count < 100 || count != roots {
		t.Errorf("%d public roots, want the %d of Mozilla's program", count, roots)
	}

	connector, err := NewTLSConnector(TLSOptions{})

	if err != nil {
		t.Fatalf("NewTLSConnector(public roots) = %v", err)
	}
	// Equal compares the certificates alone, without their constraints:
	// TestAPublicRootAnchorsNoChainIssuedAfterItsDistrustDate checks those.
	if !connector.Config().RootCAs.Equal(program) {
		t.Error("the connector does not hold exactly the roots of Mozilla's program")
	}
}

// distrustedAfter returns the constraint the bundle places on a root NSS
// phases out: a chain holding a certificate issued after date is refused.
func distrustedAfter(date time.Time) func([]*x509.Certificate) error {
	return func(chain []*x509.Certificate) error {
		for _, certificate := range chain {
			if certificate.NotBefore.After(date) {
				return fmt.Errorf("issued after %v", date)
			}
		}
		return nil
	}
}

// TestAPublicRootAnchorsNoChainIssuedAfterItsDistrustDate is Go only:
// webpki-roots, the Rust version's roots, cannot carry such a date.
func TestAPublicRootAnchorsNoChainIssuedAfterItsDistrustDate(t *testing.T) {
	fixture := newTLSFixture(t)
	server, err := x509.ParseCertificate(fixture.server.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name          string
		distrustAfter time.Time
		trusted       bool
	}{
		{"a chain issued before the date", server.NotBefore.Add(time.Minute), true},
		{"a chain issued after it", server.NotBefore.Add(-time.Minute), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			roots := rootPoolOf(slices.Values([]bundle.Root{
				{Certificate: fixture.caDER, Constraint: distrustedAfter(c.distrustAfter)},
			}))

			_, err := server.Verify(x509.VerifyOptions{DNSName: tlsTestHost, Roots: roots.pool})

			if trusted := err == nil; trusted != c.trusted {
				t.Errorf("Verify = %v; want trusted: %t", err, c.trusted)
			}
		})
	}
}

func TestAnUntrustedServerIsRefused(t *testing.T) {
	fixture := newTLSFixture(t)
	publicRootsOnly, err := NewTLSConnector(TLSOptions{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = publicRootsOnly.secure(servePeer(t, fixture.server, nil).tcp, tlsTestHost, time.Now().Add(waitBudget))

	if !errors.Is(err, ErrTLS) {
		t.Errorf("secure = %v, want ErrTLS", err)
	}
}

func TestACertificateForAnotherNameIsRefusedPinnedOrNot(t *testing.T) {
	fixture := newTLSFixture(t)
	for name, pins := range map[string][][sha256.Size]byte{
		"without pins":          nil,
		"with the server's pin": {fixture.serverKeySHA256},
	} {
		t.Run(name, func(t *testing.T) {
			peer := servePeer(t, fixture.server, nil)

			// The certificate the connector trusts is for tlsTestHost.
			_, err := fixture.connector(t, pins...).secure(peer.tcp, "other.test", time.Now().Add(waitBudget))

			var wrongName x509.HostnameError
			if !errors.Is(err, ErrTLS) || !errors.As(err, &wrongName) {
				t.Errorf("secure(other.test) = %v, want the name refused", err)
			}
			if !peer.tcp.isClientClosed() {
				t.Error("the connection of a refused server is left open")
			}
		})
	}
}

func TestTLS12IsOfferedWithRustlssSuitesOnly(t *testing.T) {
	connector, err := NewTLSConnector(TLSOptions{})
	if err != nil {
		t.Fatal(err)
	}

	config := connector.Config()

	want := []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	}
	if !slices.Equal(config.CipherSuites, want) {
		t.Errorf("TLS 1.2 suites %v, want rustls's %v", config.CipherSuites, want)
	}
}

func TestATLS12ServerThatOffersOnlyCBCIsRefused(t *testing.T) {
	fixture := newTLSFixture(t)
	server := fixture.server.Clone()
	server.MaxVersion = tls.VersionTLS12
	server.CipherSuites = []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA}

	_, err := fixture.connector(t).secure(servePeer(t, server, nil).tcp, tlsTestHost, time.Now().Add(waitBudget))

	if !errors.Is(err, ErrTLS) {
		t.Errorf("secure = %v, want no suite agreed on", err)
	}
}

func TestTheConfigurationOffersNoPostQuantumKeyShareAndFullRecords(t *testing.T) {
	connector, err := NewTLSConnector(TLSOptions{ALPNProtocols: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}

	config := connector.Config()

	want := []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}
	if !slices.Equal(config.CurvePreferences, want) || !config.DynamicRecordSizingDisabled || config.MinVersion != tls.VersionTLS12 {
		t.Errorf("curves %v, records sized dynamically: %t, minimum %x",
			config.CurvePreferences, !config.DynamicRecordSizingDisabled, config.MinVersion)
	}
	if config.ClientSessionCache == nil || config.ClientSessionCache != connector.config.ClientSessionCache {
		t.Error("the copy does not share the connector's session cache")
	}
	if config == connector.config || !slices.Equal(config.NextProtos, []string{"h2"}) {
		t.Errorf("Config() is not a copy, or offers %v", config.NextProtos)
	}
}

func TestTheConnectorKeepsItsOwnCopyOfTheOptions(t *testing.T) {
	fixture := newTLSFixture(t)
	options := TLSOptions{
		ExtraRootsDER:      [][]byte{fixture.caDER},
		WithoutPublicRoots: true,
		PinnedPublicKeys:   [][32]byte{fixture.serverKeySHA256},
		ALPNProtocols:      []string{"h2"},
	}
	connector, err := NewTLSConnector(options)
	if err != nil {
		t.Fatal(err)
	}

	options.PinnedPublicKeys[0] = [32]byte{}
	options.ALPNProtocols[0] = "changed"

	secureFake(t, connector, servePeer(t, fixture.server, nil))
	if connector.config.NextProtos[0] != "h2" {
		t.Errorf("the connector offers %v", connector.config.NextProtos)
	}
}

func TestServerNamesFollowRustlsRules(t *testing.T) {
	for name, valid := range map[string]bool{
		"secure.test":           true,
		"Secure.Test.":          true,
		"1password.com":         true,
		"_service.example":      true,
		"192.0.2.1":             true,
		"2001:db8::1":           true,
		"bad name":              false,
		"1.2.3":                 false,
		"example.123":           false,
		"":                      false,
		"-bad.example":          false,
		"fe80::1%eth0":          false,
		strings.Repeat("a", 64): false,
	} {
		if got := isValidServerName(name); got != valid {
			t.Errorf("isValidServerName(%q) = %t, want %t", name, got, valid)
		}
	}
}

func TestAnInvalidServerNameIsRefusedBeforeAnythingIsSent(t *testing.T) {
	fixture := newTLSFixture(t)
	for _, name := range []string{"bad name", "1.2.3"} {
		tcp := newFakeTCP(fakeSendRoom)
		t.Cleanup(func() { closeBoth(tcp) })

		_, err := fixture.connector(t).secure(tcp, name, time.Now().Add(waitBudget))

		var hostErr *Error
		if !errors.As(err, &hostErr) || hostErr.Kind != ErrInvalidHost || hostErr.Host != name {
			t.Errorf("secure(%q) = %v, want ErrInvalidHost", name, err)
		}
		if tcp.waitingForServer() != 0 || !tcp.isClientClosed() {
			t.Errorf("secure(%q) sent something or left the connection open", name)
		}
	}
}

func TestAHandshakeThatTimesOutReportsTheTransportsTimeout(t *testing.T) {
	fixture := newTLSFixture(t)
	tcp := newFakeTCP(fakeSendRoom)
	t.Cleanup(func() { closeBoth(tcp) })

	_, err := fixture.connector(t).secure(tcp, tlsTestHost, time.Now().Add(shortWait))

	var timeout *Error
	if !errors.As(err, &timeout) || timeout.Kind != ErrTimedOut || timeout.Operation != operationTCPRead {
		t.Errorf("secure = %v, want a TCP read timeout", err)
	}
}

func TestTheGoodbyeWaitsForRoomWithinTheWriteDeadline(t *testing.T) {
	fixture := newTLSFixture(t)
	start := make(chan struct{})
	received := make(chan int, 1)
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		if !peer.awaitStart(start) {
			return
		}
		// ReadAll ends at the close_notify; anything else shows in the
		// length.
		data, err := io.ReadAll(peer.session)
		if err != nil {
			received <- -1
			return
		}
		received <- len(data)
	})
	conn := secureFake(t, fixture.connector(t), peer)
	message := bytes.Repeat([]byte{0x42}, 20_000)
	if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(message); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Write = %v, want a timeout with the server not reading", err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(waitBudget)); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- conn.Close() }()

	close(start)

	if err := <-closed; err != nil {
		t.Errorf("Close = %v", err)
	}
	if got := <-received; got != len(message) {
		t.Errorf("the server received %d bytes before the goodbye, want %d", got, len(message))
	}
}

func TestAGoodbyeThatDoesNotFitInTimeIsDropped(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, echoOnceStarted(make(chan struct{})))
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(make([]byte, 20_000)); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Write = %v, want a timeout", err)
	}

	err := conn.Close()

	if !errors.Is(err, ErrTimedOut) || !peer.tcp.isClientClosed() {
		t.Errorf("Close = %v with the connection closed: %t; want a timeout, and closed", err, peer.tcp.isClientClosed())
	}
}
