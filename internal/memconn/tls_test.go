package memconn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"testing"
	"time"
)

// What package memconn is for: crypto/tls on both ends, each in a
// goroutine of its own, with the test moving the records between them one
// step at a time, as the simulator's TLS echo and its tests do.

const serverName = "secure.test"

// selfSignedCertificate returns a certificate for serverName and a pool
// that trusts it.
func selfSignedCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: serverName},
		DNSNames:              []string{serverName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// echoServer echoes what it reads until the client says goodbye, answers
// with its own close_notify, and closes. It reports how its session ended.
func echoServer(server *tls.Conn, conn *Conn) <-chan error {
	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(server, server)
		if err == nil {
			err = server.CloseWrite()
		}
		_ = conn.Close() // Close never fails.
		ended <- err
	}()
	return ended
}

// clientOutcome is what the client goroutine saw.
type clientOutcome struct {
	echoed []byte
	// finalErr is what the last Read returned: io.EOF for close_notify.
	finalErr error
	err      error
}

// echoClient sends message, reads its echo, says goodbye, and reads until
// the server has said goodbye too.
func echoClient(client *tls.Conn, conn *Conn, message []byte) <-chan clientOutcome {
	outcomes := make(chan clientOutcome, 1)
	go func() {
		var outcome clientOutcome
		if _, outcome.err = client.Write(message); outcome.err == nil {
			outcome.echoed = make([]byte, len(message))
			_, outcome.err = io.ReadFull(client, outcome.echoed)
		}
		if outcome.err == nil {
			outcome.err = client.CloseWrite()
		}
		if outcome.err == nil {
			_, outcome.finalErr = client.Read(make([]byte, 1))
		}
		_ = conn.Close() // Close never fails.
		outcomes <- outcome
	}()
	return outcomes
}

// relay moves what from's session wrote to to's session, and waits until
// to's session has dealt with it; it reports whether there was anything.
func relay(t *testing.T, from, to *Conn) bool {
	t.Helper()
	records := make([]byte, 64*1024)
	count := from.Extract(records)
	if count == 0 {
		return false
	}
	to.Inject(records[:count])
	if !to.AwaitReader(patience) {
		t.Fatal("a session never got back to reading")
	}
	return true
}

func TestCryptoTLSRunsOverAPairOfConnsPumpedStepByStep(t *testing.T) {
	t.Parallel()
	certificate, roots := selfSignedCertificate(t)
	serverConn, clientConn := New(serverEnd, clientEnd), New(clientEnd, serverEnd)
	t.Cleanup(func() {
		// The sessions of a failed run would otherwise wait in Read for
		// good. Close never fails, and changes nothing once a session has
		// closed its Conn.
		_ = serverConn.Close()
		_ = clientConn.Close()
	})
	server := tls.Server(serverConn, &tls.Config{Certificates: []tls.Certificate{certificate}})
	client := tls.Client(clientConn, &tls.Config{RootCAs: roots, ServerName: serverName})
	message := []byte("Sealed all the way to orbit and back.")
	serverEnded := echoServer(server, serverConn)
	outcomes := echoClient(client, clientConn, message)
	if !clientConn.AwaitReader(patience) {
		t.Fatal("the client never sent its hello")
	}

	for moved := true; moved; {
		moved = relay(t, clientConn, serverConn) || relay(t, serverConn, clientConn)
	}

	outcome := awaitResult(t, outcomes)
	if outcome.err != nil || string(outcome.echoed) != string(message) {
		t.Fatalf("the client got %q, %v; want its message back", outcome.echoed, outcome.err)
	}
	if !errors.Is(outcome.finalErr, io.EOF) || clientConn.EOFDelivered() {
		t.Errorf("the client's last Read = %v with EOFDelivered %v; want the server's close_notify",
			outcome.finalErr, clientConn.EOFDelivered())
	}
	if err := awaitResult(t, serverEnded); err != nil || serverConn.EOFDelivered() {
		t.Errorf("the server ended with %v, EOFDelivered %v; want the client's close_notify", err, serverConn.EOFDelivered())
	}
}
