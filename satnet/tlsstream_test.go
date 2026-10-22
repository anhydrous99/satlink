package satnet

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"slices"
	"testing"
	"time"
)

// tlsTestHost is the name on the test server's certificate.
const tlsTestHost = "tls.test"

// certificateValidity is how long around now the test certificates are
// valid.
const certificateValidity = time.Hour

// shortWait is a deadline the TLS tests expect to pass.
const shortWait = 100 * time.Millisecond

// tlsFixture is a test CA, a server certificate it issued for tlsTestHost,
// and what trusts it.
type tlsFixture struct {
	caDER []byte
	// ca issues the server's certificates, and issued counts them, so that
	// each has a serial number of its own.
	ca     issuer
	issued int64
	// server is the server's configuration, shared by its sessions so
	// that their tickets let later sessions resume.
	server *tls.Config
	// serverKeySHA256 is the pin of the server's public key.
	serverKeySHA256 [sha256.Size]byte
}

// newTLSFixture makes a CA and a server certificate with P-256 keys, valid
// around now.
func newTLSFixture(t *testing.T) *tlsFixture {
	t.Helper()
	return newTLSFixtureWithKeys(t, generateKey(t), generateKey(t))
}

// newTLSFixtureWithKeys makes a CA with caKey and a server certificate for
// serverKey, valid around now.
func newTLSFixtureWithKeys(t *testing.T, caKey, serverKey crypto.Signer) *tlsFixture {
	t.Helper()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "satnet test CA"},
		NotBefore:             time.Now().Add(-certificateValidity),
		NotAfter:              time.Now().Add(certificateValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER := createCertificate(t, caTemplate, caKey.Public(), issuer{caTemplate, caKey})
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &tlsFixture{caDER: caDER, ca: issuer{ca, caKey}, serverKeySHA256: publicKeySHA256(t, serverKey.Public())}
	fixture.server = &tls.Config{
		Certificates: []tls.Certificate{fixture.serverCertificate(t, serverKey)},
		MinVersion:   tls.VersionTLS12,
	}
	return fixture
}

// publicKeySHA256 returns the pin of key: the SHA-256 of its DER
// SubjectPublicKeyInfo.
func publicKeySHA256(t *testing.T, key crypto.PublicKey) [sha256.Size]byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(der)
}

// serverCertificate returns a certificate for tlsTestHost and key, valid
// around now, issued by the fixture's CA.
func (f *tlsFixture) serverCertificate(t *testing.T, key crypto.Signer) tls.Certificate {
	t.Helper()
	f.issued++
	template := &x509.Certificate{
		// The CA's own certificate is number 1.
		SerialNumber: big.NewInt(1 + f.issued),
		Subject:      pkix.Name{CommonName: tlsTestHost},
		DNSNames:     []string{tlsTestHost},
		NotBefore:    time.Now().Add(-certificateValidity),
		NotAfter:     time.Now().Add(certificateValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der := createCertificate(t, template, key.Public(), f.ca)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// generateKey returns a new P-256 key.
func generateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	return generateECDSAKey(t, elliptic.P256())
}

// generateECDSAKey returns a new ECDSA key on curve.
func generateECDSAKey(t *testing.T, curve elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// generateRSAKey returns a new RSA key of bits bits.
func generateRSAKey(t *testing.T, bits int) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// generateEd25519Key returns a new Ed25519 key.
func generateEd25519Key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// generateMLDSAKey returns a new ML-DSA-65 key.
func generateMLDSAKey(t *testing.T) *mldsa.PrivateKey {
	t.Helper()
	key, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// issuer is who signs a certificate: a CA's certificate and key.
type issuer struct {
	certificate *x509.Certificate
	key         crypto.Signer
}

// createCertificate returns template, for publicKey, signed by by.
func createCertificate(t *testing.T, template *x509.Certificate, publicKey crypto.PublicKey, by issuer) []byte {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, template, by.certificate, publicKey, by.key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// connector returns a connector that trusts only the fixture's CA, with
// options' pins.
func (f *tlsFixture) connector(t *testing.T, pins ...[sha256.Size]byte) *TLSConnector {
	t.Helper()
	connector, err := NewTLSConnector(TLSOptions{
		ExtraRootsDER:      [][]byte{f.caDER},
		WithoutPublicRoots: true,
		PinnedPublicKeys:   pins,
	})
	if err != nil {
		t.Fatal(err)
	}
	return connector
}

// tlsPeer is a TLS server over the server end of a fake TCP connection,
// serving one session in a goroutine.
type tlsPeer struct {
	tcp *fakeTCP
	// session is the server's side of the session, and raw the TCP
	// connection under it.
	session *tls.Conn
	raw     net.Conn
	// handshook is closed once the server's handshake has succeeded.
	handshook chan struct{}
	// stop is closed when the test ends, and done once the server has.
	stop chan struct{}
	done chan struct{}
}

// servePeer starts a server with config over a new fake TCP connection;
// once its handshake is done it runs behave, if any. The test's end closes
// both ends and waits for the server.
func servePeer(t *testing.T, config *tls.Config, behave func(peer *tlsPeer)) *tlsPeer {
	t.Helper()
	return servePeerOver(t, newFakeTCP(fakeSendRoom), config, behave)
}

// servePeerOver starts a server with config, as servePeer does, over tcp.
func servePeerOver(t *testing.T, tcp *fakeTCP, config *tls.Config, behave func(peer *tlsPeer)) *tlsPeer {
	t.Helper()
	peer := &tlsPeer{
		tcp:       tcp,
		raw:       tcp.server(),
		handshook: make(chan struct{}),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	peer.session = tls.Server(peer.raw, config)
	go func() {
		defer close(peer.done)
		if err := peer.session.Handshake(); err != nil {
			return
		}
		close(peer.handshook)
		if behave != nil {
			behave(peer)
		}
	}()
	t.Cleanup(func() {
		close(peer.stop)
		closeBoth(peer.tcp)
		<-peer.done
	})
	return peer
}

// awaitStart waits until start is closed, and reports false if the test
// ended first.
func (p *tlsPeer) awaitStart(start <-chan struct{}) bool {
	select {
	case <-start:
		return true
	case <-p.stop:
		return false
	}
}

// closeBoth closes both ends of tcp.
func closeBoth(tcp *fakeTCP) {
	// Closing the fake's ends cannot fail.
	_ = tcp.Close()
	_ = tcp.server().Close()
}

// echo copies what the client sends back to it until the session ends.
func echo(peer *tlsPeer) {
	// The copy ends however the session does, which the client reports.
	_, _ = io.Copy(peer.session, peer.session)
}

// echoOnceStarted echoes once start is closed: until then the server reads
// nothing, so that the client's records pile up.
func echoOnceStarted(start <-chan struct{}) func(*tlsPeer) {
	return func(peer *tlsPeer) {
		if peer.awaitStart(start) {
			echo(peer)
		}
	}
}

// sendRaw writes bytes on the server's TCP connection, outside the session.
func (p *tlsPeer) sendRaw(data []byte) {
	// The client reports what came of it.
	_, _ = p.raw.Write(data)
}

// secureFake secures peer's connection for tlsTestHost with connector,
// failing the test unless the handshake succeeds at both ends. It returns
// once the server has read the client's last handshake message, before the
// test sends anything: the server's last handshake read would otherwise
// take in records sent after it, and so make room in the connection that
// the test does not expect.
func secureFake(t *testing.T, connector *TLSConnector, peer *tlsPeer) *TLSConn {
	t.Helper()
	return secureFakeFor(t, connector, peer, tlsTestHost)
}

// secureFakeFor secures peer's connection for serverName, as secureFake
// does for tlsTestHost.
func secureFakeFor(t *testing.T, connector *TLSConnector, peer *tlsPeer, serverName string) *TLSConn {
	t.Helper()
	conn, err := connector.secure(peer.tcp, serverName, time.Now().Add(waitBudget))
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	select {
	case <-peer.handshook:
	case <-time.After(waitBudget):
		t.Fatal("the server's handshake never ended")
	}
	return conn
}

// readExactly reads len(want) bytes from conn within waitBudget, and checks
// them.
func readExactly(t *testing.T, conn *TLSConn, want []byte) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(waitBudget)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read %d bytes: %v", len(want), err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read %q, want %q", got, want)
	}
}

func TestATLSSessionEchoesOverTheBuffer(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, echo)
	conn := secureFake(t, fixture.connector(t), peer)
	message := bytes.Repeat([]byte("0123456789"), 300)

	if _, err := conn.Write(message); err != nil {
		t.Fatal(err)
	}

	readExactly(t, conn, message)
	if conn.PeerAddr() != fakeServerAddress || conn.LocalAddr().String() != fakeClientAddress.String() ||
		conn.RemoteAddr().String() != fakeServerAddress.String() {
		t.Errorf("addresses %v, %v, %v", conn.PeerAddr(), conn.LocalAddr(), conn.RemoteAddr())
	}
}

func TestABareCloseIsATruncation(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, func(*tlsPeer) {})
	conn := secureFake(t, fixture.connector(t), peer)
	<-peer.done

	peer.tcp.finishFromServer()

	_, err := conn.Read(make([]byte, 1))
	if !errors.Is(err, ErrTLSTruncated) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Read = %v, want ErrTLSTruncated matching io.ErrUnexpectedEOF", err)
	}
	if _, again := conn.Read(make([]byte, 1)); !errors.Is(again, ErrTLSTruncated) {
		t.Errorf("a second Read = %v", again)
	}
}

