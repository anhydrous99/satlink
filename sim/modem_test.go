package sim

import (
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink/ppp"
)

const (
	// modemGuard is the escape guard of the modems in these tests.
	modemGuard = 100 * time.Millisecond
	// slowCallSetup is the setup time of the calls that take some.
	slowCallSetup = 500 * time.Millisecond
	// iridiumInternet is the number data calls dial.
	iridiumInternet = "008816000025"
	// connectedGreeting is how a data call starts: CONNECT, then the PPP
	// server's opening Configure-Request, flag first.
	connectedGreeting = "\r\nCONNECT 19200\r\n\x7e"
)

func modemConfig() Config {
	config := DefaultConfig()
	config.EscapeGuard = modemGuard
	return config
}

// output returns everything the modem has to say, as text.
func output(m *modem) string {
	buffer := make([]byte, 64*1024)
	return string(buffer[:m.readInto(buffer)])
}

// quietModem returns a modem with echo off, as a client leaves it after
// initialising.
func quietModem(config Config, now time.Time) *modem {
	m := newModem(config)
	m.receive([]byte("ATE0\r"), now)
	output(m)
	return m
}

// runCommand sends a command line and returns the answer.
func runCommand(m *modem, line string, now time.Time) string {
	m.receive([]byte(line+"\r"), now)
	return output(m)
}

// slowCallSetupConfig returns a configuration whose data calls connect
// after slowCallSetup.
func slowCallSetupConfig() Config {
	config := modemConfig()
	config.Link.CallSetup = slowCallSetup
	return config
}

// diallingModem returns a modem that dialled a data call at start, which
// connects after slowCallSetup.
func diallingModem(t *testing.T, start time.Time) *modem {
	t.Helper()
	m := quietModem(slowCallSetupConfig(), start)
	dialDataCall(t, m, start)
	return m
}

// dialDataCall makes m, with echo off, dial a data call at now, which it
// answers only once the call connects or fails.
func dialDataCall(t *testing.T, m *modem, now time.Time) {
	t.Helper()
	if answer := runCommand(m, "ATD"+iridiumInternet, now); answer != "" {
		t.Fatalf("the dial answered %q at once", answer)
	}
}

// connectedModem returns a modem with a data call up, in online-data mode
// since start, and the PPP server's opening Configure-Request already
// read.
func connectedModem(t *testing.T, start time.Time) *modem {
	t.Helper()
	m := quietModem(modemConfig(), start)
	connectDataCall(t, m, start)
	return m
}

// connectDataCall makes m, with echo off and an instant call setup, dial a
// data call at now, and reads how the call starts: CONNECT, then the PPP
// server's opening Configure-Request.
func connectDataCall(t *testing.T, m *modem, now time.Time) {
	t.Helper()
	dialDataCall(t, m, now)
	m.advance(now)
	if greeting := output(m); !strings.HasPrefix(greeting, connectedGreeting) {
		t.Fatalf("the call started with %q", greeting)
	}
}

// assertAnswer fails the test unless got is want.
func assertAnswer(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("answered %q; want %q", got, want)
	}
}

func TestEchoesCommandsUntilToldNotTo(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := newModem(modemConfig())

	assertAnswer(t, runCommand(m, "AT", now), "AT\r\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "ATE0", now), "ATE0\r\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT", now), "\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "ATZ", now), "\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT", now), "AT\r\r\nOK\r\n")
}

func TestIgnoresLinesNotMeantForItAndRefusesUnknownCommands(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)

	assertAnswer(t, runCommand(m, "\x1b", now), "")
	assertAnswer(t, runCommand(m, "AT+BOGUS", now), "\r\nERROR\r\n")
	assertAnswer(t, runCommand(m, strings.Repeat("A", 600), now), "\r\nERROR\r\n")
	m.receive([]byte("AT\r\n"), now)
	assertAnswer(t, output(m), "\r\nOK\r\n")
}

