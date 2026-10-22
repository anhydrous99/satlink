package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/satlink"
	"github.com/anhydrous99/satlink/internal/logtext"
	"github.com/anhydrous99/satlink/internal/simtest"
	"github.com/anhydrous99/satlink/serial"
	"github.com/anhydrous99/satlink/sim"
)

// Each command run in-process, against a simulated handset, and compared
// with its golden file in testdata/commands.

// instant is the option that asks for a simulated handset without delays.
const instant = "--simulate=instant"

// goldenCase is a command line whose output must match the golden file name.
type goldenCase struct {
	name  string
	args  []string
	world world
}

func TestCommandsPrintWhatTheirGoldenFilesHold(t *testing.T) {
	for _, test := range append(handsetCases(), netCases()...) {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := runInProcess(t, test.world, test.args...)

			checkGolden(t, test.name, test.args, got)
		})
	}
}

// handsetCases are the commands that talk to the handset itself.
func handsetCases() []goldenCase {
	twoMessages := world{handset: inboxHolding("+881631234567", "+881639876543")}
	twoPorts := world{ports: []serial.Port{
		{Path: "/dev/ttyACM0", ProductID: 0x9575, Model: "Iridium 9575", SerialNumber: "300234010000000"},
		{Path: "/dev/ttyACM1", ProductID: 0x1234},
	}}
	return []goldenCase{
		{name: "commands/info", args: []string{instant, "info"}},
		{name: "commands/info-iridium-timing", args: []string{"--simulate", "info"}},
		{name: "commands/info-unplugged", args: []string{instant, "info"}, world: world{handset: unplugged}},
		{name: "commands/status", args: []string{instant, "status"}},
		{name: "commands/status-with-event", args: []string{instant, "status"},
			world: world{handset: smsArrivingOn("AT+CSQ")}},
		{name: "commands/sms-send", args: []string{instant, "sms", "send", "+881631234567", "hello from the field"}},
		{name: "commands/sms-list", args: []string{instant, "sms", "list"}, world: twoMessages},
		{name: "commands/sms-list-read", args: []string{instant, "sms", "list", "read"}, world: twoMessages},
		{name: "commands/sms-read", args: []string{instant, "sms", "read", "2"}, world: twoMessages},
		{name: "commands/sms-read-missing", args: []string{instant, "sms", "read", "9"}},
		{name: "commands/sms-delete", args: []string{instant, "sms", "delete", "1"}, world: twoMessages},
		{name: "commands/dial", args: []string{instant, "dial", "+881631234567"}},
		{name: "commands/dial-a-dash", args: []string{instant, "dial", "--", "-5"}},
		{name: "commands/answer-without-a-call", args: []string{instant, "answer"}},
		{name: "commands/hangup", args: []string{instant, "hangup"}},
		{name: "commands/at", args: []string{instant, "at", "AT+CSQ"}},
		{name: "commands/at-with-timeout", args: []string{instant, "at", "--timeout", "3", "AT+CGMI"}},
		{name: "commands/at-with-attached-timeout", args: []string{instant, "at", "-t3", "AT+CGMI"}},
		{name: "commands/at-refused", args: []string{instant, "at", "AT+BOGUS"}},
		{name: "commands/list", args: []string{"list"}, world: twoPorts},
		// list looks on USB whatever --simulate says.
		{name: "commands/list-ignores-simulate", args: []string{instant, "list"}, world: twoPorts},
		{name: "commands/list-none", args: []string{"list"}},
		{name: "commands/list-failed", args: []string{"list"}, world: world{discoveryFails: true}},
		// Without --port, the only handset discovered is opened.
		{name: "commands/info-without-a-handset", args: []string{"info"}},
		{name: "commands/info-with-several-handsets", args: []string{"info"}, world: twoPorts},
		{name: "commands/info-when-discovery-fails", args: []string{"info"}, world: world{discoveryFails: true}},
		// Options may follow the subcommand, as clap's global ones could.
		{name: "commands/options-after-the-command", args: []string{"info", instant, "-b", "9600"}},
	}
}