// truncatedRecord returns the header of an application data record, then
// less than it announces.
func truncatedRecord() []byte {
	return []byte{0x17, 0x03, 0x03, 0x00, 0x40, 1, 2, 3}
}

func TestACloseMidRecordIsATruncation(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		peer.sendRaw(truncatedRecord())
	})
	conn := secureFake(t, fixture.connector(t), peer)
	<-peer.done

	peer.tcp.finishFromServer()

	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, ErrTLSTruncated) {
		t.Errorf("Read = %v, want ErrTLSTruncated", err)
	}
}

// trickleSendRoom is a TCP send buffer that takes a few bytes of records at
// a time, so that the client's last flight does not leave with the write
// that queues it.
const trickleSendRoom = 16

func TestTheHandshakeIsDoneOnceTheClientsFinishedHasLeft(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeerOver(t, newFakeTCP(trickleSendRoom), fixture.server, nil)

	conn, err := fixture.connector(t).secure(peer.tcp, tlsTestHost, time.Now().Add(waitBudget))

	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if waiting := sessionBufferLimit - conn.records.room(); waiting != 0 {
		t.Errorf("%d bytes of records still wait, the client's Finished among them: the server cannot finish its handshake", waiting)
	}
}

// servePeerStoppingAfterItsFlight starts a server with config over tcp, as
// servePeerOver does, which stops once it has sent its flight, before it
// reads the client's, and reads nothing more until the test ends. The
// channel it returns is closed when the server stops.
func servePeerStoppingAfterItsFlight(t *testing.T, tcp *fakeTCP, config *tls.Config) <-chan struct{} {
	t.Helper()
	stopped, resume := make(chan struct{}), make(chan struct{})
	config = config.Clone()
	// A TLS 1.3 server, as the fixture's is, calls it between the two: its
	// flight sent, the client's still to read.
	config.VerifyConnection = func(tls.ConnectionState) error {
		close(stopped)
		<-resume
		return nil
	}
	servePeerOver(t, tcp, config, nil)
	// Cleanups run last in, first out: the server resumes before the
	// peer's cleanup waits for it to end.
	t.Cleanup(func() { close(resume) })
	return stopped
}