func TestReportsIdentityAndStatus(t *testing.T) {
	t.Parallel()
	now := time.Now()
	config := modemConfig()
	config.SignalBars = 9
	m := quietModem(config, now)

	assertAnswer(t, runCommand(m, "AT+CGMI", now), "\r\nIridium\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CGMM", now), "\r\n9575\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CGSN", now), "\r\n300234010000000\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CSQ", now), "\r\n+CSQ:5\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CSQF", now), "\r\n+CSQF:5\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CREG?", now), "\r\n+CREG: 0,1\r\n\r\nOK\r\n")
	if clock := runCommand(m, "AT+CCLK?", now); !strings.HasPrefix(clock, "\r\n+CCLK: \"") {
		t.Errorf("AT+CCLK? answered %q", clock)
	}
	assertAnswer(t, runCommand(m, "AT+CEER", now), "\r\n+CEER: No report available\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CPAS", now), "\r\n+CPAS: 000\r\n\r\nOK\r\n")
}

func TestReportsTheOtherQueries(t *testing.T) {
	t.Parallel()
	now := time.Now()
	config := modemConfig()
	config.SignalBars = -3
	m := quietModem(config, now)

	assertAnswer(t, runCommand(m, "AT+CSQ", now), "\r\n+CSQ:0\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CGMR", now), "\r\nCall Processor Version: SIM-1.0\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CIMI", now), "\r\n901039999999999\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CBC", now), "\r\n+CBC: 001,003,001,080\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CMGF=0", now), "\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CMGF=1", now), "\r\nERROR\r\n")
	assertAnswer(t, runCommand(m, "AT+CSCA?", now), "\r\n+CSCA: \"+881662900005\",145\r\n\r\nOK\r\n")
}

func TestKeepsTheBearerItIsGiven(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)

	assertAnswer(t, runCommand(m, "AT+CBST?", now), "\r\n+CBST: 7,0,1\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CBST=71,0,1", now), "\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CBST?", now), "\r\n+CBST: 71,0,1\r\n\r\nOK\r\n")
}

// ATZ restores every factory setting, not echo alone, so that after it a
// call outlives a DTR drop again until the app sends AT&D2 once more.
func TestATZRestoresTheFactorySettings(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)
	runCommand(m, "AT&D2", now)
	runCommand(m, "AT+CBST=71,0,1", now)

	runCommand(m, "ATZ", now)

	// Echo is back on, and the bearer is the default one again.
	assertAnswer(t, runCommand(m, "AT+CBST?", now), "AT+CBST?\r\r\n+CBST: 7,0,1\r\n\r\nOK\r\n")
	runCommand(m, "ATE0", now)
	connectDataCall(t, m, now)
	m.dropDTR()
	if !m.stats.InDataCall {
		t.Error("after ATZ, dropping DTR ended the call, which only AT&D2 asks for")
	}
}

func TestSendsAnSMSTypedAfterThePrompt(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)

	assertAnswer(t, runCommand(m, "AT+CMGS=19", now), "\r\n> ")
	m.receive([]byte(helloSubmitPDU+"\x1a"), now)
	assertAnswer(t, output(m), "\r\n+CMGS: 1\r\n\r\nOK\r\n")
	runCommand(m, "AT+CMGS=11", now)
	m.receive([]byte(hiSubmitPDU+"\x1a"), now)
	assertAnswer(t, output(m), "\r\n+CMGS: 2\r\n\r\nOK\r\n")

	want := []SentSMS{{Number: "+881631234567", Text: "hello"}, {Number: "+1", Text: "hi"}}
	if !slices.Equal(m.stats.SMSSent, want) {
		t.Errorf("sent %+v; want %+v", m.stats.SMSSent, want)
	}
}

func TestEscapeAbandonsAnSMS(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)
	runCommand(m, "AT+CMGS=11", now)

	m.receive([]byte("0011\x1b"), now)

	assertAnswer(t, output(m), "\r\nOK\r\n")
	if len(m.stats.SMSSent) != 0 {
		t.Errorf("sent %+v", m.stats.SMSSent)
	}
}

func TestAPDUTheHandsetCannotSendIsRefused(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)
	runCommand(m, "AT+CMGS=12", now)

	m.receive([]byte(hiSubmitPDU+"\x1a"), now)

	assertAnswer(t, output(m), "\r\n+CMS ERROR: 304\r\n")
	assertAnswer(t, runCommand(m, "AT", now), "\r\nOK\r\n")
}