// netCases are the net commands, each a data call unless it needs none.
func netCases() []goldenCase {
	return []goldenCase{
		{name: "commands/net-status", args: []string{instant, "net", "status"}},
		{name: "commands/net-data-number", args: []string{instant, "net", "status"},
			world: world{variables: map[string]string{dataNumberVariable: "12345"}}},
		// As clap's: a variable set to "" is set.
		{name: "commands/net-empty-data-number", args: []string{instant, "net", "status"},
			world: world{variables: map[string]string{dataNumberVariable: ""}}},
		{name: "commands/net-resolve", args: []string{instant, "net", "resolve", "echo.test"}},
		{name: "commands/net-resolve-address", args: []string{instant, "net", "resolve", "192.0.2.7"}},
		{name: "commands/net-resolve-unknown", args: []string{instant, "net", "resolve", "nowhere.test"}},
		{name: "commands/net-get", args: []string{instant, "net", "get", "http://web.test/"}},
		// The public roots do not hold the simulator's test CA. Like the
		// simulator's own TLS tests, it needs a clock past the start of the
		// test certificates (sim/testdata/README.md).
		{name: "commands/net-get-https-untrusted", args: []string{instant, "net", "get", "https://secure.test/"}},
		{name: "commands/net-tcp", args: []string{instant, "net", "tcp", "echo.test", "7"},
			world: world{stdin: strings.NewReader("hello over the satellite\n")}},
		{name: "commands/net-tcp-input-fails", args: []string{instant, "net", "tcp", "echo.test", "7"},
			world: world{stdin: brokenReader{}}},
		{name: "commands/net-target-refused", args: []string{instant, "net", "get", "http://*.web.test/"}},
		// Limits too small to be of use are refused before "Dialling".
		{name: "commands/net-idle-limit-too-short", args: []string{instant, "net", "--idle-secs", "24", "status"}},
		{name: "commands/net-data-limit-too-small", args: []string{instant, "net", "--max-kb", "1", "status"}},
		{name: "commands/net-rule-refused", args: []string{instant, "net", "--allow", "no-port", "status"}},
		{name: "commands/net-hang-up-unconfirmed", args: []string{instant, "net", "status"},
			world: world{handset: panickingOnEscape}},
		{name: "commands/net-setup-hang-up-unconfirmed", args: []string{instant, "net", "status"},
			world: world{handset: refusingTheCredentials}},
	}
}

func TestMonitorPrintsEventsUntilInterrupted(t *testing.T) {
	args := []string{instant, "monitor"}
	// The last of the handset's initialisation, so that the SMS arrives
	// before the monitoring starts.
	env, _, stderr := world{handset: smsArrivingOn("AT+CMGF=1")}.environment(t, args)
	// Long enough to see the event, and the end of a test that hangs.
	ctx, giveUp := context.WithTimeout(context.Background(), 10*time.Second)
	defer giveUp()
	interrupting, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &interruptingBuffer{interrupt: cancel}
	env.stdout = stdout

	status := run(interrupting, env)

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("the monitor printed nothing within 10 s")
	}
	checkGolden(t, "commands/monitor", args, outcome{status: status, stdout: stdout.String(), stderr: stderr.String()})
}

func TestTheVerboseFlagsChooseTheLogLevel(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		least slog.Level
	}{
		{"warnings by default", []string{instant, "hangup"}, slog.LevelWarn},
		{"info with -v", []string{"-v", instant, "hangup"}, slog.LevelInfo},
		{"debug with -vv", []string{"-vv", instant, "hangup"}, slog.LevelDebug},
		{"trace with three", []string{"-v", "--verbose", "-v", instant, "hangup"}, logtext.LevelTrace},
		// As clap's global options: the deepest command given them wins.
		{"the subcommand's count", []string{"-vvv", instant, "hangup", "-v"}, slog.LevelInfo},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var installed slog.Handler
			env, _, _ := world{}.environment(t, test.args)
			env.installLogger = func(handler slog.Handler) { installed = handler }

			if status := run(context.Background(), env); status != exitSuccess {
				t.Fatalf("exit status %d", status)
			}

			ctx := context.Background()
			if installed == nil || !installed.Enabled(ctx, test.least) || installed.Enabled(ctx, test.least-1) {
				t.Errorf("the logger installed does not show exactly from %v up", test.least)
			}
		})
	}
}

// readOptions reads args, in an environment where only the variables given
// are set, and returns the options every command shares.
func readOptions(t *testing.T, args []string, variables map[string]string) globalOptions {
	t.Helper()
	env, _, _ := world{variables: variables}.environment(t, args)
	c := newCLI(env, io.Discard)
	if _, exit := parseCommandLine(c.rootCommand(), args, surroundings{lookupEnv: env.lookupEnv}); exit != nil {
		t.Fatalf("%q: %s", args, exit.text)
	}
	return c.options
}

func TestThePortComesFromTheEnvironmentUnlessGiven(t *testing.T) {
	inEnvironment := map[string]string{portVariable: "/dev/ttyACM7"}
	tests := []struct {
		name      string
		args      []string
		variables map[string]string
		port      givenText
	}{
		{"from the environment", []string{"info"}, inEnvironment, givenText{text: "/dev/ttyACM7", given: true}},
		{"given", []string{"--port", "/dev/ttyACM1", "info"}, inEnvironment, givenText{text: "/dev/ttyACM1", given: true}},
		{"given short", []string{"info", "-p/dev/ttyACM2"}, inEnvironment, givenText{text: "/dev/ttyACM2", given: true}},
		{"empty in the environment", []string{"info"}, map[string]string{portVariable: ""}, givenText{given: true}},
		{"nowhere", []string{"info"}, nil, givenText{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := readOptions(t, test.args, test.variables).port; got != test.port {
				t.Errorf("port %+v, want %+v", got, test.port)
			}
		})
	}
}