// TestAHandshakeWhoseFinishedCannotLeaveInTimeFails is Go only. As Rust's
// connect does, the handshake fails when the client's last flight, its
// Finished among it, cannot leave by the deadline: the server has yet to
// finish its handshake.
func TestAHandshakeWhoseFinishedCannotLeaveInTimeFails(t *testing.T) {
	fixture := newTLSFixture(t)
	tcp := newFakeTCP(trickleSendRoom)
	serverStopped := servePeerStoppingAfterItsFlight(t, tcp, fixture.server)
	connector := fixture.connector(t)
	secured := make(chan error, 1)
	go func() {
		_, err := connector.secure(tcp, tlsTestHost, time.Now().Add(waitBudget))
		secured <- err
	}()
	select {
	case <-serverStopped:
	case <-time.After(waitBudget):
		t.Fatal("the server never sent its flight")
	}

	// The deadline passes while the client's Finished waits for room.
	if err := tcp.SetWriteDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}

	var timedOut *Error
	if err := awaitResult(t, secured); !errors.As(err, &timedOut) || timedOut.Kind != ErrTimedOut ||
		timedOut.Operation != operationTCPWrite {
		t.Errorf("secure = %v, want a TCP write timeout: the client's Finished never left", err)
	}
	if !tcp.isClientClosed() {
		t.Error("the connection of a failed handshake is left open")
	}
}

func TestACloseDuringTheHandshakeSaysSo(t *testing.T) {
	fixture := newTLSFixture(t)
	tcp := newFakeTCP(fakeSendRoom)
	t.Cleanup(func() { closeBoth(tcp) })
	tcp.finishFromServer()

	_, err := fixture.connector(t).secure(tcp, tlsTestHost, time.Now().Add(waitBudget))

	if err == nil || err.Error() != "TLS: the server closed the connection during the handshake" || !errors.Is(err, ErrTLS) {
		t.Errorf("secure = %v, want the server closing during the handshake", err)
	}
	if !tcp.isClientClosed() {
		t.Error("the connection of a failed handshake is left open")
	}
}

func TestACloseNotifyIsTheEndOfTheStream(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		_, _ = peer.session.Write([]byte("bye"))
		_ = peer.session.Close()
	})
	conn := secureFake(t, fixture.connector(t), peer)

	readExactly(t, conn, []byte("bye"))

	if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("Read = %v, want io.EOF", err)
	}
}

// TestTakingInTheServersEndFailsNothing has the server answer and end the
// stream before the client takes in what came, as WaitAcknowledged and Close
// do: the end is a Read's to report, and the session has not failed.
func TestTakingInTheServersEndFailsNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		end  func(peer *tlsPeer)
		// endOfStream is what a Read reports once the answer is read.
		endOfStream error
	}{
		{"a close_notify", func(peer *tlsPeer) { _ = peer.session.Close() }, io.EOF},
		{"a FIN after a record", func(peer *tlsPeer) { peer.tcp.finishFromServer() }, ErrTLSTruncated},
		{"a FIN inside a record", func(peer *tlsPeer) {
			peer.sendRaw(truncatedRecord())
			peer.tcp.finishFromServer()
		}, ErrTLSTruncated},
	} {
		t.Run(c.name, func(t *testing.T) {
			fixture := newTLSFixture(t)
			peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
				_, _ = peer.session.Write([]byte("answer"))
				c.end(peer)
			})
			conn := secureFake(t, fixture.connector(t), peer)
			<-peer.done

			err := conn.WaitAcknowledged(time.Now().Add(waitBudget))

			if err != nil {
				t.Fatalf("WaitAcknowledged = %v, want nil: the stream ended, and nothing failed", err)
			}
			readExactly(t, conn, []byte("answer"))
			if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, c.endOfStream) || errors.Is(err, ErrTLS) {
				t.Errorf("Read after the answer = %v, want %v", err, c.endOfStream)
			}
			sentBefore := peer.tcp.waitingForServer()
			if err := conn.Close(); err != nil || peer.tcp.waitingForServer() == sentBefore {
				t.Errorf("Close = %v, having sent %d bytes; want nil, and a close_notify of our own",
					err, peer.tcp.waitingForServer()-sentBefore)
			}
		})
	}
}

func TestAfterCloseWriteThePeerReadsCloseNotifyThenTheEnd(t *testing.T) {
	fixture := newTLSFixture(t)
	peerSaw := make(chan error, 2)
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		_, err := peer.session.Read(make([]byte, 1))
		peerSaw <- err
		_, err = peer.raw.Read(make([]byte, 1))
		peerSaw <- err
		_, _ = peer.session.Write([]byte("still here"))
	})
	conn := secureFake(t, fixture.connector(t), peer)

	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	if err := <-peerSaw; err != io.EOF {
		t.Errorf("the peer's TLS read = %v, want io.EOF for close_notify", err)
	}
	if err := <-peerSaw; err != io.EOF {
		t.Errorf("the peer's TCP read = %v, want io.EOF", err)
	}
	readExactly(t, conn, []byte("still here"))
}

func TestAReadThatTimedOutLeavesTheSessionUsable(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, echo)
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.SetReadDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}

	_, err := conn.Read(make([]byte, 1))

	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() || !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Read = %v, want a timeout", err)
	}
	if _, err := conn.Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	readExactly(t, conn, []byte("again"))
}

func TestAWriteThatTimedOutLeavesItsRecordsForTheNextCall(t *testing.T) {
	fixture := newTLSFixture(t)
	startEcho := make(chan struct{})
	peer := servePeer(t, fixture.server, echoOnceStarted(startEcho))
	conn := secureFake(t, fixture.connector(t), peer)
	message := bytes.Repeat([]byte{0x5A}, 30_000)
	if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}

	taken, err := conn.Write(message)

	if !errors.Is(err, ErrTimedOut) || taken != len(message) {
		t.Fatalf("Write = %d, %v; want all taken, and a timeout", taken, err)
	}
	close(startEcho)
	readExactly(t, conn, message)
}

// tls13RecordOverhead is what a TLS 1.3 record adds to its plaintext: a
// 5-byte header, the content type and a 16-byte AEAD tag (RFC 8446 §5.2).
const tls13RecordOverhead = 5 + 1 + 16