func TestEchoesSMSTextWhileEchoIsOn(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := newModem(modemConfig())

	m.receive([]byte("AT+CMGS=11\r"+hiSubmitPDU+"\x1a"), now)

	assertAnswer(t, output(m), "AT+CMGS=11\r\r\n> "+hiSubmitPDU+"\r\n+CMGS: 1\r\n\r\nOK\r\n")
}

func TestAnnouncesStoresAndDeletesReceivedSMS(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)

	if err := m.receiveSMS("+12025550100", "hi there", now); err != nil {
		t.Fatalf("receiveSMS: %v", err)
	}

	assertAnswer(t, output(m), "\r\n+CMTI: \"SM\",1\r\n")
	listing := strings.Split(strings.TrimSpace(runCommand(m, "AT+CMGL=4", now)), "\r\n")
	if len(listing) != 4 || !strings.HasPrefix(listing[0], "+CMGL: 1,0,,") || listing[3] != "OK" {
		t.Fatalf("listed %q", listing)
	}
	if message := decodedPDU(t, listing[1]); message.Address != "+12025550100" || message.Text != "hi there" {
		t.Errorf("listed %+v", message)
	}
	if read := runCommand(m, "AT+CMGR=1", now); !strings.HasPrefix(read, "\r\n+CMGR: 1,,") {
		t.Errorf("read %q", read)
	}
	assertAnswer(t, runCommand(m, "AT+CMGR=2", now), "\r\n+CMS ERROR: 321\r\n")
	assertAnswer(t, runCommand(m, "AT+CMGD=1", now), "\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "AT+CMGD=1", now), "\r\n+CMS ERROR: 321\r\n")
}

func TestAnSMSAnnouncementWaitsForTextEntryToFinish(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)
	runCommand(m, "AT+CMGS=11", now)

	receivedSMS(t, m, "+2", "incoming", now)

	assertAnswer(t, output(m), "")
	m.receive([]byte(hiSubmitPDU+"\x1a"), now)
	assertAnswer(t, output(m), "\r\n+CMGS: 1\r\n\r\nOK\r\n")
	assertAnswer(t, output(m), "\r\n+CMTI: \"SM\",1\r\n")
}

// An announcement also waits until the app has read the handset's earlier
// output, so that a client that drops what follows a final result, as a
// data call's hang-up does, still sees it.
func TestAnSMSAnnouncementWaitsUntilTheEarlierOutputIsRead(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)
	m.receive([]byte("AT\r"), now)

	receivedSMS(t, m, "+2", "incoming", now)

	assertAnswer(t, output(m), "\r\nOK\r\n")
	assertAnswer(t, output(m), "\r\n+CMTI: \"SM\",1\r\n")
}

func TestVoiceCallsAreAnsweredAtOnce(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)

	assertAnswer(t, runCommand(m, "ATD+12025550100;", now), "\r\nOK\r\n")

	if m.stats.CallsPlaced != 1 || m.stats.CallsConnected != 1 || m.stats.InDataCall {
		t.Errorf("stats %+v; want one voice call placed and connected", m.stats)
	}
	assertAnswer(t, runCommand(m, "ATH", now), "\r\nOK\r\n")
}

func TestADataCallConnectsAfterTheSetupTime(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := diallingModem(t, start)

	m.advance(at(start, 499))
	assertAnswer(t, output(m), "")
	if m.stats.CallsConnected != 0 {
		t.Errorf("connected %d calls before the setup time", m.stats.CallsConnected)
	}

	m.advance(at(start, 500))
	if greeting := output(m); !strings.HasPrefix(greeting, connectedGreeting) {
		t.Errorf("the call started with %q", greeting)
	}
	if m.stats.CallsPlaced != 1 || m.stats.CallsConnected != 1 || !m.stats.InDataCall {
		t.Errorf("stats %+v; want one data call placed and connected", m.stats)
	}
}