func TestTheDeepestCommandGivenAGlobalOptionWins(t *testing.T) {
	options := readOptions(t, []string{"-b", "1200", "--simulate=iridium", "sms", "-b", "2400", "list",
		"-b", "9600", instant}, nil)

	if options.baud != 9600 || options.simulate.chosen == nil || options.simulate.chosen.name != "instant" {
		t.Errorf("baud %d and simulation %+v, want 9600 and instant", options.baud, options.simulate.chosen)
	}
}

func TestTheBaudRateDefaultsToTheNominalOne(t *testing.T) {
	if baud := readOptions(t, []string{"info"}, nil).baud; baud != serial.DefaultBaud {
		t.Errorf("baud %d, want %d", baud, serial.DefaultBaud)
	}
}

func TestAPortThatCannotBeOpenedFailsTheCommand(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "ttyACM9")
	tests := []struct {
		name  string
		args  []string
		world world
		// opened is the path the command opens.
		opened string
	}{
		{"given with --port", []string{"--port", missing, "info"}, world{}, missing},
		{"named by SATLINK_PORT", []string{"info"}, world{variables: map[string]string{portVariable: missing}}, missing},
		// An empty port is a port, which no handset is
		// found by.
		{"empty", []string{"--port=", "info"}, world{}, ""},
		{"the only handset discovered", []string{"info"}, world{ports: []serial.Port{{Path: missing}}}, missing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// The serial library's error, not discovery's: the port was
			// opened, not looked for. On macOS and Linux it reads
			// "transport error: No such file or directory".
			_, openErr := serial.Open(test.opened, serial.DefaultBaud)
			want := fmt.Sprintf("error: %v\n", openErr)

			got := runInProcess(t, test.world, test.args...)

			if got.status != exitFailure || got.stderr != want || got.stdout != "" {
				t.Errorf("exit status %d, output %q and %q; want the port's failure, %q",
					got.status, got.stdout, got.stderr, want)
			}
		})
	}
}

func TestADialThatCannotBeAnnouncedCostsNoCall(t *testing.T) {
	var control *sim.Control
	keepingControl := world{handset: func(t *testing.T, config sim.Config) satlink.Transport {
		simulator := simtest.NewSimulator(t, config)
		control = simulator.Control()
		return simulator
	}}
	env, _, _ := keepingControl.environment(t, []string{instant, "net", "status"})
	env.stderr = brokenWriter{}

	status := run(context.Background(), env)

	if status != exitFailure || control == nil || control.Stats().CallsPlaced != 0 {
		t.Errorf("exit status %d with the handset %v; want a failure and no call placed", status, control)
	}
}

func TestAHelpPageThatCannotBeWrittenStillExitsAsAsked(t *testing.T) {
	env, _, _ := world{}.environment(t, []string{"--help"})
	env.stdout = brokenWriter{}

	if status := run(context.Background(), env); status != exitSuccess {
		t.Errorf("exit status %d, want %d", status, exitSuccess)
	}
}

// A handset command whose output cannot be written — a pipe to a head that has
// had enough — fails and says why, as the net commands do.
func TestAHandsetCommandWhoseOutputCannotBeWrittenFails(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		world world
	}{
		{"list", []string{"list"}, world{}},
		{"info", []string{instant, "info"}, world{}},
		{"status", []string{instant, "status"}, world{}},
		{"sms send", []string{instant, "sms", "send", "+881631234567", "hello"}, world{}},
		{"sms list", []string{instant, "sms", "list"}, world{handset: inboxHolding("+881631234567")}},
		{"sms read", []string{instant, "sms", "read", "1"}, world{handset: inboxHolding("+881631234567")}},
		{"dial", []string{instant, "dial", "+881631234567"}, world{}},
		{"at", []string{instant, "at", "AT+CSQ"}, world{}},
		// The last of the handset's initialisation, so that the SMS arrives
		// before the monitoring starts.
		{"monitor", []string{instant, "monitor"}, world{handset: smsArrivingOn("AT+CMGF=1")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			env, _, stderr := test.world.environment(t, test.args)
			env.stdout = brokenWriter{}
			// A monitor whose output never failed would run until this ended
			// it, and then succeed.
			ctx, giveUp := context.WithTimeout(context.Background(), time.Minute)
			defer giveUp()

			status := run(ctx, env)

			if want := "error: broken pipe\n"; status != exitFailure || stderr.String() != want {
				t.Errorf("exit status %d after %q, want %d after %q", status, stderr.String(), exitFailure, want)
			}
		})
	}
}

// The "(event)" lines are not best effort either: an event that standard error
// cannot take fails the command.
func TestAnEventThatCannotBeReportedFailsTheCommand(t *testing.T) {
	env, stdout, _ := world{handset: smsArrivingOn("AT+CSQ")}.environment(t, []string{instant, "status"})
	env.stderr = brokenWriter{}

	status := run(context.Background(), env)

	if status != exitFailure || !strings.HasPrefix(stdout.String(), "Signal:") {
		t.Errorf("exit status %d after %q, want a failure after the status", status, stdout.String())
	}
}