// expectSessionFull checks that conn's session holds as many records as it
// may, and beyond its limit at most the overhead of the last: the last
// chunk a write hands crypto/tls fills the room left, and its record is
// that much larger.
func expectSessionFull(t *testing.T, conn *TLSConn) {
	t.Helper()
	room := conn.records.room()
	if room > 0 {
		t.Errorf("the session has room for %d more bytes", room)
	}
	if room < -tls13RecordOverhead {
		t.Errorf("the session holds %d bytes beyond its limit, more than a record's overhead", -room)
	}
}

func TestAFullRecordBufferMakesAWriteWait(t *testing.T) {
	fixture := newTLSFixture(t)
	startEcho := make(chan struct{})
	peer := servePeer(t, fixture.server, echoOnceStarted(startEcho))
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}

	taken, err := conn.Write(make([]byte, 100_000))

	if !errors.Is(err, ErrTimedOut) || taken == 0 || taken >= 100_000 {
		t.Fatalf("Write = %d, %v; want part of it taken, and a timeout", taken, err)
	}
	expectSessionFull(t, conn)
	close(startEcho)
}

func TestWriteSomeTakesWhatTheSessionHasRoomForAndNoMore(t *testing.T) {
	fixture := newTLSFixture(t)
	startEcho := make(chan struct{})
	peer := servePeer(t, fixture.server, echoOnceStarted(startEcho))
	conn := secureFake(t, fixture.connector(t), peer)
	message := bytes.Repeat([]byte{0x5A}, 100_000)
	if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}

	taken, err := conn.WriteSome(message)
	_, fullErr := conn.WriteSome(message[taken:])

	// What TCP had no room for by the deadline is no failure: it waits in
	// the session for the next calls.
	if err != nil || taken == 0 || taken >= len(message) {
		t.Fatalf("WriteSome = %d, %v; want part of it taken, and no error", taken, err)
	}
	expectSessionFull(t, conn)
	if !errors.Is(fullErr, ErrTimedOut) {
		t.Errorf("WriteSome with the session full = %v, want ErrTimedOut", fullErr)
	}
	close(startEcho)
	readExactly(t, conn, message[:taken])
}

func TestAWriteSomeWhoseConnectionIsResetAfterTakingSaysHowMuchItTook(t *testing.T) {
	fixture := newTLSFixture(t)
	startEcho := make(chan struct{})
	peer := servePeer(t, fixture.server, echoOnceStarted(startEcho))
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.SetWriteDeadline(time.Now().Add(waitBudget)); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		taken int
		err   error
	}
	written := make(chan outcome, 1)
	go func() {
		taken, err := conn.WriteSome(make([]byte, 100_000))
		written <- outcome{taken, err}
	}()
	// The server reads nothing: WriteSome waits for TCP to take the rest of
	// what the session took.
	peer.tcp.awaitWaitingCall(t)

	if err := conn.Abort(); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-written:
		if got.taken == 0 || !errors.Is(got.err, ErrConnectionReset) {
			t.Errorf("WriteSome = %d, %v; want what the session took, and ErrConnectionReset", got.taken, got.err)
		}
	case <-time.After(waitBudget):
		t.Error("WriteSome never returned")
	}
	close(startEcho)
}

func TestWriteSomeIsRefusedAsWriteIsAndTakesNothing(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, echo)
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	taken, err := conn.WriteSome([]byte("after the goodbye"))

	if taken != 0 || !errors.Is(err, ErrConnectionReset) {
		t.Errorf("WriteSome after the goodbye = %d, %v; want 0, ErrConnectionReset", taken, err)
	}
}

func TestAnEmptyWriteSomeTakesNothing(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, echo)
	conn := secureFake(t, fixture.connector(t), peer)

	taken, err := conn.WriteSome(nil)

	if taken != 0 || err != nil {
		t.Errorf("WriteSome(nil) = %d, %v; want 0, nil", taken, err)
	}
}

func TestAFailedSessionReportsWhyOnEveryCall(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		peer.sendRaw(forgedRecord())
	})
	conn := secureFake(t, fixture.connector(t), peer)

	_, failure := conn.Read(make([]byte, 1))

	if !errors.Is(failure, ErrTLS) {
		t.Fatalf("Read = %v, want ErrTLS", failure)
	}
	_, readErr := conn.Read(make([]byte, 1))
	_, writeErr := conn.Write([]byte("x"))
	for name, err := range map[string]error{
		"Read":             readErr,
		"Write":            writeErr,
		"WaitAcknowledged": conn.WaitAcknowledged(time.Now().Add(shortWait)),
		"CloseWrite":       conn.CloseWrite(),
	} {
		if err != failure {
			t.Errorf("%s = %v, want the first failure %v", name, err, failure)
		}
	}
	// Last, as it makes the other calls fail with net.ErrClosed: as in Rust,
	// a failed session gets no close_notify.
	sentBefore := peer.tcp.waitingForServer()
	if err := conn.Close(); err != failure {
		t.Errorf("Close = %v, want the first failure %v", err, failure)
	}
	if sent := peer.tcp.waitingForServer() - sentBefore; sent != 0 || !peer.tcp.isClientClosed() {
		t.Errorf("Close sent %d bytes, and left the connection closed: %t; want nothing sent, and closed", sent, peer.tcp.isClientClosed())
	}
}

func TestDataTakenInByWaitAcknowledgedIsReadLater(t *testing.T) {
	fixture := newTLSFixture(t)
	sent := make(chan struct{})
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		_, _ = peer.session.Write([]byte("early"))
		close(sent)
		echo(peer)
	})
	conn := secureFake(t, fixture.connector(t), peer)
	<-sent
	if err := peer.tcp.awaitSentToClient(1); err != nil {
		t.Fatal(err)
	}

	if err := conn.WaitAcknowledged(time.Now().Add(waitBudget)); err != nil {
		t.Fatal(err)
	}

	if waiting := peer.tcp.waitingForClient(); waiting != 0 {
		t.Errorf("WaitAcknowledged left %d bytes in TCP", waiting)
	}
	readExactly(t, conn, []byte("early"))
}