func TestSendingAnythingDuringCallSetupAbortsIt(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := diallingModem(t, start)

	m.receive([]byte("AT\r"), at(start, 125))

	assertAnswer(t, output(m), "\r\nNO CARRIER\r\n")
	m.advance(at(start, 1000))
	assertAnswer(t, output(m), "")
	assertAnswer(t, runCommand(m, "AT+CEER", at(start, 1000)), "\r\n+CEER: Call setup aborted\r\n\r\nOK\r\n")
}

func TestInputJustAfterTheDialCommandDoesNotAbortIt(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := quietModem(slowCallSetupConfig(), start)

	// The line ends with CR LF, and a stray byte follows just in time.
	m.receive([]byte("ATD"+iridiumInternet+"\r\n"), start)
	m.receive([]byte("x"), at(start, 124))
	assertAnswer(t, output(m), "")

	m.advance(at(start, 500))
	if greeting := output(m); !strings.HasPrefix(greeting, "\r\nCONNECT 19200\r\n") {
		t.Errorf("the call started with %q", greeting)
	}
	if m.stats.DataBytesFromApp != 0 {
		t.Errorf("%d bytes went to the network", m.stats.DataBytesFromApp)
	}
}

func TestAnInstantCallIgnoresWhatCameWithTheDialCommand(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := quietModem(modemConfig(), start)

	m.receive([]byte("ATD"+iridiumInternet+"\r\n"), start)
	m.advance(start)

	if greeting := output(m); !strings.HasPrefix(greeting, "\r\nCONNECT 19200\r\n") {
		t.Errorf("the call started with %q", greeting)
	}
	if m.stats.DataBytesFromApp != 0 {
		t.Errorf("%d bytes went to the network", m.stats.DataBytesFromApp)
	}
}

func TestTheNetworkCanFailACallBeingSetUp(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := diallingModem(t, start)

	m.dropCall()

	assertAnswer(t, output(m), "\r\nNO CARRIER\r\n")
	m.advance(at(start, 1000))
	if answer := output(m); answer != "" {
		t.Errorf("the call connected after all: %q", answer)
	}
	if m.stats.CallsConnected != 0 {
		t.Errorf("connected %d calls", m.stats.CallsConnected)
	}
}

// A restart of PPP while the call is being set up does nothing, as there is
// no circuit yet whose PPP could start over, and the call connects as it
// would have, its gateway assigning the configured address.
func TestARestartOfPPPDuringCallSetupChangesNothing(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := diallingModem(t, start)
	newAddress := netip.MustParseAddr("10.64.0.3")

	err := m.restartPPP(newAddress, at(start, 100))

	if err != nil {
		t.Errorf("the restart failed: %v", err)
	}
	m.advance(at(start, 500))
	if greeting := output(m); !strings.HasPrefix(greeting, connectedGreeting) {
		t.Fatalf("after the restart, the call started with %q", greeting)
	}
	if assigned := m.call.circuit.gateway.network.ClientAddress; assigned != defaultClientAddress {
		t.Errorf("the gateway assigns %v; want the configured %v", assigned, defaultClientAddress)
	}
}

func TestEscapeReachesOnlineCommandModeAndATOReturns(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := connectedModem(t, start)

	m.receive([]byte("+++"), at(start, 100))
	m.advance(at(start, 199))
	assertAnswer(t, output(m), "")
	m.advance(at(start, 200))
	assertAnswer(t, output(m), "\r\nOK\r\n")

	assertAnswer(t, runCommand(m, "AT+CSQ", at(start, 300)), "\r\n+CSQ:4\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "ATD123", at(start, 300)), "\r\nERROR\r\n")
	if !m.stats.InDataCall {
		t.Error("the call ended in online-command mode")
	}
	assertAnswer(t, runCommand(m, "ATO", at(start, 300)), "\r\nCONNECT 19200\r\n")
	if m.stats.DataBytesFromApp != 0 {
		t.Errorf("the escape counted as %d bytes of data", m.stats.DataBytesFromApp)
	}
}

func TestHangingUpInOnlineCommandModeEndsTheCall(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := connectedModem(t, start)
	m.receive([]byte("+++"), at(start, 100))
	m.advance(at(start, 200))
	output(m)
	// The call goes on.
	assertAnswer(t, runCommand(m, "AT+CPAS", at(start, 300)), "\r\n+CPAS: 004\r\n\r\nOK\r\n")

	assertAnswer(t, runCommand(m, "ATH", at(start, 300)), "\r\nOK\r\n")

	if m.stats.InDataCall {
		t.Error("the call is still up")
	}
	assertAnswer(t, runCommand(m, "AT+CPAS", at(start, 300)), "\r\n+CPAS: 000\r\n\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "ATO", at(start, 300)), "\r\nNO CARRIER\r\n")
	assertAnswer(t, runCommand(m, "AT+CEER", at(start, 300)), "\r\n+CEER: Normal call clearing\r\n\r\nOK\r\n")
}

// In online-command mode, data from the far end waits for ATO, so that it
// cannot come between the answers to the commands the app sends meanwhile,
// such as the ATH of satlink's hang-up.
func TestOnlineCommandModeHoldsTheFarEndsDataUntilATO(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := connectedModem(t, start)
	m.receive([]byte("+++"), at(start, 100))
	m.advance(at(start, 200))
	assertAnswer(t, output(m), "\r\nOK\r\n")
	// The PPP server sends its unanswered Configure-Request again.
	resent := start.Add(ppp.DefaultTimers().Restart)

	m.advance(resent)
	held := output(m)
	m.receive([]byte("ATO\r"), resent)
	m.advance(resent)

	assertAnswer(t, held, "")
	// CONNECT, then the Configure-Request, flag first.
	if resumed := output(m); !strings.HasPrefix(resumed, "\r\nCONNECT 19200\r\n\x7e") {
		t.Errorf("ATO resumed the call with %q; want CONNECT, then the far end's data", resumed)
	}
}

func TestDataInOnlineDataModeGoesToTheNetwork(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := connectedModem(t, start)

	m.receive([]byte("AT\r"), at(start, 10))

	if answer := output(m); answer != "" {
		t.Errorf("a command answered %q; commands are data now", answer)
	}
	if m.stats.DataBytesFromApp != 3 {
		t.Errorf("%d bytes went to the network; want 3", m.stats.DataBytesFromApp)
	}
}

func TestTheNetworkCanDropTheCall(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := connectedModem(t, start)
	receivedSMS(t, m, "+2", "sent during the call", start)

	m.dropCall()

	assertAnswer(t, output(m), "\r\nNO CARRIER\r\n")
	assertAnswer(t, output(m), "\r\n+CMTI: \"SM\",1\r\n")
	if m.stats.InDataCall {
		t.Error("the call is still up")
	}
	assertAnswer(t, runCommand(m, "AT+CEER", at(start, 10)), "\r\n+CEER: Network out of order\r\n\r\nOK\r\n")
	m.dropCall()
	if answer := output(m); answer != "" {
		t.Errorf("with no call to drop, the modem said %q", answer)
	}
}

func TestClosingThePortEndsACallOnlyAfterATAndD2(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := connectedModem(t, start)

	m.dropDTR()

	if !m.stats.InDataCall {
		t.Error("DTR is ignored by default, but the call ended")
	}
	m = quietModem(modemConfig(), start)
	runCommand(m, "AT&D2", start)
	connectDataCall(t, m, start)
	m.dropDTR()
	if m.stats.InDataCall {
		t.Error("after AT&D2, dropping DTR left the call up")
	}
	assertAnswer(t, runCommand(m, "AT+CEER", start), "\r\n+CEER: Normal call clearing\r\n\r\nOK\r\n")
}

// After AT&D2, a DTR drop ends a call still being set up too, so that an app
// that closes its port during the setup leaves no call behind to connect.
func TestClosingThePortAfterATAndD2EndsACallBeingSetUp(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := quietModem(slowCallSetupConfig(), start)
	runCommand(m, "AT&D2", start)
	dialDataCall(t, m, start)

	m.dropDTR()

	m.advance(at(start, 1000))
	if answer := output(m); answer != "" {
		t.Errorf("after DTR dropped during the setup, the handset said %q; want nothing", answer)
	}
	if m.stats.CallsConnected != 0 {
		t.Errorf("connected %d calls", m.stats.CallsConnected)
	}
	assertAnswer(t, runCommand(m, "AT+CEER", at(start, 1000)), "\r\n+CEER: Normal call clearing\r\n\r\nOK\r\n")
}

func TestAnsweringWithNothingRingingFindsNoCarrier(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := quietModem(modemConfig(), now)

	assertAnswer(t, runCommand(m, "ATA", now), "\r\nNO CARRIER\r\n")
}

func TestAGatewayThatCannotStartFailsTheCall(t *testing.T) {
	t.Parallel()
	tooLong := modemConfig()
	tooLong.Authentication = &Authentication{
		Protocol:    ppp.AuthProtocolPAP,
		Credentials: ppp.Credentials{Username: strings.Repeat("x", 300)},
	}
	notIPv4 := modemConfig()
	notIPv4.Network.Hosts = []HostAddress{{Name: "v6.test", Address: netip.MustParseAddr("2001:db8::1")}}
	configs := map[string]Config{"credentials too long": tooLong, "a host without IPv4": notIPv4}

	for name, config := range configs {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			m := quietModem(config, now)

			m.receive([]byte("ATD"+iridiumInternet+"\r"), now)
			m.advance(now)

			assertAnswer(t, output(m), "\r\nNO CARRIER\r\n")
			assertAnswer(t, runCommand(m, "AT+CEER", now), "\r\n+CEER: Service or option not available\r\n\r\nOK\r\n")
		})
	}
}