func TestAPinMismatchIsRefused(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, nil)
	var otherKey [sha256.Size]byte

	_, err := fixture.connector(t, otherKey).secure(peer.tcp, tlsTestHost, time.Now().Add(waitBudget))

	if err == nil || err.Error() != "TLS: invalid peer certificate: the public key is not pinned" {
		t.Errorf("secure = %v, want the pin refused", err)
	}
}

func TestAPinnedKeyIsAccepted(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, nil)

	secureFake(t, fixture.connector(t, fixture.serverKeySHA256), peer)
}

func TestASecondSessionResumesWithTheTicketsTakenIn(t *testing.T) {
	fixture := newTLSFixture(t)
	connector := fixture.connector(t)
	first := secureFake(t, connector, servePeer(t, fixture.server, echo))
	if first.Resumed() {
		t.Fatal("the first session resumed")
	}
	if _, err := first.Write([]byte("report")); err != nil {
		t.Fatal(err)
	}
	if err := first.WaitAcknowledged(time.Now().Add(waitBudget)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := secureFake(t, connector, servePeer(t, fixture.server, nil))

	if !second.Resumed() {
		t.Error("the second session did not resume")
	}
}

func TestALPNIsNegotiated(t *testing.T) {
	fixture := newTLSFixture(t)
	fixture.server.NextProtos = []string{"h2", "http/1.1"}
	connector, err := NewTLSConnector(TLSOptions{
		ExtraRootsDER:      [][]byte{fixture.caDER},
		WithoutPublicRoots: true,
		ALPNProtocols:      []string{"http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	conn := secureFake(t, connector, servePeer(t, fixture.server, nil))

	if got := conn.ALPNProtocol(); got != "http/1.1" {
		t.Errorf("ALPNProtocol() = %q", got)
	}
}

func TestAClosedConnectionRefusesEverything(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, echo)
	conn := secureFake(t, fixture.connector(t), peer)

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	_, readErr := conn.Read(make([]byte, 1))
	_, writeErr := conn.Write([]byte("x"))
	_, writeSomeErr := conn.WriteSome([]byte("x"))
	for name, err := range map[string]error{
		"Read":             readErr,
		"Write":            writeErr,
		"WriteSome":        writeSomeErr,
		"CloseWrite":       conn.CloseWrite(),
		"WaitAcknowledged": conn.WaitAcknowledged(time.Time{}),
		"SetDeadline":      conn.SetDeadline(time.Time{}),
		"SetReadDeadline":  conn.SetReadDeadline(time.Time{}),
		"SetWriteDeadline": conn.SetWriteDeadline(time.Time{}),
		"Abort":            conn.Abort(),
	} {
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("%s = %v, want net.ErrClosed", name, err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Errorf("a second Close = %v", err)
	}
}

func TestCloseSendsCloseNotifyThenTheEnd(t *testing.T) {
	fixture := newTLSFixture(t)
	peerSaw := make(chan error, 1)
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		_, err := peer.session.Read(make([]byte, 1))
		peerSaw <- err
	})
	conn := secureFake(t, fixture.connector(t), peer)

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if err := <-peerSaw; err != io.EOF {
		t.Errorf("the peer read %v, want io.EOF for close_notify", err)
	}
	if !peer.tcp.isClientClosed() {
		t.Error("the TCP connection is still open")
	}
}

// readWhileWaiting starts a Read of conn in another goroutine and returns,
// once the Read waits for the server, the channel its error arrives on: a
// Read that started only after Close would fail at once, waking nothing.
func readWhileWaiting(t *testing.T, conn *TLSConn, peer *tlsPeer) <-chan error {
	t.Helper()
	read := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		read <- err
	}()
	peer.tcp.awaitWaitingCall(t)
	return read
}

func TestCloseWakesAWaitingTLSRead(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, nil)
	conn := secureFake(t, fixture.connector(t), peer)
	read := readWhileWaiting(t, conn, peer)

	if err := conn.Close(); err != nil {
		t.Error(err)
	}

	if err := awaitResult(t, read); !errors.Is(err, net.ErrClosed) {
		t.Errorf("the woken Read = %v, want net.ErrClosed", err)
	}
}

func TestACloseThatCannotWaitForTheGoodbyeStillWakesAWaitingRead(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, echoOnceStarted(make(chan struct{})))
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}
	// The server reads nothing, so these records stay waiting.
	if _, err := conn.Write(make([]byte, 20_000)); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Write = %v, want a timeout", err)
	}
	read := readWhileWaiting(t, conn, peer)
	if err := conn.SetWriteDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}

	err := conn.Close()

	if !errors.Is(err, ErrTimedOut) {
		t.Errorf("Close = %v, want the timeout of the goodbye it dropped", err)
	}
	if err := awaitResult(t, read); !errors.Is(err, net.ErrClosed) {
		t.Errorf("the woken Read = %v, want net.ErrClosed", err)
	}
}

func TestAnEmptyReadReturnsAtOnce(t *testing.T) {
	fixture := newTLSFixture(t)
	conn := secureFake(t, fixture.connector(t), servePeer(t, fixture.server, nil))

	if read, err := conn.Read(nil); read != 0 || err != nil {
		t.Errorf("Read(nil) = %d, %v", read, err)
	}
}

func TestAWriteAfterCloseWriteIsRefusedAsTCPRefusesOneWithoutFailingTheSession(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		_, _ = io.Copy(io.Discard, peer.session)
		_, _ = peer.session.Write([]byte("goodbye"))
	})
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	_, err := conn.Write([]byte("too late"))

	if !errors.Is(err, ErrConnectionReset) {
		t.Errorf("Write = %v, want ErrConnectionReset", err)
	}
	readExactly(t, conn, []byte("goodbye"))
}