func TestSurvivesArbitraryInputInEveryMode(t *testing.T) {
	t.Parallel()
	random := newXorShift(0x9E37_79B9_7F4A_7C15)
	// Fragments that steer the modem into its other modes now and then.
	steering := []string{"ATD123\r", "+++", "ATO\r", "AT+CMGS=11\r", "\x1a", "ATH\r"}
	now := time.Now()
	m := newModem(modemConfig())

	for round := range 3_000 {
		if round%50 == 0 {
			m.dropCall()
		}
		var input []byte
		if random.next()%4 == 0 {
			input = []byte(steering[random.next()%uint64(len(steering))])
		} else {
			for range random.next() % 40 {
				input = append(input, byte(random.next()))
			}
		}
		m.receive(input, now)
		now = now.Add(time.Duration(random.next()%150) * time.Millisecond)
		m.advance(now)
		output(m)
	}
}

func TestAnUnfinishedEscapeGoesToTheNetworkAfterTheGuardTime(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := connectedModem(t, start)

	m.receive([]byte("++"), at(start, 100))
	m.advance(at(start, 200))

	if m.stats.DataBytesFromApp != 2 {
		t.Errorf("%d bytes went to the network; want the 2 pluses", m.stats.DataBytesFromApp)
	}
	if answer := output(m); strings.Contains(answer, "OK") {
		t.Errorf("the modem answered %q; the call stays in online-data mode", answer)
	}
}

// The guard of silence before +++ counts from CONNECT, so that pluses sent
// sooner are data, as they are to a real handset.
func TestPlusesSoonerThanAGuardAfterCONNECTAreData(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := connectedModem(t, start)

	m.receive([]byte("+++"), at(start, 50))
	m.advance(at(start, 400))

	assertPlusesWereData(t, m)
}

// ATO starts the guard of silence before +++ again, as CONNECT does.
func TestPlusesSoonerThanAGuardAfterATOAreData(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := connectedModem(t, start)
	m.receive([]byte("+++"), at(start, 100))
	m.advance(at(start, 200))
	assertAnswer(t, output(m), "\r\nOK\r\n")
	assertAnswer(t, runCommand(m, "ATO", at(start, 300)), "\r\nCONNECT 19200\r\n")

	m.receive([]byte("+++"), at(start, 350))
	m.advance(at(start, 700))

	assertPlusesWereData(t, m)
}