// TestAFailedSessionStillSaysWhyOnceTheConnectionEnded is the Rust unit test
// over the fake transport: the session fails on a record no server would
// send, as when a forged record arrives, and the server then hangs up. The
// end of the connection must not hide why the session failed.
func TestAFailedSessionStillSaysWhyOnceTheConnectionEnded(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		peer.sendRaw([]byte{0x17, 0x03, 0x03, 0x00, 0x04, 1, 2, 3, 4})
	})
	conn := secureFake(t, fixture.connector(t), peer)
	<-peer.done
	if err := peer.tcp.awaitSentToClient(1); err != nil {
		t.Fatal(err)
	}
	peer.tcp.finishFromServer()

	_, failure := conn.Read(make([]byte, 16))
	_, again := conn.Read(make([]byte, 16))

	if !errors.Is(failure, ErrTLS) || again != failure {
		t.Errorf("Read = %v, then %v; want the session's failure both times", failure, again)
	}
}

func TestRecordsTCPNoLongerTakesDoNotKeepAReadFromWhatArrived(t *testing.T) {
	fixture := newTLSFixture(t)
	startEcho := make(chan struct{})
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		if peer.awaitStart(startEcho) {
			_, _ = peer.session.Write([]byte("still here"))
		}
	})
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}
	// The server reads nothing, so these records stay waiting.
	if _, err := conn.Write(make([]byte, 20_000)); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Write = %v, want a timeout", err)
	}
	// Our side of the TCP connection shuts down under the session.
	if err := peer.tcp.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	close(startEcho)

	readExactly(t, conn, []byte("still here"))
}

func TestACloseWriteThatTimedOutFinishesWhenCalledAgain(t *testing.T) {
	fixture := newTLSFixture(t)
	startReading := make(chan struct{})
	peerSaw := make(chan error, 1)
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		if !peer.awaitStart(startReading) {
			return
		}
		_, err := io.Copy(io.Discard, peer.session)
		peerSaw <- err
	})
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}
	// The server reads nothing yet, so the goodbye waits behind these.
	if _, err := conn.Write(make([]byte, 20_000)); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Write = %v, want a timeout", err)
	}
	if err := conn.CloseWrite(); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("CloseWrite = %v, want a timeout", err)
	}
	close(startReading)
	if err := conn.SetWriteDeadline(time.Now().Add(waitBudget)); err != nil {
		t.Fatal(err)
	}

	err := conn.CloseWrite()

	if err != nil {
		t.Fatalf("CloseWrite again = %v", err)
	}
	if err := <-peerSaw; err != nil {
		t.Errorf("the server's session ended with %v, want its close_notify", err)
	}
}

func TestWritesCryptoTLSRefusedSayWhy(t *testing.T) {
	failure := &Error{Kind: ErrTLS, Message: "the session failed"}
	refused := errors.New("tls: protocol is shutdown")
	for _, c := range []struct {
		name    string
		prepare func(c *TLSConn)
		want    ErrorKind
	}{
		{"after a failure", func(c *TLSConn) { c.failure.Store(failure) }, ErrTLS},
		{"after the goodbye", func(c *TLSConn) { c.goodbyeQueued.Store(true) }, ErrConnectionReset},
		{"for no reason of ours", func(*TLSConn) {}, ErrTLS},
	} {
		t.Run(c.name, func(t *testing.T) {
			conn := &TLSConn{}
			c.prepare(conn)

			err := conn.writeFailure(refused)

			if !errors.Is(err, c.want) {
				t.Errorf("writeFailure = %v, want %v", err, c.want)
			}
		})
	}
	failed := &TLSConn{}
	failed.failure.Store(failure)
	if err := failed.writeFailure(refused); err != failure {
		t.Errorf("writeFailure = %v, want the session's failure itself", err)
	}
}

// forgedRecord returns an application data record that no key can decrypt,
// as a forger's would be.
func forgedRecord() []byte {
	return append([]byte{0x17, 0x03, 0x03, 0x00, 0x20}, make([]byte, 0x20)...)
}

// sessionWithAForgedRecordWaiting returns a session over a fake connection
// in which a record no key can decrypt has arrived, unread, and its peer.
func sessionWithAForgedRecordWaiting(t *testing.T) (*TLSConn, *tlsPeer) {
	t.Helper()
	fixture := newTLSFixture(t)
	sent := make(chan struct{})
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		peer.sendRaw(forgedRecord())
		close(sent)
	})
	conn := secureFake(t, fixture.connector(t), peer)
	<-sent
	if err := peer.tcp.awaitSentToClient(1); err != nil {
		t.Fatal(err)
	}
	return conn, peer
}

func TestWaitAcknowledgedReportsAFailureItTakesIn(t *testing.T) {
	conn, _ := sessionWithAForgedRecordWaiting(t)

	err := conn.WaitAcknowledged(time.Now().Add(waitBudget))

	if !errors.Is(err, ErrTLS) {
		t.Fatalf("WaitAcknowledged = %v, want the session's failure", err)
	}
	if _, readErr := conn.Read(make([]byte, 1)); readErr != err {
		t.Errorf("Read = %v, want the same failure", readErr)
	}
}

func TestDataTakenInBeforeTheSessionFailedIsReadFirst(t *testing.T) {
	fixture := newTLSFixture(t)
	sent := make(chan struct{})
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		_, _ = peer.session.Write([]byte("authentic"))
		peer.sendRaw(forgedRecord())
		close(sent)
	})
	conn := secureFake(t, fixture.connector(t), peer)
	<-sent
	failure := conn.WaitAcknowledged(time.Now().Add(waitBudget))
	if !errors.Is(failure, ErrTLS) {
		t.Fatalf("WaitAcknowledged = %v, want the session's failure", failure)
	}
	buffer := make([]byte, 16)

	read, err := conn.Read(buffer)

	// As Rust's receive_until reads what was decrypted before anything else.
	if err != nil || string(buffer[:read]) != "authentic" {
		t.Errorf("Read = %q, %v; want what arrived intact before the failure", buffer[:read], err)
	}
	if _, again := conn.Read(buffer); again != failure {
		t.Errorf("the next Read = %v, want the session's failure %v", again, failure)
	}
}

func TestCloseWriteAndCloseReportAFailureTheyTakeIn(t *testing.T) {
	for name, end := range map[string]func(*TLSConn) error{
		"CloseWrite": (*TLSConn).CloseWrite,
		"Close":      (*TLSConn).Close,
	} {
		t.Run(name, func(t *testing.T) {
			conn, _ := sessionWithAForgedRecordWaiting(t)

			if err := end(conn); !errors.Is(err, ErrTLS) {
				t.Errorf("%s = %v, want the session's failure", name, err)
			}
		})
	}
}

func TestASessionClosedWithoutReadingStillLetsTheNextResume(t *testing.T) {
	fixture := newTLSFixture(t)
	connector := fixture.connector(t)
	// crypto/tls's server sends its ticket with its first flight: it has
	// arrived, unread, once the handshake is done.
	first := secureFake(t, connector, servePeer(t, fixture.server, nil))

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	if second := secureFake(t, connector, servePeer(t, fixture.server, nil)); !second.Resumed() {
		t.Error("the second session did not resume")
	}
}

// TestASessionResumesWhateverTheCaseOfItsServerName is Go only: rustls finds
// a session for a server name whatever the ASCII case of either.
func TestASessionResumesWhateverTheCaseOfItsServerName(t *testing.T) {
	for _, names := range []struct{ first, then string }{
		{"TLS.Test", tlsTestHost},
		{tlsTestHost, "TLS.TEST"},
	} {
		t.Run(names.first+" then "+names.then, func(t *testing.T) {
			fixture := newTLSFixture(t)
			connector := fixture.connector(t)
			first := secureFakeFor(t, connector, servePeer(t, fixture.server, nil), names.first)
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}

			second := secureFakeFor(t, connector, servePeer(t, fixture.server, nil), names.then)

			if !second.Resumed() {
				t.Errorf("a session made for %q did not resume for %q", names.first, names.then)
			}
		})
	}
}

func TestTheTakeInStopsOnceEnoughWaitsForTheReader(t *testing.T) {
	fixture := newTLSFixture(t)
	const streamed = 64 * 1024
	sent := make(chan struct{})
	peer := servePeer(t, fixture.server, func(peer *tlsPeer) {
		_, _ = peer.session.Write(make([]byte, streamed))
		close(sent)
	})
	conn := secureFake(t, fixture.connector(t), peer)
	<-sent
	if err := peer.tcp.awaitSentToClient(streamed); err != nil {
		t.Fatal(err)
	}

	if err := conn.WaitAcknowledged(time.Now().Add(waitBudget)); err != nil {
		t.Fatal(err)
	}

	conn.readMu.Lock()
	takenIn := len(conn.readAhead)
	conn.readMu.Unlock()
	// The take-in may overshoot by the record it stops after.
	if takenIn > readAheadLimit+maxPlaintextLen || peer.tcp.waitingForClient() == 0 {
		t.Errorf("%d bytes taken in ahead of the reader, %d left in TCP; want at most %d taken",
			takenIn, peer.tcp.waitingForClient(), readAheadLimit+maxPlaintextLen)
	}
}

func TestAConnectionStartsWithoutTheHandshakesDeadlines(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, nil)

	secureFake(t, fixture.connector(t), peer)

	if read, write := peer.tcp.readUntil(), peer.tcp.writeUntil(); !read.IsZero() || !write.IsZero() {
		t.Errorf("deadlines %v and %v left from the handshake", read, write)
	}
}

func TestSetDeadlineSetsBothOfTheConnectionsDeadlines(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, nil)
	conn := secureFake(t, fixture.connector(t), peer)
	deadline := time.Now().Add(time.Hour)

	if err := conn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}

	if read, write := peer.tcp.readUntil(), peer.tcp.writeUntil(); !read.Equal(deadline) || !write.Equal(deadline) {
		t.Errorf("deadlines %v and %v, want both %v", read, write, deadline)
	}
}

func TestWaitAcknowledgedTimesOutWhileItsRecordsWaitOrGoUnacknowledged(t *testing.T) {
	for _, c := range []struct {
		name      string
		written   int
		operation string
	}{
		{"records waiting for room", 20_000, operationTCPWrite},
		{"records sent, unacknowledged", 100, operationTCPFlush},
	} {
		t.Run(c.name, func(t *testing.T) {
			fixture := newTLSFixture(t)
			// The server reads nothing.
			peer := servePeer(t, fixture.server, echoOnceStarted(make(chan struct{})))
			conn := secureFake(t, fixture.connector(t), peer)
			if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write(make([]byte, c.written)); err != nil && !errors.Is(err, ErrTimedOut) {
				t.Fatal(err)
			}

			err := conn.WaitAcknowledged(time.Now().Add(shortWait))

			var timedOut *Error
			if !errors.As(err, &timedOut) || timedOut.Kind != ErrTimedOut || timedOut.Operation != c.operation {
				t.Errorf("WaitAcknowledged = %v, want the %s timed out", err, c.operation)
			}
		})
	}
}

func TestACloseWriteAfterAnAbortSaysTheConnectionWasReset(t *testing.T) {
	fixture := newTLSFixture(t)
	conn := secureFake(t, fixture.connector(t), servePeer(t, fixture.server, nil))
	if err := conn.Abort(); err != nil {
		t.Fatal(err)
	}

	err := conn.CloseWrite()

	if !errors.Is(err, ErrConnectionReset) {
		t.Errorf("CloseWrite = %v, want ErrConnectionReset", err)
	}
}

func TestACloseAfterAnAbortSaysTheConnectionWasResetAndLetsItGo(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, nil)
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.Abort(); err != nil {
		t.Fatal(err)
	}

	err := conn.Close()

	if !errors.Is(err, ErrConnectionReset) || !peer.tcp.isClientClosed() {
		t.Errorf("Close = %v, with the TCP connection closed: %t; want ErrConnectionReset, and closed",
			err, peer.tcp.isClientClosed())
	}
}