// assertPlusesWereData fails the test unless the three pluses the app sent
// were data: the modem answered nothing and passed them to the network.
func assertPlusesWereData(t *testing.T, m *modem) {
	t.Helper()
	assertAnswer(t, output(m), "")
	if m.stats.DataBytesFromApp != 3 {
		t.Errorf("%d bytes went to the network; want the 3 pluses", m.stats.DataBytesFromApp)
	}
}

// The modem moves the simulation to the time of the bytes the app writes
// before it takes them, so that an escape whose closing guard has passed
// ends online-data mode first, even when the app has read nothing since.
// Here the ATH that follows the guard hangs up.
func TestAnATHWrittenOnceTheEscapesGuardHasPassedHangsUp(t *testing.T) {
	t.Parallel()
	start := time.Now()
	m := connectedModem(t, start)
	m.receive([]byte("+++"), at(start, 100))

	m.receive([]byte("ATH\r"), at(start, 250))

	// The escape's OK, then the hang-up's.
	assertAnswer(t, output(m), "\r\nOK\r\n\r\nOK\r\n")
	if m.stats.InDataCall {
		t.Error("the call is still up: the ATH went to the network as data")
	}
}

// The modem's events reach the log with their texts and levels, which is what
// `satlink --simulate -v` shows of the simulator. The test records the
// process-wide logger, so it does not run in parallel.
func TestTheModemLogsItsEvents(t *testing.T) {
	cases := []struct {
		name  string
		event func(t *testing.T, now time.Time)
		want  logRecord
	}{
		{"a data call", dialADataCall, infoRecord("simulated handset: dialling 008816000025 for a data call")},
		{"a voice call", placeAVoiceCall, infoRecord("simulated handset: voice call to +12025550100, without audio")},
		{"an SMS", sendAnSMS, infoRecord("simulated handset: SMS to +1")},
		{"the app's hang-up", hangUpInOnlineCommandMode, infoRecord("simulated handset: the app hung up")},
		{"a network drop", dropTheCallFromTheNetwork, infoRecord("simulated handset: the network dropped the call")},
		{"a DTR drop", dropDTRAfterAndD2, infoRecord("simulated handset: DTR dropped, ending the call")},
		{"a gateway that cannot start", dialAGatewayThatCannotStart, logRecord{
			level:   slog.LevelWarn,
			message: "simulated handset: the PPP gateway cannot start: MRU 20 is below the IPv4 minimum of 68",
		}},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			recorder := recordLogs(t)

			test.event(t, time.Now())

			if got := recorder.recorded(); !slices.Contains(got, test.want) {
				t.Errorf("records %v; want one of them %v", got, test.want)
			}
		})
	}
}

func infoRecord(message string) logRecord {
	return logRecord{level: slog.LevelInfo, message: message}
}

func dialADataCall(_ *testing.T, now time.Time) {
	runCommand(quietModem(modemConfig(), now), "ATD"+iridiumInternet, now)
}

func placeAVoiceCall(_ *testing.T, now time.Time) {
	runCommand(quietModem(modemConfig(), now), "ATD+12025550100;", now)
}

func sendAnSMS(_ *testing.T, now time.Time) {
	quietModem(modemConfig(), now).receive([]byte("AT+CMGS=11\r"+hiSubmitPDU+"\x1a"), now)
}

func hangUpInOnlineCommandMode(t *testing.T, start time.Time) {
	m := connectedModem(t, start)
	m.receive([]byte("+++"), at(start, 100))
	m.advance(at(start, 200))
	runCommand(m, "ATH", at(start, 300))
}

func dropTheCallFromTheNetwork(t *testing.T, now time.Time) {
	connectedModem(t, now).dropCall()
}

func dropDTRAfterAndD2(t *testing.T, now time.Time) {
	m := quietModem(modemConfig(), now)
	runCommand(m, "AT&D2", now)
	connectDataCall(t, m, now)
	m.dropDTR()
}

// unusableMRU is below ppp.MinMRU, so the gateway's PPP cannot start.
const unusableMRU = 20

func dialAGatewayThatCannotStart(_ *testing.T, now time.Time) {
	config := modemConfig()
	config.Network.GatewayMRU = unusableMRU
	m := quietModem(config, now)
	runCommand(m, "ATD"+iridiumInternet, now)
	m.advance(now)
}