func TestWaitAcknowledgedReportsWhatAnAbortDropped(t *testing.T) {
	fixture := newTLSFixture(t)
	// The server reads nothing: what the client sends waits in TCP.
	peer := servePeer(t, fixture.server, echoOnceStarted(make(chan struct{})))
	conn := secureFake(t, fixture.connector(t), peer)
	if _, err := conn.Write([]byte("never delivered")); err != nil {
		t.Fatal(err)
	}
	woken := make(chan error, 1)
	go func() { woken <- conn.WaitAcknowledged(time.Time{}) }()
	peer.tcp.awaitWaitingCall(t)

	if err := conn.Abort(); err != nil {
		t.Fatal(err)
	}

	if err := awaitResult(t, woken); !errors.Is(err, ErrConnectionReset) {
		t.Errorf("the WaitAcknowledged it woke = %v, want ErrConnectionReset: the data never arrived", err)
	}
	if err := conn.WaitAcknowledged(time.Now().Add(shortWait)); !errors.Is(err, ErrConnectionReset) {
		t.Errorf("a later WaitAcknowledged = %v, want ErrConnectionReset", err)
	}
}

func TestADeadlineMovedWhileATLSReadWaitsApplies(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, nil)
	conn := secureFake(t, fixture.connector(t), peer)
	// Done with its handshake, the server waits for nothing: the Read is the
	// one waiting call.
	<-peer.done
	read := readWhileWaiting(t, conn, peer)

	if err := conn.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := awaitResult(t, read); !errors.Is(err, ErrTimedOut) {
		t.Errorf("Read = %v, want a timeout at the moved deadline", err)
	}
}

func TestADeadlineMovedWhileATLSWriteWaitsApplies(t *testing.T) {
	fixture := newTLSFixture(t)
	// The server reads nothing, so a write larger than the session holds
	// waits for room.
	peer := servePeer(t, fixture.server, echoOnceStarted(make(chan struct{})))
	conn := secureFake(t, fixture.connector(t), peer)
	written := make(chan error, 1)
	go func() {
		_, err := conn.Write(make([]byte, 100_000))
		written <- err
	}()
	peer.tcp.awaitWaitingCall(t)

	if err := conn.SetWriteDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := awaitResult(t, written); !errors.Is(err, ErrTimedOut) {
		t.Errorf("Write = %v, want a timeout at the moved deadline", err)
	}
}

func TestAReadDoesNotSpinOnRecordsTCPNoLongerTakes(t *testing.T) {
	fixture := newTLSFixture(t)
	peer := servePeer(t, fixture.server, echoOnceStarted(make(chan struct{})))
	conn := secureFake(t, fixture.connector(t), peer)
	if err := conn.SetWriteDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}
	// The server reads nothing, so these records stay waiting.
	if _, err := conn.Write(make([]byte, 20_000)); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Write = %v, want a timeout", err)
	}
	// Our side of the TCP connection shuts down under the session.
	if err := peer.tcp.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(shortWait)); err != nil {
		t.Fatal(err)
	}
	attempts := peer.tcp.sendAttemptsSoFar()
	read := make(chan error, 1)

	go func() {
		_, err := conn.Read(make([]byte, 1))
		read <- err
	}()

	// A read that kept trying to send them would spin, never waiting long
	// enough to see its deadline pass.
	if err := awaitResult(t, read); !errors.Is(err, ErrTimedOut) {
		t.Errorf("Read = %v, want a timeout", err)
	}
	// One try tells that the records cannot go.
	if tries := peer.tcp.sendAttemptsSoFar() - attempts; tries > 1 {
		t.Errorf("the read tried to send %d times while it waited", tries)
	}
}

func TestAPinOfACertificateTheServerSendsButDoesNotOwnIsRefused(t *testing.T) {
	fixture := newTLSFixture(t)
	// The server sends the CA after its own certificate, as servers send
	// intermediates: a pin must not let any of those stand for the server.
	server := fixture.server.Clone()
	own := server.Certificates[0]
	own.Certificate = append(slices.Clip(own.Certificate), fixture.caDER)
	server.Certificates = []tls.Certificate{own}
	ca, err := x509.ParseCertificate(fixture.caDER)
	if err != nil {
		t.Fatal(err)
	}
	peer := servePeer(t, server, nil)

	_, err = fixture.connector(t, sha256.Sum256(ca.RawSubjectPublicKeyInfo)).secure(peer.tcp, tlsTestHost, time.Now().Add(waitBudget))

	if err == nil || err.Error() != "TLS: invalid peer certificate: the public key is not pinned" {
		t.Errorf("secure = %v, want the pin refused", err)
	}
}

func TestAPinnedKeyDoesNotStandInForTheChain(t *testing.T) {
	fixture, other := newTLSFixture(t), newTLSFixture(t)
	peer := servePeer(t, fixture.server, nil)

	// Pinned to the server's own key, but trusting another CA.
	_, err := other.connector(t, fixture.serverKeySHA256).secure(peer.tcp, tlsTestHost, time.Now().Add(waitBudget))

	var unverified *tls.CertificateVerificationError
	if !errors.Is(err, ErrTLS) || !errors.As(err, &unverified) || errors.Is(err, errKeyNotPinned) {
		t.Errorf("secure = %v, want the chain refused", err)
	}
}

func TestNothingArrivedIsATemporaryTimeout(t *testing.T) {
	if !errNothingArrived.Timeout() || !(nothingArrived{}).Temporary() || errNothingArrived.Error() == "" {
		t.Error("crypto/tls would not keep the session after errNothingArrived")
	}
}

func TestTheRecordBufferLeavesDeadlinesToTheConnection(t *testing.T) {
	tcp := newFakeTCP(fakeSendRoom)
	records := newRecordBuffer(tcp)

	for name, err := range map[string]error{
		"SetDeadline":      records.SetDeadline(time.Now()),
		"SetReadDeadline":  records.SetReadDeadline(time.Now()),
		"SetWriteDeadline": records.SetWriteDeadline(time.Now()),
	} {
		if err != nil {
			t.Errorf("%s = %v", name, err)
		}
	}
	if !tcp.readUntil().IsZero() || !tcp.writeUntil().IsZero() {
		t.Error("the buffer moved the connection's deadlines")
	}
	if records.LocalAddr().String() != fakeClientAddress.String() || records.RemoteAddr().String() != fakeServerAddress.String() {
		t.Errorf("addresses %v, %v", records.LocalAddr(), records.RemoteAddr())
	}
}
